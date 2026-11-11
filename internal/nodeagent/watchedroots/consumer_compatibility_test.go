package watchedroots

import "testing"

func TestEquivalentLegacyConsumerIdentity(t *testing.T) {
	old := NormalizeRootConfig(RootConfig{RootKey: "notes", SafeRootKey: "box", SyncPolicy: SyncPolicy{Mode: SyncModeSelectedFiles, ScopeRef: "notes"},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText}, BackupPolicy: BackupPolicy{Mode: BackupModeIncrementalRaw}})
	next := old
	next.SchemaVersion = IndependentConfigSchemaVersion
	next.ConsumerSelections = &ConsumerSelections{Sync: &PathSelection{Include: LegacyTextSelection(), Exclude: []string{}}}
	if ConfigHash(old) == ConfigHash(next) || ContentConfigHash(old) != ContentConfigHash(next) {
		t.Fatal("equivalent spelling lost exact ACK or stable content identity")
	}
	next.ConsumerSelections.Sync.Include = []string{"**/*"}
	if ContentConfigHash(old) == ContentConfigHash(next) {
		t.Fatal("broader binary selection preserved old identity")
	}
}

func TestEquivalentUncoupledConsumerIdentity(t *testing.T) {
	for _, backup := range []string{BackupModeNone, BackupModeIncrementalRaw} {
		old := NormalizeRootConfig(RootConfig{RootKey: "files", SafeRootKey: "box", BackupPolicy: BackupPolicy{Mode: backup}, IndexPolicy: IndexPolicy{Mode: IndexModeMetadataOnly}})
		next := old
		next.SchemaVersion = IndependentConfigSchemaVersion
		if ContentConfigHash(old) != ContentConfigHash(next) {
			t.Fatal("equivalent independent spelling restarted work")
		}
	}
}
