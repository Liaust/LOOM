package storageretention

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
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagefidelity"
)

func NewService(catalog Catalog) Service {
	return Service{Catalog: catalog}
}

func (s Service) SafeToDelete(ctx context.Context, input SafeToDeleteInput) (SafeToDeleteResult, error) {
	ref := strings.TrimSpace(input.Ref)
	checkedAt := s.now()
	result := SafeToDeleteResult{Ref: ref, Decision: DecisionUnknown, CheckedAt: checkedAt}
	if ref == "" {
		result.Blockers = append(result.Blockers, "ref is required")
		return result, fmt.Errorf("storage ref is required")
	}
	if s.Catalog == nil {
		result.Blockers = append(result.Blockers, "retention catalog is not configured")
		return result, fmt.Errorf("storage retention catalog is not configured")
	}
	detail, err := s.Catalog.InspectEntry(ctx, ref)
	if err != nil {
		result.Blockers = append(result.Blockers, "no accepted catalog entry matched this ref")
		result.Decision = DecisionUnknown
		return result, nil
	}
	result.Entry = &detail.Entry
	result.PhysicalRefs = append([]storagecatalog.PhysicalRef{}, detail.PhysicalRefs...)
	fidelity := storagefidelity.EvaluateEntry(detail.Entry, detail.PhysicalRefs, result.CheckedAt)
	result.Warnings = append(result.Warnings, findingSummaries(storagefidelity.WarningFindings(fidelity))...)
	if detail.Entry.StorageClass == storagecatalog.StorageClassMainDocument || detail.Entry.SourceArea == storagecatalog.SourceAreaMainDocuments {
		protection := s.protectionFromDetail(ctx, detail, storagecatalog.MainDocumentProtectionInput{})
		result.Safe = protection.SafeToDeleteSource
		if blockers := storagefidelity.BlockingFindings(fidelity); len(blockers) > 0 {
			result.Safe = false
			result.Decision = DecisionNotSafe
			result.Blockers = append(result.Blockers, findingSummaries(blockers)...)
			return result, nil
		}
		if result.Safe {
			result.Decision = DecisionSafe
			result.Reasons = append(result.Reasons, "main document retained bytes are locally verified and covered by a verified cloud snapshot")
			if len(result.Warnings) > 0 {
				result.Decision = DecisionPartiallySafe
				result.Reasons = append(result.Reasons, "source payload is retained, but safe view has fidelity warnings that may need explicit acceptance")
			}
		} else {
			result.Decision = DecisionNotSafe
			result.Blockers = append(result.Blockers, protection.UnsafeReason)
		}
		return result, nil
	}
	return decideSafeToDelete(result, detail), nil
}

func (s Service) MainDocumentProtection(ctx context.Context, input storagecatalog.MainDocumentProtectionInput) (storagecatalog.MainDocumentProtectionStatus, error) {
	checkedAt := s.now()
	relativePath := normalizeMainDocumentPath(input.RelativePath, input.ViewPath)
	status := storagecatalog.MainDocumentProtectionStatus{
		RelativePath:      relativePath,
		ViewPath:          storagecatalog.MainDocumentViewPath(relativePath),
		AvailabilityState: "unknown",
		UnsafeReason:      "no accepted catalog entry matched this main Documents path",
		CheckedAt:         checkedAt,
	}
	if relativePath == "" {
		status.UnsafeReason = "main document path is required"
		return status, fmt.Errorf("main document path is required")
	}
	if s.Catalog == nil {
		status.UnsafeReason = "retention catalog is not configured"
		return status, fmt.Errorf("storage retention catalog is not configured")
	}
	detail, err := s.Catalog.InspectMainDocumentByPath(ctx, relativePath, storagecatalog.InspectOptions{IncludeDeleted: true})
	if err != nil {
		return status, nil
	}
	input.RelativePath = relativePath
	return s.protectionFromDetail(ctx, detail, input), nil
}

func (s Service) Fetch(ctx context.Context, input FetchInput) (FetchResult, error) {
	ref := strings.TrimSpace(input.Ref)
	destination := strings.TrimSpace(input.DestinationPath)
	if ref == "" {
		return FetchResult{}, fmt.Errorf("storage ref is required")
	}
	if destination == "" {
		return FetchResult{}, fmt.Errorf("destination_path is required")
	}
	if s.Catalog == nil {
		return FetchResult{}, fmt.Errorf("storage retention catalog is not configured")
	}
	detail, err := s.Catalog.InspectEntry(ctx, ref)
	if err != nil {
		return FetchResult{}, err
	}
	sourceRef, err := chooseReadableSourceRef(detail)
	if err != nil {
		return FetchResult{}, err
	}
	bytesWritten, err := copySourceToDestination(ctx, *sourceRef, destination, input.Overwrite)
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{
		Ref:             ref,
		DestinationPath: filepath.Clean(destination),
		BytesWritten:    bytesWritten,
		SourceRef:       sourceRef,
		Entry:           detail.Entry,
		CreatedAt:       s.now(),
	}, nil
}

