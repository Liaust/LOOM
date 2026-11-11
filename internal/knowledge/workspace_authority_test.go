package knowledge

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestWorkspaceAuthorityPreservesLegacyGenerationAcrossOperationalChanges(t *testing.T) {
	root := SourceRoot{NotesSourceRootID: "root", RootKind: RootKindBoxNotes, NodeKey: "main",
		SourcePath: "/box/Notes", BackendRootKey: "notes", RootRelativePath: "Notes",
		Metadata:              json.RawMessage(`{"origin":"box.watch_root_registration","registration_metadata":{"knowledge_source":{"include":["**/*"],"exclude":[],"config_hash":"old"}}}`),
		AuthorizationMetadata: json.RawMessage(`{"authority":"source_root","root_kind":"box_notes"}`)}
	config := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "notes", SafeRootKey: "box", RootRelativePath: "Notes", Include: []string{"**/*"}})
	raw, _ := json.Marshal(config)
	legacy := workspaceSourceGeneration(root, watchedroots.ConfigHash(config), raw, "policy")
	anchor := workspaceAuthority{Root: workspaceRoot(root), ConfigHash: watchedroots.ConfigHash(config),
		Config: raw, Metadata: root.Metadata, Authorization: root.AuthorizationMetadata}
	root.WorkspaceAuthority, _ = json.Marshal(anchor)
	before, _ := admissionSourceGeneration([]SourceRoot{root})
	if got := workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy"); got != legacy {
		t.Fatal("adoption rotated legacy generation", got, legacy)
	}
	config.Scan.FullRescanInterval, config.Scan.MaxRuntime, config.Watch.DebounceWindow = "12h", "1m", "5s"
	config.DisplayName = "renamed display"
	raw, _ = json.Marshal(config)
	root.Metadata = json.RawMessage(`{"origin":"box.watch_root_registration","registration_metadata":{"knowledge_source":{"include":["**/*"],"exclude":[],"config_hash":"new"}}}`)
	root.AuthorizationMetadata = json.RawMessage(`{"root_kind": "box_notes", "authority": "source_root"}`)
	if got := workspaceSourceGeneration(root, watchedroots.ConfigHash(config), raw, "policy"); got != legacy {
		t.Fatal("operational update rotated authority", got, legacy)
	}
	after, _ := admissionSourceGeneration([]SourceRoot{root})
	if before != after {
		t.Fatal("operational update restarted admission")
	}
	if workspaceSourceGeneration(root, watchedroots.ConfigHash(config), raw, "changed-policy") == legacy {
		t.Fatal("live path-policy change kept old authority")
	}
	config.Include = []string{"selected/**"}
	raw, _ = json.Marshal(config)
	if workspaceSourceGeneration(root, watchedroots.ConfigHash(config), raw, "policy") == legacy {
		t.Fatal("material scope change kept old authority")
	}
}

func TestFolderPolicyChangesProcessingNotClientAuthority(t *testing.T) {
	root := SourceRoot{NotesSourceRootID: "root", RootKind: RootKindBoxNotes, NodeKey: "main",
		SourcePath: "/box/Notes", BackendRootKey: "notes", RootRelativePath: "Notes",
		Metadata:              json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["**/*"],"exclude":[]}}}`),
		AuthorizationMetadata: json.RawMessage(`{"authority":"source_root"}`)}
	config := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "notes", SafeRootKey: "box", RootRelativePath: "Notes", Include: []string{"**/*"}})
	raw, _ := json.Marshal(config)
	anchor := workspaceAuthority{Root: workspaceRoot(root), ConfigHash: watchedroots.ConfigHash(config),
		Config: raw, Metadata: root.Metadata, Authorization: root.AuthorizationMetadata}
	root.WorkspaceAuthority, _ = json.Marshal(anchor)
	before := workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy")
	admission, _ := admissionSourceGeneration([]SourceRoot{root})
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["**/*"],"exclude":[],"policy":{"files":{"rules":[{"path":"Notes/books","enrichment":[]}]}}}}}`)
	if got := workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy"); got != before {
		t.Fatal("automatic enrichment rotated pending client edit bindings")
	}
	after, _ := admissionSourceGeneration([]SourceRoot{root})
	if after == admission {
		t.Fatal("processing policy change was hidden from admission")
	}
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["selected/**"],"exclude":[],"policy":{"files":{"rules":[{"path":"Notes/books","enrichment":[]}]}}}}}`)
	if workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy") == before {
		t.Fatal("real path selection change kept old client authority")
	}
}

func TestDeclarationEnrichmentAuditPreservesClientGeneration(t *testing.T) {
	root := SourceRoot{NotesSourceRootID: "root", RootKind: RootKindProjectMaterial, NodeKey: "main", SourcePath: "/project", RootRelativePath: ".", BackendRootKey: "files",
		Metadata: json.RawMessage(`{"registration_metadata":{"knowledge_source":{"schema_version":"project.contract.v0.6","source_hash":"before","include":["**/*"],"exclude":[]}}}`), AuthorizationMetadata: json.RawMessage(`{"authority":"source_root"}`)}
	config := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "files", SafeRootKey: "project"})
	raw, _ := json.Marshal(config)
	anchor := workspaceAuthority{Root: workspaceRoot(root), ConfigHash: watchedroots.ConfigHash(config), Config: raw, Metadata: root.Metadata, Authorization: root.AuthorizationMetadata}
	root.WorkspaceAuthority, _ = json.Marshal(anchor)
	before := workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy")
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"schema_version":"project.contract.v0.6","source_hash":"after","include":["**/*"],"exclude":[],"policy":{"files":{"rules":[{"path":"books","enrichment":[]}]}}}}}`)
	if workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy") != before {
		t.Fatal("enrichment/source digest invalidated retained edits")
	}
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"schema_version":"project.contract.v0.6","source_hash":"after","include":["selected/**"],"exclude":[]}}}`)
	if workspaceSourceGeneration(root, anchor.ConfigHash, raw, "policy") == before {
		t.Fatal("real selection change preserved old generation")
	}
}
