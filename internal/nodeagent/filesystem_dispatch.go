package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/routing"
	loomsync "loom.local/loom/internal/sync"
)

type filesystemIngestDispatchInput struct {
	Root        string `json:"root"`
	Path        string `json:"path"`
	ProjectRef  string `json:"project,omitempty"`
	ScopeRef    string `json:"scope,omitempty"`
	Name        string `json:"name,omitempty"`
	IndexPolicy string `json:"index_policy,omitempty"`
}

type filesystemDispatchResult struct {
	ResultJSON     json.RawMessage
	ResultRefsJSON json.RawMessage
}

func executeFilesystemDispatch(ctx context.Context, config Config, state State, store Store, dispatch routing.RemoteDispatchPayload) (filesystemDispatchResult, error) {
	switch filesystemEndpointName(dispatch) {
	case filesystemconnector.EndpointSafeList:
		var input filesystemconnector.SafeListInput
		if err := json.Unmarshal(dispatch.Input, &input); err != nil {
			return filesystemDispatchResult{}, filesystemExecutionError("filesystem.invalid_input", "safe_list input must be a JSON object")
		}
		result, err := filesystemconnector.SafeList(config.Filesystem, input)
		if err != nil {
			return filesystemDispatchResult{}, err
		}
		return filesystemDispatchResult{
			ResultJSON:     objectJSONFromAny(result),
			ResultRefsJSON: json.RawMessage(`{}`),
		}, nil
	case filesystemconnector.EndpointReadMetadata:
		var input filesystemconnector.ReadMetadataInput
		if err := json.Unmarshal(dispatch.Input, &input); err != nil {
			return filesystemDispatchResult{}, filesystemExecutionError("filesystem.invalid_input", "read_metadata input must be a JSON object")
		}
		result, err := filesystemconnector.ReadMetadata(config.Filesystem, input)
		if err != nil {
			return filesystemDispatchResult{}, err
		}
		return filesystemDispatchResult{
			ResultJSON:     objectJSONFromAny(result),
			ResultRefsJSON: json.RawMessage(`{}`),
		}, nil
	case filesystemconnector.EndpointIngestFile:
		result, err := executeFilesystemIngest(ctx, config, state, store, dispatch)
		if err != nil {
			return filesystemDispatchResult{}, err
		}
		return result, nil
	default:
		return filesystemDispatchResult{}, filesystemExecutionError("filesystem.unsupported_operation", "filesystem connector does not support this operation")
	}
}

