package lane

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	housekeepingStatusNoop      = "noop"
	housekeepingStatusSucceeded = "succeeded"
)

type housekeepingBatch struct {
	record BatchRecord
}

type recoveryPathClass struct {
	protected           bool
	successfulGrace     bool
	redundantSuccessful bool
	expiresAt           *time.Time
}

func Housekeep(input HousekeepingInput) (HousekeepingResult, error) {
	now := time.Now().UTC()
	if input.Now != nil {
		now = input.Now().UTC()
	}
	result := HousekeepingResult{Status: housekeepingStatusNoop, InspectedAt: now}
	boxRootPath := strings.TrimSpace(input.RootPath)
	if boxRootPath == "" {
		return result, fmt.Errorf("root path is required")
	}
	stateRel := filepath.ToSlash(strings.TrimSpace(input.StateRelPath))
	if stateRel == "" {
		stateRel = DefaultStateRelPath
	}
	statePath, err := normalizeLaneStatePath(boxRootPath, stateRel, input.StatePath)
	if err != nil {
		return result, err
	}
	rootPath := filepath.Dir(statePath)
	stateRoot := filepath.Base(statePath)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return result, err
	}
	defer root.Close()
	if err := validateHousekeepingDirectoryChain(root, stateRoot); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	if err := validateOptionalHousekeepingDirectory(root, filepath.Join(stateRoot, "batches")); err != nil {
		return result, err
	}

	batches, err := loadHousekeepingBatches(statePath)
	if err != nil {
		return result, err
	}
	for index := range batches {
		record := &batches[index].record
		if !successfulCleanupQuarantineRecord(*record) {
			continue
		}
		changed := false
		if record.LocalCleanupQuarantineExpiresAt == nil {
			expiresAt := record.CompletedAt.UTC().Add(SuccessfulCleanupQuarantineGrace)
			record.LocalCleanupQuarantineExpiresAt = &expiresAt
			changed = true
		}
		if relativePath, ok := recordedCleanupQuarantineRelative(rootPath, stateRoot, *record); ok {
			if bytes, sizeErr := stateDirectoryBytes(filepath.Join(rootPath, relativePath)); sizeErr == nil && record.LocalCleanupQuarantineBytes != bytes {
				record.LocalCleanupQuarantineBytes = bytes
				changed = true
			}
		}
		if changed {
			if err := persistHousekeepingBatch(statePath, &batches[index], &result); err != nil {
				return result, err
			}
		}
	}

	for index := range batches {
		if !successfulSafetyArtifactRecord(rootPath, stateRoot, batches[index].record) {
			continue
		}
		if err := removeSuccessfulSafetyArtifact(root, rootPath, stateRoot, statePath, &batches[index], now, &result); err != nil {
			return result, err
		}
	}

	candidates := successfulCleanupQuarantineCandidates(root, rootPath, stateRoot, batches)
	currentSuccess, hasCurrentSuccess := currentCompletedSuccessfulBatch(batches, input.CurrentBatchID)
	if hasCurrentSuccess {
		currentBatchID := currentSuccess.record.BatchID
		for index := range batches {
			if batches[index].record.BatchID == currentBatchID {
				continue
			}
			if !containsHousekeepingBatch(candidates, batches[index].record.BatchID) {
				continue
			}
			if err := removeSuccessfulCleanupQuarantine(root, rootPath, stateRoot, statePath, &batches[index], now, CleanupRemovalReasonNewerSuccess, &result); err != nil {
				return result, err
			}
		}
		if containsHousekeepingBatch(candidates, currentBatchID) {
			for index := range batches {
				record := batches[index].record
				if record.BatchID != currentBatchID || record.LocalCleanupQuarantineExpiresAt == nil || now.Before(record.LocalCleanupQuarantineExpiresAt.UTC()) {
					continue
				}
				if err := removeSuccessfulCleanupQuarantine(root, rootPath, stateRoot, statePath, &batches[index], now, CleanupRemovalReasonGraceExpired, &result); err != nil {
					return result, err
				}
			}
		}
	}

	result.RemovedSafetyArtifactBatches = sortedUniqueStrings(result.RemovedSafetyArtifactBatches)
	result.RemovedCleanupQuarantineBatches = sortedUniqueStrings(result.RemovedCleanupQuarantineBatches)
	result.UpdatedBatchRecords = sortedUniqueStrings(result.UpdatedBatchRecords)
	if len(result.RemovedSafetyArtifactBatches) > 0 || len(result.RemovedCleanupQuarantineBatches) > 0 || len(result.UpdatedBatchRecords) > 0 {
		result.Status = housekeepingStatusSucceeded
	}
	accounting, accountingErr := InspectRecoveryStorageAt(statePath)
	result.RecoveryStorage = accounting
	if accountingErr != nil {
		return result, accountingErr
	}
	return result, nil
}

