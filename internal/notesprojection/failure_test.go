package notesprojection

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestProjectionFailureDependenciesAvoidFullReadAndObservationFeedback(t *testing.T) {
	s, provider := deltaFixture(t)
	source := provider.sources["a"]
	source.RootKind, source.Declaration, source.RootRelativePath, source.ProjectSlug = RootKindProjectMaterial, "notes", "../invalid", "atlas"
	deps := []FailureDependency{sourceDependency(source)}
	source.LastSeenAt = time.Now()
	provider.sources["a"] = source
	if changed, err := s.FailureChanged(t.Context(), deps); err != nil || changed || provider.fullReads != 1 {
		t.Fatalf("observation caused full retry: %t %v", changed, err)
	}
	source.RootRelativePath = "new-root"
	provider.sources["a"] = source
	if changed, err := s.FailureChanged(t.Context(), deps); err != nil || !changed {
		t.Fatalf("material repair missed: %t %v", changed, err)
	}
	delete(provider.sources, "a")
	if changed, err := s.FailureChanged(t.Context(), deps); changed || !errors.Is(err, ErrDeltaUncertain) {
		t.Fatalf("missing evidence released failure: %t %v", changed, err)
	}
	provider.withdrawn["a"] = "source_withdrawn"
	if changed, err := s.FailureChanged(t.Context(), deps); err != nil || !changed {
		t.Fatalf("exact withdrawal missed: %t %v", changed, err)
	}
}

func TestProjectionInvalidPathsRetainExactDependenciesWithoutMutation(t *testing.T) {
	s, provider := deltaFixture(t)
	before := deltaManifest(t, s)
	source := provider.sources["a"]
	source.RootKind, source.Declaration, source.RootRelativePath, source.ProjectSlug = RootKindProjectMaterial, "notes", "../outside", "atlas"
	provider.sources["a"] = source
	provider.refuseFull = false
	_, err := s.Refresh(t.Context())
	var invalid *InvalidSourcePathsError
	if !errors.As(err, &invalid) || len(invalid.Dependencies) != 1 || invalid.Dependencies[0] != sourceDependency(source) {
		t.Fatalf("invalid source lost exact refusal: %+v %v", invalid, err)
	}
	if !reflect.DeepEqual(before, deltaManifest(t, s)) {
		t.Fatal("failed admission rewrote manifest")
	}
	provider.refuseFull = true
	if changed, err := s.FailureChanged(t.Context(), invalid.Dependencies); changed || err != nil {
		t.Fatalf("unchanged invalid source not held: %t %v", changed, err)
	}
	// The actual root contract that triggered Main's failure is now valid.
	source.RootRelativePath = "."
	provider.sources["a"] = source
	if changed, err := s.FailureChanged(t.Context(), invalid.Dependencies); !changed || err != nil {
		t.Fatalf("dot-root repair did not release hold: %t %v", changed, err)
	}
}
