package lane

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const PendingAttentionStaleAfter = 24 * time.Hour

func AcknowledgePendingItem(input AcknowledgePendingInput) (AttentionResult, error) {
	root, laneRel, statePath, rel, err := normalizePendingAttentionInput(input)
	if err != nil {
		return AttentionResult{}, err
	}
	now := attentionNow(input.Now)
	sshConfigCheck := input.SSHConfigCheck
	if sshConfigCheck == nil {
		sshConfigCheck = func(string) error { return nil }
	}
	status := BuildStatus(StatusInput{
		RootPath:       root,
		LaneRelPath:    laneRel,
		StatePath:      statePath,
		MainHost:       DefaultMainHost,
		MaxItems:       1000,
		Now:            func() time.Time { return now },
		LookupPath:     input.LookupPath,
		SSHConfigCheck: sshConfigCheck,
	})
	for _, item := range status.Items {
		if item.RelativePath != rel {
			continue
		}
		state := loadAttentionState(status.StatePath)
		if state.PendingItems == nil {
			state.PendingItems = map[string]PendingAttentionItem{}
		}
		fingerprint := pendingItemFingerprint(item)
		state.SchemaVersion = AttentionSchemaVersion
		state.UpdatedAt = now
		state.PendingItems[rel] = PendingAttentionItem{
			RelativePath: rel,
			Fingerprint:  fingerprint,
			Status:       AttentionStatusAcknowledged,
			Note:         strings.TrimSpace(input.Note),
			UpdatedAt:    now,
		}
		if err := writeAttentionState(status.StatePath, state); err != nil {
			return AttentionResult{}, err
		}
		modifiedAt := item.ModifiedAt
		return AttentionResult{
			TargetKind:      "lane_pending_item",
			TargetRef:       rel,
			Status:          StatePending,
			AttentionStatus: AttentionStatusAcknowledged,
			Note:            strings.TrimSpace(input.Note),
			UpdatedAt:       now,
			FileCount:       item.FileCount,
			DirCount:        item.DirCount,
			Bytes:           item.Bytes,
			ModifiedAt:      &modifiedAt,
		}, nil
	}
	return AttentionResult{}, fmt.Errorf("pending Lane item %q was not found", rel)
}

func AcknowledgeTransferAttention(input AcknowledgeTransferInput) (AttentionResult, error) {
	input.Status = AttentionStatusAcknowledged
	return updateTransferAttention(input)
}

func ArchiveTransferAttention(input AcknowledgeTransferInput) (AttentionResult, error) {
	input.Status = AttentionStatusArchived
	return updateTransferAttention(input)
}

func updateTransferAttention(input AcknowledgeTransferInput) (AttentionResult, error) {
	_, statePath, batchID, attentionStatus, err := normalizeTransferAttentionInput(input)
	if err != nil {
		return AttentionResult{}, err
	}
	recordPath := filepath.Join(statePath, "batches", batchID+".json")
	record, err := readBatchRecord(recordPath)
	if err != nil {
		return AttentionResult{}, fmt.Errorf("read Lane batch %s: %w", batchID, err)
	}
	if record.Status != BatchStatusFailed && record.Status != BatchStatusPromotionFailed && record.Status != BatchStatusAcceptedOnMain && record.Status != BatchStatusCatalogFailed && record.Status != BatchStatusSourceCleanupFailed && record.Status != BatchStatusObsoleteAcceptedMigrationRequired && record.Status != BatchStatusLocalCleanupWithheld {
		return AttentionResult{}, fmt.Errorf("Lane batch %s is %s; only incomplete, failed, obsolete-migration, or cleanup-withheld Lane transfer attention can be acknowledged or archived", batchID, record.Status)
	}
	now := attentionNow(input.Now)
	record.AttentionStatus = attentionStatus
	record.AttentionNote = strings.TrimSpace(input.Note)
	record.AttentionUpdatedAt = &now
	if err := writeBatchRecord(statePath, record); err != nil {
		return AttentionResult{}, err
	}
	return AttentionResult{
		TargetKind:      "lane_transfer",
		TargetRef:       batchID,
		Status:          record.Status,
		AttentionStatus: attentionStatus,
		Note:            strings.TrimSpace(input.Note),
		UpdatedAt:       now,
		FileCount:       record.FileCount,
		Bytes:           record.TotalBytes,
	}, nil
}

