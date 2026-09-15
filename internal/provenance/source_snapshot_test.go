package provenance

import (
	"regexp"
	"strings"
	"testing"
)

func TestInvariantASourceSnapshotIsPinnedAndExcludesProjectMigrations(t *testing.T) {
	snapshot, err := LoadUpstreamSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("snapshot schema version = %d, want 1", snapshot.SchemaVersion)
	}
	if snapshot.SourcePosture != "same-owner private adaptation" || snapshot.LicenseDeclaration != "absent" {
		t.Fatalf("unexpected source posture: %#v", snapshot)
	}
	const pinnedUpstreamMigrationCount = 6
	if len(snapshot.IncludedMigrations) != pinnedUpstreamMigrationCount {
		t.Fatalf("included migrations = %d, want %d", len(snapshot.IncludedMigrations), pinnedUpstreamMigrationCount)
	}
	hexDigest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for index, source := range snapshot.IncludedMigrations {
		wantVersion := index + 1
		if source.Version != wantVersion || !strings.Contains(source.Path, "/00"+string(rune('0'+wantVersion))+"_") {
			t.Fatalf("included migration %d = %#v", index, source)
		}
		if !hexDigest.MatchString(source.SHA256) {
			t.Fatalf("migration digest is not sha256: %#v", source)
		}
	}
	if len(snapshot.IncludedFixtures) == 0 {
		t.Fatal("snapshot has no upstream parity fixtures")
	}
	for _, source := range snapshot.IncludedFixtures {
		if !hexDigest.MatchString(source.SHA256) {
			t.Fatalf("fixture digest is not sha256: %#v", source)
		}
	}
	wantExcluded := map[int]bool{7: false, 8: false, 9: false}
	for _, source := range snapshot.ExcludedMigrations {
		if _, ok := wantExcluded[source.Version]; !ok {
			t.Fatalf("unexpected excluded migration: %#v", source)
		}
		wantExcluded[source.Version] = true
	}
	for version, found := range wantExcluded {
		if !found {
			t.Fatalf("excluded migration %d is not pinned", version)
		}
	}
}
