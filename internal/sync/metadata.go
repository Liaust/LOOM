package sync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const MetadataObservationSchema = "loom.sync.source_metadata.v1"

// MetadataObservation changes current observation only, never content or policy.
type MetadataObservation struct {
	SchemaVersion   string    `json:"schema_version"`
	LocalObjectRef  string    `json:"local_object_ref"`
	LocalVersionRef string    `json:"local_version_ref"`
	ObjectID        string    `json:"object_id"`
	VersionID       string    `json:"object_version_id"`
	HashURI         string    `json:"hash_uri"`
	SourcePath      string    `json:"source_path"`
	SourceMtime     time.Time `json:"source_mtime"`
}

func decodeMetadataObservation(item PushBatchItem) (MetadataObservation, error) {
	var p MetadataObservation
	if item.StreamName != StreamObjectMetadata || item.LocalSequence <= 0 || len(item.LocalRef) > 256 ||
		item.EventType != "" || item.EventLevel != "" || len(item.PayloadJSON) > 4096 {
		return p, fmt.Errorf("invalid metadata item envelope")
	}
	d := json.NewDecoder(bytes.NewReader(item.PayloadJSON))
	// Reject duplicate fields rather than allowing two interpretations of a
	// signed/idempotent observation at different JSON consumers.
	keys := json.NewDecoder(bytes.NewReader(item.PayloadJSON))
	if token, err := keys.Token(); err != nil || token != json.Delim('{') {
		return p, fmt.Errorf("metadata observation must be an object")
	}
	seen := map[string]bool{}
	for keys.More() {
		token, err := keys.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return p, fmt.Errorf("duplicate or invalid metadata field")
		}
		switch key {
		case "schema_version", "local_object_ref", "local_version_ref", "object_id", "object_version_id", "hash_uri", "source_path", "source_mtime":
		default:
			return p, fmt.Errorf("unknown metadata field")
		}
		seen[key] = true
		if err := keys.Decode(new(json.RawMessage)); err != nil {
			return p, fmt.Errorf("invalid metadata field value")
		}
	}
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid metadata observation")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, fmt.Errorf("trailing metadata observation")
	}
	if p.SchemaVersion != MetadataObservationSchema || p.SourceMtime.IsZero() || p.SourceMtime.Year() < 1 || p.SourceMtime.Year() > 9999 ||
		!strings.HasPrefix(p.SourcePath, "watched-root://") || len(p.SourcePath) > 2048 {
		return p, fmt.Errorf("invalid metadata observation identity")
	}
	for _, v := range []struct{ prefix, id string }{{"object", p.LocalObjectRef}, {"version", p.LocalVersionRef}, {"object", p.ObjectID}, {"version", p.VersionID}} {
		if err := ids.Validate(v.prefix, v.id); err != nil {
			return p, fmt.Errorf("invalid metadata object/version identity")
		}
	}
	hash, err := hex.DecodeString(strings.TrimPrefix(p.HashURI, "sha256:"))
	if err != nil || len(hash) != 32 || p.HashURI != "sha256:"+hex.EncodeToString(hash) {
		return p, fmt.Errorf("invalid metadata content hash")
	}
	return p, nil
}

func hasMetadataItems(input PushBatchInput) bool {
	for _, item := range input.Items {
		if item.ItemKind == ItemKindObjectMetadata {
			return true
		}
	}
	return false
}

func metadataBatchReplayMatches(input PushBatchInput, result PushBatchResult) bool {
	if len(input.Items) != len(result.Items) {
		return false
	}
	for _, a := range input.Items {
		matched := 0
		for _, b := range result.Items {
			if a.LocalRef == b.LocalRef && a.LocalSequence == b.LocalSequence && a.ItemKind == b.ItemKind &&
				a.StreamName == b.StreamName && hashJSON(a.PayloadJSON) == b.PayloadHash {
				matched++
			}
		}
		if matched != 1 {
			return false
		}
	}
	return true
}

func ingestObjectMetadataTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, batchID, nodeID string, item PushBatchItem) (SyncItemResult, *SyncConflict, error) {
	p, err := decodeMetadataObservation(item)
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	digest := hashJSON(item.PayloadJSON)
	refuse := func(reason string) (SyncItemResult, *SyncConflict, error) {
		c, err := insertConflictTx(ctx, tx, nodeID, batchID, item.LocalRef, ConflictObjectHashMismatch, reason, item.PayloadJSON, json.RawMessage(`{}`))
		if err != nil {
			return SyncItemResult{}, nil, err
		}
		r, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusConflicted, "", "sync.metadata_conflict", reason, digest)
		return r, &c, err
	}
	var previousRef, previousDigest, previousObject string
	var previousSequence int64
	err = tx.QueryRowContext(ctx, `SELECT local_ref,payload_hash,global_ref,(payload_json->>'local_sequence')::bigint FROM sync.batch_items
		WHERE origin_node_id=$1 AND item_kind='object_metadata' AND status IN ('accepted','duplicate')
		AND (local_ref=$2 OR (payload_json->>'local_sequence')::bigint=$3)
		ORDER BY created_at LIMIT 1`, nodeID, item.LocalRef, item.LocalSequence).Scan(&previousRef, &previousDigest, &previousObject, &previousSequence)
	if err == nil {
		if previousRef != item.LocalRef || previousDigest != digest || previousObject != p.ObjectID || previousSequence != item.LocalSequence {
			return refuse("metadata observation identity was reused")
		}
		r, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusDuplicate, p.ObjectID, "", "", digest)
		return r, nil, err
	}
	if err != sql.ErrNoRows {
		return SyncItemResult{}, nil, err
	}
	// Lock the current publication before comparing identity. Content publication
	// also updates these rows, so an obsolete observation cannot win a race.
	var fileMetadata []byte
	err = tx.QueryRowContext(ctx, `SELECT f.metadata FROM objects.objects o
		JOIN files.file_metadata f ON f.object_id=o.object_id
		JOIN objects.object_versions v ON v.object_version_id=f.latest_version_id AND v.object_id=o.object_id
		WHERE o.object_id=$1 AND f.latest_version_id=$2 AND o.status='active' AND v.status='active'
		AND f.source_node_id=$3 AND v.source_node_id=$3 AND v.content_hash=$4 AND f.source_path=$5
		AND v.source_path=$5 AND EXISTS (SELECT 1 FROM sync.batch_items bi WHERE bi.origin_node_id=$3
		 AND bi.item_kind='object_blob' AND bi.status IN ('accepted','duplicate') AND bi.local_ref=$6
		 AND bi.global_ref=$1 AND bi.metadata->>'local_version_ref'=$7
		 AND bi.metadata->>'object_version_id'=$2 AND bi.payload_json->'payload'->>'source_path'=$5)
		FOR UPDATE OF o,f`, p.ObjectID, p.VersionID, nodeID, p.HashURI, p.SourcePath, p.LocalObjectRef, p.LocalVersionRef).Scan(&fileMetadata)
	if err == sql.ErrNoRows {
		return refuse("metadata source identity is not current for this owner")
	}
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	var latestSequence int64
	var watermark struct {
		Sequence int64  `json:"source_metadata_sequence"`
		Owner    string `json:"source_metadata_owner"`
	}
	if err := json.Unmarshal(fileMetadata, &watermark); err != nil {
		return SyncItemResult{}, nil, fmt.Errorf("invalid stored metadata revision")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max((payload_json->>'local_sequence')::bigint),0)
		FROM sync.batch_items WHERE origin_node_id=$1 AND item_kind='object_metadata'
		AND status IN ('accepted','duplicate') AND global_ref=$2`, nodeID, p.ObjectID).Scan(&latestSequence); err != nil {
		return SyncItemResult{}, nil, err
	}
	if watermark.Owner == nodeID && watermark.Sequence > latestSequence {
		latestSequence = watermark.Sequence
	}
	if item.LocalSequence <= latestSequence {
		return refuse("metadata observation is older than current evidence")
	}
	patch := mustJSON(map[string]any{"source_mtime": p.SourceMtime.UTC(), "source_mtime_basis": filesystemmeta.SourceTimeBasisFilesystemMtime,
		"source_metadata_sequence": item.LocalSequence, "source_metadata_owner": nodeID})
	if _, err := tx.ExecContext(ctx, `UPDATE files.file_metadata SET source_mtime=$2,metadata=metadata || $3::jsonb,updated_at=now()
		WHERE object_id=$1`, p.ObjectID, p.SourceMtime.UTC(), patch); err != nil {
		return SyncItemResult{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE objects.objects SET metadata=metadata || $2::jsonb,updated_at=now() WHERE object_id=$1`, p.ObjectID, patch); err != nil {
		return SyncItemResult{}, nil, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{EventType: events.TypeSyncSourceMetadataObserved, EventLevel: "node_activity",
		Request: req, ScopeID: req.ScopeID, TargetKind: "object", TargetID: p.ObjectID, Status: "accepted", Result: "synced",
		VisibilityClass: "internal", Payload: map[string]any{"object_id": p.ObjectID, "version_id": p.VersionID,
			"source_node_id": nodeID, "sequence": item.LocalSequence, "observation_hash": digest}}); err != nil {
		return SyncItemResult{}, nil, err
	}
	r, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusAccepted, p.ObjectID, "", "", digest)
	return r, nil, err
}
