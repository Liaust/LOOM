package storagearchive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
)

type Catalog interface {
	ListEntries(ctx context.Context, filter storagecatalog.ListFilter) ([]storagecatalog.Entry, error)
	InspectEntry(ctx context.Context, ref string) (storagecatalog.EntryDetail, error)
	CommitArchive(ctx context.Context, input storagecatalog.ArchiveCommitInput) (storagecatalog.ArchiveCommitResult, error)
}

type archiveContentRefFinder interface {
	FindAvailablePhysicalRefsByContentAddresses(ctx context.Context, refKind string, contentAddresses []string) (map[string]storagecatalog.PhysicalRef, error)
}

type archiveDetailCatalog interface {
	ListEntryDetails(ctx context.Context, storageEntryIDs []string) (map[string]storagecatalog.EntryDetail, error)
}

type SafeToDeleteChecker interface {
	SafeToDelete(ctx context.Context, input storageretention.SafeToDeleteInput) (storageretention.SafeToDeleteResult, error)
}

type Service struct {
	Catalog              Catalog
	SafeToDelete         SafeToDeleteChecker
	ArchiveRoot          string
	OwnerNodeKey         string
	Now                  func() time.Time
	MaxEntriesPerArchive int
	writeManifestHook    func(archiveKey string, payload []byte) error
}

type ArchiveInput struct {
	SourceRef            string `json:"source_ref"`
	TargetPath           string `json:"target_path"`
	ArchiveKey           string `json:"archive_key,omitempty"`
	ArchiveKind          string `json:"archive_kind,omitempty"`
	OwnerNodeKey         string `json:"owner_node_key,omitempty"`
	MarkSourceArchived   bool   `json:"mark_source_archived,omitempty"`
	MarkSourceSuperseded bool   `json:"mark_source_superseded,omitempty"`
	DryRun               bool   `json:"dry_run,omitempty"`
}

type ArchiveResult struct {
	ArchiveManifest storagecatalog.ArchiveManifest        `json:"archive_manifest"`
	Manifest        ManifestDocument                      `json:"manifest"`
	ManifestPath    string                                `json:"manifest_path"`
	ArchiveRoot     string                                `json:"archive_root"`
	DryRun          bool                                  `json:"dry_run"`
	Entries         []ArchivedEntry                       `json:"entries"`
	SafeToDelete    []storageretention.SafeToDeleteResult `json:"safe_to_delete,omitempty"`
	CreatedAt       time.Time                             `json:"created_at"`
}

type ArchivedEntry struct {
	SourceStorageEntryID  string                      `json:"source_storage_entry_id"`
	ArchiveStorageEntryID string                      `json:"archive_storage_entry_id,omitempty"`
	SourceViewPath        string                      `json:"source_view_path"`
	ArchiveViewPath       string                      `json:"archive_view_path"`
	ArchiveObjectPath     string                      `json:"archive_object_path"`
	ChecksumAlgorithm     string                      `json:"checksum_algorithm,omitempty"`
	ChecksumHex           string                      `json:"checksum_hex,omitempty"`
	Deduped               bool                        `json:"deduped"`
	SourceRef             storagecatalog.PhysicalRef  `json:"source_ref"`
	ArchiveRef            *storagecatalog.PhysicalRef `json:"archive_ref,omitempty"`
}

type selectedEntry struct {
	ViewPath          string
	RelativePath      string
	TargetIsExactFile bool
	Detail            storagecatalog.EntryDetail
	SourceRef         storagecatalog.PhysicalRef
}

type archiveSourceCandidate struct {
	entry        storagecatalog.Entry
	catalogPath  string
	relativePath string
	exact        bool
}

func NewService(catalog Catalog, archiveRoot string) Service {
	return Service{Catalog: catalog, ArchiveRoot: archiveRoot, OwnerNodeKey: "main"}
}

func (s Service) Archive(ctx context.Context, input ArchiveInput) (ArchiveResult, error) {
	normalized, err := s.normalizeInput(input)
	if err != nil {
		return ArchiveResult{}, err
	}
	if s.Catalog == nil {
		return ArchiveResult{}, fmt.Errorf("storage archive catalog is not configured")
	}
	now := s.now()
	archiveKey := strings.TrimSpace(normalized.ArchiveKey)
	if archiveKey == "" {
		archiveKey = archiveKeyForInput(normalized)
	}
	archiveKey, err = archiveKeySegment(archiveKey)
	if err != nil {
		return ArchiveResult{}, err
	}
	targetLogicalPath, err := archiveLogicalPath(normalized.TargetPath)
	if err != nil {
		return ArchiveResult{}, err
	}
	if normalized.DryRun {
		selected, err := s.resolveSourceEntries(ctx, normalized.SourceRef)
		if err != nil {
			return ArchiveResult{}, err
		}
		manifest, entries, err := s.planArchive(ctx, normalized, archiveKey, targetLogicalPath, selected, "", now, false)
		if err != nil {
			return ArchiveResult{}, err
		}
		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			return ArchiveResult{}, fmt.Errorf("encode archive manifest: %w", err)
		}
		if len(manifestJSON) > maxArchiveCustodyManifestBytes {
			return ArchiveResult{}, fmt.Errorf("archive manifest exceeds %d bytes", maxArchiveCustodyManifestBytes)
		}
		result := ArchiveResult{
			ArchiveManifest: storagecatalog.ArchiveManifest{
				ArchiveManifestID: manifest.ArchiveManifestID,
				ArchiveKey:        archiveKey,
				ArchiveKind:       normalized.ArchiveKind,
				OwnerNodeKey:      normalized.OwnerNodeKey,
				SourceRef:         normalized.SourceRef,
				Status:            "planned",
				ManifestJSON:      manifestJSON,
				CreatedAt:         now,
				FinalizedAt:       &now,
			},
			Manifest: manifest, ManifestPath: s.manifestPath(archiveKey), ArchiveRoot: s.archiveRoot(),
			DryRun: true, Entries: entries, CreatedAt: now,
		}
		result.SafeToDelete = s.safeToDeleteResults(ctx, entries)
		return result, nil
	}

	unlock, err := s.lockArchiveKey(archiveKey)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer unlock()

	manifest, exists, err := s.readPublishedArchiveManifest(archiveKey)
	if err != nil {
		return ArchiveResult{}, err
	}
	if exists {
		if err := validateArchiveManifestRequest(manifest, normalized, archiveKey); err != nil {
			return ArchiveResult{}, err
		}
		if err := s.verifyPublishedArchiveManifest(ctx, manifest); err != nil {
			return ArchiveResult{}, err
		}
	} else {
		selected, err := s.resolveSourceEntries(ctx, normalized.SourceRef)
		if err != nil {
			return ArchiveResult{}, err
		}
		if len(selected) == 0 {
			return ArchiveResult{}, fmt.Errorf("source did not resolve to archiveable storage entries")
		}
		stageKey := "." + archiveKey + ".preparing"
		if err := s.prepareArchiveStage(stageKey); err != nil {
			return ArchiveResult{}, err
		}
		published := false
		defer func() {
			if !published {
				_ = s.removeArchiveStage(stageKey)
			}
		}()
		manifest, _, err = s.planArchive(ctx, normalized, archiveKey, targetLogicalPath, selected, stageKey, now, true)
		if err != nil {
			return ArchiveResult{}, err
		}
		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			return ArchiveResult{}, fmt.Errorf("encode archive manifest: %w", err)
		}
		if len(manifestJSON) > maxArchiveCustodyManifestBytes {
			return ArchiveResult{}, fmt.Errorf("archive manifest exceeds %d bytes", maxArchiveCustodyManifestBytes)
		}
		writeManifest := s.writeArchiveCustodyManifest
		if s.writeManifestHook != nil {
			writeManifest = s.writeManifestHook
		}
		if err := writeManifest(stageKey, manifestJSON); err != nil {
			return ArchiveResult{}, fmt.Errorf("publish staged archive manifest: %w", err)
		}
		if err := s.publishArchiveStage(stageKey, archiveKey); err != nil {
			return ArchiveResult{}, err
		}
		published = true
	}

	commitInput, archiveEntries, err := s.archiveCommitInput(manifest)
	if err != nil {
		return ArchiveResult{}, err
	}
	commitResult, err := s.commitArchiveCatalog(ctx, commitInput)
	if err != nil {
		return ArchiveResult{}, err
	}
	if len(commitResult.Entries) != len(archiveEntries) || len(commitResult.Refs) != len(archiveEntries) {
		return ArchiveResult{}, fmt.Errorf("archive catalog returned incomplete commit evidence")
	}
	for index := range archiveEntries {
		entry := commitResult.Entries[index]
		ref := commitResult.Refs[index]
		archiveEntries[index].ArchiveStorageEntryID = entry.StorageEntryID
		archiveEntries[index].ArchiveRef = &ref
	}
	result := ArchiveResult{
		ArchiveManifest: commitResult.Manifest,
		Manifest:        manifest, ManifestPath: s.manifestPath(archiveKey), ArchiveRoot: s.archiveRoot(),
		Entries: archiveEntries, CreatedAt: manifest.CreatedAt,
	}
	result.SafeToDelete = s.safeToDeleteResults(ctx, archiveEntries)
	return result, nil
}

