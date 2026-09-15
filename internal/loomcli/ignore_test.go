package loomcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/filepolicy"
)

func TestIgnoreInspectHumanAndJSONAgree(t *testing.T) {
	root := t.TempDir()
	writeIgnoreCLITestFile(t, filepath.Join(root, ".loomignore"), "custom.bin\n")
	writeIgnoreCLITestFile(t, filepath.Join(root, "keep.txt"), "keep")
	writeIgnoreCLITestFile(t, filepath.Join(root, "custom.bin"), "custom")
	writeIgnoreCLITestFile(t, filepath.Join(root, "node_modules", "pkg.js"), "dependency")

	human, stderr, err := executeRootCommand("ignore", "inspect", root, "--operation", "backup")
	if err != nil {
		t.Fatalf("human inspect: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"profile=managed", "Included: files=2 bytes=", "Ignored: files=2 bytes=", "reconstructible=1", "user=1"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human output missing %q:\n%s", want, human)
		}
	}

	stdout, stderr, err := executeRootCommand("--json", "ignore", "inspect", root, "--operation", "backup")
	if err != nil {
		t.Fatalf("json inspect: %v stderr=%s", err, stderr)
	}
	var report filepolicy.InspectionReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode JSON: %v output=%s", err, stdout)
	}
	if report.Profile != filepolicy.ProfileManaged || report.Included.Count != 2 || report.Included.Bytes != 15 || report.Ignored.Count != 2 || report.Ignored.Bytes != 16 || report.IgnoredBySource[filepolicy.RuleCategoryUser] != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestIgnoreExplainJSONGolden(t *testing.T) {
	root := t.TempDir()
	stdout, stderr, err := executeRootCommand("--json", "ignore", "explain", "ordinary.txt", "--root", root, "--operation", "lane")
	if err != nil {
		t.Fatalf("explain: %v stderr=%s", err, stderr)
	}
	var report ignoreExplanation
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode explain JSON: %v output=%s", err, stdout)
	}
	report.Root = "$ROOT"
	report.PolicyFingerprint = "$FINGERPRINT"
	stable, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"root":"$ROOT","path":"ordinary.txt","operation":"lane","profile":"faithful","policy_version":"loom-file-policy-v1","policy_fingerprint":"$FINGERPRINT","resolution":{"decision":{"path":"ordinary.txt","included":true,"profile":"faithful","rule_category":"none","policy_version":"loom-file-policy-v1"},"trace":[{"path":"ordinary.txt","included":true,"profile":"faithful","rule_category":"none","policy_version":"loom-file-policy-v1"}]}}`
	if string(stable) != want {
		t.Fatalf("stable JSON changed:\n got %s\nwant %s", stable, want)
	}
}

func TestIgnoreExplainStableJSONTrace(t *testing.T) {
	root := t.TempDir()
	writeIgnoreCLITestFile(t, filepath.Join(root, ".loomignore"), "*.tmp\n!keep.tmp\n")
	stdout, stderr, err := executeRootCommand("--json", "ignore", "explain", "keep.tmp", "--root", root, "--operation", "lane")
	if err != nil {
		t.Fatalf("explain: %v stderr=%s", err, stderr)
	}
	var report ignoreExplanation
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode explain JSON: %v output=%s", err, stdout)
	}
	if report.Profile != filepolicy.ProfileFaithful || !report.Resolution.Decision.Included || len(report.Resolution.Trace) != 3 || report.Resolution.Decision.SourceLine != 2 {
		t.Fatalf("unexpected explanation: %#v", report)
	}
}

func TestIgnoreInspectReportsErrorsAndTruncation(t *testing.T) {
	root := t.TempDir()
	writeIgnoreCLITestFile(t, filepath.Join(root, "a.txt"), "a")
	writeIgnoreCLITestFile(t, filepath.Join(root, "b.txt"), "b")
	stdout, stderr, err := executeRootCommand("--json", "ignore", "inspect", root, "--max-entries", "1")
	if err != nil {
		t.Fatalf("truncated inspect: %v stderr=%s", err, stderr)
	}
	var report filepolicy.InspectionReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || !report.Truncated {
		t.Fatalf("truncation report=%#v decodeErr=%v output=%s", report, err, stdout)
	}

	writeIgnoreCLITestFile(t, filepath.Join(root, ".loomignore"), "[bad\n")
	stdout, _, err = executeRootCommand("--json", "ignore", "inspect", root)
	if err == nil {
		t.Fatal("expected malformed policy error")
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || len(report.Errors) == 0 || !strings.Contains(report.Errors[0], "malformed") {
		t.Fatalf("error report=%#v decodeErr=%v output=%s", report, err, stdout)
	}
}

func writeIgnoreCLITestFile(t *testing.T, pathValue, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathValue, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
