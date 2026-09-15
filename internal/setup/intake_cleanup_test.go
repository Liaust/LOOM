package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/box"
)

func TestRetiredIntakeCleanupRemovesOnlyReviewedEmptyDirectoriesAndKnownLinks(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	boxRoot := filepath.Join(root, "box")
	stateRoot := filepath.Join(root, "box-state")
	adminHome := filepath.Join(root, "home", "loomadmin")
	loomdeskHome := filepath.Join(root, "home", "loomdesk")
	for _, path := range []string{boxRoot, stateRoot, adminHome, loomdeskHome, filepath.Join(boxRoot, box.DefaultLaneDirName), filepath.Join(boxRoot, "Dropzone")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(boxRoot, "Dropzone", "keep.txt"), []byte("preserve"), 0o644); err != nil {
		t.Fatalf("write non-empty Dropzone evidence: %v", err)
	}
	knownLink := filepath.Join(adminHome, box.LegacyMainBoxLinkName)
	if err := os.Symlink(boxRoot, knownLink); err != nil {
		t.Fatalf("create known obsolete link: %v", err)
	}
	unknownLink := filepath.Join(adminHome, "'LOOM BOX'")
	if err := os.Symlink(filepath.Join(root, "unexpected"), unknownLink); err != nil {
		t.Fatalf("create unexpected obsolete-name link: %v", err)
	}

	plan := retiredIntakeTestPlan(boxRoot, stateRoot, adminHome)
	cleanup, err := PlanRetiredIntakeCleanup(plan, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("PlanRetiredIntakeCleanup returned error: %v", err)
	}
	plan.RetiredIntakeCleanup = &cleanup
	plan.PlanHash = HashPlan(plan)

	assertRetiredIntakeItemState(t, cleanup, "main_box_lane", RetiredIntakeCleanupEligibleEmptyDir)
	assertRetiredIntakeItemState(t, cleanup, "main_box_dropzone", RetiredIntakeCleanupSkippedNonEmpty)
	assertRetiredIntakeItemState(t, cleanup, "obsolete_legacy_main_box_link_loomadmin", RetiredIntakeCleanupEligibleKnownLink)
	assertRetiredIntakeItemState(t, cleanup, "obsolete_quoted_box_link_loomadmin", RetiredIntakeCleanupSkippedLinkTarget)

	result, err := ApplyRetiredIntakeCleanup(RetiredIntakeCleanupApplyInput{Plan: plan, ConfirmDigest: cleanup.PlanDigest, Yes: true})
	if err != nil {
		t.Fatalf("ApplyRetiredIntakeCleanup returned error: %v", err)
	}
	if result.Status != "applied" || len(result.Removed) != 2 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(boxRoot, box.DefaultLaneDirName)); !os.IsNotExist(err) {
		t.Fatalf("reviewed empty Main Lane directory was not removed: %v", err)
	}
	if _, err := os.Lstat(knownLink); !os.IsNotExist(err) {
		t.Fatalf("reviewed known obsolete link was not removed: %v", err)
	}
	if payload, err := os.ReadFile(filepath.Join(boxRoot, "Dropzone", "keep.txt")); err != nil || string(payload) != "preserve" {
		t.Fatalf("non-empty Dropzone path changed: payload=%q err=%v", payload, err)
	}
	if target, err := os.Readlink(unknownLink); err != nil || target != filepath.Join(root, "unexpected") {
		t.Fatalf("unexpected-target link changed: target=%q err=%v", target, err)
	}
}

func TestRetiredIntakeCleanupRefusesChangedOrNewlyNonEmptyTargets(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	boxRoot := filepath.Join(root, "box")
	stateRoot := filepath.Join(root, "box-state")
	home := filepath.Join(root, "home", "loomadmin")
	for _, path := range []string{boxRoot, stateRoot, home, filepath.Join(boxRoot, box.DefaultLaneDirName), filepath.Join(boxRoot, box.LegacyLaneDirName)} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	plan := retiredIntakeTestPlan(boxRoot, stateRoot, home)
	cleanup, err := PlanRetiredIntakeCleanup(plan, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("PlanRetiredIntakeCleanup returned error: %v", err)
	}
	plan.RetiredIntakeCleanup = &cleanup
	plan.PlanHash = HashPlan(plan)

	changed := filepath.Join(boxRoot, box.DefaultLaneDirName)
	if err := os.Remove(changed); err != nil {
		t.Fatalf("replace planned directory: %v", err)
	}
	if err := os.Mkdir(changed, 0o755); err != nil {
		t.Fatalf("recreate planned directory: %v", err)
	}
	newlyNonEmpty := filepath.Join(boxRoot, box.LegacyLaneDirName)
	if err := os.WriteFile(filepath.Join(newlyNonEmpty, "arrived-after-plan.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write changed target content: %v", err)
	}

	result, err := ApplyRetiredIntakeCleanup(RetiredIntakeCleanupApplyInput{Plan: plan, ConfirmDigest: cleanup.PlanDigest, Yes: true})
	if err != nil {
		t.Fatalf("ApplyRetiredIntakeCleanup returned error: %v", err)
	}
	for _, path := range []string{changed, newlyNonEmpty} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("changed target %s should remain: %v", path, err)
		}
	}
	if !cleanupResultHasStatus(result, "main_box_lane", RetiredIntakeCleanupSkippedChanged) || !cleanupResultHasStatus(result, "main_box_legacy_lane", RetiredIntakeCleanupSkippedChanged) {
		t.Fatalf("changed targets were not reported as skipped: %#v", result)
	}
}