func (s Service) normalizeInput(input ArchiveInput) (ArchiveInput, error) {
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	if input.SourceRef == "" {
		return ArchiveInput{}, fmt.Errorf("source_ref is required")
	}
	input.TargetPath = strings.Trim(strings.ReplaceAll(input.TargetPath, "\\", "/"), "/")
	if input.TargetPath == "" {
		return ArchiveInput{}, fmt.Errorf("target_path is required")
	}
	if _, err := archiveLogicalPath(input.TargetPath); err != nil {
		return ArchiveInput{}, err
	}
	input.ArchiveKey = strings.TrimSpace(input.ArchiveKey)
	input.ArchiveKind = strings.TrimSpace(input.ArchiveKind)
	if input.ArchiveKind == "" {
		input.ArchiveKind = inferArchiveKind(input.TargetPath)
	}
	input.OwnerNodeKey = strings.TrimSpace(input.OwnerNodeKey)
	if input.OwnerNodeKey == "" {
		input.OwnerNodeKey = strings.TrimSpace(s.OwnerNodeKey)
	}
	if input.OwnerNodeKey == "" {
		input.OwnerNodeKey = "main"
	}
	if input.MarkSourceArchived && input.MarkSourceSuperseded {
		return ArchiveInput{}, fmt.Errorf("mark_source_archived and mark_source_superseded are mutually exclusive")
	}
	return input, nil
}

func (s Service) planArchive(ctx context.Context, input ArchiveInput, archiveKey, targetLogicalPath string, selected []selectedEntry, stageKey string, now time.Time, materialize bool) (ManifestDocument, []ArchivedEntry, error) {
	if len(selected) == 0 {
		return ManifestDocument{}, nil, fmt.Errorf("source did not resolve to archiveable storage entries")
	}
	canonicalSelected, err := canonicalizeArchiveEvidence(ctx, selected)
	if err != nil {
		return ManifestDocument{}, nil, err
	}
	selected = canonicalSelected
	reusableObjects, err := s.reusableArchiveObjects(ctx, selected)
	if err != nil {
		return ManifestDocument{}, nil, err
	}
	manifestID := ids.NewStorageArchiveManifestID()
	items := make([]ManifestItem, 0, len(selected))
	entries := make([]ArchivedEntry, 0, len(selected))
	contentPaths := map[string]string{}
	for _, selectedItem := range selected {
		sourceEntry := selectedItem.Detail.Entry
		contentKey := contentKeyForEntry(sourceEntry, selectedItem.SourceRef)
		objectRelative := contentPaths[contentKey]
		deduped := objectRelative != ""
		if objectRelative == "" {
			finalObjectPath := s.archiveObjectPath(archiveKey, contentKey, sourceEntry)
			objectRelative, err = filepath.Rel(s.archiveKeyRoot(archiveKey), finalObjectPath)
			if err != nil || objectRelative == "." || strings.HasPrefix(objectRelative, ".."+string(filepath.Separator)) {
				return ManifestDocument{}, nil, fmt.Errorf("archive object path escaped archive key")
			}
			objectRelative = filepath.ToSlash(objectRelative)
			contentPaths[contentKey] = objectRelative
			if materialize {
				stageObjectPath := filepath.Join(s.archiveKeyRoot(stageKey), filepath.FromSlash(objectRelative))
				existing, hasExisting := reusableObjects[contentKey]
				reused, err := s.materializeArchiveObject(ctx, selectedItem.SourceRef, stageObjectPath, existing, hasExisting, sourceEntry)
				if err != nil {
					return ManifestDocument{}, nil, fmt.Errorf("archive %s: %w", selectedItem.ViewPath, err)
				}
				deduped = reused
			}
		}
		archiveLogical := archiveItemLogicalPath(targetLogicalPath, selectedItem.RelativePath, selectedItem.TargetIsExactFile)
		archiveView := path.Join("main", "Archive", archiveLogical)
		item := ManifestItem{
			SourceStorageEntryID:    sourceEntry.StorageEntryID,
			ArchiveStorageEntryID:   ids.NewStorageEntryID(),
			ArchivePhysicalRefID:    ids.NewStoragePhysicalRefID(),
			SourceViewPath:          selectedItem.ViewPath,
			SourceLogicalPath:       sourceEntry.LogicalPath,
			SourceOriginalPath:      firstNonEmpty(sourceEntry.CurrentViewPath, sourceEntry.OriginalSourcePath, sourceEntry.LogicalPath),
			SourceMimeType:          sourceEntry.MimeType,
			SourceRefKind:           selectedItem.SourceRef.RefKind,
			SourceRefURI:            selectedItem.SourceRef.URI,
			SourceAvailabilityState: sourceEntry.AvailabilityState,
			ArchiveViewPath:         archiveView,
			ArchiveLogicalPath:      archiveLogical,
			ChecksumAlgorithm:       sourceEntry.ChecksumAlgorithm,
			ChecksumHex:             sourceEntry.ChecksumHex,
			SizeBytes:               sourceEntry.SizeBytes,
			FileClass:               sourceEntry.FileClass,
			ContentKey:              contentKey,
			ArchiveObjectPath:       objectRelative,
			Deduped:                 deduped,
		}
		items = append(items, item)
		entries = append(entries, ArchivedEntry{
			SourceStorageEntryID:  sourceEntry.StorageEntryID,
			ArchiveStorageEntryID: item.ArchiveStorageEntryID,
			SourceViewPath:        selectedItem.ViewPath,
			ArchiveViewPath:       archiveView,
			ArchiveObjectPath:     filepath.Join(s.archiveKeyRoot(archiveKey), filepath.FromSlash(objectRelative)),
			ChecksumAlgorithm:     sourceEntry.ChecksumAlgorithm,
			ChecksumHex:           sourceEntry.ChecksumHex,
			Deduped:               deduped,
			SourceRef:             selectedItem.SourceRef,
		})
	}
	return ManifestDocument{
		SchemaVersion:        ManifestSchemaVersion,
		CatalogRecovery:      CatalogRecoverySchemaVersion,
		ArchiveManifestID:    manifestID,
		ArchiveKey:           archiveKey,
		ArchiveKind:          input.ArchiveKind,
		SourceRef:            input.SourceRef,
		TargetPath:           input.TargetPath,
		OwnerNodeKey:         input.OwnerNodeKey,
		MarkSourceArchived:   input.MarkSourceArchived,
		MarkSourceSuperseded: input.MarkSourceSuperseded,
		ExpectedEntryCount:   len(items),
		ArchivedEntryCount:   len(items),
		Complete:             true,
		Entries:              items,
		CreatedAt:            now,
	}, entries, nil
}

