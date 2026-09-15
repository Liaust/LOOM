package workflows

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifestAcceptsExecutableWorkflow(t *testing.T) {
	manifest, err := ParseManifest([]byte(validWorkflowManifest()))
	if err != nil {
		t.Fatalf("ParseManifest returned error: %v", err)
	}
	if manifest.Workflow.ID != "example_workflow" {
		t.Fatalf("workflow id = %q", manifest.Workflow.ID)
	}
	if manifest.Workflow.Version != "0.1.0" {
		t.Fatalf("workflow version = %q", manifest.Workflow.Version)
	}
	if manifest.Execution.TimeoutSeconds != 60 {
		t.Fatalf("timeout = %d", manifest.Execution.TimeoutSeconds)
	}
}

func TestParseManifestDefaultsVersion(t *testing.T) {
	payload := strings.Replace(validWorkflowManifest(), "  version: 0.1.0\n", "", 1)
	manifest, err := ParseManifest([]byte(payload))
	if err != nil {
		t.Fatalf("ParseManifest returned error: %v", err)
	}
	if manifest.Workflow.Version != "0.1.0" {
		t.Fatalf("workflow version = %q, want default 0.1.0", manifest.Workflow.Version)
	}
}

func TestParseManifestRejectsPlaceholderWorkflow(t *testing.T) {
	payload := strings.Replace(validWorkflowManifest(), "  kind: workflow", "  kind: placeholder", 1)
	if _, err := ParseManifest([]byte(payload)); err == nil {
		t.Fatal("expected placeholder workflow to be rejected by executable workflow registry")
	}
}

func TestParseManifestRejectsUnsafeArtifactPath(t *testing.T) {
	payload := strings.Replace(validWorkflowManifest(), "artifacts: []", "artifacts:\n  - key: leak\n    path: ../outside.txt\n    type: file", 1)
	if _, err := ParseManifest([]byte(payload)); err == nil {
		t.Fatal("expected unsafe artifact path to be rejected")
	}
}

func TestHashPackageChangesWithWorkflowFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "loom.workflow.yaml"), []byte(validWorkflowManifest()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/usr/bin/env bash\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := HashPackage(root)
	if err != nil {
		t.Fatalf("HashPackage returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/usr/bin/env bash\necho changed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := HashPackage(root)
	if err != nil {
		t.Fatalf("HashPackage returned error: %v", err)
	}
	if first == second {
		t.Fatal("content hash should change after package file changes")
	}
}

func validWorkflowManifest() string {
	return `kind: loom.workflow
schema_version: workflow.contract.v0.3.1
workflow:
  id: example_workflow
  name: Example Workflow
  status: active
  version: 0.1.0
implementation:
  kind: workflow
entrypoint:
  command:
    - ./run.sh
runtime:
  shell: bash
execution:
  timeout_seconds: 60
  network: false
  filesystem:
    mode: read_only
inputs:
  schema:
    type: object
outputs:
  schema:
    type: object
artifacts: []
usage_documents:
  - path: README.md
metadata:
  test: true
`
}
