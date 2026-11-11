package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ab "loom.local/loom/internal/applicationbuild"
	"loom.local/loom/internal/jobs"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
)

// ApplicationBuildCoordinator bridges existing declaration operations and jobs.
// Job input references frozen custody; it never accepts a client command or path.
type ApplicationBuildCoordinator struct {
	Jobs      jobs.Service
	Source    *SourceApplicationAdapter
	Parent    *Service
	Snapshots string
}

type applicationBuildInput struct {
	Operation  string `json:"operation"`
	Action     string `json:"action"`
	InputHash  string `json:"input_hash"`
	FrozenRoot string `json:"frozen_root"`
}

func applicationBuildKey(call ActionCall) string {
	return hashBytes([]byte(call.OperationID + "\x00" + call.Action.ID + "\x00" + call.Action.InputHash))
}

func (c *ApplicationBuildCoordinator) Observe(ctx context.Context, call ActionCall) (Observation, error) {
	job, err := c.Jobs.ApplicationBuild(ctx, applicationBuildKey(call))
	if errors.Is(err, sql.ErrNoRows) {
		return Observation{State: Absent}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	if job.ManualAction {
		return Observation{State: Uncertain, CauseCode: "application_build_attention_required"}, nil
	}
	if job.Status == jobs.StatusFailed || job.Status == jobs.StatusCancelled || job.Status == jobs.StatusCompleted {
		return Observation{State: Absent}, nil
	}
	return Observation{State: Pending, CauseCode: "application_build_pending"}, nil
}

func (c *ApplicationBuildCoordinator) Ensure(ctx context.Context, call ActionCall, p applicationSourcePayload) (bool, error) {
	key := applicationBuildKey(call)
	job, err := c.Jobs.ApplicationBuild(ctx, key)
	if err == nil {
		if job.ManualAction {
			return false, fail(pc.DeclarationOwnerFailed, "application_build_attention_required")
		}
		switch job.Status {
		case jobs.StatusCompleted:
			return true, nil
		case jobs.StatusFailed, jobs.StatusCancelled:
			if job.FailureCode != nil && *job.FailureCode == "application_build_prerequisite_failed" {
				return false, fail(pc.DeclarationTargetUnavailable, "application_build_prerequisite_failed")
			}
			return false, fail(pc.DeclarationOwnerFailed, "application_build_failed")
		default:
			return false, nil
		}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	freezer, ok := c.Source.Builder.(interface {
		Freeze(context.Context, ab.Input, string, ab.Snapshot) error
	})
	if !ok {
		return false, fail(pc.DeclarationTargetUnavailable, "application_snapshot_custody_unavailable")
	}
	if !filepath.IsAbs(c.Snapshots) || filepath.Clean(c.Snapshots) != c.Snapshots {
		return false, fail(pc.DeclarationInvalid, "application_snapshot_root_invalid")
	}
	if err := os.MkdirAll(c.Snapshots, 0700); err != nil {
		return false, err
	}
	destination := filepath.Join(c.Snapshots, key[len("sha256:"):])
	in, err := validateSourceApplicationPayload(p)
	if err != nil {
		return false, err
	}
	err = freezer.Freeze(ctx, in, destination, p.SourceSnapshot)
	if os.IsExist(err) {
		copyInput := in
		copyInput.SourceRoot = destination
		var got ab.Snapshot
		got, err = c.Source.Builder.Snapshot(ctx, copyInput)
		if err == nil && !same(got, p.SourceSnapshot) {
			err = ab.ErrSourceChanged
		}
	}
	if err != nil {
		return false, sourceApplicationFailure(ctx, err)
	}
	if err := call.Fence.Check(ctx); err != nil {
		return false, err
	}
	raw, _ := json.Marshal(applicationBuildInput{Operation: call.OperationID, Action: call.Action.ID, InputHash: call.Action.InputHash, FrozenRoot: destination})
	_, err = c.Jobs.EnqueueApplicationBuild(ctx, declarationRequest(call.Principal), call.Target.OwnerNodeID, call.Target.ProjectID, key, raw)
	return false, err
}

func (c *ApplicationBuildCoordinator) Next(ctx context.Context, node string, now time.Time) (*time.Time, error) {
	return c.Jobs.NextApplicationBuildAt(ctx, node, now)
}

func (c *ApplicationBuildCoordinator) RunOne(ctx context.Context, req requestctx.Context, workerRunID string) (bool, error) {
	claim, err := c.Jobs.ClaimApplicationBuild(ctx, req, workerRunID)
	if errors.Is(err, jobs.ErrNoQueuedJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var input applicationBuildInput
	if json.Unmarshal(claim.Job.InputJSON, &input) != nil || filepath.Dir(input.FrozenRoot) != c.Snapshots {
		return true, fmt.Errorf("invalid retained application build")
	}
	o, err := loadOperation(ctx, c.Parent.db, input.Operation)
	if err != nil {
		return true, err
	}
	var action pc.DeclarationAction
	for _, a := range o.Resolution.Plan.Basis.Actions {
		if a.ID == input.Action {
			action = a
			break
		}
	}
	if action.ID == "" || action.InputHash != input.InputHash {
		return true, fmt.Errorf("application build parent mismatch")
	}
	expectedKey := hashBytes([]byte(o.ID + "\x00" + action.ID + "\x00" + action.InputHash))
	if claim.Job.TargetID == nil || *claim.Job.TargetID != expectedKey || filepath.Base(input.FrozenRoot) != expectedKey[len("sha256:"):] || claim.Job.OriginActorID != o.Principal.ActorID || claim.Job.ExecutionNodeID != o.Resolution.Plan.Basis.Target.OwnerNodeID {
		return true, fmt.Errorf("application build custody/actor mismatch")
	}
	var p applicationPayload
	if strictOwnerPayload(o.Resolution.Payloads[input.Action], &p) != nil || p.Source == nil {
		return true, fmt.Errorf("application build payload mismatch")
	}
	if claim.Job.Status == jobs.StatusRunning {
		buildStarted := false
		buildErr := c.Parent.executeAuth(ctx, o.Principal, o.Resolution.Plan.Basis, action, o.Request.ApprovalRefs)
		if buildErr == nil {
			buildErr = c.Parent.checkCurrent(ctx, o.Principal, o.Resolution, allReceipts(o))
		}
		if buildErr == nil {
			buildStarted = true
			buildCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				timer := time.NewTicker(25 * time.Second)
				defer timer.Stop()
				for {
					select {
					case <-buildCtx.Done():
						return
					case <-timer.C:
						if err := c.Jobs.RenewLease(buildCtx, claim.Runner.RunnerID, claim.Job.JobID, 2*time.Minute, workerRunID); err != nil {
							cancel()
							return
						}
					}
				}
			}()
			_, buildErr = c.Source.resolve(buildCtx, *p.Source, true, input.FrozenRoot)
			cancel()
			<-done
		}
		var detail *ab.CommandFailure
		if errors.As(buildErr, &detail) && detail.Detail != "" && claim.Attempt.WorkdirPath != "" {
			if err := os.MkdirAll(claim.Attempt.WorkdirPath, 0700); err != nil {
				return true, err
			}
			log := filepath.Join(claim.Attempt.WorkdirPath, "compiler.stderr")
			if err := os.WriteFile(log, []byte(detail.Detail), 0600); err != nil {
				return true, err
			}
			if _, err := c.Jobs.AppendJobLogRef(ctx, claim.Job.JobID, claim.Attempt.JobAttemptID, "stderr", log); err != nil {
				return true, err
			}
		}
		if ctx.Err() != nil || errors.Is(buildErr, context.Canceled) || errors.Is(buildErr, context.DeadlineExceeded) {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			return true, c.Jobs.HoldApplicationBuild(recovery, claim)
		}
		if err := c.Jobs.FinishApplicationBuild(ctx, req, claim, buildErr, buildStarted); err != nil {
			return true, err
		}
	}
	// Only the existing owner may publish/install; it rechecks current authority,
	// source and original operation fences after compilation and on every resume.
	request := o.Request
	request.OperationID = o.ID
	result, applyErr := c.Parent.Apply(ctx, o.Principal, request)
	done := applicationParentDone(result)
	if applicationBuildPending(applyErr) {
		applyErr = nil
	}
	return true, c.Jobs.FinishApplicationParent(ctx, claim, done, applyErr)
}

func applicationBuildPending(err error) bool {
	var pending *Failure
	return errors.As(err, &pending) && pc.DeclarationWaitingForOwner(pending.Code, pending.Cause)
}

func applicationParentDone(result pc.DeclarationResult) bool {
	if result.State == pc.DeclarationOperationSucceeded || result.State == pc.DeclarationOperationFailed || result.State == pc.DeclarationOperationSuperseded {
		return true
	}
	// A partial operation can contain a terminal owner failure. Only confirmed
	// pending owner work warrants another continuation; never hot-loop a failure.
	for _, issue := range result.Errors {
		if issue.Code != pc.DeclarationOwnerPending {
			return true
		}
	}
	return false
}
