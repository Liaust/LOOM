package update

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type releaseRetentionFixture struct {
	now         time.Time
	releasesDir string
	activePath  string
	stateDir    string
	paths       map[string]string
}

func TestPlanReleaseRetentionProtectsRequiredReleases(t *testing.T) {
	fixture := newReleaseRetentionFixture(t)
	plan, err := PlanReleaseRetention(fixture.input())
	if err != nil {
		t.Fatalf("PlanReleaseRetention returned error: %v", err)
	}
	if plan.Status != PlanStatusReady || IsReleaseRetentionBlocked(plan) {
		t.Fatalf("plan status = %q diagnostics=%#v", plan.Status, plan.Diagnostics)
	}
	assertRetentionReason(t, plan, "current", "active_symlink")
	assertRetentionReason(t, plan, "rollback", "immediate_rollback")
	assertRetentionReason(t, plan, "recent-success-1", "recent_successful_update")
	assertRetentionReason(t, plan, "recent-success-2", "recent_successful_update")
	assertRetentionReason(t, plan, "pinned", "operator_pin")
	assertRetentionReason(t, plan, "young", "young_release")

	gotDelete := retentionNames(plan.Delete)
	wantDelete := []string{"abandoned", "failed-target", "old-success"}
	if !slices.Equal(gotDelete, wantDelete) {
		t.Fatalf("delete names = %v, want %v", gotDelete, wantDelete)
	}
	if plan.DeleteBytes <= 0 || plan.ProtectedByte <= 0 {
		t.Fatalf("plan byte totals were not populated: %#v", plan)
	}
	if !strings.HasPrefix(plan.PlanHash, "sha256:") {
		t.Fatalf("plan hash = %q", plan.PlanHash)
	}

	repeat, err := PlanReleaseRetention(fixture.input())
	if err != nil {
		t.Fatalf("repeat PlanReleaseRetention returned error: %v", err)
	}
	if repeat.PlanHash != plan.PlanHash {
		t.Fatalf("plan hash changed without state change: %s != %s", repeat.PlanHash, plan.PlanHash)
	}
}

func TestApplyReleaseRetentionRequiresReviewedHashAndPreservesProtected(t *testing.T) {
	fixture := newReleaseRetentionFixture(t)
	plan, err := PlanReleaseRetention(fixture.input())
	if err != nil {
		t.Fatalf("PlanReleaseRetention returned error: %v", err)
	}

	withoutYes, err := ApplyReleaseRetention(ReleaseRetentionApplyInput{
		PlanInput:        fixture.input(),
		ExpectedPlanHash: plan.PlanHash,
	})
	if err != nil {
		t.Fatalf("ApplyReleaseRetention without confirmation returned error: %v", err)
	}
	if !withoutYes.Refused || withoutYes.Refusal == "" {
		t.Fatalf("apply without confirmation was not refused: %#v", withoutYes)
	}

	wrongHash, err := ApplyReleaseRetention(ReleaseRetentionApplyInput{
		PlanInput:        fixture.input(),
		ExpectedPlanHash: "sha256:wrong",
		Yes:              true,
	})
	if err != nil {
		t.Fatalf("ApplyReleaseRetention with wrong hash returned error: %v", err)
	}
	if !wrongHash.Refused || !strings.Contains(wrongHash.Refusal, "plan changed") {
		t.Fatalf("wrong hash was not refused: %#v", wrongHash)
	}

	result, err := ApplyReleaseRetention(ReleaseRetentionApplyInput{
		PlanInput:        fixture.input(),
		ExpectedPlanHash: plan.PlanHash,
		Yes:              true,
	})
	if err != nil {
		t.Fatalf("ApplyReleaseRetention returned error: %v", err)
	}
	if result.Status != UpdateStatusSucceeded || result.Refused {
		t.Fatalf("apply result = %#v", result)
	}
	if got, want := retentionNames(result.Deleted), retentionNames(plan.Delete); !slices.Equal(got, want) {
		t.Fatalf("deleted names = %v, want %v", got, want)
	}
	for _, entry := range plan.Delete {
		if _, err := os.Lstat(entry.Path); !os.IsNotExist(err) {
			t.Fatalf("deletion candidate still exists: %s err=%v", entry.Path, err)
		}
	}
	for _, entry := range plan.Protected {
		if info, err := os.Stat(entry.Path); err != nil || !info.IsDir() {
			t.Fatalf("protected release was removed or changed: %s info=%v err=%v", entry.Path, info, err)
		}
	}
}

