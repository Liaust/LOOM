package lane

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/filepolicy"
)

func TestLaneTransferProfilesUseOneInventory(t *testing.T) {
	root, lanePath := lanePolicyFixture(t)

	faithful, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileFaithful)
	if err != nil {
		t.Fatalf("faithful plan: %v", err)
	}
	assertLanePlanPaths(t, faithful, true, ".git/config", "node_modules/pkg/index.js", ".venv/bin/python", ".env", ".loomignore")
	assertLanePlanPaths(t, faithful, false, "custom.tmp", ".loom/state/runtime.db", ".loom/tmp/export.tar", ".loom-partial/chunk", "README.md")

	sourceOnly, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileSourceOnly)
	if err != nil {
		t.Fatalf("source-only plan: %v", err)
	}
	assertLanePlanPaths(t, sourceOnly, true, ".git/config", ".env", ".loomignore")
	assertLanePlanPaths(t, sourceOnly, false, "node_modules/pkg/index.js", ".venv/bin/python", "custom.tmp")

	exact, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileExact)
	if err != nil {
		t.Fatalf("exact plan: %v", err)
	}
	assertLanePlanPaths(t, exact, true, ".git/config", "node_modules/pkg/index.js", ".venv/bin/python", "custom.tmp", ".loomignore")
	assertLanePlanPaths(t, exact, false, ".loom/state/runtime.db", ".loom/tmp/export.tar", ".loom-partial/chunk", "README.md")
	if len(exact.Warnings) == 0 || !strings.Contains(exact.Warnings[0], "bypasses .loomignore") {
		t.Fatalf("exact warning missing: %#v", exact.Warnings)
	}
	if faithful.PolicyFingerprint == exact.PolicyFingerprint || faithful.InventoryHash == exact.InventoryHash {
		t.Fatalf("profile-specific plan evidence did not change")
	}
}

func TestLaneTransportRecommendationDoesNotChangeCanonicalInventory(t *testing.T) {
	root, lanePath := lanePolicyFixture(t)
	plan, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileFaithful)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Transport.SchemaVersion != BundlePlanSchemaVersion || plan.Transport.SelectedMode == "" {
		t.Fatalf("transport recommendation missing: %#v", plan.Transport)
	}
	originalHash := plan.InventoryHash
	forced, err := SelectBundleTransport(plan, TransportModeBundleSeed)
	if err != nil {
		t.Fatal(err)
	}
	plan.Transport = forced
	if got := transferInventoryHash(plan); got != originalHash {
		t.Fatalf("transport selection changed inventory hash: got %q want %q", got, originalHash)
	}
	if len(plan.PolicyHashes) == 0 || plan.PolicyFingerprint == "" {
		t.Fatalf("policy evidence was lost: %#v", plan)
	}
}

func TestLaneNestedIgnoreNegationControlsFaithfulInventory(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, "project", ".loomignore"), "node_modules/\n!node_modules/keep/\n!node_modules/keep/keep.js\n")
	mustLaneFile(t, filepath.Join(lanePath, "project", "node_modules", "pkg", "index.js"), "drop")
	mustLaneFile(t, filepath.Join(lanePath, "project", "node_modules", "keep", "keep.js"), "keep")
	plan, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileFaithful)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	assertLanePlanPaths(t, plan, true, "project/.loomignore", "project/node_modules/keep/keep.js")
	assertLanePlanPaths(t, plan, false, "project/node_modules/pkg/index.js")
}

func TestLaneSendDryRunMatchesCompiledPlanAndUsesManifest(t *testing.T) {
	root, lanePath := lanePolicyFixture(t)
	plan, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileSourceOnly)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Send(context.Background(), SendInput{
		RootPath:       root,
		SourceNodeKey:  "macbook",
		DryRun:         true,
		Profile:        filepolicy.ProfileSourceOnly,
		Now:            func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.FileCount != plan.FileCount || result.TotalBytes != plan.TotalBytes || result.InventoryHash != plan.InventoryHash || result.PolicyFingerprint != plan.PolicyFingerprint {
		t.Fatalf("dry run diverged from plan: result=%#v plan=%#v", result, plan)
	}
	var rsyncCommand string
	for _, command := range result.Commands {
		if strings.Contains(command.Name, "rsync") {
			rsyncCommand = strings.Join(command.Args, " ")
		}
	}
	if !strings.Contains(rsyncCommand, "--files-from=<compiled-transfer-plan>") || !strings.Contains(rsyncCommand, "--no-recursive") || strings.Contains(rsyncCommand, "--exclude") {
		t.Fatalf("dry-run rsync does not use compiled inventory: %s", rsyncCommand)
	}
}

func TestWriteTransferPlanFileListDoesNotMarkDirectoriesRecursive(t *testing.T) {
	root, lanePath := lanePolicyFixture(t)
	plan, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileSourceOnly)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "plan.json")
	fileListPath := filepath.Join(filepath.Dir(manifestPath), "plan.files")
	if err := writeTransferPlanFiles(manifestPath, fileListPath, plan); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(fileListPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range bytes.Split(payload, []byte{0}) {
		if len(entry) > 0 && bytes.HasSuffix(entry, []byte("/")) {
			t.Fatalf("compiled directory entry requests recursive expansion: %q", entry)
		}
	}
}