func normalizePendingAttentionInput(input AcknowledgePendingInput) (root, laneRel, statePath, rel string, err error) {
	root, err = normalizeLocalRoot(input.RootPath)
	if err != nil {
		return "", "", "", "", err
	}
	laneRel = filepath.ToSlash(strings.TrimSpace(input.LaneRelPath))
	if laneRel == "" {
		laneRel = DefaultLaneRelPath
	}
	stateRel := filepath.ToSlash(strings.TrimSpace(input.StateRelPath))
	if stateRel == "" {
		stateRel = DefaultStateRelPath
	}
	statePath, err = normalizeLaneStatePath(root, stateRel, input.StatePath)
	if err != nil {
		return "", "", "", "", err
	}
	rel, err = normalizePendingRelativePath(input.RelativePath)
	if err != nil {
		return "", "", "", "", err
	}
	return root, laneRel, statePath, rel, nil
}

func normalizeTransferAttentionInput(input AcknowledgeTransferInput) (root, statePath, batchID, attentionStatus string, err error) {
	root, err = normalizeLocalRoot(input.RootPath)
	if err != nil {
		return "", "", "", "", err
	}
	stateRel := filepath.ToSlash(strings.TrimSpace(input.StateRelPath))
	if stateRel == "" {
		stateRel = DefaultStateRelPath
	}
	statePath, err = normalizeLaneStatePath(root, stateRel, input.StatePath)
	if err != nil {
		return "", "", "", "", err
	}
	batchID = safeToken(strings.TrimSpace(input.BatchID), "batch")
	if batchID == "" {
		return "", "", "", "", fmt.Errorf("batch id is required")
	}
	attentionStatus = NormalizeAttentionStatus(input.Status)
	switch attentionStatus {
	case AttentionStatusAcknowledged, AttentionStatusArchived:
		return root, statePath, batchID, attentionStatus, nil
	default:
		return "", "", "", "", fmt.Errorf("unsupported Lane attention status %q", input.Status)
	}
}

func normalizeLocalRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("root path is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func normalizePendingRelativePath(value string) (string, error) {
	rel := filepath.ToSlash(strings.TrimSpace(value))
	if rel == "" {
		return "", fmt.Errorf("pending Lane relative path is required")
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("pending Lane relative path must not be absolute")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("pending Lane relative path must stay inside LOOM Lane")
	}
	return cleaned, nil
}

func NormalizeAttentionStatus(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", AttentionStatusActive:
		return AttentionStatusActive
	case AttentionStatusAcknowledged, "ack":
		return AttentionStatusAcknowledged
	case AttentionStatusArchived, "archive":
		return AttentionStatusArchived
	default:
		return strings.TrimSpace(strings.ToLower(value))
	}
}

func transferAttentionStatus(record BatchRecord) string {
	status := NormalizeAttentionStatus(record.AttentionStatus)
	if record.Status == BatchStatusFailed || record.Status == BatchStatusPromotionFailed || record.Status == BatchStatusAcceptedOnMain || record.Status == BatchStatusCatalogFailed || record.Status == BatchStatusSourceCleanupFailed || record.Status == BatchStatusObsoleteAcceptedMigrationRequired || record.Status == BatchStatusLocalCleanupWithheld {
		switch status {
		case AttentionStatusAcknowledged, AttentionStatusArchived:
			return status
		default:
			return AttentionStatusActive
		}
	}
	if status == AttentionStatusAcknowledged || status == AttentionStatusArchived {
		return status
	}
	return ""
}

func pendingItemFingerprint(item PendingItem) string {
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d",
		item.RelativePath,
		item.Kind,
		item.Bytes,
		item.FileCount,
		item.DirCount,
		item.ModifiedAt.UTC().UnixNano(),
	)
}

