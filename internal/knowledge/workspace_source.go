package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/storagecatalog"
	ownerreports "loom.local/loom/internal/watchedroots"
)

// A policy exclusion is not a transient source outage or delivery failure.
var ErrNotesPathExcluded = errors.New("Notes source path is excluded")

// NotesAdmissionError lets bounded diagnostics expose a phase instead of the
// underlying error text. Existing callers retain their original error semantics.
type NotesAdmissionError struct {
	Phase  string
	Reason string
	Err    error
}

func (e *NotesAdmissionError) Error() string { return e.Err.Error() }
func (e *NotesAdmissionError) Unwrap() error { return e.Err }

// WithWritableNotesSource is a source-owner seam, not a capability authorization
// endpoint. The caller must explicitly select the collection and authorize edits.
// It works before any KnowledgeObject exists and never resolves retained copies.
func (s *Service) WithWritableNotesSource(ctx context.Context, id, nodeKey, relative string, fn func(SourceRoot, string) error) error {
	return s.WithWritableNotesSourcePaths(ctx, id, nodeKey, []string{relative}, fn)
}

// WithWritableNotesSourcePaths checks both sides of a rename under one transaction.
// The first path supplies the source generation; all paths share current policy.
func (s *Service) WithWritableNotesSourcePaths(ctx context.Context, id, nodeKey string, relatives []string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, relatives, false, false, false, fn)
}

// WithWritableNotesDirectoryPaths admits actual directory paths, including
// absent directories, under the same owner, generation and policy fences.
func (s *Service) WithWritableNotesDirectoryPaths(ctx context.Context, id, nodeKey string, relatives []string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, relatives, false, len(relatives) == 0, true, fn)
}

// WithReadableNotesDirectoryPaths observes reference-only source structure. It
// grants no directory mutation and keeps the same custody and privacy fences.
func (s *Service) WithReadableNotesDirectoryPaths(ctx context.Context, id, nodeKey string, relatives []string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, relatives, true, len(relatives) == 0, true, fn)
}

// WithReferenceNotesSource retains the same enrollment/privacy/custody fences,
// but permits a reference snapshot, never a source write.
func (s *Service) WithReferenceNotesSource(ctx context.Context, id, nodeKey, relative string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, []string{relative}, true, false, false, fn)
}

// WithNotesWorkspaceRoot admits a registered collection before it contains a
// file or an index row. Every subsequent file read/write still checks its path.
func (s *Service) WithNotesWorkspaceRoot(ctx context.Context, id, nodeKey string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourcePaths(ctx, id, nodeKey, nil, false, true, false, fn)
}

func (s *Service) withWorkspaceSourcePaths(ctx context.Context, id, nodeKey string, relatives []string, reference, rootOnly, directory bool, fn func(SourceRoot, string) error) (result error) {
	return s.withWorkspaceSourceAdmission(ctx, id, nodeKey, relatives, reference, rootOnly, directory, false, fn)
}

// WithNotesRecoveryRoot grants only current-filesystem backup preparation. All
// owner/configuration/custody fences remain; unresolved absences stay unresolved.
func (s *Service) WithNotesRecoveryRoot(ctx context.Context, id, nodeKey string, fn func(SourceRoot, string) error) error {
	return s.withWorkspaceSourceAdmission(ctx, id, nodeKey, nil, true, true, false, true, fn)
}

