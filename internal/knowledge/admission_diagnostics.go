package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AdmissionPhaseError qualifies an existing failure without exposing its cause text.
type AdmissionPhaseError struct {
	Phase     string
	Elapsed   time.Duration
	Budget    time.Duration
	Retryable bool
	Cause     error
}

func (e *AdmissionPhaseError) Error() string {
	return fmt.Sprintf("Notes admission %s: %s (elapsed=%s budget=%s retryable=%t)",
		admissionPhaseName(e.Phase), admissionFailureReason(e.Cause),
		max(0, e.Elapsed).Truncate(time.Millisecond), max(0, e.Budget).Truncate(time.Millisecond), e.Retryable)
}

func (e *AdmissionPhaseError) Unwrap() error { return e.Cause }

// Only these fields are forwarded into runtime results; Cause is never serialized.
type AdmissionPhaseWarning struct {
	Phase     string `json:"phase"`
	Reason    string `json:"reason"`
	ElapsedMS int64  `json:"elapsed_ms"`
	BudgetMS  int64  `json:"budget_ms"`
	Retryable bool   `json:"retryable"`
}

// NormalizeAdmissionPhaseWarning projects saved diagnostics onto the closed
// vocabulary, without retaining arbitrary phase/reason text or negative timing.
func NormalizeAdmissionPhaseWarning(warning *AdmissionPhaseWarning) *AdmissionPhaseWarning {
	if warning == nil {
		return nil
	}
	safe := *warning
	safe.Phase = admissionPhaseName(safe.Phase)
	switch safe.Reason {
	case "deadline_exceeded", "canceled", "operation_failed":
	default:
		safe.Reason = "unknown"
	}
	safe.ElapsedMS, safe.BudgetMS = max(0, safe.ElapsedMS), max(0, safe.BudgetMS)
	return &safe
}

func (e *AdmissionPhaseError) Warning() *AdmissionPhaseWarning {
	return &AdmissionPhaseWarning{
		Phase: admissionPhaseName(e.Phase), Reason: admissionFailureReason(e.Cause),
		ElapsedMS: max(0, e.Elapsed.Milliseconds()), BudgetMS: max(0, e.Budget.Milliseconds()), Retryable: e.Retryable,
	}
}

func admissionPhaseName(phase string) string {
	switch phase {
	case "source_root_reconciliation", "source_root_listing", "source_generation",
		"priority_selection", "priority_reconciliation", "catalog_selection", "catalog_hydration",
		"inventory_selection", "candidate_reconciliation", "object_admission",
		"canonical_file_link", "metadata_search_ensure", "pipeline_ensure", "pipeline_publication":
		return phase
	default:
		return "unknown"
	}
}

func admissionFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "operation_failed"
	}
}

// Start immediately before the owned operation; zero budget means no deadline.
func admissionPhase(ctx context.Context, phase string) func(error) error {
	if admissionPhaseName(phase) == "unknown" {
		panic("unsupported admission phase")
	}
	started := time.Now()
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = max(0, deadline.Sub(started))
	}
	return func(err error) error {
		if err == nil {
			return nil
		}
		var existing *AdmissionPhaseError
		if errors.As(err, &existing) {
			return err
		}
		if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = errors.Join(err, ctx.Err())
		}
		return &AdmissionPhaseError{Phase: phase, Elapsed: time.Since(started), Budget: budget,
			Retryable: errors.Is(err, context.DeadlineExceeded), Cause: err}
	}
}
