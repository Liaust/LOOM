package watchedroots

import (
	"testing"

	"loom.local/loom/internal/filepolicy"
)

func independentConsumerConfig() RootConfig {
	return RootConfig{
		SchemaVersion: IndependentConfigSchemaVersion, RootKey: "notes", SafeRootKey: "slice09",
		Include: []string{"**/*"}, Scan: ScanConfig{StabilityWindow: "0s"},
		BackupPolicy: BackupPolicy{Mode: BackupModeIncrementalRaw, MaxFileBytes: 1 << 20, MaxBatchBytes: 1 << 20},
		SyncPolicy:   SyncPolicy{Mode: SyncModeSelectedFiles, ProjectRef: "project_test"},
		IndexPolicy:  IndexPolicy{Mode: IndexModeMarkdownText}, DeletePolicy: DeletePolicy{Mode: DeleteModeTombstone},
	}
}

func TestIndependentConsumersDoNotChangeObservationAdmission(t *testing.T) {
	config := independentConsumerConfig()
	config.ConsumerSelections = &ConsumerSelections{
		Backup: &PathSelection{Include: []string{"**/*"}, Exclude: []string{"no-backup/**"}},
		Sync:   &PathSelection{Include: []string{"**/*"}, Exclude: []string{"no-sync/**"}},
		Index:  &PathSelection{Include: []string{"**/*"}, Exclude: []string{"no-index/**"}},
	}
	root := testValidatedRoot(t, config)
	for _, tc := range []struct {
		path                string
		backup, sync, index string
	}{
		{"note.md", BackupModeIncrementalRaw, SyncModeSelectedFiles, IndexModeMarkdownText},
		{"paper.pdf", BackupModeIncrementalRaw, SyncModeSelectedFiles, IndexModeMetadataOnly},
		{"no-backup/note.md", BackupModeNone, SyncModeSelectedFiles, IndexModeMarkdownText},
		{"no-sync/note.md", BackupModeIncrementalRaw, SyncModeNone, IndexModeMarkdownText},
		{"no-index/note.md", BackupModeIncrementalRaw, SyncModeSelectedFiles, IndexModeNone},
	} {
		classification := ClassifyPath(PathObservation{RelativePath: tc.path, Safe: true, Exists: true, Kind: PathKindFile}, root)
		if !classification.Included || classification.Policies.Backup != tc.backup || classification.Policies.Sync != tc.sync || classification.Policies.Index != tc.index {
			t.Fatalf("%s: independent policies/admission changed: %+v", tc.path, classification)
		}
	}
	root.Config.SchemaVersion = ConfigSchemaVersion
	root.Config.ConsumerSelections = nil
	if policies := EffectivePolicies(root.Config, "paper.pdf"); policies.Sync != SyncModeNone || policies.Backup != BackupModeIncrementalRaw {
		t.Fatalf("legacy non-text coupling changed: %+v", policies)
	}
}

func TestIndependentFolderRulesUseDescriptorRelativePaths(t *testing.T) {
	config := NormalizeRootConfig(independentConsumerConfig())
	config.RootRelativePath = "Documents/Notes"
	protect, noProtect := true, false
	empty := []string{}
	config.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{
		{Path: "Documents/Notes/private", Search: "none", Protect: &noProtect},
		{Path: "Documents/Notes/private/catalog", Search: "metadata"},
		{Path: "Documents/Notes/private/share", Search: "text", Protect: &protect},
		{Path: "Documents/Notes/light", Enrichment: &empty},
	}}
	for _, tc := range []struct {
		path          string
		backup, index string
	}{
		{"ordinary.md", BackupModeIncrementalRaw, IndexModeMarkdownText},
		{"private/note.md", BackupModeNone, IndexModeNone},
		{"private/catalog/note.md", BackupModeNone, IndexModeMetadataOnly},
		{"private/share/note.md", BackupModeIncrementalRaw, IndexModeMarkdownText},
		{"private/share/paper.pdf", BackupModeIncrementalRaw, IndexModeMetadataOnly},
		{"light/note.md", BackupModeIncrementalRaw, IndexModeMarkdownText},
	} {
		policies := EffectivePolicies(config, tc.path)
		if policies.Backup != tc.backup || policies.Index != tc.index || policies.Sync != SyncModeSelectedFiles {
			t.Fatalf("%s: rules coupled independent consumers or used wrong relative path: %+v", tc.path, policies)
		}
	}
	config.BackupPolicy.Mode, config.IndexPolicy.Mode = BackupModeNone, IndexModeNone
	if policies := EffectivePolicies(config, "private/share/note.md"); policies.Backup != BackupModeNone || policies.Index != IndexModeNone {
		t.Fatalf("rules enabled disabled root consumers: %+v", policies)
	}
	config.BackupPolicy.Mode, config.IndexPolicy.Mode = BackupModeIncrementalRaw, IndexModeMetadataOnly
	config.ConsumerSelections = &ConsumerSelections{
		Backup: &PathSelection{Include: []string{"ordinary.md"}}, Index: &PathSelection{Include: []string{"ordinary.md"}},
	}
	if policies := EffectivePolicies(config, "private/share/note.md"); policies.Backup != BackupModeNone || policies.Index != IndexModeNone || policies.Sync != SyncModeSelectedFiles {
		t.Fatalf("rules escaped consumer selections: %+v", policies)
	}
	config.ConsumerSelections = nil
	if policies := EffectivePolicies(config, "private/share/note.md"); policies.Index != IndexModeMarkdownText {
		t.Fatalf("explicit text rule did not override inherited metadata mode: %+v", policies)
	}
}

func TestIndependentConsumersCannotEscapeObservationEnvelope(t *testing.T) {
	config := independentConsumerConfig()
	config.Include, config.Exclude = []string{"notes/**"}, []string{"notes/private/**"}
	config.ConsumerSelections = &ConsumerSelections{Sync: &PathSelection{Include: []string{"**/*"}}}
	protect := true
	config.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{{Path: "root/notes/private", Search: "text", Protect: &protect}}}
	root := testValidatedRoot(t, config)
	for _, relative := range []string{"outside.md", "notes/private/note.md", "notes/.loom-notes-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/original/note.md"} {
		classification := ClassifyPath(PathObservation{RelativePath: relative, Safe: true, Exists: true, Kind: PathKindFile}, root)
		if classification.Included {
			t.Fatalf("consumer selection escaped observation/safety envelope: %s", relative)
		}
	}
	if classification := ClassifyPath(PathObservation{RelativePath: "notes/escape.md", Exists: true, Kind: PathKindFile}, root); classification.Included {
		t.Fatal("consumer selection admitted an unsafe path")
	}
}

func TestIndependentConflictingRulesCannotEmitOutputs(t *testing.T) {
	config := independentConsumerConfig()
	config.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{
		{Path: "root/a*/note.md", Search: "none"}, {Path: "root/*b/note.md", Search: "text"},
	}}
	root := testValidatedRoot(t, config)
	classification := ClassifyPath(PathObservation{RelativePath: "ab/note.md", Safe: true, Exists: true, Kind: PathKindFile}, root)
	if classification.Included || classification.ReasonCode != ReasonExcludedFilePolicy {
		t.Fatalf("ambiguous policy was admitted: %+v", classification)
	}
	state := PathState{RelativePath: "ab/note.md", Status: PathStatusDeleted, LocalObjectID: "known-object"}
	if actions := planOutputActions(root, state); len(actions) != 0 {
		t.Fatalf("ambiguous policy produced outputs: %+v", actions)
	}
}
