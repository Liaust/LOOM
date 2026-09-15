package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/storagecatalog"
)

type Service struct {
	DB                *sql.DB
	PrivateBackupRoot string
	Progress          realtime.Service
}

func NewService(db *sql.DB) Service {
	return NewServiceWithPrivateBackupRoot(db, filepath.Join("/var/lib/loom", "private-backups"))
}

func NewServiceWithPrivateBackupRoot(db *sql.DB, privateBackupRoot string) Service {
	privateBackupRoot = strings.TrimSpace(privateBackupRoot)
	if privateBackupRoot == "" {
		privateBackupRoot = filepath.Join("/var/lib/loom", "private-backups")
	}
	return Service{DB: db, PrivateBackupRoot: privateBackupRoot, Progress: realtime.NewService(db)}
}

func (s Service) PushBatch(ctx context.Context, req requestctx.Context, input PushBatchInput) (PushBatchResult, error) {
	input, err := normalizePushBatchInput(input)
	if err != nil {
		return PushBatchResult{}, err
	}
	if input.CredentialToken == "" {
		return PushBatchResult{}, fmt.Errorf("credential_token is required")
	}
	nodeService := nodes.NewService(s.DB)
	_, node, err := nodeService.AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return PushBatchResult{}, err
	}
	if input.NodeRef != "" && input.NodeRef != node.NodeID && input.NodeRef != node.NodeKey {
		return PushBatchResult{}, fmt.Errorf("credential does not belong to node %s", input.NodeRef)
	}
	if len(input.Items) == 0 {
		return PushBatchResult{}, fmt.Errorf("items are required")
	}

	if input.IdempotencyKey != "" {
		if existing, err := s.getBatchByIdempotency(ctx, node.NodeID, input.IdempotencyKey); err == nil {
			result, err := s.batchResult(ctx, existing)
			if err == nil && hasMetadataItems(input) && !metadataBatchReplayMatches(input, result) {
				return PushBatchResult{}, fmt.Errorf("metadata batch idempotency payload mismatch")
			}
			return result, err
		} else if err != sql.ErrNoRows {
			return PushBatchResult{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PushBatchResult{}, err
	}
	defer tx.Rollback()
	if hasMetadataItems(input) {
		// Serialize stream identities and cursor advancement across batches.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "sync-metadata:"+node.NodeID); err != nil {
			return PushBatchResult{}, err
		}
	}

	cursorBefore, err := s.cursorSnapshotTx(ctx, tx, node.NodeID)
	if err != nil {
		return PushBatchResult{}, err
	}
	batchID := ids.NewSyncBatchID()
	batch, err := insertBatchTx(ctx, tx, batchID, node.NodeID, input, cursorBefore)
	if err != nil {
		return PushBatchResult{}, err
	}

	eventReq := req
	eventReq.OriginNodeID = node.NodeID
	eventReq.OriginNodeKey = node.NodeKey
	eventReq.Source = "node-sync"

	results := make([]SyncItemResult, 0, len(input.Items))
	conflicts := []SyncConflict{}
	streams := map[string]struct{}{}
	acceptedCount := 0
	conflictCount := 0
	failedCount := 0
	for _, item := range input.Items {
		streams[item.StreamName] = struct{}{}
		result, conflict, err := s.ingestBatchItemTx(ctx, tx, eventReq, batchID, node.NodeID, item)
		if err != nil {
			if item.ItemKind == ItemKindObjectMetadata {
				return PushBatchResult{}, err
			}
			result = SyncItemResult{
				LocalRef:      item.LocalRef,
				ItemKind:      item.ItemKind,
				StreamName:    item.StreamName,
				LocalSequence: item.LocalSequence,
				Status:        ItemStatusFailed,
				ErrorCode:     "sync.item_failed",
				ErrorMessage:  err.Error(),
				PayloadHash:   hashJSON(item.PayloadJSON),
				Metadata:      json.RawMessage(`{}`),
			}
			failedCount++
		} else {
			switch result.Status {
			case ItemStatusAccepted, ItemStatusDuplicate:
				acceptedCount++
			case ItemStatusConflicted:
				conflictCount++
			case ItemStatusFailed:
				failedCount++
			}
		}
		results = append(results, result)
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
	}

	cursors := []SyncCursor{}
	for streamName := range streams {
		cursor, err := s.advanceCursorTx(ctx, tx, node.NodeID, streamName, batchID)
		if err != nil {
			return PushBatchResult{}, err
		}
		cursors = append(cursors, cursor)
	}
	cursorAfter, err := s.cursorSnapshotTx(ctx, tx, node.NodeID)
	if err != nil {
		return PushBatchResult{}, err
	}

	status := BatchStatusAccepted
	switch {
	case failedCount > 0:
		status = BatchStatusPartial
	case conflictCount > 0 && acceptedCount == 0:
		status = BatchStatusConflicted
	case conflictCount > 0:
		status = BatchStatusPartial
	}
	batch, err = updateBatchCompletionTx(ctx, tx, batchID, status, acceptedCount, conflictCount, failedCount, cursorAfter)
	if err != nil {
		return PushBatchResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return PushBatchResult{}, err
	}
	s.recordSyncProgress(ctx, eventReq, batch)
	return PushBatchResult{
		Batch:     batch,
		Items:     results,
		Cursors:   cursors,
		Conflicts: conflicts,
	}, nil
}

func (s Service) GetStatus(ctx context.Context, nodeRef string) (SyncStatus, error) {
	node, err := nodes.NewService(s.DB).GetNode(ctx, nodeRef)
	if err != nil {
		return SyncStatus{}, err
	}
	cursors, err := s.ListCursors(ctx, node.NodeID)
	if err != nil {
		return SyncStatus{}, err
	}
	batches, err := s.ListBatches(ctx, ListFilter{NodeRef: node.NodeID, Limit: 10})
	if err != nil {
		return SyncStatus{}, err
	}
	conflicts, err := s.ListConflicts(ctx, ListFilter{NodeRef: node.NodeID, Status: "open", Limit: 10})
	if err != nil {
		return SyncStatus{}, err
	}
	replicas, err := s.ListReplicas(ctx, ListFilter{NodeRef: node.NodeID, Limit: 10})
	if err != nil {
		return SyncStatus{}, err
	}
	return SyncStatus{
		Node:          node,
		Cursors:       cursors,
		RecentBatches: batches,
		OpenConflicts: conflicts,
		Replicas:      replicas,
		Summary: SyncSummary{
			CursorCount:       len(cursors),
			RecentBatchCount:  len(batches),
			OpenConflictCount: len(conflicts),
			ReplicaCount:      len(replicas),
		},
	}, nil
}

