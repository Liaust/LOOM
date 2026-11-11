package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

// The anchor retains the verified generation inputs, not permission. Every
// source read/write still requires current owner, custody and path admission.
type workspaceAuthority struct {
	Root          workspaceRootIdentity `json:"root"`
	ConfigHash    string                `json:"config_hash"`
	Config        json.RawMessage       `json:"config"`
	Metadata      json.RawMessage       `json:"metadata"`
	Authorization json.RawMessage       `json:"authorization"`
	Epoch         string                `json:"epoch,omitempty"`
}

func nullableWorkspaceAuthority(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

type workspaceRootIdentity struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Node     string  `json:"node"`
	NodeID   *string `json:"node_id"`
	Project  *string `json:"project"`
	Path     string  `json:"path"`
	Relative string  `json:"relative"`
	Backend  string  `json:"backend"`
}

func workspaceRoot(root SourceRoot) workspaceRootIdentity {
	return workspaceRootIdentity{root.NotesSourceRootID, root.RootKind, root.NodeKey,
		root.NodeID, root.ProjectID, root.SourcePath, root.RootRelativePath, root.BackendRootKey}
}

func normalizedSourceDeclaration(root SourceRoot) any {
	source, ok := sourcePolicyDeclaration(root).(map[string]any)
	if ok {
		delete(source, "config_hash")
	}
	return source
}

func workspaceSourceDeclaration(root SourceRoot, config watchedroots.RootConfig) any {
	source, _ := normalizedSourceDeclaration(root).(map[string]any)
	if version, _ := source["schema_version"].(string); projectcontracts.IsDeclarationSchemaVersion(version) {
		// Current source closure/owner ACKs are checked before this fingerprint.
		// A source-byte digest is audit evidence, not the client's edit authority.
		delete(source, "source_hash")
	}
	// Automatic enrichment never changes the file's read/write selection. Keep
	// full policy in admission/pipeline evidence, not in the client edit epoch.
	if policy, ok := source["policy"].(map[string]any); ok {
		delete(policy, "files")
		if len(policy) == 0 {
			delete(source, "policy")
		}
	}
	// Legacy Box Notes may have no knowledge_source envelope at all. The new
	// compiler carries the same existing selectors beside files; that redundant
	// wrapper is not new write authority. Other metadata/selector changes remain.
	if root.RootKind == RootKindBoxNotes && len(source) == 2 {
		include, includeOK := expandedSourcePatterns(source["include"])
		exclude, excludeOK := expandedSourcePatterns(source["exclude"])
		_, hasInclude := source["include"]
		_, hasExclude := source["exclude"]
		if hasInclude && hasExclude && includeOK && excludeOK && slices.Equal(include, config.Include) && slices.Equal(exclude, config.Exclude) {
			return nil
		}
	}
	return source
}

func workspaceAuthorityFingerprint(root SourceRoot, config json.RawMessage) (string, error) {
	var parsed watchedroots.RootConfig
	if err := json.Unmarshal(config, &parsed); err != nil || parsed.RootKey != root.BackendRootKey {
		return "", ErrNotesCustodyPaused
	}
	var authorization any
	if err := json.Unmarshal(root.AuthorizationMetadata, &authorization); err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Root          workspaceRootIdentity
		Config        string
		Declaration   any
		Authorization any
	}{workspaceRoot(root), watchedroots.ContentConfigHash(parsed), workspaceSourceDeclaration(root, parsed), authorization})
	if err != nil {
		return "", err
	}
	return hashArtifactValue(string(raw)), nil
}

func (a workspaceAuthority) source() SourceRoot {
	return SourceRoot{NotesSourceRootID: a.Root.ID, RootKind: a.Root.Kind,
		NodeKey: a.Root.Node, NodeID: a.Root.NodeID, ProjectID: a.Root.Project,
		SourcePath: a.Root.Path, RootRelativePath: a.Root.Relative, BackendRootKey: a.Root.Backend,
		Metadata: a.Metadata, AuthorizationMetadata: a.Authorization}
}

func (a workspaceAuthority) matches(root SourceRoot, config json.RawMessage) bool {
	if a.ConfigHash == "" || !reflect.DeepEqual(a.Root, workspaceRoot(root)) {
		return false
	}
	old, err := workspaceAuthorityFingerprint(a.source(), a.Config)
	if err != nil {
		return false
	}
	next, err := workspaceAuthorityFingerprint(root, config)
	return err == nil && old == next
}

func workspaceSourceGeneration(root SourceRoot, configHash string, config json.RawMessage, policy string) string {
	var anchor workspaceAuthority
	if json.Unmarshal(root.WorkspaceAuthority, &anchor) != nil || !anchor.matches(root, config) {
		anchor = workspaceAuthority{Root: workspaceRoot(root), ConfigHash: configHash, Metadata: root.Metadata}
	}
	raw, _ := json.Marshal(struct {
		ID, Path, Node, Kind, ConfigHash, PolicyFingerprint string
		Metadata                                            json.RawMessage
	}{anchor.Root.ID, anchor.Root.Path, anchor.Root.Node, anchor.Root.Kind, anchor.ConfigHash, policy, anchor.Metadata})
	if anchor.Epoch != "" {
		raw = append(raw, []byte("\n"+anchor.Epoch)...)
	}
	return hashArtifactValue(string(raw))
}

// Operational ACK metadata may change without changing admission membership.
// Keep the exact old admission declaration across that compatible change.
func admissionSourceDeclaration(root SourceRoot) any {
	var anchor workspaceAuthority
	if json.Unmarshal(root.WorkspaceAuthority, &anchor) == nil &&
		reflect.DeepEqual(anchor.Root, workspaceRoot(root)) &&
		reflect.DeepEqual(normalizedSourceDeclaration(anchor.source()), normalizedSourceDeclaration(root)) {
		return sourcePolicyDeclaration(anchor.source())
	}
	return sourcePolicyDeclaration(root)
}

func prepareWorkspaceAuthorityTx(ctx context.Context, tx *sql.Tx, root SourceRoot, previous SourceRoot) (json.RawMessage, error) {
	var configHash string
	var config []byte
	query := `SELECT config_hash,config_json FROM box.watch_root_registrations WHERE box_watch_root_registration_id=$1`
	var id any = root.BoxWatchRootRegistrationID
	if root.ProjectWatchedRootRegistrationID != nil {
		query = `SELECT config_hash,config_json FROM projects.project_watched_root_registrations WHERE project_watched_root_registration_id=$1`
		id = root.ProjectWatchedRootRegistrationID
	}
	if err := tx.QueryRowContext(ctx, query, id).Scan(&configHash, &config); err != nil {
		if err == sql.ErrNoRows {
			return previous.WorkspaceAuthority, nil
		}
		return nil, err
	}
	var old workspaceAuthority
	if json.Unmarshal(previous.WorkspaceAuthority, &old) == nil && old.matches(root, config) {
		return previous.WorkspaceAuthority, nil
	}
	anchor := workspaceAuthority{Root: workspaceRoot(root), ConfigHash: configHash,
		Config: config, Metadata: root.Metadata, Authorization: root.AuthorizationMetadata}
	if len(previous.WorkspaceAuthority) > 0 {
		fingerprint, err := workspaceAuthorityFingerprint(root, config)
		if err != nil {
			return nil, fmt.Errorf("workspace authority configuration: %w", err)
		}
		anchor.Epoch = fingerprint
	}
	return json.Marshal(anchor)
}