func canonicalizeArchiveEvidence(ctx context.Context, selected []selectedEntry) ([]selectedEntry, error) {
	for index := range selected {
		size, checksum, err := deriveArchiveSourceEvidence(ctx, selected[index].SourceRef)
		if err != nil {
			return nil, fmt.Errorf("derive archive evidence for %s: %w", selected[index].ViewPath, err)
		}
		entry := selected[index].Detail.Entry
		if entry.SizeBytes != nil && *entry.SizeBytes != size {
			return nil, fmt.Errorf("archive source size changed for %s: got %d want %d", selected[index].ViewPath, size, *entry.SizeBytes)
		}
		if strings.EqualFold(strings.TrimSpace(entry.ChecksumAlgorithm), "sha256") && strings.TrimSpace(entry.ChecksumHex) != "" &&
			!strings.EqualFold(strings.TrimSpace(entry.ChecksumHex), checksum) {
			return nil, fmt.Errorf("archive source checksum changed for %s", selected[index].ViewPath)
		}
		if algorithm, value, ok := strings.Cut(strings.TrimSpace(selected[index].SourceRef.ContentAddress), ":"); ok &&
			strings.EqualFold(strings.TrimSpace(algorithm), "sha256") && strings.TrimSpace(value) != "" &&
			!strings.EqualFold(strings.TrimSpace(value), checksum) {
			return nil, fmt.Errorf("archive source content address changed for %s", selected[index].ViewPath)
		}
		entry.ChecksumAlgorithm = "sha256"
		entry.ChecksumHex = checksum
		entry.SizeBytes = int64Pointer(size)
		selected[index].Detail.Entry = entry
	}
	return selected, nil
}

func deriveArchiveSourceEvidence(ctx context.Context, sourceRef storagecatalog.PhysicalRef) (int64, string, error) {
	sourcePath, ok := localPathFromURI(sourceRef.URI)
	if !ok {
		return 0, "", fmt.Errorf("source ref is not a local file: %s", sourceRef.URI)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	var reader io.Reader = file
	var expectedSize *int64
	if sourceRef.RefKind == storagecatalog.PhysicalRefKindBackupArtifact {
		member := backupArtifactContentMember(sourceRef)
		tarReader := tar.NewReader(file)
		found := false
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return 0, "", err
			}
			if header == nil || header.Name != member {
				continue
			}
			if header.Typeflag != tar.TypeReg {
				return 0, "", fmt.Errorf("backup artifact member %q is not a regular file", member)
			}
			expectedSize = int64Pointer(header.Size)
			reader = tarReader
			found = true
			break
		}
		if !found {
			return 0, "", fmt.Errorf("backup artifact member %q was not found", member)
		}
	} else {
		info, err := file.Stat()
		if err != nil {
			return 0, "", err
		}
		if !info.Mode().IsRegular() {
			return 0, "", fmt.Errorf("archive source is not a regular file: %s", sourcePath)
		}
		expectedSize = int64Pointer(info.Size())
	}
	hash := sha256.New()
	size, err := copyWithContext(ctx, hash, reader)
	if err != nil {
		return 0, "", err
	}
	if expectedSize != nil && size != *expectedSize {
		return 0, "", fmt.Errorf("archive source was truncated: got %d want %d", size, *expectedSize)
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func int64Pointer(value int64) *int64 {
	return &value
}

func validateArchiveManifestRequest(manifest ManifestDocument, input ArchiveInput, archiveKey string) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.CatalogRecovery != CatalogRecoverySchemaVersion || !manifest.Complete ||
		manifest.ArchiveKey != archiveKey || manifest.ArchiveKind != input.ArchiveKind ||
		manifest.SourceRef != input.SourceRef || manifest.TargetPath != input.TargetPath ||
		manifest.OwnerNodeKey != input.OwnerNodeKey ||
		manifest.MarkSourceArchived != input.MarkSourceArchived ||
		manifest.MarkSourceSuperseded != input.MarkSourceSuperseded {
		return fmt.Errorf("archive key %q already contains different custody evidence", archiveKey)
	}
	if manifest.ExpectedEntryCount <= 0 || manifest.ArchivedEntryCount != manifest.ExpectedEntryCount || len(manifest.Entries) != manifest.ExpectedEntryCount {
		return fmt.Errorf("archive key %q contains an incomplete custody manifest", archiveKey)
	}
	if err := ids.Validate(ids.StorageArchiveManifestPrefix, manifest.ArchiveManifestID); err != nil {
		return fmt.Errorf("archive key %q has invalid manifest identity: %w", archiveKey, err)
	}
	return nil
}

