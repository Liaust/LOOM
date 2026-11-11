package box

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/projectcontracts"
)

func TestBoxFolderPolicyStrictVersionAndRoundTrip(t *testing.T) {
	base := strings.Replace(validContractPayload(t, ProfileMain), SchemaVersion, FolderPolicySchemaVersion, 1)
	valid := base + "files:\n  rules:\n    - path: Notes/books\n      enrichment: []\n"
	contract, err := ParseContract([]byte(valid))
	if err != nil || contract.Files == nil {
		t.Fatalf("new descriptor: %+v %v", contract, err)
	}
	encoded, _ := json.Marshal(contract)
	var again Contract
	if err := json.Unmarshal(encoded, &again); err != nil || ValidateContract(again) != nil || !reflect.DeepEqual(contract.Files, again.Files) {
		t.Fatalf("lost empty allowlist: %s %v", encoded, err)
	}
	for _, payload := range []string{
		strings.Replace(valid, FolderPolicySchemaVersion, SchemaVersion, 1),
		base + "files: null\n",
		base + "Files: null\n",
		base + "files: {rules: [{path: Notes, Enrichment: null}]}\n",
		base + "files: {rules: [{path: Notes, enrichment: null}]}\n",
		base + "files: {rules: [{path: 123, enrichment: []}]}\n",
		base + "files: {rules: [{path: Notes, search: yes}]}\n",
		base + "files: {rules: [{path: Notes, protect: 'false'}]}\n",
		base + "files: {rules: [{path: Notes, search: null}]}\n",
		base + "files: {rules: [{path: Notes, protect: null}]}\n",
		base + "filse: {}\n",
		strings.Replace(base, FolderPolicySchemaVersion, "' "+FolderPolicySchemaVersion+" '", 1) + "filse: {}\n",
		valid + "---\nignored: true\n",
	} {
		if _, err := ParseContract([]byte(payload)); err == nil {
			t.Fatalf("accepted unsupported descriptor:\n%s", payload)
		}
	}
	// Legacy unknown-field behavior is unchanged, but cannot silently eat files.
	if _, err := ParseContract([]byte(validContractPayload(t, ProfileMain) + "legacy_note: kept\n")); err != nil {
		t.Fatal(err)
	}
}

func TestBoxFolderPolicyPreservesWatchConfigAndNoOpInit(t *testing.T) {
	root := t.TempDir()
	resolved := Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "macbook", RuntimeStateRoot: t.TempDir()}
	if _, err := Init(InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	before, err := BuildWatchPlan(WatchStatusInput{Resolved: resolved})
	if err != nil {
		t.Fatal(err)
	}
	contractPath := ContractPath(root)
	original, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(original), SchemaVersion, FolderPolicySchemaVersion, 1) + "files:\n  rules:\n    - path: Notes/books\n      enrichment: []\n"
	if err := os.WriteFile(contractPath, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := BuildWatchPlan(WatchStatusInput{Resolved: resolved})
	if err != nil || len(after.WatchedRoots) != len(before.WatchedRoots) {
		t.Fatalf("plan: %+v %v", after.Diagnostics, err)
	}
	for i, old := range before.WatchedRoots {
		item := after.WatchedRoots[i]
		if item.BackendRootKey != old.BackendRootKey || item.ConfigHash != old.ConfigHash || !reflect.DeepEqual(item.ConfigJSON, old.ConfigJSON) {
			t.Fatal("folder enrichment changed a source/sync/index/backup selector")
		}
		if item.Key == AreaNotes {
			raw, _ := json.Marshal(item.Metadata["knowledge_source"].(map[string]any)["policy"])
			policy, err := projectcontracts.DecodeKnowledgeSourcePolicy(raw)
			if err != nil || policy.Files == nil || policy.Files.Rules[0].Path != "Notes/books" {
				t.Fatalf("policy not delivered to Notes: %s %v", raw, err)
			}
		}
	}
	result, err := Init(InitInput{Resolved: resolved})
	if err != nil || len(result.CreatedFiles) != 0 || len(result.BackupFiles) != 0 {
		t.Fatalf("repeat init was not a no-op: %+v %v", result, err)
	}
	actual, _ := os.ReadFile(contractPath)
	if string(actual) != updated {
		t.Fatal("init rewrote the explicit descriptor")
	}
	// Newly created descendants need no additional descriptor or root apply.
	if err := os.MkdirAll(filepath.Join(root, "Notes", "books", "new"), 0700); err != nil {
		t.Fatal(err)
	}
	repeated, err := BuildWatchPlan(WatchStatusInput{Resolved: resolved})
	if err != nil || !reflect.DeepEqual(after.WatchedRoots, repeated.WatchedRoots) {
		t.Fatalf("ordinary directory creation changed the compiled plan: %v", err)
	}
}

func TestBoxFolderPolicyEmptyRulesKeepLegacyMetadata(t *testing.T) {
	resolved := Resolved{RootPath: t.TempDir(), Profile: ProfileMain, OwnerNode: "main", RuntimeStateRoot: t.TempDir()}
	if _, err := Init(InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	contract := *Inspect(resolved).Contract
	before, diagnostics := watchRootItemForArea(contract, AreaNotes)
	if hasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	contract.SchemaVersion = FolderPolicySchemaVersion
	contract.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{}}
	after, diagnostics := watchRootItemForArea(contract, AreaNotes)
	if hasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(before, after) {
		t.Fatal("empty folder rules introduced new metadata/config work", diagnostics)
	}
}