func (s Service) ListCursors(ctx context.Context, nodeRef string) ([]SyncCursor, error) {
	nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, nodeRef)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, syncCursorSelectSQL()+`
		WHERE node_id = $1
		ORDER BY stream_name
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cursors := []SyncCursor{}
	for rows.Next() {
		cursor, err := scanSyncCursor(rows)
		if err != nil {
			return nil, err
		}
		cursors = append(cursors, cursor)
	}
	return cursors, rows.Err()
}

func (s Service) ListBatches(ctx context.Context, filter ListFilter) ([]SyncBatch, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := syncBatchSelectSQL() + ` WHERE true`
	args := []any{}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		query += fmt.Sprintf(" AND origin_node_id = $%d", len(args))
	}
	if filter.Status != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY received_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batches := []SyncBatch{}
	for rows.Next() {
		batch, err := scanSyncBatch(rows)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}

func (s Service) ListConflicts(ctx context.Context, filter ListFilter) ([]SyncConflict, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := syncConflictSelectSQL() + ` WHERE true`
	args := []any{}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		query += fmt.Sprintf(" AND origin_node_id = $%d", len(args))
	}
	if filter.Status != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conflicts := []SyncConflict{}
	for rows.Next() {
		conflict, err := scanSyncConflict(rows)
		if err != nil {
			return nil, err
		}
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}

func (s Service) ListReplicas(ctx context.Context, filter ListFilter) ([]SyncReplica, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := syncReplicaSelectSQL() + ` AS sr WHERE true`
	args := []any{}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		query += fmt.Sprintf(" AND source_node_id = $%d", len(args))
	}
	if filter.Status != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		query += fmt.Sprintf(" AND freshness_state = $%d", len(args))
	}
	if filter.ProjectRef != "" {
		project, err := projects.NewService(s.DB).ResolveProjectRef(ctx, filter.ProjectRef)
		if err != nil {
			return nil, err
		}
		args = append(args, project.ProjectScopeID)
		query += fmt.Sprintf(`
			AND EXISTS (
				SELECT 1
				FROM objects.object_scope_links osl
				WHERE osl.scope_id = $%d
				  AND osl.object_id = sr.metadata->>'object_id'
				  AND osl.relevance_status = 'active'
			)`, len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC, created_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	replicas := []SyncReplica{}
	for rows.Next() {
		replica, err := scanSyncReplica(rows)
		if err != nil {
			return nil, err
		}
		replicas = append(replicas, replica)
	}
	return replicas, rows.Err()
}

func (s Service) IngestSyncedObject(ctx context.Context, req requestctx.Context, objectService objects.Service, searchService search.Service, input SyncedObjectInput) (SyncedObjectResult, error) {
	input, err := normalizeSyncedObjectInput(input)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	_, node, err := nodes.NewService(s.DB).AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	if input.NodeRef != "" && input.NodeRef != node.NodeID && input.NodeRef != node.NodeKey {
		return SyncedObjectResult{}, fmt.Errorf("credential does not belong to node %s", input.NodeRef)
	}

	if input.IdempotencyKey != "" {
		if existing, err := s.getBatchByIdempotency(ctx, node.NodeID, input.IdempotencyKey); err == nil {
			return s.syncedObjectResultFromBatch(ctx, objectService, existing)
		} else if err != sql.ErrNoRows {
			return SyncedObjectResult{}, err
		}
	}

	payload := syncedObjectPayload(input)
	payloadHash := hashJSON(payload)
	if existing, err := s.getAcceptedObjectItemByLocalObjectVersion(ctx, node.NodeID, input.LocalObjectRef, input.LocalVersionRef); err == nil {
		if existing.PayloadHash == payloadHash {
			return s.recordDuplicateSyncedObject(ctx, node.NodeID, input, existing)
		}
		return s.recordConflictedSyncedObject(ctx, node.NodeID, input, payload, objectOrDefault(mustJSON(map[string]any{
			"global_ref":   existing.GlobalRef,
			"payload_hash": existing.PayloadHash,
		})), ConflictObjectHashMismatch, "local object version was already synced with a different payload")
	} else if err != sql.ErrNoRows {
		return SyncedObjectResult{}, err
	}

	content, err := base64.StdEncoding.DecodeString(input.ContentBase64)
	if err != nil {
		return SyncedObjectResult{}, fmt.Errorf("content_base64 is invalid: %w", err)
	}
	if int64(len(content)) != input.SizeBytes {
		return SyncedObjectResult{}, fmt.Errorf("size_bytes does not match decoded content length")
	}
	if got := hashBytes(content); got != input.HashURI {
		return s.recordConflictedSyncedObject(ctx, node.NodeID, input, payload, objectOrDefault(mustJSON(map[string]any{
			"declared_hash_uri": input.HashURI,
			"computed_hash_uri": got,
		})), ConflictObjectHashMismatch, "uploaded content hash does not match hash_uri")
	}

	tempPath, cleanup, err := writeSyncedObjectTemp(content, input.LogicalName)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	defer cleanup()

	objectReq := req
	objectReq.OriginNodeID = node.NodeID
	objectReq.OriginNodeKey = node.NodeKey
	objectReq.Source = "node-sync"
	metadata := objectOrDefault(input.Metadata)
	ingestInput := objects.IngestFileInput{
		Path:             tempPath,
		ProjectRef:       input.ProjectRef,
		ScopeRef:         input.ScopeRef,
		Name:             input.LogicalName,
		RelationshipType: "primary",
		ObjectType:       "file",
		StateClass:       "replica",
		Metadata: objectOrDefault(mustJSON(map[string]any{
			"source":            "node-sync",
			"local_object_ref":  input.LocalObjectRef,
			"local_version_ref": input.LocalVersionRef,
			"source_path":       input.SourcePath,
			"metadata":          jsonObject(metadata),
		})),
	}
	var ingestResult objects.IngestFileResult
	if previous, err := s.getLatestAcceptedObjectItemByLocalRef(ctx, node.NodeID, input.LocalObjectRef); err == nil {
		previousObjectID := metadataString(previous.Metadata, "object_id")
		if previousObjectID == "" {
			previousObjectID = previous.GlobalRef
		}
		ingestResult, err = objectService.IngestFileVersion(ctx, objectReq, previousObjectID, ingestInput)
		if err != nil {
			return SyncedObjectResult{}, err
		}
	} else if err == sql.ErrNoRows {
		ingestResult, err = objectService.IngestFile(ctx, objectReq, ingestInput)
		if err != nil {
			return SyncedObjectResult{}, err
		}
	} else {
		return SyncedObjectResult{}, err
	}
	if ingestResult.Object.LatestVersion == nil || ingestResult.Object.Blob == nil {
		return SyncedObjectResult{}, fmt.Errorf("object ingest did not produce a version/blob")
	}
	objectID := ingestResult.Object.Object.ObjectID
	versionID := ingestResult.Object.LatestVersion.ObjectVersionID
	blobID := ingestResult.Object.Blob.BlobID
	if err := s.applySyncedObjectMetadata(ctx, node.NodeID, input, objectID, versionID); err != nil {
		return SyncedObjectResult{}, err
	}

	indexStatus := "not_requested"
	if input.IndexingState != "" && input.IndexingState != storagecatalog.IndexingStateIndexed {
		indexStatus = input.IndexingState
	} else if indexPolicyAllowsText(input.IndexPolicy) {
		indexResult, err := searchService.EnqueueObjectVersion(ctx, objectReq, search.IndexEnqueueInput{
			ObjectID:        objectID,
			ObjectVersionID: versionID,
		})
		if err != nil {
			indexStatus = "failed"
		} else {
			indexStatus = indexResult.Status
		}
	} else if input.IndexPolicy == IndexPolicyNone || input.IndexPolicy == IndexPolicyPrivateNoIndex {
		indexStatus = "disabled_by_policy"
	}

	return s.recordAcceptedSyncedObject(ctx, node.NodeID, input, payloadHash, objectID, versionID, blobID, ingestResult.Object.Blob.HashURI, indexStatus)
}

func (s Service) StorePrivateBackup(ctx context.Context, req requestctx.Context, input PrivateBackupInput) (PrivateBackupResult, error) {
	input, err := normalizePrivateBackupInput(input)
	if err != nil {
		return PrivateBackupResult{}, err
	}
	_, node, err := nodes.NewService(s.DB).AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return PrivateBackupResult{}, err
	}
	if input.NodeRef != "" && input.NodeRef != node.NodeID && input.NodeRef != node.NodeKey {
		return PrivateBackupResult{}, fmt.Errorf("credential does not belong to node %s", input.NodeRef)
	}
	if input.IdempotencyKey != "" {
		if existing, err := s.getPrivateBackupByIdempotency(ctx, node.NodeID, input.IdempotencyKey); err == nil {
			return PrivateBackupResult{Operation: existing}, nil
		} else if err != sql.ErrNoRows {
			return PrivateBackupResult{}, err
		}
	}
	payload, err := base64.StdEncoding.DecodeString(input.PayloadBase64)
	if err != nil {
		return PrivateBackupResult{}, fmt.Errorf("payload_base64 is invalid: %w", err)
	}
	if int64(len(payload)) != input.CoarseSizeBytes {
		return PrivateBackupResult{}, fmt.Errorf("coarse_size_bytes does not match decoded payload length")
	}
	operationID := ids.NewPrivateBackupID()
	metadata := sanitizedPrivateBackupMetadata(input.Metadata)
	operationDir, rootKey, batchKey, err := privateBackupOperationDir(s.PrivateBackupRoot, node.NodeKey, operationID, metadata)
	if err != nil {
		return PrivateBackupResult{}, err
	}
	storageRef := filepath.Join(operationDir, "payload.tar")
	payloadHash := sha256.Sum256(payload)
	metadataObject := jsonObject(metadata)
	metadataObject["payload_sha256"] = hex.EncodeToString(payloadHash[:])
	metadataObject["source_node_key"] = node.NodeKey
	metadataObject["root_key"] = rootKey
	metadataObject["custody_batch_key"] = batchKey
	metadataObject["custody_relative_path"] = filepath.ToSlash(strings.TrimPrefix(storageRef, filepath.Clean(s.PrivateBackupRoot)+string(filepath.Separator)))
	metadata = objectOrDefault(mustJSON(metadataObject))
	manifest := objectOrDefault(mustJSON(map[string]any{
		"schema_version":              "storage.private_backup_manifest.v0.7",
		"private_backup_operation_id": operationID,
		"source_node_id":              node.NodeID,
		"source_node_key":             node.NodeKey,
		"root_key":                    rootKey,
		"batch_key":                   batchKey,
		"payload_path":                "payload.tar",
		"payload_sha256":              hex.EncodeToString(payloadHash[:]),
		"coarse_size_bytes":           input.CoarseSizeBytes,
		"metadata":                    metadataObject,
	}))
	if err := writePrivateBackupCustody(s.PrivateBackupRoot, operationDir, payload, manifest); err != nil {
		return PrivateBackupResult{}, err
	}
	operation, err := insertPrivateBackupOperation(ctx, s.DB, operationID, node.NodeID, input.IdempotencyKey, input.CoarseSizeBytes, storageRef, metadata)
	if err != nil {
		_ = removePrivateBackupCustody(s.PrivateBackupRoot, operationDir)
		return PrivateBackupResult{}, err
	}
	eventReq := req
	eventReq.OriginNodeID = node.NodeID
	eventReq.OriginNodeKey = node.NodeKey
	eventReq.Source = "node-sync"
	_, _ = events.NewService(s.DB).Append(ctx, events.AppendInput{
		EventType:       events.TypeSyncPrivateBackupStored,
		EventLevel:      "audit",
		Request:         eventReq,
		TargetKind:      "private_backup",
		TargetID:        operation.PrivateBackupOperationID,
		Status:          "stored",
		Result:          "ok",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"private_backup_operation_id": operation.PrivateBackupOperationID,
			"origin_node_id":              node.NodeID,
			"coarse_size_bytes":           operation.CoarseSizeBytes,
		},
	})
	s.recordPrivateBackupProgress(ctx, eventReq, operation)
	return PrivateBackupResult{Operation: operation}, nil
}

func privateBackupOperationDir(root, nodeKey, operationID string, metadata json.RawMessage) (string, string, string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return "", "", "", fmt.Errorf("private backup root is required")
	}
	nodeSegment, err := privateBackupPathSegment("source node key", nodeKey)
	if err != nil {
		return "", "", "", err
	}
	operationSegment, err := privateBackupPathSegment("private backup operation ID", operationID)
	if err != nil {
		return "", "", "", err
	}
	object := jsonObject(metadata)
	rootKey, _ := object["root_key"].(string)
	if strings.TrimSpace(rootKey) == "" {
		rootKey = "private"
	}
	rootSegment, err := privateBackupPathSegment("root key", rootKey)
	if err != nil {
		return "", "", "", err
	}
	batchKey, _ := object["local_batch_id"].(string)
	if strings.TrimSpace(batchKey) == "" {
		batchKey = operationID
	}
	batchSegment, err := privateBackupPathSegment("backup batch key", batchKey)
	if err != nil {
		return "", "", "", err
	}
	operationDir := filepath.Join(root, nodeSegment, rootSegment, batchSegment)
	if batchSegment != operationSegment {
		operationDir = filepath.Join(operationDir, "artifacts", operationSegment)
	}
	return operationDir, rootSegment, batchSegment, nil
}

func privateBackupPathSegment(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") || filepath.Base(value) != value {
		return "", fmt.Errorf("%s is not a safe path segment", label)
	}
	return value, nil
}

func writePrivateBackupFile(path string, payload []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func writePrivateBackupCustody(backupRoot, operationDir string, payload, manifest []byte) error {
	backupRoot = filepath.Clean(strings.TrimSpace(backupRoot))
	if backupRoot == "." || backupRoot == "" {
		return fmt.Errorf("private backup root is required")
	}
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return fmt.Errorf("create private backup root: %w", err)
	}
	rootInfo, err := os.Lstat(backupRoot)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("private backup root must be a real directory")
	}
	relativeDir, err := filepath.Rel(backupRoot, filepath.Clean(operationDir))
	if err != nil || relativeDir == "." || relativeDir == "" || relativeDir == ".." || strings.HasPrefix(relativeDir, ".."+string(filepath.Separator)) {
		return fmt.Errorf("private backup operation path escapes backup root")
	}
	confined, err := os.OpenRoot(backupRoot)
	if err != nil {
		return err
	}
	defer confined.Close()
	parent := filepath.Dir(relativeDir)
	if err := confined.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create private backup operation parent: %w", err)
	}
	current := ""
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := confined.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("private backup custody component %q must be a real directory", current)
		}
	}
	if _, err := confined.Lstat(relativeDir); err == nil {
		return fmt.Errorf("private backup custody operation already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	tempDir := filepath.Join(parent, "."+filepath.Base(relativeDir)+".tmp")
	if err := confined.Mkdir(tempDir, 0o700); err != nil {
		return fmt.Errorf("create private backup operation temp: %w", err)
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = confined.RemoveAll(tempDir)
		}
	}()
	payloadPath := filepath.Join(tempDir, "payload.tar")
	manifestPath := filepath.Join(tempDir, "manifest.json")
	if err := writePrivateBackupRootFile(confined, payloadPath, payload, 0o600); err != nil {
		return fmt.Errorf("write private backup payload: %w", err)
	}
	if err := writePrivateBackupRootFile(confined, manifestPath, manifest, 0o600); err != nil {
		return fmt.Errorf("write private backup manifest: %w", err)
	}
	if err := confined.Rename(tempDir, relativeDir); err != nil {
		return fmt.Errorf("publish private backup operation: %w", err)
	}
	removeTemp = false
	return nil
}

func writePrivateBackupRootFile(root *os.Root, path string, payload []byte, mode os.FileMode) error {
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = root.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(path)
		return err
	}
	return nil
}

func removePrivateBackupCustody(backupRoot, operationDir string) error {
	backupRoot = filepath.Clean(strings.TrimSpace(backupRoot))
	relativeDir, err := filepath.Rel(backupRoot, filepath.Clean(operationDir))
	if err != nil || relativeDir == "." || relativeDir == "" || relativeDir == ".." || strings.HasPrefix(relativeDir, ".."+string(filepath.Separator)) {
		return fmt.Errorf("private backup operation path escapes backup root")
	}
	root, err := os.OpenRoot(backupRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(relativeDir)
}

func (s Service) CreateDeletionRequest(ctx context.Context, req requestctx.Context, input DeletionRequestInput) (DeletionRequestResult, error) {
	input, err := normalizeDeletionRequestInput(input)
	if err != nil {
		return DeletionRequestResult{}, err
	}
	_, node, err := nodes.NewService(s.DB).AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return DeletionRequestResult{}, err
	}
	if input.NodeRef != "" && input.NodeRef != node.NodeID && input.NodeRef != node.NodeKey {
		return DeletionRequestResult{}, fmt.Errorf("credential does not belong to node %s", input.NodeRef)
	}
	if input.IdempotencyKey != "" {
		if existing, err := s.getDeletionRequestByIdempotency(ctx, node.NodeID, input.IdempotencyKey); err == nil {
			return DeletionRequestResult{Request: existing}, nil
		} else if err != sql.ErrNoRows {
			return DeletionRequestResult{}, err
		}
	}
	metadata := objectOrDefault(mustJSON(map[string]any{
		"source":          "loom-node-agent",
		"idempotency_key": input.IdempotencyKey,
		"client_metadata": jsonObject(objectOrDefault(input.Metadata)),
	}))
	deletion, err := insertDeletionRequest(ctx, s.DB, ids.NewDeletionRequestID(), node.NodeID, input, metadata)
	if err != nil {
		return DeletionRequestResult{}, err
	}
	eventReq := req
	eventReq.OriginNodeID = node.NodeID
	eventReq.OriginNodeKey = node.NodeKey
	eventReq.Source = "node-sync"
	if _, err := events.NewService(s.DB).Append(ctx, events.AppendInput{
		EventType:       events.TypeSyncDeletionRequested,
		EventLevel:      "audit",
		Request:         eventReq,
		TargetKind:      input.TargetKind,
		TargetID:        input.TargetRef,
		Status:          "recorded",
		Result:          "no_hard_delete",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"deletion_request_id": deletion.DeletionRequestID,
			"origin_node_id":      node.NodeID,
			"target_kind":         input.TargetKind,
			"target_ref":          input.TargetRef,
			"requested_action":    input.RequestedAction,
		},
	}); err != nil {
		return DeletionRequestResult{}, err
	}
	return DeletionRequestResult{Request: deletion}, nil
}

func (s Service) recordSyncProgress(ctx context.Context, req requestctx.Context, batch SyncBatch) {
	if s.Progress.DB == nil {
		return
	}
	status := realtime.ProgressStatusSucceeded
	severity := realtime.ProgressSeverityNormal
	message := "Sync batch accepted."
	switch batch.Status {
	case BatchStatusPartial:
		status = realtime.ProgressStatusWarning
		severity = realtime.ProgressSeverityWarning
		message = "Sync batch partially accepted."
	case BatchStatusConflicted:
		status = realtime.ProgressStatusWarning
		severity = realtime.ProgressSeverityWarning
		message = "Sync batch has conflicts."
	case BatchStatusFailed:
		status = realtime.ProgressStatusFailed
		severity = realtime.ProgressSeverityError
		message = "Sync batch failed."
	case BatchStatusReceived:
		status = realtime.ProgressStatusRunning
		message = "Sync batch received."
	}
	_, _ = s.Progress.UpdateProgress(ctx, req, realtime.UpdateProgressInput{
		SourceKind: realtime.ProgressSourceKindSync,
		SourceRef:  batch.SyncBatchID,
		Status:     status,
		Stage:      "batch_completed",
		Message:    message,
		Payload: objectOrDefault(mustJSON(map[string]any{
			"sync_batch_id":  batch.SyncBatchID,
			"origin_node_id": batch.OriginNodeID,
			"batch_kind":     batch.BatchKind,
			"item_count":     batch.ItemCount,
			"accepted_count": batch.AcceptedCount,
			"conflict_count": batch.ConflictCount,
			"failed_count":   batch.FailedCount,
		})),
		Severity: severity,
		Metadata: json.RawMessage(`{}`),
	})
}

func (s Service) recordPrivateBackupProgress(ctx context.Context, req requestctx.Context, operation PrivateBackupOperation) {
	if s.Progress.DB == nil {
		return
	}
	_, _ = s.Progress.UpdateProgress(ctx, req, realtime.UpdateProgressInput{
		SourceKind: realtime.ProgressSourceKindPrivateBackup,
		SourceRef:  operation.PrivateBackupOperationID,
		Status:     realtime.ProgressStatusSucceeded,
		Stage:      "stored",
		Message:    "Private raw backup stored.",
		Payload: objectOrDefault(mustJSON(map[string]any{
			"private_backup_operation_id": operation.PrivateBackupOperationID,
			"origin_node_id":              operation.OriginNodeID,
			"status":                      operation.Status,
			"coarse_size_bytes":           operation.CoarseSizeBytes,
		})),
		Severity: realtime.ProgressSeverityNormal,
		Metadata: json.RawMessage(`{}`),
	})
}

func (s Service) ListPrivateBackups(ctx context.Context, filter ListFilter) ([]PrivateBackupOperation, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := privateBackupSelectSQL() + ` WHERE true`
	args := []any{}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, nodeID)
		query += fmt.Sprintf(" AND origin_node_id = $%d", len(args))
	}
	if filter.Status != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	operations := []PrivateBackupOperation{}
	for rows.Next() {
		operation, err := scanPrivateBackupOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (s Service) ListDeletionRequests(ctx context.Context, filter ListFilter) ([]DeletionRequest, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := deletionRequestSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if filter.NodeRef != "" {
		nodeID, err := nodes.NewService(s.DB).ResolveNodeRef(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		add("origin_node_id =", nodeID)
	}
	if filter.Status != "" {
		status := strings.ToLower(strings.TrimSpace(filter.Status))
		switch status {
		case DeletionRequestStatusAll:
		case DeletionRequestStatusActive:
			query += " AND status IN ('pending_review', 'recorded')"
		default:
			normalized := NormalizeDeletionRequestStatus(status)
			if normalized == "" {
				return nil, fmt.Errorf("invalid deletion request status %q", filter.Status)
			}
			add("status =", normalized)
		}
	} else if filter.ActiveOnly && !filter.IncludeResolved {
		query += " AND status IN ('pending_review', 'recorded')"
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY requested_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deletions := []DeletionRequest{}
	for rows.Next() {
		deletion, err := scanDeletionRequest(rows)
		if err != nil {
			return nil, err
		}
		deletions = append(deletions, deletion)
	}
	return deletions, rows.Err()
}

func (s Service) GetDeletionRequest(ctx context.Context, ref string) (DeletionRequest, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return DeletionRequest{}, fmt.Errorf("deletion request ref is required")
	}
	return scanDeletionRequest(s.DB.QueryRowContext(ctx, deletionRequestSelectSQL()+`
		WHERE deletion_request_id = $1
		LIMIT 1
	`, ref))
}

func (s Service) ReviewDeletionRequest(ctx context.Context, req requestctx.Context, input DeletionRequestUpdateInput) (DeletionRequest, error) {
	return s.updateDeletionRequestStatus(ctx, req, input, DeletionRequestStatusPendingReview)
}

func (s Service) ApproveDeletionRequest(ctx context.Context, req requestctx.Context, input DeletionRequestUpdateInput) (DeletionRequest, error) {
	return s.updateDeletionRequestStatus(ctx, req, input, DeletionRequestStatusApproved)
}

func (s Service) DenyDeletionRequest(ctx context.Context, req requestctx.Context, input DeletionRequestUpdateInput) (DeletionRequest, error) {
	if strings.TrimSpace(input.Reason) == "" {
		return DeletionRequest{}, fmt.Errorf("reason is required when denying a deletion request")
	}
	return s.updateDeletionRequestStatus(ctx, req, input, DeletionRequestStatusDenied)
}

func (s Service) CompleteDeletionRequest(ctx context.Context, req requestctx.Context, input DeletionRequestUpdateInput) (DeletionRequest, error) {
	return s.updateDeletionRequestStatus(ctx, req, input, DeletionRequestStatusCompleted)
}

func (s Service) updateDeletionRequestStatus(ctx context.Context, req requestctx.Context, input DeletionRequestUpdateInput, targetStatus string) (DeletionRequest, error) {
	requestRef := strings.TrimSpace(input.RequestRef)
	if requestRef == "" {
		return DeletionRequest{}, fmt.Errorf("request_ref is required")
	}
	targetStatus = NormalizeDeletionRequestStatus(targetStatus)
	if targetStatus == "" || targetStatus == DeletionRequestStatusRecorded {
		return DeletionRequest{}, fmt.Errorf("target deletion request status is invalid")
	}
	reason := strings.TrimSpace(input.Reason)
	if len(reason) > 1000 {
		return DeletionRequest{}, fmt.Errorf("reason is too long")
	}
	current, err := s.GetDeletionRequest(ctx, requestRef)
	if err != nil {
		return DeletionRequest{}, err
	}
	if !deletionRequestTransitionAllowed(current.Status, targetStatus) {
		return DeletionRequest{}, fmt.Errorf("cannot move deletion request %s from %s to %s", current.DeletionRequestID, current.Status, targetStatus)
	}
	return scanDeletionRequest(s.DB.QueryRowContext(ctx, deletionRequestSelectSQL(`
		UPDATE sync.deletion_requests
		SET status = $2,
		    reason = CASE WHEN nullif($3, '') IS NULL THEN reason ELSE $3 END,
		    reviewed_at = now(),
		    reviewed_by_actor_id = nullif($4, '')
		WHERE deletion_request_id = $1
		RETURNING
	`), current.DeletionRequestID, targetStatus, reason, req.ActorID))
}

func deletionRequestTransitionAllowed(from, to string) bool {
	from = NormalizeDeletionRequestStatus(from)
	to = NormalizeDeletionRequestStatus(to)
	if from == "" {
		from = DeletionRequestStatusPendingReview
	}
	if to == "" || to == DeletionRequestStatusRecorded {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case DeletionRequestStatusRecorded, DeletionRequestStatusPendingReview:
		return true
	case DeletionRequestStatusApproved:
		return to == DeletionRequestStatusCompleted || to == DeletionRequestStatusDenied
	default:
		return false
	}
}

func (s Service) getBatchByIdempotency(ctx context.Context, nodeID, key string) (SyncBatch, error) {
	return scanSyncBatch(s.DB.QueryRowContext(ctx, syncBatchSelectSQL()+`
		WHERE origin_node_id = $1 AND idempotency_key = $2
	`, nodeID, key))
}

func (s Service) batchResult(ctx context.Context, batch SyncBatch) (PushBatchResult, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT sync_batch_item_id, local_ref, item_kind,
		       COALESCE(payload_json->>'stream_name', ''), COALESCE((payload_json->>'local_sequence')::bigint, 0),
		       status, global_ref, error_code, error_message, payload_hash, metadata
		FROM sync.batch_items
		WHERE sync_batch_id = $1
		ORDER BY created_at ASC
	`, batch.SyncBatchID)
	if err != nil {
		return PushBatchResult{}, err
	}
	defer rows.Close()
	items := []SyncItemResult{}
	for rows.Next() {
		var item SyncItemResult
		var metadata []byte
		if err := rows.Scan(
			&item.SyncBatchItemID,
			&item.LocalRef,
			&item.ItemKind,
			&item.StreamName,
			&item.LocalSequence,
			&item.Status,
			&item.GlobalRef,
			&item.ErrorCode,
			&item.ErrorMessage,
			&item.PayloadHash,
			&metadata,
		); err != nil {
			return PushBatchResult{}, err
		}
		item.Metadata = jsonOrEmpty(metadata)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return PushBatchResult{}, err
	}
	cursors, err := s.ListCursors(ctx, batch.OriginNodeID)
	if err != nil {
		return PushBatchResult{}, err
	}
	conflicts, err := s.ListConflicts(ctx, ListFilter{NodeRef: batch.OriginNodeID, Status: "open", Limit: 50})
	if err != nil {
		return PushBatchResult{}, err
	}
	return PushBatchResult{Batch: batch, Items: items, Cursors: cursors, Conflicts: conflicts}, nil
}