func executeFilesystemIngest(ctx context.Context, config Config, state State, store Store, dispatch routing.RemoteDispatchPayload) (filesystemDispatchResult, error) {
	store, unlock, err := store.lockLocalSync(ctx)
	if err != nil {
		return filesystemDispatchResult{}, err
	}
	defer unlock()
	var input filesystemIngestDispatchInput
	if err := json.Unmarshal(dispatch.Input, &input); err != nil {
		return filesystemDispatchResult{}, filesystemExecutionError("filesystem.invalid_input", "ingest_file input must be a JSON object")
	}
	prepared, err := filesystemconnector.PrepareIngest(config.Filesystem, filesystemconnector.IngestFileInput{
		Root: input.Root,
		Path: input.Path,
	})
	if err != nil {
		return filesystemDispatchResult{}, err
	}
	if err := filesystemconnector.ValidatePreparedIngestForInlineUpload(prepared, loomsync.MaxInlineObjectUploadBytes); err != nil {
		return filesystemDispatchResult{}, err
	}

	projectRef := strings.TrimSpace(input.ProjectRef)
	scopeRef := strings.TrimSpace(input.ScopeRef)
	if projectRef == "" && scopeRef == "" {
		scopeRef = strings.TrimSpace(dispatch.ScopeID)
	}
	if projectRef != "" && scopeRef != "" {
		return filesystemDispatchResult{}, filesystemExecutionError("filesystem.invalid_destination", "provide either project or scope, not both")
	}
	if projectRef == "" && scopeRef == "" {
		return filesystemDispatchResult{}, filesystemExecutionError("filesystem.invalid_destination", "ingest_file requires a project or scope destination")
	}

	logicalName := safeLogicalName(input.Name)
	if logicalName == "" {
		logicalName = prepared.LogicalName
	}
	indexPolicy, err := normalizeFilesystemIndexPolicy(input.IndexPolicy)
	if err != nil {
		return filesystemDispatchResult{}, err
	}
	if err := store.EnsureSyncDataDirs(); err != nil {
		return filesystemDispatchResult{}, err
	}
	sequence, err := store.nextLocalSequence(loomsync.StreamObjectBlobs)
	if err != nil {
		return filesystemDispatchResult{}, err
	}

	localObjectID := ids.NewObjectID()
	localVersionID := ids.NewObjectVersionID()
	syncedInput := loomsync.SyncedObjectInput{
		NodeRef:            state.NodeID,
		CredentialToken:    state.CredentialToken,
		LocalObjectRef:     localObjectID,
		LocalVersionRef:    localVersionID,
		LocalSequence:      sequence,
		ProjectRef:         projectRef,
		ScopeRef:           scopeRef,
		LogicalName:        logicalName,
		SourcePath:         prepared.LogicalPath,
		SourceMtime:        &prepared.SourceMtime,
		SourceMtimeBasis:   prepared.SourceMtimeBasis,
		SourceCreatedAt:    prepared.SourceCreatedAt,
		SourceCreatedBasis: prepared.SourceCreatedBasis,
		SizeBytes:          prepared.SizeBytes,
		MimeType:           prepared.MimeType,
		HashURI:            prepared.HashURI,
		IndexPolicy:        indexPolicy,
		RawBackupPolicy:    loomsync.RawBackupPolicyNormal,
		ContentBase64:      base64.StdEncoding.EncodeToString(prepared.Content),
		Metadata: objectJSON(map[string]any{
			"source":             "filesystem-connector",
			"node_key":           config.NodeKey,
			"connector":          filesystemconnector.ProviderKey,
			"root":               prepared.RootKey,
			"relative_path":      prepared.RelativePath,
			"logical_source":     prepared.LogicalPath,
			"capability_call_id": dispatch.CapabilityCallID,
			"route_id":           dispatch.RouteID,
			"slice":              "13_part_2",
		}),
	}
	syncedInput.IdempotencyKey = filesystemIngestIdempotencyKey(state.NodeID, dispatch.CapabilityCallID, prepared.RootKey, prepared.RelativePath, prepared.HashURI)

	client, err := NewClient(config.MainURL)
	if err != nil {
		return filesystemDispatchResult{}, err
	}
	uploadEnvelope, err := client.UploadSyncedObject(ctx, filesystemDispatchCorrelationID(dispatch), syncedInput.IdempotencyKey, syncedInput)
	if err != nil {
		return filesystemDispatchResult{}, err
	}
	if err := recordFilesystemObjectUpload(store, config, state, prepared, syncedInput, uploadEnvelope.Data); err != nil {
		return filesystemDispatchResult{}, err
	}

	result := map[string]any{
		"root":              prepared.RootKey,
		"path":              prepared.RelativePath,
		"logical_source":    prepared.LogicalPath,
		"logical_name":      logicalName,
		"size_bytes":        prepared.SizeBytes,
		"mime_type":         prepared.MimeType,
		"hash_uri":          uploadEnvelope.Data.HashURI,
		"object_id":         uploadEnvelope.Data.ObjectID,
		"object_version_id": uploadEnvelope.Data.VersionID,
		"blob_id":           uploadEnvelope.Data.BlobID,
		"index_status":      uploadEnvelope.Data.IndexStatus,
		"sync_batch_id":     uploadEnvelope.Data.Batch.SyncBatchID,
		"sync_item_id":      uploadEnvelope.Data.Item.SyncBatchItemID,
		"replica_id":        replicaID(uploadEnvelope.Data),
	}
	resultRefs := map[string]any{
		"object_id":         uploadEnvelope.Data.ObjectID,
		"object_version_id": uploadEnvelope.Data.VersionID,
		"blob_id":           uploadEnvelope.Data.BlobID,
		"hash_uri":          uploadEnvelope.Data.HashURI,
		"sync_batch_id":     uploadEnvelope.Data.Batch.SyncBatchID,
		"sync_item_id":      uploadEnvelope.Data.Item.SyncBatchItemID,
		"replica_id":        replicaID(uploadEnvelope.Data),
	}
	return filesystemDispatchResult{
		ResultJSON:     objectJSONFromAny(result),
		ResultRefsJSON: objectJSONFromAny(resultRefs),
	}, nil
}

