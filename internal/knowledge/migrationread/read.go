package migrationread

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DeclarationMigrationRead contains private domain rows, not public migration facts.
// The caller owns the read-only repeatable-read transaction and authorization.
type DeclarationMigrationRead struct {
	Completeness string
	Revision     string
	Roots        []SourceRoot
	Custody      []Custody
}

func ReadDeclarationMigrationKnowledgeTx(ctx context.Context, tx *sql.Tx, projectID, nodeID string) (out DeclarationMigrationRead, err error) {
	out.Completeness = "unavailable"
	out.Roots = []SourceRoot{}
	if tx == nil || projectID == "" || nodeID == "" {
		return out, errors.New("migration_scope_transaction_required")
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+sourceRootColumns()+` FROM knowledge.notes_source_roots WHERE project_id=$1 ORDER BY notes_source_root_id LIMIT 4097`, projectID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanSourceRoot(rows)
		if e != nil {
			return out, e
		}
		out.Roots = append(out.Roots, v)
		if len(out.Roots) > 4096 {
			out.Completeness = "truncated"
			return out, errors.New("migration_owner_bound")
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = readCustody(ctx, tx, projectID, &out); err != nil {
		return out, err
	}
	raw, err := json.Marshal(struct {
		Roots   []SourceRoot
		Custody []Custody
	}{out.Roots, out.Custody})
	if err != nil {
		return out, err
	}
	out.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	out.Completeness = "complete"
	return out, nil
}

// SourceRoot is private observation metadata. It is never serialized into the assessment.
type SourceRoot struct {
	NotesSourceRootID                string          `json:"notes_source_root_id"`
	RootKind                         string          `json:"root_kind"`
	NodeID                           *string         `json:"node_id,omitempty"`
	NodeKey                          string          `json:"node_key,omitempty"`
	ProjectID                        *string         `json:"project_id,omitempty"`
	BoxWatchRootRegistrationID       *string         `json:"box_watch_root_registration_id,omitempty"`
	ProjectWatchedRootRegistrationID *string         `json:"project_watched_root_registration_id,omitempty"`
	BackendRootKey                   string          `json:"backend_root_key"`
	DisplayName                      string          `json:"display_name,omitempty"`
	SourcePath                       string          `json:"source_path,omitempty"`
	RootRelativePath                 string          `json:"root_relative_path,omitempty"`
	Status                           string          `json:"status"`
	AuthorizationMetadata            json.RawMessage `json:"authorization_metadata"`
	Metadata                         json.RawMessage `json:"metadata"`
	CreatedAt                        time.Time       `json:"created_at"`
	UpdatedAt                        time.Time       `json:"updated_at"`
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
	root.AuthorizationMetadata = json.RawMessage(authorizationMetadata)
	root.Metadata = json.RawMessage(metadata)
	return root, nil
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// Custody retains existing transition and pointer evidence, including historical
// transitions without a current pointer. No Notes text, chunk or vector is read.
type Custody struct {
	ObjectID, RootID, EventID, ArchiveOperationID                               string
	ProjectEventID, PreviousEventID                                             *string
	OriginalPath, CanonicalPath, RelativePath, ManifestDigest, TransitionDigest string
	CreatedAt                                                                   time.Time
	Current                                                                     bool
}

func readCustody(ctx context.Context, tx *sql.Tx, projectID string, out *DeclarationMigrationRead) error {
	out.Custody = []Custody{}
	rows, err := tx.QueryContext(ctx, `SELECT t.knowledge_object_id,o.notes_source_root_id,
 t.workspace_lifecycle_event_id,t.archive_operation_id,t.project_event_id,t.previous_event_id,
 t.original_path,t.canonical_path,t.workspace_relative_path,t.manifest_digest,t.transition_digest,t.created_at,
 EXISTS(SELECT 1 FROM knowledge.notes_current_custody c WHERE c.knowledge_object_id=t.knowledge_object_id AND c.workspace_lifecycle_event_id=t.workspace_lifecycle_event_id)
 FROM knowledge.notes_custody_transitions t JOIN knowledge.knowledge_objects o USING(knowledge_object_id)
 JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
 WHERE r.project_id=$1 ORDER BY t.knowledge_object_id,t.workspace_lifecycle_event_id LIMIT 4097`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v Custody
		if err := rows.Scan(&v.ObjectID, &v.RootID, &v.EventID, &v.ArchiveOperationID, &v.ProjectEventID, &v.PreviousEventID, &v.OriginalPath, &v.CanonicalPath, &v.RelativePath, &v.ManifestDigest, &v.TransitionDigest, &v.CreatedAt, &v.Current); err != nil {
			return err
		}
		out.Custody = append(out.Custody, v)
		if len(out.Roots)+len(out.Custody) > 4096 {
			out.Completeness = "truncated"
			return errors.New("migration_owner_bound")
		}
	}
	return rows.Err()
}