func successfulCleanupQuarantineRecord(record BatchRecord) bool {
	return record.Status == BatchStatusLocalCleanupDone && record.CompletedAt != nil && record.LocalCleanupQuarantinePath != "" && (record.LocalCleanupQuarantineState == CleanupQuarantineRetainedForRecovery || record.LocalCleanupQuarantineState == CleanupQuarantineRemovalPending)
}

func completedSuccessfulBatchRecord(record BatchRecord) bool {
	return record.CompletedAt != nil && (record.Status == BatchStatusLocalCleanupDone || record.Status == BatchStatusCataloged || record.Status == BatchStatusPublishedStorageView)
}

func currentCompletedSuccessfulBatch(batches []housekeepingBatch, requestedBatchID string) (housekeepingBatch, bool) {
	if requestedBatchID != "" {
		for _, batch := range batches {
			if batch.record.BatchID == requestedBatchID && completedSuccessfulBatchRecord(batch.record) {
				return batch, true
			}
		}
	}
	var current housekeepingBatch
	found := false
	for _, batch := range batches {
		if !completedSuccessfulBatchRecord(batch.record) {
			continue
		}
		if !found || transferRecordAfter(batch.record, current.record) {
			current = batch
			found = true
		}
	}
	return current, found
}

func successfulSafetyArtifactRecord(rootPath, stateRoot string, record BatchRecord) bool {
	if !completedSuccessfulBatchRecord(record) {
		return false
	}
	if record.LocalSafetyCleanupState == SafetyArtifactRemovedAfterSuccess {
		return false
	}
	if _, ok := recordedSafetyArtifactRelative(rootPath, stateRoot, record); !ok {
		return false
	}
	if record.Status == BatchStatusCataloged || record.Status == BatchStatusPublishedStorageView {
		return record.LocalCleanupQuarantinePath == ""
	}
	if record.LocalCleanupQuarantinePath == "" {
		return record.LocalCleanupQuarantineState == CleanupQuarantineRestored
	}
	if record.LocalCleanupQuarantineState == CleanupQuarantineRetainedForRecovery || record.LocalCleanupQuarantineState == CleanupQuarantineRemovalPending || record.LocalCleanupQuarantineState == CleanupQuarantineRemovedAfterGrace {
		return true
	}
	return false
}

func successfulCleanupQuarantineCandidates(root *os.Root, rootPath, stateRoot string, batches []housekeepingBatch) []housekeepingBatch {
	var candidates []housekeepingBatch
	for _, batch := range batches {
		if !successfulCleanupQuarantineRecord(batch.record) {
			continue
		}
		relativePath, ok := recordedCleanupQuarantineRelative(rootPath, stateRoot, batch.record)
		if !ok {
			continue
		}
		if batch.record.LocalCleanupQuarantineState == CleanupQuarantineRemovalPending {
			candidates = append(candidates, batch)
			continue
		}
		info, err := root.Lstat(relativePath)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			candidates = append(candidates, batch)
		}
	}
	return candidates
}

