package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filepolicy"
	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/storagecatalog"
)

const (
	KnowledgeObjectOriginStorageCatalog = "storage.storage_entries"
	KnowledgeObjectOriginSyncedObject   = "sync.object_replica"
	KnowledgeObjectPipelineMetadata     = "notes-metadata"
	KnowledgeObjectPipelineTextReady    = "notes-text-ready"
	KnowledgeObjectPipelineVersionV08   = "v0.8"
)

type KnowledgeObjectReconcileInput struct {
	DryRun            bool                   `json:"dry_run,omitempty"`
	DiscoverFromStore bool                   `json:"discover_from_store,omitempty"`
	SourceRootID      string                 `json:"source_root_id,omitempty"`
	IncludeDeleted    bool                   `json:"include_deleted,omitempty"`
	SourceRoots       []SourceRoot           `json:"source_roots,omitempty"`
	StorageEntries    []storagecatalog.Entry `json:"storage_entries,omitempty"`
	SyncedObjects     []SyncedObjectEntry    `json:"synced_objects,omitempty"`
}

type KnowledgeObjectReconcileResult struct {
	DryRun           bool                       `json:"dry_run"`
	Candidates       []KnowledgeObjectCandidate `json:"candidates"`
	KnowledgeObjects []KnowledgeObject          `json:"knowledge_objects"`
	PipelineRuns     []PipelineRun              `json:"pipeline_runs,omitempty"`
	Skipped          []KnowledgeObjectSkip      `json:"skipped,omitempty"`
	Applied          int                        `json:"applied"`
}

type KnowledgeObjectCandidate struct {
	Origin    string               `json:"origin"`
	OriginRef string               `json:"origin_ref"`
	Root      SourceRoot           `json:"source_root"`
	Entry     storagecatalog.Entry `json:"storage_entry"`
	Synced    *SyncedObjectEntry   `json:"synced_object,omitempty"`
	Object    KnowledgeObject      `json:"knowledge_object"`
}

type KnowledgeObjectSkip struct {
	Origin         string `json:"origin"`
	OriginRef      string `json:"origin_ref,omitempty"`
	StorageEntryID string `json:"storage_entry_id,omitempty"`
	Reason         string `json:"reason"`
}

type SyncedObjectEntry struct {
	ScopeKey          string          `json:"scope_key,omitempty"`
	NotesSourceRootID string          `json:"notes_source_root_id"`
	BackendRootKey    string          `json:"backend_root_key"`
	ReplicaID         string          `json:"replica_id"`
	StorageRef        string          `json:"storage_ref"`
	ObjectID          string          `json:"object_id"`
	ObjectVersionID   string          `json:"object_version_id"`
	BlobStoragePath   string          `json:"blob_storage_path,omitempty"`
	SourceNodeID      string          `json:"source_node_id,omitempty"`
	SourceNodeKey     string          `json:"source_node_key,omitempty"`
	ProjectID         string          `json:"project_id,omitempty"`
	SourcePath        string          `json:"source_path,omitempty"`
	LogicalName       string          `json:"logical_name,omitempty"`
	FileClass         string          `json:"file_class,omitempty"`
	MimeType          string          `json:"mime_type,omitempty"`
	SizeBytes         *int64          `json:"size_bytes,omitempty"`
	SourceHash        string          `json:"source_hash,omitempty"`
	IndexPolicy       string          `json:"index_policy,omitempty"`
	RawBackupPolicy   string          `json:"raw_backup_policy,omitempty"`
	ObjectMetadata    json.RawMessage `json:"object_metadata,omitempty"`
	VersionMetadata   json.RawMessage `json:"version_metadata,omitempty"`
	FileMetadata      json.RawMessage `json:"file_metadata,omitempty"`
	LastSeenAt        time.Time       `json:"last_seen_at"`
}

