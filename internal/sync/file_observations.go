package sync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/requestctx"
)

const FileObservationSchema = "loom.sync.file_observation.v1"

type FileObservation struct {
	SchemaVersion  string     `json:"schema_version"`
	LocalObjectRef string     `json:"local_object_ref"`
	RootKey        string     `json:"root_key"`
	RelativePath   string     `json:"relative_path"`
	Generation     string     `json:"generation"`
	Presence       string     `json:"presence"`
	ObservedAt     time.Time  `json:"observed_at"`
	SourceMtime    *time.Time `json:"source_mtime,omitempty"`
	SizeBytes      int64      `json:"size_bytes"`
}

func isFileObservation(raw json.RawMessage) bool {
	var p struct {
		SchemaVersion string `json:"schema_version"`
	}
	return json.Unmarshal(raw, &p) == nil && p.SchemaVersion == FileObservationSchema
}

func decodeFileObservation(item PushBatchItem) (FileObservation, error) {
	var p FileObservation
	if item.StreamName != StreamObjectMetadata || item.LocalSequence <= 0 || item.LocalRef == "" || len(item.LocalRef) > 256 ||
		item.EventType != "" || item.EventLevel != "" || len(item.PayloadJSON) > 4096 {
		return p, fmt.Errorf("invalid file observation envelope")
	}
	keys := json.NewDecoder(bytes.NewReader(item.PayloadJSON))
	if token, err := keys.Token(); err != nil || token != json.Delim('{') {
		return p, fmt.Errorf("file observation must be an object")
	}
	seen := map[string]bool{}
	for keys.More() {
		token, err := keys.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return p, fmt.Errorf("duplicate file observation field")
		}
		switch key {
		case "schema_version", "local_object_ref", "root_key", "relative_path", "generation", "presence", "observed_at", "source_mtime", "size_bytes":
		default:
			return p, fmt.Errorf("unknown file observation field")
		}
		seen[key] = true
		if err := keys.Decode(new(json.RawMessage)); err != nil {
			return p, fmt.Errorf("invalid file observation field")
		}
	}
	d := json.NewDecoder(bytes.NewReader(item.PayloadJSON))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid file observation: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, fmt.Errorf("trailing file observation")
	}
	if p.SchemaVersion != FileObservationSchema || p.Generation == "" || len(p.Generation) > 128 ||
		p.RootKey == "" || len(p.RootKey) > 128 || strings.ContainsAny(p.RootKey, "/\\\x00") ||
		p.RelativePath == "" || len(p.RelativePath) > 2048 || path.IsAbs(p.RelativePath) || path.Clean(p.RelativePath) != p.RelativePath ||
		p.RelativePath == "." || p.RelativePath == ".." || strings.HasPrefix(p.RelativePath, "../") || strings.ContainsAny(p.RelativePath, "\\\x00") ||
		p.ObservedAt.IsZero() || p.ObservedAt.Year() < 1 || p.ObservedAt.Year() > 9999 || p.SizeBytes < 0 {
		return p, fmt.Errorf("invalid file observation identity")
	}
	if err := ids.Validate("object", p.LocalObjectRef); err != nil {
		return p, err
	}
	switch p.Presence {
	case "present", "missing", "unavailable":
	default:
		return p, fmt.Errorf("invalid file observation presence")
	}
	if p.SourceMtime != nil && (p.SourceMtime.IsZero() || p.SourceMtime.Year() < 1 || p.SourceMtime.Year() > 9999) {
		return p, fmt.Errorf("invalid file observation mtime")
	}
	if p.Presence == "present" && p.SourceMtime == nil {
		return p, fmt.Errorf("present file requires source mtime")
	}
	return p, nil
}

func validateMetadataItem(item PushBatchItem) error {
	if isFileObservation(item.PayloadJSON) {
		_, err := decodeFileObservation(item)
		return err
	}
	_, err := decodeMetadataObservation(item)
	return err
}

func ingestFileObservationTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, batchID, nodeID string, item PushBatchItem) (SyncItemResult, *SyncConflict, error) {
	p, err := decodeFileObservation(item)
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	digest := hashJSON(item.PayloadJSON)
	refuse := func(reason string) (SyncItemResult, *SyncConflict, error) {
		c, err := insertConflictTx(ctx, tx, nodeID, batchID, item.LocalRef, ConflictObjectHashMismatch, reason, item.PayloadJSON, json.RawMessage(`{}`))
		if err != nil {
			return SyncItemResult{}, nil, err
		}
		r, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusConflicted, "", "sync.file_observation_conflict", reason, digest)
		return r, &c, err
	}
	var oldRef, oldDigest, objectID string
	var oldSequence int64
	err = tx.QueryRowContext(ctx, `SELECT local_ref,payload_hash,global_ref,(payload_json->>'local_sequence')::bigint
	 FROM sync.batch_items WHERE origin_node_id=$1 AND item_kind='object_metadata'
	 AND status IN ('accepted','duplicate') AND (local_ref=$2 OR (payload_json->>'local_sequence')::bigint=$3)
	 ORDER BY created_at LIMIT 1`, nodeID, item.LocalRef, item.LocalSequence).Scan(&oldRef, &oldDigest, &objectID, &oldSequence)
	if err == nil {
		if oldRef != item.LocalRef || oldDigest != digest || oldSequence != item.LocalSequence {
			return refuse("file observation identity was reused")
		}
		r, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusDuplicate, objectID, "", "", digest)
		return r, nil, err
	}
	if err != sql.ErrNoRows {
		return SyncItemResult{}, nil, err
	}
	// The existing authenticated root report fences generation and admission;
	// source paths/scopes come from that registration, never from an arbitrary read.
	var config []byte
	var generation string
	var compatibleGeneration bool
	err = tx.QueryRowContext(ctx, `SELECT config_json,config_hash,observation_generations ? $3 FROM watched_roots.roots
	 WHERE node_id=$1 AND root_key=$2 FOR SHARE`, nodeID, p.RootKey, p.Generation).Scan(&config, &generation, &compatibleGeneration)
	if err == sql.ErrNoRows {
		// Keep the durable outbox pending when the first root report has not
		// arrived. Missing transport ordering is not a terminal file conflict.
		return SyncItemResult{}, nil, fmt.Errorf("file observation awaits root report")
	}
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	if generation != p.Generation && !compatibleGeneration {
		return refuse("file observation root generation is not current")
	}
	var root struct {
		SyncPolicy struct {
			ProjectRef string `json:"project_ref"`
			ScopeRef   string `json:"scope_ref"`
		} `json:"sync_policy"`
		BackupPolicy struct {
			Mode string `json:"mode"`
		} `json:"backup_policy"`
		IndexPolicy struct {
			Mode string `json:"mode"`
		} `json:"index_policy"`
	}
	if err := json.Unmarshal(config, &root); err != nil {
		return SyncItemResult{}, nil, err
	}
	scope := ""
	if root.SyncPolicy.ProjectRef != "" {
		err = tx.QueryRowContext(ctx, `SELECT project_scope_id FROM projects.projects WHERE
		 (project_id=$1 OR slug=$1) AND status='active'`, root.SyncPolicy.ProjectRef).Scan(&scope)
	} else if root.SyncPolicy.ScopeRef != "" {
		err = tx.QueryRowContext(ctx, `SELECT scope_id FROM scopes.scopes WHERE (scope_id=$1 OR scope_key=$1) AND status='active'`, root.SyncPolicy.ScopeRef).Scan(&scope)
	}
	if err == sql.ErrNoRows {
		return refuse("file observation scope is unavailable")
	}
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	sourcePath := "watched-root://" + p.RootKey + "/" + p.RelativePath
	managedPath := sourcePath
	var sourceRoot string
	err = tx.QueryRowContext(ctx, `SELECT source_path FROM knowledge.notes_source_roots
	 WHERE node_id=$1 AND backend_root_key=$2 AND status='active' ORDER BY notes_source_root_id LIMIT 1`, nodeID, p.RootKey).Scan(&sourceRoot)
	if err != nil && err != sql.ErrNoRows {
		return SyncItemResult{}, nil, err
	}
	if path.IsAbs(sourceRoot) {
		managedPath = path.Join(sourceRoot, p.RelativePath)
	}
	indexPolicy, rawBackupPolicy := IndexPolicyMetadataOnly, RawBackupPolicyNormal
	if root.IndexPolicy.Mode == "none" {
		indexPolicy = IndexPolicyNone
	}
	if root.BackupPolicy.Mode == "private_raw" {
		indexPolicy, rawBackupPolicy = IndexPolicyPrivateNoIndex, RawBackupPolicyPrivateRawBackup
	}
	objectID, err = objects.ObserveFileTx(ctx, tx, req, objects.FileObservation{
		NodeID: nodeID, RootKey: p.RootKey, RelativePath: p.RelativePath, Generation: p.Generation,
		SourcePath: sourcePath, ManagedPath: managedPath, ScopeID: scope, Presence: p.Presence,
		Sequence: item.LocalSequence, ObservedAt: p.ObservedAt, ModifiedAt: p.SourceMtime, SizeBytes: p.SizeBytes,
		InitialIndexPolicy: indexPolicy, InitialRawBackupPolicy: rawBackupPolicy})
	if errors.Is(err, objects.ErrFileObservationConflict) {
		return refuse("file observation is older than current source evidence")
	}
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	r, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusAccepted, objectID, "", "", digest)
	return r, nil, err
}