func removeSuccessfulSafetyArtifact(root *os.Root, rootPath, stateRoot, statePath string, batch *housekeepingBatch, now time.Time, result *HousekeepingResult) error {
	record := &batch.record
	removalReason := SafetyRemovalReasonMainCataloged
	if record.Status == BatchStatusPublishedStorageView || record.Metrics.ExportRefreshDurationMS > 0 {
		removalReason = SafetyRemovalReasonMainPublished
	}
	relativePath, ok := recordedSafetyArtifactRelative(rootPath, stateRoot, *record)
	if !ok {
		return nil
	}
	if record.LocalSafetyCleanupState != SafetyArtifactRemovalPending {
		record.LocalSafetyCleanupState = SafetyArtifactRemovalPending
		record.LocalSafetyRemovalReason = removalReason
		if record.SelectedTransport == TransportModeBundleSeed {
			record.BundleCleanupState = BundleCleanupRemovalPending
		}
		if err := persistHousekeepingBatch(statePath, batch, result); err != nil {
			return err
		}
	}
	if err := removeHousekeepingDirectory(root, relativePath); err != nil {
		return fmt.Errorf("remove successful Lane safety artifact for %s: %w", record.BatchID, err)
	}
	removedAt := now
	record.LocalSafetyCleanupState = SafetyArtifactRemovedAfterSuccess
	record.LocalSafetyRemovedAt = &removedAt
	record.LocalSafetyRemovalReason = removalReason
	if record.SelectedTransport == TransportModeBundleSeed {
		record.BundleCleanupState = BundleCleanupRemovedAfterSuccess
	}
	if err := persistHousekeepingBatch(statePath, batch, result); err != nil {
		return err
	}
	result.RemovedSafetyArtifactBatches = append(result.RemovedSafetyArtifactBatches, record.BatchID)
	return nil
}

func removeSuccessfulCleanupQuarantine(root *os.Root, rootPath, stateRoot, statePath string, batch *housekeepingBatch, now time.Time, reason string, result *HousekeepingResult) error {
	record := &batch.record
	relativePath, ok := recordedCleanupQuarantineRelative(rootPath, stateRoot, *record)
	if !ok {
		return nil
	}
	if record.LocalCleanupQuarantineState != CleanupQuarantineRemovalPending {
		record.LocalCleanupQuarantineState = CleanupQuarantineRemovalPending
		record.LocalCleanupQuarantineRemovalReason = reason
		if err := persistHousekeepingBatch(statePath, batch, result); err != nil {
			return err
		}
	} else if record.LocalCleanupQuarantineRemovalReason != "" {
		reason = record.LocalCleanupQuarantineRemovalReason
	}
	if err := removeHousekeepingDirectory(root, relativePath); err != nil {
		return fmt.Errorf("remove successful Lane cleanup quarantine for %s: %w", record.BatchID, err)
	}
	removedAt := now
	record.LocalCleanupQuarantineState = CleanupQuarantineRemovedAfterGrace
	record.LocalCleanupQuarantineRemovedAt = &removedAt
	record.LocalCleanupQuarantineRemovalReason = reason
	if err := persistHousekeepingBatch(statePath, batch, result); err != nil {
		return err
	}
	result.RemovedCleanupQuarantineBatches = append(result.RemovedCleanupQuarantineBatches, record.BatchID)
	return nil
}

func persistHousekeepingBatch(statePath string, batch *housekeepingBatch, result *HousekeepingResult) error {
	if err := writeBatchRecord(statePath, batch.record); err != nil {
		return fmt.Errorf("persist Lane housekeeping record %s: %w", batch.record.BatchID, err)
	}
	result.UpdatedBatchRecords = append(result.UpdatedBatchRecords, batch.record.BatchID)
	return nil
}

func loadHousekeepingBatches(statePath string) ([]housekeepingBatch, error) {
	batchDir := filepath.Join(statePath, "batches")
	entries, err := os.ReadDir(batchDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var batches []housekeepingBatch
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		pathValue := filepath.Join(batchDir, entry.Name())
		record, err := readBatchRecord(pathValue)
		if err != nil {
			return nil, fmt.Errorf("read Lane housekeeping record %s: %w", pathValue, err)
		}
		if safeToken(record.BatchID, "") == "" || safeToken(record.BatchID, "") != record.BatchID || entry.Name() != record.BatchID+".json" {
			return nil, fmt.Errorf("Lane housekeeping record %s has unsafe batch identity %q", pathValue, record.BatchID)
		}
		batches = append(batches, housekeepingBatch{record: record})
	}
	return batches, nil
}

