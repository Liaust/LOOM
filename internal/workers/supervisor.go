package workers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"loom.local/loom/internal/requestctx"
)

const defaultSupervisorPollInterval = 60 * time.Second

type Supervisor struct {
	Service      Service
	PollInterval time.Duration
	Logger       *slog.Logger
	Wakes        *QueueWakes
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
	// Each bounded lane remains serial. Worker/resource leases still arbitrate
	// execution; long maintenance and AI calls cannot hold the control loop.
	runSupervisorLanes(ctx, func(ctx context.Context, lane string) { s.runLoop(ctx, req, lane) })
}

func (s Supervisor) runLoop(ctx context.Context, req requestctx.Context, lane string) {
	interval := s.PollInterval
	if interval <= 0 {
		interval = defaultSupervisorPollInterval
	}
	probes := map[string]time.Time{}
	kinds := map[string]string{}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for ctx.Err() == nil {
		now := time.Now().UTC()
		if _, err := s.runDueWithReadinessKinds(ctx, req, now, lane, probes, interval, kinds); err != nil && s.Logger != nil {
			s.Logger.Warn("worker supervisor check failed", "component", "workers", "error", err.Error())
		}
		next, err := s.Service.nextSupervisorDeadline(ctx, lane, time.Now().UTC(), interval, probes)
		if err != nil {
			next = time.Now().Add(interval)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(max(0, time.Until(next)))
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.Wakes.channel(lane):
			hints := s.Wakes.take(lane)
			invalidateWorkerProbes(probes, kinds, hints)
			if err := s.Service.refreshQueueWakeDeadlines(ctx, hints, time.Now().UTC(), lane); err != nil && s.Logger != nil {
				s.Logger.Warn("queue wake recheck failed; deadline fallback active", "component", "workers")
			}
			if (lane == "control" || lane == "") && (hints[KindAutomationScheduler] || hints["*"]) {
				if err := s.Service.refreshSchedulerWakeDeadline(ctx, time.Now().UTC()); err != nil && s.Logger != nil {
					s.Logger.Warn("scheduler wake recheck failed; deadline fallback active", "component", "workers", "error", err.Error())
				}
			}
		}
	}
}

func (s Supervisor) RunDueOnce(ctx context.Context, req requestctx.Context, now time.Time) (SupervisorResult, error) {
	return s.runDueOnce(ctx, req, now, "")
}

func (s Supervisor) runDueOnce(ctx context.Context, req requestctx.Context, now time.Time, lane string) (SupervisorResult, error) {
	return s.runDueWithReadiness(ctx, req, now, lane, map[string]time.Time{}, defaultSupervisorPollInterval)
}

func (s Supervisor) runDueWithReadiness(ctx context.Context, req requestctx.Context, now time.Time, lane string, probes map[string]time.Time, safety time.Duration) (SupervisorResult, error) {
	return s.runDueWithReadinessKinds(ctx, req, now, lane, probes, safety, nil)
}

func invalidateWorkerProbes(probes map[string]time.Time, kinds map[string]string, hints map[string]bool) {
	for id := range probes {
		if hints["*"] || hints[kinds[id]] {
			delete(probes, id)
		}
	}
}