func TestApplyReleaseRetentionRejectsStateChangeAfterPlan(t *testing.T) {
	fixture := newReleaseRetentionFixture(t)
	plan, err := PlanReleaseRetention(fixture.input())
	if err != nil {
		t.Fatalf("PlanReleaseRetention returned error: %v", err)
	}
	makeReleaseRetentionDir(t, fixture.releasesDir, "appeared-after-plan", fixture.now.Add(-10*24*time.Hour))

	result, err := ApplyReleaseRetention(ReleaseRetentionApplyInput{
		PlanInput:        fixture.input(),
		ExpectedPlanHash: plan.PlanHash,
		Yes:              true,
	})
	if err != nil {
		t.Fatalf("ApplyReleaseRetention returned error: %v", err)
	}
	if !result.Refused || !strings.Contains(result.Refusal, "plan changed") {
		t.Fatalf("changed release root was not refused: %#v", result)
	}
	for _, entry := range plan.Delete {
		if _, err := os.Stat(entry.Path); err != nil {
			t.Fatalf("candidate was deleted despite changed plan: %s: %v", entry.Path, err)
		}
	}
}

func TestPlanReleaseRetentionBlocksInvalidEvidence(t *testing.T) {
	t.Run("missing active manifest", func(t *testing.T) {
		fixture := newReleaseRetentionFixture(t)
		if err := os.Remove(ActiveManifestPath(fixture.stateDir)); err != nil {
			t.Fatalf("remove active manifest: %v", err)
		}
		plan, err := PlanReleaseRetention(fixture.input())
		if err != nil {
			t.Fatalf("PlanReleaseRetention returned error: %v", err)
		}
		if !IsReleaseRetentionBlocked(plan) {
			t.Fatalf("missing active manifest did not block: %#v", plan.Diagnostics)
		}
	})

	t.Run("invalid history manifest", func(t *testing.T) {
		fixture := newReleaseRetentionFixture(t)
		path := filepath.Join(HistoryDir(fixture.stateDir), "broken.yaml")
		if err := os.WriteFile(path, []byte("not: [valid"), 0o640); err != nil {
			t.Fatalf("write invalid history: %v", err)
		}
		plan, err := PlanReleaseRetention(fixture.input())
		if err != nil {
			t.Fatalf("PlanReleaseRetention returned error: %v", err)
		}
		if !IsReleaseRetentionBlocked(plan) {
			t.Fatalf("invalid history did not block: %#v", plan.Diagnostics)
		}
	})

	t.Run("non-directory release entry", func(t *testing.T) {
		fixture := newReleaseRetentionFixture(t)
		if err := os.WriteFile(filepath.Join(fixture.releasesDir, "unexpected-file"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write unexpected file: %v", err)
		}
		if _, err := PlanReleaseRetention(fixture.input()); err == nil || !strings.Contains(err.Error(), "non-directory") {
			t.Fatalf("non-directory release entry error = %v", err)
		}
	})

	t.Run("release root symlink", func(t *testing.T) {
		fixture := newReleaseRetentionFixture(t)
		alias := filepath.Join(t.TempDir(), "releases-link")
		if err := os.Symlink(fixture.releasesDir, alias); err != nil {
			t.Fatalf("symlink release root: %v", err)
		}
		input := fixture.input()
		input.ReleasesDir = alias
		if _, err := PlanReleaseRetention(input); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("symlink release root error = %v", err)
		}
	})
}