func recordedCleanupQuarantineRelative(rootPath, stateRoot string, record BatchRecord) (string, bool) {
	return recordedStateDirectoryRelative(rootPath, stateRoot, "cleanup", record.BatchID, record.LocalCleanupQuarantinePath)
}

func recordedSafetyArtifactRelative(rootPath, stateRoot string, record BatchRecord) (string, bool) {
	category := "sent"
	recordedPath := record.LocalSafetyPath
	if record.SelectedTransport == TransportModeBundleSeed {
		category = "bundles"
		recordedPath = firstNonEmptyString(record.BundleArtifactPath, record.LocalSafetyPath)
	}
	return recordedStateDirectoryRelative(rootPath, stateRoot, category, record.BatchID, recordedPath)
}

func recordedStateDirectoryRelative(rootPath, stateRoot, category, batchID, recordedPath string) (string, bool) {
	if safeToken(batchID, "") == "" || safeToken(batchID, "") != batchID || strings.TrimSpace(recordedPath) == "" {
		return "", false
	}
	relativePath := filepath.Join(stateRoot, category, batchID)
	expectedPath := filepath.Join(rootPath, relativePath)
	if filepath.Clean(recordedPath) != filepath.Clean(expectedPath) {
		return "", false
	}
	return relativePath, true
}

func removeHousekeepingDirectory(root *os.Root, relativePath string) error {
	info, err := root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Lane recovery path %s is not a no-follow directory", relativePath)
	}
	if err := validateHousekeepingDirectoryChain(root, filepath.Dir(relativePath)); err != nil {
		return err
	}
	info, err = root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Lane recovery path %s is not a no-follow directory", relativePath)
	}
	return root.RemoveAll(relativePath)
}

func validateHousekeepingDirectoryChain(root *os.Root, relativePath string) error {
	cleaned, err := safeCleanupRelativeRoot(filepath.ToSlash(relativePath), "Lane recovery")
	if err != nil {
		return err
	}
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(cleaned), "/") {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Lane recovery parent %s is not a no-follow directory", current)
		}
	}
	return nil
}

func validateOptionalHousekeepingDirectory(root *os.Root, relativePath string) error {
	info, err := root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Lane recovery path %s is not a no-follow directory", relativePath)
	}
	return nil
}

func containsHousekeepingBatch(batches []housekeepingBatch, batchID string) bool {
	for _, batch := range batches {
		if batch.record.BatchID == batchID {
			return true
		}
	}
	return false
}

func InspectRecoveryStorage(rootPath, stateRelPath string) (RecoveryStorage, error) {
	if strings.TrimSpace(stateRelPath) == "" {
		stateRelPath = DefaultStateRelPath
	}
	statePath, err := normalizeLaneStatePath(rootPath, stateRelPath, "")
	if err != nil {
		return RecoveryStorage{}, err
	}
	return InspectRecoveryStorageAt(statePath)
}