func (s *Service) withWorkspaceSourceAdmission(ctx context.Context, id, nodeKey string, relatives []string, reference, rootOnly, directory, recovery bool, fn func(SourceRoot, string) error) (result error) {
	phase := "request"
	defer func() {
		if result != nil {
			reason := ""
			var admission *NotesAdmissionError
			if errors.As(result, &admission) {
				reason = admission.Reason
			}
			result = &NotesAdmissionError{Phase: phase, Reason: reason, Err: result}
		}
	}()
	if !rootOnly && (len(relatives) < 1 || len(relatives) > 2) {
		return fmt.Errorf("one or two Notes paths required")
	}
	if id == "" || nodeKey == "" || fn == nil {
		return fmt.Errorf("invalid Notes source identity")
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
	phase = "database_connection"
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
	phase = "project_custody_lock"
	if project.Valid {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "loom:project-repository:project:id:"+project.String); err != nil {
			return err
		}
	}
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return err
	}
	phase = "source_identity"
	args := []any{id}
	selection := notesSelectedRootSQL(notesBoxSelection(ctx), "notes_source_roots", &args)
	root, err := scanSourceRoot(tx.QueryRowContext(ctx, `SELECT `+sourceRootColumns()+` FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1 AND `+selection+` FOR SHARE`, args...))
	if err != nil {
		return err
	}
	if derefString(root.ProjectID) != project.String || root.NodeKey != nodeKey || root.Status != SourceRootStatusActive || !filepath.IsAbs(root.SourcePath) || filepath.Clean(root.SourcePath) != root.SourcePath {
		return ErrNotesCustodyPaused
	}
	phase = "source_policy"
	applicationData := false
	if registration, ok := jsonObject(root.Metadata)["registration_metadata"].(map[string]any); ok {
		if source, ok := registration["knowledge_source"].(map[string]any); ok {
			_, applicationData = source["application_data"]
			if applicationData && !reference && (!rootOnly || directory) {
				return fmt.Errorf("application-data Notes sources remain reference-only")
			}
		}
	}

	for _, relative := range relatives {
		if err := workspaceSourcePathAllowed(root, relative, reference, directory); err != nil {
			return err
		}
	}
	switch root.RootKind {
	case RootKindBoxNotes, RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectNotes, RootKindProjectMaterial:
	default:
		return fmt.Errorf("unsupported writable Notes root")
	}
	// Lock the current owner node and registration rows so withdrawal/config edits
	// cannot pass between eligibility and the callback's filesystem publication.
	phase = "node_identity"
	var role, status, key string
	if err := tx.QueryRowContext(ctx, `SELECT node_role,status,node_key FROM nodes.nodes WHERE node_id=$1 FOR SHARE`, root.NodeID).Scan(&role, &status, &key); err != nil {
		return err
	}
	if role != "main" || status != "active" || key != nodeKey {
		return ErrNotesCustodyPaused
	}
	// Owner reports lock the watched root before updating its registration.
	// Use that same order, then recheck the binding under registration lock.
	phase = "registration_binding"
	reportLookup := `SELECT watched_root_id FROM box.watch_root_registrations WHERE box_watch_root_registration_id=$1 AND node_id=$2`
	reportRegistration := root.BoxWatchRootRegistrationID
	if root.ProjectID != nil {
		reportLookup = `SELECT watched_root_id FROM projects.project_watched_root_registrations WHERE project_watched_root_registration_id=$1 AND node_id=$2`
		reportRegistration = root.ProjectWatchedRootRegistrationID
	}
	if reportRegistration == nil {
		return ErrNotesCustodyPaused
	}
	var lockedReportID string
	err = tx.QueryRowContext(ctx, `SELECT report.watched_root_id FROM watched_roots.roots report WHERE report.watched_root_id=(`+reportLookup+`) FOR SHARE OF report`, reportRegistration, root.NodeID).Scan(&lockedReportID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotesCustodyPaused
	}
	if err != nil {
		return err
	}
	phase = "registration_policy"
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
		phase = "project_registration"
		var registrationStatus, projectRoot string
		if err := tx.QueryRowContext(ctx, `SELECT registration_status,project_root FROM projects.project_contract_registrations WHERE project_contract_registration_id=$1 AND project_id=$2 FOR SHARE`, registrationID, root.ProjectID).Scan(&registrationStatus, &projectRoot); err != nil {
			return err
		}
		// The declaration membership query below verifies the exact current
		// application allocation/path. Root/reference admission never grants a write.
		if registrationStatus != "registered" || (!applicationData && filepath.Join(projectRoot, root.RootRelativePath) != root.SourcePath) {
			return ErrNotesCustodyPaused
		}
	}
	phase = "owner_acknowledgement"
	if !reportID.Valid || reportID.String != lockedReportID || configHash == "" {
		return ErrNotesCustodyPaused
	}
	var reportStatus, reportHash, reportKey, reportNode string
	var reportConfig, reportSummary, reportMetadata []byte
	if err := tx.QueryRowContext(ctx, `SELECT status,config_hash,root_key,node_id,config_json,summary_json,metadata FROM watched_roots.roots WHERE watched_root_id=$1 FOR SHARE`, reportID).Scan(&reportStatus, &reportHash, &reportKey, &reportNode, &reportConfig, &reportSummary, &reportMetadata); err != nil {
		return err
	}
	if reportHash != configHash || reportKey != root.BackendRootKey || reportNode != derefString(root.NodeID) || !workspaceJSONEqual(configJSON, reportConfig) {
		return &NotesAdmissionError{Reason: "owner_configuration_mismatch", Err: ErrNotesCustodyPaused}
	}
	admission := ownerreports.SourceAdmissionState(reportStatus, reportSummary, reportMetadata)
	if !rootOnly {
		admission = ownerreports.SourceEntryAdmissionState(reportStatus, reportSummary, reportMetadata)
	}
	if recovery {
		admission = ownerreports.SourceRecoveryAdmissionState(reportStatus, reportSummary, reportMetadata)
		root.RecoveryMissingDeferred = admission.MissingDeferred
	}
	if !admission.Allowed {
		return &NotesAdmissionError{Reason: admission.Reason, Err: ErrNotesCustodyPaused}
	}
	root.SourceIntakeStage = admission.Stage

	phase = "declaration_membership"
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT `+declarationEnrollmentMembershipSQL("r")+` FROM knowledge.notes_source_roots r WHERE notes_source_root_id=$1`, id).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotesCustodyPaused
	}
	phase = "path_policy"
	var policyFingerprint string
	if rootOnly {
		policyFingerprint, err = workspacePathPolicy(root, configJSON, "")
		if err != nil {
			return err
		}
	}
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
		fingerprint, err := workspacePathPolicyKind(root, configJSON, relative, directory)
		if err != nil {
			return err
		}
		if index == 0 {
			policyFingerprint = fingerprint
		} else if fingerprint != policyFingerprint {
			return ErrNotesCustodyPaused
		}

	}
	generation := workspaceSourceGeneration(root, configHash, configJSON, policyFingerprint)
	if err := ctx.Err(); err != nil {
		return err
	}
	phase = "source_callback"
	if err := fn(root, generation); err != nil {
		return err
	}
	phase = "transaction_commit"
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

func workspaceOwnerReportUsable(status string, summary, metadata []byte) bool {
	return ownerreports.SourceAdmissionState(status, summary, metadata).Allowed
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
	return workspacePathPolicyKind(root, raw, relative, false)
}

func workspacePathPolicyKind(root SourceRoot, raw []byte, relative string, directory bool) (string, error) {
	var config rootpolicy.RootConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", err
	}
	if config.RootKey != root.BackendRootKey || len(config.Include) == 0 {
		return "", ErrNotesCustodyPaused
	}
	if workspace, err := independentWorkspacePolicy(root); err != nil {
		return "", err
	} else if workspace != nil {
		config.Include, config.Exclude = workspace.Include, workspace.Exclude
	}
	if err := rootpolicy.ValidatePatterns(config.Include); err != nil {
		return "", err
	}
	if err := rootpolicy.ValidatePatterns(config.Exclude); err != nil {
		return "", err
	}
	included, _ := rootpolicy.MatchAny(config.Include, relative)
	if directory {
		included = workspaceDirectoryIncluded(config.Include, relative)
	}
	if relative != "" && !included {
		return "", fmt.Errorf("%w: outside current watched-root includes", ErrNotesPathExcluded)
	}
	if config.IgnorePolicy.Profile == "" {
		excluded, _ := rootpolicy.MatchAny(config.Exclude, relative)
		if relative != "" && excluded {
			return "", fmt.Errorf("%w: current watched-root policy", ErrNotesPathExcluded)
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
	if relative == "" {
		return resolver.Fingerprint(), nil
	}
	resolution, err := resolver.Resolve(relative, directory)
	if err != nil {
		return "", err
	}
	if !resolution.Decision.Included {
		return "", fmt.Errorf("%w: current file policy", ErrNotesPathExcluded)
	}
	return resolver.Fingerprint(), nil
}

func workspaceSourcePathAllowed(root SourceRoot, relative string, reference, directory bool) error {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00") {
		return ErrNotesPathExcluded
	}
	for _, part := range strings.Split(relative, "/") {
		if strings.HasPrefix(part, ".") || strings.TrimSpace(part) != part || part == "credentials" || part == "private_no_index" {
			return ErrNotesPathExcluded
		}
	}
	if !directory {
		switch strings.ToLower(path.Ext(relative)) {
		case ".md", ".markdown", ".txt", ".canvas", ".base":
		case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wav", ".svg", ".csv", ".xlsx", ".dat", ".json":
			if !reference {
				return fmt.Errorf("Notes source is reference-only")
			}
		default:
			return fmt.Errorf("Notes source is reference-only")
		}
	}
	if filepolicy.IsIndexingExcludedPath(relative) || ignoredNotesKnowledgeRelativePath(relative) {
		return ErrNotesPathExcluded
	}
	workspace, err := independentWorkspacePolicy(root)
	if err != nil {
		return err
	}
	if workspace != nil {
		included, _ := rootpolicy.MatchAny(workspace.Include, relative)
		if directory {
			included = workspaceDirectoryIncluded(workspace.Include, relative)
		}
		excluded, _ := rootpolicy.MatchAny(workspace.Exclude, relative)
		if !included || excluded {
			return ErrNotesPathExcluded
		}
		return nil
	}
	if !directory {
		if expandedSourceExcluded(root, relative, nil) {
			return ErrNotesPathExcluded
		}
		return nil
	}
	// Source declarations have their own include scope in addition to the
	// watched-root config. Match directory prefixes, not an invented note name.
	switch root.RootKind {
	case RootKindBoxNotes, RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectMaterial:
		if registration, ok := jsonObject(root.Metadata)["registration_metadata"].(map[string]any); ok {
			if source, ok := registration["knowledge_source"].(map[string]any); ok {
				include, includeOK := expandedSourcePatterns(source["include"])
				exclude, excludeOK := expandedSourcePatterns(source["exclude"])
				if !includeOK || !excludeOK || !workspaceDirectoryIncluded(include, relative) {
					return ErrNotesPathExcluded
				}
				if excluded, _ := rootpolicy.MatchAny(exclude, relative); excluded {
					return ErrNotesPathExcluded
				}
			}
		}
	}
	return nil
}

// NotesWorkspacePolicy reads normalized, owner-registered source intent. It is
// not source admission; every later read/write still checks current ownership.
func NotesWorkspacePolicy(root SourceRoot) (*projectcontracts.KnowledgeWorkspacePolicy, error) {
	return independentWorkspacePolicy(root)
}

// NotesWorkspaceDirectoryIncluded is a conservative discovery filter, not a
// read/write grant. Exact source admission remains required at publication.
func NotesWorkspaceDirectoryIncluded(patterns []string, relative string) bool {
	return workspaceDirectoryIncluded(patterns, relative)
}

func independentWorkspacePolicy(root SourceRoot) (*projectcontracts.KnowledgeWorkspacePolicy, error) {
	if root.RootKind != RootKindProjectMaterial {
		return nil, nil
	}
	registration, _ := jsonObject(root.Metadata)["registration_metadata"].(map[string]any)
	source, _ := registration["knowledge_source"].(map[string]any)
	version, _ := source["schema_version"].(string)
	if !projectcontracts.IsDeclarationSchemaVersion(version) {
		return nil, nil
	}
	value, present := source["policy"]
	if !present {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrNotesCustodyPaused
	}
	policy, err := projectcontracts.DecodeKnowledgeSourcePolicy(raw)
	if err != nil {
		return nil, ErrNotesCustodyPaused
	}
	return policy.Workspace, nil
}

// A directory is eligible if it matches an include or can be an ancestor of
// one. Consume only real path segments; ** may consume zero or more segments.
func workspaceDirectoryIncluded(patterns []string, relative string) bool {
	for _, pattern := range patterns {
		if rootpolicy.ValidatePatterns([]string{pattern}) != nil {
			continue
		}
		parts := strings.Split(path.Clean(pattern), "/")
		segments := strings.Split(relative, "/")
		type position struct{ pattern, segment int }
		seen := map[position]bool{}
		var match func(int, int) bool
		match = func(p, s int) bool {
			if s == len(segments) {
				return true
			}
			key := position{p, s}
			if p == len(parts) || seen[key] {
				return false
			}
			seen[key] = true
			if parts[p] == "**" {
				return match(p+1, s) || match(p, s+1)
			}
			ok, _ := path.Match(parts[p], segments[s])
			return ok && match(p+1, s+1)
		}
		if match(0, 0) {
			return true
		}
	}
	return false
}
