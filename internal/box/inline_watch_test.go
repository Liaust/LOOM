package box

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/filepolicy"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

func initializedLegacyPolicyBox(t *testing.T, profile string) string {
	t.Helper()
	root := initializedWatchBox(t, profile)
	contract, err := LoadContract(ContractPath(root))
	if err != nil {
		t.Fatal(err)
	}
	contract.SchemaVersion = SchemaVersion
	for _, area := range []string{AreaNotes, AreaDocuments} {
		declared := contract.Areas[area]
		declared.Watch = nil
		contract.Areas[area] = declared
		contract.Policies[area] = ".loom/policies/" + area + ".watch.yaml"
		path := filepath.Join(root, contract.Policies[area])
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		writeBoxTestFile(t, path, renderWatchPolicy(defaultWatchPolicy(area)))
	}
	writeBoxTestFile(t, ContractPath(root), renderContractYAML(contract))
	return root
}

func writeBoxTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func boxTestConfig(t *testing.T, contract Contract, area string) (agentwatchedroots.RootConfig, projectcontracts.ProjectWatchedRootItem) {
	t.Helper()
	item, diagnostics := watchRootItemForArea(contract, area)
	if hasErrorDiagnostics(diagnostics) || item.BackendRootKey == "" {
		t.Fatalf("watch root missing or invalid: %+v %+v", item, diagnostics)
	}
	var config agentwatchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	return config, item
}

func TestInlineWatchContractStrictReader(t *testing.T) {
	base := strings.Replace(validContractPayload(t, ProfileMain), SchemaVersion, FolderPolicySchemaVersion, 1)
	for _, area := range []string{AreaNotes, AreaDocuments} {
		base = strings.Replace(base, "  "+area+": .loom/policies/"+area+".watch.yaml\n", "", 1)
		path := strings.Title(area)
		base = strings.Replace(base, "  "+area+":\n    path: "+path+"\n    enabled: true\n", "  "+area+":\n    path: "+path+"\n    enabled: true\n    watch:\n      enabled: true\n      backup:\n        enabled: true\n", 1)
	}
	contract, err := ParseContract([]byte(base))
	if err != nil || contract.Areas[AreaNotes].Watch == nil {
		t.Fatalf("inline descriptor: %+v %v", contract, err)
	}
	if _, err := ParseContract([]byte(renderContractYAML(contract))); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]string{
		"old schema":         strings.Replace(base, FolderPolicySchemaVersion, SchemaVersion, 1),
		"unknown":            strings.Replace(base, "      backup:", "      bakcup:", 1),
		"wrong boolean type": strings.Replace(base, "      enabled: true", "      enabled: 'true'", 1),
		"wrong schema":       strings.Replace(base, "    watch:", "    watch:\n      schema_version: wrong", 1),
		"wrong area":         strings.Replace(base, "    watch:", "    watch:\n      area: documents", 1),
		"wrong mode":         strings.Replace(base, "    watch:", "    watch:\n      mode: custody_transfer", 1),
		"path escape":        strings.Replace(base, "    watch:", "    watch:\n      path: Documents", 1),
		"two sources":        base + "  notes: .loom/policies/notes.watch.yaml\n",
		"bad text limit":     strings.Replace(base, "    watch:", "    watch:\n      index: {enabled: true, max_text_bytes: -1}", 1),
		"bad processing":     strings.Replace(base, "    watch:", "    watch:\n      processing: {ocr: unsupported}", 1),
		"null":               strings.Replace(base, "    watch:\n      enabled: true\n      backup:\n        enabled: true", "    watch: null", 1),
		"extra document":     base + "---\nignored: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseContract([]byte(payload)); err == nil {
				t.Fatalf("accepted invalid inline descriptor:\n%s", payload)
			}
		})
	}
	legacyNull := strings.Replace(validContractPayload(t, ProfileMain), "    path: Notes", "    watch: null\n    path: Notes", 1)
	if _, err := ParseContract([]byte(legacyNull)); err == nil {
		t.Fatal("old schema silently ignored inline watch")
	}
}