func InspectRecoveryStorageAt(statePath string) (RecoveryStorage, error) {
	if !filepath.IsAbs(strings.TrimSpace(statePath)) {
		return RecoveryStorage{}, fmt.Errorf("Lane state path must be absolute")
	}
	statePath = filepath.Clean(statePath)
	rootPath := filepath.Dir(statePath)
	stateRoot := filepath.Base(statePath)
	root, rootErr := os.OpenRoot(rootPath)
	if errors.Is(rootErr, os.ErrNotExist) {
		return RecoveryStorage{}, nil
	}
	if rootErr != nil {
		return RecoveryStorage{}, rootErr
	}
	defer root.Close()
	if err := validateHousekeepingDirectoryChain(root, stateRoot); errors.Is(err, os.ErrNotExist) {
		return RecoveryStorage{}, nil
	} else if err != nil {
		return RecoveryStorage{}, err
	}
	if err := validateOptionalHousekeepingDirectory(root, filepath.Join(stateRoot, "batches")); err != nil {
		return RecoveryStorage{}, err
	}
	classes := map[string]recoveryPathClass{}
	var scanErrors []string
	batches, err := loadHousekeepingBatches(statePath)
	if err != nil {
		scanErrors = append(scanErrors, err.Error())
	}
	for _, batch := range batches {
		record := batch.record
		if pathValue, ok := recordedCleanupQuarantineRelative(rootPath, stateRoot, record); ok {
			class := recoveryPathClass{protected: true}
			if successfulCleanupQuarantineRecord(record) {
				class.protected = false
				class.successfulGrace = true
				class.expiresAt = record.LocalCleanupQuarantineExpiresAt
			}
			mergeRecoveryPathClass(classes, pathValue, class)
		}
		if pathValue, ok := recordedSafetyArtifactRelative(rootPath, stateRoot, record); ok && record.LocalSafetyCleanupState != SafetyArtifactRemovedAfterSuccess {
			class := recoveryPathClass{protected: true}
			if successfulSafetyArtifactRecord(rootPath, stateRoot, record) {
				class.protected = false
				class.redundantSuccessful = true
			}
			mergeRecoveryPathClass(classes, pathValue, class)
		}
	}

	accounting := RecoveryStorage{}
	for _, category := range []string{"cleanup", "sent", "bundles"} {
		categoryPath := filepath.Join(statePath, category)
		if validationErr := validateOptionalHousekeepingDirectory(root, filepath.Join(stateRoot, category)); validationErr != nil {
			scanErrors = append(scanErrors, validationErr.Error())
			continue
		}
		entries, readErr := os.ReadDir(categoryPath)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			scanErrors = append(scanErrors, readErr.Error())
			continue
		}
		for _, entry := range entries {
			absolutePath := filepath.Join(categoryPath, entry.Name())
			relativePath := filepath.Join(stateRoot, category, entry.Name())
			bytes, sizeErr := stateDirectoryBytes(absolutePath)
			if sizeErr != nil {
				scanErrors = append(scanErrors, sizeErr.Error())
			}
			accounting.RetainedBytes += bytes
			if category == "cleanup" {
				accounting.CleanupQuarantineBytes += bytes
			} else {
				accounting.TransportSafetyBytes += bytes
			}
			class, ok := classes[relativePath]
			if !ok {
				accounting.UntrackedBytes += bytes
				accounting.UntrackedPathCount++
				accounting.ProtectedEvidenceBytes += bytes
				accounting.ProtectedEvidenceCount++
				continue
			}
			switch {
			case class.protected:
				accounting.ProtectedEvidenceBytes += bytes
				accounting.ProtectedEvidenceCount++
			case class.successfulGrace:
				accounting.SuccessfulGraceBytes += bytes
				accounting.SuccessfulQuarantineCount++
				if class.expiresAt != nil && (accounting.NextSuccessfulQuarantineExpiresAt == nil || class.expiresAt.Before(*accounting.NextSuccessfulQuarantineExpiresAt)) {
					expiresAt := class.expiresAt.UTC()
					accounting.NextSuccessfulQuarantineExpiresAt = &expiresAt
				}
			case class.redundantSuccessful:
				accounting.RedundantSuccessfulArtifactBytes += bytes
				accounting.RedundantSuccessfulArtifactCount++
			}
		}
	}
	if len(scanErrors) > 0 {
		return accounting, fmt.Errorf("inspect Lane recovery storage: %s", strings.Join(sortedUniqueStrings(scanErrors), "; "))
	}
	return accounting, nil
}

func mergeRecoveryPathClass(classes map[string]recoveryPathClass, pathValue string, class recoveryPathClass) {
	existing, ok := classes[pathValue]
	if !ok || class.protected || (!existing.protected && class.successfulGrace) {
		classes[pathValue] = class
	}
}

func stateDirectoryBytes(pathValue string) (int64, error) {
	info, err := os.Lstat(pathValue)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("Lane recovery path %s is a symlink", pathValue)
	}
	if info.Mode().IsRegular() {
		return info.Size(), nil
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("Lane recovery path %s has unsupported type %s", pathValue, info.Mode().Type())
	}
	var total int64
	err = filepath.WalkDir(pathValue, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == pathValue || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Lane recovery path %s contains a symlink", current)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Lane recovery path %s contains unsupported type %s", current, info.Mode().Type())
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
