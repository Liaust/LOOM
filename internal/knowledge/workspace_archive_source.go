package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filepolicy"
	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

// WorkspaceArchiveSourceReader is a local source-owner seam, not caller access
// authorization. Callers must select/authorize the collection independently.
// It neither enrolls sources nor permits writes through the writable resolver.
type WorkspaceArchiveSourceReader struct {
	Knowledge *Service
	Workspace storagearchive.WorkspaceMoveService
	NodeKey   string
}

// ArchiveSourceReader reuses the already-configured local archive owner,
// including its trusted roots and manifest-key provider. Nil means unavailable.
func (s *Service) ArchiveSourceReader(owner *storagearchive.WorkspaceMoveService, nodeKey string) *WorkspaceArchiveSourceReader {
	if s == nil || owner == nil {
		return nil
	}
	return &WorkspaceArchiveSourceReader{Knowledge: s, Workspace: *owner, NodeKey: nodeKey}
}

type WorkspaceArchiveFile struct {
	RelativePath string `json:"relative_path"`
	SourceHash   string `json:"source_hash"`
	SizeBytes    int64  `json:"size_bytes"`
}

type WorkspaceArchiveSource struct {
	Root               SourceRoot             `json:"root"`
	CanonicalRoot      string                 `json:"canonical_root"`
	Generation         string                 `json:"generation"`
	ArchiveOperationID string                 `json:"archive_operation_id"`
	ManifestDigest     string                 `json:"manifest_digest"`
	ReadOnly           bool                   `json:"read_only"`
	Files              []WorkspaceArchiveFile `json:"files"`
}