func TestInlineWatchLegacySemanticEquivalence(t *testing.T) {
	root := initializedLegacyPolicyBox(t, ProfileMain)
	contract, err := LoadContract(ContractPath(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, area := range []string{AreaNotes, AreaDocuments} {
		for _, variant := range []string{"default", "no_backup", "no_index", "no_sync"} {
			t.Run(area+"/"+variant, func(t *testing.T) {
				legacy := cloneContract(contract)
				policy := defaultWatchPolicy(area)
				switch variant {
				case "no_backup":
					policy.Backup.Enabled = false
				case "no_index":
					policy.Text.Enabled, policy.Index.Enabled = false, false
				case "no_sync":
					policy.Text.Enabled, policy.Index.Enabled = false, false
					policy.Sync.Enabled, policy.Object.Enabled = false, false
				}
				writeBoxTestFile(t, filepath.Join(root, legacy.Policies[area]), renderWatchPolicy(policy))
				oldConfig, _ := boxTestConfig(t, legacy, area)
				if oldConfig.SchemaVersion != agentwatchedroots.ConfigSchemaVersion {
					t.Fatal("legacy descriptor changed config version")
				}
				inline := cloneContract(legacy)
				inline.SchemaVersion = FolderPolicySchemaVersion
				delete(inline.Policies, area)
				declared := inline.Areas[area]
				policy.SchemaVersion, policy.Area, policy.Path, policy.Mode = "", "", "", ""
				declared.Watch = &policy
				inline.Areas[area] = declared
				if err := ValidateContract(inline); err != nil {
					t.Fatal(err)
				}
				newConfig, _ := boxTestConfig(t, inline, area)
				if newConfig.SchemaVersion != agentwatchedroots.IndependentConfigSchemaVersion {
					t.Fatal("inline descriptor did not select independent consumers")
				}
				for _, path := range []string{"note.md", "nested/NOTE.Md", "a.markdown", "a.MARKDOWN", "b.mdown", "b.rst", "b.adoc", "a.txt", "a.TEXT", "a.pdf", "nested/image.png", "canvas.canvas", "no-extension"} {
					before := agentwatchedroots.EffectivePolicies(oldConfig, path)
					after := agentwatchedroots.EffectivePolicies(newConfig, path)
					if before != after {
						t.Fatalf("%s: legacy %+v inline %+v", path, before, after)
					}
				}
				newConfig.SchemaVersion, newConfig.ConsumerSelections = oldConfig.SchemaVersion, nil
				if !reflect.DeepEqual(oldConfig, newConfig) {
					t.Fatalf("inline settings changed unrelated config: legacy %+v inline %+v", oldConfig, newConfig)
				}
			})
		}
	}
}

func TestBoxFilesIndependentSearchProtect(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "inline"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			root := initializedWatchBox(t, ProfileMain)
			if legacy {
				root = initializedLegacyPolicyBox(t, ProfileMain)
			}
			contract, err := LoadContract(ContractPath(root))
			if err != nil {
				t.Fatal(err)
			}
			contract.SchemaVersion = FolderPolicySchemaVersion
			off, on := false, true
			contract.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{
				{Path: "Notes/private", Search: "none"},
				{Path: "Notes/private/open", Search: "text"},
				{Path: "Notes/local", Protect: &off},
				{Path: "Notes/local/keep", Protect: &on},
				{Path: "Notes/metadata", Search: "metadata"},
			}}
			writeBoxTestFile(t, ContractPath(root), renderContractYAML(contract))
			plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: contract.OwnerNode}})
			if err != nil || len(plan.WatchedRoots) != 2 {
				t.Fatalf("files plan: %+v %v", plan.Diagnostics, err)
			}
			config, before := boxTestConfig(t, contract, AreaNotes)
			if config.SchemaVersion != agentwatchedroots.IndependentConfigSchemaVersion || config.Files == nil || config.ConsumerSelections == nil || config.ConsumerSelections.Sync == nil {
				t.Fatalf("missing independent policy: %+v", config)
			}
			for _, test := range []struct {
				path, backup, sync, index string
			}{
				{"normal.md", "incremental_raw", "selected_files", "markdown_text"},
				{"private/nested/note.md", "incremental_raw", "selected_files", "none"},
				{"private/book.pdf", "incremental_raw", "none", "none"},
				{"private/open/note.md", "incremental_raw", "selected_files", "markdown_text"},
				{"local/note.md", "none", "selected_files", "markdown_text"},
				{"local/keep/note.md", "incremental_raw", "selected_files", "markdown_text"},
				{"metadata/note.md", "incremental_raw", "selected_files", "metadata_only"},
				{"metadata/book.pdf", "incremental_raw", "none", "metadata_only"},
				{"attachment.pdf", "incremental_raw", "none", "metadata_only"},
			} {
				policies := agentwatchedroots.EffectivePolicies(config, test.path)
				if policies.Backup != test.backup || policies.Sync != test.sync || policies.Index != test.index {
					t.Fatalf("%s: %+v", test.path, policies)
				}
			}
			if len(config.Exclude) != 0 || config.ConsumerSelections.Backup != nil || config.ConsumerSelections.Index != nil {
				t.Fatal("search exclusions narrowed observation or backup")
			}
			stages := []string{}
			contract.Files.Rules = append(contract.Files.Rules, filepolicy.FolderRule{Path: "Notes/new", Enrichment: &stages})
			contract.Files.Rules[0].Enrichment = &stages
			_, after := boxTestConfig(t, contract, AreaNotes)
			if string(before.ConfigJSON) != string(after.ConfigJSON) || before.ConfigHash != after.ConfigHash {
				t.Fatal("enrichment-only edits changed consumer configuration")
			}
		})
	}
}