func (s *Service) ReconcileKnowledgeObjects(ctx context.Context, input KnowledgeObjectReconcileInput) (KnowledgeObjectReconcileResult, error) {
	if input.DiscoverFromStore {
		if s == nil || s.store.db == nil {
			return KnowledgeObjectReconcileResult{}, fmt.Errorf("knowledge store is not configured")
		}
		roots, err := s.store.ListSourceRoots(ctx, SourceRootFilter{
			NotesSourceRootID: input.SourceRootID,
		})
		if err != nil {
			return KnowledgeObjectReconcileResult{}, err
		}
		entries, err := s.store.ListStorageEntriesForNotesRoots(ctx, input.IncludeDeleted)
		if err != nil {
			return KnowledgeObjectReconcileResult{}, err
		}
		syncedObjects, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, input.SourceRootID)
		if err != nil {
			return KnowledgeObjectReconcileResult{}, err
		}
		input.SourceRoots = roots
		input.StorageEntries = entries
		input.SyncedObjects = syncedObjects
	}

	result := KnowledgeObjectReconcileResult{DryRun: input.DryRun}
	candidates, skipped := s.BuildKnowledgeObjectCandidates(input.SourceRoots, input.StorageEntries, input.SyncedObjects)
	result.Candidates = candidates
	result.Skipped = skipped
	if input.DryRun {
		result.KnowledgeObjects = objectsFromCandidates(candidates)
		return result, nil
	}
	if s == nil || s.store.db == nil {
		return result, fmt.Errorf("knowledge store is not configured")
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return result, err
	}
	for _, candidate := range candidates {
		object, err := s.store.UpsertKnowledgeObject(ctx, candidate.Object)
		if err != nil {
			if errors.Is(err, ErrNotesCustodyPaused) {
				result.Skipped = append(result.Skipped, KnowledgeObjectSkip{Origin: candidate.Origin, OriginRef: candidate.OriginRef, Reason: "source_custody_paused"})
				continue
			}
			return result, err
		}
		result.KnowledgeObjects = append(result.KnowledgeObjects, object)
		result.Applied++
		if object.DeletedAt == nil && object.ProcessingState != ProcessingStateDeleted {
			run, err := s.EnsurePipelineRun(ctx, object, policy.Policy, false, DefaultPipelinePriority)
			if err != nil {
				if errors.Is(err, ErrNotesCustodyPaused) {
					result.Skipped = append(result.Skipped, KnowledgeObjectSkip{Origin: candidate.Origin, OriginRef: candidate.OriginRef, Reason: "source_custody_paused"})
					continue
				}
				return result, err
			}
			result.PipelineRuns = append(result.PipelineRuns, run)
		}
	}
	return result, nil
}

func (s *Service) BuildKnowledgeObjectCandidates(roots []SourceRoot, entries []storagecatalog.Entry, syncedObjectSets ...[]SyncedObjectEntry) ([]KnowledgeObjectCandidate, []KnowledgeObjectSkip) {
	candidates := []KnowledgeObjectCandidate{}
	skipped := []KnowledgeObjectSkip{}
	storageCandidateIndexes := map[storageKnowledgeCandidateKey]int{}
	for _, entry := range entries {
		root, ok, reason := matchSourceRootForEntry(roots, entry)
		if !ok {
			skipped = append(skipped, KnowledgeObjectSkip{
				Origin:         KnowledgeObjectOriginStorageCatalog,
				OriginRef:      storageEntryRef(entry),
				StorageEntryID: entry.StorageEntryID,
				Reason:         reason,
			})
			continue
		}
		relativePath, reason := relativePathFromStorageEntry(entry)
		if reason != "" {
			skipped = append(skipped, KnowledgeObjectSkip{
				Origin:         KnowledgeObjectOriginStorageCatalog,
				OriginRef:      storageEntryRef(entry),
				StorageEntryID: entry.StorageEntryID,
				Reason:         reason,
			})
			continue
		}
		if ignoredNotesKnowledgeRelativePath(relativePath) || expandedSourceExcluded(root, relativePath, entry.Metadata) {
			skipped = append(skipped, KnowledgeObjectSkip{
				Origin:         KnowledgeObjectOriginStorageCatalog,
				OriginRef:      storageEntryRef(entry),
				StorageEntryID: entry.StorageEntryID,
				Reason:         ignoredNotesKnowledgeContractReason,
			})
			continue
		}
		object, err := s.knowledgeObjectFromStorageEntry(root, entry, relativePath)
		if err != nil {
			skipped = append(skipped, KnowledgeObjectSkip{
				Origin:         KnowledgeObjectOriginStorageCatalog,
				OriginRef:      storageEntryRef(entry),
				StorageEntryID: entry.StorageEntryID,
				Reason:         "invalid_knowledge_object: " + err.Error(),
			})
			continue
		}
		candidate := KnowledgeObjectCandidate{
			Origin:    KnowledgeObjectOriginStorageCatalog,
			OriginRef: storageEntryRef(entry),
			Root:      root,
			Entry:     entry,
			Object:    object,
		}
		candidates = appendPreferredStorageKnowledgeCandidate(candidates, storageCandidateIndexes, candidate)
	}
	for _, syncedObjects := range syncedObjectSets {
		for _, synced := range syncedObjects {
			root, ok, reason := matchSourceRootForSyncedObject(roots, synced)
			if !ok {
				skipped = append(skipped, KnowledgeObjectSkip{
					Origin:    KnowledgeObjectOriginSyncedObject,
					OriginRef: syncedObjectRef(synced),
					Reason:    reason,
				})
				continue
			}
			relativePath, reason := relativePathFromSyncedObject(root, synced)
			if reason != "" {
				skipped = append(skipped, KnowledgeObjectSkip{
					Origin:    KnowledgeObjectOriginSyncedObject,
					OriginRef: syncedObjectRef(synced),
					Reason:    reason,
				})
				continue
			}
			if ignoredNotesKnowledgeRelativePath(relativePath) || expandedSourceExcluded(root, relativePath, synced.ObjectMetadata) || expandedSourceExcluded(root, relativePath, synced.VersionMetadata) || expandedSourceExcluded(root, relativePath, synced.FileMetadata) || synced.IndexPolicy == "private_no_index" {
				skipped = append(skipped, KnowledgeObjectSkip{
					Origin:    KnowledgeObjectOriginSyncedObject,
					OriginRef: syncedObjectRef(synced),
					Reason:    ignoredNotesKnowledgeContractReason,
				})
				continue
			}
			object, err := s.knowledgeObjectFromSyncedObject(root, synced, relativePath)
			if err != nil {
				skipped = append(skipped, KnowledgeObjectSkip{
					Origin:    KnowledgeObjectOriginSyncedObject,
					OriginRef: syncedObjectRef(synced),
					Reason:    "invalid_knowledge_object: " + err.Error(),
				})
				continue
			}
			syncedCopy := synced
			candidates = append(candidates, KnowledgeObjectCandidate{
				Origin:    KnowledgeObjectOriginSyncedObject,
				OriginRef: syncedObjectRef(synced),
				Root:      root,
				Synced:    &syncedCopy,
				Object:    object,
			})
		}
	}
	return candidates, skipped
}

