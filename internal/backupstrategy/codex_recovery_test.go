package backupstrategy

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexCaptureBoundaryAndProtectedInventory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "recovery-codex")
	packagePath := filepath.Join(directory, "codex-fixture")
	if err := os.MkdirAll(packagePath, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(directory, ".staging"), 0700); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "installation_id", Method: zip.Deflate}
	header.SetMode(0440)
	entry, _ := writer.CreateHeader(header)
	entry.Write([]byte("fixture"))
	writer.Close()
	digest, fileHash := sha256.Sum256(data.Bytes()), sha256.Sum256([]byte("fixture"))
	now := time.Now().UTC()
	manifest := map[string]any{"schema": "loom.codex.capture.v1", "id": "codex-fixture", "origin": "/home/agents/.codex", "capture_policy": "sqlite_online_and_complete_file_prefixes", "started_at": now, "created_at": now,
		"payload_size": data.Len(), "payload_sha256": hex.EncodeToString(digest[:]), "entries": []map[string]any{{"path": "installation_id", "size": 7, "sha256": hex.EncodeToString(fileHash[:])}}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(packagePath, "manifest.json"), raw, 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "codex.zip"), data.Bytes(), 0440); err != nil {
		t.Fatal(err)
	}
	roots := []DirectArchiveRoot{{Name: "agents", Path: root}}
	exclusions, err := CodexArchiveBoundary(t.Context(), roots, nil, directory, now.Add(time.Second))
	if err != nil || len(exclusions) != 1 || exclusions[0].RelativePath != "recovery-codex/.staging" {
		t.Fatalf("boundary: %v %v", exclusions, err)
	}
	if _, err := CodexArchiveBoundary(t.Context(), roots, exclusions, directory, now.Add(26*time.Hour)); err == nil {
		t.Fatal("stale evidence accepted")
	}
	if err := os.Chmod(filepath.Join(packagePath, "manifest.json"), 0470); err != nil {
		t.Fatal(err)
	}
	if _, err := CodexArchiveBoundary(t.Context(), roots, nil, directory, now.Add(time.Second)); err == nil {
		t.Fatal("activation-induced writable recovery mode accepted")
	}
	if err := os.Chmod(filepath.Join(packagePath, "manifest.json"), 0440); err != nil {
		t.Fatal(err)
	}
	manifest["payload_sha256"] = strings.Repeat("a", 64)
	raw, _ = json.Marshal(manifest)
	if err := os.Chmod(filepath.Join(packagePath, "manifest.json"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "manifest.json"), raw, 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(packagePath, "manifest.json"), 0440); err != nil {
		t.Fatal(err)
	}
	if _, err := CodexArchiveBoundary(t.Context(), roots, nil, directory, now.Add(time.Second)); err == nil {
		t.Fatal("drift admitted")
	}
	for _, name := range []string{"auth.json", "config.toml", "queue_1.sqlite", "logs_2.sqlite", "packages/bin", "../auth.json", "sessions/../auth.json"} {
		if codexCaptureFileAllowed(name) {
			t.Fatalf("secret/runtime entry allowed: %s", name)
		}
	}
}

func TestCodexRecoveryActualMainCapture(t *testing.T) {
	if os.Getenv("LOOM_TEST_MAIN_CODEX_CAPTURE") != "1" {
		t.Skip("explicit read-only Main capture check not selected")
	}
	roots := []DirectArchiveRoot{{Name: "agents", Path: "/srv/loom/agents"}}
	exclusions, err := CodexArchiveBoundary(t.Context(), roots, nil, CodexRecoveryPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(exclusions) != 1 || exclusions[0].RelativePath != "mina/recovery-codex/.staging" {
		t.Fatalf("unexpected capture staging exclusion: %+v", exclusions)
	}
}