func (s Service) Restore(ctx context.Context, input RestoreInput) (RestoreResult, error) {
	fetch, err := s.Fetch(ctx, FetchInput{
		Ref:             input.Ref,
		DestinationPath: input.DestinationPath,
		Overwrite:       input.Overwrite,
	})
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{
		FetchResult: fetch,
		RestoredAt:  s.now(),
	}, nil
}

func (s Service) RecordTombstone(ctx context.Context, input RecordTombstoneInput) (RecordTombstoneResult, error) {
	ref := strings.TrimSpace(input.Ref)
	if ref == "" {
		return RecordTombstoneResult{}, fmt.Errorf("storage ref is required")
	}
	if s.Catalog == nil {
		return RecordTombstoneResult{}, fmt.Errorf("storage retention catalog is not configured")
	}
	detail, err := s.Catalog.InspectEntry(ctx, ref)
	if err != nil {
		return RecordTombstoneResult{}, err
	}
	tombstone, err := s.Catalog.CreateTombstone(ctx, storagecatalog.TombstoneInput{
		StorageEntryID: detail.Entry.StorageEntryID,
		TombstoneKind:  input.TombstoneKind,
		Reason:         input.Reason,
		CreatedBy:      input.CreatedBy,
		MarkEntry:      input.MarkEntry,
		Metadata:       tombstoneMetadata(input, detail.Entry),
	})
	if err != nil {
		return RecordTombstoneResult{}, err
	}
	return RecordTombstoneResult{Entry: detail.Entry, Tombstone: tombstone, CreatedAt: s.now()}, nil
}

func (s Service) Status(ctx context.Context) (storagecatalog.RetentionStatus, error) {
	if s.Catalog == nil {
		return storagecatalog.RetentionStatus{}, fmt.Errorf("storage retention catalog is not configured")
	}
	return s.Catalog.RetentionStatus(ctx)
}

func (s Service) protectionFromDetail(ctx context.Context, detail storagecatalog.EntryDetail, input storagecatalog.MainDocumentProtectionInput) storagecatalog.MainDocumentProtectionStatus {
	entry := detail.Entry
	checkedAt := s.now()
	relativePath := normalizeMainDocumentPath(input.RelativePath, entry.CurrentViewPath)
	if relativePath == "" {
		relativePath = entry.LogicalPath
	}
	status := storagecatalog.MainDocumentProtectionStatus{
		AcceptedByCatalog: true,
		StorageEntryID:    entry.StorageEntryID,
		ViewPath:          entry.CurrentViewPath,
		LogicalPath:       entry.LogicalPath,
		RelativePath:      relativePath,
		AvailabilityState: entry.AvailabilityState,
		RetentionState:    entry.RetentionState,
		CheckedAt:         checkedAt,
	}
	if status.ViewPath == "" && relativePath != "" {
		status.ViewPath = storagecatalog.MainDocumentViewPath(relativePath)
	}

	currentRef := newestRefByKind(detail.PhysicalRefs, storagecatalog.PhysicalRefKindLocalPath)
	if currentRef != nil {
		if sourcePath, ok := localPathFromURI(currentRef.URI); ok {
			status.CurrentSourcePath = sourcePath
			if info, err := os.Stat(sourcePath); err == nil && info.Mode().IsRegular() {
				status.CurrentSourcePresent = true
			}
		}
	}

	retentionRef := newestRefByKind(detail.PhysicalRefs, storagecatalog.PhysicalRefKindRetentionPayload)
	if retentionRef != nil {
		if retainedPath, ok := localPathFromURI(retentionRef.URI); ok {
			status.RetainedPayloadPath = retainedPath
			if verified, verifiedAt := verifyRetainedPayload(entry, *retentionRef, retainedPath); verified {
				status.RetainedPayloadPresent = true
				status.RetainedPayloadVerified = true
				status.RetainedPayloadVerifiedAt = &verifiedAt
			} else if exists(retainedPath) {
				status.RetainedPayloadPresent = true
			}
		}
	}

	coverage := input.CloudCoverage
	if !coverage.Confirmed && s.MainDocumentCloudCoverage != nil {
		if loaded, err := s.MainDocumentCloudCoverage(ctx); err == nil {
			coverage = loaded
		}
	}
	if coverage.Confirmed && coverage.CoversStorageRetention {
		status.CloudBackupConfirmed = true
		status.CloudBackupRef = coverage.Ref
	}

	switch {
	case !status.AcceptedByCatalog:
		status.UnsafeReason = "no accepted catalog entry matched this main Documents path"
	case !status.RetainedPayloadPresent:
		status.UnsafeReason = "no retained payload exists for this main document"
	case !status.RetainedPayloadVerified:
		status.UnsafeReason = "retained payload exists but checksum verification failed"
	case !status.CloudBackupConfirmed:
		status.UnsafeReason = "retained payload has not been confirmed in a verified cloud snapshot"
	case coverage.VerifiedAt == nil || status.RetainedPayloadVerifiedAt == nil:
		status.UnsafeReason = "cloud or retention verification time is unknown"
	case coverage.VerifiedAt.Before(*status.RetainedPayloadVerifiedAt):
		status.UnsafeReason = "latest verified cloud snapshot predates retained payload verification"
	default:
		status.SafeToDeleteSource = true
	}
	return status
}

