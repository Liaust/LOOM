package projectcontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func declarationEnrollmentFixture(t *testing.T, resources, policy string) string {
	t.Helper()
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", "resources:\n"+resources, 1)
	root := declarationTestRoot(t, []byte(raw))
	for _, folder := range []string{"journal", "notes", "repos", "unrelated", "journal/child", ".loom/policies"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if policy != "" {
		if err := os.WriteFile(filepath.Join(root, ".loom/policies/backup.yaml"), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const enrollmentKnowledge = "  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes}\n"
const enrollmentProtection = "  retained:\n    kind: protection\n    protection: {policy_ref: .loom/policies/backup.yaml}\n"
const enrollmentPolicy = `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  defaults:
    max_file_bytes: 1048576
    max_batch_bytes: 2097152
    max_pending_items: 9
    max_pending_bytes: 4194304
    include_deletion_markers: false
    on_limit: degrade_and_require_manual_action
    include: ['**/*.md']
    exclude: ['private/**']
  roots:
    - key: journal_owner
      path: journal
`

func TestDeclarationEnrollmentExplicitPath(t *testing.T) {
	root := declarationEnrollmentFixture(t, enrollmentKnowledge, "")
	a := Analyze(root)
	if !a.Report.OK || len(a.Report.WatchedRoots) != 1 || len(a.Plan.WatchedRoots) != 1 {
		t.Fatalf("enrollment: %+v", a.Report)
	}
	item := a.Report.WatchedRoots[0]
	if item.Key != "reading" || item.RootRelativePath != "journal" || item.SyncMode != "selected_files" || item.IndexMode != "markdown_text" || item.BackupMode != "none" || item.ActivationStatus != "pending_agent_apply" {
		t.Fatalf("owner: %+v", item)
	}
	var config watchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	if config.SyncPolicy.MaxFileBytes <= 0 || config.IndexPolicy.MaxTextBytes <= 0 || !config.IgnorePolicy.DiscoverUserRules || config.IgnorePolicy.Profile != "managed" {
		t.Fatalf("lost defaults: %+v", config)
	}
	if len(a.Report.Facets) != 0 || len(a.Report.RepositoryMembers) != 0 {
		t.Fatal("inferred enrollment")
	}
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
		t.Fatal(err)
	}
	// Mapping is source-only, even after paths and source files disappear.
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
		t.Fatal(err)
	}
	a.Plan.WatchedRoots[0].Metadata["knowledge_source"].(map[string]any)["root_relative_path"] = "unrelated"
	if a.Report.WatchedRoots[0].Metadata["knowledge_source"].(map[string]any)["root_relative_path"] != "journal" {
		t.Fatal("plan metadata aliases report")
	}
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err == nil {
		t.Fatal("substitution accepted")
	}
}
func TestDeclarationEnrollmentProtectionComposition(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			resources := strings.Replace(enrollmentKnowledge, "category: notes", "category: notes, protection: retained", 1) + enrollmentProtection
			policy := enrollmentPolicy
			if disabled {
				policy = strings.Replace(policy, "enabled: true", "enabled: false", 1)
			}
			a := Analyze(declarationEnrollmentFixture(t, resources, policy))
			if !a.Report.OK || len(a.Plan.WatchedRoots) != 1 {
				t.Fatalf("compose: %+v", a.Report.Diagnostics)
			}
			item := a.Plan.WatchedRoots[0]
			if item.RootRelativePath != "journal" || item.SyncMode != "selected_files" || item.IndexMode != "markdown_text" {
				t.Fatalf("knowledge lost: %+v", item)
			}
			if disabled {
				if item.Key != "reading" || item.BackupMode != "none" {
					t.Fatal("disabled backup enabled")
				}
				return
			}
			if item.Key != "journal_owner" || item.BackupMode != "incremental_raw" || strings.Join(item.Include, ",") != "**/*.md" || strings.Join(item.Exclude, ",") != "private/**" {
				t.Fatalf("widened or lost owner: %+v", item)
			}
			var config watchedroots.RootConfig
			_ = json.Unmarshal(item.ConfigJSON, &config)
			if config.BackupPolicy.MaxFileBytes != 1048576 || config.BackupPolicy.MaxBatchBytes != 2097152 || config.BackupPolicy.MaxPendingItems != 9 || config.BackupPolicy.MaxPendingBytes != 4194304 || *config.BackupPolicy.IncludeDeletionMarkers {
				t.Fatalf("backup limits: %+v", config.BackupPolicy)
			}
			if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDeclarationEnrollmentProtectionConflicts(t *testing.T) {
	for name, resources := range map[string]string{
		"unattached":    enrollmentKnowledge + strings.Replace(enrollmentProtection, "policy_ref: .loom/policies/backup.yaml", "policy_ref: .loom/policies/backup.yaml, path: journal", 1),
		"key_collision": strings.Replace(enrollmentKnowledge, "reading:", "journal_owner:", 1) + strings.Replace(enrollmentProtection, "policy_ref: .loom/policies/backup.yaml", "policy_ref: .loom/policies/backup.yaml, path: journal", 1),
	} {
		t.Run(name, func(t *testing.T) {
			a := Analyze(declarationEnrollmentFixture(t, resources, enrollmentPolicy))
			if a.Report.OK || a.Plan.Registerable {
				t.Fatal("conflict admitted")
			}
		})
	}
	resources := strings.Replace(enrollmentKnowledge, "category: notes", "category: notes, protection: retained", 1) + enrollmentProtection
	a := Analyze(declarationEnrollmentFixture(t, resources, strings.Replace(enrollmentPolicy, "key: journal_owner", "key: journal_owner\n      safe_root: another", 1)))
	if a.Report.OK {
		t.Fatal("incompatible safe root accepted")
	}
}
func TestDeclarationEnrollmentProtectionDisabledAncestor(t *testing.T) {
	resources := strings.Replace(enrollmentKnowledge, "path: journal, category: notes", "path: journal/child, category: notes, protection: retained", 1) + enrollmentProtection + "  ancestor:\n    kind: protection\n    protection: {policy_ref: .loom/policies/ancestor.yaml, path: journal}\n"
	policy := strings.Replace(strings.Replace(enrollmentPolicy, "enabled: true", "enabled: false", 1), "path: journal", "path: journal/child", 1)
	root := declarationEnrollmentFixture(t, resources, policy)
	if err := os.WriteFile(filepath.Join(root, ".loom/policies/ancestor.yaml"), []byte(enrollmentPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	a := Analyze(root)
	if !a.Report.OK || len(a.Plan.WatchedRoots) != 2 {
		t.Fatalf("independent ancestor coverage lost: %+v", a.Report.Diagnostics)
	}
}

func TestDeclarationEnrollmentExactNumericEvidence(t *testing.T) {
	for _, pair := range [][2]int64{{9007199254740992, 9007199254740993}, {9223372036854775806, 9223372036854775807}} {
		if declarationJSONEqual(map[string]any{"limit": pair[0]}, map[string]any{"limit": pair[1]}) {
			t.Fatal("distinct policy limits aliased")
		}
	}
	if !declarationJSONEqual(map[string]any{"limit": int64(9007199254740993)}, json.RawMessage(`{"limit":9007199254740993}`)) {
		t.Fatal("exact number did not round trip")
	}
	if declarationJSONEqual(json.RawMessage(`{"limit":1,"limit":2}`), json.RawMessage(`{"limit":2}`)) {
		t.Fatal("duplicate field collapsed into accepted evidence")
	}
	resources := strings.Replace(enrollmentKnowledge, "category: notes", "category: notes, protection: retained", 1) + enrollmentProtection
	a := Analyze(declarationEnrollmentFixture(t, resources, strings.ReplaceAll(enrollmentPolicy, "1048576", "9007199254740992")))
	if !a.Report.OK || len(a.Plan.WatchedRoots) != 1 {
		t.Fatal(a.Report.Diagnostics)
	}
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
		t.Fatal(err)
	}
	a.Plan.WatchedRoots[0].ConfigJSON = []byte(strings.ReplaceAll(string(a.Plan.WatchedRoots[0].ConfigJSON), "9007199254740992", "9007199254740993"))
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err == nil {
		t.Fatal("plan changed a backup limit beyond float64 precision")
	}
}

func TestDeclarationEnrollmentProtectionSharedSelection(t *testing.T) {
	resources := strings.Replace(enrollmentKnowledge, "category: notes", "category: notes, protection: retained", 1) + enrollmentProtection + "  shared:\n    kind: protection\n    protection: {policy_ref: .loom/policies/backup.yaml, path: journal}\n"
	a := Analyze(declarationEnrollmentFixture(t, resources, enrollmentPolicy))
	if !a.Report.OK || len(a.Plan.WatchedRoots) != 1 || len(a.Plan.Declaration.Protection) != 2 {
		t.Fatalf("shared selection: %+v", a.Report.Diagnostics)
	}
	if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
		t.Fatal(err)
	}
	if len(a.Plan.Declaration.Protection[0].WatchedRoots)+len(a.Plan.Declaration.Protection[1].WatchedRoots) != 1 {
		t.Fatal("duplicate owner config")
	}
}