func applyPendingItemAttention(item *PendingItem, state AttentionState, now time.Time) {
	if item == nil {
		return
	}
	if !item.ModifiedAt.IsZero() {
		age := now.Sub(item.ModifiedAt.UTC())
		if age < 0 {
			age = 0
		}
		item.AgeSeconds = int64(age.Seconds())
	}
	item.Fingerprint = pendingItemFingerprint(*item)
	record, ok := state.PendingItems[item.RelativePath]
	if ok && record.Status == AttentionStatusAcknowledged && record.Fingerprint == item.Fingerprint {
		item.AttentionStatus = AttentionStatusAcknowledged
		item.AttentionReason = "Pending item was acknowledged and will stay visible until the user sends or moves it."
		item.NextActions = pendingItemSafeActions(*item, true)
		return
	}
	item.AttentionStatus = AttentionStatusActive
	item.AttentionReason = "Pending item is still in LOOM Lane; choose send, inspect, or acknowledge without deleting it."
	item.NextActions = pendingItemSafeActions(*item, false)
}

func pendingItemSafeActions(item PendingItem, acknowledged bool) []SafeAction {
	rel := shellQuote(item.RelativePath)
	actions := []SafeAction{
		{Key: "inspect", Label: "Inspect item", Description: "Review path, age, size, and fidelity before acting.", Command: "loom lane status --include-fidelity", Risk: "inspect"},
		{Key: "send", Label: "Send explicitly", Description: "Transfer all pending Lane contents to main. This remains an explicit sensitive action.", Command: "loom lane send", Risk: "sensitive"},
	}
	if !acknowledged {
		actions = append(actions, SafeAction{
			Key:         "acknowledge_pending",
			Label:       "Acknowledge without deleting",
			Description: "Stop treating this current file state as active Lane attention. The file remains in LOOM Lane.",
			Command:     "loom lane acknowledge-pending " + rel,
			Risk:        "safe_run",
		})
	}
	return actions
}