func decideSafeToDelete(result SafeToDeleteResult, detail storagecatalog.EntryDetail) SafeToDeleteResult {
	entry := detail.Entry
	fidelity := storagefidelity.EvaluateEntry(entry, detail.PhysicalRefs, result.CheckedAt)
	result.Warnings = appendMissingStrings(result.Warnings, findingSummaries(storagefidelity.WarningFindings(fidelity)))
	if blockers := storagefidelity.BlockingFindings(fidelity); len(blockers) > 0 {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, findingSummaries(blockers)...)
		return result
	}
	_, readableErr := chooseReadableSourceRef(detail)
	hasReadableRef := readableErr == nil || zeroByteRetainedByCatalog(entry, fidelity)
	checksumOK := refsDoNotContradictChecksum(entry, detail.PhysicalRefs)

	if entry.ProcessingState == storagecatalog.ProcessingStateFailed || entry.AvailabilityState == storagecatalog.AvailabilityStateFailed {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, "catalog entry is failed")
		return result
	}
	if entry.StorageClass == storagecatalog.StorageClassPrivateBackup && entry.ProcessingState == storagecatalog.ProcessingStateMetadataOnly {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, "watched-root backup is metadata-only and has no retained file payload")
		return result
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateExcluded {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, "file was excluded and has not been retained")
		return result
	}
	if entry.AvailabilityState == storagecatalog.AvailabilityStatePending || entry.AvailabilityState == storagecatalog.AvailabilityStateDiscovered {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, "file is not accepted by main yet")
		return result
	}
	if !checksumOK {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, "available physical ref checksum does not match catalog checksum")
		return result
	}
	if entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned || entry.AvailabilityState == storagecatalog.AvailabilityStateDeleted {
		result.Decision = DecisionSafe
		result.Safe = true
		result.Reasons = append(result.Reasons, "source deletion has already been recorded as a tombstone")
		return result
	}
	if !hasReadableRef {
		result.Decision = DecisionNotSafe
		result.Blockers = append(result.Blockers, "no available retained physical ref was found")
		return result
	}

	switch entry.StorageClass {
	case storagecatalog.StorageClassDropzoneCustody:
		result.Decision = DecisionSafe
		result.Safe = true
		result.Reasons = append(result.Reasons, "Dropzone transfer is accepted into main custody")
	case storagecatalog.StorageClassPrivateBackup:
		result.Decision = DecisionSafe
		result.Safe = true
		result.Reasons = append(result.Reasons, "watched-root backup has an accepted retained copy on main")
	case storagecatalog.StorageClassMainDocument:
		switch entry.RetentionState {
		case storagecatalog.RetentionStateSnapshot, storagecatalog.RetentionStateRetained:
			result.Decision = DecisionSafe
			result.Safe = true
			result.Reasons = append(result.Reasons, "main document has a retained snapshot")
		case storagecatalog.RetentionStatePending:
			result.Decision = DecisionNotSafe
			result.Blockers = append(result.Blockers, "main document snapshot is still pending")
		default:
			result.Decision = DecisionUnknown
			result.Blockers = append(result.Blockers, "main document retention state is not retained")
		}
	case storagecatalog.StorageClassArchiveEntry:
		result.Decision = DecisionSafe
		result.Safe = true
		result.Reasons = append(result.Reasons, "archive entry is available on main")
	default:
		result.Decision = DecisionUnknown
		result.Blockers = append(result.Blockers, "storage class is not covered by safe-to-delete policy")
	}
	if result.Safe && len(result.Warnings) > 0 {
		result.Decision = DecisionPartiallySafe
		result.Reasons = append(result.Reasons, "source payload is retained, but safe view has fidelity warnings that may need explicit acceptance")
	}
	return result
}

