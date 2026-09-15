package storagefidelity

import (
	"encoding/json"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageview"
)

func EvaluateEntry(entry storagecatalog.Entry, refs []storagecatalog.PhysicalRef, now time.Time) Evaluation {
	eval := Evaluation{
		StorageEntryID:  entry.StorageEntryID,
		ViewPath:        firstNonEmpty(entry.CurrentViewPath, entry.LogicalPath),
		FileClass:       entry.FileClass,
		ProcessingState: entry.ProcessingState,
		Availability:    entry.AvailabilityState,
		RetentionState:  entry.RetentionState,
		PayloadRetained: payloadRetained(entry, refs),
		CloudVerified:   hasAvailableCloudRef(refs),
		CheckedAt:       now,
	}
	addEntryFindings(&eval, entry.Metadata)
	addCatalogStateFindings(&eval, entry)
	if requiresPayload(entry) && !eval.PayloadRetained {
		addFinding(&eval, SeverityError, "payload_not_retained", "payload bytes are not retained in a non-source physical ref", true)
	}
	finalizeEvaluation(&eval)
	return eval
}

func EvaluateViewEntry(entry storageview.ViewEntry, now time.Time) Evaluation {
	eval := Evaluation{
		StorageEntryID:  entry.StorageEntryID,
		ViewPath:        entry.ViewPath,
		FileClass:       entry.FileClass,
		ProcessingState: entry.ProcessingState,
		Availability:    entry.AvailabilityState,
		RetentionState:  entry.RetentionState,
		PayloadRetained: viewEntryLooksRetained(entry),
		CheckedAt:       now,
	}
	addEntryFindings(&eval, entry.Metadata)
	addViewStateFindings(&eval, entry)
	finalizeEvaluation(&eval)
	return eval
}

func ReportForViewEntries(prefix, node string, entries []storageview.ViewEntry, now time.Time) Report {
	prefix = normalizePrefix(prefix)
	node = strings.TrimSpace(node)
	report := Report{Prefix: prefix, Node: node, GeneratedAt: now}
	for _, entry := range entries {
		if node != "" && !strings.EqualFold(entry.OriginNodeKey, node) {
			continue
		}
		viewPath := normalizePrefix(entry.ViewPath)
		if prefix != "" && viewPath != prefix && !strings.HasPrefix(viewPath, prefix+"/") {
			continue
		}
		eval := EvaluateViewEntry(entry, now)
		if len(eval.Findings) == 0 && eval.Decision == DecisionSafe {
			continue
		}
		report.Items = append(report.Items, eval)
		addToSummary(&report.Summary, eval)
	}
	return report
}

// ReportForCatalogEntries evaluates canonical catalog rows directly. It keeps
// the retired view-path vocabulary for operator filtering without constructing
// a generated tree or synthetic status sidecars.
func ReportForCatalogEntries(prefix, node string, entries []storagecatalog.Entry, now time.Time) Report {
	prefix = normalizePrefix(prefix)
	node = strings.TrimSpace(node)
	report := Report{Prefix: prefix, Node: node, GeneratedAt: now}
	for _, entry := range entries {
		if node != "" && !strings.EqualFold(entry.OriginNodeKey, node) {
			continue
		}
		viewEntry, err := storageview.CatalogViewEntry(entry)
		if err != nil {
			continue
		}
		viewPath := normalizePrefix(viewEntry.ViewPath)
		if prefix != "" && viewPath != prefix && !strings.HasPrefix(viewPath, prefix+"/") {
			continue
		}
		eval := EvaluateViewEntry(viewEntry, now)
		if len(eval.Findings) == 0 && eval.Decision == DecisionSafe {
			continue
		}
		report.Items = append(report.Items, eval)
		addToSummary(&report.Summary, eval)
	}
	return report
}

func BlockingFindings(eval Evaluation) []Finding {
	out := []Finding{}
	for _, finding := range eval.Findings {
		if finding.Blocking {
			out = append(out, finding)
		}
	}
	return out
}

func WarningFindings(eval Evaluation) []Finding {
	out := []Finding{}
	for _, finding := range eval.Findings {
		if finding.Severity == SeverityWarning && !finding.Blocking {
			out = append(out, finding)
		}
	}
	return out
}

