package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifestYAML = `
kind: loom.script
id: word_count
name: Word Count
version: 0.1.0
description: Count words in one text object.
entrypoint:
  command: ["python3", "word_count.py"]
runtime:
  type: python
inputs:
  type: object
outputs:
  type: object
execution:
  timeout_seconds: 60
  network: false
  filesystem:
    read:
      - input_objects
    write:
      - job_output_dir
      - job_artifact_dir
artifacts:
  - key: report
    path: artifacts/report.md
    type: text_report
    title: Word Count Report
usage_documents:
  - path: docs/usage.md
    target: capability
`

func TestParseManifestAcceptsValidManifest(t *testing.T) {
	manifest, err := ParseManifest([]byte(validManifestYAML))
	if err != nil {
		t.Fatalf("ParseManifest returned error: %v", err)
	}
	if manifest.ID != "word_count" {
		t.Fatalf("manifest ID = %q", manifest.ID)
	}
	if len(manifest.Entrypoint.Command) != 2 {
		t.Fatalf("expected entrypoint command with 2 parts, got %d", len(manifest.Entrypoint.Command))
	}
	hash, err := HashManifest(manifest)
	if err != nil {
		t.Fatalf("HashManifest returned error: %v", err)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("expected sha256 manifest hash, got %q", hash)
	}
}

func TestParseManifestRejectsInvalidKind(t *testing.T) {
	payload := strings.Replace(validManifestYAML, "kind: loom.script", "kind: loom.skill", 1)
	if _, err := ParseManifest([]byte(payload)); err == nil {
		t.Fatal("ParseManifest accepted skill kind")
	}
}

func TestParseManifestRejectsMissingEntrypoint(t *testing.T) {
	payload := `
kind: loom.script
id: word_count
name: Word Count
version: 0.1.0
execution:
  timeout_seconds: 60
`
	if _, err := ParseManifest([]byte(payload)); err == nil {
		t.Fatal("ParseManifest accepted missing entrypoint")
	}
}

func TestParseManifestRejectsShellStringCommand(t *testing.T) {
	payload := strings.Replace(validManifestYAML, `command: ["python3", "word_count.py"]`, `command: "python3 word_count.py"`, 1)
	if _, err := ParseManifest([]byte(payload)); err == nil {
		t.Fatal("ParseManifest accepted shell string command")
	}
}

func TestParseManifestRejectsArtifactPathTraversal(t *testing.T) {
	payload := strings.Replace(validManifestYAML, "path: artifacts/report.md", "path: ../report.md", 1)
	if _, err := ParseManifest([]byte(payload)); err == nil {
		t.Fatal("ParseManifest accepted artifact path traversal")
	}
}

func TestHashPackageIsDeterministicAndContentSensitive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "loom.script.yaml"), validManifestYAML)
	writeFile(t, filepath.Join(root, "word_count.py"), "print('words')\n")
	writeFile(t, filepath.Join(root, "tmp", "ignored.txt"), "ignored\n")

	first, err := HashPackage(root)
	if err != nil {
		t.Fatalf("HashPackage returned error: %v", err)
	}
	second, err := HashPackage(root)
	if err != nil {
		t.Fatalf("HashPackage returned error: %v", err)
	}
	if first != second {
		t.Fatalf("package hash was not deterministic: %s != %s", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("expected sha256 package hash, got %q", first)
	}

	writeFile(t, filepath.Join(root, "word_count.py"), "print('changed')\n")
	changed, err := HashPackage(root)
	if err != nil {
		t.Fatalf("HashPackage returned error: %v", err)
	}
	if changed == first {
		t.Fatal("package hash did not change after package content changed")
	}
}

func writeFile(t *testing.T, path string, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