func (s Service) verifyPublishedArchiveManifest(ctx context.Context, manifest ManifestDocument) error {
	seenEntries := map[string]struct{}{}
	seenRefs := map[string]struct{}{}
	seenSources := map[string]struct{}{}
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return err
	}
	defer root.Close()
	for index, item := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ids.Validate(ids.StorageEntryPrefix, item.SourceStorageEntryID); err != nil {
			return fmt.Errorf("archive item %d has invalid source identity: %w", index, err)
		}
		if err := ids.Validate(ids.StorageEntryPrefix, item.ArchiveStorageEntryID); err != nil {
			return fmt.Errorf("archive item %d has invalid entry identity: %w", index, err)
		}
		if err := ids.Validate(ids.StoragePhysicalRefPrefix, item.ArchivePhysicalRefID); err != nil {
			return fmt.Errorf("archive item %d has invalid ref identity: %w", index, err)
		}
		if _, duplicate := seenEntries[item.ArchiveStorageEntryID]; duplicate {
			return fmt.Errorf("archive manifest repeats entry %s", item.ArchiveStorageEntryID)
		}
		if _, duplicate := seenRefs[item.ArchivePhysicalRefID]; duplicate {
			return fmt.Errorf("archive manifest repeats ref %s", item.ArchivePhysicalRefID)
		}
		seenEntries[item.ArchiveStorageEntryID] = struct{}{}
		seenRefs[item.ArchivePhysicalRefID] = struct{}{}
		if _, duplicate := seenSources[item.SourceStorageEntryID]; duplicate {
			return fmt.Errorf("archive manifest repeats source entry %s", item.SourceStorageEntryID)
		}
		seenSources[item.SourceStorageEntryID] = struct{}{}
		objectRelative, err := confinedArchiveObjectPath(item.ArchiveObjectPath)
		if err != nil {
			return fmt.Errorf("archive item %d: %w", index, err)
		}
		normalizedArchiveLogical, err := storagecatalog.NormalizeLogicalPath(item.ArchiveLogicalPath)
		if err != nil || normalizedArchiveLogical != item.ArchiveLogicalPath ||
			(normalizedArchiveLogical != targetLogicalPathForManifest(manifest) && !strings.HasPrefix(normalizedArchiveLogical, targetLogicalPathForManifest(manifest)+"/")) ||
			item.ArchiveViewPath != path.Join("main", "Archive", normalizedArchiveLogical) {
			return fmt.Errorf("archive item %d has inconsistent logical custody paths", index)
		}
		normalizedSourceLogical, sourceLogicalErr := storagecatalog.NormalizeLogicalPath(item.SourceLogicalPath)
		if sourceLogicalErr != nil || normalizedSourceLogical != item.SourceLogicalPath || strings.TrimSpace(item.SourceViewPath) == "" ||
			strings.TrimSpace(item.SourceOriginalPath) == "" || !storagecatalog.ValidPhysicalRefKind(item.SourceRefKind) ||
			!storagecatalog.ValidAvailabilityState(item.SourceAvailabilityState) {
			return fmt.Errorf("archive item %d has incomplete source catalog evidence", index)
		}
		if _, ok := localPathFromURI(item.SourceRefURI); !ok {
			return fmt.Errorf("archive item %d source ref is not a local custody path", index)
		}
		if item.SizeBytes == nil || *item.SizeBytes < 0 || !strings.EqualFold(strings.TrimSpace(item.ChecksumAlgorithm), "sha256") ||
			len(strings.TrimSpace(item.ChecksumHex)) != sha256.Size*2 {
			return fmt.Errorf("archive item %d has incomplete payload evidence", index)
		}
		checksum := strings.ToLower(strings.TrimSpace(item.ChecksumHex))
		if _, err := hex.DecodeString(checksum); err != nil {
			return fmt.Errorf("archive item %d has invalid checksum evidence", index)
		}
		expectedContentKey := "sha256:" + checksum
		expectedObjectPath := filepath.Join(s.objectsRoot(manifest.ArchiveKey), "sha256", checksum[:2], checksum)
		expectedObjectRelative, relErr := filepath.Rel(s.archiveKeyRoot(manifest.ArchiveKey), expectedObjectPath)
		if relErr != nil || filepath.ToSlash(expectedObjectRelative) != filepath.ToSlash(objectRelative) || item.ContentKey != expectedContentKey {
			return fmt.Errorf("archive item %d content identity does not match its custody object", index)
		}
		if err := verifyArchiveObjectAt(ctx, root, filepath.Join(manifest.ArchiveKey, objectRelative), archiveEvidenceEntry(item)); err != nil {
			return fmt.Errorf("archive item %d: %w", index, err)
		}
	}
	return nil
}

func targetLogicalPathForManifest(manifest ManifestDocument) string {
	target, _ := archiveLogicalPath(manifest.TargetPath)
	return target
}

func confinedArchiveObjectPath(value string) (string, error) {
	value = filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
	if value == "." || value == "" || filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive object path must be relative to its archive key")
	}
	if value != "objects" && !strings.HasPrefix(value, "objects"+string(filepath.Separator)) {
		return "", fmt.Errorf("archive object path must be below objects")
	}
	return value, nil
}

func (s Service) archiveCommitInput(manifest ManifestDocument) (storagecatalog.ArchiveCommitInput, []ArchivedEntry, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return storagecatalog.ArchiveCommitInput{}, nil, err
	}
	finalizedAt := manifest.CreatedAt
	commit := storagecatalog.ArchiveCommitInput{Manifest: storagecatalog.CreateArchiveManifestInput{
		ArchiveManifestID: manifest.ArchiveManifestID, ArchiveKey: manifest.ArchiveKey,
		ArchiveKind: manifest.ArchiveKind, OwnerNodeKey: manifest.OwnerNodeKey,
		SourceRef: manifest.SourceRef, Status: "complete", ManifestJSON: manifestJSON,
		FinalizedAt: &finalizedAt,
	}}
	entries := make([]ArchivedEntry, 0, len(manifest.Entries))
	for _, item := range manifest.Entries {
		objectRelative, err := confinedArchiveObjectPath(item.ArchiveObjectPath)
		if err != nil {
			return storagecatalog.ArchiveCommitInput{}, nil, err
		}
		objectPath := filepath.Join(s.archiveKeyRoot(manifest.ArchiveKey), objectRelative)
		entryMetadata := archiveEntryMetadata(manifest, item)
		refMetadata, err := json.Marshal(map[string]any{
			"schema_version": "storage.archive_physical_ref.v0.7", "source": "storagearchive.service",
			"archive_manifest_id": manifest.ArchiveManifestID, "source_storage_entry_id": item.SourceStorageEntryID,
			"source_ref_kind": item.SourceRefKind, "source_uri": item.SourceRefURI,
		})
		if err != nil {
			return storagecatalog.ArchiveCommitInput{}, nil, err
		}
		commit.Entries = append(commit.Entries, storagecatalog.ArchiveCommitEntryInput{
			Entry: storagecatalog.RegisterEntryInput{
				StorageEntryID: item.ArchiveStorageEntryID, StorageClass: storagecatalog.StorageClassArchiveEntry,
				SourceArea: storagecatalog.SourceAreaMainArchive, OriginNodeKey: manifest.OwnerNodeKey,
				ArchiveManifestID: manifest.ArchiveManifestID, LogicalPath: item.ArchiveLogicalPath,
				OriginalSourcePath: item.SourceOriginalPath, CurrentViewPath: item.ArchiveViewPath,
				ChecksumAlgorithm: item.ChecksumAlgorithm, ChecksumHex: item.ChecksumHex, SizeBytes: item.SizeBytes,
				MimeType: item.SourceMimeType, FileClass: item.FileClass, ProcessingState: storagecatalog.ProcessingStateMetadataOnly,
				AvailabilityState: storagecatalog.AvailabilityStateAvailable, RetentionState: storagecatalog.RetentionStateRetained,
				Metadata: entryMetadata,
			},
			Ref: storagecatalog.RegisterPhysicalRefInput{
				StoragePhysicalRefID: item.ArchivePhysicalRefID, StorageEntryID: item.ArchiveStorageEntryID,
				RefKind: storagecatalog.PhysicalRefKindArchiveFile, URI: objectPath, NodeKey: manifest.OwnerNodeKey,
				ContentAddress: item.ContentKey, Status: storagecatalog.PhysicalRefStatusAvailable, Metadata: refMetadata,
			},
			Item: storagecatalog.ArchiveItemInput{
				ArchiveManifestID: manifest.ArchiveManifestID, StorageEntryID: item.ArchiveStorageEntryID,
				ArchivePath: item.ArchiveLogicalPath, Metadata: archiveItemMetadata(item),
			},
		})
		if manifest.MarkSourceArchived || manifest.MarkSourceSuperseded {
			desired := storagecatalog.AvailabilityStateArchived
			if manifest.MarkSourceSuperseded {
				desired = storagecatalog.AvailabilityStateSuperseded
			}
			commit.SourceDispositions = append(commit.SourceDispositions, storagecatalog.ArchiveSourceDispositionInput{
				StorageEntryID: item.SourceStorageEntryID, ExpectedAvailability: item.SourceAvailabilityState,
				AvailabilityState: desired,
			})
		}
		entries = append(entries, ArchivedEntry{
			SourceStorageEntryID: item.SourceStorageEntryID, ArchiveStorageEntryID: item.ArchiveStorageEntryID,
			SourceViewPath: item.SourceViewPath, ArchiveViewPath: item.ArchiveViewPath, ArchiveObjectPath: objectPath,
			ChecksumAlgorithm: item.ChecksumAlgorithm, ChecksumHex: item.ChecksumHex, Deduped: item.Deduped,
			SourceRef: storagecatalog.PhysicalRef{StorageEntryID: item.SourceStorageEntryID, RefKind: item.SourceRefKind, URI: item.SourceRefURI},
		})
	}
	return commit, entries, nil
}