func TestRetiredIntakeCleanupRefusesIntermediateParentSwapBeforeDirectoryRemoval(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	boxRoot := filepath.Join(root, "box")
	stateRoot := filepath.Join(root, "box-state")
	home := filepath.Join(root, "home", "loomadmin")
	parent := filepath.Join(boxRoot, ".loom", "state")
	target := filepath.Join(parent, "dropzone")
	for _, path := range []string{target, stateRoot, home} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	plan := retiredIntakeTestPlan(boxRoot, stateRoot, home)
	cleanup, err := PlanRetiredIntakeCleanup(plan, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("PlanRetiredIntakeCleanup returned error: %v", err)
	}
	plan.RetiredIntakeCleanup = &cleanup
	plan.PlanHash = HashPlan(plan)
	assertRetiredIntakeItemState(t, cleanup, "main_box_legacy_dropzone_state", RetiredIntakeCleanupEligibleEmptyDir)

	renamedParent := filepath.Join(boxRoot, ".loom", "state-reviewed")
	replacementTarget := filepath.Join(parent, "dropzone")
	hookRan := false
	result, err := applyRetiredIntakeCleanup(
		RetiredIntakeCleanupApplyInput{Plan: plan, ConfirmDigest: cleanup.PlanDigest, Yes: true},
		retiredIntakeCleanupApplyHooks{beforeFinalRevalidation: func(item RetiredIntakeCleanupItem) error {
			if item.ID != "main_box_legacy_dropzone_state" {
				return nil
			}
			hookRan = true
			if err := os.Rename(parent, renamedParent); err != nil {
				return err
			}
			return os.MkdirAll(replacementTarget, 0o755)
		}},
	)
	if err != nil {
		t.Fatalf("applyRetiredIntakeCleanup returned error: %v", err)
	}
	if !hookRan {
		t.Fatal("intermediate-parent swap hook did not run")
	}
	if !cleanupResultHasStatus(result, "main_box_legacy_dropzone_state", RetiredIntakeCleanupSkippedChanged) {
		t.Fatalf("intermediate-parent swap was not reported as changed: %#v", result)
	}
	for _, path := range []string{filepath.Join(renamedParent, "dropzone"), replacementTarget} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("cleanup removed directory from swapped tree %s: info=%#v err=%v", path, info, err)
		}
	}
}

func TestRetiredIntakeCleanupRefusesCleanupRootSwapBeforeKnownLinkRemoval(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	boxRoot := filepath.Join(root, "box")
	stateRoot := filepath.Join(root, "box-state")
	home := filepath.Join(root, "home", "loomadmin")
	for _, path := range []string{boxRoot, stateRoot, home} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	linkName := box.LegacyMainBoxLinkName
	if err := os.Symlink(boxRoot, filepath.Join(home, linkName)); err != nil {
		t.Fatalf("create reviewed known link: %v", err)
	}

	plan := retiredIntakeTestPlan(boxRoot, stateRoot, home)
	cleanup, err := PlanRetiredIntakeCleanup(plan, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("PlanRetiredIntakeCleanup returned error: %v", err)
	}
	plan.RetiredIntakeCleanup = &cleanup
	plan.PlanHash = HashPlan(plan)
	id := "obsolete_legacy_main_box_link_loomadmin"
	assertRetiredIntakeItemState(t, cleanup, id, RetiredIntakeCleanupEligibleKnownLink)

	renamedHome := filepath.Join(root, "home", "loomadmin-reviewed")
	hookRan := false
	result, err := applyRetiredIntakeCleanup(
		RetiredIntakeCleanupApplyInput{Plan: plan, ConfirmDigest: cleanup.PlanDigest, Yes: true},
		retiredIntakeCleanupApplyHooks{beforeFinalRevalidation: func(item RetiredIntakeCleanupItem) error {
			if item.ID != id {
				return nil
			}
			hookRan = true
			if err := os.Rename(home, renamedHome); err != nil {
				return err
			}
			if err := os.Mkdir(home, 0o755); err != nil {
				return err
			}
			return os.Symlink(boxRoot, filepath.Join(home, linkName))
		}},
	)
	if err != nil {
		t.Fatalf("applyRetiredIntakeCleanup returned error: %v", err)
	}
	if !hookRan {
		t.Fatal("cleanup-root swap hook did not run")
	}
	if !cleanupResultHasStatus(result, id, RetiredIntakeCleanupSkippedChanged) {
		t.Fatalf("cleanup-root swap was not reported as changed: %#v", result)
	}
	for _, path := range []string{filepath.Join(renamedHome, linkName), filepath.Join(home, linkName)} {
		if target, err := os.Readlink(path); err != nil || target != boxRoot {
			t.Fatalf("cleanup removed known link from swapped tree %s: target=%q err=%v", path, target, err)
		}
	}
}