func (s Service) syncedObjectResultFromBatch(ctx context.Context, objectService objects.Service, batch SyncBatch) (SyncedObjectResult, error) {
	item, err := s.firstBatchItem(ctx, batch.SyncBatchID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	result := SyncedObjectResult{
		Batch:       batch,
		Item:        item,
		ObjectID:    item.GlobalRef,
		HashURI:     metadataString(item.Metadata, "hash_uri"),
		IndexStatus: metadataString(item.Metadata, "index_status"),
		Metadata:    item.Metadata,
	}
	result.VersionID = metadataString(item.Metadata, "object_version_id")
	result.BlobID = metadataString(item.Metadata, "blob_id")
	if item.GlobalRef != "" {
		detail, err := objectService.GetObject(ctx, item.GlobalRef)
		if err == nil {
			result.ObjectID = detail.Object.ObjectID
			if result.VersionID == "" && detail.LatestVersion != nil {
				result.VersionID = detail.LatestVersion.ObjectVersionID
			}
			if result.BlobID == "" && detail.Blob != nil {
				result.BlobID = detail.Blob.BlobID
			}
			if result.HashURI == "" && detail.Blob != nil {
				result.HashURI = detail.Blob.HashURI
			}
		}
	}
	if replicas, err := s.ListReplicas(ctx, ListFilter{NodeRef: batch.OriginNodeID, Limit: 50}); err == nil {
		for _, replica := range replicas {
			if replica.ReplicatedID == result.VersionID || replica.ReplicatedID == result.ObjectID {
				copy := replica
				result.Replica = &copy
				break
			}
		}
	}
	if cursors, err := s.ListCursors(ctx, batch.OriginNodeID); err == nil {
		for _, cursor := range cursors {
			if cursor.StreamName == item.StreamName {
				result.Cursor = cursor
				break
			}
		}
	}
	if conflicts, err := s.ListConflicts(ctx, ListFilter{NodeRef: batch.OriginNodeID, Status: "open", Limit: 50}); err == nil {
		for _, conflict := range conflicts {
			if conflict.SyncBatchID != nil && *conflict.SyncBatchID == batch.SyncBatchID && conflict.LocalRef == item.LocalRef {
				copy := conflict
				result.Conflict = &copy
				break
			}
		}
	}
	return result, nil
}

func (s Service) recordAcceptedSyncedObject(ctx context.Context, nodeID string, input SyncedObjectInput, payloadHash, objectID, versionID, blobID, hashURI, indexStatus string) (SyncedObjectResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	defer tx.Rollback()

	cursorBefore, err := s.cursorSnapshotTx(ctx, tx, nodeID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	batch, err := insertBatchTx(ctx, tx, ids.NewSyncBatchID(), nodeID, PushBatchInput{
		IdempotencyKey: input.IdempotencyKey,
		BatchKind:      BatchKindObjectBlobs,
		Items:          []PushBatchItem{{LocalRef: input.LocalObjectRef}},
		Metadata: objectOrDefault(mustJSON(map[string]any{
			"source": "node-sync-object-upload",
		})),
	}, cursorBefore)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	itemInput := objectUploadBatchItem(input)
	item, err := insertBatchItemTx(ctx, tx, batch.SyncBatchID, nodeID, itemInput, ItemStatusAccepted, objectID, "", "", payloadHash)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	item.Metadata = syncedObjectItemMetadata(input, map[string]any{
		"object_id":         objectID,
		"object_version_id": versionID,
		"blob_id":           blobID,
		"hash_uri":          hashURI,
		"index_status":      indexStatus,
	})
	if _, err := tx.ExecContext(ctx, `
		UPDATE sync.batch_items
		SET metadata = $2
		WHERE sync_batch_item_id = $1
	`, item.SyncBatchItemID, item.Metadata); err != nil {
		return SyncedObjectResult{}, err
	}
	replica, err := upsertReplicaTx(ctx, tx, nodeID, objectID, versionID, hashURI, input)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	cursor, err := s.advanceCursorTx(ctx, tx, nodeID, StreamObjectBlobs, batch.SyncBatchID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	cursorAfter, err := s.cursorSnapshotTx(ctx, tx, nodeID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	batch, err = updateBatchCompletionTx(ctx, tx, batch.SyncBatchID, BatchStatusAccepted, 1, 0, 0, cursorAfter)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncedObjectResult{}, err
	}
	return SyncedObjectResult{
		Batch:       batch,
		Item:        item,
		Cursor:      cursor,
		Replica:     &replica,
		ObjectID:    objectID,
		VersionID:   versionID,
		BlobID:      blobID,
		HashURI:     hashURI,
		IndexStatus: indexStatus,
		Metadata:    item.Metadata,
	}, nil
}

func (s Service) recordDuplicateSyncedObject(ctx context.Context, nodeID string, input SyncedObjectInput, existing SyncItemResult) (SyncedObjectResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	defer tx.Rollback()
	cursorBefore, err := s.cursorSnapshotTx(ctx, tx, nodeID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	batch, err := insertBatchTx(ctx, tx, ids.NewSyncBatchID(), nodeID, PushBatchInput{
		IdempotencyKey: input.IdempotencyKey,
		BatchKind:      BatchKindObjectBlobs,
		Items:          []PushBatchItem{{LocalRef: input.LocalObjectRef}},
	}, cursorBefore)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	itemInput := objectUploadBatchItem(input)
	item, err := insertBatchItemTx(ctx, tx, batch.SyncBatchID, nodeID, itemInput, ItemStatusDuplicate, existing.GlobalRef, "", "", existing.PayloadHash)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	item.Metadata = syncedObjectItemMetadata(input, jsonObject(existing.Metadata))
	if _, err := tx.ExecContext(ctx, `UPDATE sync.batch_items SET metadata = $2 WHERE sync_batch_item_id = $1`, item.SyncBatchItemID, item.Metadata); err != nil {
		return SyncedObjectResult{}, err
	}
	cursor, err := s.advanceCursorTx(ctx, tx, nodeID, StreamObjectBlobs, batch.SyncBatchID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	cursorAfter, err := s.cursorSnapshotTx(ctx, tx, nodeID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	batch, err = updateBatchCompletionTx(ctx, tx, batch.SyncBatchID, BatchStatusAccepted, 1, 0, 0, cursorAfter)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncedObjectResult{}, err
	}
	return SyncedObjectResult{
		Batch:       batch,
		Item:        item,
		Cursor:      cursor,
		ObjectID:    existing.GlobalRef,
		VersionID:   metadataString(existing.Metadata, "object_version_id"),
		BlobID:      metadataString(existing.Metadata, "blob_id"),
		HashURI:     metadataString(existing.Metadata, "hash_uri"),
		IndexStatus: metadataString(existing.Metadata, "index_status"),
		Metadata:    item.Metadata,
	}, nil
}

func (s Service) recordConflictedSyncedObject(ctx context.Context, nodeID string, input SyncedObjectInput, localPayload, mainPayload json.RawMessage, conflictType, summary string) (SyncedObjectResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	defer tx.Rollback()
	cursorBefore, err := s.cursorSnapshotTx(ctx, tx, nodeID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	batch, err := insertBatchTx(ctx, tx, ids.NewSyncBatchID(), nodeID, PushBatchInput{
		IdempotencyKey: input.IdempotencyKey,
		BatchKind:      BatchKindObjectBlobs,
		Items:          []PushBatchItem{{LocalRef: input.LocalObjectRef}},
	}, cursorBefore)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	conflict, err := insertConflictTx(ctx, tx, nodeID, batch.SyncBatchID, input.LocalObjectRef, conflictType, summary, localPayload, mainPayload)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	item, err := insertBatchItemTx(ctx, tx, batch.SyncBatchID, nodeID, objectUploadBatchItem(input), ItemStatusConflicted, "", "sync."+conflictType, summary, hashJSON(localPayload))
	if err != nil {
		return SyncedObjectResult{}, err
	}
	item.Metadata = syncedObjectItemMetadata(input, jsonObject(item.Metadata))
	if _, err := tx.ExecContext(ctx, `UPDATE sync.batch_items SET metadata = $2 WHERE sync_batch_item_id = $1`, item.SyncBatchItemID, item.Metadata); err != nil {
		return SyncedObjectResult{}, err
	}
	cursor, err := s.advanceCursorTx(ctx, tx, nodeID, StreamObjectBlobs, batch.SyncBatchID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	cursorAfter, err := s.cursorSnapshotTx(ctx, tx, nodeID)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	batch, err = updateBatchCompletionTx(ctx, tx, batch.SyncBatchID, BatchStatusConflicted, 0, 1, 0, cursorAfter)
	if err != nil {
		return SyncedObjectResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncedObjectResult{}, err
	}
	return SyncedObjectResult{
		Batch:    batch,
		Item:     item,
		Conflict: &conflict,
		Cursor:   cursor,
		Metadata: item.Metadata,
	}, nil
}

func (s Service) firstBatchItem(ctx context.Context, batchID string) (SyncItemResult, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT sync_batch_item_id, local_ref, item_kind,
		       COALESCE(payload_json->>'stream_name', ''), COALESCE((payload_json->>'local_sequence')::bigint, 0),
		       status, global_ref, error_code, error_message, payload_hash, metadata
		FROM sync.batch_items
		WHERE sync_batch_id = $1
		ORDER BY created_at ASC
		LIMIT 1
	`, batchID)
	var item SyncItemResult
	var metadata []byte
	if err := row.Scan(
		&item.SyncBatchItemID,
		&item.LocalRef,
		&item.ItemKind,
		&item.StreamName,
		&item.LocalSequence,
		&item.Status,
		&item.GlobalRef,
		&item.ErrorCode,
		&item.ErrorMessage,
		&item.PayloadHash,
		&metadata,
	); err != nil {
		return SyncItemResult{}, err
	}
	item.Metadata = jsonOrEmpty(metadata)
	return item, nil
}

func (s Service) getAcceptedObjectItemByLocalObjectVersion(ctx context.Context, nodeID, localRef, localVersionRef string) (SyncItemResult, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT sync_batch_item_id, local_ref, item_kind,
		       COALESCE(payload_json->>'stream_name', ''), COALESCE((payload_json->>'local_sequence')::bigint, 0),
		       status, global_ref, error_code, error_message, payload_hash, metadata
		FROM sync.batch_items
		WHERE origin_node_id = $1
		  AND local_ref = $2
		  AND item_kind = $3
		  AND status IN ('accepted', 'duplicate')
		  AND COALESCE(metadata->>'local_version_ref', payload_json->>'local_version_ref', '') = $4
		ORDER BY created_at DESC
		LIMIT 1
	`, nodeID, localRef, ItemKindObjectBlob, localVersionRef)
	return scanSyncItemResult(row)
}

func (s Service) getLatestAcceptedObjectItemByLocalRef(ctx context.Context, nodeID, localRef string) (SyncItemResult, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT sync_batch_item_id, local_ref, item_kind,
		       COALESCE(payload_json->>'stream_name', ''), COALESCE((payload_json->>'local_sequence')::bigint, 0),
		       status, global_ref, error_code, error_message, payload_hash, metadata
		FROM sync.batch_items
		WHERE origin_node_id = $1
		  AND local_ref = $2
		  AND item_kind = $3
		  AND status IN ('accepted', 'duplicate')
		ORDER BY created_at DESC
		LIMIT 1
	`, nodeID, localRef, ItemKindObjectBlob)
	return scanSyncItemResult(row)
}

func scanSyncItemResult(scanner interface{ Scan(dest ...any) error }) (SyncItemResult, error) {
	var item SyncItemResult
	var metadata []byte
	if err := scanner.Scan(
		&item.SyncBatchItemID,
		&item.LocalRef,
		&item.ItemKind,
		&item.StreamName,
		&item.LocalSequence,
		&item.Status,
		&item.GlobalRef,
		&item.ErrorCode,
		&item.ErrorMessage,
		&item.PayloadHash,
		&metadata,
	); err != nil {
		return SyncItemResult{}, err
	}
	item.Metadata = jsonOrEmpty(metadata)
	return item, nil
}

func (s Service) ingestBatchItemTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, batchID, nodeID string, item PushBatchItem) (SyncItemResult, *SyncConflict, error) {
	if item.ItemKind == ItemKindObjectMetadata {
		return ingestObjectMetadataTx(ctx, tx, req, batchID, nodeID, item)
	}
	payload := objectOrDefault(item.PayloadJSON)
	payloadHash := hashJSON(payload)
	if item.ItemKind != ItemKindEvent {
		conflict, err := insertConflictTx(ctx, tx, nodeID, batchID, item.LocalRef, ConflictUnsupportedItemKind, "unsupported sync item kind", payload, json.RawMessage(`{}`))
		if err != nil {
			return SyncItemResult{}, nil, err
		}
		result, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusConflicted, "", "sync.unsupported_item_kind", "unsupported sync item kind", payloadHash)
		return result, &conflict, err
	}

	existing, err := getIngestedEventByLocalRefTx(ctx, tx, nodeID, item.LocalRef)
	if err == nil {
		if existing.PayloadHash == payloadHash {
			result, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusDuplicate, existing.GlobalEventID, "", "", payloadHash)
			return result, nil, err
		}
		mainPayload := objectOrDefault(mustJSON(map[string]any{
			"global_event_id": existing.GlobalEventID,
			"payload_hash":    existing.PayloadHash,
		}))
		conflict, err := insertConflictTx(ctx, tx, nodeID, batchID, item.LocalRef, ConflictDuplicatePayloadMismatch, "local event was already ingested with a different payload", payload, mainPayload)
		if err != nil {
			return SyncItemResult{}, nil, err
		}
		result, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusConflicted, "", "sync.duplicate_payload_mismatch", "local event was already ingested with a different payload", payloadHash)
		return result, &conflict, err
	}
	if err != sql.ErrNoRows {
		return SyncItemResult{}, nil, err
	}

	createdAt := time.Now().UTC()
	if item.CreatedAt != nil && !item.CreatedAt.IsZero() {
		createdAt = item.CreatedAt.UTC()
	}
	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       item.EventType,
		EventLevel:      item.EventLevel,
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      "sync_event",
		TargetID:        item.LocalRef,
		Status:          "accepted",
		Result:          "synced",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"origin_node_id":   nodeID,
			"local_event_id":   item.LocalRef,
			"local_sequence":   item.LocalSequence,
			"stream_name":      item.StreamName,
			"local_created_at": createdAt.Format(time.RFC3339Nano),
			"payload":          jsonObject(payload),
		},
	})
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sync.ingested_events (
			ingested_event_id, origin_node_id, local_event_id, local_sequence,
			stream_name, event_type, event_level, local_created_at, global_event_id,
			payload_hash, payload_json, metadata
		)
		VALUES ($1, $2, $3, $4,
		        $5, $6, $7, $8, $9,
		        $10, $11, $12)
	`, ids.NewIngestedEventID(), nodeID, item.LocalRef, item.LocalSequence,
		item.StreamName, item.EventType, item.EventLevel, createdAt, event.EventID,
		payloadHash, payload, objectOrDefault(item.Metadata))
	if err != nil {
		return SyncItemResult{}, nil, err
	}
	result, err := insertBatchItemTx(ctx, tx, batchID, nodeID, item, ItemStatusAccepted, event.EventID, "", "", payloadHash)
	return result, nil, err
}

type ingestedEventRef struct {
	GlobalEventID string
	PayloadHash   string
}

func getIngestedEventByLocalRefTx(ctx context.Context, tx *sql.Tx, nodeID, localRef string) (ingestedEventRef, error) {
	var ref ingestedEventRef
	err := tx.QueryRowContext(ctx, `
		SELECT global_event_id, payload_hash
		FROM sync.ingested_events
		WHERE origin_node_id = $1 AND local_event_id = $2
	`, nodeID, localRef).Scan(&ref.GlobalEventID, &ref.PayloadHash)
	return ref, err
}

func insertBatchTx(ctx context.Context, tx *sql.Tx, batchID, nodeID string, input PushBatchInput, cursorBefore json.RawMessage) (SyncBatch, error) {
	return scanSyncBatch(tx.QueryRowContext(ctx, syncBatchSelectSQL(`
		INSERT INTO sync.batches (
			sync_batch_id, origin_node_id, idempotency_key, batch_kind, status,
			item_count, cursor_before_json, metadata
		)
		VALUES ($1, $2, nullif($3, ''), $4, 'received',
		        $5, $6, $7)
		RETURNING
	`), batchID, nodeID, input.IdempotencyKey, input.BatchKind, len(input.Items), objectOrDefault(cursorBefore), objectOrDefault(input.Metadata)))
}

func insertBatchItemTx(ctx context.Context, tx *sql.Tx, batchID, nodeID string, item PushBatchItem, status, globalRef, errorCode, errorMessage, payloadHash string) (SyncItemResult, error) {
	itemPayload := objectOrDefault(item.PayloadJSON)
	itemMetadata := objectOrDefault(item.Metadata)
	itemEnvelope := objectOrDefault(mustJSON(map[string]any{
		"stream_name":    item.StreamName,
		"local_sequence": item.LocalSequence,
		"event_type":     item.EventType,
		"event_level":    item.EventLevel,
		"payload":        jsonObject(itemPayload),
		"item_metadata":  jsonObject(itemMetadata),
	}))
	batchItemID := ids.NewSyncBatchItemID()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sync.batch_items (
			sync_batch_item_id, sync_batch_id, origin_node_id, local_ref,
			item_kind, status, global_ref, error_code, error_message,
			payload_hash, payload_json, metadata
		)
		VALUES ($1, $2, $3, $4,
		        $5, $6, $7, $8, $9,
		        $10, $11, $12)
	`, batchItemID, batchID, nodeID, item.LocalRef,
		item.ItemKind, status, globalRef, errorCode, errorMessage,
		payloadHash, itemEnvelope, itemMetadata)
	if err != nil {
		return SyncItemResult{}, err
	}
	return SyncItemResult{
		SyncBatchItemID: batchItemID,
		LocalRef:        item.LocalRef,
		ItemKind:        item.ItemKind,
		StreamName:      item.StreamName,
		LocalSequence:   item.LocalSequence,
		Status:          status,
		GlobalRef:       globalRef,
		ErrorCode:       errorCode,
		ErrorMessage:    errorMessage,
		PayloadHash:     payloadHash,
		Metadata:        itemMetadata,
	}, nil
}

func insertConflictTx(ctx context.Context, tx *sql.Tx, nodeID, batchID, localRef, conflictType, summary string, localPayload, mainPayload json.RawMessage) (SyncConflict, error) {
	return scanSyncConflict(tx.QueryRowContext(ctx, syncConflictSelectSQL(`
		INSERT INTO sync.conflicts (
			sync_conflict_id, origin_node_id, sync_batch_id, local_ref,
			conflict_type, status, summary, local_payload_json, main_payload_json,
			metadata
		)
		VALUES ($1, $2, nullif($3, ''), $4,
		        $5, 'open', $6, $7, $8,
		        '{}'::jsonb)
		RETURNING
	`), ids.NewSyncConflictID(), nodeID, batchID, localRef,
		conflictType, summary, objectOrDefault(localPayload), objectOrDefault(mainPayload)))
}

func updateBatchCompletionTx(ctx context.Context, tx *sql.Tx, batchID, status string, accepted, conflicts, failed int, cursorAfter json.RawMessage) (SyncBatch, error) {
	return scanSyncBatch(tx.QueryRowContext(ctx, syncBatchSelectSQL(`
		UPDATE sync.batches
		SET status = $2,
		    accepted_count = $3,
		    conflict_count = $4,
		    failed_count = $5,
		    cursor_after_json = $6,
		    completed_at = now()
		WHERE sync_batch_id = $1
		RETURNING
	`), batchID, status, accepted, conflicts, failed, objectOrDefault(cursorAfter)))
}

func (s Service) cursorSnapshotTx(ctx context.Context, tx *sql.Tx, nodeID string) (json.RawMessage, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT stream_name, last_accepted_sequence, last_accepted_local_event_id
		FROM sync.cursors
		WHERE node_id = $1
		ORDER BY stream_name
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var streamName, localEventID string
		var sequence int64
		if err := rows.Scan(&streamName, &sequence, &localEventID); err != nil {
			return nil, err
		}
		out[streamName] = map[string]any{
			"last_accepted_sequence":       sequence,
			"last_accepted_local_event_id": localEventID,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objectOrDefault(mustJSON(out)), nil
}

func (s Service) advanceCursorTx(ctx context.Context, tx *sql.Tx, nodeID, streamName, batchID string) (SyncCursor, error) {
	var current int64
	err := tx.QueryRowContext(ctx, `
		SELECT last_accepted_sequence
		FROM sync.cursors
		WHERE node_id = $1 AND stream_name = $2
	`, nodeID, streamName).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return SyncCursor{}, err
	}
	last := current
	rows, err := tx.QueryContext(ctx, `
		SELECT COALESCE((payload_json->>'local_sequence')::bigint, 0)
		FROM sync.batch_items
		WHERE origin_node_id = $1
		  AND COALESCE(payload_json->>'stream_name', '') = $2
		  AND COALESCE((payload_json->>'local_sequence')::bigint, 0) > $3
		  AND status IN ('accepted', 'duplicate')
		ORDER BY COALESCE((payload_json->>'local_sequence')::bigint, 0) ASC
	`, nodeID, streamName, current)
	if err != nil {
		return SyncCursor{}, err
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			return SyncCursor{}, err
		}
		if seq != last+1 {
			break
		}
		last = seq
	}
	if err := rows.Close(); err != nil {
		return SyncCursor{}, err
	}
	if err := rows.Err(); err != nil {
		return SyncCursor{}, err
	}
	localEventID := ""
	if last > 0 {
		_ = tx.QueryRowContext(ctx, `
			SELECT local_ref
			FROM sync.batch_items
			WHERE origin_node_id = $1
			  AND COALESCE(payload_json->>'stream_name', '') = $2
			  AND COALESCE((payload_json->>'local_sequence')::bigint, 0) = $3
			  AND status IN ('accepted', 'duplicate')
			ORDER BY created_at ASC
			LIMIT 1
		`, nodeID, streamName, last).Scan(&localEventID)
	}
	return scanSyncCursor(tx.QueryRowContext(ctx, syncCursorSelectSQL(`
		INSERT INTO sync.cursors (
			sync_cursor_id, node_id, stream_name, last_accepted_sequence,
			last_accepted_local_event_id, last_batch_id, last_success_at, updated_at
		)
		VALUES ($1, $2, $3, $4,
		        $5, $6, now(), now())
		ON CONFLICT (node_id, stream_name)
		DO UPDATE SET
			last_accepted_sequence = EXCLUDED.last_accepted_sequence,
			last_accepted_local_event_id = EXCLUDED.last_accepted_local_event_id,
			last_batch_id = EXCLUDED.last_batch_id,
			last_success_at = now(),
			updated_at = now()
		RETURNING
	`), ids.NewSyncCursorID(), nodeID, streamName, last, localEventID, batchID))
}

func normalizePushBatchInput(input PushBatchInput) (PushBatchInput, error) {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.BatchKind = strings.TrimSpace(input.BatchKind)
	if input.BatchKind == "" {
		input.BatchKind = BatchKindEvents
	}
	input.Metadata = objectOrDefault(input.Metadata)
	for i := range input.Items {
		item := &input.Items[i]
		item.LocalRef = strings.TrimSpace(item.LocalRef)
		item.ItemKind = strings.TrimSpace(item.ItemKind)
		item.StreamName = strings.TrimSpace(item.StreamName)
		item.EventType = strings.TrimSpace(item.EventType)
		item.EventLevel = strings.TrimSpace(item.EventLevel)
		if item.ItemKind == ItemKindObjectMetadata {
			// Validate the original JSON before generic normalization can erase
			// duplicate fields or turn malformed values into an empty object.
			if _, err := decodeMetadataObservation(*item); err != nil {
				return PushBatchInput{}, fmt.Errorf("items[%d]: %w", i, err)
			}
		}
		item.PayloadJSON = objectOrDefault(item.PayloadJSON)
		item.Metadata = objectOrDefault(item.Metadata)
		if item.LocalRef == "" {
			return PushBatchInput{}, fmt.Errorf("items[%d].local_ref is required", i)
		}
		if item.ItemKind == "" {
			item.ItemKind = ItemKindEvent
		}
		if item.StreamName == "" {
			item.StreamName = StreamEvents
		}
		if item.LocalSequence <= 0 {
			return PushBatchInput{}, fmt.Errorf("items[%d].local_sequence must be positive", i)
		}
		if item.ItemKind == ItemKindEvent {
			if item.EventType == "" {
				return PushBatchInput{}, fmt.Errorf("items[%d].event_type is required", i)
			}
			if item.EventLevel == "" {
				item.EventLevel = "node_activity"
			}
		}
	}
	return input, nil
}

func normalizeSyncedObjectInput(input SyncedObjectInput) (SyncedObjectInput, error) {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.LocalObjectRef = strings.TrimSpace(input.LocalObjectRef)
	input.LocalVersionRef = strings.TrimSpace(input.LocalVersionRef)
	input.ProjectRef = strings.TrimSpace(input.ProjectRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.LogicalName = strings.TrimSpace(input.LogicalName)
	input.SourcePath = strings.TrimSpace(input.SourcePath)
	input.SourceMtimeBasis = strings.TrimSpace(input.SourceMtimeBasis)
	input.SourceCreatedBasis = strings.TrimSpace(input.SourceCreatedBasis)
	if input.SourceMtime != nil {
		value := input.SourceMtime.UTC()
		input.SourceMtime = &value
		if input.SourceMtimeBasis == "" {
			input.SourceMtimeBasis = filesystemmeta.SourceTimeBasisFilesystemMtime
		}
	}
	if input.SourceCreatedAt != nil {
		value := input.SourceCreatedAt.UTC()
		input.SourceCreatedAt = &value
		if input.SourceCreatedBasis == "" {
			input.SourceCreatedBasis = filesystemmeta.SourceTimeBasisFilesystemBirthtime
		}
	}
	input.MimeType = strings.TrimSpace(input.MimeType)
	input.HashURI = strings.ToLower(strings.TrimSpace(input.HashURI))
	input.IndexPolicy = strings.TrimSpace(input.IndexPolicy)
	input.RawBackupPolicy = strings.TrimSpace(input.RawBackupPolicy)
	input.FileClass = strings.TrimSpace(input.FileClass)
	input.ClassificationSource = strings.TrimSpace(input.ClassificationSource)
	input.IndexingState = strings.TrimSpace(input.IndexingState)
	input.IndexingReason = strings.TrimSpace(input.IndexingReason)
	input.ContentBase64 = strings.TrimSpace(input.ContentBase64)
	input.Metadata = objectOrDefault(input.Metadata)
	if input.CredentialToken == "" {
		return SyncedObjectInput{}, fmt.Errorf("credential_token is required")
	}
	if input.LocalObjectRef == "" {
		return SyncedObjectInput{}, fmt.Errorf("local_object_ref is required")
	}
	if input.LocalVersionRef == "" {
		return SyncedObjectInput{}, fmt.Errorf("local_version_ref is required")
	}
	if input.LocalSequence <= 0 {
		return SyncedObjectInput{}, fmt.Errorf("local_sequence must be positive")
	}
	if input.ProjectRef == "" && input.ScopeRef == "" {
		return SyncedObjectInput{}, fmt.Errorf("project_ref or scope_ref is required")
	}
	if input.ProjectRef != "" && input.ScopeRef != "" {
		return SyncedObjectInput{}, fmt.Errorf("provide either project_ref or scope_ref, not both")
	}
	if input.LogicalName == "" {
		input.LogicalName = filepath.Base(input.SourcePath)
	}
	if input.LogicalName == "." || input.LogicalName == "/" || input.LogicalName == "" {
		return SyncedObjectInput{}, fmt.Errorf("logical_name is required")
	}
	if input.SourcePath == "" {
		return SyncedObjectInput{}, fmt.Errorf("source_path is required")
	}
	if input.SizeBytes < 0 || input.SizeBytes > MaxInlineObjectUploadBytes {
		return SyncedObjectInput{}, fmt.Errorf("size_bytes must be between 0 and %d", MaxInlineObjectUploadBytes)
	}
	if !strings.HasPrefix(input.HashURI, "sha256:") || len(strings.TrimPrefix(input.HashURI, "sha256:")) != 64 {
		return SyncedObjectInput{}, fmt.Errorf("hash_uri must be a sha256 URI")
	}
	if input.MimeType == "" {
		input.MimeType = "application/octet-stream"
	}
	if input.IndexPolicy == "" {
		input.IndexPolicy = IndexPolicyTextLater
	}
	if !validIndexPolicy(input.IndexPolicy) {
		return SyncedObjectInput{}, fmt.Errorf("unsupported index_policy: %s", input.IndexPolicy)
	}
	if input.RawBackupPolicy == "" {
		input.RawBackupPolicy = RawBackupPolicyNormal
	}
	if !validRawBackupPolicy(input.RawBackupPolicy) {
		return SyncedObjectInput{}, fmt.Errorf("unsupported raw_backup_policy: %s", input.RawBackupPolicy)
	}
	if input.FileClass == "" || input.ClassificationSource == "" || input.IndexingState == "" {
		classification := storagecatalog.Classify(storagecatalog.ClassificationInput{
			Path:          input.LogicalName,
			MimeType:      input.MimeType,
			SizeBytes:     input.SizeBytes,
			MaxIndexBytes: storagecatalog.DefaultMaxIndexBytes,
		})
		if input.FileClass == "" {
			input.FileClass = classification.FileClass
		}
		if input.ClassificationSource == "" {
			input.ClassificationSource = classification.ClassificationSource
		}
		if input.IndexingState == "" {
			input.IndexingState = classification.IndexingState
		}
		if input.IndexingReason == "" {
			input.IndexingReason = classification.Reason
		}
	}
	if input.ContentBase64 == "" && input.SizeBytes != 0 {
		return SyncedObjectInput{}, fmt.Errorf("content_base64 is required")
	}
	return input, nil
}

func normalizePrivateBackupInput(input PrivateBackupInput) (PrivateBackupInput, error) {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.PayloadBase64 = strings.TrimSpace(input.PayloadBase64)
	input.Metadata = objectOrDefault(input.Metadata)
	if input.CredentialToken == "" {
		return PrivateBackupInput{}, fmt.Errorf("credential_token is required")
	}
	if input.PayloadBase64 == "" {
		return PrivateBackupInput{}, fmt.Errorf("payload_base64 is required")
	}
	if input.CoarseSizeBytes <= 0 || input.CoarseSizeBytes > MaxPrivateBackupBytes {
		return PrivateBackupInput{}, fmt.Errorf("coarse_size_bytes must be between 1 and %d", MaxPrivateBackupBytes)
	}
	return input, nil
}

func normalizeDeletionRequestInput(input DeletionRequestInput) (DeletionRequestInput, error) {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.TargetKind = strings.TrimSpace(input.TargetKind)
	input.TargetRef = strings.TrimSpace(input.TargetRef)
	input.RequestedAction = strings.TrimSpace(input.RequestedAction)
	input.Reason = strings.TrimSpace(input.Reason)
	input.Metadata = objectOrDefault(input.Metadata)
	if input.CredentialToken == "" {
		return DeletionRequestInput{}, fmt.Errorf("credential_token is required")
	}
	if input.TargetKind == "" {
		input.TargetKind = "object"
	}
	switch input.TargetKind {
	case "object", "object_version", "private_backup":
	default:
		return DeletionRequestInput{}, fmt.Errorf("unsupported deletion target_kind: %s", input.TargetKind)
	}
	if input.TargetRef == "" {
		return DeletionRequestInput{}, fmt.Errorf("target_ref is required")
	}
	if input.RequestedAction == "" {
		input.RequestedAction = "tombstone"
	}
	switch input.RequestedAction {
	case "tombstone", "unlink", "purge_requested":
	default:
		return DeletionRequestInput{}, fmt.Errorf("unsupported requested_action: %s", input.RequestedAction)
	}
	if input.Reason == "" {
		input.Reason = "node requested deletion review"
	}
	return input, nil
}

func sanitizedPrivateBackupMetadata(raw json.RawMessage) json.RawMessage {
	source := "loom-node-agent"
	backupKind := "private_raw_folder"
	clientGeneratedAt := ""
	itemCountCoarse := any(nil)
	object := jsonObject(objectOrDefault(raw))
	if text, ok := object["source"].(string); ok && strings.TrimSpace(text) == "loom-node-agent" {
		source = "loom-node-agent"
	}
	if text, ok := object["backup_kind"].(string); ok {
		switch strings.TrimSpace(text) {
		case "private_raw_folder", "watched_root_file":
			backupKind = strings.TrimSpace(text)
		}
	}
	if text, ok := object["client_generated_at"].(string); ok {
		clientGeneratedAt = strings.TrimSpace(text)
	}
	if count, ok := object["item_count_coarse"]; ok {
		itemCountCoarse = count
	}
	out := map[string]any{
		"source":      source,
		"backup_kind": backupKind,
	}
	if clientGeneratedAt != "" {
		out["client_generated_at"] = clientGeneratedAt
	}
	if itemCountCoarse != nil {
		out["item_count_coarse"] = itemCountCoarse
	}
	for _, key := range []string{"root_key", "source_path", "local_batch_id"} {
		if text, ok := object[key].(string); ok && strings.TrimSpace(text) != "" {
			out[key] = strings.TrimSpace(text)
		}
	}
	if backupKind == "watched_root_file" {
		for _, key := range []string{
			"root_key",
			"relative_path",
			"content_hash_uri",
			"backup_mode",
			"local_batch_id",
			"local_artifact_id",
			"local_item_id",
		} {
			if text, ok := object[key].(string); ok && strings.TrimSpace(text) != "" {
				out[key] = strings.TrimSpace(text)
			}
		}
	}
	return objectOrDefault(mustJSON(out))
}

func validIndexPolicy(value string) bool {
	switch value {
	case IndexPolicyNone, IndexPolicyMetadataOnly, IndexPolicyTextLater, IndexPolicySemanticLater, IndexPolicyPrivateNoIndex:
		return true
	default:
		return false
	}
}

func validRawBackupPolicy(value string) bool {
	switch value {
	case RawBackupPolicyNormal, RawBackupPolicyPriority, RawBackupPolicyRateLimited, RawBackupPolicyDelayed, RawBackupPolicyPrivateRawBackup, RawBackupPolicyExcludedTemp:
		return true
	default:
		return false
	}
}

func indexPolicyAllowsText(policy string) bool {
	switch policy {
	case IndexPolicyTextLater, IndexPolicySemanticLater:
		return true
	default:
		return false
	}
}

func syncedObjectPayload(input SyncedObjectInput) json.RawMessage {
	return objectOrDefault(mustJSON(map[string]any{
		"local_object_ref":     input.LocalObjectRef,
		"local_version_ref":    input.LocalVersionRef,
		"local_sequence":       input.LocalSequence,
		"stream_name":          StreamObjectBlobs,
		"logical_name":         input.LogicalName,
		"source_path":          input.SourcePath,
		"source_mtime":         input.SourceMtime,
		"source_mtime_basis":   input.SourceMtimeBasis,
		"source_created_at":    input.SourceCreatedAt,
		"source_created_basis": input.SourceCreatedBasis,
		"size_bytes":           input.SizeBytes,
		"mime_type":            input.MimeType,
		"hash_uri":             input.HashURI,
		"index_policy":         input.IndexPolicy,
		"raw_backup_policy":    input.RawBackupPolicy,
		"file_class":           input.FileClass,
		"classification": map[string]any{
			"source":         input.ClassificationSource,
			"indexing_state": input.IndexingState,
			"reason":         input.IndexingReason,
		},
	}))
}

func objectUploadBatchItem(input SyncedObjectInput) PushBatchItem {
	return PushBatchItem{
		LocalRef:      input.LocalObjectRef,
		ItemKind:      ItemKindObjectBlob,
		StreamName:    StreamObjectBlobs,
		LocalSequence: input.LocalSequence,
		PayloadJSON:   syncedObjectPayload(input),
		Metadata: objectOrDefault(mustJSON(withSyncedSourceTimes(map[string]any{
			"local_version_ref":     input.LocalVersionRef,
			"hash_uri":              input.HashURI,
			"file_class":            input.FileClass,
			"classification_source": input.ClassificationSource,
			"indexing_state":        input.IndexingState,
		}, input))),
	}
}

func syncedObjectItemMetadata(input SyncedObjectInput, values map[string]any) json.RawMessage {
	if values == nil {
		values = map[string]any{}
	}
	values["local_version_ref"] = input.LocalVersionRef
	values["hash_uri"] = input.HashURI
	values["file_class"] = input.FileClass
	values["classification_source"] = input.ClassificationSource
	values["indexing_state"] = input.IndexingState
	if input.IndexingReason != "" {
		values["indexing_reason"] = input.IndexingReason
	}
	if localOutboxID := metadataString(input.Metadata, "local_outbox_id"); localOutboxID != "" {
		values["local_outbox_id"] = localOutboxID
	}
	withSyncedSourceTimes(values, input)
	return objectOrDefault(mustJSON(values))
}

func withSyncedSourceTimes(values map[string]any, input SyncedObjectInput) map[string]any {
	values["source_mtime"] = input.SourceMtime
	values["source_mtime_basis"] = input.SourceMtimeBasis
	values["source_created_at"] = input.SourceCreatedAt
	values["source_created_basis"] = input.SourceCreatedBasis
	return values
}

func (s Service) applySyncedObjectMetadata(ctx context.Context, nodeID string, input SyncedObjectInput, objectID string, versionID string) error {
	textExtractable := input.IndexingState == storagecatalog.IndexingStateIndexed && storagecatalog.IsTextIndexCandidate(input.FileClass)
	_, err := s.DB.ExecContext(ctx, `
		UPDATE objects.objects
		SET metadata = metadata || $2::jsonb,
		    updated_at = now()
		WHERE object_id = $1
	`, objectID, objectOrDefault(mustJSON(withSyncedSourceTimes(map[string]any{
		"slice":                 "12_part_2",
		"sync_source":           "workspace_node",
		"local_object_ref":      input.LocalObjectRef,
		"local_version_ref":     input.LocalVersionRef,
		"file_class":            input.FileClass,
		"classification_source": input.ClassificationSource,
		"indexing_state":        input.IndexingState,
	}, input))))
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		UPDATE objects.object_versions
		SET source_path = $3,
		    synced_at = now(),
		    mime_type = nullif($4, ''),
		    metadata = metadata || $5::jsonb
		WHERE object_version_id = $1 AND object_id = $2
	`, versionID, objectID, input.SourcePath, input.MimeType, objectOrDefault(mustJSON(withSyncedSourceTimes(map[string]any{
		"slice":                 "12_part_2",
		"local_version_ref":     input.LocalVersionRef,
		"file_class":            input.FileClass,
		"classification_source": input.ClassificationSource,
		"indexing_state":        input.IndexingState,
	}, input))))
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		UPDATE files.file_metadata
		SET source_node_id = $2,
		    source_path = $3,
		    source_mtime = $4,
		    mime_type = nullif($5, ''),
		    text_extractable = $6,
		    index_policy = $7,
		    raw_backup_policy = $8,
		    metadata = metadata || $9::jsonb,
		    updated_at = now()
		WHERE object_id = $1
	`, objectID, nodeID, input.SourcePath, input.SourceMtime, input.MimeType, textExtractable, input.IndexPolicy, input.RawBackupPolicy,
		objectOrDefault(mustJSON(withSyncedSourceTimes(map[string]any{
			"slice":                 "12_part_2",
			"local_object_ref":      input.LocalObjectRef,
			"local_version_ref":     input.LocalVersionRef,
			"file_class":            input.FileClass,
			"classification_source": input.ClassificationSource,
			"indexing_state":        input.IndexingState,
			"indexing_reason":       input.IndexingReason,
		}, input))))
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO files.object_locations (
			object_location_id, object_id, version_id, node_id, location_type,
			path_or_uri, is_canonical_location, freshness_state, last_verified_at,
			metadata
		)
		VALUES ($1, $2, $3, $4, 'local_filesystem',
		        $5, false, 'fresh', now(), $6)
	`, ids.NewObjectLocationID(), objectID, versionID, nodeID, input.SourcePath, objectOrDefault(mustJSON(map[string]any{
		"slice":             "12_part_2",
		"local_object_ref":  input.LocalObjectRef,
		"local_version_ref": input.LocalVersionRef,
	})))
	return err
}

func upsertReplicaTx(ctx context.Context, tx *sql.Tx, sourceNodeID, objectID, versionID, storageRef string, input SyncedObjectInput) (SyncReplica, error) {
	replicaMode := "main_backup"
	if input.RawBackupPolicy == RawBackupPolicyPrivateRawBackup {
		replicaMode = "raw_private_backup"
	}
	replicaNodeID, err := resolveMainReplicaNodeIDTx(ctx, tx)
	if err != nil {
		return SyncReplica{}, err
	}
	return scanSyncReplica(tx.QueryRowContext(ctx, syncReplicaSelectSQL(`
		INSERT INTO sync.replicas (
			replica_id, replicated_kind, replicated_id, source_node_id, replica_node_id,
			replica_mode, freshness_state, source_cursor_ref, storage_ref,
			last_verified_at, metadata
		)
		VALUES ($1, 'object_version', $2, $3, $4,
		        $5, 'fresh', $6, $7,
		        now(), $8)
		ON CONFLICT (replicated_kind, replicated_id, source_node_id, replica_node_id, replica_mode)
		DO UPDATE SET
			freshness_state = 'fresh',
			source_cursor_ref = EXCLUDED.source_cursor_ref,
			storage_ref = EXCLUDED.storage_ref,
			last_verified_at = now(),
			updated_at = now(),
			metadata = EXCLUDED.metadata
		RETURNING
	`), ids.NewReplicaID(), versionID, sourceNodeID, replicaNodeID, replicaMode,
		fmt.Sprintf("%s:%d", StreamObjectBlobs, input.LocalSequence),
		storageRef,
		objectOrDefault(mustJSON(map[string]any{
			"object_id":             objectID,
			"local_object_ref":      input.LocalObjectRef,
			"local_version_ref":     input.LocalVersionRef,
			"raw_backup_policy":     input.RawBackupPolicy,
			"index_policy":          input.IndexPolicy,
			"file_class":            input.FileClass,
			"classification_source": input.ClassificationSource,
			"indexing_state":        input.IndexingState,
		}))))
}

func resolveMainReplicaNodeIDTx(ctx context.Context, tx *sql.Tx) (string, error) {
	var nodeID string
	err := tx.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_role = 'main' OR node_id = 'dev-main'
		ORDER BY CASE WHEN node_role = 'main' THEN 0 ELSE 1 END, created_at ASC
		LIMIT 1
	`).Scan(&nodeID)
	return nodeID, err
}

func writeSyncedObjectTemp(content []byte, logicalName string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "loom-sync-object-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	name := filepath.Base(logicalName)
	if name == "." || name == "/" || name == "" {
		name = "object"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func syncBatchSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		sync_batch_id, origin_node_id, idempotency_key, batch_kind, status,
		item_count, accepted_count, conflict_count, failed_count,
		cursor_before_json, cursor_after_json, received_at, completed_at, metadata
	`
	}
	return `
		SELECT
		sync_batch_id, origin_node_id, idempotency_key, batch_kind, status,
		item_count, accepted_count, conflict_count, failed_count,
		cursor_before_json, cursor_after_json, received_at, completed_at, metadata
		FROM sync.batches
	`
}

func scanSyncBatch(scanner interface{ Scan(dest ...any) error }) (SyncBatch, error) {
	var batch SyncBatch
	var idempotencyKey sql.NullString
	var completedAt sql.NullTime
	var cursorBefore, cursorAfter, metadata []byte
	if err := scanner.Scan(
		&batch.SyncBatchID,
		&batch.OriginNodeID,
		&idempotencyKey,
		&batch.BatchKind,
		&batch.Status,
		&batch.ItemCount,
		&batch.AcceptedCount,
		&batch.ConflictCount,
		&batch.FailedCount,
		&cursorBefore,
		&cursorAfter,
		&batch.ReceivedAt,
		&completedAt,
		&metadata,
	); err != nil {
		return SyncBatch{}, err
	}
	batch.IdempotencyKey = stringPtr(idempotencyKey)
	batch.CompletedAt = timePtr(completedAt)
	batch.CursorBefore = jsonOrEmpty(cursorBefore)
	batch.CursorAfter = jsonOrEmpty(cursorAfter)
	batch.Metadata = jsonOrEmpty(metadata)
	return batch, nil
}

func syncCursorSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		sync_cursor_id, node_id, stream_name, last_accepted_sequence,
		last_accepted_local_event_id, last_batch_id, last_success_at,
		last_error_at, last_error_code, last_error_message, metadata, updated_at
	`
	}
	return `
		SELECT
		sync_cursor_id, node_id, stream_name, last_accepted_sequence,
		last_accepted_local_event_id, last_batch_id, last_success_at,
		last_error_at, last_error_code, last_error_message, metadata, updated_at
		FROM sync.cursors
	`
}

func scanSyncCursor(scanner interface{ Scan(dest ...any) error }) (SyncCursor, error) {
	var cursor SyncCursor
	var lastBatchID sql.NullString
	var lastSuccessAt, lastErrorAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&cursor.SyncCursorID,
		&cursor.NodeID,
		&cursor.StreamName,
		&cursor.LastAcceptedSequence,
		&cursor.LastAcceptedLocalEventID,
		&lastBatchID,
		&lastSuccessAt,
		&lastErrorAt,
		&cursor.LastErrorCode,
		&cursor.LastErrorMessage,
		&metadata,
		&cursor.UpdatedAt,
	); err != nil {
		return SyncCursor{}, err
	}
	cursor.LastBatchID = stringPtr(lastBatchID)
	cursor.LastSuccessAt = timePtr(lastSuccessAt)
	cursor.LastErrorAt = timePtr(lastErrorAt)
	cursor.Metadata = jsonOrEmpty(metadata)
	return cursor, nil
}

func syncConflictSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		sync_conflict_id, origin_node_id, sync_batch_id, local_ref,
		conflict_type, status, summary, local_payload_json, main_payload_json,
		metadata, created_at, resolved_at
	`
	}
	return `
		SELECT
		sync_conflict_id, origin_node_id, sync_batch_id, local_ref,
		conflict_type, status, summary, local_payload_json, main_payload_json,
		metadata, created_at, resolved_at
		FROM sync.conflicts
	`
}

