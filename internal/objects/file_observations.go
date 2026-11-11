package objects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

var ErrFileObservationConflict = errors.New("file observation conflicts with current source")

// FileObservation is already admitted by the authenticated root owner. It does
// not create a blob/version, change indexing policy, or request data deletion.
type FileObservation struct {
	NodeID, RootKey, RelativePath, Generation  string
	SourcePath, ManagedPath, ScopeID           string
	Presence                                   string
	Sequence                                   int64
	ObservedAt                                 time.Time
	ModifiedAt                                 *time.Time
	SizeBytes                                  int64
	InitialIndexPolicy, InitialRawBackupPolicy string
}

func ObserveFileTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, p FileObservation) (string, error) {
	return observeFileTx(ctx, tx, req, p, false)
}

// Legacy adoption reserves no producer sequence. A later real observation
// always wins; already observed identities cannot be rewritten by this path.
func AdoptLegacyFileTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, p FileObservation) (string, error) {
	p.Sequence = 0
	return observeFileTx(ctx, tx, req, p, true)
}

func observeFileTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, p FileObservation, legacy bool) (string, error) {
	if p.NodeID == "" || p.RootKey == "" || p.RelativePath == "" || p.Generation == "" ||
		(!legacy && p.Sequence <= 0) || p.ObservedAt.IsZero() || p.SizeBytes < 0 || p.SourcePath == "" || p.ManagedPath == "" {
		return "", fmt.Errorf("invalid admitted file observation")
	}
	switch p.Presence {
	case "present", "missing", "unavailable":
	default:
		return "", fmt.Errorf("invalid file presence")
	}
	// Serialize path admission, including overlapping declarations of one path.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "file-source:"+p.NodeID+":"+p.ManagedPath); err != nil {
		return "", err
	}
	var objectID string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT f.object_id,f.source_sequence FROM files.file_metadata f
		LEFT JOIN objects.object_versions v ON v.object_version_id=f.latest_version_id
		WHERE f.source_node_id=$1 AND (f.managed_source_path=$2 OR
		 (f.managed_source_path IS NULL AND (f.source_path=$3 OR f.source_path=$2)))
		ORDER BY (f.managed_source_path IS NOT NULL) DESC,v.created_at DESC NULLS LAST,f.object_id
		LIMIT 1 FOR UPDATE OF f`, p.NodeID, p.ManagedPath, p.SourcePath).Scan(&objectID, &sequence)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if err == nil && ((!legacy && sequence >= p.Sequence) || (legacy && sequence > 0)) {
		return "", ErrFileObservationConflict
	}
	if err == nil && legacy {
		var presence, managed string
		if err = tx.QueryRowContext(ctx, `SELECT source_presence,COALESCE(managed_source_path,'') FROM files.file_metadata WHERE object_id=$1`, objectID).Scan(&presence, &managed); err != nil {
			return "", err
		}
		if presence != "unobserved" && managed == p.ManagedPath {
			return objectID, nil
		}
	}
	if objectID == "" {
		objectID = ids.NewObjectID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO objects.objects
		 (object_id,object_type,name,owner_actor_id,home_scope_id,state_class,status,metadata)
		 VALUES ($1,'file',$2,nullif($3,''),nullif($4,''),'replica','active','{}'::jsonb)`,
			objectID, path.Base(p.RelativePath), req.ActorID, p.ScopeID); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO files.file_metadata
		 (object_id,logical_name,extension,source_node_id,source_path,index_policy,raw_backup_policy)
		 VALUES ($1,$2,nullif($3,''),$4,$5,$6,$7)`,
			objectID, path.Base(p.RelativePath), strings.TrimPrefix(path.Ext(p.RelativePath), "."), p.NodeID, p.SourcePath,
			p.InitialIndexPolicy, p.InitialRawBackupPolicy); err != nil {
			return "", err
		}
	}
	if p.ScopeID != "" {
		if _, err := ensureScopeLinkTx(ctx, tx, ids.NewObjectScopeLinkID(), objectID, p.ScopeID, "primary", req.ActorID); err != nil {
			return "", err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE files.file_metadata SET managed_source_path=$2,
	 source_root_key=$3,source_relative_path=$4,source_generation=$5,source_sequence=$6,
	 source_presence=$7,source_observed_at=$8,source_observed_size=$9,
	 source_mtime=COALESCE($10::timestamptz,source_mtime),updated_at=now() WHERE object_id=$1`,
		objectID, p.ManagedPath, p.RootKey, p.RelativePath, p.Generation, p.Sequence,
		p.Presence, p.ObservedAt.UTC(), p.SizeBytes, p.ModifiedAt)
	if err != nil {
		return "", err
	}
	return objectID, nil
}

// CurrentFileSourceSQL is a serving fence, not a protected-data deletion rule.
// Unobserved legacy files keep their old admission until metadata adoption.
func CurrentFileSourceSQL(file string) string {
	return `(NOT EXISTS (SELECT 1 FROM files.file_metadata withdrawn_file
	 WHERE withdrawn_file.source_node_id=` + file + `.source_node_id
	 AND withdrawn_file.source_presence IN ('missing','unavailable')
	 AND (withdrawn_file.object_id=` + file + `.object_id OR withdrawn_file.source_path=` + file + `.source_path
	 OR withdrawn_file.managed_source_path=` + file + `.managed_source_path))
	 AND NOT EXISTS (SELECT 1 FROM watched_roots.roots unavailable_root
	 WHERE unavailable_root.node_id=` + file + `.source_node_id
	 AND unavailable_root.root_key=COALESCE(NULLIF(` + file + `.source_root_key,''),
	 CASE WHEN starts_with(` + file + `.source_path,'watched-root://') THEN split_part(` + file + `.source_path,'/',3) END)
	 AND unavailable_root.metadata->>'root_reachable'='false'))`
}

// Resolve the body writer from declared membership, even before the Notes
// admission worker creates a knowledge row. No second ownership registry.
func NotesFileBodyOwnerSQL(file string) string {
	return `(EXISTS (SELECT 1 FROM knowledge.knowledge_objects retained_owner
	 WHERE retained_owner.canonical_file_id=` + file + `.object_id AND retained_owner.deleted_at IS NULL)
	 OR EXISTS (SELECT 1 FROM knowledge.notes_source_roots body_root
	 WHERE body_root.node_id=` + file + `.source_node_id AND body_root.status='active'
	 AND (body_root.backend_root_key=COALESCE(NULLIF(` + file + `.source_root_key,''),
	 CASE WHEN starts_with(` + file + `.source_path,'watched-root://') THEN split_part(` + file + `.source_path,'/',3) END)
	 OR (starts_with(body_root.source_path,'/') AND
	 starts_with(COALESCE(` + file + `.managed_source_path,` + file + `.source_path),rtrim(body_root.source_path,'/')||'/')))))`
}