func TestLegacyEnrichmentDescriptorKeepsConfigAndAuthoredFiles(t *testing.T) {
	for _, version := range []string{SchemaVersion, FolderPolicySchemaVersion} {
		t.Run(version, func(t *testing.T) {
			root := initializedLegacyPolicyBox(t, ProfileMain)
			contract, err := LoadContract(ContractPath(root))
			if err != nil {
				t.Fatal(err)
			}
			contract.SchemaVersion = version
			_, before := boxTestConfig(t, contract, AreaNotes)
			contract.SchemaVersion = FolderPolicySchemaVersion
			stages := []string{}
			contract.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{{Path: "Notes/books", Enrichment: &stages}}}
			_, after := boxTestConfig(t, contract, AreaNotes)
			if string(before.ConfigJSON) != string(after.ConfigJSON) || before.ConfigHash != after.ConfigHash {
				t.Fatal("legacy enrichment changed the byte-identical v0.2 config")
			}
			paths := map[string]string{ContractPath(root): "# Keep my descriptor comments\n" + renderContractYAML(contract)}
			for _, area := range []string{AreaNotes, AreaDocuments} {
				paths[filepath.Join(root, contract.Policies[area])] = "# Keep my policy comments\n" + renderWatchPolicy(defaultWatchPolicy(area))
			}
			for path, content := range paths {
				writeBoxTestFile(t, path, content)
			}
			for range 2 {
				result, err := Init(InitInput{Resolved: Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: contract.OwnerNode}})
				if err != nil || len(result.CreatedFiles) != 0 || len(result.BackupFiles) != 0 {
					t.Fatalf("existing descriptor init: %+v %v", result, err)
				}
			}
			for path, content := range paths {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != content {
					t.Fatalf("init changed authored file %s: %v", path, err)
				}
			}
		})
	}
}

func TestExplicitWatchPolicyStrictCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.watch.yaml")
	for _, version := range []string{WatchPolicySchemaVersion, LegacyWatchPolicySchemaVersion} {
		valid := strings.Replace(notesPolicy(Contract{}), WatchPolicySchemaVersion, version, 1)
		writeBoxTestFile(t, path, valid)
		if _, diagnostics := loadWatchPolicy(path, AreaNotes); hasErrorDiagnostics(diagnostics) {
			t.Fatal(diagnostics)
		}
		for _, payload := range []string{
			valid + "typo: true\n",
			valid + "---\nignored: true\n",
			strings.Replace(valid, "enabled: true", "enabled: 'true'", 1),
			strings.Replace(valid, "    enabled: true", "    enabled: true\n    unknown: true", 1),
		} {
			writeBoxTestFile(t, path, payload)
			if _, diagnostics := loadWatchPolicy(path, AreaNotes); !hasErrorDiagnostics(diagnostics) {
				t.Fatalf("accepted invalid compatibility policy:\n%s", payload)
			}
		}
	}
}

func TestInlineWatchFilesCannotEnableDisabledConsumers(t *testing.T) {
	contract := DefaultContract(Resolved{RootPath: t.TempDir(), Profile: ProfileMain, OwnerNode: "main"})
	area := contract.Areas[AreaNotes]
	area.Watch = &WatchPolicy{Enabled: true}
	contract.Areas[AreaNotes] = area
	protect := true
	contract.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{{Path: "Notes", Search: "text", Protect: &protect}}}
	config, _ := boxTestConfig(t, contract, AreaNotes)
	policies := agentwatchedroots.EffectivePolicies(config, "note.md")
	if policies.Sync != "none" || policies.Index != "none" || policies.Backup != "none" {
		t.Fatalf("files enabled disabled consumers: %+v", policies)
	}
	area.Enabled = false
	contract.Areas[AreaNotes] = area
	if item, diagnostics := watchRootItemForArea(contract, AreaNotes); hasErrorDiagnostics(diagnostics) || item.BackendRootKey != "" {
		t.Fatalf("disabled area emitted a root: %+v %+v", item, diagnostics)
	}
}

func TestInlineInitLeavesUnreferencedWatchFilesUntouched(t *testing.T) {
	root := t.TempDir()
	policyDir := filepath.Join(root, ".loom", "policies")
	if err := os.MkdirAll(policyDir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "# Authored policy not referenced by box.yaml\ninvalid-for-a-watch-policy: retained\n"
	for _, area := range []string{AreaNotes, AreaDocuments} {
		writeBoxTestFile(t, filepath.Join(policyDir, area+".watch.yaml"), content)
	}
	resolved := Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "main", RuntimeStateRoot: t.TempDir()}
	for range 2 {
		result, err := Init(InitInput{Resolved: resolved})
		if err != nil || len(result.BackupFiles) != 0 {
			t.Fatalf("inline init: %+v %v", result, err)
		}
		plan, err := BuildWatchPlan(WatchStatusInput{Resolved: resolved})
		if err != nil || len(plan.WatchedRoots) != 2 {
			t.Fatalf("unreferenced policy overrode inline settings: %+v %v", plan.Diagnostics, err)
		}
	}
	for _, area := range []string{AreaNotes, AreaDocuments} {
		path := filepath.Join(policyDir, area+".watch.yaml")
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != content {
			t.Fatalf("unreferenced authored file changed: %s %v", path, err)
		}
	}
}