func (s Service) commitArchiveCatalog(ctx context.Context, input storagecatalog.ArchiveCommitInput) (storagecatalog.ArchiveCommitResult, error) {
	return s.Catalog.CommitArchive(ctx, input)
}

func (s Service) resolveSourceEntries(ctx context.Context, sourceRef string) ([]selectedEntry, error) {
	sourceRef = strings.Trim(strings.ReplaceAll(sourceRef, "\\", "/"), "/")
	if strings.HasPrefix(sourceRef, ids.StorageEntryPrefix+"_") {
		detail, err := s.Catalog.InspectEntry(ctx, sourceRef)
		if err != nil {
			return nil, err
		}
		ref, err := chooseReadableSourceRef(detail)
		if err != nil {
			return nil, err
		}
		viewPath := firstNonEmpty(detail.Entry.CurrentViewPath, detail.Entry.LogicalPath)
		return []selectedEntry{{
			ViewPath:          viewPath,
			RelativePath:      path.Base(viewPath),
			TargetIsExactFile: true,
			Detail:            detail,
			SourceRef:         ref,
		}}, nil
	}
	entries, err := s.listArchiveSourceEntries(ctx, sourceRef)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(sourceRef, "/") + "/"
	candidates := make([]archiveSourceCandidate, 0, len(entries))
	seenPaths := map[string]string{}
	var exactCount int
	for _, entry := range entries {
		if !archiveCatalogEntrySelectable(entry) {
			continue
		}
		catalogPath, err := storageview.CatalogPathForEntry(entry)
		if err != nil {
			return nil, err
		}
		exact := catalogPath == sourceRef
		if !exact && !strings.HasPrefix(catalogPath, prefix) {
			continue
		}
		if previousID, duplicate := seenPaths[catalogPath]; duplicate && previousID != entry.StorageEntryID {
			return nil, fmt.Errorf("source path is ambiguous in catalog: %s", catalogPath)
		}
		seenPaths[catalogPath] = entry.StorageEntryID
		relativePath := path.Base(catalogPath)
		if !exact {
			relativePath = strings.TrimPrefix(catalogPath, prefix)
		}
		if exact {
			exactCount++
		}
		candidates = append(candidates, archiveSourceCandidate{entry: entry, catalogPath: catalogPath, relativePath: relativePath, exact: exact})
	}
	if exactCount > 1 {
		return nil, fmt.Errorf("source path is ambiguous in catalog: %s", sourceRef)
	}
	if exactCount == 1 {
		for _, candidate := range candidates {
			if candidate.exact && candidate.entry.FileClass != storagecatalog.FileClassDirectory {
				item, err := s.selectedEntriesForCandidates(ctx, []archiveSourceCandidate{candidate})
				if err != nil {
					return nil, err
				}
				item[0].TargetIsExactFile = true
				return item, nil
			}
		}
	}
	descendants := candidates[:0]
	for _, candidate := range candidates {
		if !candidate.exact && candidate.entry.FileClass != storagecatalog.FileClassDirectory {
			descendants = append(descendants, candidate)
		}
	}
	if len(descendants) == 0 {
		return nil, fmt.Errorf("source path was not found in storage catalog: %s", sourceRef)
	}
	selected, err := s.selectedEntriesForCandidates(ctx, descendants)
	if err != nil {
		return nil, err
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ViewPath < selected[j].ViewPath })
	return selected, nil
}

func archiveCatalogEntrySelectable(entry storagecatalog.Entry) bool {
	if entry.DeletedAt != nil {
		return false
	}
	switch entry.AvailabilityState {
	case storagecatalog.AvailabilityStateAvailable, storagecatalog.AvailabilityStateArchived, storagecatalog.AvailabilityStateSuperseded:
		return true
	default:
		return false
	}
}

func (s Service) selectedEntriesForCandidates(ctx context.Context, candidates []archiveSourceCandidate) ([]selectedEntry, error) {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.entry.StorageEntryID)
	}
	details := make(map[string]storagecatalog.EntryDetail, len(ids))
	if catalog, ok := s.Catalog.(archiveDetailCatalog); ok {
		loaded, err := catalog.ListEntryDetails(ctx, ids)
		if err != nil {
			return nil, err
		}
		details = loaded
	} else {
		for _, id := range ids {
			detail, err := s.Catalog.InspectEntry(ctx, id)
			if err != nil {
				return nil, err
			}
			details[id] = detail
		}
	}
	selected := make([]selectedEntry, 0, len(candidates))
	for _, candidate := range candidates {
		detail, ok := details[candidate.entry.StorageEntryID]
		if !ok || detail.Entry.StorageEntryID != candidate.entry.StorageEntryID {
			return nil, fmt.Errorf("catalog detail missing for archive source %s", candidate.entry.StorageEntryID)
		}
		ref, err := chooseReadableSourceRef(detail)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", candidate.catalogPath, err)
		}
		selected = append(selected, selectedEntry{
			ViewPath: candidate.catalogPath, RelativePath: candidate.relativePath,
			Detail: detail, SourceRef: ref,
		})
	}
	return selected, nil
}

func (s Service) listArchiveSourceEntries(ctx context.Context, sourceRef string) ([]storagecatalog.Entry, error) {
	pageSize := s.maxEntries()
	if pageSize <= 0 || pageSize > 5000 {
		pageSize = 5000
	}
	filter := archiveCatalogListFilter(sourceRef, pageSize)
	var entries []storagecatalog.Entry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := s.Catalog.ListEntries(ctx, filter)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
		if len(page) < pageSize {
			return entries, nil
		}
		filter.Offset += len(page)
	}
}

func archiveCatalogListFilter(sourceRef string, limit int) storagecatalog.ListFilter {
	filter := storagecatalog.ListFilter{Limit: limit, IncludeDeleted: true, PathPrefixes: archiveCatalogQueryPrefixes(sourceRef)}
	segments := strings.Split(strings.Trim(sourceRef, "/"), "/")
	switch {
	case len(segments) >= 2 && strings.EqualFold(segments[0], "main") && strings.EqualFold(segments[1], "Documents"):
		filter.SourceArea = storagecatalog.SourceAreaMainDocuments
	case len(segments) >= 2 && strings.EqualFold(segments[0], "main") && strings.EqualFold(segments[1], "Archive"):
		filter.SourceArea = storagecatalog.SourceAreaMainArchive
	case len(segments) >= 3 && strings.EqualFold(segments[1], "Dropzone"):
		filter.SourceArea = storagecatalog.SourceAreaDropzone
		filter.OriginNodeKey = segments[0]
	case len(segments) >= 3 && strings.EqualFold(segments[1], "Backups"):
		filter.OriginNodeKey = segments[0]
		switch strings.ToLower(segments[2]) {
		case "documents":
			filter.SourceArea = storagecatalog.SourceAreaDocuments
		case "notes":
			filter.SourceArea = storagecatalog.SourceAreaNotes
		case "projects":
			filter.SourceArea = storagecatalog.SourceAreaProjects
		case "external watched roots":
			filter.SourceArea = storagecatalog.SourceAreaExternalWatchedRoot
		}
	}
	return filter
}

