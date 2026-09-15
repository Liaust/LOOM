package mainstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/storagecatalog"
)

const (
	DefaultStableWindow   = 15 * time.Second
	DefaultMaxFilesPerRun = 200
	DefaultNodeKey        = "main"
)

type Catalog interface {
	RegisterMainDocument(ctx context.Context, input storagecatalog.MainDocumentInput) (storagecatalog.EntryDetail, error)
	ListActiveMainDocuments(ctx context.Context, limit int) ([]storagecatalog.Entry, error)
	InspectEntry(ctx context.Context, ref string) (storagecatalog.EntryDetail, error)
	TombstoneMainDocument(ctx context.Context, input storagecatalog.MainDocumentTombstoneInput) (storagecatalog.Tombstone, error)
}

type filesystemObservationCatalog interface {
	RegisterFilesystemObservation(ctx context.Context, input storagecatalog.RegisterFilesystemObservationInput) (storagecatalog.FilesystemObservation, error)
}

type Service struct {
	state *serviceState
}

type serviceState struct {
	catalog Catalog
	config  Config
	mu      sync.Mutex
	status  Status
}

func NewService(catalog Catalog, config Config) Service {
	normalized := normalizeConfig(config)
	return Service{state: &serviceState{catalog: catalog, config: normalized}}
}

