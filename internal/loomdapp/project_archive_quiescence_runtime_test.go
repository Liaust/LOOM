package loomdapp

import (
	"testing"

	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/storagearchive"
)

func TestProjectArchiveQuiescenceRuntimeUsesRoutedVerifier(t *testing.T) {
	verifier := projectArchiveQuiescenceRuntime(routing.Service{})
	routed, ok := verifier.(*storagearchive.RoutedProjectRuntimeQuiescenceVerifier)
	if !ok {
		t.Fatalf("project archive quiescence verifier = %T, want routed verifier", verifier)
	}
	if routed.Routing == nil {
		t.Fatal("routed verifier has no routing service")
	}
}
