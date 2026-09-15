package portal

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageview"
)

func storageSelectableItems(state ScreenState) []SelectableItem {
	return withOperationalActionGroupsFromCandidates(state, storageCollapsedSelectableItems(state), storageDetailedSelectableItems(state))
}

func storageDetailedSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Storage
	items := []SelectableItem{}
	row := 0
	add := func(item SelectableItem) {
		item.Screen = ScreenStorage
		item.RowIndex = row
		items = append(items, item)
		row++
	}

	if len(data.Tree.Entries) > 0 {
		for _, entry := range data.Tree.Entries {
			add(storageViewEntrySelectableItem(entry))
		}
		return items
	}

	seen := map[string]bool{}
	for _, item := range storageNodeSelectableItems(data.Tree.Root, seen) {
		add(item)
	}
	return items
}

func storageSearchSelectableItems(state ScreenState, filter string, query string) []SelectableItem {
	if strings.TrimSpace(query) == "" {
		return storageCollapsedSelectableItems(state)
	}
	items := storageDetailedSelectableItems(state)
	result := make([]SelectableItem, 0, len(items))
	for _, item := range items {
		if storageSelectableHiddenByDefault(item) || !selectableMatchesSearchFilter(item, filter) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return storageCollapsedSelectableItems(state)
	}
	return result
}

func storageSelectableHiddenByDefault(item SelectableItem) bool {
	ref := firstNonEmpty(item.RecordRef, item.RecordLabel, item.Label)
	if storagePathHiddenByDefault(ref) {
		return true
	}
	if item.PrimaryAction != nil {
		payload := item.PrimaryAction.Executor.Payload
		if payload["entry_kind"] == storageview.EntryKindStatus || payload["entry_kind"] == storageview.EntryKindControl {
			return true
		}
		if storagePathHiddenByDefault(payload["view_path"]) {
			return true
		}
	}
	return false
}