func archiveCatalogQueryPrefixes(sourceRef string) []string {
	values := []string{strings.Trim(sourceRef, "/")}
	segments := strings.Split(strings.Trim(sourceRef, "/"), "/")
	var logical string
	switch {
	case len(segments) >= 3 && strings.EqualFold(segments[0], "main") &&
		(strings.EqualFold(segments[1], "Documents") || strings.EqualFold(segments[1], "Archive")):
		logical = strings.Join(segments[2:], "/")
	case len(segments) >= 5 && strings.EqualFold(segments[1], "Backups") && strings.EqualFold(segments[3], "current"):
		logical = strings.Join(segments[4:], "/")
	case len(segments) >= 6 && strings.EqualFold(segments[1], "Backups") && strings.EqualFold(segments[2], "External Watched Roots") && strings.EqualFold(segments[4], "current"):
		logical = strings.Join(segments[5:], "/")
	case len(segments) >= 6 && strings.EqualFold(segments[1], "Backups") && strings.EqualFold(segments[2], "Projects") && strings.EqualFold(segments[4], "current"):
		logical = strings.Join(segments[5:], "/")
	case len(segments) >= 3 && strings.EqualFold(segments[1], "Dropzone"):
		logical = strings.Join(segments[2:], "/")
	}
	if logical != "" && logical != values[0] {
		values = append(values, logical)
	}
	return values
}

func (s Service) safeToDeleteResults(ctx context.Context, entries []ArchivedEntry) []storageretention.SafeToDeleteResult {
	if s.SafeToDelete == nil {
		return nil
	}
	results := make([]storageretention.SafeToDeleteResult, 0, len(entries))
	for _, entry := range entries {
		result, err := s.SafeToDelete.SafeToDelete(ctx, storageretention.SafeToDeleteInput{Ref: entry.SourceStorageEntryID})
		if err == nil {
			results = append(results, result)
		}
	}
	return results
}

func chooseReadableSourceRef(detail storagecatalog.EntryDetail) (storagecatalog.PhysicalRef, error) {
	refs := storagecatalog.ResolvePhysicalRefs(detail.PhysicalRefs, storagecatalog.ResolvePhysicalRefOptions{LocalOnly: true})
	if len(refs) > 0 {
		return refs[0], nil
	}
	return storagecatalog.PhysicalRef{}, fmt.Errorf("no readable retained source found for storage entry %s", detail.Entry.StorageEntryID)
}

func (s Service) reusableArchiveObjects(ctx context.Context, selected []selectedEntry) (map[string]storagecatalog.PhysicalRef, error) {
	finder, ok := s.Catalog.(archiveContentRefFinder)
	if !ok {
		return nil, nil
	}
	contentAddresses := make([]string, 0, len(selected))
	for _, item := range selected {
		contentAddresses = append(contentAddresses, contentKeyForEntry(item.Detail.Entry, item.SourceRef))
	}
	refs, err := finder.FindAvailablePhysicalRefsByContentAddresses(ctx, storagecatalog.PhysicalRefKindArchiveFile, contentAddresses)
	if err != nil {
		return nil, fmt.Errorf("find reusable archive objects: %w", err)
	}
	return refs, nil
}

func (s Service) materializeArchiveObject(ctx context.Context, sourceRef storagecatalog.PhysicalRef, objectPath string, existing storagecatalog.PhysicalRef, hasExisting bool, sourceEntry storagecatalog.Entry) (bool, error) {
	if hasExisting {
		reused, err := s.linkExistingArchiveObject(ctx, existing.URI, objectPath, sourceEntry)
		if err != nil {
			return false, err
		}
		if reused {
			return true, nil
		}
	}
	return false, s.copySourceToArchive(ctx, sourceRef, objectPath, sourceEntry)
}

func (s Service) linkExistingArchiveObject(ctx context.Context, existingPath, objectPath string, sourceEntry storagecatalog.Entry) (bool, error) {
	root, sourceRel, err := s.openArchiveRelative(existingPath)
	if err != nil {
		// Catalog rows created before canonical archive custody may reference a
		// different root. They are not eligible for filesystem-level reuse.
		return false, nil
	}
	defer root.Close()
	targetRel, err := archiveRelativePath(s.archiveRoot(), objectPath)
	if err != nil {
		return false, err
	}
	info, err := root.Lstat(sourceRel)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect reusable archive object: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("reusable archive object must be a regular file: %s", existingPath)
	}
	if err := verifyArchiveObjectAt(ctx, root, sourceRel, sourceEntry); err != nil {
		return false, fmt.Errorf("verify reusable archive object: %w", err)
	}
	if sourceRel == targetRel {
		return true, nil
	}
	if err := ensureRealArchiveDirectories(root, filepath.Dir(targetRel), 0o750); err != nil {
		return false, err
	}
	if _, err := root.Lstat(targetRel); err == nil {
		return true, verifyArchiveObjectAt(ctx, root, targetRel, sourceEntry)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := root.Link(sourceRel, targetRel); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return false, nil
		}
		if errors.Is(err, os.ErrExist) {
			return true, verifyArchiveObjectAt(ctx, root, targetRel, sourceEntry)
		}
		return false, fmt.Errorf("link reusable archive object: %w", err)
	}
	if err := verifyArchiveObjectAt(ctx, root, targetRel, sourceEntry); err != nil {
		_ = root.Remove(targetRel)
		return false, err
	}
	return true, nil
}

func (s Service) copySourceToArchive(ctx context.Context, sourceRef storagecatalog.PhysicalRef, objectPath string, sourceEntry storagecatalog.Entry) error {
	root, targetRel, err := s.openArchiveRelative(objectPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(targetRel); err == nil {
		return verifyArchiveObjectAt(ctx, root, targetRel, sourceEntry)
	} else if !os.IsNotExist(err) {
		return err
	}
	sourcePath, ok := localPathFromURI(sourceRef.URI)
	if !ok {
		return fmt.Errorf("source ref is not a local file: %s", sourceRef.URI)
	}
	parentRel := filepath.Dir(targetRel)
	if err := ensureRealArchiveDirectories(root, parentRel, 0o750); err != nil {
		return err
	}
	tmpRel := filepath.Join(parentRel, ".loom-archive-"+ids.NewStoragePhysicalRefID())
	tmp, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = root.Remove(tmpRel)
		}
	}()
	switch sourceRef.RefKind {
	case storagecatalog.PhysicalRefKindBackupArtifact:
		err = copyTarMember(ctx, sourcePath, backupArtifactContentMember(sourceRef), tmp)
	default:
		err = copyRegularFile(ctx, sourcePath, tmp)
	}
	if syncErr := tmp.Sync(); syncErr != nil && err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := root.Link(tmpRel, targetRel); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := verifyArchiveObjectAt(ctx, root, targetRel, sourceEntry); err != nil {
			return err
		}
	} else if err := verifyArchiveObjectAt(ctx, root, targetRel, sourceEntry); err != nil {
		_ = root.Remove(targetRel)
		return err
	}
	if err := root.Remove(tmpRel); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func verifyArchiveObjectAt(ctx context.Context, root *os.Root, objectRel string, entry storagecatalog.Entry) error {
	if err := requireRealArchiveDirectories(root, filepath.Dir(objectRel)); err != nil {
		return err
	}
	info, err := root.Lstat(objectRel)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("archive object is not a regular file: %s", objectRel)
	}
	if entry.SizeBytes != nil && *entry.SizeBytes != info.Size() {
		return fmt.Errorf("archive object size mismatch for %s: got %d want %d", objectRel, info.Size(), *entry.SizeBytes)
	}
	if strings.EqualFold(strings.TrimSpace(entry.ChecksumAlgorithm), "sha256") && strings.TrimSpace(entry.ChecksumHex) != "" {
		got, err := archiveObjectSHA256Hex(ctx, root, objectRel)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, strings.TrimSpace(entry.ChecksumHex)) {
			return fmt.Errorf("archive object checksum mismatch for %s: got sha256:%s want sha256:%s", objectRel, got, strings.TrimSpace(entry.ChecksumHex))
		}
	}
	return nil
}