func (s *Service) knowledgeObjectFromStorageEntry(root SourceRoot, entry storagecatalog.Entry, relativePath string) (KnowledgeObject, error) {
	now := s.currentTime()
	lastSeenAt := entry.UpdatedAt
	if lastSeenAt.IsZero() {
		lastSeenAt = now
	}
	deletedAt := deletedAtForStorageEntry(entry, now)
	fileClass := strings.TrimSpace(entry.FileClass)
	if fileClass == "" {
		fileClass = storagecatalog.FileClassUnknown
	}
	textReady := textPipelineReady(fileClass, entry)
	sourceHash := sourceHashFromStorageEntry(entry)
	storageEntryID := stringPtrIfNotEmpty(entry.StorageEntryID)
	sourceNodeID := entry.OriginNodeID
	if sourceNodeID == nil {
		sourceNodeID = root.NodeID
	}
	projectID := entry.ProjectID
	if projectID == nil {
		projectID = root.ProjectID
	}
	object := KnowledgeObject{
		NotesSourceRootID: root.NotesSourceRootID,
		StorageEntryID:    storageEntryID,
		SourceNodeID:      sourceNodeID,
		SourceNodeKey:     firstNonEmpty(entry.OriginNodeKey, root.NodeKey),
		ProjectID:         projectID,
		SourcePath:        sourcePathForKnowledgeObject(root, entry, relativePath),
		RelativePath:      relativePath,
		Title:             path.Base(relativePath),
		FileClass:         fileClass,
		MimeType:          strings.TrimSpace(entry.MimeType),
		SizeBytes:         entry.SizeBytes,
		SourceHash:        sourceHash,
		SourceRevision:    sourceRevisionFromStorageEntry(entry, sourceHash),
		ProcessingState:   processingStateFromStorageEntry(entry),
		PipelineKey:       pipelineKeyForStorageEntry(textReady),
		PipelineVersion:   KnowledgeObjectPipelineVersionV08,
		LastSeenAt:        lastSeenAt,
		Metadata:          knowledgeObjectMetadata(root, entry, textReady),
		DeletedAt:         deletedAt,
	}
	return s.PrepareKnowledgeObject(object)
}

func preserveKnowledgeObjectProcessing(existing, next KnowledgeObject) KnowledgeObject {
	if strings.TrimSpace(existing.SourceRevision) != "" && strings.TrimSpace(existing.SourceRevision) == strings.TrimSpace(next.SourceRevision) {
		_, nextSequence, fresh := currentSyncedMetadataObservation(next)
		_, previousSequence, _ := currentSyncedMetadataObservation(existing)
		if !fresh || nextSequence <= previousSequence {
			next.SourceCreatedAt = existing.SourceCreatedAt
			next.SourceModifiedAt = existing.SourceModifiedAt
			next.RecencyAt = existing.RecencyAt
			next.RecencyBasis = existing.RecencyBasis
			next.AbsoluteTimeMetadata = existing.AbsoluteTimeMetadata
		}
	}
	if existing.DeletedAt != nil || next.DeletedAt != nil {
		return next
	}
	if strings.TrimSpace(existing.SourceRevision) == "" || strings.TrimSpace(existing.SourceRevision) != strings.TrimSpace(next.SourceRevision) {
		return next
	}
	if next.ProcessingState != ProcessingStateMetadataOnly {
		return next
	}
	if !shouldPreserveKnowledgeObjectProcessing(existing, next) {
		return next
	}
	next.ProcessingState = existing.ProcessingState
	next.PipelineKey = existing.PipelineKey
	next.PipelineVersion = existing.PipelineVersion
	next.LastProcessedAt = existing.LastProcessedAt
	next.LastErrorCode = existing.LastErrorCode
	next.LastErrorMessage = existing.LastErrorMessage
	next.Metadata = preserveKnowledgeObjectProcessedMetadata(next.Metadata, existing.Metadata)
	return next
}

type storageKnowledgeCandidateKey struct {
	NotesSourceRootID string
	RelativePath      string
}

