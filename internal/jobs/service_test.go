package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseResultFileAllowsMissingFile(t *testing.T) {
	result, err := parseResultFile(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("parseResultFile returned error: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %q", result.Status)
	}
	if len(result.Outputs) != 0 {
		t.Fatalf("expected empty outputs, got %d", len(result.Outputs))
	}
}

func TestParseResultFileReadsOutputsAndArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	payload := []byte(`{
		"status":"ok",
		"outputs":{"word_count":42},
		"artifacts":[{"key":"report","path":"artifacts/report.md","type":"text_report"}]
	}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := parseResultFile(path)
	if err != nil {
		t.Fatalf("parseResultFile returned error: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("expected ok status, got %q", result.Status)
	}
	if got := inferOutputType(result.Outputs["word_count"]); got != "number" {
		t.Fatalf("expected number output type, got %q", got)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Key != "report" {
		t.Fatalf("unexpected artifacts: %#v", result.Artifacts)
	}
}

func TestParseResultFileRejectsArtifactWithoutKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, []byte(`{"artifacts":[{"path":"artifacts/report.md"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := parseResultFile(path); err == nil {
		t.Fatal("expected artifact without key to be rejected")
	}
}

func TestExecutableEnvironmentIncludesWorkflowVars(t *testing.T) {
	workflowID := "workflow_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	versionID := "workflow_version_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	claim := JobClaim{
		Job: Job{
			JobID:             "job_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			JobType:           TypeWorkflowRun,
			WorkflowID:        &workflowID,
			WorkflowVersionID: &versionID,
			InputJSON:         []byte(`{"input":{"ok":true}}`),
		},
		Attempt: Attempt{AttemptNumber: 2},
		Package: ExecutablePackage{
			Slug: "workflow-smoke",
		},
	}
	paths := runPaths{
		InputDir:    "/tmp/input",
		OutputDir:   "/tmp/output",
		ArtifactDir: "/tmp/artifacts",
		TempDir:     "/tmp/temp",
		ResultFile:  "/tmp/result.json",
	}

	env, err := executableEnvironment(claim, paths, "")
	if err != nil {
		t.Fatalf("executableEnvironment returned error: %v", err)
	}
	for _, want := range []string{
		"LOOM_JOB_TYPE=workflow_run",
		"LOOM_ATTEMPT=2",
		"LOOM_WORKFLOW_ID=" + workflowID,
		"LOOM_WORKFLOW_VERSION_ID=" + versionID,
		"LOOM_WORKFLOW_SLUG=workflow-smoke",
	} {
		if !envContains(env, want) {
			t.Fatalf("environment missing %q in %#v", want, env)
		}
	}
}

func TestExecutableEnvironmentInjectsCredentialBindings(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN_SOURCE", "secret-token")
	metadata := jobMetadata("", "", "", "test", []CredentialBinding{{
		Ref:      "telegram.bot_token",
		Kind:     "env",
		ExposeAs: "TELEGRAM_BOT_TOKEN",
		Source: CredentialBindingSource{
			Kind: "env",
			Env:  "TELEGRAM_BOT_TOKEN_SOURCE",
		},
	}})
	claim := JobClaim{
		Job: Job{
			JobID:     "job_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			JobType:   TypeScriptRun,
			InputJSON: []byte(`{}`),
			Metadata:  metadata,
		},
		Attempt: Attempt{AttemptNumber: 1},
	}
	paths := runPaths{
		InputDir:    "/tmp/input",
		OutputDir:   "/tmp/output",
		ArtifactDir: "/tmp/artifacts",
		TempDir:     "/tmp/temp",
		ResultFile:  "/tmp/result.json",
	}

	env, err := executableEnvironment(claim, paths, "")
	if err != nil {
		t.Fatalf("executableEnvironment returned error: %v", err)
	}
	if !envContains(env, "TELEGRAM_BOT_TOKEN=secret-token") {
		t.Fatalf("environment missing injected credential in %#v", env)
	}
	if strings.Contains(string(metadata), "secret-token") {
		t.Fatalf("job metadata leaked secret value: %s", metadata)
	}
}

func TestProgressSourceKindUsesSpecificRunKinds(t *testing.T) {
	if got := progressSourceKind(TypeScriptRun); got != "script_run" {
		t.Fatalf("script progress source = %q", got)
	}
	if got := progressSourceKind(TypeWorkflowRun); got != "workflow_run" {
		t.Fatalf("workflow progress source = %q", got)
	}
}

func envContains(env []string, want string) bool {
	for _, item := range env {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}
