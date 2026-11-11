package watchedroots

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
)

func TestLegacyConfigBytesAndHashUnchanged(t *testing.T) {
	config := NormalizeRootConfig(RootConfig{RootKey: "notes", SafeRootKey: "slice09", Exclude: []string{"scratch/**"}})
	const legacy = `{
		"schema_version":"watched_root.config.v0.2", "root_key":"notes", "display_name":"notes",
		"safe_root_key":"slice09", "root_relative_path":".", "include":["**/*"], "exclude":["scratch/**"],
		"ignore_policy":{"discover_user_rules":false},
		"watch":{"enabled":false,"backend":"none","debounce_window":"45s"},
		"scan":{"startup_scan":"stale_only","full_rescan_interval":"6h","max_files_per_run":10000,
			"max_dirs_per_run":2000,"max_runtime":"5m","stability_window":"2s","follow_symlinks":false,
			"hidden_policy":"exclude_by_default","max_hash_file_bytes":52428800,"max_hash_bytes_per_run":1073741824},
		"backup_policy":{"mode":"none","max_file_bytes":1048576,"max_batch_bytes":1048576,
			"max_pending_items":10000,"max_pending_bytes":1073741824,"include_deletion_markers":true,
			"on_limit":"degrade_and_require_manual_action"},
		"sync_policy":{"mode":"none","max_file_bytes":1048576,"logical_name_strategy":"relative_path"},
		"index_policy":{"mode":"none","max_text_bytes":524288},
		"delete_policy":{"mode":"local_state_only","mass_delete_threshold_count":100,"mass_delete_threshold_percent":20},
		"fidelity_policy":{"observe_empty_directories":true,"observe_symlinks":true,"follow_symlinks":false,
			"observe_xattrs":true,"observe_acl_presence":true,"preserve_generated_apple_metadata":false,
			"package_directory_mode":"observe_boundary","path_collision_mode":"warn",
			"special_file_mode":"observe_skip","executable_bit_mode":"observe"}
	}`
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("legacy config bytes changed:\n%s\nwant:\n%s", got, want.Bytes())
	}
	sum := sha256.Sum256(want.Bytes())
	if ConfigHash(config) != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("legacy config hash changed")
	}
}

func TestIndependentConfigValidatesNewFields(t *testing.T) {
	fsConfig := filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("slice09", t.TempDir())}}
	for _, tc := range []struct {
		name   string
		config RootConfig
		field  string
	}{
		{"legacy selections", RootConfig{ConsumerSelections: &ConsumerSelections{}}, "v0.3"},
		{"legacy files", RootConfig{Files: &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{}}}, "v0.3"},
		{"backup empty include", RootConfig{SchemaVersion: IndependentConfigSchemaVersion, ConsumerSelections: &ConsumerSelections{Backup: &PathSelection{}}}, "backup.include"},
		{"sync empty include", RootConfig{SchemaVersion: IndependentConfigSchemaVersion, ConsumerSelections: &ConsumerSelections{Sync: &PathSelection{}}}, "sync.include"},
		{"index empty include", RootConfig{SchemaVersion: IndependentConfigSchemaVersion, ConsumerSelections: &ConsumerSelections{Index: &PathSelection{}}}, "index.include"},
		{"blank include", independentSelectionConfig([]string{" "}, nil), "sync.include"},
		{"blank exclude", independentSelectionConfig([]string{"**/*"}, []string{" "}), "sync.exclude"},
		{"traversal", independentSelectionConfig([]string{"a/../b"}, nil), "sync.include"},
		{"absolute", independentSelectionConfig([]string{"/tmp/*"}, nil), "sync.include"},
		{"malformed", independentSelectionConfig([]string{"**/*"}, []string{"["}), "sync.exclude"},
		{"nul", independentSelectionConfig([]string{"bad\x00path"}, nil), "sync.include"},
		{"backslash", independentSelectionConfig([]string{`notes\*`}, nil), "sync.include"},
		{"files missing rules", RootConfig{SchemaVersion: IndependentConfigSchemaVersion, Files: &filepolicy.FolderPolicy{}}, "files.rules"},
		{"files traversal", RootConfig{SchemaVersion: IndependentConfigSchemaVersion, Files: &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{{Path: "../notes", Search: "none"}}}}, "files.rules[0].path"},
		{"files search", RootConfig{SchemaVersion: IndependentConfigSchemaVersion, Files: &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{{Path: "notes", Search: "invalid"}}}}, "files.rules[0].search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.config.RootKey, tc.config.SafeRootKey = "notes", "slice09"
			if _, err := ValidatePortableRootConfig(tc.config); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("portable validation: %v, wanted %s", err, tc.field)
			}
			if _, err := ValidateRootConfigForRun(tc.config, fsConfig); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("runtime validation: %v, wanted %s", err, tc.field)
			}
		})
	}
}