func (s Service) RunOnce(ctx context.Context, override Config) (ImportResult, error) {
	started := time.Now()
	if s.state == nil {
		return ImportResult{}, fmt.Errorf("main storage service is not configured")
	}
	config := s.state.config
	config = mergeConfig(config, override)
	config = normalizeConfig(config)
	if s.state.catalog == nil {
		return ImportResult{}, fmt.Errorf("main storage catalog is not configured")
	}

	now := nowFromConfig(config)
	status := Status{
		BackingRoot:          config.BackingRoot,
		RetentionRoot:        config.RetentionRoot,
		StableWindowSeconds:  int64(config.StableWindow.Seconds()),
		MaxFilesPerRun:       config.MaxFilesPerRun,
		MaxBytesHashedPerRun: config.MaxBytesHashedPerRun,
		MaxRuntimeSeconds:    int64(config.MaxRuntime.Seconds()),
		GeneratedAt:          now,
	}
	if err := os.MkdirAll(config.BackingRoot, 0o770); err != nil {
		status.FilesFailed++
		status.Imports = append(status.Imports, FileStatus{State: StateFailedImport, Error: err.Error()})
		status.Metrics.TotalDurationMS = durationMSSince(started)
		s.storeStatus(status)
		return ImportResult{Status: status}, fmt.Errorf("prepare main documents root: %w", err)
	}
	status.Exists = true

	stageStarted := time.Now()
	scan, err := discoverFiles(config.BackingRoot)
	status.Metrics.DiscoverDurationMS = durationMSSince(stageStarted)
	if err != nil {
		status.FilesFailed++
		status.Imports = append(status.Imports, FileStatus{State: StateFailedImport, Error: err.Error()})
		status.Metrics.TotalDurationMS = durationMSSince(started)
		s.storeStatus(status)
		return ImportResult{Status: status}, err
	}
	files := scan.Files
	status.Imports = append(status.Imports, scan.Ignored...)
	status.Imports = append(status.Imports, scan.Directories...)
	status.FilesDiscovered = int64(len(files))
	status.FilesSkipped = int64(len(scan.Ignored))
	status.DirectoriesObserved = int64(len(scan.Directories))
	status.ObservationsRecorded += s.registerScanObservations(ctx, config, scan.ObservationStatuses(), now)
	stageStarted = time.Now()
	activeEntries, err := s.state.catalog.ListActiveMainDocuments(ctx, 0)
	status.Metrics.ActiveCatalogFetchDurationMS = durationMSSince(stageStarted)
	if err != nil {
		status.FilesFailed++
		status.Imports = append(status.Imports, FileStatus{State: StateFailedImport, Error: err.Error()})
		status.Metrics.TotalDurationMS = durationMSSince(started)
		s.storeStatus(status)
		return ImportResult{Status: status}, err
	}
	activeStates := activeMainDocumentStates(activeEntries)
	processable := make([]discoveredFile, 0, len(files))
	for _, file := range files {
		active, ok := activeStates[file.RelativePath]
		if ok && fileMatchesActiveMainDocument(file, active) {
			status.FilesAlreadyCataloged++
			continue
		}
		processable = append(processable, file)
	}
	files = processable
	files = prioritizeUncatalogedFiles(files, activeEntries)
	if config.MaxFilesPerRun > 0 && len(files) > config.MaxFilesPerRun {
		status.FilesRemaining = int64(len(files) - config.MaxFilesPerRun)
		files = files[:config.MaxFilesPerRun]
	}

	for idx, file := range files {
		if ctx.Err() != nil {
			status.FilesFailed++
			status.Imports = append(status.Imports, FileStatus{
				RelativePath: file.RelativePath,
				State:        StateFailedImport,
				Error:        ctx.Err().Error(),
			})
			break
		}
		itemStatus := FileStatus{
			RelativePath: file.RelativePath,
			State:        StatePendingImport,
			ObjectKind:   filesystemmeta.ObjectKindRegularFile,
			SizeBytes:    file.SizeBytes,
			ModifiedAt:   &file.ModifiedAt,
			Fidelity:     file.Fidelity,
		}
		if config.MaxRuntime > 0 && time.Since(started) >= config.MaxRuntime {
			itemStatus.State = StateBudgetDeferred
			itemStatus.DelayReason = fmt.Sprintf("runtime budget %s exhausted; remaining work deferred", config.MaxRuntime)
			status.RuntimeBudgetExhausted = true
			status.FilesRemaining += int64(len(files) - idx)
			status.Metrics.BudgetStopCount++
			status.Imports = append(status.Imports, itemStatus)
			break
		}
		if config.MaxBytesHashedPerRun > 0 && status.BytesHashed > 0 && status.BytesHashed+file.SizeBytes > config.MaxBytesHashedPerRun {
			itemStatus.State = StateBudgetDeferred
			itemStatus.DelayReason = fmt.Sprintf("byte budget %d exhausted after hashing %d bytes; remaining work deferred", config.MaxBytesHashedPerRun, status.BytesHashed)
			status.ByteBudgetExhausted = true
			status.FilesRemaining += int64(len(files) - idx)
			status.Metrics.BudgetStopCount++
			status.Imports = append(status.Imports, itemStatus)
			break
		}
		age := now.Sub(file.ModifiedAt)
		if age < config.StableWindow {
			itemStatus.State = StateCopyingUnstable
			itemStatus.DelayReason = fmt.Sprintf("modified %s ago; waiting for %s stable window", age.Round(time.Second), config.StableWindow)
			status.FilesDelayed++
			status.Imports = append(status.Imports, itemStatus)
			continue
		}

		itemStatus.State = StateHashing
		stageStarted = time.Now()
		checksum, stable, reason, err := hashStableFile(file)
		status.Metrics.HashDurationMS += durationMSSince(stageStarted)
		status.Metrics.HashOperations++
		if err != nil {
			itemStatus.State = StateFailedImport
			itemStatus.Error = err.Error()
			status.FilesFailed++
			status.Imports = append(status.Imports, itemStatus)
			continue
		}
		if !stable {
			itemStatus.State = StateCopyingUnstable
			itemStatus.DelayReason = reason
			status.FilesDelayed++
			status.Imports = append(status.Imports, itemStatus)
			continue
		}
		itemStatus.ChecksumURI = "sha256:" + checksum
		status.BytesHashed += file.SizeBytes
		itemStatus.State = StateRetentionCopying
		stageStarted = time.Now()
		retention, err := ensureRetentionPayload(ctx, file, config, checksum, now)
		status.Metrics.RetentionCopyDurationMS += durationMSSince(stageStarted)
		status.Metrics.RetentionCopyOperations++
		if err != nil {
			itemStatus.State = StateFailedImport
			itemStatus.Error = err.Error()
			status.FilesFailed++
			status.Imports = append(status.Imports, itemStatus)
			continue
		}
		if stable, reason := fileStillMatchesSnapshot(file); !stable {
			itemStatus.State = StateCopyingUnstable
			itemStatus.DelayReason = reason
			status.FilesDelayed++
			status.Imports = append(status.Imports, itemStatus)
			continue
		}
		itemStatus.RetentionPath = retention.Path

		stageStarted = time.Now()
		detail, err := s.state.catalog.RegisterMainDocument(ctx, storagecatalog.MainDocumentInput{
			NodeKey:               config.NodeKey,
			BackingRoot:           config.BackingRoot,
			RelativePath:          file.RelativePath,
			PhysicalPath:          file.PhysicalPath,
			FileSizeBytes:         file.SizeBytes,
			ChecksumAlgorithm:     "sha256",
			ChecksumValue:         checksum,
			ModifiedAt:            file.ModifiedAt,
			ImportedAt:            now,
			RetentionPath:         retention.Path,
			RetentionVerifiedAt:   retention.VerifiedAt,
			FilesystemObservation: file.Fidelity,
		})
		status.Metrics.RegisterDurationMS += durationMSSince(stageStarted)
		status.Metrics.RegisterOperations++
		if err != nil {
			itemStatus.State = StateFailedImport
			itemStatus.Error = err.Error()
			status.FilesFailed++
			status.Imports = append(status.Imports, itemStatus)
			continue
		}
		itemStatus.State = StateCataloged
		itemStatus.StorageEntryID = detail.Entry.StorageEntryID
		status.FilesAccepted++
		status.ObservationsRecorded += s.registerFileObservation(ctx, config, file, detail.Entry.StorageEntryID, now)
		status.LatestAcceptedAt = &now
		status.Imports = append(status.Imports, itemStatus)
	}

	stageStarted = time.Now()
	reconcile, err := s.reconcile(ctx, ReconcileInput{
		DryRun:    true,
		CreatedBy: "loom.main_documents_import",
		Reason:    "main/Documents source path disappeared during import reconciliation; deferred for manual review",
		Now:       now,
	}, config)
	status.Metrics.ReconcileDurationMS = durationMSSince(stageStarted)
	if err != nil {
		status.FilesFailed++
		status.Imports = append(status.Imports, FileStatus{
			State: StateFailedImport,
			Error: err.Error(),
		})
		status.Metrics.TotalDurationMS = durationMSSince(started)
		s.storeStatus(status)
		return ImportResult{Status: status}, err
	}
	status.FilesMissingCataloged = reconcile.MissingCataloged
	status.FilesTombstoned = reconcile.Tombstoned
	status.FilesFailed += reconcile.Failed
	status.FilesSkipped += reconcile.Skipped
	status.Imports = append(status.Imports, reconcile.Items...)
	status.Metrics.TotalDurationMS = durationMSSince(started)
	s.storeStatus(status)
	return ImportResult{Status: status}, nil
}