func appendPreferredStorageKnowledgeCandidate(candidates []KnowledgeObjectCandidate, indexes map[storageKnowledgeCandidateKey]int, candidate KnowledgeObjectCandidate) []KnowledgeObjectCandidate {
	key := storageKnowledgeCandidateKey{
		NotesSourceRootID: strings.TrimSpace(candidate.Object.NotesSourceRootID),
		RelativePath:      strings.TrimSpace(candidate.Object.RelativePath),
	}
	if key.NotesSourceRootID == "" || key.RelativePath == "" {
		return append(candidates, candidate)
	}
	if existingIndex, ok := indexes[key]; ok {
		if preferStorageKnowledgeCandidate(candidate, candidates[existingIndex]) {
			candidates[existingIndex] = candidate
		}
		return candidates
	}
	indexes[key] = len(candidates)
	return append(candidates, candidate)
}

func preferStorageKnowledgeCandidate(next, existing KnowledgeObjectCandidate) bool {
	nextHistorical := historicalStorageAvailability(next.Entry.AvailabilityState)
	existingHistorical := historicalStorageAvailability(existing.Entry.AvailabilityState)
	if nextHistorical != existingHistorical {
		return !nextHistorical
	}
	if !next.Entry.UpdatedAt.Equal(existing.Entry.UpdatedAt) {
		return next.Entry.UpdatedAt.After(existing.Entry.UpdatedAt)
	}
	nextPriority := storageAvailabilitySelectionPriority(next.Entry.AvailabilityState)
	existingPriority := storageAvailabilitySelectionPriority(existing.Entry.AvailabilityState)
	if nextPriority != existingPriority {
		return nextPriority < existingPriority
	}
	return strings.TrimSpace(next.Entry.StorageEntryID) > strings.TrimSpace(existing.Entry.StorageEntryID)
}

func historicalStorageAvailability(value string) bool {
	switch strings.TrimSpace(value) {
	case storagecatalog.AvailabilityStateArchived, storagecatalog.AvailabilityStateSuperseded:
		return true
	default:
		return false
	}
}

func storageAvailabilitySelectionPriority(value string) int {
	switch strings.TrimSpace(value) {
	case storagecatalog.AvailabilityStateDeleted, storagecatalog.AvailabilityStateTombstoned:
		return 0
	case storagecatalog.AvailabilityStateAvailable:
		return 1
	case storagecatalog.AvailabilityStateFailed:
		return 2
	case storagecatalog.AvailabilityStatePending:
		return 3
	case storagecatalog.AvailabilityStateDiscovered:
		return 4
	case storagecatalog.AvailabilityStateArchived, storagecatalog.AvailabilityStateSuperseded:
		return 5
	default:
		return 6
	}
}

func shouldPreserveKnowledgeObjectProcessing(existing, next KnowledgeObject) bool {
	if next.FileClass == storagecatalog.FileClassDirectory {
		return false
	}
	switch existing.ProcessingState {
	case ProcessingStateTextExtracted, ProcessingStateChunked, ProcessingStateIndexed, ProcessingStateEmbedded, ProcessingStateFailed:
		return true
	case ProcessingStateStale:
		return existing.PipelineKey == KnowledgeObjectPipelineNotesFileExtraction || existing.PipelineKey == KnowledgeObjectPipelineMarkdownText
	default:
		return false
	}
}

func preserveKnowledgeObjectProcessedMetadata(next, existing json.RawMessage) json.RawMessage {
	var nextMap map[string]any
	if err := json.Unmarshal(next, &nextMap); err != nil || nextMap == nil {
		return next
	}
	var existingMap map[string]any
	if err := json.Unmarshal(existing, &existingMap); err != nil || existingMap == nil {
		return next
	}
	if value, ok := existingMap["text_pipeline"]; ok {
		nextMap["text_pipeline"] = value
	}
	payload, err := json.Marshal(nextMap)
	if err != nil {
		return next
	}
	return payload
}