func requireRealArchiveDirectories(root *os.Root, relative string) error {
	relative = filepath.Clean(relative)
	if relative == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("unsafe archive custody directory %q", relative)
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("archive custody component %q must be a real directory", current)
		}
	}
	return nil
}

func archiveObjectSHA256Hex(ctx context.Context, root *os.Root, objectRel string) (string, error) {
	file, err := root.Open(objectRel)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := copyWithContext(ctx, hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyRegularFile(ctx context.Context, sourcePath string, destination io.Writer) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = copyWithContext(ctx, destination, file)
	return err
}

func copyTarMember(ctx context.Context, tarPath, member string, destination io.Writer) error {
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("backup artifact member %q was not found", member)
		}
		if err != nil {
			return err
		}
		if header == nil || header.Name != member {
			continue
		}
		if header.FileInfo().IsDir() {
			return fmt.Errorf("backup artifact member %q is a directory", member)
		}
		_, err = copyWithContext(ctx, destination, reader)
		return err
	}
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 1024*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			m, writeErr := destination.Write(buffer[:n])
			written += int64(m)
			if writeErr != nil {
				return written, writeErr
			}
			if m != n {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func backupArtifactContentMember(ref storagecatalog.PhysicalRef) string {
	var metadata map[string]any
	if len(ref.Metadata) > 0 && json.Unmarshal(ref.Metadata, &metadata) == nil {
		if value, ok := metadata["content_member"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "content"
}

func writeManifestFile(pathValue string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o770); err != nil {
		return err
	}
	var decoded any
	if json.Unmarshal(payload, &decoded) == nil {
		if formatted, err := json.MarshalIndent(decoded, "", "  "); err == nil {
			payload = append(formatted, '\n')
		}
	}
	return os.WriteFile(pathValue, payload, 0o640)
}

const maxArchiveCustodyManifestBytes = 64 << 20

func (s Service) lockArchiveKey(archiveKey string) (func(), error) {
	rootPath := s.archiveRoot()
	if err := os.MkdirAll(rootPath, 0o750); err != nil {
		return nil, err
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("archive root must be a real directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := ensureRealArchiveDirectories(root, ".locks", 0o750); err != nil {
		return nil, err
	}
	lockFile, err := root.OpenFile(filepath.Join(".locks", archiveKey+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}, nil
}

func (s Service) readPublishedArchiveManifest(archiveKey string) (ManifestDocument, bool, error) {
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return ManifestDocument{}, false, err
	}
	defer root.Close()
	keyInfo, err := root.Lstat(archiveKey)
	if os.IsNotExist(err) {
		return ManifestDocument{}, false, nil
	}
	if err != nil {
		return ManifestDocument{}, false, err
	}
	if keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.IsDir() {
		return ManifestDocument{}, false, fmt.Errorf("archive key %q must be a real directory", archiveKey)
	}
	manifestRel := filepath.Join(archiveKey, "manifest.json")
	manifestInfo, err := root.Lstat(manifestRel)
	if err != nil {
		if os.IsNotExist(err) {
			return ManifestDocument{}, false, fmt.Errorf("archive key %q exists without a committed manifest", archiveKey)
		}
		return ManifestDocument{}, false, err
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return ManifestDocument{}, false, fmt.Errorf("archive key %q manifest must be a regular file", archiveKey)
	}
	file, err := root.Open(manifestRel)
	if err != nil {
		return ManifestDocument{}, false, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxArchiveCustodyManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return ManifestDocument{}, false, readErr
	}
	if closeErr != nil {
		return ManifestDocument{}, false, closeErr
	}
	if len(payload) > maxArchiveCustodyManifestBytes {
		return ManifestDocument{}, false, fmt.Errorf("archive key %q manifest exceeds %d bytes", archiveKey, maxArchiveCustodyManifestBytes)
	}
	var manifest ManifestDocument
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return ManifestDocument{}, false, fmt.Errorf("decode archive key %q manifest: %w", archiveKey, err)
	}
	return manifest, true, nil
}

func (s Service) prepareArchiveStage(stageKey string) error {
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(stageKey); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("archive staging path %q must be a real directory", stageKey)
		}
		if err := root.RemoveAll(stageKey); err != nil {
			return fmt.Errorf("remove abandoned archive staging: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return ensureRealArchiveDirectories(root, filepath.Join(stageKey, "objects"), 0o750)
}

func (s Service) removeArchiveStage(stageKey string) error {
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(stageKey)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("archive staging path %q is not a real directory", stageKey)
	}
	return root.RemoveAll(stageKey)
}

func (s Service) publishArchiveStage(stageKey, archiveKey string) error {
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(archiveKey); err == nil {
		return fmt.Errorf("archive key %q was published concurrently", archiveKey)
	} else if !os.IsNotExist(err) {
		return err
	}
	stageInfo, err := root.Lstat(stageKey)
	if err != nil {
		return err
	}
	if stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.IsDir() {
		return fmt.Errorf("archive staging path %q must be a real directory", stageKey)
	}
	if _, err := root.Lstat(filepath.Join(stageKey, "manifest.json")); err != nil {
		return fmt.Errorf("archive staging manifest is not committed: %w", err)
	}
	if err := root.Rename(stageKey, archiveKey); err != nil {
		return fmt.Errorf("commit archive key: %w", err)
	}
	return syncDirectory(s.archiveRoot())
}

func syncDirectory(pathValue string) error {
	directory, err := os.Open(pathValue)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (s Service) writeArchiveCustodyManifest(archiveKey string, payload []byte) error {
	var decoded any
	if json.Unmarshal(payload, &decoded) == nil {
		if formatted, err := json.MarshalIndent(decoded, "", "  "); err == nil {
			payload = append(formatted, '\n')
		}
	}
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return err
	}
	defer root.Close()
	manifestRel := filepath.Join(archiveKey, "manifest.json")
	tempRel := filepath.Join(archiveKey, ".manifest.tmp-"+ids.NewStorageArchiveManifestID())
	file, err := root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = root.Remove(tempRel)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(tempRel)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(tempRel)
		return err
	}
	if err := root.Rename(tempRel, manifestRel); err != nil {
		_ = root.Remove(tempRel)
		return err
	}
	return syncDirectory(filepath.Join(s.archiveRoot(), archiveKey))
}

func archiveKeySegment(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") || filepath.Base(value) != value {
		return "", fmt.Errorf("archive_key must be one safe directory name")
	}
	if value == "objects" || value == "manifests" {
		return "", fmt.Errorf("archive_key %q is reserved for legacy restore compatibility", value)
	}
	if strings.HasPrefix(value, ".") {
		return "", fmt.Errorf("archive_key %q is reserved for internal archive state", value)
	}
	return value, nil
}