func scanSyncConflict(scanner interface{ Scan(dest ...any) error }) (SyncConflict, error) {
	var conflict SyncConflict
	var batchID sql.NullString
	var localPayload, mainPayload, metadata []byte
	var resolvedAt sql.NullTime
	if err := scanner.Scan(
		&conflict.SyncConflictID,
		&conflict.OriginNodeID,
		&batchID,
		&conflict.LocalRef,
		&conflict.ConflictType,
		&conflict.Status,
		&conflict.Summary,
		&localPayload,
		&mainPayload,
		&metadata,
		&conflict.CreatedAt,
		&resolvedAt,
	); err != nil {
		return SyncConflict{}, err
	}
	conflict.SyncBatchID = stringPtr(batchID)
	conflict.LocalPayload = jsonOrEmpty(localPayload)
	conflict.MainPayload = jsonOrEmpty(mainPayload)
	conflict.Metadata = jsonOrEmpty(metadata)
	conflict.ResolvedAt = timePtr(resolvedAt)
	return conflict, nil
}

func syncReplicaSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		replica_id, replicated_kind, replicated_id, source_node_id, replica_node_id,
		replica_mode, freshness_state, source_cursor_ref, storage_ref,
		last_verified_at, metadata, created_at, updated_at
	`
	}
	return `
		SELECT
		replica_id, replicated_kind, replicated_id, source_node_id, replica_node_id,
		replica_mode, freshness_state, source_cursor_ref, storage_ref,
		last_verified_at, metadata, created_at, updated_at
		FROM sync.replicas
	`
}

func scanSyncReplica(scanner interface{ Scan(dest ...any) error }) (SyncReplica, error) {
	var replica SyncReplica
	var lastVerifiedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&replica.ReplicaID,
		&replica.ReplicatedKind,
		&replica.ReplicatedID,
		&replica.SourceNodeID,
		&replica.ReplicaNodeID,
		&replica.ReplicaMode,
		&replica.FreshnessState,
		&replica.SourceCursorRef,
		&replica.StorageRef,
		&lastVerifiedAt,
		&metadata,
		&replica.CreatedAt,
		&replica.UpdatedAt,
	); err != nil {
		return SyncReplica{}, err
	}
	replica.LastVerifiedAt = timePtr(lastVerifiedAt)
	replica.Metadata = jsonOrEmpty(metadata)
	return replica, nil
}

func insertPrivateBackupOperation(ctx context.Context, db *sql.DB, operationID, nodeID, idempotencyKey string, coarseSizeBytes int64, storageRef string, metadata json.RawMessage) (PrivateBackupOperation, error) {
	return scanPrivateBackupOperation(db.QueryRowContext(ctx, privateBackupSelectSQL(`
		INSERT INTO sync.private_backup_operations (
			private_backup_operation_id, origin_node_id, idempotency_key, status,
			coarse_size_bytes, storage_ref, completed_at, metadata
		)
		VALUES ($1, $2, nullif($3, ''), 'stored',
		        $4, $5, now(), $6)
		RETURNING
	`), operationID, nodeID, idempotencyKey, coarseSizeBytes, storageRef, objectOrDefault(metadata)))
}

func (s Service) getPrivateBackupByIdempotency(ctx context.Context, nodeID, idempotencyKey string) (PrivateBackupOperation, error) {
	return scanPrivateBackupOperation(s.DB.QueryRowContext(ctx, privateBackupSelectSQL()+`
		WHERE origin_node_id = $1 AND idempotency_key = $2
		LIMIT 1
	`, nodeID, idempotencyKey))
}

func privateBackupSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		private_backup_operation_id, origin_node_id, idempotency_key, status,
		coarse_size_bytes, storage_ref, started_at, completed_at,
		error_code, error_message, metadata
	`
	}
	return `
		SELECT
		private_backup_operation_id, origin_node_id, idempotency_key, status,
		coarse_size_bytes, storage_ref, started_at, completed_at,
		error_code, error_message, metadata
		FROM sync.private_backup_operations
	`
}

