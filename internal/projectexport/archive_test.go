package projectexport

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestProjectExportModeInventories(t *testing.T) {
	root := exportFixture(t, true)
	tests := []struct {
		mode   Mode
		want   []string
		absent []string
	}{
		{ModeHuman,
			[]string{".loomignore", "README.md", "nested/", "nested/keep.txt"},
			[]string{".loom/", ".loom/project.yaml", ".loom-acceptance/", "AGENTS.md", "ignored.txt", "nested/drop.txt"}},
		{ModePortable,
			[]string{".loom-acceptance/", ".loom-acceptance/probe", ".loom/", ".loom/.gitignore", ".loom/agents/", ".loom/agents/project.md", ".loom/project.yaml", ".loom/tools/", ".loom/tools/check.sh", ".loomignore", "AGENTS.md", "README.md", "nested/", "nested/keep.txt"},
			[]string{".loom/state/", ".loom/state/cache", ".loom/tmp/", ".loom/tmp/work", "ignored.txt", "nested/drop.txt"}},
		{ModeArchival,
			[]string{".loom-acceptance/", ".loom-acceptance/probe", ".loom/", ".loom/.gitignore", ".loom/agents/", ".loom/agents/project.md", ".loom/export/registrations.json", ".loom/project.yaml", ".loom/tools/", ".loom/tools/check.sh", ".loomignore", "AGENTS.md", "README.md", "nested/", "nested/keep.txt"},
			[]string{".loom/state/", ".loom/tmp/", "ignored.txt", "nested/drop.txt"}},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: test.mode, RegistrationReferences: RegistrationReferences{ProjectRef: "export-fixture", ContractHash: "sha256:abc"}})
			if err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			result, err := WriteArchive(context.Background(), &archive, plan)
			if err != nil {
				t.Fatal(err)
			}
			got := tarInventory(t, archive.Bytes())
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("inventory:\n got %#v\nwant %#v", got, test.want)
			}
			for _, path := range test.absent {
				if contains(got, path) {
					t.Fatalf("unexpected archive path %q", path)
				}
			}
			if result.PolicyVersion == "" || result.PolicyFingerprint == "" || result.ArchiveChecksum == "" || result.ArchiveBytes == 0 {
				t.Fatalf("incomplete summary: %#v", result)
			}
		})
	}
}

func TestHumanExportPreservesCustomRootAgents(t *testing.T) {
	root := exportFixture(t, false)
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModeHuman})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(entryNames(plan.Entries), "AGENTS.md") {
		t.Fatal("custom root AGENTS.md was not preserved")
	}
}

func TestProjectExportIsDeterministic(t *testing.T) {
	root := exportFixture(t, false)
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable})
	if err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	firstResult, err := WriteArchive(context.Background(), &first, plan)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := WriteArchive(context.Background(), &second, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || firstResult.ArchiveChecksum != secondResult.ArchiveChecksum {
		t.Fatal("archive output is not deterministic")
	}
}

func TestProjectExportRejectsPolicyOrContentChangeAfterPlan(t *testing.T) {
	root := exportFixture(t, false)
	policyPlan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable})
	if err != nil {
		t.Fatal(err)
	}
	writeExportFile(t, filepath.Join(root, ".loomignore"), "different.txt\n")
	if _, err := WriteArchive(context.Background(), io.Discard, policyPlan); err == nil || !strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("policy change error = %v", err)
	}

	root = exportFixture(t, false)
	contentPlan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable})
	if err != nil {
		t.Fatal(err)
	}
	writeExportFile(t, filepath.Join(root, "README.md"), "READ ME")
	if _, err := WriteArchive(context.Background(), io.Discard, contentPlan); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("content change error = %v", err)
	}
}

