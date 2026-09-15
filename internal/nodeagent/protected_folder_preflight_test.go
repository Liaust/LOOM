package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
)

func TestProtectedFolderPreflightReportsManagedPolicyWithoutContents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".loomignore"), []byte("private.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "included.txt"), []byte("included-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("private-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := preflightPayload(root, backupcontracts.DefaultPreflightBudget())
	payload.Include = []string{"**/*"}
	result := inspectProtectedFolder(Config{}, Store{DataDir: filepath.Join(t.TempDir(), "state")}, payload)
	if !result.Exists || !result.Directory || !result.Readable || result.CanonicalPath == "" {
		t.Fatalf("incomplete directory evidence: %#v", result)
	}
	if result.Policy.IncludedCount != 2 || result.Policy.IgnoredCount != 1 || result.Policy.Fingerprint == "" || len(result.Policy.PolicyFiles) != 1 {
		t.Fatalf("unexpected policy evidence: %#v", result.Policy)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || containsAny(string(encoded), "included-secret", "private-secret") {
		t.Fatalf("preflight leaked file content: %s", encoded)
	}
}

func TestProtectedFolderPreflightUsesIntegratedManagedPolicy(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".loomignore":               "private.txt\n",
		".git/config":               "git",
		"node_modules/pkg/index.js": "dependency",
		".venv/bin/python":          "environment",
		"venv/bin/python":           "environment",
		".pytest_cache/cache":       "cache",
		"nested/.loomignore":        "*.log\n!keep.log\n",
		"nested/drop.log":           "ignored",
		"nested/keep.log":           "included",
		"private.txt":               "ignored",
	}
	for relative, content := range files {
		pathValue := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(pathValue), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pathValue, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := inspectProtectedFolder(Config{}, Store{DataDir: filepath.Join(t.TempDir(), "state")}, preflightPayload(root, backupcontracts.DefaultPreflightBudget()))
	if hasBlockingFinding(result, "preflight.policy_invalid") || result.Truncated {
		t.Fatalf("managed policy preflight failed: %#v", result)
	}
	if result.Policy.Profile != "managed" || len(result.Policy.PolicyFiles) != 2 || result.Policy.PolicyFiles[0] != ".loomignore" || result.Policy.PolicyFiles[1] != "nested/.loomignore" {
		t.Fatalf("policy discovery was not visible: %#v", result.Policy)
	}
	if result.Policy.IncludedCount != 4 || result.Policy.IgnoredCount != 6 {
		t.Fatalf("managed policy counts = included %d ignored %d; .git must be included and generated environments ignored: %#v", result.Policy.IncludedCount, result.Policy.IgnoredCount, result.Policy)
	}
}

func TestProtectedFolderPreflightBlocksMalformedLoomignore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".loomignore"), []byte("[bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := inspectProtectedFolder(Config{}, Store{DataDir: filepath.Join(t.TempDir(), "state")}, preflightPayload(root, backupcontracts.DefaultPreflightBudget()))
	if !hasBlockingFinding(result, "preflight.policy_invalid") {
		t.Fatalf("malformed .loomignore was not an explicit blocker: %#v", result)
	}
}

func TestProtectedFolderPreflightBlocksUnsafePathsAndOverlaps(t *testing.T) {
	stateDir := t.TempDir()
	filePath := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	generated := filepath.Join(t.TempDir(), "loom-storage", "Backups")
	if err := os.MkdirAll(generated, 0o700); err != nil {
		t.Fatal(err)
	}
	safeRoot := t.TempDir()
	tests := []struct {
		name, path string
		config     Config
		code       string
	}{
		{name: "root", path: string(filepath.Separator), code: "preflight.forbidden_root"},
		{name: "file", path: filePath, code: "preflight.not_directory"},
		{name: "missing", path: missing, code: "preflight.path_missing"},
		{name: "runtime", path: stateDir, code: "preflight.forbidden_root"},
		{name: "generated", path: generated, code: "preflight.forbidden_root"},
		{name: "safe-root", path: filepath.Join(safeRoot, "child"), config: Config{Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("existing", safeRoot)}}}, code: "preflight.root_overlap"},
	}
	if err := os.MkdirAll(filepath.Join(safeRoot, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := inspectProtectedFolder(test.config, Store{DataDir: stateDir}, preflightPayload(test.path, backupcontracts.DefaultPreflightBudget()))
			if !hasBlockingFinding(result, test.code) {
				t.Fatalf("missing blocking %s finding: %#v", test.code, result)
			}
		})
	}
}

