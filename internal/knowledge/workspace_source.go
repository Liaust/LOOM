package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/storagecatalog"
)

// WithWritableNotesSource is a source-owner seam, not a capability authorization
// endpoint. The caller must explicitly select the collection and authorize edits.
// It works before any KnowledgeObject exists and never resolves retained copies.
func (s *Service) WithWritableNotesSource(ctx context.Context, id, nodeKey, relative string, fn func(SourceRoot, string) error) error {
	return s.WithWritableNotesSourcePaths(ctx, id, nodeKey, []string{relative}, fn)
}

// WithWritableNotesSourcePaths checks both sides of a rename under one transaction.
// The first path supplies the source generation; all paths share current policy.
func (s *Service) WithWritableNotesSourcePaths(ctx context.Context, id, nodeKey string, relatives []string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, relatives, false, fn)
}

// WithReferenceNotesSource retains the same enrollment/privacy/custody fences,
// but permits a reference snapshot, never a source write.
func (s *Service) WithReferenceNotesSource(ctx context.Context, id, nodeKey, relative string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, []string{relative}, true, fn)
}

func (s *Service) withWorkspaceSourcePaths(ctx context.Context, id, nodeKey string, relatives []string, reference bool, fn func(SourceRoot, string) error) error {
	if len(relatives) < 1 || len(relatives) > 2 {
		return fmt.Errorf("one or two Notes paths required")
	}

	if s == nil || s.store.db == nil {
		return fmt.Errorf("knowledge store required")
	}
	for _, relative := range relatives {
		if id == "" || nodeKey == "" || relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00") || strings.HasPrefix(relative, "../") {
			return fmt.Errorf("invalid writable source identity/path")
		}
	}
	// Pool admission must remain cancellable; only the acquired transaction's
	// lifetime is detached so callback cleanup can retain its source fences.
	conn, err := s.store.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Determine project lock before taking custody/root row locks, in the same
	// order used by the project archive coordinator. Recheck identity under lock.
	var project sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT project_id FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1`, id).Scan(&project); err != nil {
		return err
	}
	// Keep source locks through callback cleanup when its request is canceled.
	// Admission queries still use ctx; the deferred rollback owns transaction
	// lifetime instead of database/sql's asynchronous cancellation rollback.
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if project.Valid {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "loom:project-repository:project:id:"+project.String); err != nil {
			return err
		}
	}
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return err
	}
	root, err := scanSourceRoot(tx.QueryRowContext(ctx, `SELECT `+sourceRootColumns()+` FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1 FOR SHARE`, id))
	if err != nil {
		return err
	}
	if derefString(root.ProjectID) != project.String || root.NodeKey != nodeKey || root.Status != SourceRootStatusActive || !filepath.IsAbs(root.SourcePath) || filepath.Clean(root.SourcePath) != root.SourcePath {
		return ErrNotesCustodyPaused
	}
	if registration, ok := jsonObject(root.Metadata)["registration_metadata"].(map[string]any); ok {
		if source, ok := registration["knowledge_source"].(map[string]any); ok {
			if _, applicationData := source["application_data"]; applicationData {
				return fmt.Errorf("application-data Notes sources remain reference-only")
			}
		}
	}

	for _, relative := range relatives {
		for _, part := range strings.Split(relative, "/") {
			if strings.HasPrefix(part, ".") {
				return fmt.Errorf("hidden Notes source path denied")
			}
		}
		switch strings.ToLower(path.Ext(relative)) {
		case ".md", ".markdown", ".txt":
		case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".canvas", ".wav", ".svg", ".csv", ".xlsx", ".dat", ".json":
			if !reference {
				return fmt.Errorf("Notes source is reference-only")
			}
		default:
			return fmt.Errorf("Notes source is reference-only")
		}
		if filepolicy.IsIndexingExcludedPath(relative) || ignoredNotesKnowledgeRelativePath(relative) || expandedSourceExcluded(root, relative, nil) {
			return fmt.Errorf("Notes source path is excluded")
		}
	}
	switch root.RootKind {
	case RootKindBoxNotes, RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectNotes, RootKindProjectMaterial:
	default:
		return fmt.Errorf("unsupported writable Notes root")
	}
	// Lock the current owner node and registration rows so withdrawal/config edits
	// cannot pass between eligibility and the callback's filesystem publication.
	var role, status, key string
	if err := tx.QueryRowContext(ctx, `SELECT node_role,status,node_key FROM nodes.nodes WHERE node_id=$1 FOR SHARE`, root.NodeID).Scan(&role, &status, &key); err != nil {
		return err
	}
	if role != "main" || status != "active" || key != nodeKey {
		return ErrNotesCustodyPaused
	}
	var reportID sql.NullString
	var configHash string
	var configJSON []byte
	if root.ProjectID == nil {
		if root.BoxWatchRootRegistrationID == nil {
			return ErrNotesCustodyPaused
		}
		var owner, pathValue, backend, relativeRoot, activation string
		var metadata []byte
		err = tx.QueryRowContext(ctx, `SELECT owner_node_key,box_root_path,backend_root_key,root_relative_path,activation_status,metadata,watched_root_id,config_hash,config_json
   FROM box.watch_root_registrations WHERE box_watch_root_registration_id=$1 AND node_id=$2 FOR SHARE`, root.BoxWatchRootRegistrationID, root.NodeID).Scan(&owner, &pathValue, &backend, &relativeRoot, &activation, &metadata, &reportID, &configHash, &configJSON)
		if err != nil {
			return err
		}
		if owner != root.NodeKey || backend != root.BackendRootKey || relativeRoot != root.RootRelativePath || filepath.Join(pathValue, relativeRoot) != root.SourcePath || !workspaceWriterActive(activation) || !workspaceRegistrationMetadataMatches(root, metadata) {
			return ErrNotesCustodyPaused
		}
	} else {
		if root.ProjectWatchedRootRegistrationID == nil {
			return ErrNotesCustodyPaused
		}
		var projectStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM projects.projects WHERE project_id=$1 FOR SHARE`, root.ProjectID).Scan(&projectStatus); err != nil {
			return err
		}
		if projectStatus != "active" {
			return ErrNotesCustodyPaused
		}
		var activation, registrationID, owner, backend, relativeRoot string
		var metadata []byte
		err = tx.QueryRowContext(ctx, `SELECT activation_status,project_contract_registration_id,owner_node_key,backend_root_key,root_relative_path,metadata,watched_root_id,config_hash,config_json
   FROM projects.project_watched_root_registrations WHERE project_watched_root_registration_id=$1 AND project_id=$2 AND node_id=$3 FOR SHARE`, root.ProjectWatchedRootRegistrationID, root.ProjectID, root.NodeID).Scan(&activation, &registrationID, &owner, &backend, &relativeRoot, &metadata, &reportID, &configHash, &configJSON)
		if err != nil {
			return err
		}
		if !workspaceWriterActive(activation) || owner != root.NodeKey || backend != root.BackendRootKey || relativeRoot != root.RootRelativePath || !workspaceRegistrationMetadataMatches(root, metadata) {
			return ErrNotesCustodyPaused
		}
		var registrationStatus, projectRoot string
		if err := tx.QueryRowContext(ctx, `SELECT registration_status,project_root FROM projects.project_contract_registrations WHERE project_contract_registration_id=$1 AND project_id=$2 FOR SHARE`, registrationID, root.ProjectID).Scan(&registrationStatus, &projectRoot); err != nil {
			return err
		}
		// Application-data paths need a separate live application write contract.
		// They remain reference-only in W3a, even when readable by the indexer.
		if registrationStatus != "registered" || filepath.Join(projectRoot, root.RootRelativePath) != root.SourcePath {
			return ErrNotesCustodyPaused
		}
	}
	if !reportID.Valid || configHash == "" {
		return ErrNotesCustodyPaused
	}
	var reportStatus, reportHash, reportKey, reportNode string
	var reportConfig []byte
	if err := tx.QueryRowContext(ctx, `SELECT status,config_hash,root_key,node_id,config_json FROM watched_roots.roots WHERE watched_root_id=$1 FOR SHARE`, reportID).Scan(&reportStatus, &reportHash, &reportKey, &reportNode, &reportConfig); err != nil {
		return err
	}
	if reportStatus != "healthy" || reportHash != configHash || reportKey != root.BackendRootKey || reportNode != derefString(root.NodeID) || !workspaceJSONEqual(configJSON, reportConfig) {
		return ErrNotesCustodyPaused
	}

	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT `+declarationEnrollmentMembershipSQL("r")+` FROM knowledge.notes_source_roots r WHERE notes_source_root_id=$1`, id).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotesCustodyPaused
	}
	var policyFingerprint string
	for index, relative := range relatives {
		if err := requireNotesCustodyWriteTx(ctx, tx, KnowledgeObject{NotesSourceRootID: id, SourcePath: filepath.Join(root.SourcePath, relative), RelativePath: relative}); err != nil {
			return err
		}
		// If indexing already knows this path, its privacy/custody denial still wins;
		// absence of an index row is not a reason to deny an original-file save.
		err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM knowledge.knowledge_objects o WHERE o.notes_source_root_id=$1 AND o.relative_path=$2 AND
 (NOT `+notesCustodyWriteAllowedSQL("o")+` OR o.metadata->>'private_no_index'='true' OR o.metadata->>'index_policy'='private_no_index'
 OR EXISTS (SELECT 1 FROM files.file_metadata f LEFT JOIN objects.objects object ON object.object_id=f.object_id
 WHERE f.object_id=o.metadata->'synced_object'->>'object_id' AND (f.index_policy='private_no_index'
 OR f.metadata->>'private_no_index'='true' OR object.metadata->>'private_no_index'='true'))
 OR EXISTS (SELECT 1 FROM storage.storage_entries e WHERE e.storage_entry_id=o.storage_entry_id
 AND (e.metadata->>'private_no_index'='true' OR e.metadata->>'index_policy'='private_no_index'))))`, id, relative).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrNotesCustodyPaused
		}
		fingerprint, err := workspacePathPolicy(root, configJSON, relative)
		if err != nil {
			return err
		}
		if index == 0 {
			policyFingerprint = fingerprint
		} else if fingerprint != policyFingerprint {
			return ErrNotesCustodyPaused
		}

	}
	raw, _ := json.Marshal(struct {
		ID, Path, Node, Kind, ConfigHash, PolicyFingerprint string
		Metadata                                            json.RawMessage
	}{root.NotesSourceRootID, root.SourcePath, root.NodeKey, root.RootKind, configHash, policyFingerprint, root.Metadata})
	generation := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fn(root, generation); err != nil {
		return err
	}
	return tx.Commit()
}
func workspaceWriterActive(status string) bool { return status == "applied" || status == "reported" }
func workspaceRegistrationMetadataMatches(root SourceRoot, raw []byte) bool {
	var expected any
	if err := json.Unmarshal(raw, &expected); err != nil {
		return false
	}
	registration := jsonObject(root.Metadata)["registration_metadata"]
	a, _ := json.Marshal(registration)
	b, _ := json.Marshal(expected)
	return string(a) == string(b)
}

