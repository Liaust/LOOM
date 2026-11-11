package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAdmissionDiagnosticsPhasesAndIdentity(t *testing.T) {
	for _, phase := range []string{"source_root_reconciliation", "source_root_listing", "source_generation",
		"priority_selection", "priority_reconciliation", "catalog_selection", "catalog_hydration",
		"inventory_selection", "candidate_reconciliation", "object_admission",
		"canonical_file_link", "metadata_search_ensure", "pipeline_ensure", "pipeline_publication"} {
		for _, cause := range []error{context.DeadlineExceeded, context.Canceled, errors.New("secret SQL source text")} {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			finish := admissionPhase(ctx, phase)
			if finish(nil) != nil {
				t.Fatal("successful phase fabricated a warning")
			}
			err := finish(fmt.Errorf("private failure: %w", cause))
			cancel()
			var got *AdmissionPhaseError
			if !errors.As(err, &got) || !errors.Is(err, cause) || got.Phase != phase || got.Elapsed < 0 || got.Budget <= 0 || got.Budget > time.Second || got.Retryable != errors.Is(cause, context.DeadlineExceeded) {
				t.Fatalf("phase error=%+v", got)
			}
			raw, err := json.Marshal(got.Warning())
			if err != nil || strings.Contains(string(raw)+got.Error(), "private") || strings.Contains(string(raw)+got.Error(), "secret") || len(got.Error()) > 256 {
				t.Fatalf("unbounded/sensitive diagnostic: %s %s", raw, got.Error())
			}
		}
	}
}

func TestAdmissionDiagnosticsNoDeadlineAndNestedPhase(t *testing.T) {
	inner := admissionPhase(t.Context(), "catalog_hydration")(context.DeadlineExceeded)
	outer := admissionPhase(t.Context(), "catalog_selection")(inner)
	if outer != inner {
		t.Fatal("outer phase obscured actual hydration failure")
	}
	got := inner.(*AdmissionPhaseError)
	if got.Budget != 0 || !got.Retryable {
		t.Fatalf("no deadline does not mean no execution budget: %+v", got)
	}
	if admissionPhase(t.Context(), "candidate_reconciliation")(nil) != nil {
		t.Fatal("no-work success is not quiet")
	}
}

func TestAdmissionDiagnosticsCanceledDriverError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	finish := admissionPhase(ctx, "priority_selection")
	cancel()
	driverErr := errors.New("driver cancellation with sensitive SQL")
	err := finish(driverErr)
	if !errors.Is(err, driverErr) || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("lost cause/cancellation identity or exposed driver detail", err)
	}
}

func TestAdmissionDiagnosticsRejectUnknownPhase(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("accepted an arbitrary phase")
		}
	}()
	admissionPhase(t.Context(), "secret phase")
}

func TestAdmissionDiagnosticsSanitizeExportedFields(t *testing.T) {
	err := &AdmissionPhaseError{Phase: "sensitive source", Elapsed: -time.Second, Budget: -time.Second, Cause: errors.New("secret")}
	if warning := err.Warning(); warning.Phase != "unknown" || warning.ElapsedMS != 0 || warning.BudgetMS != 0 || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe warning: %+v", warning)
	}
}

func TestAdmissionDiagnosticsNormalizeSavedWarning(t *testing.T) {
	if NormalizeAdmissionPhaseWarning(nil) != nil {
		t.Fatal("missing warning became a reported warning")
	}
	for _, phase := range []string{"source_root_reconciliation", "source_root_listing", "source_generation",
		"priority_selection", "priority_reconciliation", "catalog_selection", "catalog_hydration",
		"inventory_selection", "candidate_reconciliation", "object_admission",
		"canonical_file_link", "metadata_search_ensure", "pipeline_ensure", "pipeline_publication"} {
		for _, reason := range []string{"deadline_exceeded", "canceled", "operation_failed"} {
			warning := &AdmissionPhaseWarning{Phase: phase, Reason: reason, ElapsedMS: 800, BudgetMS: 590, Retryable: true}
			if safe := NormalizeAdmissionPhaseWarning(warning); *safe != *warning || safe == warning {
				t.Fatalf("normalization changed closed fields or reused input: %+v", safe)
			}
		}
	}
	warning := &AdmissionPhaseWarning{Phase: "private phase\n", Reason: "private SQL failure", ElapsedMS: -1, BudgetMS: -500}
	safe := NormalizeAdmissionPhaseWarning(warning)
	if safe.Phase != "unknown" || safe.Reason != "unknown" || safe.ElapsedMS != 0 || safe.BudgetMS != 0 || safe.Retryable {
		t.Fatalf("unsafe saved warning: %+v", safe)
	}
	if warning.Phase != "private phase\n" || warning.ElapsedMS != -1 {
		t.Fatal("normalization mutated historical input")
	}
}
