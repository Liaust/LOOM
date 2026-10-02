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
	// Jobs may run for hours. Their existing single-runner loop must not block
	// the scheduler/dispatcher that enqueues them or unrelated maintenance ticks.
	jobsDone := make(chan struct{})
	go func() {
		defer close(jobsDone)
		s.runLoop(ctx, req, "jobs")
	}()
	// The Notes transport keeps a bounded live connection; it must not hold
	// the serial background lane while waiting for the next device edit.
	notesDone := make(chan struct{})
	go func() {
		defer close(notesDone)
		s.runLoop(ctx, req, "notes")
	}()
	s.runLoop(ctx, req, "background")
	<-jobsDone
	<-notesDone
}

func (s Supervisor) runLoop(ctx context.Context, req requestctx.Context, lane string) {
	_, _ = s.runDueOnce(ctx, req, time.Now().UTC(), lane)

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
			if _, err := s.runDueOnce(ctx, req, now.UTC(), lane); err != nil && s.Logger != nil {
				s.Logger.Warn("worker supervisor tick failed",
					slog.String("component", "workers"),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

func (s Supervisor) RunDueOnce(ctx context.Context, req requestctx.Context, now time.Time) (SupervisorResult, error) {
	return s.runDueOnce(ctx, req, now, "")
}

func (s Supervisor) runDueOnce(ctx context.Context, req requestctx.Context, now time.Time, lane string) (SupervisorResult, error) {
	result := SupervisorResult{}
	if lane == "background" || lane == "" {
		repair, err := s.Service.RepairStaleRuns(ctx, req, now)
		if err != nil {
			return SupervisorResult{}, err
		}
		result.RepairedStaleRuns = repair.RepairedInstances
	}

	workers, err := s.Service.listDueWorkers(ctx, now, 50, lane)
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