func scanPrivateBackupOperation(scanner interface{ Scan(dest ...any) error }) (PrivateBackupOperation, error) {
	var operation PrivateBackupOperation
	var idempotencyKey sql.NullString
	var completedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&operation.PrivateBackupOperationID,
		&operation.OriginNodeID,
		&idempotencyKey,
		&operation.Status,
		&operation.CoarseSizeBytes,
		&operation.StorageRef,
		&operation.StartedAt,
		&completedAt,
		&operation.ErrorCode,
		&operation.ErrorMessage,
		&metadata,
	); err != nil {
		return PrivateBackupOperation{}, err
	}
	operation.IdempotencyKey = stringPtr(idempotencyKey)
	operation.CompletedAt = timePtr(completedAt)
	operation.Metadata = jsonOrEmpty(metadata)
	return operation, nil
}

func insertDeletionRequest(ctx context.Context, db *sql.DB, deletionRequestID, nodeID string, input DeletionRequestInput, metadata json.RawMessage) (DeletionRequest, error) {
	return scanDeletionRequest(db.QueryRowContext(ctx, deletionRequestSelectSQL(`
		INSERT INTO sync.deletion_requests (
			deletion_request_id, origin_node_id, target_kind, target_ref,
			requested_action, status, reason, metadata
		)
		VALUES ($1, $2, $3, $4,
		        $5, 'pending_review', $6, $7)
		RETURNING
	`), deletionRequestID, nodeID, input.TargetKind, input.TargetRef, input.RequestedAction, input.Reason, objectOrDefault(metadata)))
}