func storageNodeSelectableItems(root storageview.Node, seen map[string]bool) []SelectableItem {
	if root.Name == "" && root.Path == "" && len(root.Children) == 0 {
		return nil
	}
	items := []SelectableItem{}
	var walk func(storageview.Node)
	walk = func(node storageview.Node) {
		ref := firstNonEmpty(node.Path, node.Name)
		if ref != "" && !seen[ref] {
			seen[ref] = true
			action := NewStorageNodeInspectAction(node)
			items = append(items, SelectableItem{
				Kind:          SelectableKindRecord,
				Label:         firstNonEmpty(node.Path, node.Name),
				Description:   storageNodeDescription(node),
				RecordKind:    "storage_node",
				RecordRef:     ref,
				RecordLabel:   firstNonEmpty(node.Path, node.Name),
				PrimaryAction: actionPtr(action),
			})
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return items
}

func storageViewEntrySelectableItem(entry storageview.ViewEntry) SelectableItem {
	ref := storageEntryRef(entry)
	action := NewStorageEntryInspectAction(entry)
	related := []PortalAction{}
	if ref != "" && entry.StorageEntryID != "" {
		related = append(related, NewStorageSafeToDeleteAction(entry))
		if entry.EntryKind == storageview.EntryKindFile {
			related = append(related, NewStorageFetchAction(entry), NewStorageRestoreAction(entry))
		}
		related = append(related, NewStorageArchiveAction(entry))
	}
	return SelectableItem{
		Kind:           SelectableKindRecord,
		Label:          firstNonEmpty(entry.ViewPath, entry.DisplayName, entry.StorageEntryID),
		Description:    storageViewEntryDescription(entry),
		RecordKind:     storageViewEntryRecordKind(entry),
		RecordRef:      ref,
		RecordLabel:    firstNonEmpty(entry.ViewPath, entry.DisplayName, entry.StorageEntryID),
		PrimaryAction:  actionPtr(action),
		RelatedActions: related,
	}
}

func NewStorageNodeInspectAction(node storageview.Node) PortalAction {
	ref := firstNonEmpty(node.Path, node.Name, "storage")
	payload := map[string]string{
		"view_path":        ref,
		"entry_kind":       firstNonEmpty(node.EntryKind, storageview.EntryKindDirectory),
		"permissions":      firstNonEmpty(node.Permissions, "-"),
		"writable":         fmt.Sprintf("%t", node.Writable),
		"generated":        fmt.Sprintf("%t", node.Generated),
		"read_only_reason": node.ReadOnlyReason,
		"children":         fmt.Sprintf("%d", len(node.Children)),
	}
	action := NewPortalRecordInspectAction("storage", ScreenStorage, "storage_node", ref, firstNonEmpty(node.Path, node.Name), payload)
	action.ID = "storage.node." + storageActionIDPart(ref) + ".inspect"
	action.Label = "Inspect Folder"
	action.Description = "Inspect this generated LOOM Main storage folder."
	action.TargetKind = "storage_node"
	action.RawCommand = []string{"loom", "storage", "tree"}
	action.RefreshScreen = ScreenStorage
	return action
}

func NewStorageCollapsedPathInspectAction(row storageCollapsedRow) PortalAction {
	payload := map[string]string{
		"view_path":   row.Path,
		"permissions": firstNonEmpty(row.Status, "-"),
		"writable":    fmt.Sprintf("%t", row.Writable),
		"files":       fmt.Sprintf("%d", row.Files),
		"directories": fmt.Sprintf("%d", row.Directories),
		"description": row.Description,
	}
	action := NewPortalRecordInspectAction("storage", ScreenStorage, row.RecordKind, row.Path, row.Path, payload)
	action.ID = "storage.path." + storageActionIDPart(row.Path) + ".inspect"
	action.Label = "Inspect Storage Path"
	action.Description = "Inspect this top-level LOOM Main storage path."
	action.TargetKind = row.RecordKind
	action.RawCommand = []string{"loom", "storage", "tree"}
	action.RefreshScreen = ScreenStorage
	return action
}

func NewStorageEntryInspectAction(entry storageview.ViewEntry) PortalAction {
	ref := storageEntryRef(entry)
	payload := storageViewEntryPayload(entry)
	action := PortalAction{
		ID:            "storage.entry." + storageActionIDPart(ref) + ".inspect",
		Label:         "Inspect Storage Entry",
		Description:   "Inspect this LOOM Main storage entry through the backend catalog.",
		Domain:        "storage",
		SourceScreen:  ScreenStorage,
		TargetKind:    storageViewEntryRecordKind(entry),
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(entry.ViewPath, entry.DisplayName, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorStorageInspect, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "storage", "inspect", ref},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewStorageSafeToDeleteAction(entry storageview.ViewEntry) PortalAction {
	ref := storageEntryRef(entry)
	action := PortalAction{
		ID:            "storage.entry." + storageActionIDPart(ref) + ".safe_to_delete",
		Label:         "Check Safe To Delete",
		Description:   "Ask LOOM whether the source copy can be removed without losing custody.",
		Domain:        "storage",
		SourceScreen:  ScreenStorage,
		TargetKind:    storageViewEntryRecordKind(entry),
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(entry.ViewPath, entry.DisplayName, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorStorageSafeToDelete, Target: ref, Payload: storageViewEntryPayload(entry)},
		RawCommand:    []string{"loom", "storage", "safe-to-delete", ref},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewStorageFetchAction(entry storageview.ViewEntry) PortalAction {
	ref := storageEntryRef(entry)
	action := PortalAction{
		ID:           "storage.entry." + storageActionIDPart(ref) + ".fetch",
		Label:        "Fetch To Path",
		Description:  "Copy this storage entry from main custody to a local destination path.",
		Domain:       "storage",
		SourceScreen: ScreenStorage,
		TargetKind:   storageViewEntryRecordKind(entry),
		TargetRef:    ref,
		TargetLabel:  firstNonEmpty(entry.ViewPath, entry.DisplayName, ref),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "destination_path", Label: "Destination Path", Kind: ActionFieldPath, Required: true, Placeholder: "/absolute/path/to/file", Help: "Absolute local path where LOOM should write the fetched file."},
			{Name: "overwrite", Label: "Overwrite", Kind: ActionFieldBoolean, Value: "false", Help: "Set true only when replacing an existing local file is intentional."},
		},
		InputValues:   map[string]string{"overwrite": "false"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorStorageFetch, Target: ref, Payload: storageViewEntryPayload(entry)},
		RawCommand:    []string{"loom", "storage", "fetch", ref, "--to", "<path>"},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewStorageRestoreAction(entry storageview.ViewEntry) PortalAction {
	ref := storageEntryRef(entry)
	action := PortalAction{
		ID:           "storage.entry." + storageActionIDPart(ref) + ".restore",
		Label:        "Restore To Path",
		Description:  "Restore a retained storage entry and record the restore reason.",
		Domain:       "storage",
		SourceScreen: ScreenStorage,
		TargetKind:   storageViewEntryRecordKind(entry),
		TargetRef:    ref,
		TargetLabel:  firstNonEmpty(entry.ViewPath, entry.DisplayName, ref),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "destination_path", Label: "Destination Path", Kind: ActionFieldPath, Required: true, Placeholder: "/absolute/path/to/file"},
			{Name: "reason", Label: "Reason", Kind: ActionFieldText, Value: "portal restore"},
			{Name: "overwrite", Label: "Overwrite", Kind: ActionFieldBoolean, Value: "false"},
		},
		InputValues:   map[string]string{"reason": "portal restore", "overwrite": "false"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorStorageRestore, Target: ref, Payload: storageViewEntryPayload(entry)},
		RawCommand:    []string{"loom", "storage", "restore", ref, "--to", "<path>"},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewStorageArchiveAction(entry storageview.ViewEntry) PortalAction {
	ref := storageEntryRef(entry)
	action := PortalAction{
		ID:           "storage.entry." + storageActionIDPart(ref) + ".archive",
		Label:        "Archive Storage Entry",
		Description:  "Move or plan moving this entry into main/archive custody.",
		Domain:       "storage",
		SourceScreen: ScreenStorage,
		TargetKind:   storageViewEntryRecordKind(entry),
		TargetRef:    ref,
		TargetLabel:  firstNonEmpty(entry.ViewPath, entry.DisplayName, ref),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "target_path", Label: "Archive Path", Kind: ActionFieldPath, Required: true, Placeholder: "main/archive/project-name"},
			{Name: "archive_kind", Label: "Archive Kind", Kind: ActionFieldText, Value: "manual_archive"},
			{Name: "dry_run", Label: "Dry Run", Kind: ActionFieldBoolean, Value: "true", Help: "Default is preview only. Set false to create the archive."},
			{Name: "mark_source_archived", Label: "Mark Source Archived", Kind: ActionFieldBoolean, Value: "false"},
		},
		InputValues: map[string]string{
			"archive_kind":         "manual_archive",
			"dry_run":              "true",
			"mark_source_archived": "false",
		},
		Executor:      PortalActionExecutor{Kind: PortalExecutorStorageArchive, Target: ref, Payload: storageViewEntryPayload(entry)},
		RawCommand:    []string{"loom", "storage", "archive", ref, "--to", "<main/archive/path>", "--dry-run"},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewStorageRetentionStatusAction(status storagecatalog.RetentionStatus) PortalAction {
	payload := map[string]string{
		"entries":           fmt.Sprintf("%d", status.Entries),
		"retained":          fmt.Sprintf("%d", status.Retained),
		"snapshots":         fmt.Sprintf("%d", status.Snapshots),
		"pending":           fmt.Sprintf("%d", status.Pending),
		"expired":           fmt.Sprintf("%d", status.Expired),
		"tombstoned":        fmt.Sprintf("%d", status.Tombstoned),
		"failed":            fmt.Sprintf("%d", status.Failed),
		"safe_candidates":   fmt.Sprintf("%d", status.SafeCandidates),
		"unsafe_candidates": fmt.Sprintf("%d", status.UnsafeCandidates),
		"generated_at":      timeOrDash(status.GeneratedAt),
	}
	action := PortalAction{
		ID:            "storage.retention.status",
		Label:         "Retention Status",
		Description:   "Inspect retention, tombstone, and safe-to-delete counts.",
		Domain:        "storage",
		SourceScreen:  ScreenStorage,
		TargetKind:    "storage_retention",
		TargetRef:     "storage_retention",
		TargetLabel:   "Storage Retention",
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorStorageRetentionStatus, Target: "storage_retention", Payload: payload},
		RawCommand:    []string{"loom", "storage", "retention", "status"},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewMainDocumentsStatusAction(status mainstorage.Status) PortalAction {
	payload := map[string]string{
		"exists":                       fmt.Sprintf("%t", status.Exists),
		"stable_window":                fmt.Sprintf("%ds", status.StableWindowSeconds),
		"max_files_per_run":            fmt.Sprintf("%d", status.MaxFilesPerRun),
		"files_discovered":             fmt.Sprintf("%d", status.FilesDiscovered),
		"files_accepted":               fmt.Sprintf("%d", status.FilesAccepted),
		"files_delayed":                fmt.Sprintf("%d", status.FilesDelayed),
		"files_skipped":                fmt.Sprintf("%d", status.FilesSkipped),
		"files_failed":                 fmt.Sprintf("%d", status.FilesFailed),
		"bytes_hashed":                 fmt.Sprintf("%d", status.BytesHashed),
		"latest_accepted_at":           timePtrOrDash(status.LatestAcceptedAt),
		"generated_at":                 timeOrDash(status.GeneratedAt),
		"metrics_total_duration_ms":    fmt.Sprintf("%d", status.Metrics.TotalDurationMS),
		"metrics_discover_duration_ms": fmt.Sprintf("%d", status.Metrics.DiscoverDurationMS),
		"metrics_active_catalog_fetch_duration_ms": fmt.Sprintf("%d", status.Metrics.ActiveCatalogFetchDurationMS),
		"metrics_hash_duration_ms":                 fmt.Sprintf("%d", status.Metrics.HashDurationMS),
		"metrics_retention_copy_duration_ms":       fmt.Sprintf("%d", status.Metrics.RetentionCopyDurationMS),
		"metrics_register_duration_ms":             fmt.Sprintf("%d", status.Metrics.RegisterDurationMS),
		"metrics_reconcile_duration_ms":            fmt.Sprintf("%d", status.Metrics.ReconcileDurationMS),
		"metrics_hash_operations":                  fmt.Sprintf("%d", status.Metrics.HashOperations),
		"metrics_retention_copy_operations":        fmt.Sprintf("%d", status.Metrics.RetentionCopyOperations),
		"metrics_register_operations":              fmt.Sprintf("%d", status.Metrics.RegisterOperations),
	}
	action := PortalAction{
		ID:            "storage.main_documents.status",
		Label:         "Main Documents Status",
		Description:   "Inspect writable main/Documents ingestion status.",
		Domain:        "storage",
		SourceScreen:  ScreenStorage,
		TargetKind:    "main_documents",
		TargetRef:     "main_documents",
		TargetLabel:   "Main Documents",
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorMainDocumentsStatus, Target: "main_documents", Payload: payload},
		RawCommand:    []string{"loom", "storage", "main-documents", "status"},
		RefreshScreen: ScreenStorage,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewStorageMountHelperAction() PortalAction {
	payload := map[string]string{
		"default_protocol": "smb",
		"mac_helper":       "scripts/loom-macbook main-storage-status",
		"smb_preflight":    "scripts/loom-macbook smb-preflight",
		"smb_mount":        "scripts/loom-macbook mount-main",
		"mount_path":       "~/loom-storage",
		"rclone_optional":  "scripts/loom-macbook mount-main-rclone",
		"note":             "SMB is the default MacBook/Finder path. rclone remains available for explicit portable automation.",
	}
	action := NewPortalRecordInspectAction("storage", ScreenStorage, "storage_mount_helper", "storage_mount", "MacBook Storage Mount Helper", payload)
	action.ID = "storage.mount.helper.inspect"
	action.Label = "Mount Helper Status"
	action.Description = "Show the MacBook storage helper; SMB is default and rclone is optional."
	action.TargetKind = "storage_mount"
	action.RawCommand = []string{"scripts/loom-macbook", "main-storage-status"}
	action.RefreshScreen = ScreenStorage
	return action
}

func storageEntryRef(entry storageview.ViewEntry) string {
	return firstNonEmpty(entry.StorageEntryID, entry.ViewPath, entry.DisplayName)
}

func storageViewEntryRecordKind(entry storageview.ViewEntry) string {
	if strings.EqualFold(entry.SourceArea, storagecatalog.SourceAreaMainArchive) || strings.EqualFold(entry.StorageClass, storagecatalog.StorageClassArchiveEntry) || strings.HasPrefix(strings.TrimPrefix(entry.ViewPath, "/"), "main/archive") {
		return "storage_archive"
	}
	if strings.EqualFold(entry.SourceArea, storagecatalog.SourceAreaDropzone) || strings.EqualFold(entry.StorageClass, storagecatalog.StorageClassDropzoneCustody) {
		return "storage_transfer"
	}
	if entry.EntryKind == storageview.EntryKindDirectory || entry.Generated {
		return "storage_node"
	}
	return "storage_entry"
}

func storageViewEntryDescription(entry storageview.ViewEntry) string {
	parts := []string{}
	for _, part := range []string{entry.EntryKind, entry.StorageClass, entry.SourceArea, entry.OriginNodeKey, entry.FileClass, entry.AvailabilityState, entry.RetentionState} {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
	}
	if entry.SizeBytes != nil {
		parts = append(parts, storageFormatBytes(*entry.SizeBytes))
	}
	eval := storagefidelity.EvaluateViewEntry(entry, time.Now().UTC())
	if len(eval.Findings) > 0 {
		parts = append(parts, "fidelity:"+eval.Severity)
	}
	return strings.Join(parts, "  ")
}

func storageNodeDescription(node storageview.Node) string {
	parts := []string{firstNonEmpty(node.EntryKind, storageview.EntryKindDirectory), firstNonEmpty(node.Permissions, "-")}
	if node.Generated {
		parts = append(parts, "generated")
	}
	if node.Writable {
		parts = append(parts, "writable")
	}
	if node.ReadOnlyReason != "" {
		parts = append(parts, node.ReadOnlyReason)
	}
	return strings.Join(parts, "  ")
}

func storageViewEntryPayload(entry storageview.ViewEntry) map[string]string {
	payload := map[string]string{
		"view_path":          entry.ViewPath,
		"display_name":       entry.DisplayName,
		"entry_kind":         entry.EntryKind,
		"storage_entry_id":   entry.StorageEntryID,
		"storage_class":      entry.StorageClass,
		"source_area":        entry.SourceArea,
		"origin_node":        entry.OriginNodeKey,
		"logical_path":       entry.LogicalPath,
		"permissions":        entry.Permissions,
		"writable":           fmt.Sprintf("%t", entry.Writable),
		"generated":          fmt.Sprintf("%t", entry.Generated),
		"read_only_reason":   entry.ReadOnlyReason,
		"file_class":         entry.FileClass,
		"processing_state":   entry.ProcessingState,
		"availability_state": entry.AvailabilityState,
		"retention_state":    entry.RetentionState,
	}
	if entry.SizeBytes != nil {
		payload["size"] = storageFormatBytes(*entry.SizeBytes)
		payload["size_bytes"] = fmt.Sprintf("%d", *entry.SizeBytes)
	}
	if entry.ChecksumAlgorithm != "" || entry.ChecksumHex != "" {
		payload["checksum"] = strings.TrimSpace(entry.ChecksumAlgorithm + ":" + entry.ChecksumHex)
	}
	eval := storagefidelity.EvaluateViewEntry(entry, time.Now().UTC())
	payload["fidelity_severity"] = eval.Severity
	payload["fidelity_decision"] = eval.Decision
	payload["safe_to_delete_fidelity"] = fmt.Sprintf("%t", eval.SafeToDelete)
	if len(eval.Findings) > 0 {
		payload["fidelity_findings"] = portalFidelityFindingList(eval.Findings)
	}
	return payload
}

func portalFidelityFindingList(findings []storagefidelity.Finding) string {
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		part := finding.Kind
		if finding.Blocking {
			part += "!"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

func storageActionIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "storage"
	}
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "storage"
	}
	if len(result) > 80 {
		return result[:80]
	}
	return result
}

func storageFormatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	scaled := float64(value)
	unit := "B"
	for _, candidate := range units {
		scaled = scaled / 1024
		unit = candidate
		if scaled < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", scaled, unit)
}
