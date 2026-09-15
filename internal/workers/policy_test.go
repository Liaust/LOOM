package workers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeSetWorkerPolicyInputRequiresExplicitApplyContract(t *testing.T) {
	base := SetWorkerPolicyInput{
		Policy:                    TickPolicy{Mode: TickModeInterval, IntervalSeconds: 60},
		ExpectedPolicyFingerprint: "sha256:" + strings.Repeat("A", 64),
		Confirm:                   true,
		Reason:                    " acceptance cycle ",
		IdempotencyKey:            " policy-test ",
	}
	normalized, policy, raw, err := normalizeSetWorkerPolicyInput(base)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ExpectedPolicyFingerprint != "sha256:"+strings.Repeat("a", 64) || normalized.Reason != "acceptance cycle" || normalized.IdempotencyKey != "policy-test" || policy.SchemaVersion != TickPolicySchemaVersion || string(raw) != `{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60}` {
		t.Fatalf("normalized input = %#v policy=%#v raw=%s", normalized, policy, raw)
	}

	for name, mutate := range map[string]func(*SetWorkerPolicyInput){
		"missing fingerprint": func(input *SetWorkerPolicyInput) { input.ExpectedPolicyFingerprint = "" },
		"missing mode":        func(input *SetWorkerPolicyInput) { input.Policy.Mode = "" },
		"missing reason":      func(input *SetWorkerPolicyInput) { input.Reason = "" },
		"missing idempotency": func(input *SetWorkerPolicyInput) { input.IdempotencyKey = "" },
		"both modes":          func(input *SetWorkerPolicyInput) { input.DryRun = true },
		"short interval":      func(input *SetWorkerPolicyInput) { input.Policy.IntervalSeconds = 1 },
		"unsupported mode":    func(input *SetWorkerPolicyInput) { input.Policy.Mode = "cron" },
		"oversized reason":    func(input *SetWorkerPolicyInput) { input.Reason = strings.Repeat("r", maxWorkerPolicyReasonBytes+1) },
		"oversized retry key": func(input *SetWorkerPolicyInput) {
			input.IdempotencyKey = strings.Repeat("i", maxWorkerPolicyIdempotencyKeyBytes+1)
		},
		"malformed fingerprint": func(input *SetWorkerPolicyInput) { input.ExpectedPolicyFingerprint = "sha256:not-a-digest" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, _, _, err := normalizeSetWorkerPolicyInput(input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestWorkerPolicyStateIsNormalizedAndNextRunEvidenceIsDerived(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	state, err := workerPolicyState(WorkerInstance{
		WorkerInstanceID: "worker_instance_test",
		WorkerKey:        "main.test",
		Locality:         LocalityMainOwned,
		LifecycleStatus:  LifecycleActive,
		Enabled:          true,
		TickPolicyJSON:   json.RawMessage(`{"interval_seconds":60,"mode":"interval"}`),
		NextRunAfter:     &next,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if state.Policy.SchemaVersion != TickPolicySchemaVersion || state.PolicyFingerprint == "" || state.NextRunAfter == nil || !state.NextRunAfter.Equal(next) {
		t.Fatalf("state = %#v", state)
	}
}

func TestReplayWorkerPolicyMutationRequiresExactKeyAndRequest(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	oldPolicy := TickPolicy{SchemaVersion: TickPolicySchemaVersion, Mode: TickModeManual}
	newPolicy := TickPolicy{SchemaVersion: TickPolicySchemaVersion, Mode: TickModeInterval, IntervalSeconds: 60}
	oldRaw, _ := json.Marshal(oldPolicy)
	newRaw, _ := json.Marshal(newPolicy)
	oldFingerprint, _ := TickPolicyFingerprint(oldRaw)
	newFingerprint, _ := TickPolicyFingerprint(newRaw)
	input := SetWorkerPolicyInput{Policy: newPolicy, ExpectedPolicyFingerprint: oldFingerprint, Reason: "acceptance", Confirm: true, IdempotencyKey: "idem-one"}
	requestFingerprint, err := workerPolicyRequestFingerprint(input, newFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	newNextRunAfter := newPolicy.NextAfter(now)
	oldState := WorkerPolicyState{WorkerInstanceID: "worker_instance_test", WorkerKey: "main.test", Locality: LocalityMainOwned, LifecycleStatus: LifecycleActive, Enabled: true, Policy: oldPolicy, PolicyFingerprint: oldFingerprint, CapturedAt: now}
	newState := oldState
	newState.Policy = newPolicy
	newState.PolicyFingerprint = newFingerprint
	newState.NextRunAfter = newNextRunAfter
	marker, _ := json.Marshal(map[string]any{"policy_control": workerPolicyMutationRecord{
		SchemaVersion: workerPolicyMetadataSchema, IdempotencyKeyHash: workerPolicyIdempotencyKeyHash(input.IdempotencyKey), RequestFingerprint: requestFingerprint,
		ChangedAt: now, Reason: input.Reason, Old: oldState, New: newState,
	}})
	instance := WorkerInstance{WorkerInstanceID: "worker_instance_test", WorkerKey: "main.test", Locality: LocalityMainOwned, LifecycleStatus: LifecycleActive, Enabled: true, TickPolicyJSON: newRaw, NextRunAfter: newNextRunAfter, Metadata: marker}
	current, err := workerPolicyState(instance, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replay, ok := replayWorkerPolicyMutation(instance, input, requestFingerprint, current)
	if !ok || !replay.IdempotentReplay || replay.Old.PolicyFingerprint != oldFingerprint || replay.New.PolicyFingerprint != newFingerprint {
		t.Fatalf("replay = %#v ok=%t", replay, ok)
	}
	input.IdempotencyKey = "different"
	if _, ok := replayWorkerPolicyMutation(instance, input, requestFingerprint, current); ok {
		t.Fatal("different idempotency key replayed")
	}
	input.IdempotencyKey = "idem-one"
	advanced := current
	advancedNext := now.Add(2 * time.Hour)
	advanced.NextRunAfter = &advancedNext
	advancedReplay, ok := replayWorkerPolicyMutation(instance, input, requestFingerprint, advanced)
	if !ok || advancedReplay.New.NextRunAfter == nil || !advancedReplay.New.NextRunAfter.Equal(*newNextRunAfter) || !advancedReplay.New.CapturedAt.Equal(now) {
		t.Fatalf("retry lost historical next-run evidence after scheduling state advanced: %#v ok=%t", advancedReplay, ok)
	}
}
