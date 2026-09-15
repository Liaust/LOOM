package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type SourceRootFilter struct {
	SourceLifecycle   SourceLifecycleFilter `json:"source_lifecycle,omitempty"`
	SourceCategory    string                `json:"source_category,omitempty"`
	NotesSourceRootID string                `json:"notes_source_root_id,omitempty"`
	RootKind          string                `json:"root_kind,omitempty"`
	NodeKey           string                `json:"node_key,omitempty"`
	ProjectID         string                `json:"project_id,omitempty"`
	Status            string                `json:"status,omitempty"`
	IncludeInactive   bool                  `json:"include_inactive,omitempty"`
}

func (s Store) ListSourceRoots(ctx context.Context, filter SourceRootFilter) ([]SourceRoot, error) {
	return s.listSourceRoots(ctx, filter, false)
}

// Operational discovery above keeps disabled registrations for reconciliation.
// User-facing reads instead use object-level custody and current read authority.
func (s Store) ListReadableSourceRoots(ctx context.Context, filter SourceRootFilter) ([]SourceRoot, error) {
	lifecycle, err := NormalizeSourceLifecycleFilter(filter.SourceLifecycle)
	if err != nil {
		return nil, err
	}
	filter.SourceLifecycle = lifecycle
	return s.listSourceRoots(ctx, filter, true)
}