func (s *Service) knowledgeObjectFromSyncedObject(root SourceRoot, entry SyncedObjectEntry, relativePath string) (KnowledgeObject, error) {
	now := s.currentTime()
	lastSeenAt := entry.LastSeenAt
	if lastSeenAt.IsZero() {
		lastSeenAt = now
	}
	fileClass := strings.TrimSpace(entry.FileClass)
	if fileClass == "" {
		fileClass = storagecatalog.FileClassUnknown
	}
	textReady := textPipelineReadyForFileClass(fileClass)
	sourceHash := sourceHashFromSyncedObject(entry)
	object := KnowledgeObject{
		NotesSourceRootID: root.NotesSourceRootID,
		SourceNodeID:      stringPtrIfNotEmpty(firstNonEmpty(entry.SourceNodeID, valueOrEmpty(root.NodeID))),
		SourceNodeKey:     firstNonEmpty(entry.SourceNodeKey, root.NodeKey),
		ProjectID:         stringPtrIfNotEmpty(firstNonEmpty(entry.ProjectID, valueOrEmpty(root.ProjectID))),
		SourcePath:        sourcePathForSyncedObject(root, entry, relativePath),
		RelativePath:      relativePath,
		Title:             path.Base(relativePath),
		FileClass:         fileClass,
		MimeType:          strings.TrimSpace(entry.MimeType),
		SizeBytes:         entry.SizeBytes,
		SourceHash:        sourceHash,
		SourceRevision:    sourceRevisionFromSyncedObject(entry, sourceHash),
		ProcessingState:   ProcessingStateMetadataOnly,
		PipelineKey:       pipelineKeyForStorageEntry(textReady),
		PipelineVersion:   KnowledgeObjectPipelineVersionV08,
		LastSeenAt:        lastSeenAt,
		Metadata:          knowledgeObjectSyncedMetadata(root, entry, textReady),
	}
	if mtime, _, ok := currentSyncedMetadataObservation(object); ok {
		resolved, err := resolveCurrentSyncedAbsoluteTime(lastSeenAt, object.Metadata, mtime)
		if err != nil {
			return KnowledgeObject{}, err
		}
		object.SourceCreatedAt, object.SourceModifiedAt = resolved.SourceCreatedAt, resolved.SourceModifiedAt
		object.RecencyAt, object.RecencyBasis = resolved.RecencyAt, resolved.RecencyBasis
		object.AbsoluteTimeMetadata = absoluteTimeMetadataJSON(resolved)
	}
	return s.PrepareKnowledgeObject(object)
}

func currentSyncedMetadataObservation(object KnowledgeObject) (time.Time, int64, bool) {
	var metadata struct {
		Origin string `json:"origin"`
		File   struct {
			Owner    string    `json:"source_metadata_owner"`
			Sequence int64     `json:"source_metadata_sequence"`
			Mtime    time.Time `json:"source_mtime"`
			Basis    string    `json:"source_mtime_basis"`
		} `json:"file_metadata"`
	}
	if json.Unmarshal(object.Metadata, &metadata) != nil || metadata.Origin != KnowledgeObjectOriginSyncedObject ||
		object.SourceNodeID == nil || metadata.File.Owner != *object.SourceNodeID || metadata.File.Owner == "" ||
		metadata.File.Sequence <= 0 || metadata.File.Mtime.IsZero() || metadata.File.Basis != AbsoluteTimeBasisSourceFilesystemMtime {
		return time.Time{}, 0, false
	}
	return metadata.File.Mtime.UTC(), metadata.File.Sequence, true
}

func matchSourceRootForEntry(roots []SourceRoot, entry storagecatalog.Entry) (SourceRoot, bool, string) {
	if entry.SourceArea != storagecatalog.SourceAreaNotes && entry.SourceArea != storagecatalog.SourceAreaProjects && entry.SourceArea != storagecatalog.SourceAreaExternalWatchedRoot {
		return SourceRoot{}, false, "unsupported_source_area"
	}
	if strings.TrimSpace(entry.WatchedRootKey) == "" {
		return SourceRoot{}, false, "missing_watched_root_key"
	}
	if len(roots) == 0 {
		return SourceRoot{}, false, "no_source_roots"
	}
	inactiveMatch := false
	for _, root := range roots {
		if !sourceRootIdentityMatchesEntry(root, entry) {
			continue
		}
		if root.Status != SourceRootStatusActive {
			inactiveMatch = true
			continue
		}
		return root, true, ""
	}
	if inactiveMatch {
		return SourceRoot{}, false, "source_root_inactive"
	}
	return SourceRoot{}, false, "no_matching_source_root"
}

func sourceRootIdentityMatchesEntry(root SourceRoot, entry storagecatalog.Entry) bool {
	if strings.TrimSpace(root.BackendRootKey) == "" || strings.TrimSpace(entry.WatchedRootKey) != strings.TrimSpace(root.BackendRootKey) {
		return false
	}
	if !sourceRootNodeMatchesEntry(root, entry) {
		return false
	}
	switch root.RootKind {
	case RootKindBoxNotes:
		return entry.SourceArea == storagecatalog.SourceAreaNotes
	case RootKindBoxTopics, RootKindBoxLibrary:
		return entry.SourceArea == storagecatalog.SourceAreaExternalWatchedRoot && entry.ProjectID == nil && expandedSourceNodeMatches(root, entry.OriginNodeKey, valueOrEmpty(entry.OriginNodeID))
	case RootKindProjectNotes, RootKindProjectMaterial:
		if entry.SourceArea != storagecatalog.SourceAreaProjects {
			return false
		}
		return stringPtrEquals(root.ProjectID, entry.ProjectID) && (root.RootKind != RootKindProjectMaterial || expandedSourceNodeMatches(root, entry.OriginNodeKey, valueOrEmpty(entry.OriginNodeID)))
	default:
		return false
	}
}