func archiveLogicalPath(targetPath string) (string, error) {
	targetPath = strings.Trim(strings.ReplaceAll(targetPath, "\\", "/"), "/")
	const prefix = "main/Archive/"
	if targetPath == "main/Archive" {
		return "", fmt.Errorf("target_path must point inside main/Archive")
	}
	if !strings.HasPrefix(targetPath, prefix) {
		return "", fmt.Errorf("target_path must be under main/Archive")
	}
	logical := strings.TrimPrefix(targetPath, prefix)
	normalized, err := storagecatalog.NormalizeLogicalPath(logical)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func archiveItemLogicalPath(targetLogicalPath, relativePath string, single bool) string {
	if single {
		return targetLogicalPath
	}
	relativePath = strings.Trim(relativePath, "/")
	if relativePath == "" {
		return targetLogicalPath
	}
	return path.Join(targetLogicalPath, relativePath)
}

func inferArchiveKind(targetPath string) string {
	lower := strings.ToLower(targetPath)
	switch {
	case strings.Contains(lower, "/projects/"):
		return "project_archive"
	case strings.Contains(lower, "/notes/"):
		return "notes_archive"
	case strings.Contains(lower, "/documents/"):
		return "document_archive"
	default:
		return "manual_archive"
	}
}

func archiveKeyForInput(input ArchiveInput) string {
	label := strings.Trim(strings.TrimPrefix(strings.ReplaceAll(input.TargetPath, "\\", "/"), "main/Archive/"), "/")
	label = storageview.SanitizeLabel(label)
	if strings.HasPrefix(label, ".") {
		label = "archive-" + strings.TrimLeft(label, ".")
	}
	if label == "archive-" {
		label = "archive-untitled"
	}
	evidence := strings.Join([]string{input.SourceRef, input.TargetPath, input.ArchiveKind, input.OwnerNodeKey}, "\x00")
	digest := sha256.Sum256([]byte(evidence))
	return label + "-" + hex.EncodeToString(digest[:6])
}

func contentKeyForEntry(entry storagecatalog.Entry, ref storagecatalog.PhysicalRef) string {
	algorithm := strings.TrimSpace(entry.ChecksumAlgorithm)
	value := strings.TrimSpace(entry.ChecksumHex)
	if algorithm != "" && value != "" {
		return strings.ToLower(algorithm + ":" + value)
	}
	if strings.TrimSpace(ref.ContentAddress) != "" {
		return strings.ToLower(strings.TrimSpace(ref.ContentAddress))
	}
	hash := sha256.Sum256([]byte(ref.RefKind + "\x00" + ref.URI))
	return "ref-sha256:" + hex.EncodeToString(hash[:])
}

func (s Service) archiveObjectPath(archiveKey, contentKey string, entry storagecatalog.Entry) string {
	kind, value, ok := strings.Cut(contentKey, ":")
	if !ok || strings.TrimSpace(value) == "" {
		kind = "content"
		value = storageview.SanitizeLabel(contentKey)
	}
	value = storageview.SanitizeLabel(value)
	if len(value) > 2 {
		return filepath.Join(s.objectsRoot(archiveKey), kind, value[:2], value)
	}
	return filepath.Join(s.objectsRoot(archiveKey), kind, "short", firstNonEmpty(value, entry.StorageEntryID))
}

func (s Service) archiveKeyRoot(archiveKey string) string {
	return filepath.Join(s.archiveRoot(), archiveKey)
}

func (s Service) prepareArchiveKeyRoot(archiveKey string) error {
	rootPath := s.archiveRoot()
	if err := os.MkdirAll(rootPath, 0o750); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("archive root must be a real directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	return ensureRealArchiveDirectories(root, filepath.Join(archiveKey, "objects"), 0o750)
}

func (s Service) openArchiveRelative(pathValue string) (*os.Root, string, error) {
	relative, err := archiveRelativePath(s.archiveRoot(), pathValue)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(s.archiveRoot())
	if err != nil {
		return nil, "", err
	}
	return root, relative, nil
}

func archiveRelativePath(rootPath, pathValue string) (string, error) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	if rootPath == "." || rootPath == "" || pathValue == "." || pathValue == "" || !filepath.IsAbs(rootPath) || !filepath.IsAbs(pathValue) {
		return "", fmt.Errorf("archive path and root must be absolute")
	}
	relative, err := filepath.Rel(rootPath, pathValue)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("archive object path %q is outside archive root %q", pathValue, rootPath)
	}
	return relative, nil
}

func ensureRealArchiveDirectories(root *os.Root, relative string, mode os.FileMode) error {
	relative = filepath.Clean(relative)
	if relative == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("unsafe archive custody directory %q", relative)
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("archive custody component %q must be a real directory", current)
		}
	}
	return nil
}

func (s Service) objectsRoot(archiveKey string) string {
	return filepath.Join(s.archiveKeyRoot(archiveKey), "objects")
}

func (s Service) manifestPath(archiveKey string) string {
	return filepath.Join(s.archiveKeyRoot(archiveKey), "manifest.json")
}

func (s Service) archiveRoot() string {
	root := strings.TrimSpace(s.ArchiveRoot)
	if root == "" {
		root = filepath.Join(os.TempDir(), "loom-storage-archive")
	}
	return filepath.Clean(root)
}

func (s Service) maxEntries() int {
	if s.MaxEntriesPerArchive > 0 {
		return s.MaxEntriesPerArchive
	}
	return 5000
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func archiveEvidenceEntry(item ManifestItem) storagecatalog.Entry {
	return storagecatalog.Entry{
		StorageEntryID: item.SourceStorageEntryID, LogicalPath: item.SourceLogicalPath,
		OriginalSourcePath: item.SourceOriginalPath, CurrentViewPath: item.SourceViewPath,
		ChecksumAlgorithm: item.ChecksumAlgorithm, ChecksumHex: item.ChecksumHex,
		SizeBytes: item.SizeBytes, MimeType: item.SourceMimeType, FileClass: item.FileClass,
		AvailabilityState: item.SourceAvailabilityState,
	}
}

func archiveEntryMetadata(manifest ManifestDocument, item ManifestItem) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version":          "storage.archive_entry.v0.6",
		"source":                  "storagearchive.service",
		"source_ref":              manifest.SourceRef,
		"source_storage_entry_id": item.SourceStorageEntryID,
		"source_view_path":        item.SourceViewPath,
		"source_logical_path":     item.SourceLogicalPath,
		"source_ref_kind":         item.SourceRefKind,
		"source_uri":              item.SourceRefURI,
		"archive_target_path":     manifest.TargetPath,
		"content_key":             item.ContentKey,
		"deduped":                 item.Deduped,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func archiveItemMetadata(item ManifestItem) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version":          "storage.archive_item.v0.6",
		"source":                  "storagearchive.service",
		"source_storage_entry_id": item.SourceStorageEntryID,
		"source_view_path":        item.SourceViewPath,
		"source_logical_path":     item.SourceLogicalPath,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func localPathFromURI(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), true
	}
	if strings.HasPrefix(raw, "file://") {
		parsed, err := url.Parse(raw)
		if err != nil || strings.TrimSpace(parsed.Path) == "" {
			return "", false
		}
		return filepath.Clean(parsed.Path), true
	}
	return "", false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