func zeroByteRetainedByCatalog(entry storagecatalog.Entry, fidelity storagefidelity.Evaluation) bool {
	return entry.SizeBytes != nil && *entry.SizeBytes == 0 && fidelity.PayloadRetained
}

func findingSummaries(findings []storagefidelity.Finding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		if finding.Summary != "" {
			out = append(out, finding.Kind+": "+finding.Summary)
		} else {
			out = append(out, finding.Kind)
		}
	}
	return out
}

func appendMissingStrings(values []string, additions []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value == "" || seen[value] {
			continue
		}
		values = append(values, value)
		seen[value] = true
	}
	return values
}

func chooseReadableSourceRef(detail storagecatalog.EntryDetail) (*storagecatalog.PhysicalRef, error) {
	refs := storagecatalog.ResolvePhysicalRefs(detail.PhysicalRefs, storagecatalog.ResolvePhysicalRefOptions{LocalOnly: true})
	if len(refs) > 0 {
		return &refs[0], nil
	}
	return nil, fmt.Errorf("no readable retained source found for storage entry %s", detail.Entry.StorageEntryID)
}

func copySourceToDestination(ctx context.Context, ref storagecatalog.PhysicalRef, destination string, overwrite bool) (int64, error) {
	destination = filepath.Clean(destination)
	if exists(destination) && !overwrite {
		return 0, fmt.Errorf("destination already exists: %s", destination)
	}
	sourcePath, ok := localPathFromURI(ref.URI)
	if !ok {
		return 0, fmt.Errorf("source ref is not a local file: %s", ref.URI)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, fmt.Errorf("create destination parent: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".loom-fetch-*")
	if err != nil {
		return 0, fmt.Errorf("create temporary destination: %w", err)
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	var bytesWritten int64
	switch ref.RefKind {
	case storagecatalog.PhysicalRefKindBackupArtifact:
		bytesWritten, err = copyTarMember(ctx, sourcePath, backupArtifactContentMember(ref), tmp)
	default:
		bytesWritten, err = copyRegularFile(ctx, sourcePath, tmp)
	}
	if closeErr := tmp.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	backupPath, restoreBackup, err := prepareDestinationForReplace(destination, overwrite)
	if err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		if restoreBackup {
			if restoreErr := os.Rename(backupPath, destination); restoreErr != nil {
				return 0, fmt.Errorf("move fetched file into place: %w; restore destination backup: %v", err, restoreErr)
			}
		}
		return 0, fmt.Errorf("move fetched file into place: %w", err)
	}
	if restoreBackup {
		if err := os.Remove(backupPath); err != nil {
			return 0, fmt.Errorf("remove destination backup: %w", err)
		}
	}
	removeTmp = false
	return bytesWritten, nil
}

func prepareDestinationForReplace(destination string, overwrite bool) (string, bool, error) {
	info, statErr := os.Lstat(destination)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect destination: %w", statErr)
	}
	if !overwrite {
		return "", false, fmt.Errorf("destination already exists: %s", destination)
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("destination is a directory: %s", destination)
	}
	backup, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".loom-replace-backup-*")
	if err != nil {
		return "", false, fmt.Errorf("reserve destination backup: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", false, fmt.Errorf("close destination backup placeholder: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return "", false, fmt.Errorf("remove destination backup placeholder: %w", err)
	}
	if err := os.Rename(destination, backupPath); err != nil {
		return "", false, fmt.Errorf("backup destination before replace: %w", err)
	}
	return backupPath, true, nil
}

func copyRegularFile(ctx context.Context, sourcePath string, destination *os.File) (int64, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("open retained source: %w", err)
	}
	defer source.Close()
	return copyWithContext(ctx, destination, source)
}

func copyTarMember(ctx context.Context, tarPath, member string, destination *os.File) (int64, error) {
	file, err := os.Open(tarPath)
	if err != nil {
		return 0, fmt.Errorf("open retained backup artifact: %w", err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("backup artifact member %q was not found", member)
		}
		if err != nil {
			return 0, fmt.Errorf("read backup artifact: %w", err)
		}
		if header == nil || header.Name != member {
			continue
		}
		if header.FileInfo().IsDir() {
			return 0, fmt.Errorf("backup artifact member %q is a directory", member)
		}
		return copyWithContext(ctx, destination, reader)
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
				return written, fmt.Errorf("write destination: %w", writeErr)
			}
			if m != n {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("read retained source: %w", readErr)
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

func refsDoNotContradictChecksum(entry storagecatalog.Entry, refs []storagecatalog.PhysicalRef) bool {
	if strings.TrimSpace(entry.ChecksumHex) == "" || strings.TrimSpace(entry.ChecksumAlgorithm) == "" {
		return true
	}
	want := strings.ToLower(strings.TrimSpace(entry.ChecksumAlgorithm + ":" + entry.ChecksumHex))
	for _, ref := range refs {
		if ref.Status != "" && ref.Status != storagecatalog.PhysicalRefStatusAvailable {
			continue
		}
		got := strings.ToLower(strings.TrimSpace(ref.ContentAddress))
		if got == "" {
			continue
		}
		if got != want {
			return false
		}
	}
	return true
}

func newestRefByKind(refs []storagecatalog.PhysicalRef, kind string) *storagecatalog.PhysicalRef {
	for i := range refs {
		ref := refs[i]
		if ref.RefKind != kind {
			continue
		}
		if ref.Status != "" && ref.Status != storagecatalog.PhysicalRefStatusAvailable {
			continue
		}
		clone := ref
		return &clone
	}
	return nil
}

func normalizeMainDocumentPath(relativePath, viewPath string) string {
	value := strings.Trim(strings.TrimSpace(relativePath), "/")
	if value == "" {
		value = strings.Trim(strings.TrimSpace(viewPath), "/")
	}
	value = strings.TrimPrefix(value, "main/Documents/")
	value = strings.TrimPrefix(value, "Documents/")
	return strings.Trim(value, "/")
}

func verifyRetainedPayload(entry storagecatalog.Entry, ref storagecatalog.PhysicalRef, retainedPath string) (bool, time.Time) {
	info, err := os.Stat(retainedPath)
	if err != nil || !info.Mode().IsRegular() {
		return false, time.Time{}
	}
	if entry.SizeBytes != nil && *entry.SizeBytes != info.Size() {
		return false, time.Time{}
	}
	if strings.TrimSpace(entry.ChecksumHex) != "" && strings.EqualFold(strings.TrimSpace(entry.ChecksumAlgorithm), "sha256") {
		got, err := hashSHA256(retainedPath)
		if err != nil || !strings.EqualFold(got, strings.TrimSpace(entry.ChecksumHex)) {
			return false, time.Time{}
		}
	}
	if strings.TrimSpace(ref.ContentAddress) != "" && strings.TrimSpace(entry.ChecksumHex) != "" {
		want := strings.ToLower(strings.TrimSpace(entry.ChecksumAlgorithm + ":" + entry.ChecksumHex))
		if strings.ToLower(strings.TrimSpace(ref.ContentAddress)) != want {
			return false, time.Time{}
		}
	}
	if ts := retentionVerifiedAt(ref.Metadata); !ts.IsZero() {
		return true, ts
	}
	if !ref.UpdatedAt.IsZero() {
		return true, ref.UpdatedAt.UTC()
	}
	if !ref.CreatedAt.IsZero() {
		return true, ref.CreatedAt.UTC()
	}
	return true, time.Now().UTC()
}

func hashSHA256(pathValue string) (string, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func retentionVerifiedAt(metadata json.RawMessage) time.Time {
	var payload map[string]any
	if len(metadata) == 0 || json.Unmarshal(metadata, &payload) != nil {
		return time.Time{}
	}
	value, _ := payload["retention_verified_at"].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
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

func exists(pathValue string) bool {
	_, err := os.Lstat(pathValue)
	return err == nil
}

func tombstoneMetadata(input RecordTombstoneInput, entry storagecatalog.Entry) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version":   "storage.retention_tombstone.v0.6",
		"source":           "storageretention.service",
		"input_ref":        input.Ref,
		"storage_entry_id": entry.StorageEntryID,
		"logical_path":     entry.LogicalPath,
		"view_path":        entry.CurrentViewPath,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