func (s Service) Status(ctx context.Context) (Status, error) {
	_ = ctx
	if s.state == nil {
		return Status{}, fmt.Errorf("main storage service is not configured")
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if !s.state.status.GeneratedAt.IsZero() {
		return s.state.status, nil
	}
	config := normalizeConfig(s.state.config)
	status := Status{
		BackingRoot:          config.BackingRoot,
		RetentionRoot:        config.RetentionRoot,
		StableWindowSeconds:  int64(config.StableWindow.Seconds()),
		MaxFilesPerRun:       config.MaxFilesPerRun,
		MaxBytesHashedPerRun: config.MaxBytesHashedPerRun,
		MaxRuntimeSeconds:    int64(config.MaxRuntime.Seconds()),
		GeneratedAt:          nowFromConfig(config),
	}
	if info, err := os.Stat(config.BackingRoot); err == nil && info.IsDir() {
		status.Exists = true
	}
	return status, nil
}

func (s Service) Reconcile(ctx context.Context, input ReconcileInput) (ReconcileResult, error) {
	if s.state == nil {
		return ReconcileResult{}, fmt.Errorf("main storage service is not configured")
	}
	config := normalizeConfig(s.state.config)
	return s.reconcile(ctx, input, config)
}

func (s Service) BackfillRetention(ctx context.Context, input RetentionBackfillInput) (RetentionBackfillResult, error) {
	if s.state == nil {
		return RetentionBackfillResult{}, fmt.Errorf("main storage service is not configured")
	}
	config := normalizeConfig(s.state.config)
	if s.state.catalog == nil {
		return RetentionBackfillResult{}, fmt.Errorf("main storage catalog is not configured")
	}
	now := input.Now
	if now.IsZero() {
		now = nowFromConfig(config)
	} else {
		now = now.UTC()
	}
	result := RetentionBackfillResult{
		BackingRoot:   config.BackingRoot,
		RetentionRoot: config.RetentionRoot,
		DryRun:        input.DryRun || !input.Yes,
		GeneratedAt:   now,
	}
	if err := os.MkdirAll(config.RetentionRoot, 0o750); err != nil {
		result.Failed++
		result.Items = append(result.Items, RetentionBackfillItem{State: StateFailedImport, Error: err.Error()})
		return result, fmt.Errorf("prepare retention root: %w", err)
	}
	entries, err := s.state.catalog.ListActiveMainDocuments(ctx, input.Limit)
	if err != nil {
		result.Failed++
		result.Items = append(result.Items, RetentionBackfillItem{State: StateFailedImport, Error: err.Error()})
		return result, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			result.Failed++
			result.Items = append(result.Items, RetentionBackfillItem{
				RelativePath:   entry.LogicalPath,
				StorageEntryID: entry.StorageEntryID,
				State:          StateFailedImport,
				Error:          err.Error(),
			})
			break
		}
		result.Scanned++
		item := RetentionBackfillItem{
			RelativePath:   filepath.ToSlash(strings.Trim(entry.LogicalPath, "/")),
			StorageEntryID: entry.StorageEntryID,
			State:          "pending",
		}
		detail, err := s.state.catalog.InspectEntry(ctx, entry.StorageEntryID)
		if err != nil {
			item.State = StateFailedImport
			item.Error = err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		if retentionRef := latestBackfillRef(detail.PhysicalRefs, storagecatalog.PhysicalRefKindRetentionPayload); retentionRef != nil {
			item.State = "already_retained"
			item.RetentionPath = retentionRef.URI
			result.AlreadyRetained++
			result.Items = append(result.Items, item)
			continue
		}
		sourceRef := latestBackfillRef(detail.PhysicalRefs, storagecatalog.PhysicalRefKindLocalPath)
		if sourceRef == nil {
			item.State = StateMissingSource
			item.Error = "no available local_path physical ref"
			result.MissingSource++
			result.Items = append(result.Items, item)
			continue
		}
		item.SourcePath = sourceRef.URI
		if strings.TrimSpace(entry.ChecksumHex) == "" || strings.ToLower(strings.TrimSpace(entry.ChecksumAlgorithm)) != "sha256" || entry.SizeBytes == nil {
			item.State = StateFailedImport
			item.Error = "entry is missing sha256 checksum or size"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		info, err := os.Stat(sourceRef.URI)
		if err != nil {
			item.State = StateMissingSource
			item.Error = err.Error()
			result.MissingSource++
			result.Items = append(result.Items, item)
			continue
		}
		file := discoveredFile{
			RelativePath: item.RelativePath,
			PhysicalPath: sourceRef.URI,
			SizeBytes:    info.Size(),
			ModifiedAt:   info.ModTime().UTC(),
		}
		if file.SizeBytes != *entry.SizeBytes {
			item.State = StateFailedImport
			item.Error = fmt.Sprintf("source size = %d, catalog size = %d", file.SizeBytes, *entry.SizeBytes)
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		item.RetentionPath = retentionPayloadPath(config.RetentionRoot, entry.ChecksumHex)
		if result.DryRun {
			item.State = "would_create_retention_payload"
			result.WouldCreate++
			result.Items = append(result.Items, item)
			continue
		}
		retention, err := ensureRetentionPayload(ctx, file, config, entry.ChecksumHex, now)
		if err != nil {
			item.State = StateFailedImport
			item.Error = err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		if stable, reason := fileStillMatchesSnapshot(file); !stable {
			item.State = StatePendingImport
			item.Error = reason
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		item.RetentionPath = retention.Path
		_, err = s.state.catalog.RegisterMainDocument(ctx, storagecatalog.MainDocumentInput{
			StorageEntryID:      entry.StorageEntryID,
			NodeKey:             firstNonEmptyMainStorage(detail.Entry.OriginNodeKey, config.NodeKey),
			BackingRoot:         config.BackingRoot,
			RelativePath:        item.RelativePath,
			PhysicalPath:        sourceRef.URI,
			RetentionPath:       retention.Path,
			FileSizeBytes:       *entry.SizeBytes,
			ChecksumAlgorithm:   "sha256",
			ChecksumValue:       entry.ChecksumHex,
			ModifiedAt:          file.ModifiedAt,
			ImportedAt:          now,
			RetentionVerifiedAt: retention.VerifiedAt,
		})
		if err != nil {
			item.State = StateFailedImport
			item.Error = err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		item.State = "created_retention_payload"
		result.Created++
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (s Service) reconcile(ctx context.Context, input ReconcileInput, config Config) (ReconcileResult, error) {
	if s.state.catalog == nil {
		return ReconcileResult{}, fmt.Errorf("main storage catalog is not configured")
	}
	config = normalizeConfig(config)
	now := input.Now
	if now.IsZero() {
		now = nowFromConfig(config)
	} else {
		now = now.UTC()
	}
	result := ReconcileResult{
		BackingRoot: config.BackingRoot,
		LegacyRoot:  config.LegacyRoot,
		DryRun:      input.DryRun || !input.Yes,
		GeneratedAt: now,
		Migration: DocumentsMigrationPlan{
			Status:        DocumentsMigrationNotRequested,
			LegacyRoot:    config.LegacyRoot,
			CanonicalRoot: config.BackingRoot,
		},
	}
	compareLegacy := input.CompareLegacy || (input.Yes && distinctDocumentsRoots(config.LegacyRoot, config.BackingRoot))
	if compareLegacy {
		plan, err := planDocumentsMigration(ctx, config.LegacyRoot, config.BackingRoot)
		result.Migration = plan
		if err != nil {
			result.Failed++
			return result, err
		}
		if plan.Status == DocumentsMigrationBlocked || plan.LegacyOnly > 0 {
			result.CatalogMutationBlocked = true
			if plan.Status == DocumentsMigrationBlocked {
				result.CatalogMutationBlockReason = "legacy and canonical Documents contain differing or unsupported collisions"
			} else {
				result.CatalogMutationBlockReason = "legacy-only Documents must be moved through the reviewed production migration before catalog tombstones can be applied"
			}
		}
	}
	if result.CatalogMutationBlocked && !result.DryRun {
		return result, nil
	}
	if !result.DryRun {
		if err := os.MkdirAll(config.BackingRoot, 0o770); err != nil {
			result.Failed++
			result.Items = append(result.Items, FileStatus{State: StateFailedImport, Error: err.Error()})
			return result, fmt.Errorf("prepare main documents root: %w", err)
		}
	}
	scan, exists, err := discoverFilesIfExists(config.BackingRoot)
	if err != nil {
		result.Failed++
		result.Items = append(result.Items, FileStatus{State: StateFailedImport, Error: err.Error()})
		return result, err
	}
	_ = exists
	present := map[string]discoveredFile{}
	for _, file := range scan.Files {
		present[file.RelativePath] = file
	}
	result.FilesPresent = int64(len(present))

	entries, err := s.state.catalog.ListActiveMainDocuments(ctx, 0)
	if err != nil {
		result.Failed++
		result.Items = append(result.Items, FileStatus{State: StateFailedImport, Error: err.Error()})
		return result, err
	}
	result.ActiveCataloged = int64(len(entries))
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "main/Documents source path disappeared during reconciliation"
	}
	createdBy := strings.TrimSpace(input.CreatedBy)
	if createdBy == "" {
		createdBy = "loom.mainstorage.reconcile"
	}

	for _, entry := range entries {
		if ctx.Err() != nil {
			result.Failed++
			result.Items = append(result.Items, FileStatus{
				RelativePath: entry.LogicalPath,
				State:        StateFailedImport,
				Error:        ctx.Err().Error(),
			})
			break
		}
		rel := filepath.ToSlash(strings.Trim(entry.LogicalPath, "/"))
		if rel == "" {
			result.Skipped++
			result.Items = append(result.Items, FileStatus{
				RelativePath:   entry.LogicalPath,
				State:          StateIgnored,
				StorageEntryID: entry.StorageEntryID,
				IgnoredReason:  "catalog entry has no logical path",
			})
			continue
		}
		if _, ok := present[rel]; ok {
			continue
		}
		status := FileStatus{
			RelativePath:   rel,
			State:          StateMissingSource,
			StorageEntryID: entry.StorageEntryID,
		}
		if entry.SizeBytes != nil {
			status.SizeBytes = *entry.SizeBytes
		}
		result.MissingCataloged++
		if entry.RetentionState == storagecatalog.RetentionStateSnapshot || entry.RetentionState == storagecatalog.RetentionStateRetained {
			result.Retained++
		}
		if result.DryRun {
			status.State = StateMissingDeferred
			status.DelayReason = "missing source deferred; run main-documents reconcile --yes only after verifying retained bytes and cloud backup"
			result.Items = append(result.Items, status)
			continue
		}
		tombstone, err := s.state.catalog.TombstoneMainDocument(ctx, storagecatalog.MainDocumentTombstoneInput{
			StorageEntryID: entry.StorageEntryID,
			RelativePath:   rel,
			TombstoneKind:  storagecatalog.TombstoneKindSourceDeleted,
			Reason:         reason,
			CreatedBy:      createdBy,
			TombstonedAt:   now,
		})
		if err != nil {
			status.State = StateFailedImport
			status.Error = err.Error()
			result.Failed++
			result.Items = append(result.Items, status)
			continue
		}
		_ = tombstone
		status.State = StateTombstoned
		result.Tombstoned++
		result.Items = append(result.Items, status)
	}
	return result, nil
}

type activeMainDocumentState struct {
	sizeBytes         *int64
	modifiedAt        *time.Time
	checksumAlgorithm string
	checksumHex       string
}

func activeMainDocumentStates(entries []storagecatalog.Entry) map[string]activeMainDocumentState {
	active := make(map[string]activeMainDocumentState, len(entries))
	for _, entry := range entries {
		rel := filepath.ToSlash(strings.Trim(entry.LogicalPath, "/"))
		if rel == "" {
			continue
		}
		active[rel] = activeMainDocumentState{
			sizeBytes:         entry.SizeBytes,
			modifiedAt:        mainDocumentMetadataModifiedAt(entry.Metadata),
			checksumAlgorithm: strings.TrimSpace(entry.ChecksumAlgorithm),
			checksumHex:       strings.TrimSpace(entry.ChecksumHex),
		}
	}
	return active
}

func fileMatchesActiveMainDocument(file discoveredFile, active activeMainDocumentState) bool {
	if active.sizeBytes == nil || *active.sizeBytes != file.SizeBytes {
		return false
	}
	if active.modifiedAt == nil || !active.modifiedAt.UTC().Equal(file.ModifiedAt.UTC()) {
		return false
	}
	return active.checksumAlgorithm != "" && active.checksumHex != ""
}

func mainDocumentMetadataModifiedAt(metadata json.RawMessage) *time.Time {
	if len(metadata) == 0 {
		return nil
	}
	var payload struct {
		ModifiedAt string `json:"modified_at"`
	}
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.ModifiedAt) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, payload.ModifiedAt)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func prioritizeUncatalogedFiles(files []discoveredFile, activeEntries []storagecatalog.Entry) []discoveredFile {
	if len(files) == 0 {
		return files
	}
	active := make(map[string]struct{}, len(activeEntries))
	for _, entry := range activeEntries {
		rel := filepath.ToSlash(strings.Trim(entry.LogicalPath, "/"))
		if rel != "" {
			active[rel] = struct{}{}
		}
	}
	out := append([]discoveredFile(nil), files...)
	sort.SliceStable(out, func(i, j int) bool {
		_, iActive := active[out[i].RelativePath]
		_, jActive := active[out[j].RelativePath]
		if iActive != jActive {
			return !iActive
		}
		if !iActive && !jActive && !out[i].ModifiedAt.Equal(out[j].ModifiedAt) {
			return out[i].ModifiedAt.After(out[j].ModifiedAt)
		}
		return out[i].RelativePath < out[j].RelativePath
	})
	return out
}

func latestBackfillRef(refs []storagecatalog.PhysicalRef, kind string) *storagecatalog.PhysicalRef {
	for i := range refs {
		if refs[i].RefKind != kind {
			continue
		}
		if refs[i].Status == "" || refs[i].Status == storagecatalog.PhysicalRefStatusAvailable {
			return &refs[i]
		}
	}
	return nil
}

func firstNonEmptyMainStorage(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s Service) registerScanObservations(ctx context.Context, config Config, statuses []FileStatus, observedAt time.Time) int64 {
	var recorded int64
	for _, status := range statuses {
		if status.Fidelity == nil {
			continue
		}
		if s.registerObservation(ctx, config.NodeKey, "main_documents", status.RelativePath, "", *status.Fidelity, observedAt) {
			recorded++
		}
	}
	return recorded
}

func (s Service) registerFileObservation(ctx context.Context, config Config, file discoveredFile, storageEntryID string, observedAt time.Time) int64 {
	if file.Fidelity == nil {
		return 0
	}
	if s.registerObservation(ctx, config.NodeKey, "main_documents", file.RelativePath, storageEntryID, *file.Fidelity, observedAt) {
		return 1
	}
	return 0
}

func (s Service) registerObservation(ctx context.Context, nodeKey, sourceRef, logicalPath, storageEntryID string, observation filesystemmeta.Observation, observedAt time.Time) bool {
	catalog, ok := s.state.catalog.(filesystemObservationCatalog)
	if !ok {
		return false
	}
	_, err := catalog.RegisterFilesystemObservation(ctx, storagecatalog.FilesystemObservationInputFromMeta(storagecatalog.FilesystemObservationInputOptions{
		StorageEntryID: storageEntryID,
		SourceArea:     storagecatalog.SourceAreaMainDocuments,
		SourceNodeKey:  nodeKey,
		SourceRef:      sourceRef,
		LogicalPath:    logicalPath,
		ObservedAt:     observedAt,
	}, observation))
	return err == nil
}

func (s Service) storeStatus(status Status) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.status = status
}

func durationMSSince(start time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func distinctDocumentsRoots(legacyRoot, canonicalRoot string) bool {
	legacyRoot = filepath.Clean(strings.TrimSpace(legacyRoot))
	canonicalRoot = filepath.Clean(strings.TrimSpace(canonicalRoot))
	return legacyRoot != "." && legacyRoot != "" && canonicalRoot != "." && canonicalRoot != "" && legacyRoot != canonicalRoot
}

const maxDocumentsMigrationItems = 1000

type documentsMigrationPath struct {
	pathValue string
	typeName  string
	sizeBytes int64
}

func planDocumentsMigration(ctx context.Context, legacyRoot, canonicalRoot string) (DocumentsMigrationPlan, error) {
	legacyRoot = filepath.Clean(strings.TrimSpace(legacyRoot))
	canonicalRoot = filepath.Clean(strings.TrimSpace(canonicalRoot))
	plan := DocumentsMigrationPlan{
		Status:        DocumentsMigrationNotRequested,
		LegacyRoot:    legacyRoot,
		CanonicalRoot: canonicalRoot,
	}
	if legacyRoot == "." || legacyRoot == "" {
		return plan, nil
	}
	if canonicalRoot == "." || canonicalRoot == "" {
		plan.Status = DocumentsMigrationBlocked
		plan.Conflicts = 1
		return plan, fmt.Errorf("canonical main Documents root is required")
	}
	if legacyRoot == canonicalRoot {
		plan.Status = DocumentsMigrationNotNeeded
		return plan, nil
	}
	if pathWithinMainDocuments(legacyRoot, canonicalRoot) || pathWithinMainDocuments(canonicalRoot, legacyRoot) {
		plan.Status = DocumentsMigrationBlocked
		plan.Conflicts = 1
		plan.Items = []DocumentsMigrationItem{{
			State:         DocumentsMigrationConflict,
			LegacyPath:    legacyRoot,
			CanonicalPath: canonicalRoot,
			Reason:        "legacy and canonical Documents roots must not overlap",
		}}
		return plan, fmt.Errorf("legacy and canonical main Documents roots overlap")
	}

	legacy, legacyExists, err := inventoryDocumentsMigrationRoot(ctx, legacyRoot)
	plan.LegacyExists = legacyExists
	if err != nil {
		plan.Status = DocumentsMigrationBlocked
		plan.Conflicts++
		return plan, err
	}
	canonical, canonicalExists, err := inventoryDocumentsMigrationRoot(ctx, canonicalRoot)
	plan.CanonicalExists = canonicalExists
	if err != nil {
		plan.Status = DocumentsMigrationBlocked
		plan.Conflicts++
		return plan, err
	}

	keys := make([]string, 0, len(legacy)+len(canonical))
	seen := make(map[string]struct{}, len(legacy)+len(canonical))
	for key := range legacy {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range canonical {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	buckets := map[string][]DocumentsMigrationItem{}
	appendItem := func(item DocumentsMigrationItem) {
		bucket := buckets[item.State]
		if len(bucket) < maxDocumentsMigrationItems {
			buckets[item.State] = append(bucket, item)
		}
	}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		oldItem, oldOK := legacy[key]
		newItem, newOK := canonical[key]
		item := DocumentsMigrationItem{RelativePath: key}
		if oldOK {
			item.LegacyPath = oldItem.pathValue
			item.LegacyType = oldItem.typeName
			item.LegacySizeBytes = oldItem.sizeBytes
		}
		if newOK {
			item.CanonicalPath = newItem.pathValue
			item.CanonicalType = newItem.typeName
			item.CanonicalSizeBytes = newItem.sizeBytes
		}
		switch {
		case oldOK && !newOK:
			item.State = DocumentsMigrationLegacyOnly
			plan.LegacyOnly++
		case !oldOK && newOK:
			item.State = DocumentsMigrationCanonicalOnly
			plan.CanonicalOnly++
		default:
			item.State, item.Reason = compareDocumentsMigrationPath(ctx, oldItem, newItem, &item)
			if item.State == DocumentsMigrationEquivalent {
				plan.Equivalent++
			} else {
				plan.Conflicts++
			}
		}
		appendItem(item)
	}

	for _, state := range []string{
		DocumentsMigrationConflict,
		DocumentsMigrationLegacyOnly,
		DocumentsMigrationCanonicalOnly,
		DocumentsMigrationEquivalent,
	} {
		remaining := maxDocumentsMigrationItems - len(plan.Items)
		if remaining <= 0 {
			break
		}
		items := buckets[state]
		if len(items) > remaining {
			items = items[:remaining]
		}
		plan.Items = append(plan.Items, items...)
	}
	total := plan.LegacyOnly + plan.CanonicalOnly + plan.Equivalent + plan.Conflicts
	plan.ItemsTruncated = total > int64(len(plan.Items))
	switch {
	case plan.Conflicts > 0:
		plan.Status = DocumentsMigrationBlocked
	case len(legacy) == 0:
		plan.Status = DocumentsMigrationNotNeeded
	default:
		plan.Status = DocumentsMigrationReady
	}
	return plan, nil
}

func inventoryDocumentsMigrationRoot(ctx context.Context, root string) (map[string]documentsMigrationPath, bool, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return map[string]documentsMigrationPath{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect main Documents migration root %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, true, fmt.Errorf("main Documents migration root %s must be a real directory", root)
	}
	items := map[string]documentsMigrationPath{}
	err = filepath.WalkDir(root, func(pathValue string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if pathValue == root {
			return nil
		}
		rel, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		typeName := "unsupported"
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			typeName = "symlink"
		case info.IsDir():
			typeName = "directory"
		case info.Mode().IsRegular():
			typeName = "regular_file"
		}
		items[filepath.ToSlash(rel)] = documentsMigrationPath{
			pathValue: pathValue,
			typeName:  typeName,
			sizeBytes: info.Size(),
		}
		if typeName == "symlink" {
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, true, fmt.Errorf("inventory main Documents migration root %s: %w", root, err)
	}
	return items, true, nil
}

func compareDocumentsMigrationPath(ctx context.Context, legacy, canonical documentsMigrationPath, item *DocumentsMigrationItem) (string, string) {
	if legacy.typeName != canonical.typeName {
		return DocumentsMigrationConflict, "path types differ"
	}
	switch legacy.typeName {
	case "directory":
		return DocumentsMigrationEquivalent, "directories match"
	case "regular_file":
		if legacy.sizeBytes != canonical.sizeBytes {
			return DocumentsMigrationConflict, "file sizes differ"
		}
		legacyHash, err := hashDocumentsMigrationFile(ctx, legacy.pathValue)
		if err != nil {
			return DocumentsMigrationConflict, err.Error()
		}
		canonicalHash, err := hashDocumentsMigrationFile(ctx, canonical.pathValue)
		if err != nil {
			return DocumentsMigrationConflict, err.Error()
		}
		item.LegacySHA256 = legacyHash
		item.CanonicalSHA256 = canonicalHash
		if legacyHash != canonicalHash {
			return DocumentsMigrationConflict, "file checksums differ"
		}
		return DocumentsMigrationEquivalent, "file size and checksum match"
	default:
		return DocumentsMigrationConflict, "symlinks and special filesystem objects require explicit operator review"
	}
}

func hashDocumentsMigrationFile(ctx context.Context, pathValue string) (string, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return "", fmt.Errorf("open migration file %s: %w", pathValue, err)
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("hash migration file %s: %w", pathValue, readErr)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func pathWithinMainDocuments(pathValue, root string) bool {
	rel, err := filepath.Rel(root, pathValue)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type discoveredFile struct {
	RelativePath string
	PhysicalPath string
	SizeBytes    int64
	ModifiedAt   time.Time
	Fidelity     *filesystemmeta.Observation
}

type scanResult struct {
	Files       []discoveredFile
	Directories []FileStatus
	Ignored     []FileStatus
}

func (r scanResult) ObservationStatuses() []FileStatus {
	out := make([]FileStatus, 0, len(r.Directories)+len(r.Ignored))
	out = append(out, r.Directories...)
	out = append(out, r.Ignored...)
	return out
}

func discoverFiles(root string) (scanResult, error) {
	var result scanResult
	if err := filepath.WalkDir(root, func(pathValue string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == root {
			return nil
		}
		info, err := dirEntry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		observation, obsErr := filesystemmeta.DetectPath(pathValue, filesystemmeta.DetectOptions{
			RootPath:          root,
			IncludeXattrNames: true,
		})
		if obsErr != nil {
			return obsErr
		}
		fidelity := observation
		if shouldSkip, reason := shouldSkipRelativePath(rel); shouldSkip {
			modified := info.ModTime().UTC()
			result.Ignored = append(result.Ignored, FileStatus{
				RelativePath:  rel,
				State:         StateIgnored,
				ObjectKind:    fidelity.Kind,
				SizeBytes:     info.Size(),
				ModifiedAt:    &modified,
				IgnoredReason: reason,
				Fidelity:      &fidelity,
			})
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		_ = normalizeWritableMainDocumentsPath(pathValue, info)
		if _, err := storagecatalog.NormalizeLogicalPath(rel); err != nil {
			return nil
		}
		if dirEntry.IsDir() {
			modified := info.ModTime().UTC()
			status := FileStatus{
				RelativePath: rel,
				State:        "observed",
				ObjectKind:   fidelity.Kind,
				ModifiedAt:   &modified,
				Fidelity:     &fidelity,
			}
			if fidelity.IsPackage {
				status.IgnoredReason = "package directory observed as metadata boundary"
				result.Ignored = append(result.Ignored, status)
				return filepath.SkipDir
			}
			if isEmptyDirectory(pathValue) {
				result.Directories = append(result.Directories, status)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			modified := info.ModTime().UTC()
			result.Ignored = append(result.Ignored, FileStatus{
				RelativePath:  rel,
				State:         StateIgnored,
				ObjectKind:    fidelity.Kind,
				SizeBytes:     info.Size(),
				ModifiedAt:    &modified,
				IgnoredReason: "non-regular filesystem object observed as metadata",
				Fidelity:      &fidelity,
			})
			return nil
		}
		result.Files = append(result.Files, discoveredFile{
			RelativePath: rel,
			PhysicalPath: pathValue,
			SizeBytes:    info.Size(),
			ModifiedAt:   info.ModTime().UTC(),
			Fidelity:     &fidelity,
		})
		return nil
	}); err != nil {
		return scanResult{}, fmt.Errorf("scan main documents root: %w", err)
	}
	sort.Slice(result.Files, func(i, j int) bool {
		return result.Files[i].RelativePath < result.Files[j].RelativePath
	})
	sort.Slice(result.Ignored, func(i, j int) bool {
		return result.Ignored[i].RelativePath < result.Ignored[j].RelativePath
	})
	sort.Slice(result.Directories, func(i, j int) bool {
		return result.Directories[i].RelativePath < result.Directories[j].RelativePath
	})
	return result, nil
}

func discoverFilesIfExists(root string) (scanResult, bool, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return scanResult{}, false, nil
	}
	if err != nil {
		return scanResult{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return scanResult{}, true, fmt.Errorf("main documents root %s must be a real directory", root)
	}
	scan, err := discoverFiles(root)
	return scan, true, err
}

func isEmptyDirectory(pathValue string) bool {
	entries, err := os.ReadDir(pathValue)
	return err == nil && len(entries) == 0
}

func normalizeWritableMainDocumentsPath(pathValue string, info os.FileInfo) error {
	mode := info.Mode().Perm()
	if info.IsDir() {
		desired := mode | 0o770
		if desired != mode || info.Mode()&os.ModeSetgid == 0 {
			return chmodMainDocumentsPath(pathValue, desired|0o2000)
		}
		return nil
	}
	if info.Mode().IsRegular() {
		desired := mode | 0o660
		if desired != mode {
			return os.Chmod(pathValue, desired)
		}
	}
	return nil
}

func chmodMainDocumentsPath(pathValue string, mode os.FileMode) error {
	return syscall.Chmod(pathValue, uint32(mode.Perm()|mode&0o7000))
}

func shouldSkipRelativePath(rel string) (bool, string) {
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if segment == "" {
			return true, "empty path segment"
		}
		if shouldSkip, reason := shouldSkipPathSegment(segment); shouldSkip {
			return true, reason
		}
	}
	return false, ""
}

func shouldSkipPathSegment(segment string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(segment))
	switch segment {
	case "":
		return true, "empty path segment"
	case ".loom":
		return true, "LOOM metadata directory"
	case ".DS_Store":
		return true, "macOS Finder metadata"
	case ".Spotlight-V100", ".TemporaryItems", ".Trashes", ".fseventsd", ".DocumentRevisions-V100", ".apdisk", ".AppleDouble", ".LSOverride", ".metadata_never_index", ".com.apple.timemachine.donotpresent", "Icon\r", "Network Trash Folder", "Temporary Items":
		return true, "platform metadata"
	}
	switch {
	case strings.HasSuffix(lower, ".loom-meta.json"):
		return true, "LOOM metadata sidecar"
	case strings.HasPrefix(segment, "._"):
		return true, "AppleDouble sidecar"
	case strings.HasPrefix(segment, "~$") || strings.HasPrefix(segment, ".~lock."):
		return true, "office lock file"
	case strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".download") || strings.HasSuffix(lower, ".crdownload"):
		return true, "temporary partial-download file"
	case strings.HasSuffix(lower, ".icloud"):
		return true, "cloud placeholder sidecar"
	case strings.HasPrefix(lower, ".nfs") || strings.HasPrefix(lower, ".smbdelete"):
		return true, "network filesystem temporary file"
	default:
		return false, ""
	}
}

func hashStableFile(file discoveredFile) (checksum string, stable bool, reason string, err error) {
	if stable, reason := fileStillMatchesSnapshot(file); !stable {
		return "", false, reason, nil
	}
	checksum, err = hashFile(file.PhysicalPath)
	if err != nil {
		return "", false, "", err
	}
	if stable, reason := fileStillMatchesSnapshot(file); !stable {
		return "", false, reason, nil
	}
	return checksum, true, "", nil
}

func fileStillMatchesSnapshot(file discoveredFile) (bool, string) {
	info, err := os.Stat(file.PhysicalPath)
	if err != nil {
		return false, fmt.Sprintf("file is not readable yet: %v", err)
	}
	if !info.Mode().IsRegular() {
		return false, "path is no longer a regular file"
	}
	if info.Size() != file.SizeBytes {
		return false, fmt.Sprintf("size changed from %d to %d during import check", file.SizeBytes, info.Size())
	}
	if !info.ModTime().UTC().Equal(file.ModifiedAt) {
		return false, "mtime changed during import check"
	}
	return true, ""
}

func hashFile(pathValue string) (string, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", pathValue, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", pathValue, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func mergeConfig(base Config, override Config) Config {
	if strings.TrimSpace(override.BackingRoot) != "" {
		base.BackingRoot = override.BackingRoot
	}
	if strings.TrimSpace(override.LegacyRoot) != "" {
		base.LegacyRoot = override.LegacyRoot
	}
	if strings.TrimSpace(override.RetentionRoot) != "" {
		base.RetentionRoot = override.RetentionRoot
	}
	if strings.TrimSpace(override.NodeKey) != "" {
		base.NodeKey = override.NodeKey
	}
	if override.StableWindow > 0 {
		base.StableWindow = override.StableWindow
	}
	if override.MaxFilesPerRun > 0 {
		base.MaxFilesPerRun = override.MaxFilesPerRun
	}
	if override.MaxBytesHashedPerRun > 0 {
		base.MaxBytesHashedPerRun = override.MaxBytesHashedPerRun
	}
	if override.MaxRuntime > 0 {
		base.MaxRuntime = override.MaxRuntime
	}
	if override.Now != nil {
		base.Now = override.Now
	}
	return base
}

func normalizeConfig(config Config) Config {
	config.BackingRoot = strings.TrimSpace(config.BackingRoot)
	config.LegacyRoot = strings.TrimSpace(config.LegacyRoot)
	config.RetentionRoot = strings.TrimSpace(config.RetentionRoot)
	if config.RetentionRoot == "" {
		if config.BackingRoot != "" {
			config.RetentionRoot = filepath.Join(filepath.Dir(config.BackingRoot), "storage-retention")
		} else {
			config.RetentionRoot = DefaultRetentionRoot
		}
	}
	if config.NodeKey = strings.TrimSpace(config.NodeKey); config.NodeKey == "" {
		config.NodeKey = DefaultNodeKey
	}
	if config.StableWindow <= 0 {
		config.StableWindow = DefaultStableWindow
	}
	if config.MaxFilesPerRun <= 0 {
		config.MaxFilesPerRun = DefaultMaxFilesPerRun
	}
	if config.MaxBytesHashedPerRun < 0 {
		config.MaxBytesHashedPerRun = 0
	}
	if config.MaxRuntime < 0 {
		config.MaxRuntime = 0
	}
	return config
}

func nowFromConfig(config Config) time.Time {
	if config.Now != nil {
		return config.Now().UTC()
	}
	return time.Now().UTC()
}