func sourceRootNodeMatchesEntry(root SourceRoot, entry storagecatalog.Entry) bool {
	if strings.TrimSpace(root.NodeKey) != "" && strings.TrimSpace(entry.OriginNodeKey) != "" && strings.TrimSpace(root.NodeKey) != strings.TrimSpace(entry.OriginNodeKey) {
		return false
	}
	if root.NodeID != nil && entry.OriginNodeID != nil && strings.TrimSpace(*root.NodeID) != strings.TrimSpace(*entry.OriginNodeID) {
		return false
	}
	return true
}

func stringPtrEquals(left, right *string) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func matchSourceRootForSyncedObject(roots []SourceRoot, entry SyncedObjectEntry) (SourceRoot, bool, string) {
	if strings.TrimSpace(entry.NotesSourceRootID) == "" && strings.TrimSpace(entry.BackendRootKey) == "" {
		return SourceRoot{}, false, "missing_source_root_identity"
	}
	if len(roots) == 0 {
		return SourceRoot{}, false, "no_source_roots"
	}
	inactiveMatch := false
	for _, root := range roots {
		if !sourceRootIdentityMatchesSyncedObject(root, entry) {
			continue
		}
		if root.Status != SourceRootStatusActive {
			inactiveMatch = true
			continue
		}
		return root, true, ""
	}
	if inactiveMatch {
		return SourceRoot{}, false, "source_root_inactive"
	}
	return SourceRoot{}, false, "no_matching_source_root"
}

func sourceRootIdentityMatchesSyncedObject(root SourceRoot, entry SyncedObjectEntry) bool {
	if root.RootKind == RootKindBoxNotes || root.RootKind == RootKindBoxTopics || root.RootKind == RootKindBoxLibrary {
		boxID := stringMapValue(jsonObject(root.Metadata), "box_id")
		return boxID != "" && root.ProjectID == nil && entry.ProjectID == "" &&
			expandedSourceNodeMatches(root, entry.SourceNodeKey, entry.SourceNodeID) &&
			root.BackendRootKey != "" && root.BackendRootKey == entry.BackendRootKey &&
			root.NotesSourceRootID == entry.NotesSourceRootID &&
			entry.ScopeKey == "loom_box:"+boxID+":"+strings.TrimPrefix(root.RootKind, "box_") &&
			strings.HasPrefix(entry.SourcePath, "watched-root://"+root.BackendRootKey+"/")
	}
	if root.RootKind == RootKindProjectMaterial {
		if !expandedSourceNodeMatches(root, entry.SourceNodeKey, entry.SourceNodeID) || valueOrEmpty(root.ProjectID) == "" || valueOrEmpty(root.ProjectID) != entry.ProjectID || root.BackendRootKey != entry.BackendRootKey {
			return false
		}
	}
	if strings.TrimSpace(entry.NotesSourceRootID) != "" {
		return strings.TrimSpace(root.NotesSourceRootID) == strings.TrimSpace(entry.NotesSourceRootID)
	}
	if root.RootKind != RootKindProjectNotes && root.RootKind != RootKindProjectMaterial {
		return false
	}
	if strings.TrimSpace(root.BackendRootKey) == "" || strings.TrimSpace(root.BackendRootKey) != strings.TrimSpace(entry.BackendRootKey) {
		return false
	}
	if root.ProjectID != nil && strings.TrimSpace(entry.ProjectID) != "" && strings.TrimSpace(*root.ProjectID) != strings.TrimSpace(entry.ProjectID) {
		return false
	}
	return true
}

func expandedSourceNodeMatches(root SourceRoot, key, id string) bool {
	return root.NodeKey != "" && root.NodeKey == key && root.NodeID != nil && *root.NodeID == id
}

func expandedSourceExcluded(root SourceRoot, relativePath string, metadata json.RawMessage) bool {
	if root.RootKind != RootKindBoxNotes && root.RootKind != RootKindBoxTopics && root.RootKind != RootKindBoxLibrary && root.RootKind != RootKindProjectMaterial {
		return false
	}
	if filepolicy.IsIndexingExcludedPath(relativePath) {
		return true
	}
	if registration, ok := jsonObject(root.Metadata)["registration_metadata"].(map[string]any); ok {
		if declaration, ok := registration["knowledge_source"].(map[string]any); ok {
			include, includeOK := expandedSourcePatterns(declaration["include"])
			exclude, excludeOK := expandedSourcePatterns(declaration["exclude"])
			if !includeOK || !excludeOK || len(include) == 0 {
				return true
			}
			matched, _ := rootpolicy.MatchAny(include, relativePath)
			ignored, _ := rootpolicy.MatchAny(exclude, relativePath)
			if !matched || ignored {
				return true
			}
		}
	}
	for _, part := range strings.Split(strings.ToLower(relativePath), "/") {
		if part == ".hermes" || part == ".codex" || part == ".orca" || part == ".repo" || part == "credentials" || part == "private_no_index" || strings.HasPrefix(part, ".env.") {
			return true
		}
	}
	m := jsonObject(metadata)
	return m["private_no_index"] == true || m["index_policy"] == "private_no_index"
}

