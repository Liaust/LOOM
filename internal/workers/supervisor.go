package workers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"loom.local/loom/internal/requestctx"
)

const defaultSupervisorPollInterval = 2 * time.Second

type Supervisor struct {
	Service      Service
	PollInterval time.Duration
	Logger       *slog.Logger
}

type SupervisorResult struct {
	Checked           int      `json:"checked"`
	Due               int      `json:"due"`
	Started           int      `json:"started"`
	Skipped           int      `json:"skipped"`
	Failed            int      `json:"failed"`
	RepairedStaleRuns int      `json:"repaired_stale_runs"`
	Errors            []string `json:"errors,omitempty"`
}

func NewSupervisor(service Service, logger *slog.Logger) Supervisor {
	return Supervisor{
		Service:      service,
		PollInterval: defaultSupervisorPollInterval,
		Logger:       logger,
	}
}

func (s Supervisor) Run(ctx context.Context, req requestctx.Context) {
	_, _ = s.RunDueOnce(ctx, req, time.Now().UTC())

	interval := s.PollInterval
	if interval <= 0 {
		interval = defaultSupervisorPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := s.RunDueOnce(ctx, req, now.UTC()); err != nil && s.Logger != nil {
				s.Logger.Warn("worker supervisor tick failed",
					slog.String("component", "workers"),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

func (s Supervisor) RunDueOnce(ctx context.Context, req requestctx.Context, now time.Time) (SupervisorResult, error) {
	result := SupervisorResult{}
	repair, err := s.Service.RepairStaleRuns(ctx, req, now)
	if err != nil {
		return SupervisorResult{}, err
	}
	result.RepairedStaleRuns = repair.RepairedInstances

	workers, err := s.Service.ListDueWorkers(ctx, now, 50)
	if err != nil {
		return SupervisorResult{}, err
	}

	result.Checked = len(workers)
	for _, worker := range workers {
		policy, err := ParseTickPolicy(worker.TickPolicyJSON)
		if err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, err.Error())
			s.logWorkerError("worker supervisor skipped invalid tick policy", worker, err)
			continue
		}
		if !policy.Due(now, worker.NextRunAfter) {
			result.Skipped++
			continue
		}
		result.Due++
		fingerprint, err := TickPolicyFingerprint(worker.TickPolicyJSON)
		if err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, err.Error())
			s.logWorkerError("worker supervisor skipped policy without stable fingerprint", worker, err)
			continue
		}

		runInput := RunOnceInput{
			Reason:                    "supervisor tick",
			TriggerKind:               TriggerSupervisorTick,
			TriggerRef:                worker.WorkerKey,
			IdempotencyKey:            supervisorTickIdempotencyKey(worker, policy, now),
			ExpectedPolicyFingerprint: fingerprint,
			ScheduleEvidenceAt:        now.UTC(),
		}
		if _, err := s.Service.RunOnce(ctx, req, worker.WorkerInstanceID, runInput); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, err.Error())
			s.logWorkerError("worker supervisor run failed", worker, err)
			if _, advanceErr := s.Service.advanceDailyScheduleAfterFailedAttempt(ctx, worker, policy, now); advanceErr != nil {
				result.Errors = append(result.Errors, advanceErr.Error())
				s.logWorkerError("worker supervisor could not advance failed daily occurrence", worker, advanceErr)
			}
			continue
		}
		result.Started++
	}
	return result, nil
}

func (s Supervisor) logWorkerError(message string, worker WorkerInstance, err error) {
	if s.Logger == nil {
		return
	}
	s.Logger.Warn(message,
		slog.String("component", "workers"),
		slog.String("worker_key", worker.WorkerKey),
		slog.String("worker_kind", worker.WorkerKind),
		slog.String("error", err.Error()),
	)
}

func supervisorTickIdempotencyKey(worker WorkerInstance, policy TickPolicy, now time.Time) string {
	if policy.Mode == TickModeDailyLocal && worker.NextRunAfter != nil {
		return fmt.Sprintf("worker_tick_%s_daily_%d", worker.WorkerInstanceID, worker.NextRunAfter.UTC().Unix())
	}
	interval := policy.IntervalSeconds
	if interval <= 0 {
		interval = 60
	}
	bucket := now.UTC().Unix() / int64(interval)
	return fmt.Sprintf("worker_tick_%s_%d", worker.WorkerInstanceID, bucket)
}