func TestProjectExportRejectsSymlinkEscapeAndTraversal(t *testing.T) {
	root := exportFixture(t, false)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeExportFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable}); err == nil || !strings.Contains(err.Error(), "escapes file-policy root") {
		t.Fatalf("symlink escape error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable})
	if err != nil {
		t.Fatal(err)
	}
	plan.Entries = []Entry{{RelativePath: "../escape", Kind: "file", Mode: 0o644, Generated: []byte("x"), Size: 1}}
	if _, err := WriteArchive(context.Background(), io.Discard, plan); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal error = %v", err)
	}
	plan.Entries = []Entry{{RelativePath: "link", Kind: "symlink", Mode: 0o777, LinkTarget: "../outside"}}
	if _, err := WriteArchive(context.Background(), io.Discard, plan); err == nil || !strings.Contains(err.Error(), "unsafe project export symlink") {
		t.Fatalf("archive symlink traversal error = %v", err)
	}
}

func TestProjectExportRewritesSafeSymlinkAsPortableRelativeTarget(t *testing.T) {
	root := exportFixture(t, false)
	if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, "readme-link")); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range plan.Entries {
		if entry.RelativePath == "readme-link" {
			found = true
			if entry.Kind != "symlink" || entry.LinkTarget != "README.md" {
				t.Fatalf("unsafe or non-portable symlink entry: %#v", entry)
			}
		}
	}
	if !found {
		t.Fatal("safe symlink was not included")
	}
}

func TestProjectExportBoundsFinalArchiveBytes(t *testing.T) {
	root := exportFixture(t, false)
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModeHuman, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteArchive(context.Background(), io.Discard, plan); err == nil || !strings.Contains(err.Error(), "archive exceeds 1024 bytes") {
		t.Fatalf("archive bound error = %v", err)
	}
}

func TestProjectExportOverwriteAndCancellation(t *testing.T) {
	root := exportFixture(t, false)
	output := filepath.Join(t.TempDir(), "project.tar")
	writeExportFile(t, output, "existing")
	if _, err := ExportToFile(context.Background(), root, output, false, PlanOptions{Mode: ModePortable}); err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("overwrite error = %v", err)
	}
	if got := readExportFile(t, output); got != "existing" {
		t.Fatalf("existing output changed: %q", got)
	}
	if _, err := ExportToFile(context.Background(), root, output, true, PlanOptions{Mode: ModePortable}); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	other := filepath.Join(filepath.Dir(output), "cancelled.tar")
	if _, err := ExportToFile(cancelled, root, other, false, PlanOptions{Mode: ModePortable}); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("cancelled output exists: %v", err)
	}
	partials, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".cancelled.tar.partial-*"))
	if err != nil || len(partials) != 0 {
		t.Fatalf("partial outputs = %#v, err=%v", partials, err)
	}
}

func TestArchivalManifestContainsReferencesButNoCredentialValues(t *testing.T) {
	root := exportFixture(t, false)
	writeExportFile(t, filepath.Join(root, "credentials.yaml"), "ref: pass://loom/example\n")
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModeArchival, RegistrationReferences: RegistrationReferences{ProjectRef: "export-fixture", ProjectID: "project_1", RegistrationRefs: []string{"watch:watch_1"}}})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := WriteArchive(context.Background(), &archive, plan); err != nil {
		t.Fatal(err)
	}
	files := untarFiles(t, archive.Bytes())
	manifest := string(files[ArchiveManifestPath])
	if !strings.Contains(manifest, "project_1") || strings.Contains(manifest, "pass://") {
		t.Fatalf("unexpected manifest: %s", manifest)
	}
	if !strings.Contains(string(files["credentials.yaml"]), "pass://loom/example") {
		t.Fatal("credential reference was not preserved as source data")
	}
}