func expandedSourcePatterns(value any) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	patterns := make([]string, 0, len(values))
	for _, v := range values {
		pattern, ok := v.(string)
		if !ok {
			return nil, false
		}
		patterns = append(patterns, pattern)
	}
	return patterns, rootpolicy.ValidatePatterns(patterns) == nil
}

func relativePathFromStorageEntry(entry storagecatalog.Entry) (string, string) {
	for _, candidate := range []string{entry.OriginalSourcePath, entry.LogicalPath} {
		relativePath, err := storagecatalog.NormalizeLogicalPath(candidate)
		if err == nil {
			return relativePath, ""
		}
	}
	return "", "missing_relative_path"
}

func relativePathFromSyncedObject(root SourceRoot, entry SyncedObjectEntry) (string, string) {
	sourcePath := strings.TrimSpace(entry.SourcePath)
	prefix := "watched-root://" + strings.TrimSpace(root.BackendRootKey) + "/"
	candidates := []string{}
	if strings.HasPrefix(sourcePath, prefix) {
		candidates = append(candidates, strings.TrimPrefix(sourcePath, prefix))
	}
	candidates = append(candidates, entry.LogicalName)
	for _, candidate := range candidates {
		relativePath, err := storagecatalog.NormalizeLogicalPath(candidate)
		if err == nil {
			return relativePath, ""
		}
	}
	return "", "missing_relative_path"
}

func sourcePathForKnowledgeObject(root SourceRoot, entry storagecatalog.Entry, relativePath string) string {
	if strings.TrimSpace(root.SourcePath) != "" {
		return filepath.Join(root.SourcePath, filepath.FromSlash(relativePath))
	}
	return firstNonEmpty(entry.OriginalSourcePath, entry.LogicalPath)
}

func sourcePathForSyncedObject(root SourceRoot, entry SyncedObjectEntry, relativePath string) string {
	if strings.TrimSpace(root.SourcePath) != "" {
		return filepath.Join(root.SourcePath, filepath.FromSlash(relativePath))
	}
	return strings.TrimSpace(entry.SourcePath)
}

func processingStateFromStorageEntry(entry storagecatalog.Entry) string {
	switch strings.TrimSpace(entry.AvailabilityState) {
	case storagecatalog.AvailabilityStateDeleted, storagecatalog.AvailabilityStateTombstoned:
		return ProcessingStateDeleted
	case storagecatalog.AvailabilityStateArchived, storagecatalog.AvailabilityStateSuperseded:
		return ProcessingStateStale
	case storagecatalog.AvailabilityStateFailed:
		return ProcessingStateFailed
	}
	if entry.DeletedAt != nil {
		return ProcessingStateDeleted
	}
	if strings.TrimSpace(entry.ProcessingState) == storagecatalog.ProcessingStateFailed {
		return ProcessingStateFailed
	}
	return ProcessingStateMetadataOnly
}

func deletedAtForStorageEntry(entry storagecatalog.Entry, fallback time.Time) *time.Time {
	if entry.DeletedAt != nil {
		return entry.DeletedAt
	}
	switch strings.TrimSpace(entry.AvailabilityState) {
	case storagecatalog.AvailabilityStateDeleted, storagecatalog.AvailabilityStateTombstoned:
		deletedAt := entry.UpdatedAt
		if deletedAt.IsZero() {
			deletedAt = fallback
		}
		return &deletedAt
	default:
		return nil
	}
}

func textPipelineReady(fileClass string, entry storagecatalog.Entry) bool {
	if processingStateFromStorageEntry(entry) != ProcessingStateMetadataOnly {
		return false
	}
	switch fileClass {
	case storagecatalog.FileClassMarkdown, storagecatalog.FileClassText:
		return true
	default:
		return false
	}
}

func textPipelineReadyForFileClass(fileClass string) bool {
	switch fileClass {
	case storagecatalog.FileClassMarkdown, storagecatalog.FileClassText:
		return true
	default:
		return false
	}
}

func pipelineKeyForStorageEntry(textReady bool) string {
	if textReady {
		return KnowledgeObjectPipelineTextReady
	}
	return KnowledgeObjectPipelineMetadata
}

func sourceHashFromStorageEntry(entry storagecatalog.Entry) string {
	algorithm := strings.ToLower(strings.TrimSpace(entry.ChecksumAlgorithm))
	checksum := strings.ToLower(strings.TrimSpace(entry.ChecksumHex))
	if algorithm != "sha256" || checksum == "" {
		return ""
	}
	sourceHash := "sha256:" + checksum
	if !sha256Pattern.MatchString(sourceHash) {
		return ""
	}
	return sourceHash
}