func TestRetiredIntakeCleanupRefusesSymlinkedParentAndDigestMismatch(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	boxRoot := filepath.Join(root, "box")
	stateRoot := filepath.Join(root, "box-state")
	home := filepath.Join(root, "home", "loomadmin")
	external := filepath.Join(root, "external")
	for _, path := range []string{boxRoot, stateRoot, home, filepath.Join(external, "state", "dropzone")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := os.Symlink(external, filepath.Join(boxRoot, ".loom")); err != nil {
		t.Fatalf("create symlinked cleanup parent: %v", err)
	}
	plan := retiredIntakeTestPlan(boxRoot, stateRoot, home)
	cleanup, err := PlanRetiredIntakeCleanup(plan, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("PlanRetiredIntakeCleanup returned error: %v", err)
	}
	assertRetiredIntakeItemState(t, cleanup, "main_box_legacy_dropzone_state", RetiredIntakeCleanupSkippedUnsafePath)
	plan.RetiredIntakeCleanup = &cleanup
	plan.PlanHash = HashPlan(plan)
	if _, err := ApplyRetiredIntakeCleanup(RetiredIntakeCleanupApplyInput{Plan: plan, ConfirmDigest: "sha256:wrong", Yes: true}); err == nil {
		t.Fatal("cleanup apply accepted a mismatched reviewed digest")
	}
	if _, err := os.Stat(filepath.Join(external, "state", "dropzone")); err != nil {
		t.Fatalf("symlinked external target was changed: %v", err)
	}
}

func TestRetiredIntakeCleanupRefusesSymlinkedCleanupRootAncestor(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	realRoot := filepath.Join(root, "real")
	linkedRoot := filepath.Join(root, "linked")
	boxRoot := filepath.Join(linkedRoot, "box")
	stateRoot := filepath.Join(root, "box-state")
	home := filepath.Join(root, "home", "loomadmin")
	for _, path := range []string{filepath.Join(realRoot, "box", box.DefaultLaneDirName), stateRoot, home} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatalf("create symlinked cleanup root ancestor: %v", err)
	}

	plan := retiredIntakeTestPlan(boxRoot, stateRoot, home)
	cleanup, err := PlanRetiredIntakeCleanup(plan, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("PlanRetiredIntakeCleanup returned error: %v", err)
	}
	assertRetiredIntakeItemState(t, cleanup, "main_box_lane", RetiredIntakeCleanupSkippedUnsafePath)
	if _, err := os.Stat(filepath.Join(realRoot, "box", box.DefaultLaneDirName)); err != nil {
		t.Fatalf("symlinked cleanup root target was changed: %v", err)
	}
}

func TestWorkspaceSetupPlanHasNoRetiredMainIntakeCleanup(t *testing.T) {
	t.Parallel()

	root := retiredIntakeTempRoot(t)
	plan, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "workspace", NodeRole: "primary_workspace", HomeDir: filepath.Join(root, "home"), BoxPath: filepath.Join(root, "loom-box")},
		Facts: TargetFacts{OS: "darwin", Arch: "arm64", UserName: "tester", HomeDir: filepath.Join(root, "home")},
		Now:   func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.RetiredIntakeCleanup != nil {
		t.Fatalf("workspace plan must not carry Main cleanup inventory: %#v", plan.RetiredIntakeCleanup)
	}
	if area, ok := box.DefaultContract(box.Resolved{RootPath: plan.Paths.BoxPath, Profile: box.ProfileWorkspace}).Areas[box.AreaLane]; !ok || !area.Enabled {
		t.Fatalf("workspace Lane contract was not preserved: %#v", area)
	}
}

func retiredIntakeTestPlan(boxRoot, stateRoot, home string) SetupPlan {
	return SetupPlan{
		SchemaVersion: SchemaVersion,
		Spec: SetupSpec{
			NodeKind:   "main",
			NodeRole:   "main",
			BoxProfile: box.ProfileMain,
			HomeDir:    home,
		},
		Paths: PathPlan{BoxPath: boxRoot, BoxStateRoot: stateRoot},
	}
}

func retiredIntakeTempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test cleanup root: %v", err)
	}
	return root
}

func assertRetiredIntakeItemState(t *testing.T, plan RetiredIntakeCleanupPlan, id, want string) {
	t.Helper()
	for _, item := range plan.Items {
		if item.ID == id {
			if item.State != want {
				t.Fatalf("cleanup item %s state = %q, want %q: %#v", id, item.State, want, item)
			}
			return
		}
	}
	t.Fatalf("cleanup item %s missing from %#v", id, plan.Items)
}

func cleanupResultHasStatus(result RetiredIntakeCleanupResult, id, status string) bool {
	for _, change := range append(append([]RetiredIntakeCleanupChange{}, result.Removed...), result.Skipped...) {
		if change.ID == id && change.Status == status {
			return true
		}
	}
	return false
}