func newReleaseRetentionFixture(t *testing.T) releaseRetentionFixture {
	t.Helper()
	root := t.TempDir()
	fixture := releaseRetentionFixture{
		now:         time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		releasesDir: filepath.Join(root, "releases"),
		activePath:  filepath.Join(root, "current"),
		stateDir:    filepath.Join(root, "update"),
		paths:       map[string]string{},
	}
	if err := os.MkdirAll(fixture.releasesDir, 0o755); err != nil {
		t.Fatalf("mkdir releases: %v", err)
	}
	old := fixture.now.Add(-10 * 24 * time.Hour)
	for _, name := range []string{
		"current", "rollback", "recent-success-1", "recent-success-2",
		"old-success", "failed-target", "pinned", "abandoned",
	} {
		fixture.paths[name] = makeReleaseRetentionDir(t, fixture.releasesDir, name, old)
	}
	fixture.paths["young"] = makeReleaseRetentionDir(t, fixture.releasesDir, "young", fixture.now.Add(-time.Hour))
	if err := os.Symlink(fixture.paths["current"], fixture.activePath); err != nil {
		t.Fatalf("symlink active release: %v", err)
	}
	writeReleaseRetentionManifest(t, ActiveManifestPath(fixture.stateDir), UpdateManifest{
		Status:    UpdateStatusSucceeded,
		StartedAt: fixture.now.Add(-time.Hour),
		Active:    ReleaseState{Path: fixture.paths["rollback"]},
		Target:    ReleaseState{Path: fixture.paths["current"]},
	})
	history := []struct {
		name    string
		status  string
		target  string
		started time.Time
	}{
		{"recent-2", UpdateStatusSucceeded, fixture.paths["recent-success-2"], fixture.now.Add(-3 * time.Hour)},
		{"recent-1", UpdateStatusSucceeded, fixture.paths["recent-success-1"], fixture.now.Add(-4 * time.Hour)},
		{"old-success", UpdateStatusSucceeded, fixture.paths["old-success"], fixture.now.Add(-5 * time.Hour)},
		{"failed", UpdateStatusFailed, fixture.paths["failed-target"], fixture.now.Add(-2 * time.Hour)},
	}
	for _, item := range history {
		writeReleaseRetentionManifest(t, filepath.Join(HistoryDir(fixture.stateDir), item.name+".yaml"), UpdateManifest{
			Status:    item.status,
			StartedAt: item.started,
			Active:    ReleaseState{Path: fixture.paths["rollback"]},
			Target:    ReleaseState{Path: item.target},
		})
	}
	return fixture
}

func (fixture releaseRetentionFixture) input() ReleaseRetentionInput {
	return ReleaseRetentionInput{
		ReleasesDir:        fixture.releasesDir,
		ActivePath:         fixture.activePath,
		StateDir:           fixture.stateDir,
		KeepSuccessful:     2,
		ProtectYoungerThan: 48 * time.Hour,
		Pins:               []string{"pinned"},
		Now:                func() time.Time { return fixture.now },
	}
}

func makeReleaseRetentionDir(t *testing.T, root, name string, modified time.Time) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir release %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(path, "release.txt"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatalf("write release %s: %v", name, err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set release time %s: %v", name, err)
	}
	return path
}

func writeReleaseRetentionManifest(t *testing.T, path string, manifest UpdateManifest) {
	t.Helper()
	manifest.SchemaVersion = UpdateManifestSchemaVersion
	manifest.Rollback = RollbackPlan{Class: RollbackClassServiceOnly}
	if err := WriteUpdateManifest(path, manifest); err != nil {
		t.Fatalf("write update manifest %s: %v", path, err)
	}
}

func assertRetentionReason(t *testing.T, plan ReleaseRetentionPlan, name, reason string) {
	t.Helper()
	for _, entry := range plan.Protected {
		if entry.Name == name {
			if !slices.Contains(entry.Reasons, reason) {
				t.Fatalf("release %s reasons = %v, want %s", name, entry.Reasons, reason)
			}
			return
		}
	}
	t.Fatalf("release %s was not protected", name)
}

func retentionNames(entries []ReleaseRetentionEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}