func (s Service) getDeletionRequestByIdempotency(ctx context.Context, nodeID, idempotencyKey string) (DeletionRequest, error) {
	return scanDeletionRequest(s.DB.QueryRowContext(ctx, deletionRequestSelectSQL()+`
		WHERE origin_node_id = $1
		  AND metadata->>'idempotency_key' = $2
		ORDER BY requested_at DESC
		LIMIT 1
	`, nodeID, idempotencyKey))
}

func deletionRequestSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		deletion_request_id, origin_node_id, target_kind, target_ref,
		requested_action, status, reason, requested_at,
		reviewed_at, reviewed_by_actor_id, metadata
	`
	}
	return `
		SELECT
		deletion_request_id, origin_node_id, target_kind, target_ref,
		requested_action, status, reason, requested_at,
		reviewed_at, reviewed_by_actor_id, metadata
		FROM sync.deletion_requests
	`
}

func scanDeletionRequest(scanner interface{ Scan(dest ...any) error }) (DeletionRequest, error) {
	var request DeletionRequest
	var reviewedAt sql.NullTime
	var reviewedByActorID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&request.DeletionRequestID,
		&request.OriginNodeID,
		&request.TargetKind,
		&request.TargetRef,
		&request.RequestedAction,
		&request.Status,
		&request.Reason,
		&request.RequestedAt,
		&reviewedAt,
		&reviewedByActorID,
		&metadata,
	); err != nil {
		return DeletionRequest{}, err
	}
	request.ReviewedAt = timePtr(reviewedAt)
	request.ReviewedByActorID = stringPtr(reviewedByActorID)
	request.Metadata = jsonOrEmpty(metadata)
	return request, nil
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return json.RawMessage(`{}`)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return normalized
}

func jsonObject(raw json.RawMessage) map[string]any {
	object := map[string]any{}
	_ = json.Unmarshal(objectOrDefault(raw), &object)
	if object == nil {
		return map[string]any{}
	}
	return object
}

func metadataString(raw json.RawMessage, key string) string {
	object := jsonObject(raw)
	value, ok := object[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func hashJSON(raw json.RawMessage) string {
	sum := sha256.Sum256(objectOrDefault(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func jsonOrEmpty(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