// WithRoot retains project/custody/source fences through the callback. Root
// preserves the original SourceRoot identity/path; CanonicalRoot is read-only.
// Files come only from authenticated inventory, including never-indexed files.
// Do not cache a path as authority: every payload read must use WithFile.
func (r WorkspaceArchiveSourceReader) WithRoot(ctx context.Context, id string, fn func(WorkspaceArchiveSource) error) error {
	if r.Knowledge == nil || r.Knowledge.store.db == nil || id == "" || r.NodeKey == "" || fn == nil {
		return fmt.Errorf("%w: trusted archive reader and source identity required", ErrInvalid)
	}
	conn, err := r.Knowledge.store.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var projectID string
	if err := conn.QueryRowContext(ctx, `SELECT project_id FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1 AND project_id IS NOT NULL`, id).Scan(&projectID); err != nil {
		return err
	}
	// Match the writable source/coordinator lock order. Cancellation must not
	// release custody while callback cleanup still holds an archive file open.
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "loom:project-repository:project:id:"+projectID); err != nil {
		return err
	}
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return err
	}
	root, err := scanSourceRoot(tx.QueryRowContext(ctx, `SELECT `+sourceRootColumns()+` FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1 FOR SHARE`, id))
	if err != nil {
		return err
	}
	if derefString(root.ProjectID) != projectID || root.NodeKey != r.NodeKey ||
		(root.RootKind != RootKindProjectNotes && root.RootKind != RootKindProjectMaterial) ||
		(root.Status != SourceRootStatusActive && root.Status != SourceRootStatusDisabled) {
		return ErrNotesCustodyPaused
	}
	var stateJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT archive_state FROM projects.projects WHERE project_id=$1 AND status='archived' FOR SHARE`, projectID).Scan(&stateJSON); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotesCustodyPaused
		}
		return err
	}
	state, valid := projects.ParseProjectPhysicalArchiveState(stateJSON)
	if !valid || state.Restore != nil || state.Phase != projects.ProjectArchivePhaseComplete {
		return ErrNotesCustodyPaused
	}
	if err := lockNotesUpstream(ctx, tx, state.OperationID); err != nil {
		return err
	}
	var current bool
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle_state='archived' AND restore_operation_id IS NULL
	 AND NOT EXISTS (SELECT 1 FROM storage.workspace_archive_operations moving
	 WHERE moving.operation_kind='restore' AND moving.plan_json->>'archive_operation_id'=$1)
	 FROM storage.workspace_archive_manifests WHERE workspace_archive_operation_id=$1`, state.OperationID).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrNotesCustodyPaused
	}
	owner := r.Workspace
	owner.Journal = notesCustodyJournal{tx: tx}
	evidence, err := owner.ReadLifecycleEvidence(ctx, state.OperationID)
	if err != nil {
		return err
	}
	if evidence.Plan.Kind != storagearchive.WorkspaceKindProject || evidence.Plan.ObjectID != projectID || evidence.Plan.OperationKind != storagearchive.WorkspaceOperationArchive {
		return ErrNotesCustodyPaused
	}
	if _, err := notesProjectCompletion(ctx, tx, evidence); err != nil {
		return err
	}
	var payloadPath string
	if err := tx.QueryRowContext(ctx, `SELECT storage.resolve_custody_path($1,$2)`, "workspace:"+state.OperationID, evidence.Plan.Destination.Path.AbsolutePath).Scan(&payloadPath); err != nil {
		return err
	}
	held, err := openArchiveWorkspacePath(payloadPath, true)
	if err != nil {
		return err
	}
	defer held.close()
	var payload unix.Stat_t
	if err := unix.Fstat(int(held.current().Fd()), &payload); err != nil {
		return err
	}
	identity := evidence.ArchivePlan.Inventory.RootIdentity
	if uint64(payload.Dev) != identity.DeviceID || payload.Ino != identity.Inode {
		return ErrFileSourceChanged
	}
	if !notesCustodyRelativePath(root.RootRelativePath) {
		return ErrNotesCustodyPaused
	}
	collection, err := openArchiveWorkspacePath(filepath.Join(payloadPath, filepath.FromSlash(root.RootRelativePath)), true)
	if err != nil {
		return err
	}
	defer collection.close()
	var collectionStat unix.Stat_t
	if err := unix.Fstat(int(collection.current().Fd()), &collectionStat); err != nil {
		return err
	}
	member := false
	for _, entry := range evidence.ArchivePlan.Inventory.Entries {
		if entry.RelativePath == root.RootRelativePath && entry.Kind == storagearchive.InventoryEntryDirectory &&
			uint64(collectionStat.Dev) == entry.DeviceID && collectionStat.Ino == entry.Inode {
			member = true
			break
		}
	}
	if !member {
		return ErrFileSourceChanged
	}
	config, declaration, err := archiveWorkspaceDeclaration(ctx, tx, root, state.OperationID)
	if err != nil {
		return err
	}
	// Source reconciliation after deactivation replaces registration metadata
	// with its stop receipt. Recover the declaration from the accepted plan, not
	// an indexed object's historical metadata or an arbitrary filesystem scan.
	policyRoot := root
	metadata := jsonObject(root.Metadata)
	registration := map[string]any{}
	if source := jsonObject(declaration); len(source) != 0 {
		registration["knowledge_source"] = source
	}
	metadata["registration_metadata"] = registration
	policyRoot.Metadata, err = json.Marshal(metadata)
	if err != nil {
		return err
	}
	source, err := archiveWorkspaceSelection(ctx, policyRoot, config, evidence, payloadPath)
	if err != nil {
		return err
	}
	source.Root = root
	denied, err := archiveWorkspacePrivatePaths(ctx, tx, root)
	if err != nil {
		return err
	}
	files := source.Files[:0]
	for _, file := range source.Files {
		original := filepath.Join(root.SourcePath, filepath.FromSlash(file.RelativePath))
		canonical := filepath.Join(source.CanonicalRoot, filepath.FromSlash(file.RelativePath))
		workspacePath := filepath.ToSlash(filepath.Join(evidence.Plan.Source.Path.RelativePath, root.RootRelativePath, file.RelativePath))
		if !denied[file.RelativePath] && !denied[original] && !denied[canonical] && !denied[workspacePath] && !denied[(&url.URL{Scheme: "file", Path: original}).String()] {
			files = append(files, file)
		}
	}
	source.Files = files
	// Privacy changes invalidate a previously selected delivery generation too.
	source.Generation = archiveWorkspaceHash(source.Generation, files)
	if err := held.check(); err != nil {
		return err
	}
	if err := collection.check(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fn(source); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := held.check(); err != nil {
		return err
	}
	if err := collection.check(); err != nil {
		return err
	}
	afterPrivacy, err := archiveWorkspacePrivatePaths(ctx, tx, root)
	if err != nil {
		return err
	}
	if archiveWorkspaceHash(denied) != archiveWorkspaceHash(afterPrivacy) {
		return ErrFileSourceChanged
	}
	return tx.Commit()
}