func TestArchivalManifestReplacesReservedSourcePath(t *testing.T) {
	root := exportFixture(t, false)
	writeExportFile(t, filepath.Join(root, filepath.FromSlash(ArchiveManifestPath)), `{"stale":"source copy"}`)
	plan, err := PlanProject(context.Background(), root, PlanOptions{
		Mode: ModeArchival,
		RegistrationReferences: RegistrationReferences{
			ProjectRef: "export-fixture",
			ProjectID:  "project_fresh",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestEntries := 0
	for _, entry := range plan.Entries {
		if entry.RelativePath == ArchiveManifestPath {
			manifestEntries++
			if entry.Generated == nil || entry.Decision.Pattern != "archival-registration-manifest" {
				t.Fatalf("reserved path was not replaced by generated manifest: %#v", entry)
			}
		}
	}
	if manifestEntries != 1 {
		t.Fatalf("planned manifest entries = %d, want 1", manifestEntries)
	}
	foundReplacementDecision := false
	for _, entry := range plan.Ignored {
		if entry.RelativePath == ArchiveManifestPath && entry.Decision.Pattern == "reserved-generated-archival-manifest" {
			foundReplacementDecision = true
			break
		}
	}
	if !foundReplacementDecision {
		t.Fatalf("reserved source replacement is not visible in ignored plan: %#v", plan.Ignored)
	}

	var archive bytes.Buffer
	if _, err := WriteArchive(context.Background(), &archive, plan); err != nil {
		t.Fatal(err)
	}
	inventory := tarInventory(t, archive.Bytes())
	manifestPaths := 0
	for _, name := range inventory {
		if name == ArchiveManifestPath {
			manifestPaths++
		}
	}
	if manifestPaths != 1 {
		t.Fatalf("archive manifest paths = %d, inventory=%#v", manifestPaths, inventory)
	}
	manifest := string(untarFiles(t, archive.Bytes())[ArchiveManifestPath])
	if !strings.Contains(manifest, "project_fresh") || strings.Contains(manifest, "source copy") {
		t.Fatalf("archive did not contain the fresh generated manifest: %s", manifest)
	}
}

func TestWriteArchiveRejectsDuplicatePaths(t *testing.T) {
	root := exportFixture(t, false)
	plan, err := PlanProject(context.Background(), root, PlanOptions{Mode: ModePortable})
	if err != nil {
		t.Fatal(err)
	}
	plan.Entries = append(plan.Entries, plan.Entries[0])
	if _, err := WriteArchive(context.Background(), io.Discard, plan); err == nil || !strings.Contains(err.Error(), "duplicate project export archive path") {
		t.Fatalf("duplicate archive path error = %v", err)
	}
}

func exportFixture(t *testing.T, managedAgents bool) string {
	t.Helper()
	root := t.TempDir()
	writeExportFile(t, filepath.Join(root, ".loom/project.yaml"), "kind: loom.project\nschema_version: project.contract.v0.3\nproject:\n  slug: export-fixture\n  name: Export Fixture\n  owner_node: main\n")
	writeExportFile(t, filepath.Join(root, ".loom/.gitignore"), "state/\ntmp/\n")
	writeExportFile(t, filepath.Join(root, ".loom/agents/project.md"), "agent guidance\n")
	writeExportFile(t, filepath.Join(root, ".loom/tools/check.sh"), "#!/bin/sh\n")
	writeExportFile(t, filepath.Join(root, ".loom/state/cache"), "runtime")
	writeExportFile(t, filepath.Join(root, ".loom/tmp/work"), "runtime")
	writeExportFile(t, filepath.Join(root, ".loom-acceptance/probe"), "runtime")
	writeExportFile(t, filepath.Join(root, ".loomignore"), "ignored.txt\nnested/*\n!nested/keep.txt\n")
	agents := "custom agent guidance\n"
	if managedAgents {
		agents = "# Agent\n" + ManagedAgentsMarker + "\n"
	}
	writeExportFile(t, filepath.Join(root, "AGENTS.md"), agents)
	writeExportFile(t, filepath.Join(root, "README.md"), "read me")
	writeExportFile(t, filepath.Join(root, "ignored.txt"), "ignored")
	writeExportFile(t, filepath.Join(root, "nested/drop.txt"), "drop")
	writeExportFile(t, filepath.Join(root, "nested/keep.txt"), "keep")
	return root
}

func tarInventory(t *testing.T, payload []byte) []string {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(payload))
	paths := []string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, header.Name)
	}
	sort.Strings(paths)
	return paths
}

func untarFiles(t *testing.T, payload []byte) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(payload))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			result[header.Name] = content
		}
	}
	return result
}

func entryNames(entries []Entry) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.RelativePath)
	}
	return result
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func writeExportFile(t *testing.T, pathValue, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathValue, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
func readExportFile(t *testing.T, pathValue string) string {
	t.Helper()
	content, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
