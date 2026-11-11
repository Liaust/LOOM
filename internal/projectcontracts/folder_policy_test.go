package projectcontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestFilesystemDeclarationRootDefaults(t *testing.T) {
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), ProjectSchemaV05, ProjectSchemaV06, 1)
	raw = strings.Replace(raw, "resources: {}", "resources:\n  files:\n    kind: knowledge\n    knowledge: {path: '.', category: notes}", 1)
	root := declarationTestRoot(t, []byte(raw))
	a := Analyze(root)
	if !a.Report.OK || len(a.Plan.WatchedRoots) != 1 {
		t.Fatalf("%+v", a.Report.Diagnostics)
	}
	var config watchedroots.RootConfig
	if err := json.Unmarshal(a.Plan.WatchedRoots[0].ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != watchedroots.IndependentConfigSchemaVersion || config.RootRelativePath != "." || config.Files == nil || config.ConsumerSelections.Sync == nil {
		t.Fatalf("%+v", config)
	}
	policies := watchedroots.EffectivePolicies(config, "notes/paper.pdf")
	if policies.Sync != watchedroots.SyncModeNone {
		t.Fatalf("PDF selected for automatic device delivery: %+v", policies)
	}
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(raw, ProjectSchemaV06, ProjectSchemaV05, 1)
	if _, err := ParseProjectDeclaration([]byte(legacy)); err == nil {
		t.Fatal("legacy root knowledge accepted")
	}
}

func TestFilesystemDeclarationConsumersShareObserverNotFilters(t *testing.T) {
	resources := strings.Replace(enrollmentKnowledge, "category: notes", "category: notes, protection: retained, include: ['**/*.md']", 1) + enrollmentProtection
	policy := strings.Replace(enrollmentPolicy, "include: ['**/*.md']", "include: ['**/*']", 1)
	root := declarationEnrollmentFixture(t, resources, policy)
	file := filepath.Join(root, CanonicalRootContractPath)
	raw, _ := os.ReadFile(file)
	raw = []byte(strings.Replace(string(raw), ProjectSchemaV05, ProjectSchemaV06, 1))
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := Analyze(root)
	if !a.Report.OK || len(a.Plan.WatchedRoots) != 1 {
		t.Fatalf("%+v", a.Report.Diagnostics)
	}
	var config watchedroots.RootConfig
	if err := json.Unmarshal(a.Plan.WatchedRoots[0].ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	if config.ConsumerSelections == nil || config.ConsumerSelections.Backup.Include[0] != "**/*" || config.ConsumerSelections.Index.Include[0] != "**/*.md" {
		t.Fatalf("%+v", config)
	}
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
		t.Fatal(err)
	}
}
