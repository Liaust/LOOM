package workspacefiles

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/filepolicy"
)

func explainBoxFixture(t *testing.T) (*Service, string, box.Contract) {
	t.Helper()
	s, root := filesFixture(t)
	contract := box.DefaultContract(box.Resolved{RootPath: root, OwnerNode: "main", Profile: box.ProfileMain})
	writeExplainBox(t, root, contract)
	return s, root, contract
}

func writeExplainBox(t *testing.T, root string, contract box.Contract) {
	t.Helper()
	raw, err := yaml.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".loom/box.yaml", string(raw))
}

func TestExplainCurrentUnregisteredFileAndSerializedQualifiers(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "fresh/unindexed.pdf", "actual bytes")
	got, err := s.Explain(context.Background(), "box", "fresh/unindexed.pdf")
	if err != nil || got.Source.Kind != "file" || got.SourcePath != filepath.Join(root, "fresh/unindexed.pdf") {
		t.Fatalf("physical source: %+v %v", got, err)
	}
	if got.Intent.State != "unknown" || got.Intent.Search != "unknown" || got.Intent.Enrichment != nil || got.Intent.Protect != nil || !strings.Contains(got.Intent.Reason, "defaults were not applied") {
		t.Fatalf("invented policy: %+v", got.Intent)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"root_registration":"unknown"`, `"device_local_availability":"unknown"`, `"applied_config":"not_checked"`, `"backup_coverage":"not_verified"`, `"verification":"not_checked"`, `"protect":null`, `"enrichment":null`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("missing qualifier %s in %s", field, raw)
		}
	}
	for _, name := range []string{".loom", "fresh/.loom"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("explain wrote a descriptor: %s %v", name, err)
		}
	}
	if _, err := s.Explain(context.Background(), "box", "missing.pdf"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing original: %v", err)
	}
}

func TestExplainBoxInheritedFieldsHaveIndependentExactSources(t *testing.T) {
	s, root, contract := explainBoxFixture(t)
	on, off, noStages := true, false, []string{}
	contract.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{
		{Path: "Notes", Search: "text", Protect: &on},
		{Path: "Notes/research", Search: "metadata", Enrichment: &noStages},
		{Path: "Notes/research/private", Protect: &off},
	}}
	writeExplainBox(t, root, contract)
	writeFile(t, root, "Notes/research/private/paper.pdf", "untouched original")
	got, err := s.Explain(context.Background(), "box", "Notes/research/private/paper.pdf")
	if err != nil || got.Intent.State != "known" || got.Intent.Search != "metadata" || got.Intent.Protect == nil || *got.Intent.Protect || got.Intent.Enrichment == nil || len(*got.Intent.Enrichment) != 0 {
		t.Fatalf("inherited intent: %+v %v", got.Intent, err)
	}
	for field, pattern := range map[string]string{"search": "Notes/research", "enrichment": "Notes/research", "protect": "Notes/research/private"} {
		source := got.Intent.Sources[field]
		if source.Descriptor != ".loom/box.yaml" || source.Field != "files.rules."+field || source.Pattern != pattern || source.Default {
			t.Errorf("%s source: %+v", field, source)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, got.Path))
	if err != nil || string(body) != "untouched original" {
		t.Fatalf("source changed: %q %v", body, err)
	}
	// A root descriptor and rules do not establish a registered/applied watcher.
	if got.RootRegistration != "unknown" || got.AppliedConfig != "not_checked" || got.BackupCoverage != "not_verified" {
		t.Fatalf("intent promoted into evidence: %+v", got)
	}
	writeFile(t, root, "elsewhere/note.md", "not a watched area")
	unknown, err := s.Explain(context.Background(), "box", "elsewhere/note.md")
	if err != nil || unknown.Intent.Search != "unknown" {
		t.Fatalf("Box defaults escaped area: %+v %v", unknown.Intent, err)
	}
}

func TestExplainLegacyLaunchpadPolicyAndUnconfinedPolicyRejected(t *testing.T) {
	s, root, contract := explainBoxFixture(t)
	contract.SchemaVersion = box.SchemaVersion
	delete(contract.Areas, box.AreaDocuments)
	contract.Areas[box.AreaLaunchpad] = box.Area{Path: "Launchpad", Enabled: true}
	for _, area := range []string{box.AreaNotes, box.AreaLaunchpad} {
		declared := contract.Areas[area]
		declared.Watch = nil
		contract.Areas[area] = declared
		contract.Policies[area] = ".loom/policies/" + area + ".yaml"
		policy := box.WatchPolicy{SchemaVersion: box.LegacyWatchPolicySchemaVersion, Area: area, Mode: "watched_root", Path: declared.Path, Enabled: true, Backup: box.WatchPolicyToggle{Enabled: true}, Catalog: box.WatchPolicyToggle{Enabled: true}}
		raw, err := yaml.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, root, contract.Policies[area], string(raw))
	}
	writeExplainBox(t, root, contract)
	writeFile(t, root, "Launchpad/book.pdf", "retained by intent only")
	got, err := s.Explain(context.Background(), "box", "Launchpad/book.pdf")
	if err != nil || got.Intent.Search != "metadata" || got.Intent.Protect == nil || !*got.Intent.Protect || got.Intent.Sources["search"].Descriptor != ".loom/policies/launchpad.yaml" {
		t.Fatalf("legacy area: %+v %v", got.Intent, err)
	}
	writeFile(t, root, ".loom/policies/launchpad.yaml", "schema_version: loom.box.watch_policy.v0.4.1\narea: launchpad\nmode: watched_root\nenabled: true\npath: Projects\n")
	got, err = s.Explain(context.Background(), "box", "Launchpad/book.pdf")
	if err != nil || got.Intent.State != "unknown" || got.Intent.Protect != nil {
		t.Fatalf("out-of-area policy accepted: %+v %v", got.Intent, err)
	}
}

const explainProject = `kind: loom.project
schema_version: project.contract.v0.6
project:
  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV
  slug: example
  name: Example
  status: active
  owner_node: main
resources:
  material:
    kind: knowledge
    knowledge: {path: '.', category: notes}
files:
  rules:
    - path: research
      search: none
      enrichment: []
    - path: research/keep
      protect: false
`

func TestExplainNearestProjectAndLegacyFilename(t *testing.T) {
	for _, filename := range []string{".loom/project.yaml", "loom.project.yaml"} {
		t.Run(filename, func(t *testing.T) {
			s, root, _ := explainBoxFixture(t)
			writeFile(t, root, "Projects/example/"+filename, explainProject)
			writeFile(t, root, "Projects/example/research/keep/note.md", "local file")
			got, err := s.Explain(context.Background(), "box", "Projects/example/research/keep/note.md")
			if err != nil || got.Intent.State != "known" || got.Intent.Search != "none" || got.Intent.WatchPlan != "compiled_intent_only" || got.Intent.Enrichment == nil || got.Intent.Protect == nil || *got.Intent.Protect {
				t.Fatalf("project rules: %+v %v", got.Intent, err)
			}
			if got.Intent.Descriptor != "Projects/example/"+filename || got.Intent.Sources["search"].Pattern != "research" {
				t.Fatalf("project identity/source: %+v", got.Intent)
			}
			writeFile(t, root, "Projects/example/ordinary.md", "default text")
			defaults, err := s.Explain(context.Background(), "box", "Projects/example/ordinary.md")
			if err != nil || defaults.Intent.Search != "text" || defaults.Intent.Enrichment == nil || !defaults.Intent.Sources["search"].Default {
				t.Fatalf("new-schema defaults: %+v %v", defaults.Intent, err)
			}
			writeFile(t, root, "Projects/example/research/.loom/project.yaml", explainProject)
			nearest, err := s.Explain(context.Background(), "box", "Projects/example/research/keep/note.md")
			if err != nil || nearest.Intent.OwnerPath != "Projects/example/research" || nearest.Intent.Search != "text" {
				t.Fatalf("nearest owner: %+v %v", nearest.Intent, err)
			}
		})
	}
}

func TestExplainUnregisteredProjectDoesNotNeedBoxInitialization(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "Projects/new/.loom/project.yaml", explainProject)
	writeFile(t, root, "Projects/new/note.md", "current source")
	got, err := s.Explain(context.Background(), "box", "Projects/new/note.md")
	if err != nil || got.Intent.State != "known" || got.Intent.Search != "text" || got.RootRegistration != "unknown" || got.AppliedConfig != "not_checked" {
		t.Fatalf("unregistered project: %+v %v", got, err)
	}
	writeFile(t, root, "other/.loom/project.yaml", explainProject)
	writeFile(t, root, "other/note.md", "not under a known project area")
	got, err = s.Explain(context.Background(), "box", "other/note.md")
	if err != nil || got.Intent.State != "unknown" {
		t.Fatalf("guessed project root: %+v %v", got, err)
	}
}

func TestExplainProjectConflictingAliasesAndExternalProtectionStayQualified(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "Projects/example/.loom/project.yaml", explainProject)
	writeFile(t, root, "Projects/example/loom.project.yaml", strings.Replace(explainProject, "name: Example", "name: Different", 1))
	writeFile(t, root, "Projects/example/note.md", "current file")
	got, err := s.Explain(context.Background(), "box", "Projects/example/note.md")
	if err != nil || got.Intent.State != "unknown" || got.Intent.Search != "unknown" || !strings.Contains(got.Intent.Reason, "disagree") {
		t.Fatalf("conflicting descriptors: %+v %v", got.Intent, err)
	}
	if err := os.Remove(filepath.Join(root, "Projects/example/loom.project.yaml")); err != nil {
		t.Fatal(err)
	}
	declaration := strings.Replace(explainProject, "category: notes}", "category: notes, protection: retained}", 1)
	declaration = strings.Replace(declaration, "files:\n", "  retained:\n    kind: protection\n    protection: {policy_ref: .loom/backup.yaml}\nfiles:\n", 1)
	writeFile(t, root, "Projects/example/.loom/project.yaml", declaration)
	got, err = s.Explain(context.Background(), "box", "Projects/example/note.md")
	if err != nil || got.Intent.State != "known" || got.Intent.Search != "text" || got.Intent.Protect != nil || got.Intent.WatchPlan != "not_checked" || got.BackupCoverage != "not_verified" {
		t.Fatalf("external protection invented: %+v %v", got.Intent, err)
	}
}

func TestExplainPreservesOriginalBoundaryAndBoundsDescriptors(t *testing.T) {
	s, root, _ := explainBoxFixture(t)
	writeFile(t, root, "Notes/note.md", "actual source")
	outside := t.TempDir()
	writeFile(t, outside, "private.md", "outside source")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../private.md", "/etc/passwd", "Notes/../note.md", "escape/private.md", ".loom/state/token", `Notes\note.md`} {
		if _, err := s.Explain(context.Background(), "box", relative); err == nil {
			t.Errorf("unsafe source accepted: %s", relative)
		}
	}
	if err := os.Remove(filepath.Join(root, ".loom/box.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "private.md"), filepath.Join(root, ".loom/box.yaml")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Explain(context.Background(), "box", "Notes/note.md")
	if err != nil || got.Intent.State != "unknown" || !strings.Contains(got.Intent.Reason, "permission denied") {
		t.Fatalf("symlink descriptor: %+v %v", got.Intent, err)
	}
	if err := os.Remove(filepath.Join(root, ".loom/box.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, ".loom/box.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = s.Explain(context.Background(), "box", "Notes/note.md")
	if err != nil || got.Intent.State != "unknown" || !strings.Contains(got.Intent.Reason, "regular file") {
		t.Fatalf("special descriptor: %+v %v", got.Intent, err)
	}
	if err := os.Remove(filepath.Join(root, ".loom/box.yaml")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".loom/box.yaml", strings.Repeat(" ", maxExplainDescriptorBytes+1))
	got, err = s.Explain(context.Background(), "box", "Notes/note.md")
	if err != nil || got.Intent.State != "unknown" || !strings.Contains(got.Intent.Reason, "exceeds") {
		t.Fatalf("oversized descriptor: %+v %v", got.Intent, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Explain(cancelled, "box", "Notes/note.md"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestExplainArchiveIsReadOnlyAndUnverified(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "old.md", "retained bytes")
	s.Roots = append(s.Roots, Root{Ref: "archive", SourcePath: root, Verification: "verified"})
	got, err := s.Explain(context.Background(), "archive", "old.md")
	if err != nil || !got.ReadOnly || got.Verification != "not_checked" || got.Intent.State != "unknown" || got.BackupCoverage != "not_verified" {
		t.Fatalf("archive claim: %+v %v", got, err)
	}
}
