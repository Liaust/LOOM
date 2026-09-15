package workers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDetachedWorkerCompletionContextSurvivesRequestCancellation(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	completionCtx, cancelCompletion := detachedWorkerCompletionContext(requestCtx)
	defer cancelCompletion()
	if err := completionCtx.Err(); err != nil {
		t.Fatalf("completion context inherited canceled request: %v", err)
	}
	deadline, ok := completionCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > workerCompletionTimeout {
		t.Fatalf("completion deadline = %v, want bounded future deadline", deadline)
	}
}

func TestNormalizeRunOnceInputDefaultsManualTrigger(t *testing.T) {
	input, err := normalizeRunOnceInput(RunOnceInput{Reason: " operator check "})
	if err != nil {
		t.Fatalf("normalizeRunOnceInput returned error: %v", err)
	}
	if input.TriggerKind != TriggerManual {
		t.Fatalf("trigger kind = %q, want %q", input.TriggerKind, TriggerManual)
	}
	if input.TriggerRef != "operator check" {
		t.Fatalf("trigger ref = %q, want reason", input.TriggerRef)
	}
	if string(input.Metadata) != `{}` {
		t.Fatalf("metadata = %s, want {}", input.Metadata)
	}
}

func TestNormalizeRunOnceInputAcceptsSupervisorTrigger(t *testing.T) {
	input, err := normalizeRunOnceInput(RunOnceInput{
		TriggerKind:               TriggerSupervisorTick,
		TriggerRef:                " main.policy_expiry ",
		ExpectedPolicyFingerprint: " sha256:test ",
		Metadata:                  json.RawMessage(`{"source":"test"}`),
		ScheduleEvidenceAt:        time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("normalizeRunOnceInput returned error: %v", err)
	}
	if input.TriggerKind != TriggerSupervisorTick {
		t.Fatalf("trigger kind = %q, want %q", input.TriggerKind, TriggerSupervisorTick)
	}
	if input.TriggerRef != "main.policy_expiry" {
		t.Fatalf("trigger ref = %q, want main.policy_expiry", input.TriggerRef)
	}
	if input.ExpectedPolicyFingerprint != "sha256:test" {
		t.Fatalf("expected fingerprint = %q", input.ExpectedPolicyFingerprint)
	}
}

func TestNormalizeRunOnceInputRejectsSupervisorWithoutPolicyEvidence(t *testing.T) {
	_, err := normalizeRunOnceInput(RunOnceInput{TriggerKind: TriggerSupervisorTick, TriggerRef: "main.policy_expiry", ScheduleEvidenceAt: time.Now().UTC()})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeRunOnceInputRejectsExternalSupervisorTriggerWithoutInternalTime(t *testing.T) {
	_, err := normalizeRunOnceInput(RunOnceInput{TriggerKind: TriggerSupervisorTick, TriggerRef: "main.policy_expiry", ExpectedPolicyFingerprint: "sha256:test"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeRunOnceInputRejectsInvalidTrigger(t *testing.T) {
	_, err := normalizeRunOnceInput(RunOnceInput{TriggerKind: "timer"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}