func (s Supervisor) runDueWithReadinessKinds(ctx context.Context, req requestctx.Context, now time.Time, lane string, probes map[string]time.Time, safety time.Duration, kinds map[string]string) (SupervisorResult, error) {
	result := SupervisorResult{}
	if lane == "control" || lane == "" {
		repair, err := s.Service.RepairStaleRuns(ctx, req, now)
		if err != nil {
			return SupervisorResult{}, err
		}
		result.RepairedStaleRuns = repair.RepairedInstances
	}

	excluded := make([]string, 0, len(probes))
	for id, until := range probes {
		if now.Before(until) {
			excluded = append(excluded, id)
		}
	}
	workers, err := s.Service.listDueWorkersExcluding(ctx, now, 50, lane, excluded)
	if err != nil {
		return SupervisorResult{}, err
	}

	result.Checked = len(workers)
	for _, worker := range workers {
		if kinds != nil {
			kinds[worker.WorkerInstanceID] = worker.WorkerKind
		}
		if until, ok := probes[worker.WorkerInstanceID]; ok && now.Before(until) {
			continue
		}
		policy, err := ParseTickPolicy(worker.TickPolicyJSON)
		if err != nil {
			probes[worker.WorkerInstanceID] = now.Add(safety)
			result.Skipped++
			result.Errors = append(result.Errors, err.Error())
			s.logWorkerError("worker supervisor skipped invalid tick policy", worker, err)
			continue
		}
		if !policy.Due(now, worker.NextRunAfter) {
			result.Skipped++
			continue
		}
		if runtime, ok := s.Service.Registry.Get(worker.WorkerKind); ok && policy.Mode == TickModeInterval {
			if readiness, ok := runtime.(AutomaticReadiness); ok {
				ready, next, err := readiness.AutomaticReady(ctx, worker, now)
				if err != nil || !ready {
					until := now.Add(safety)
					if err == nil && next != nil && next.After(now) && next.Before(until) {
						until = *next
					}
					probes[worker.WorkerInstanceID] = until
					if err != nil {
						s.logWorkerError("worker readiness unavailable", worker, err)
					}
					result.Skipped++
					continue
				}
			}
		}
		delete(probes, worker.WorkerInstanceID)
		result.Due++
		fingerprint, err := TickPolicyFingerprint(worker.TickPolicyJSON)
		if err != nil {
			probes[worker.WorkerInstanceID] = now.Add(safety)
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
			probes[worker.WorkerInstanceID] = time.Now().Add(safety)
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
	if (worker.WorkerKind == KindAutomationScheduler || queueWakeKind(worker.WorkerKind)) && policy.Mode == TickModeInterval && worker.NextRunAfter != nil {
		return fmt.Sprintf("worker_tick_%s_scheduler_%s", worker.WorkerInstanceID, worker.NextRunAfter.UTC().Format(time.RFC3339Nano))
	}
	interval := policy.IntervalSeconds
	if interval <= 0 {
		interval = 60
	}
	bucket := now.UTC().Unix() / int64(interval)
	return fmt.Sprintf("worker_tick_%s_%d", worker.WorkerInstanceID, bucket)
}

func queueWakeKind(kind string) bool {
	switch kind {
	case KindJobRunner, KindApplicationBuilder, KindKnowledgeIndexer, KindKnowledgeHeavy, KindAutomationDispatcher:
		return true
	}
	return false
}

// Recompute an idle deadline from durable queue readiness, never from the hint
// alone. Locking the instance also serializes this with policy changes/claims.
func (s Service) refreshQueueWakeDeadlines(ctx context.Context, hints map[string]bool, now time.Time, lane string) error {
	if s.Registry == nil {
		return nil
	}
	for _, kind := range []string{KindJobRunner, KindApplicationBuilder, KindKnowledgeIndexer, KindKnowledgeHeavy, KindAutomationDispatcher} {
		if lane != "" && workerLane(kind) != lane {
			continue
		}
		if !hints["*"] && !hints[kind] {
			continue
		}
		runtime, ok := s.Registry.Get(kind)
		if !ok {
			continue
		}
		readyRuntime, ok := runtime.(AutomaticReadiness)
		if !ok {
			continue
		}
		if err := s.refreshQueueKindDeadline(ctx, kind, readyRuntime, now); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) refreshQueueKindDeadline(ctx context.Context, kind string, runtime AutomaticReadiness, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, workerInstanceSelectSQL()+`
		WHERE worker_kind=$1 AND locality=$2 AND enabled AND NOT paused
		AND lifecycle_status IN ('registered','active','degraded','failed')
		AND current_run_id IS NULL AND tick_policy_json->>'mode'='interval'
		ORDER BY worker_key LIMIT 50 FOR UPDATE SKIP LOCKED`, kind, LocalityMainOwned)
	if err != nil {
		return err
	}
	var instances []WorkerInstance
	for rows.Next() {
		instance, err := scanWorkerInstance(rows)
		if err != nil {
			rows.Close()
			return err
		}
		instances = append(instances, instance)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if !queueWakeEligible(instance, now) {
			continue
		}
		ready, next, err := runtime.AutomaticReady(ctx, instance, now)
		if err != nil {
			return err
		}
		if ready {
			next = &now
		}
		if next == nil {
			continue
		}
		deadline := next.UTC()
		if instance.BackoffUntil != nil && instance.BackoffUntil.After(deadline) {
			deadline = *instance.BackoffUntil
		}
		if instance.NextRunAfter != nil && !deadline.Before(*instance.NextRunAfter) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances SET next_run_after=$2,updated_at=now() WHERE worker_instance_id=$1`, instance.WorkerInstanceID, deadline); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func queueWakeEligible(instance WorkerInstance, now time.Time) bool {
	policy, err := ParseTickPolicy(instance.TickPolicyJSON)
	if err != nil || policy.Mode != TickModeInterval || !queueWakeKind(instance.WorkerKind) ||
		instance.Locality != LocalityMainOwned || !instance.Enabled || instance.Paused || instance.CurrentRunID != nil {
		return false
	}
	switch instance.LifecycleStatus {
	case LifecycleRegistered, LifecycleActive, LifecycleDegraded, LifecycleFailed:
	default:
		return false
	}
	return !(instance.ConsecutiveFails > 0 && instance.NextRunAfter != nil && instance.NextRunAfter.After(now))
}

// A schedule-owner hint requests discovery, not execution of a schedule. Move
// only the scheduler's worker deadline; RunOnce still locks and checks policy.
func (s Service) refreshSchedulerWakeDeadline(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := refreshSchedulerWakeDeadlineTx(ctx, tx, s.Registry, now); err != nil {
		return err
	}
	return tx.Commit()
}

// This optional hook is deliberately used only for automation_scheduler.
type schedulerWakeRuntime interface {
	NextSchedulerWakeDeadline(context.Context, *sql.Tx, WorkerInstance, time.Time) (*time.Time, error)
}

func refreshSchedulerWakeDeadlineTx(ctx context.Context, tx *sql.Tx, registry *Registry, now time.Time) error {
	if registry == nil {
		return nil
	}
	runtime, ok := registry.Get(KindAutomationScheduler)
	if !ok {
		return nil
	}
	deadlineRuntime, ok := runtime.(schedulerWakeRuntime)
	if !ok {
		return nil
	}
	rows, err := tx.QueryContext(ctx, workerInstanceSelectSQL()+`
		WHERE worker_kind = $1 AND locality = $2 AND enabled AND NOT paused
		  AND lifecycle_status IN ('registered','active','degraded','failed')
		  AND current_run_id IS NULL AND tick_policy_json->>'mode' = $3
		ORDER BY worker_key LIMIT 50 FOR UPDATE SKIP LOCKED
	`, KindAutomationScheduler, LocalityMainOwned, TickModeInterval)
	if err != nil {
		return err
	}
	var instances []WorkerInstance
	for rows.Next() {
		instance, err := scanWorkerInstance(rows)
		if err != nil {
			rows.Close()
			return err
		}
		instances = append(instances, instance)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.ConsecutiveFails > 0 && instance.NextRunAfter != nil && instance.NextRunAfter.After(now) {
			continue
		}
		proposed, err := deadlineRuntime.NextSchedulerWakeDeadline(ctx, tx, instance, now)
		if err != nil {
			return err
		}
		if next := schedulerWakeDeadline(instance, now, proposed); next != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances
				SET next_run_after = $2, updated_at = now() WHERE worker_instance_id = $1
			`, instance.WorkerInstanceID, *next); err != nil {
				return err
			}
		}
	}
	return nil
}

func schedulerWakeDeadline(instance WorkerInstance, now time.Time, proposed *time.Time) *time.Time {
	policy, err := ParseTickPolicy(instance.TickPolicyJSON)
	if err != nil || policy.Mode != TickModeInterval || instance.WorkerKind != KindAutomationScheduler ||
		instance.Locality != LocalityMainOwned || !instance.Enabled || instance.Paused || instance.CurrentRunID != nil {
		return nil
	}
	switch instance.LifecycleStatus {
	case LifecycleRegistered, LifecycleActive, LifecycleDegraded, LifecycleFailed:
	default:
		return nil
	}
	if instance.ConsecutiveFails > 0 && instance.NextRunAfter != nil && instance.NextRunAfter.After(now) {
		return nil
	}
	if proposed == nil {
		return nil
	}
	next := proposed.UTC()
	if instance.BackoffUntil != nil && instance.BackoffUntil.After(next) {
		next = *instance.BackoffUntil
	}
	if instance.NextRunAfter != nil && !next.Before(*instance.NextRunAfter) {
		return nil
	}
	return &next
}