func workspaceJSONEqual(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

func workspacePathPolicy(root SourceRoot, raw []byte, relative string) (string, error) {
	var config rootpolicy.RootConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", err
	}
	if config.RootKey != root.BackendRootKey || len(config.Include) == 0 {
		return "", ErrNotesCustodyPaused
	}
	if err := rootpolicy.ValidatePatterns(config.Include); err != nil {
		return "", err
	}
	if err := rootpolicy.ValidatePatterns(config.Exclude); err != nil {
		return "", err
	}
	included, _ := rootpolicy.MatchAny(config.Include, relative)
	if !included {
		return "", fmt.Errorf("Notes path is outside current watched-root includes")
	}
	if config.IgnorePolicy.Profile == "" {
		excluded, _ := rootpolicy.MatchAny(config.Exclude, relative)
		if excluded {
			return "", fmt.Errorf("Notes path is excluded by current watched-root policy")
		}
		return "legacy-patterns", nil
	}
	profile, err := filepolicy.ParseProfile(config.IgnorePolicy.Profile)
	if err != nil {
		return "", err
	}
	// The compiled watched-root config names its safe-root-relative location.
	// Recover that existing anchor; never select a new arbitrary policy boundary.
	rootRelative := config.RootRelativePath
	if rootRelative == "" || path.IsAbs(rootRelative) || path.Clean(rootRelative) != rootRelative || rootRelative == ".." || strings.HasPrefix(rootRelative, "../") {
		return "", ErrNotesCustodyPaused
	}
	safeRoot := root.SourcePath
	if rootRelative != "." {
		for range strings.Split(rootRelative, "/") {
			safeRoot = filepath.Dir(safeRoot)
		}
	}
	if filepath.Join(safeRoot, filepath.FromSlash(rootRelative)) != root.SourcePath {
		return "", ErrNotesCustodyPaused
	}
	policyRelative := config.IgnorePolicy.PolicyRootRelativePath
	if policyRelative == "" {
		policyRelative = "."
	}
	if path.IsAbs(policyRelative) || path.Clean(policyRelative) != policyRelative || policyRelative == ".." || strings.HasPrefix(policyRelative, "../") {
		return "", ErrNotesCustodyPaused
	}
	resolver, err := filepolicy.NewResolver(root.SourcePath, profile, filepolicy.ResolverOptions{
		DiscoverUserRules: config.IgnorePolicy.DiscoverUserRules, PolicyRoot: filepath.Join(safeRoot, filepath.FromSlash(policyRelative)),
		ContractExcludes: config.Exclude, MaxPolicyBytes: 1 << 20, DiscoveryDeadline: time.Now().Add(2 * time.Second),
	})
	if err != nil {
		return "", err
	}
	resolution, err := resolver.Resolve(relative, false)
	if err != nil {
		return "", err
	}
	if !resolution.Decision.Included {
		return "", fmt.Errorf("Notes path is excluded by current file policy")
	}
	return resolver.Fingerprint(), nil
}