func transferSafeActions(summary TransferSummary) []SafeAction {
	description := "Review the Lane batch record and command output."
	if summary.Status == BatchStatusTransferred {
		description = "Review the bundled batch that reached main staging but did not finish main acceptance."
	} else if summary.Status == BatchStatusPromotionFailed {
		description = "Review the staged batch and imports promotion failure before resuming the same batch identity."
	} else if summary.Status == BatchStatusAcceptedOnMain {
		description = "Review the promoted imports batch and resume its idempotent catalog registration."
	} else if summary.Status == BatchStatusCatalogFailed {
		description = "Review the committed imports batch and resume its idempotent catalog registration."
	} else if summary.Status == BatchStatusSourceCleanupFailed {
		description = "Review the cataloged imports batch and retry removal of transport-only staging."
	} else if summary.Status == BatchStatusObsoleteAcceptedMigrationRequired {
		description = "The batch still uses obsolete lane/accepted custody and requires the explicit historical migration plan."
	} else if summary.Status == BatchStatusLocalCleanupWithheld {
		description = "Review the promoted main copy and the changed local Lane source that caused cleanup to be withheld."
	}
	actions := []SafeAction{
		{Key: "inspect", Label: "Inspect transfer", Description: description, Command: "loom lane status --include-fidelity", Risk: "inspect"},
	}
	if bundleResumeEligibleStatus(summary.Status) && summary.SelectedTransport == TransportModeBundleSeed && summary.BundleArtifactPath != "" {
		description := "Reverify the retained local bundle artifact and resume its transfer without rebuilding it."
		if summary.AllowCrossDevicePromotion {
			description += " The same batch reuses its durably recorded explicit cross-filesystem promotion authorization."
		}
		actions = append(actions, SafeAction{Key: "resume_bundle", Label: "Resume verified bundle transfer", Description: description, Command: "loom lane send --bundle --resume", Risk: "sensitive"})
	}
	if fileTreeResumeEligibleStatus(summary.Status) && !laneBatchHasMainCustody(summary.Status) && summary.SelectedTransport == TransportModeFileTree {
		description := "Resume the same reviewed batch identity and refuse changed source inventory."
		if summary.AllowCrossDevicePromotion {
			description += " The same batch reuses its durably recorded explicit cross-filesystem promotion authorization."
		}
		actions = append(actions, SafeAction{Key: "resume_file_tree", Label: "Resume file-tree transfer", Description: description, Command: "loom lane send --no-bundle --resume", Risk: "sensitive"})
	}
	if summary.Status == BatchStatusPromotionFailed || summary.Status == BatchStatusAcceptedOnMain || summary.Status == BatchStatusCatalogFailed || summary.Status == BatchStatusSourceCleanupFailed {
		repairDescription := "Retry idempotent imports verification, catalog registration, and transport staging cleanup."
		switch summary.Status {
		case BatchStatusAcceptedOnMain, BatchStatusCatalogFailed:
			repairDescription = "Retry idempotent catalog registration for the already promoted imports batch, then clean transport staging."
		case BatchStatusSourceCleanupFailed:
			repairDescription = "Retry idempotent transport-staging cleanup, then honor the original keep-local or safe local-quarantine policy; completed local-cleanup evidence is never repeated."
		}
		if summary.AllowCrossDevicePromotion && (summary.Status == BatchStatusPromotionFailed || summary.Status == BatchStatusAcceptedOnMain || summary.Status == BatchStatusCatalogFailed) {
			repairDescription += " The same batch reuses its durably recorded explicit cross-filesystem promotion authorization."
		}
		actions = append(actions, SafeAction{Key: "repair_main_custody", Label: "Repair main custody", Description: repairDescription, Command: "loom lane repair " + shellQuote(summary.BatchID), Risk: "sensitive"})
	}
	if laneTransferFailureNeedsAttention(summary.Status) && summary.AttentionStatus == AttentionStatusActive {
		batch := shellQuote(summary.BatchID)
		actions = append(actions,
			SafeAction{Key: "acknowledge_transfer", Label: "Acknowledge transfer attention", Description: "Keep the incomplete or failed transfer history but clear active attention.", Command: "loom lane acknowledge-transfer " + batch, Risk: "safe_run"},
			SafeAction{Key: "archive_transfer", Label: "Archive transfer attention", Description: "Keep the incomplete or failed batch record as audit history and hide it from active attention.", Command: "loom lane archive-transfer " + batch, Risk: "safe_run"},
		)
	}
	if summary.Status == BatchStatusLocalCleanupWithheld && summary.AttentionStatus == AttentionStatusActive {
		batch := shellQuote(summary.BatchID)
		if summary.SelectedTransport == TransportModeBundleSeed {
			actions = append(actions, SafeAction{Key: "repair_transport_cleanup", Label: "Repair transport cleanup", Description: "Retry idempotent transport-staging cleanup while preserving promoted and cataloged custody and the changed local source.", Command: "loom lane repair " + batch, Risk: "sensitive"})
		}
		actions = append(actions,
			SafeAction{Key: "send_changed_source", Label: "Send changed Lane source", Description: "Create a new custody batch for the current changed source; the already accepted copy remains on main.", Command: "loom lane send", Risk: "sensitive"},
			SafeAction{Key: "acknowledge_transfer", Label: "Acknowledge cleanup attention", Description: "Keep the accepted batch and cleanup-withheld audit history while clearing active transfer attention.", Command: "loom lane acknowledge-transfer " + batch, Risk: "safe_run"},
			SafeAction{Key: "archive_transfer", Label: "Archive cleanup attention", Description: "Keep the cleanup-withheld batch as audit history and hide it from active transfer attention.", Command: "loom lane archive-transfer " + batch, Risk: "safe_run"},
		)
	}
	return actions
}

func loadAttentionState(statePath string) AttentionState {
	path := attentionStatePath(statePath)
	payload, err := os.ReadFile(path)
	if err != nil {
		return AttentionState{SchemaVersion: AttentionSchemaVersion, PendingItems: map[string]PendingAttentionItem{}}
	}
	var state AttentionState
	if err := json.Unmarshal(payload, &state); err != nil {
		return AttentionState{SchemaVersion: AttentionSchemaVersion, PendingItems: map[string]PendingAttentionItem{}}
	}
	if state.PendingItems == nil {
		state.PendingItems = map[string]PendingAttentionItem{}
	}
	if strings.TrimSpace(state.SchemaVersion) == "" {
		state.SchemaVersion = AttentionSchemaVersion
	}
	return state
}

func writeAttentionState(statePath string, state AttentionState) error {
	if strings.TrimSpace(state.SchemaVersion) == "" {
		state.SchemaVersion = AttentionSchemaVersion
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	if state.PendingItems == nil {
		state.PendingItems = map[string]PendingAttentionItem{}
	}
	if err := os.MkdirAll(statePath, 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(attentionStatePath(statePath), payload, 0o644)
}

func attentionStatePath(statePath string) string {
	return filepath.Join(statePath, "attention.json")
}

func attentionNow(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}