func archiveWorkspaceDeclaration(ctx context.Context, tx *sql.Tx, root SourceRoot, operation string) ([]byte, []byte, error) {
	// Owner reports take the report lock before updating the registration.
	// Preserve that order even for a stopped writer's late report.
	var reportID string
	if err := tx.QueryRowContext(ctx, `SELECT report.watched_root_id FROM watched_roots.roots report
	 JOIN projects.project_watched_root_registrations writer USING(watched_root_id)
	 WHERE writer.project_watched_root_registration_id=$1 AND writer.node_id=$2 FOR SHARE OF report`, root.ProjectWatchedRootRegistrationID, root.NodeID).Scan(&reportID); err != nil {
		return nil, nil, err
	}
	var config, declaration []byte
	err := tx.QueryRowContext(ctx, archiveWorkspaceDeclarationSQL(), root.NotesSourceRootID, operation, reportID).Scan(&config, &declaration)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: archive declaration unavailable: %w", ErrNotesCustodyPaused, err)
	}
	return config, declaration, nil
}

func archiveWorkspaceDeclarationSQL() string {
	stop := notesProjectArchiveStopSQL("r", "candidate", "$2")
	effective := `COALESCE(r.metadata#>'{registration_metadata,knowledge_source}',expected.value#>'{metadata,knowledge_source}')`
	versioned := declarationRegistrationAuthoritySQL("r", stop+` AND accepted.registration_status IN ('registered','archived') AND `+
		declarationWatchSourceMatchesSQL("r", effective, `expected.value#>'{metadata,knowledge_source}'`))
	// Retained Notes keep their original notes_contract identity after declaration
	// adoption; material authority intentionally cannot admit that identity.
	retained := declarationLegacyNotesAuthoritySQL("r", "true", stop)
	// Legacy roots still require exact accepted configuration/metadata. A missing
	// declaration is permitted only for legacy project_notes, never material.
	legacy := `(` + declarationUnversionedRootSQL("r") + ` AND accepted.contract_schema_version NOT IN ('project.contract.v0.5','project.contract.v0.6')
	 AND writer.config_json=expected.value->'config_json' AND writer.config_hash=expected.value->>'config_hash'
	 AND writer.source_kinds_json=expected.value->'source_kinds'
	 AND (r.status='disabled' OR r.metadata->'registration_metadata'=expected.value->'metadata'))`
	return `SELECT writer.config_json,COALESCE(` + effective + `,'{}'::jsonb)
	 FROM knowledge.notes_source_roots r
	 JOIN nodes.nodes node ON node.node_id=r.node_id AND node.node_key=r.node_key AND node.node_role='main' AND node.status='active'
	 JOIN projects.project_watched_root_registrations writer ON writer.project_watched_root_registration_id=r.project_watched_root_registration_id
	 JOIN projects.project_contract_registrations accepted USING(project_contract_registration_id)
	 JOIN watched_roots.roots report ON report.watched_root_id=writer.watched_root_id
	 CROSS JOIN LATERAL (SELECT r.project_id,r.node_id AS source_node_id,r.node_key AS source_node_key) candidate
	 CROSS JOIN LATERAL jsonb_array_elements(accepted.registration_plan_json->'watched_roots') expected
	 WHERE r.notes_source_root_id=$1 AND ` + stop + `
	 AND accepted.project_id=r.project_id AND accepted.registration_status IN ('registered','archived')
	 AND accepted.project_root || CASE WHEN r.root_relative_path='.' THEN '' ELSE '/' || r.root_relative_path END=r.source_path
	 AND expected.value->>'backend_root_key'=r.backend_root_key
	 AND expected.value->>'root_relative_path'=r.root_relative_path
	 AND expected.value->>'owner_node'=r.node_key
	 AND report.node_id=r.node_id AND report.root_key=r.backend_root_key
	 AND report.watched_root_id=$3
	 AND report.config_json=writer.config_json AND report.config_hash=writer.config_hash
	 AND (` + versioned + ` OR ` + legacy + ` OR ` + retained + `)
	 FOR SHARE OF writer,accepted,report,node`
}