func TestLaneSendRefusesPolicyChangeAfterPlanning(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, ".loomignore"), "*.tmp\n")
	mustLaneFile(t, filepath.Join(lanePath, "send.txt"), "send")
	mustLaneFile(t, filepath.Join(lanePath, "later.tmp"), "later")
	var sawRsync bool
	var policyChanged bool
	_, err := Send(context.Background(), SendInput{
		RootPath:       root,
		SourceNodeKey:  "macbook",
		Now:            func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
		NewBatchID:     func(time.Time) string { return "lane_policy_change" },
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Runner: laneTestRunner(func(_ context.Context, name string, args ...string) (string, string, error) {
			if strings.Contains(name, "rsync") {
				sawRsync = true
			}
			if name == "ssh" && !policyChanged {
				mustLaneFile(t, filepath.Join(lanePath, ".loomignore"), "*.cache\n")
				policyChanged = true
			}
			return "", "", nil
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "policy or inventory changed") {
		t.Fatalf("expected policy re-plan error, got %v", err)
	}
	if sawRsync {
		t.Fatal("rsync ran after policy changed")
	}
}

func TestLaneSourceOnlySafetyCopyAndRsyncManifestMatchPlan(t *testing.T) {
	root, lanePath := lanePolicyFixture(t)
	plan, err := BuildTransferPlan(lanePath, root, filepolicy.ProfileSourceOnly)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result, err := Send(context.Background(), SendInput{
		RootPath:       root,
		SourceNodeKey:  "macbook",
		Profile:        filepolicy.ProfileSourceOnly,
		Now:            func() time.Time { return now },
		NewBatchID:     func(time.Time) string { return "lane_source_only" },
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Runner:         laneTestRunner(nil),
	})
	if err != nil {
		t.Fatalf("source-only send: %v", err)
	}
	if result.FileCount != plan.FileCount || result.TotalBytes != plan.TotalBytes || result.InventoryHash != plan.InventoryHash {
		t.Fatalf("send diverged from source-only plan: result=%#v plan=%#v", result, plan)
	}
	safety := filepath.Join(root, DefaultStateRelPath, "sent", "lane_source_only")
	if _, err := os.Stat(safety); !os.IsNotExist(err) {
		t.Fatalf("successful source-only safety copy was not retired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.LocalCleanupQuarantinePath, ".git", "config")); err != nil {
		t.Fatalf("source-only cleanup quarantine lost .git: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.LocalCleanupQuarantinePath, "node_modules", "pkg", "index.js")); !os.IsNotExist(err) {
		t.Fatalf("source-only cleanup quarantine included dependency: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lanePath, "node_modules", "pkg", "index.js")); err != nil {
		t.Fatalf("ignored dependency should remain locally: %v", err)
	}
	manifestPath := filepath.Join(root, DefaultStateRelPath, "plans", "lane_source_only.json")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted TransferPlan
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != TransferPlanSchemaVersion || persisted.InventoryHash != plan.InventoryHash || persisted.PolicyFingerprint != plan.PolicyFingerprint {
		t.Fatalf("persisted manifest diverged: %#v", persisted)
	}
	fileList, err := os.ReadFile(filepath.Join(root, DefaultStateRelPath, "plans", "lane_source_only.files"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, value := range strings.Split(string(fileList), "\x00") {
		listed[strings.TrimSuffix(value, "/")] = true
	}
	for _, entry := range plan.Entries {
		if !listed[entry.RelativePath] {
			t.Fatalf("rsync file list missing %q", entry.RelativePath)
		}
	}
}

func lanePolicyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, ".loomignore"), "custom.tmp\n")
	for pathValue, content := range map[string]string{
		".git/config":               "git",
		"node_modules/pkg/index.js": "dep",
		".venv/bin/python":          "python",
		".env":                      "TOKEN=reference-only",
		"custom.tmp":                "ignored",
		".loom/state/runtime.db":    "state",
		".loom/tmp/export.tar":      "temp",
		".loom-partial/chunk":       "partial",
		"README.md":                 "lane control",
		"docs/README.md":            "user readme",
	} {
		mustLaneFile(t, filepath.Join(lanePath, filepath.FromSlash(pathValue)), content)
	}
	return root, lanePath
}

func mustLaneFile(t *testing.T, pathValue, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathValue, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertLanePlanPaths(t *testing.T, plan TransferPlan, included bool, paths ...string) {
	t.Helper()
	set := map[string]bool{}
	if included {
		for _, entry := range plan.Entries {
			set[entry.RelativePath] = true
		}
	} else {
		for _, decision := range plan.Ignored {
			set[decision.Path] = true
		}
	}
	for _, pathValue := range paths {
		if !set[pathValue] {
			t.Fatalf("path %q included=%t not found in plan: %#v", pathValue, included, plan)
		}
	}
}