func independentSelectionConfig(include, exclude []string) RootConfig {
	return RootConfig{SchemaVersion: IndependentConfigSchemaVersion,
		ConsumerSelections: &ConsumerSelections{Sync: &PathSelection{Include: include, Exclude: exclude}}}
}

func TestIndependentConfigStrictReaderAndRoundTrip(t *testing.T) {
	const valid = `{"schema_version":"watched_root.config.v0.3","root_key":"notes","safe_root_key":"slice09",
		"consumer_selections":{"backup":{"include":["**/*"]},"sync":{"include":["**/*.md"],"exclude":["private/**"]}},
		"files":{"rules":[{"path":"private","search":"none","protect":false,"enrichment":[]}]}}`
	var config RootConfig
	if err := communication.DecodeStrictJSONObject([]byte(valid), &config); err != nil {
		t.Fatal(err)
	}
	config, err := ValidatePortableRootConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip RootConfig
	if err := communication.DecodeStrictJSONObject(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if ConfigHash(roundTrip) != ConfigHash(config) || roundTrip.Files.Rules[0].Protect == nil || *roundTrip.Files.Rules[0].Protect || roundTrip.Files.Rules[0].Enrichment == nil {
		t.Fatal("new fields lost during round trip")
	}
	for _, raw := range []string{
		`{"consumer_selections":{"unknown":{"include":["**/*"]}}}`,
		`{"consumer_selections":{"sync":{"include":["**/*"],"unknown":true}}}`,
		`{"files":{"rules":[{"path":"notes","unknown":true}]}}`,
		`{"consumer_selections":{"sync":{"include":"**/*"}}}`,
	} {
		if err := communication.DecodeStrictJSONObject([]byte(raw), new(RootConfig)); err == nil {
			t.Fatalf("strict config reader accepted %s", raw)
		}
	}
}

func TestIndependentConfigNormalizationAndSemanticHashes(t *testing.T) {
	stages := []string{"vision", "ocr", "ocr"}
	protect := false
	config := independentSelectionConfig([]string{"z/**", " a/** ", "z/**"}, []string{"tmp/**", "cache/**", "tmp/**"})
	config.RootKey, config.SafeRootKey = "notes", "slice09"
	config.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{
		{Path: "z", Protect: &protect}, {Path: "a", Search: "none", Enrichment: &stages},
	}}
	normalized, err := ValidatePortableRootConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if config.ConsumerSelections.Sync.Include[0] != "z/**" || config.Files.Rules[0].Path != "z" || len(stages) != 3 {
		t.Fatal("normalization mutated caller-owned fields")
	}
	if ConfigHash(config) != ConfigHash(normalized) || ContentConfigHash(config) != ContentConfigHash(normalized) {
		t.Fatal("equivalent normalized selectors changed hashes")
	}
	before, contentBefore := ConfigHash(normalized), ContentConfigHash(normalized)
	for _, change := range []func(*RootConfig){
		func(c *RootConfig) { c.ConsumerSelections.Backup = &PathSelection{Include: []string{"selected/**"}} },
		func(c *RootConfig) { c.ConsumerSelections.Sync.Exclude = []string{"private/**"} },
		func(c *RootConfig) { c.ConsumerSelections.Index = &PathSelection{Include: []string{"selected/**"}} },
		func(c *RootConfig) { c.Files.Rules[0].Search = "metadata" },
		func(c *RootConfig) { *c.Files.Rules[1].Protect = true },
		func(c *RootConfig) { *c.Files.Rules[0].Enrichment = []string{} },
	} {
		candidate := NormalizeRootConfig(normalized)
		change(&candidate)
		if ConfigHash(candidate) == before || ContentConfigHash(candidate) == contentBefore {
			t.Fatal("semantic policy change was omitted from configuration/coverage hash")
		}
	}
}