func recordFilesystemObjectUpload(store Store, config Config, state State, prepared filesystemconnector.PreparedIngest, input loomsync.SyncedObjectInput, result loomsync.SyncedObjectResult) error {
	store, unlock, err := store.lockLocalSync(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	now := time.Now().UTC()
	metadata := objectJSON(map[string]any{
		"source":         "filesystem-connector",
		"node_key":       config.NodeKey,
		"connector":      filesystemconnector.ProviderKey,
		"root":           prepared.RootKey,
		"relative_path":  prepared.RelativePath,
		"logical_source": prepared.LogicalPath,
		"hash_uri":       input.HashURI,
		"slice":          "13_part_2",
	})
	object := LocalSyncObject{
		LocalObjectID:      input.LocalObjectRef,
		LocalVersionID:     input.LocalVersionRef,
		LocalSequence:      input.LocalSequence,
		ProjectRef:         input.ProjectRef,
		ScopeRef:           input.ScopeRef,
		LogicalName:        input.LogicalName,
		SourcePath:         input.SourcePath,
		SourceMtime:        prepared.SourceMtime,
		SourceMtimeBasis:   prepared.SourceMtimeBasis,
		SourceCreatedAt:    prepared.SourceCreatedAt,
		SourceCreatedBasis: prepared.SourceCreatedBasis,
		SizeBytes:          input.SizeBytes,
		MimeType:           input.MimeType,
		HashURI:            input.HashURI,
		IndexPolicy:        input.IndexPolicy,
		RawBackupPolicy:    input.RawBackupPolicy,
		CreatedAt:          now,
		Metadata:           metadata,
		SyncStatus:         result.Item.Status,
		MainObjectID:       result.ObjectID,
		MainVersionID:      result.VersionID,
		MainBlobID:         result.BlobID,
		SyncedAt:           &now,
	}
	objects, err := store.LoadSyncObjects()
	if err != nil {
		return err
	}
	objects = append(objects, object)
	if err := store.SaveSyncObjects(objects); err != nil {
		return err
	}

	payload := objectJSON(map[string]any{
		"local_object_ref":  input.LocalObjectRef,
		"local_version_ref": input.LocalVersionRef,
		"local_sequence":    input.LocalSequence,
		"stream_name":       loomsync.StreamObjectBlobs,
		"logical_name":      input.LogicalName,
		"source_path":       input.SourcePath,
		"size_bytes":        input.SizeBytes,
		"mime_type":         input.MimeType,
		"hash_uri":          input.HashURI,
		"index_policy":      input.IndexPolicy,
		"raw_backup_policy": input.RawBackupPolicy,
	})
	outboxItem := LocalSyncOutboxItem{
		LocalOutboxID: ids.NewLocalOutboxID(),
		LocalRef:      input.LocalObjectRef,
		ItemKind:      loomsync.ItemKindObjectBlob,
		StreamName:    loomsync.StreamObjectBlobs,
		LocalSequence: input.LocalSequence,
		PayloadHash:   rawJSONHash(payload),
		Status:        result.Item.Status,
		CreatedAt:     now,
		LastAttemptAt: &now,
		SyncedAt:      &now,
		GlobalRef:     result.ObjectID,
		PayloadJSON:   payload,
	}
	if result.Item.Status == loomsync.ItemStatusFailed || result.Item.Status == loomsync.ItemStatusConflicted {
		outboxItem.LastErrorCode = result.Item.ErrorCode
		outboxItem.LastErrorMessage = result.Item.ErrorMessage
	}
	outbox, err := store.LoadSyncOutbox()
	if err != nil {
		return err
	}
	outbox = append(outbox, outboxItem)
	if err := store.SaveSyncOutbox(outbox); err != nil {
		return err
	}
	if err := store.updateLocalQueuedCursor(loomsync.StreamObjectBlobs, input.LocalSequence, now); err != nil {
		return err
	}
	if strings.TrimSpace(result.Cursor.NodeID) != "" {
		if err := store.applyRemoteCursor(result.Cursor, now); err != nil {
			return err
		}
	}
	_ = state
	return nil
}

func filesystemEndpointName(dispatch routing.RemoteDispatchPayload) string {
	for _, value := range []string{dispatch.CapabilityAddress, dispatch.Operation} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if strings.HasSuffix(value, "@filesystem."+filesystemconnector.EndpointSafeList) {
			return filesystemconnector.EndpointSafeList
		}
		if strings.HasSuffix(value, "@filesystem."+filesystemconnector.EndpointReadMetadata) {
			return filesystemconnector.EndpointReadMetadata
		}
		if strings.HasSuffix(value, "@filesystem."+filesystemconnector.EndpointIngestFile) {
			return filesystemconnector.EndpointIngestFile
		}
	}
	return ""
}

