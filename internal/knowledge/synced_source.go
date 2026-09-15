package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const defaultSyncedSourceMaxBytes int64 = 5 * 1024 * 1024

type syncedSourceBinding struct {
	VersionID, ReplicaID, BlobID string
	Path, Hash                   string
	Size                         int64
}

func isSyncedKnowledgeObject(object KnowledgeObject) bool {
	var metadata struct {
		Origin string          `json:"origin"`
		Synced json.RawMessage `json:"synced_object"`
	}
	return json.Unmarshal(object.Metadata, &metadata) == nil &&
		(metadata.Origin == KnowledgeObjectOriginSyncedObject || len(metadata.Synced) != 0)
}

// The original source path is citation metadata, not a path on this machine.
// Only the current database-owned version/replica/blob may authorize the read.
func (s *Service) resolveSyncedSource(ctx context.Context, object KnowledgeObject) (syncedSourceBinding, error) {
	var binding syncedSourceBinding
	if s == nil || s.store.db == nil {
		return binding, fmt.Errorf("knowledge store is not configured")
	}
	err := s.store.db.QueryRowContext(ctx, `
	 SELECT v.object_version_id,r.replica_id,b.blob_id,b.storage_path,b.hash_uri,b.size_bytes
	 FROM knowledge.knowledge_objects o
	 JOIN objects.object_versions v ON v.object_version_id=o.metadata->'synced_object'->>'object_version_id'
	 AND v.object_id=o.metadata->'synced_object'->>'object_id'
	 JOIN files.file_metadata f ON f.object_id=v.object_id AND f.latest_version_id=v.object_version_id
	 AND COALESCE(v.source_node_id,f.source_node_id)=o.source_node_id
	 AND COALESCE(v.source_path,f.source_path)=o.metadata->'synced_object'->>'source_path'
	 JOIN sync.replicas r ON r.replica_id=o.metadata->'synced_object'->>'replica_id'
	 AND r.replicated_kind='object_version' AND r.replicated_id=v.object_version_id AND r.freshness_state='fresh'
	 AND r.source_node_id=o.source_node_id
	 JOIN files.blobs b ON b.blob_id=v.blob_id AND b.status='verified' AND b.hash_algorithm='sha256'
	 AND b.hash_uri='sha256:'||b.hash_hex AND b.hash_uri=v.content_hash
	 AND b.hash_uri=r.storage_ref AND b.hash_uri=o.source_hash
	 AND b.hash_uri=o.metadata->'synced_object'->>'storage_ref'
	 AND b.size_bytes=v.size_bytes AND b.size_bytes=o.size_bytes
	 WHERE o.knowledge_object_id=$1 AND o.source_hash=$2 AND o.source_revision=$3
	 AND o.notes_source_root_id=$4 AND o.source_path=$5 AND o.relative_path=$6
	 AND o.source_node_id IS NOT DISTINCT FROM $7::text AND o.source_node_key=$8
	 AND o.project_id IS NOT DISTINCT FROM $9::text AND o.size_bytes=$10::bigint
	 AND o.file_class=$11 AND o.mime_type=$12 AND o.metadata=$13::jsonb
	 AND o.deleted_at IS NULL AND o.storage_entry_id IS NULL
	 AND o.metadata->>'origin'='sync.object_replica'
	 AND `+notesKnowledgeVisibilitySQL("o", false)+`
	 AND `+notesCustodyWriteAllowedSQL("o"),
		object.KnowledgeObjectID, object.SourceHash, object.SourceRevision,
		object.NotesSourceRootID, object.SourcePath, object.RelativePath,
		object.SourceNodeID, object.SourceNodeKey, object.ProjectID, object.SizeBytes,
		object.FileClass, object.MimeType, object.Metadata).
		Scan(&binding.VersionID, &binding.ReplicaID, &binding.BlobID, &binding.Path, &binding.Hash, &binding.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, fmt.Errorf("%w: synced source binding is no longer current or readable", ErrConflict)
	}
	if err != nil {
		return binding, err
	}
	return binding, nil
}

func (s *Service) readSyncedObjectSource(ctx context.Context, object KnowledgeObject, maxBytes int64) ([]byte, error) {
	return s.readSyncedObjectSourceWith(ctx, object, maxBytes, readVerifiedSyncedBlob)
}

func (s *Service) readSyncedObjectSourceWith(ctx context.Context, object KnowledgeObject, maxBytes int64, read func(context.Context, syncedSourceBinding, int64) ([]byte, error)) ([]byte, error) {
	before, err := s.resolveSyncedSource(ctx, object)
	if err != nil {
		return nil, err
	}
	payload, err := read(ctx, before, maxBytes)
	if err != nil {
		return nil, err
	}
	after, err := s.resolveSyncedSource(ctx, object)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, fmt.Errorf("%w: synced source binding changed during read", ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return payload, nil
}

func readVerifiedSyncedBlob(ctx context.Context, binding syncedSourceBinding, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hash := strings.TrimPrefix(binding.Hash, "sha256:")
	digest, err := hex.DecodeString(hash)
	if err != nil || !strings.HasPrefix(binding.Hash, "sha256:") || len(digest) != sha256.Size || strings.ToLower(hash) != hash || binding.Size < 0 || !filepath.IsAbs(binding.Path) || filepath.Clean(binding.Path) != binding.Path {
		return nil, fmt.Errorf("%w: invalid synced blob binding", ErrConflict)
	}
	if maxBytes <= 0 {
		maxBytes = defaultSyncedSourceMaxBytes
	}
	if binding.Size > maxBytes || binding.Size == int64(^uint64(0)>>1) {
		return nil, fmt.Errorf("synced source size %d exceeds max_bytes %d", binding.Size, maxBytes)
	}
	parent, name := filepath.Dir(binding.Path), filepath.Base(binding.Path)
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, fmt.Errorf("open synced blob parent: %w", err)
	}
	defer root.Close()
	parentBefore, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	// Hold the parent and refuse a link or special leaf. NONBLOCK also makes a
	// substituted FIFO fail promptly, without waiting for an external writer.
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open synced blob: %w", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() != binding.Size {
		return nil, fmt.Errorf("%w: synced blob type or size mismatch", ErrConflict)
	}
	payload, err := io.ReadAll(io.LimitReader(file, binding.Size+1))
	if err != nil {
		return nil, fmt.Errorf("read synced blob: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	named, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	parentAfter, err := os.Stat(parent)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	if !os.SameFile(parentBefore, parentAfter) || !os.SameFile(before, named) || !named.Mode().IsRegular() || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(payload)) != binding.Size || hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("%w: synced blob identity or content mismatch", ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return payload, nil
}