func sourceRevisionFromStorageEntry(entry storagecatalog.Entry, sourceHash string) string {
	if sourceHash != "" {
		return sourceHash
	}
	if !entry.UpdatedAt.IsZero() {
		return "storage_entry_updated_at:" + entry.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return "storage_entry:" + strings.TrimSpace(entry.StorageEntryID)
}

func sourceHashFromSyncedObject(entry SyncedObjectEntry) string {
	for _, candidate := range []string{entry.SourceHash, entry.StorageRef} {
		sourceHash := strings.ToLower(strings.TrimSpace(candidate))
		if sha256Pattern.MatchString(sourceHash) {
			return sourceHash
		}
	}
	return ""
}

func sourceRevisionFromSyncedObject(entry SyncedObjectEntry, sourceHash string) string {
	if sourceHash != "" {
		return sourceHash
	}
	if strings.TrimSpace(entry.ObjectVersionID) != "" {
		return "object_version:" + strings.TrimSpace(entry.ObjectVersionID)
	}
	return "sync_replica:" + strings.TrimSpace(entry.ReplicaID)
}

func knowledgeObjectMetadata(root SourceRoot, entry storagecatalog.Entry, textReady bool) json.RawMessage {
	mode := "metadata_only"
	if textReady {
		mode = "text_ready"
	}
	metadata := map[string]any{
		"schema_version":      "knowledge.notes_object.v0.8",
		"origin":              KnowledgeObjectOriginStorageCatalog,
		"indexing_mode":       mode,
		"text_pipeline_ready": textReady,
		"source_root": map[string]any{
			"notes_source_root_id": root.NotesSourceRootID,
			"root_kind":            root.RootKind,
			"node_key":             root.NodeKey,
			"backend_root_key":     root.BackendRootKey,
			"source_path":          root.SourcePath,
			"root_relative_path":   root.RootRelativePath,
			"knowledge_source":     sourceDeclaration(root),
		},
		"storage_entry": map[string]any{
			"storage_entry_id":     entry.StorageEntryID,
			"storage_class":        entry.StorageClass,
			"source_area":          entry.SourceArea,
			"origin_node_key":      entry.OriginNodeKey,
			"watched_root_key":     entry.WatchedRootKey,
			"logical_path":         entry.LogicalPath,
			"original_source_path": entry.OriginalSourcePath,
			"current_view_path":    entry.CurrentViewPath,
			"checksum_algorithm":   entry.ChecksumAlgorithm,
			"checksum_hex":         entry.ChecksumHex,
			"processing_state":     entry.ProcessingState,
			"availability_state":   entry.AvailabilityState,
			"retention_state":      entry.RetentionState,
		},
	}
	if root.ProjectID != nil {
		metadata["project_id"] = *root.ProjectID
	}
	if decoded := jsonValue(entry.Metadata); decoded != nil {
		metadata["storage_metadata"] = decoded
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func knowledgeObjectSyncedMetadata(root SourceRoot, entry SyncedObjectEntry, textReady bool) json.RawMessage {
	mode := "metadata_only"
	if textReady {
		mode = "text_ready"
	}
	metadata := map[string]any{
		"schema_version":      "knowledge.notes_object.v0.8",
		"origin":              KnowledgeObjectOriginSyncedObject,
		"indexing_mode":       mode,
		"text_pipeline_ready": textReady,
		"source_root": map[string]any{
			"notes_source_root_id": root.NotesSourceRootID,
			"root_kind":            root.RootKind,
			"node_key":             root.NodeKey,
			"backend_root_key":     root.BackendRootKey,
			"source_path":          root.SourcePath,
			"root_relative_path":   root.RootRelativePath,
			"knowledge_source":     sourceDeclaration(root),
		},
		"synced_object": map[string]any{
			"scope_key":         entry.ScopeKey,
			"replica_id":        entry.ReplicaID,
			"storage_ref":       entry.StorageRef,
			"object_id":         entry.ObjectID,
			"object_version_id": entry.ObjectVersionID,
			"blob_storage_path": entry.BlobStoragePath,
			"source_node_key":   entry.SourceNodeKey,
			"source_path":       entry.SourcePath,
			"logical_name":      entry.LogicalName,
			"index_policy":      entry.IndexPolicy,
			"raw_backup_policy": entry.RawBackupPolicy,
		},
	}
	if root.ProjectID != nil {
		metadata["project_id"] = *root.ProjectID
	}
	if decoded := jsonValue(entry.ObjectMetadata); decoded != nil {
		metadata["object_metadata"] = decoded
	}
	if decoded := jsonValue(entry.VersionMetadata); decoded != nil {
		metadata["version_metadata"] = decoded
	}
	if decoded := jsonValue(entry.FileMetadata); decoded != nil {
		metadata["file_metadata"] = decoded
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func jsonValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

func storageEntryRef(entry storagecatalog.Entry) string {
	return firstNonEmpty(entry.StorageEntryID, entry.OriginalSourcePath, entry.LogicalPath)
}

func syncedObjectRef(entry SyncedObjectEntry) string {
	return firstNonEmpty(entry.ReplicaID, entry.ObjectVersionID, entry.ObjectID, entry.SourcePath, entry.LogicalName)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func objectsFromCandidates(candidates []KnowledgeObjectCandidate) []KnowledgeObject {
	objects := make([]KnowledgeObject, 0, len(candidates))
	for _, candidate := range candidates {
		objects = append(objects, candidate.Object)
	}
	return objects
}
