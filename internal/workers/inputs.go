package workers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type WorkerFilter struct {
	Limit   int
	Kind    string
	Status  string
	Health  string
	NodeRef string
}

type RunFilter struct {
	Limit   int
	Status  string
	Trigger string
}

type RunOnceInput struct {
	Reason                    string          `json:"reason"`
	Metadata                  json.RawMessage `json:"metadata"`
	IdempotencyKey            string          `json:"idempotency_key"`
	TriggerKind               string          `json:"trigger_kind,omitempty"`
	TriggerRef                string          `json:"trigger_ref,omitempty"`
	ExpectedPolicyFingerprint string          `json:"expected_policy_fingerprint,omitempty"`
	ScheduleEvidenceAt        time.Time       `json:"-"`
}

func normalizeRunOnceInput(input RunOnceInput) (RunOnceInput, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.TriggerKind = strings.TrimSpace(input.TriggerKind)
	input.TriggerRef = strings.TrimSpace(input.TriggerRef)
	input.ExpectedPolicyFingerprint = strings.TrimSpace(input.ExpectedPolicyFingerprint)
	input.Metadata = rawJSONObjectOrDefault(input.Metadata)

	if input.TriggerKind == "" {
		input.TriggerKind = TriggerManual
	}
	if !validTriggerKind(input.TriggerKind) {
		return RunOnceInput{}, fmt.Errorf("%w: trigger kind %q is invalid", ErrInvalid, input.TriggerKind)
	}
	if input.TriggerRef == "" && input.TriggerKind == TriggerManual {
		input.TriggerRef = input.Reason
	}
	if input.TriggerKind == TriggerSupervisorTick && input.ExpectedPolicyFingerprint == "" {
		return RunOnceInput{}, fmt.Errorf("%w: supervisor tick requires expected_policy_fingerprint", ErrInvalid)
	}
	if input.TriggerKind == TriggerSupervisorTick && input.ScheduleEvidenceAt.IsZero() {
		return RunOnceInput{}, fmt.Errorf("%w: supervisor tick requires internal schedule evidence time", ErrInvalid)
	}
	return input, nil
}

func validTriggerKind(value string) bool {
	switch value {
	case TriggerManual, TriggerSupervisorTick, TriggerSchedule, TriggerDirectEvent, TriggerRetry, TriggerStartup:
		return true
	default:
		return false
	}
}