func isFilesystemDispatch(dispatch routing.RemoteDispatchPayload) bool {
	return filesystemEndpointName(dispatch) != "" ||
		strings.Contains(strings.ToLower(dispatch.ProviderAddress), "@filesystem")
}

func normalizeFilesystemIndexPolicy(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default":
		return loomsync.IndexPolicyTextLater, nil
	case "skip_text":
		return loomsync.IndexPolicyMetadataOnly, nil
	case "skip_all":
		return loomsync.IndexPolicyNone, nil
	case loomsync.IndexPolicyNone, loomsync.IndexPolicyMetadataOnly, loomsync.IndexPolicyTextLater, loomsync.IndexPolicySemanticLater, loomsync.IndexPolicyPrivateNoIndex:
		return strings.TrimSpace(value), nil
	default:
		return "", filesystemExecutionError("filesystem.invalid_index_policy", "unsupported index_policy")
	}
}

func filesystemIngestIdempotencyKey(nodeID, capabilityCallID, rootKey, relativePath, hashURI string) string {
	raw, _ := json.Marshal(map[string]any{
		"node_id":            strings.TrimSpace(nodeID),
		"capability_call_id": strings.TrimSpace(capabilityCallID),
		"root":               strings.TrimSpace(rootKey),
		"relative_path":      strings.TrimSpace(relativePath),
		"hash_uri":           strings.TrimSpace(hashURI),
	})
	sum := sha256.Sum256(raw)
	return "node-agent.filesystem.ingest." + strings.TrimSpace(nodeID) + "." + hex.EncodeToString(sum[:])[:24]
}

func filesystemDispatchCorrelationID(dispatch routing.RemoteDispatchPayload) string {
	if strings.TrimSpace(dispatch.CapabilityCallID) != "" {
		return "filesystem-" + dispatch.CapabilityCallID
	}
	return "filesystem-connector"
}

func replicaID(result loomsync.SyncedObjectResult) string {
	if result.Replica == nil {
		return ""
	}
	return result.Replica.ReplicaID
}

func objectJSONFromAny(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func filesystemExecutionError(code, message string) error {
	return &filesystemconnector.PolicyError{Code: code, Message: message}
}

func filesystemDispatchErrorCode(err error) string {
	return filesystemconnector.PolicyErrorCode(err)
}

func filesystemDispatchErrorMessage(err error) string {
	return filesystemconnector.PolicyErrorMessage(err)
}

func safeLogicalName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return filepath.Base(name)
}