func addCatalogStateFindings(eval *Evaluation, entry storagecatalog.Entry) {
	switch entry.AvailabilityState {
	case storagecatalog.AvailabilityStateFailed:
		addFinding(eval, SeverityError, "availability_failed", "catalog availability is failed", true)
	case storagecatalog.AvailabilityStatePending, storagecatalog.AvailabilityStateDiscovered:
		addFinding(eval, SeverityError, "availability_pending", "main has not accepted this entry yet", true)
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateFailed {
		addFinding(eval, SeverityError, "processing_failed", "catalog processing failed", true)
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateExcluded {
		addFinding(eval, SeverityError, "processing_excluded", "entry was excluded from retained storage", true)
	}
	if entry.StorageClass == storagecatalog.StorageClassPrivateBackup && entry.ProcessingState == storagecatalog.ProcessingStateMetadataOnly && requiresPayload(entry) {
		addFinding(eval, SeverityError, "metadata_only_payload", "entry is metadata-only but payload bytes exist", true)
	}
}

func addViewStateFindings(eval *Evaluation, entry storageview.ViewEntry) {
	switch entry.AvailabilityState {
	case storagecatalog.AvailabilityStateFailed:
		addFinding(eval, SeverityError, "availability_failed", "catalog availability is failed", true)
	case storagecatalog.AvailabilityStatePending, storagecatalog.AvailabilityStateDiscovered:
		addFinding(eval, SeverityError, "availability_pending", "main has not accepted this entry yet", true)
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateFailed {
		addFinding(eval, SeverityError, "processing_failed", "catalog processing failed", true)
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateExcluded {
		addFinding(eval, SeverityError, "processing_excluded", "entry was excluded from retained storage", true)
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateMetadataOnly && entry.SizeBytes != nil && *entry.SizeBytes > 0 && entry.EntryKind == storageview.EntryKindFile && viewEntryMetadataOnlyPayloadIsBlocking(entry) {
		addFinding(eval, SeverityError, "metadata_only_payload", "entry is metadata-only but payload bytes exist", true)
	}
}

func viewEntryMetadataOnlyPayloadIsBlocking(entry storageview.ViewEntry) bool {
	if entry.FileClass == storagecatalog.FileClassGeneratedMetadata {
		return false
	}
	// Archive entries use metadata_only to mean that no indexing/processing was
	// performed. Their immutable object is nevertheless intentional physical
	// custody, so its presence is not a fidelity contradiction.
	if entry.StorageClass == storagecatalog.StorageClassArchiveEntry {
		return false
	}
	if entry.StorageClass == storagecatalog.StorageClassMainDocument || entry.SourceArea == storagecatalog.SourceAreaMainDocuments {
		return false
	}
	return true
}

func addEntryFindings(eval *Evaluation, metadata json.RawMessage) {
	meta := metadataMap(metadata)
	metaText := strings.ToLower(string(metadata))
	objectKind := stringMeta(meta, "object_kind", "kind")
	if eval.FileClass == storagecatalog.FileClassGeneratedMetadata || boolMeta(meta, "generated_metadata") || containsAny(metaText, "appledouble", ".ds_store") {
		addFinding(eval, SeverityWarning, "generated_metadata", "generated platform metadata is tracked as diagnostics, not normal user payload", false)
	}
	if eval.FileClass == storagecatalog.FileClassPackage || boolMeta(meta, "is_package") || stringMeta(meta, "package_kind") != "" || containsAny(metaText, "package directory") {
		blocking := !eval.PayloadRetained
		addFinding(eval, severityForBlocking(blocking), "package_boundary", "package directory boundary may need explicit faithful restore handling", blocking)
	}
	if boolMeta(meta, "permission_denied") || eval.ProcessingState == storagecatalog.IndexingStatePermissionDenied {
		addFinding(eval, SeverityError, "permission_denied", "source path could not be inspected because of permissions", true)
	}
	if eval.ProcessingState == storagecatalog.IndexingStateTooLarge || boolMeta(meta, "backup_file_too_large") || boolMeta(meta, "skipped_too_large") || containsAny(strings.ToLower(stringMeta(meta, "error_code", "error")), "too_large") {
		addFinding(eval, SeverityError, "skipped_too_large", "file exceeded configured ingest/index limits and was not fully retained", true)
	}
	if stringMeta(meta, "symlink_target") != "" || objectKind == "symlink" {
		addFinding(eval, SeverityWarning, "symlink_safe_view", "safe view records symlink metadata instead of recreating a live symlink", false)
	}
	if boolMeta(meta, "special_file") || objectKind == "special" || objectKind == "socket" || objectKind == "fifo" || objectKind == "device" {
		addFinding(eval, SeverityError, "special_file", "special filesystem object is metadata-only and needs manual restore handling", true)
	}
	if objectKind != filesystemmeta.ObjectKindDirectory && (boolMeta(meta, "executable") || sourceModeExecutable(meta)) {
		addFinding(eval, SeverityWarning, "executable_mode_not_restored", "safe view strips executable bits; faithful restore may need chmod", false)
	}
	if hasExtendedMetadata(meta) {
		addFinding(eval, SeverityWarning, "extended_metadata_not_applied", "extended metadata is observed but not applied in safe views", false)
	}
}

func finalizeEvaluation(eval *Evaluation) {
	eval.Severity = SeverityInfo
	blocking := false
	warnings := false
	for _, finding := range eval.Findings {
		if finding.Severity == SeverityError {
			eval.Severity = SeverityError
		}
		if finding.Blocking {
			blocking = true
		}
		if finding.Severity == SeverityWarning {
			warnings = true
		}
	}
	switch {
	case blocking:
		eval.Decision = DecisionNotSafe
		eval.SafeToDelete = false
	case warnings:
		eval.Decision = DecisionPartiallySafe
		eval.SafeToDelete = true
		if eval.Severity != SeverityError {
			eval.Severity = SeverityWarning
		}
	default:
		eval.Decision = DecisionSafe
		eval.SafeToDelete = true
		if len(eval.Findings) == 0 {
			eval.Severity = SeverityInfo
		}
	}
}

func addToSummary(summary *Summary, eval Evaluation) {
	summary.Items++
	switch eval.Severity {
	case SeverityError:
		summary.Errors++
	case SeverityWarning:
		summary.Warnings++
	default:
		summary.Info++
	}
	switch eval.Decision {
	case DecisionSafe:
		summary.Safe++
	case DecisionPartiallySafe:
		summary.PartiallySafe++
	case DecisionNotSafe:
		summary.NotSafe++
	default:
		summary.Unknown++
	}
}

func payloadRetained(entry storagecatalog.Entry, refs []storagecatalog.PhysicalRef) bool {
	if entry.SizeBytes != nil && *entry.SizeBytes == 0 {
		return entry.RetentionState == storagecatalog.RetentionStateRetained ||
			entry.RetentionState == storagecatalog.RetentionStateSnapshot ||
			entry.AvailabilityState == storagecatalog.AvailabilityStateAvailable ||
			entry.AvailabilityState == storagecatalog.AvailabilityStateArchived
	}
	for _, ref := range refs {
		if storagecatalog.PhysicalRefRetainsPayload(entry, ref) {
			return true
		}
	}
	return false
}

func hasAvailableCloudRef(refs []storagecatalog.PhysicalRef) bool {
	for _, ref := range refs {
		class, ok := storagecatalog.ClassifyPhysicalRef(ref)
		if ok && class == storagecatalog.PhysicalRefClassCloudCopy && storagecatalog.PhysicalRefAvailable(ref) {
			return true
		}
	}
	return false
}

func viewEntryLooksRetained(entry storageview.ViewEntry) bool {
	if entry.SizeBytes != nil && *entry.SizeBytes == 0 {
		return entry.RetentionState == storagecatalog.RetentionStateRetained ||
			entry.RetentionState == storagecatalog.RetentionStateSnapshot ||
			entry.AvailabilityState == storagecatalog.AvailabilityStateAvailable ||
			entry.AvailabilityState == storagecatalog.AvailabilityStateArchived
	}
	return entry.AvailabilityState == storagecatalog.AvailabilityStateAvailable ||
		entry.AvailabilityState == storagecatalog.AvailabilityStateArchived ||
		entry.RetentionState == storagecatalog.RetentionStateRetained ||
		entry.RetentionState == storagecatalog.RetentionStateSnapshot
}

func requiresPayload(entry storagecatalog.Entry) bool {
	if entry.FileClass == storagecatalog.FileClassDirectory {
		return false
	}
	if entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned {
		return false
	}
	if entry.SizeBytes == nil {
		return entry.FileClass != storagecatalog.FileClassGeneratedMetadata
	}
	return *entry.SizeBytes > 0
}

func addFinding(eval *Evaluation, severity, kind, summary string, blocking bool) {
	if kind == "" {
		return
	}
	for _, existing := range eval.Findings {
		if existing.Kind == kind {
			return
		}
	}
	eval.Findings = append(eval.Findings, Finding{Severity: severity, Kind: kind, Summary: summary, Blocking: blocking})
}

func severityForBlocking(blocking bool) string {
	if blocking {
		return SeverityError
	}
	return SeverityWarning
}

func metadataMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if nested, ok := out["filesystem_observation"].(map[string]any); ok {
		for key, value := range nested {
			if _, exists := out[key]; !exists {
				out[key] = value
			}
		}
	}
	return out
}

func boolMeta(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	value, ok := meta[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func stringMeta(meta map[string]any, keys ...string) string {
	if meta == nil {
		return ""
	}
	for _, key := range keys {
		value, ok := meta[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(strings.ToLower(typed))
		}
	}
	return ""
}

func hasExtendedMetadata(meta map[string]any) bool {
	if boolMeta(meta, "has_xattrs") ||
		boolMeta(meta, "has_acl") ||
		boolMeta(meta, "has_resource_fork") ||
		boolMeta(meta, "has_finder_tags") ||
		boolMeta(meta, "has_quarantine") {
		return true
	}
	if meta == nil {
		return false
	}
	value, ok := meta["xattr_names"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	default:
		return false
	}
}

func sourceModeExecutable(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	value, ok := meta["source_mode"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)&0o111 != 0
	case int:
		return typed&0o111 != 0
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "0") {
			var parsed int
			for _, r := range trimmed {
				if r < '0' || r > '7' {
					return false
				}
				parsed = parsed*8 + int(r-'0')
			}
			return parsed&0o111 != 0
		}
	}
	return false
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func normalizePrefix(value string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
