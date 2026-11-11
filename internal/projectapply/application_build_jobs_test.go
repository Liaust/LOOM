package projectapply

import (
	pc "loom.local/loom/internal/projectcontracts"
	"testing"
)

func TestApplicationBuildParentStopsOnTerminalPartialFailure(t *testing.T) {
	result := pc.DeclarationResult{State: pc.DeclarationOperationPartial, Errors: []pc.DeclarationError{{Code: pc.DeclarationOwnerFailed}}}
	if !applicationParentDone(result) {
		t.Fatal("terminal owner failure would loop")
	}
	result.Errors[0].Code = pc.DeclarationOwnerPending
	if applicationParentDone(result) {
		t.Fatal("pending installation lost continuation")
	}
	result.State = pc.DeclarationOperationSucceeded
	if !applicationParentDone(result) {
		t.Fatal("completed operation would loop")
	}
}

func TestApplicationBuildPendingIsNotACompilerOrContinuationFailure(t *testing.T) {
	for _, code := range []pc.DeclarationErrorCode{pc.DeclarationOwnerPending, pc.DeclarationOwnerFailed} {
		if !applicationBuildPending(fail(code, "owner_pending")) {
			t.Fatal("pending owner acknowledgement would stop Main continuation")
		}
	}
	if applicationBuildPending(fail(pc.DeclarationOwnerFailed, "application_build_failed")) || applicationBuildPending(nil) {
		t.Fatal("terminal failure mistaken for pending")
	}
}