func (s Store) listSourceRoots(ctx context.Context, filter SourceRootFilter, readable bool) ([]SourceRoot, error) {
	if err := validateSourceCategory(filter.SourceCategory); err != nil {
		return nil, err
	}
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	clauses := []string{"1 = 1"}
	args := []any{}
	if filter.SourceCategory != "" {
		args = append(args, filter.SourceCategory)
		clauses = append(clauses, fmt.Sprintf("(%s) = $%d", sourceCategorySQL("root_kind"), len(args)))
	}
	if strings.TrimSpace(filter.NotesSourceRootID) != "" {
		args = append(args, strings.TrimSpace(filter.NotesSourceRootID))
		clauses = append(clauses, fmt.Sprintf("notes_source_root_id = $%d", len(args)))
	}
	if strings.TrimSpace(filter.RootKind) != "" {
		args = append(args, strings.TrimSpace(filter.RootKind))
		clauses = append(clauses, fmt.Sprintf("root_kind = $%d", len(args)))
	}
	if strings.TrimSpace(filter.NodeKey) != "" {
		args = append(args, strings.TrimSpace(filter.NodeKey))
		clauses = append(clauses, fmt.Sprintf("node_key = $%d", len(args)))
	}
	if strings.TrimSpace(filter.ProjectID) != "" {
		args = append(args, strings.TrimSpace(filter.ProjectID))
		clauses = append(clauses, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if strings.TrimSpace(filter.Status) != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	} else if !readable && !filter.IncludeInactive {
		clauses = append(clauses, "status = 'active'")
	}
	columns := sourceRootColumns()
	if readable {
		clauses = append(clauses, notesReadableRootSQL("notes_source_roots", filter.SourceLifecycle, filter.IncludeInactive))
		columns += `, ` + notesRootLifecycleCountsSQL("notes_source_roots")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+`
		FROM knowledge.notes_source_roots
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY root_kind, node_key, project_id NULLS FIRST, backend_root_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := []SourceRoot{}
	scan := scanSourceRoot
	if readable {
		scan = scanReadableSourceRoot
	}
	for rows.Next() {
		root, err := scan(rows)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

func (s Store) ListBoxWatchRootRegistrations(ctx context.Context) ([]BoxWatchRootRegistration, error) {
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT box_watch_root_registration_id, box_id, box_root_path, node_id,
		       owner_node_key, area_key, local_root_key, backend_root_key,
		       source_kinds_json, root_relative_path, display_name,
		       activation_status, metadata
		FROM box.watch_root_registrations
		WHERE area_key IN ('notes', 'topics', 'library')
		   OR local_root_key = 'notes'
		   OR backend_root_key = 'loom_box__notes'
		   OR source_kinds_json ? 'box_notes'
		ORDER BY owner_node_key, backend_root_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []BoxWatchRootRegistration{}
	for rows.Next() {
		var registration BoxWatchRootRegistration
		var sourceKinds, metadata []byte
		if err := rows.Scan(
			&registration.BoxWatchRootRegistrationID,
			&registration.BoxID,
			&registration.BoxRootPath,
			&registration.NodeID,
			&registration.OwnerNodeKey,
			&registration.AreaKey,
			&registration.LocalRootKey,
			&registration.BackendRootKey,
			&sourceKinds,
			&registration.RootRelativePath,
			&registration.DisplayName,
			&registration.ActivationStatus,
			&metadata,
		); err != nil {
			return nil, err
		}
		registration.SourceKinds = jsonArrayOrEmpty(sourceKinds)
		registration.Metadata = jsonObjectOrEmpty(metadata)
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Store) ListProjectWatchedRootRegistrations(ctx context.Context) ([]ProjectWatchedRootRegistration, error) {
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pwr.project_watched_root_registration_id,
		       pwr.project_contract_registration_id,
		       pwr.project_id,
		       pcr.project_root,
		       pwr.node_id,
		       pwr.owner_node_key,
		       pwr.local_root_key,
		       pwr.backend_root_key,
		       pwr.source_kinds_json,
		       pwr.root_relative_path,
		       pwr.display_name,
		       pwr.activation_status,
		       pwr.metadata
		FROM projects.project_watched_root_registrations pwr
		JOIN projects.project_contract_registrations pcr
		  ON pcr.project_contract_registration_id = pwr.project_contract_registration_id
		WHERE pwr.local_root_key = 'notes'
		   OR pwr.source_kinds_json ? 'notes_contract'
		   OR pwr.source_kinds_json ? 'project_material'
		ORDER BY pwr.owner_node_key, pwr.project_id, pwr.backend_root_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectWatchedRootRegistration{}
	for rows.Next() {
		var registration ProjectWatchedRootRegistration
		var sourceKinds, metadata []byte
		if err := rows.Scan(
			&registration.ProjectWatchedRootRegistrationID,
			&registration.ProjectContractRegistrationID,
			&registration.ProjectID,
			&registration.ProjectRoot,
			&registration.NodeID,
			&registration.OwnerNodeKey,
			&registration.LocalRootKey,
			&registration.BackendRootKey,
			&sourceKinds,
			&registration.RootRelativePath,
			&registration.DisplayName,
			&registration.ActivationStatus,
			&metadata,
		); err != nil {
			return nil, err
		}
		registration.SourceKinds = jsonArrayOrEmpty(sourceKinds)
		registration.Metadata = jsonObjectOrEmpty(metadata)
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Store) UpsertSourceRoot(ctx context.Context, root SourceRoot) (SourceRoot, error) {
	if s.db == nil {
		return SourceRoot{}, fmt.Errorf("knowledge store is not configured")
	}
	if err := ValidateSourceRoot(root); err != nil {
		return SourceRoot{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO knowledge.notes_source_roots (
			notes_source_root_id, root_kind, node_id, node_key, project_id,
			box_watch_root_registration_id, project_watched_root_registration_id,
			backend_root_key, display_name, source_path, root_relative_path,
			status, authorization_metadata, metadata, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
		        $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (root_kind, node_key, (COALESCE(project_id, '')), backend_root_key) DO UPDATE
		SET node_id = EXCLUDED.node_id,
		    box_watch_root_registration_id = EXCLUDED.box_watch_root_registration_id,
		    project_watched_root_registration_id = EXCLUDED.project_watched_root_registration_id,
		    display_name = EXCLUDED.display_name,
		    source_path = EXCLUDED.source_path,
		    root_relative_path = EXCLUDED.root_relative_path,
		    status = EXCLUDED.status,
		    authorization_metadata = EXCLUDED.authorization_metadata,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+sourceRootColumns(),
		root.NotesSourceRootID,
		root.RootKind,
		nullableString(root.NodeID),
		root.NodeKey,
		nullableString(root.ProjectID),
		nullableString(root.BoxWatchRootRegistrationID),
		nullableString(root.ProjectWatchedRootRegistrationID),
		root.BackendRootKey,
		root.DisplayName,
		root.SourcePath,
		root.RootRelativePath,
		root.Status,
		root.AuthorizationMetadata,
		root.Metadata,
		root.CreatedAt,
		root.UpdatedAt,
	)
	return scanSourceRoot(row)
}

func sourceRootColumns() string {
	return `notes_source_root_id, root_kind, node_id, node_key, project_id,
	        box_watch_root_registration_id, project_watched_root_registration_id,
	        backend_root_key, display_name, source_path, root_relative_path,
	        status, authorization_metadata, metadata, created_at, updated_at`
}

type sourceRootScanner interface {
	Scan(dest ...any) error
}

func scanSourceRoot(scanner sourceRootScanner) (SourceRoot, error) {
	var root SourceRoot
	var nodeID, projectID, boxRegistrationID, projectRegistrationID sql.NullString
	var authorizationMetadata, metadata []byte
	if err := scanner.Scan(
		&root.NotesSourceRootID,
		&root.RootKind,
		&nodeID,
		&root.NodeKey,
		&projectID,
		&boxRegistrationID,
		&projectRegistrationID,
		&root.BackendRootKey,
		&root.DisplayName,
		&root.SourcePath,
		&root.RootRelativePath,
		&root.Status,
		&authorizationMetadata,
		&metadata,
		&root.CreatedAt,
		&root.UpdatedAt,
	); err != nil {
		return SourceRoot{}, err
	}
	root.NodeID = nullStringPtr(nodeID)
	root.ProjectID = nullStringPtr(projectID)
	root.BoxWatchRootRegistrationID = nullStringPtr(boxRegistrationID)
	root.ProjectWatchedRootRegistrationID = nullStringPtr(projectRegistrationID)
	root.AuthorizationMetadata = jsonObjectOrEmpty(authorizationMetadata)
	root.Metadata = jsonObjectOrEmpty(metadata)
	root.SourceContext = sourceContextForRoot(root, "")
	return root, nil
}

func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	out := strings.TrimSpace(value.String)
	return &out
}

func jsonArrayOrEmpty(raw []byte) json.RawMessage {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return json.RawMessage(`[]`)
	}
	var decoded []any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}

func jsonObjectOrEmpty(raw []byte) json.RawMessage {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return emptyJSONObject
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return emptyJSONObject
	}
	return json.RawMessage(raw)
}