func TestProtectedFolderRecheckAllowsOnlyItsExactManagedRootAndRuntime(t *testing.T) {
	base := t.TempDir()
	store := Store{ConfigPath: filepath.Join(base, "config.json"), StatePath: filepath.Join(base, "state.json"), DataDir: filepath.Join(base, "data")}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	config := Config{MainURL: "http://main.test", NodeKey: "workspace-test", DisplayName: "Workspace Test", Filesystem: filesystemconnector.Config{}}
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	photosPath := t.TempDir()
	otherPath := t.TempDir()
	photos := desiredRootFixture(t, "photos", photosPath, 10*1024*1024)
	other := desiredRootFixture(t, "documents", otherPath, 10*1024*1024)
	desired := reconcilePayload(t, "node_test", 1, []backupcontracts.ProtectedFolderDesiredRoot{photos, other})
	if outcome := reconcileProtectedFolders(store, config, desired); outcome.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("setup reconcile = %#v", outcome)
	}
	config = mustLoadNodeConfig(t, store)

	identity := backupcontracts.RecheckIdentityForContractKey("photos")
	payload := preflightPayload(photosPath, backupcontracts.DefaultPreflightBudget())
	payload.Recheck = &identity
	result := inspectProtectedFolder(config, store, payload)
	if hasBlockingFinding(result, "preflight.root_overlap") || !result.Readable {
		t.Fatalf("active contract could not recheck itself: %#v", result)
	}

	firstTime := payload
	firstTime.Recheck = nil
	if result := inspectProtectedFolder(config, store, firstTime); !hasBlockingFinding(result, "preflight.root_overlap") {
		t.Fatalf("anonymous preflight weakened overlap safety: %#v", result)
	}

	crossContract := payload
	crossContract.RequestedPath = otherPath
	if result := inspectProtectedFolder(config, store, crossContract); !hasBlockingFinding(result, "preflight.root_overlap") {
		t.Fatalf("cross-contract overlap was accepted: %#v", result)
	}

	if err := noderuntime.NewStore(store.DataDir).DeleteInstance(identity.WorkerKey); err != nil {
		t.Fatal(err)
	}
	if result := inspectProtectedFolder(config, store, payload); !hasBlockingFinding(result, "preflight.root_overlap") {
		t.Fatalf("missing exact managed runtime still bypassed overlap: %#v", result)
	}
}

func TestProtectedFolderPreflightDoesNotFollowSymlinksAndExposesTruncation(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	budget := backupcontracts.DefaultPreflightBudget()
	budget.MaxEntries = 2
	result := inspectProtectedFolder(Config{}, Store{DataDir: filepath.Join(t.TempDir(), "state")}, preflightPayload(root, budget))
	if !result.Truncated || !hasFinding(result, "preflight.scan_truncated") {
		t.Fatalf("expected explicit truncation: %#v", result)
	}
	if result.FileCount > 2 {
		t.Fatalf("walk exceeded entry budget: %#v", result)
	}

	symlinkRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	symlinkResult := inspectProtectedFolder(Config{}, Store{DataDir: filepath.Join(t.TempDir(), "state")}, preflightPayload(symlinkRoot, backupcontracts.DefaultPreflightBudget()))
	if !hasBlockingFinding(symlinkResult, "preflight.symlink_root") {
		t.Fatalf("symlink root was accepted: %#v", symlinkResult)
	}
}

func TestProtectedFolderPreflightMessageProducesStructuredAck(t *testing.T) {
	root := t.TempDir()
	payload := preflightPayload(root, backupcontracts.DefaultPreflightBudget())
	payload.TargetNode = "node_test"
	encoded, _ := json.Marshal(payload)
	message := communication.Message{CommunicationMessageID: "message_test", NodeID: "node_test", Kind: communication.KindProtectedFolderPreflight, PayloadJSON: encoded}
	ack := buildProtectedFolderPreflightAck(Config{NodeKey: "workspace-test"}, State{NodeID: "node_test"}, Store{DataDir: filepath.Join(t.TempDir(), "state")}, message)
	if ack.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("ack = %#v", ack)
	}
	var result backupcontracts.PreflightResult
	if err := json.Unmarshal(ack.ResultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != backupcontracts.ProtectedFolderPreflightResultVersion {
		t.Fatalf("result = %#v", result)
	}
}

func preflightPayload(pathValue string, budget backupcontracts.PreflightBudget) backupcontracts.ProtectedFolderPreflightPayload {
	return backupcontracts.ProtectedFolderPreflightPayload{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, PreflightID: "backup_preflight_test", TargetNode: "node_test", RequestedPath: pathValue, Ignore: backupcontracts.IgnorePolicy{Profile: "managed", DiscoverUserRules: true}, Budget: budget, ExpiresAt: time.Now().Add(time.Minute)}
}

func hasFinding(result backupcontracts.PreflightResult, code string) bool {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
func hasBlockingFinding(result backupcontracts.PreflightResult, code string) bool {
	for _, finding := range result.Findings {
		if finding.Code == code && finding.Blocking {
			return true
		}
	}
	return false
}
func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