func archiveWorkspacePrivatePaths(ctx context.Context, tx *sql.Tx, root SourceRoot) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.relative_path FROM knowledge.knowledge_objects o
	 WHERE o.notes_source_root_id=$1 AND (o.deleted_at IS NOT NULL OR o.metadata->>'private_no_index'='true' OR o.metadata->>'index_policy'='private_no_index'
	 OR EXISTS (SELECT 1 FROM files.file_metadata f JOIN objects.objects obj USING(object_id)
	 LEFT JOIN objects.object_versions v ON v.object_version_id=f.latest_version_id
	 WHERE f.object_id=COALESCE(o.canonical_file_id,o.metadata#>>'{synced_object,object_id}')
	 AND (f.index_policy='private_no_index' OR f.metadata->>'private_no_index'='true' OR obj.metadata->>'private_no_index'='true'
	 OR f.metadata->>'index_policy'='private_no_index' OR obj.metadata->>'index_policy'='private_no_index'
	 OR v.metadata->>'private_no_index'='true' OR v.metadata->>'index_policy'='private_no_index'))
	 OR EXISTS (SELECT 1 FROM storage.storage_entries entry WHERE entry.storage_entry_id=o.storage_entry_id
	 AND (entry.metadata->>'private_no_index'='true' OR entry.metadata->>'index_policy'='private_no_index')))
	 UNION SELECT substring(f.source_path FROM char_length($2::text)+1)
	 FROM files.file_metadata f JOIN objects.objects obj USING(object_id)
	 LEFT JOIN objects.object_versions v ON v.object_version_id=f.latest_version_id
	 WHERE f.source_node_id=$3 AND starts_with(f.source_path,$2)
	 AND (f.index_policy='private_no_index' OR f.metadata->>'private_no_index'='true' OR obj.metadata->>'private_no_index'='true'
	 OR f.metadata->>'index_policy'='private_no_index' OR obj.metadata->>'index_policy'='private_no_index'
	 OR v.metadata->>'private_no_index'='true' OR v.metadata->>'index_policy'='private_no_index')
	 UNION SELECT unnest(ARRAY[entry.original_source_path,entry.logical_path,entry.current_view_path])
	 FROM storage.storage_entries entry WHERE entry.project_id=$4 AND entry.watched_root_key=$5
	 AND (entry.origin_node_id=$3 OR entry.origin_node_key=$6)
	 AND (entry.metadata->>'private_no_index'='true' OR entry.metadata->>'index_policy'='private_no_index')`,
		root.NotesSourceRootID, "watched-root://"+root.BackendRootKey+"/", root.NodeID, root.ProjectID, root.BackendRootKey, root.NodeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	denied := map[string]bool{}
	for rows.Next() {
		var relative sql.NullString
		if err := rows.Scan(&relative); err != nil {
			return nil, err
		}
		if relative.Valid {
			denied[relative.String] = true
		}
	}
	return denied, rows.Err()
}

func archiveWorkspaceSelection(ctx context.Context, root SourceRoot, raw []byte, e storagearchive.WorkspaceLifecycleEvidence, currentPayload ...string) (WorkspaceArchiveSource, error) {
	var out WorkspaceArchiveSource
	if e.Plan.Kind != storagearchive.WorkspaceKindProject || e.Plan.OperationKind != storagearchive.WorkspaceOperationArchive ||
		e.Manifest.LifecycleState != storagearchive.WorkspaceLifecycleArchived || derefString(root.ProjectID) != e.Plan.ObjectID ||
		!notesCustodyRelativePath(root.RootRelativePath) || root.SourcePath != filepath.Join(e.Plan.Source.Path.AbsolutePath, filepath.FromSlash(root.RootRelativePath)) {
		return out, ErrNotesCustodyPaused
	}
	var config rootpolicy.RootConfig
	if json.Unmarshal(raw, &config) != nil || config.RootKey != root.BackendRootKey || len(config.Include) == 0 ||
		rootpolicy.ValidatePatterns(config.Include) != nil || rootpolicy.ValidatePatterns(config.Exclude) != nil {
		return out, ErrNotesCustodyPaused
	}
	if workspace, err := independentWorkspacePolicy(root); err != nil {
		return out, err
	} else if workspace != nil {
		config.Include, config.Exclude = workspace.Include, workspace.Exclude
	}
	declaration, _ := sourceDeclaration(root).(map[string]any)
	if root.RootKind == RootKindProjectMaterial && (declaration == nil || declaration["enabled"] != true) {
		return out, ErrNotesCustodyPaused
	}
	if declaration != nil && declaration["enabled"] != true {
		return out, ErrNotesCustodyPaused
	}
	payload := e.Plan.Destination.Path.AbsolutePath
	if len(currentPayload) > 0 {
		payload = currentPayload[0]
	}
	out = WorkspaceArchiveSource{Root: root, CanonicalRoot: filepath.Join(payload, filepath.FromSlash(root.RootRelativePath)),
		ArchiveOperationID: e.Plan.OperationID, ManifestDigest: e.ManifestDigest, ReadOnly: true, Files: []WorkspaceArchiveFile{}}
	resolver, err := archiveWorkspacePolicy(ctx, root, config, e, payload)
	if err != nil {
		return WorkspaceArchiveSource{}, err
	}
	prefix := root.RootRelativePath + "/"
	for _, entry := range e.ArchivePlan.Inventory.Entries {
		if err := ctx.Err(); err != nil {
			return WorkspaceArchiveSource{}, err
		}
		if entry.Kind != storagearchive.InventoryEntryFile || !strings.HasPrefix(entry.RelativePath, prefix) {
			continue
		}
		relative := strings.TrimPrefix(entry.RelativePath, prefix)
		if workspaceSourcePathAllowed(root, relative, true, false) != nil {
			continue
		}
		included, _ := rootpolicy.MatchAny(config.Include, relative)
		excluded, _ := rootpolicy.MatchAny(config.Exclude, relative)
		if !included || excluded {
			continue
		}
		if resolver != nil {
			resolution, err := resolver.Resolve(relative, false)
			if err != nil {
				return WorkspaceArchiveSource{}, err
			}
			if !resolution.Decision.Included {
				continue
			}
		}
		out.Files = append(out.Files, WorkspaceArchiveFile{RelativePath: relative, SourceHash: entry.ContentDigest, SizeBytes: entry.SizeBytes})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].RelativePath < out.Files[j].RelativePath })
	out.Generation = archiveWorkspaceHash(root, raw, e.Plan.OperationID, e.Event.EventID, e.ManifestDigest)
	return out, nil
}

func archiveWorkspaceHash(values ...any) string {
	raw, _ := json.Marshal(values)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func archiveWorkspacePolicy(ctx context.Context, root SourceRoot, config rootpolicy.RootConfig, e storagearchive.WorkspaceLifecycleEvidence, currentPayload ...string) (*filepolicy.Resolver, error) {
	if config.IgnorePolicy.Profile == "" {
		return nil, nil
	}
	profile, err := filepolicy.ParseProfile(config.IgnorePolicy.Profile)
	if err != nil {
		return nil, err
	}
	// Map the existing policy boundary, never infer a wider archive boundary.
	if config.RootRelativePath != "." && !notesCustodyRelativePath(config.RootRelativePath) {
		return nil, ErrNotesCustodyPaused
	}
	safe := root.SourcePath
	if config.RootRelativePath != "." {
		for range strings.Split(config.RootRelativePath, "/") {
			safe = filepath.Dir(safe)
		}
	}
	if filepath.Join(safe, filepath.FromSlash(config.RootRelativePath)) != root.SourcePath {
		return nil, ErrNotesCustodyPaused
	}
	policy := config.IgnorePolicy.PolicyRootRelativePath
	if policy == "" {
		policy = "."
	}
	if policy != "." && !notesCustodyRelativePath(policy) {
		return nil, ErrNotesCustodyPaused
	}
	boundary, err := filepath.Rel(e.Plan.Source.Path.AbsolutePath, filepath.Join(safe, filepath.FromSlash(policy)))
	if err != nil || (boundary != "." && !notesCustodyRelativePath(filepath.ToSlash(boundary))) {
		return nil, fmt.Errorf("%w: policy boundary is not retained in this archive", ErrNotesCustodyPaused)
	}
	payload := e.Plan.Destination.Path.AbsolutePath
	if len(currentPayload) > 0 {
		payload = currentPayload[0]
	}
	canonical := filepath.Join(payload, filepath.FromSlash(root.RootRelativePath))
	resolver, err := filepolicy.NewResolver(canonical, profile, filepolicy.ResolverOptions{DiscoverUserRules: config.IgnorePolicy.DiscoverUserRules,
		PolicyRoot: filepath.Join(payload, boundary), ContractExcludes: config.Exclude,
		MaxPolicyBytes: 1 << 20, MaxDiscoveryEntries: len(e.ArchivePlan.Inventory.Entries) + 1, DiscoveryDeadline: time.Now().Add(2 * time.Second)})
	if err != nil {
		return nil, err
	}
	inventory := map[string]storagearchive.NoFollowInventoryEntry{}
	for _, entry := range e.ArchivePlan.Inventory.Entries {
		inventory[entry.RelativePath] = entry
	}
	seen := map[string]bool{}
	for _, policyFile := range resolver.PolicyFiles() {
		relative, err := filepath.Rel(payload, policyFile.Path)
		entry, found := inventory[filepath.ToSlash(relative)]
		if err != nil || !found || entry.Kind != storagearchive.InventoryEntryFile || entry.ContentDigest != policyFile.ContentHash {
			return nil, ErrNotesCustodyPaused
		}
		seen[entry.RelativePath] = true
		if err := withArchiveWorkspaceFile(ctx, policyFile.Path, WorkspaceArchiveFile{SizeBytes: entry.SizeBytes, SourceHash: entry.ContentDigest}, func(io.Reader) error { return nil }); err != nil {
			return nil, err
		}
	}
	if config.IgnorePolicy.DiscoverUserRules && profile != filepolicy.ProfileExact {
		for _, entry := range inventory {
			if filepath.Base(entry.RelativePath) != ".loomignore" {
				continue
			}
			dir := filepath.Dir(filepath.Join(payload, filepath.FromSlash(entry.RelativePath)))
			underRoot := dir == canonical || strings.HasPrefix(dir, canonical+string(filepath.Separator))
			ancestor := (dir == filepath.Join(payload, boundary) || strings.HasPrefix(dir, filepath.Join(payload, boundary)+string(filepath.Separator))) && strings.HasPrefix(canonical, dir+string(filepath.Separator))
			if (underRoot || ancestor) && !seen[entry.RelativePath] {
				return nil, fmt.Errorf("%w: retained file policy is missing", ErrNotesCustodyPaused)
			}
		}
	}
	return resolver, nil
}

// WithFile authenticates current custody and generation for each read. The
// reader is valid only in fn; it is digest-checked before fn and checked again
// afterward. Callers must discard unpublished output on any returned error.
// maxBytes is the caller's existing text/asset budget, not a new delivery limit.
func (r WorkspaceArchiveSourceReader) WithFile(ctx context.Context, id, relative, generation string, maxBytes int64, fn func(WorkspaceArchiveSource, WorkspaceArchiveFile, io.Reader) error) error {
	if !notesCustodyRelativePath(relative) || generation == "" || maxBytes < 0 || fn == nil {
		return fmt.Errorf("%w: archive path, generation and byte budget required", ErrInvalid)
	}
	return r.WithRoot(ctx, id, func(source WorkspaceArchiveSource) error {
		if source.Generation != generation {
			return ErrFileSourceChanged
		}
		for _, file := range source.Files {
			if file.RelativePath == relative {
				if file.SizeBytes > maxBytes {
					return fmt.Errorf("%w: archived reference exceeds byte budget", ErrInvalid)
				}
				return withArchiveWorkspaceFile(ctx, filepath.Join(source.CanonicalRoot, filepath.FromSlash(relative)), file, func(reader io.Reader) error {
					return fn(source, file, reader)
				})
			}
		}
		return ErrNotesPathExcluded
	})
}

// Open every component read-only/no-follow and retain its descriptor. Reopen
// the names after reading so replacing any parent or the leaf cannot succeed.
type archiveWorkspacePath struct {
	files []*os.File
	parts []string
}

func (p *archiveWorkspacePath) current() *os.File { return p.files[len(p.files)-1] }
func (p *archiveWorkspacePath) close() {
	for i := len(p.files) - 1; i >= 0; i-- {
		p.files[i].Close()
	}
}
func (p *archiveWorkspacePath) check() error {
	for i, part := range p.parts {
		var held, named unix.Stat_t
		if unix.Fstat(int(p.files[i+1].Fd()), &held) != nil || unix.Fstatat(int(p.files[i].Fd()), part, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			held.Dev != named.Dev || held.Ino != named.Ino || held.Mode&unix.S_IFMT != named.Mode&unix.S_IFMT {
			return ErrFileSourceChanged
		}
	}
	return nil
}

func openArchiveWorkspacePath(absolute string, directory bool) (_ *archiveWorkspacePath, err error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return nil, ErrNotesPathExcluded
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	held := &archiveWorkspacePath{files: []*os.File{os.NewFile(uintptr(fd), "/")}, parts: strings.Split(strings.TrimPrefix(absolute, "/"), "/")}
	defer func() {
		if err != nil {
			held.close()
		}
	}()
	for i, part := range held.parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if directory || i < len(held.parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(int(held.current().Fd()), part, flags, 0)
		if err != nil {
			return nil, err
		}
		held.files = append(held.files, os.NewFile(uintptr(next), part))
	}
	return held, nil
}

func withArchiveWorkspaceFile(ctx context.Context, absolute string, expected WorkspaceArchiveFile, fn func(io.Reader) error) error {
	held, err := openArchiveWorkspacePath(absolute, false)
	if err != nil {
		return err
	}
	defer held.close()
	file := held.current()
	var before unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &before); err != nil {
		return err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size != expected.SizeBytes {
		return ErrFileSourceNotRegular
	}
	check := func() error {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, &archiveWorkspaceContextReader{ctx: ctx, reader: io.LimitReader(file, expected.SizeBytes+1)})
		if err != nil {
			return err
		}
		if n != expected.SizeBytes || fmt.Sprintf("sha256:%x", hash.Sum(nil)) != expected.SourceHash {
			return ErrFileSourceChanged
		}
		var after unix.Stat_t
		if unix.Fstat(int(file.Fd()), &after) != nil || after.Size != before.Size || after.Nlink != 1 {
			return ErrFileSourceChanged
		}
		if err := held.check(); err != nil {
			return err
		}
		_, err = file.Seek(0, io.SeekStart)
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if err := fn(&archiveWorkspaceContextReader{ctx: ctx, reader: io.LimitReader(file, expected.SizeBytes)}); err != nil {
		return err
	}
	return check()
}

type archiveWorkspaceContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *archiveWorkspaceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
