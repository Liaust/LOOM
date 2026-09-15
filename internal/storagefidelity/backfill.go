package storagefidelity

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/storagecatalog"
)

const (
	BackfillSourceAll           = "all"
	BackfillSourceWatchedRoots  = "watched-roots"
	BackfillSourceMainDocuments = "main-documents"
	BackfillSourceDropzone      = "dropzone"
	BackfillSourceLane          = "lane"
)

type BackfillInput struct {
	Source         string `json:"source,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
	Apply          bool   `json:"apply,omitempty"`
	Yes            bool   `json:"yes,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeDeleted bool   `json:"include_deleted,omitempty"`
}

type BackfillResult struct {
	Source                 string         `json:"source"`
	DryRun                 bool           `json:"dry_run"`
	Applied                bool           `json:"applied"`
	Scanned                int            `json:"scanned"`
	Observed               int            `json:"observed"`
	FindingsPlanned        int            `json:"findings_planned"`
	FindingsRecorded       int            `json:"findings_recorded"`
	FindingsAlreadyPresent int            `json:"findings_already_present"`
	PayloadRewrites        int            `json:"payload_rewrites"`
	ExportRefreshRequested bool           `json:"export_refresh_requested"`
	Items                  []BackfillItem `json:"items,omitempty"`
	Notes                  []string       `json:"notes,omitempty"`
	GeneratedAt            time.Time      `json:"generated_at"`
}

type BackfillItem struct {
	StorageEntryID       string     `json:"storage_entry_id"`
	SourceArea           string     `json:"source_area"`
	NodeKey              string     `json:"node_key,omitempty"`
	LogicalPath          string     `json:"logical_path"`
	ViewPath             string     `json:"view_path,omitempty"`
	SourceRef            string     `json:"source_ref,omitempty"`
	PhysicalRefID        string     `json:"physical_ref_id,omitempty"`
	PhysicalRefKind      string     `json:"physical_ref_kind,omitempty"`
	PhysicalURI          string     `json:"physical_uri,omitempty"`
	PhysicalPath         string     `json:"physical_path,omitempty"`
	ObjectKind           string     `json:"object_kind,omitempty"`
	ObservationID        string     `json:"observation_id,omitempty"`
	Observed             bool       `json:"observed"`
	Applied              bool       `json:"applied"`
	Skipped              bool       `json:"skipped,omitempty"`
	Error                string     `json:"error,omitempty"`
	Evaluation           Evaluation `json:"evaluation"`
	Findings             []Finding  `json:"findings,omitempty"`
	RecordedFindingIDs   []string   `json:"recorded_finding_ids,omitempty"`
	ExistingFindingKinds []string   `json:"existing_finding_kinds,omitempty"`

	embeddedObservation *filesystemmeta.Observation
}

type BackfillCatalog interface {
	ListEntries(context.Context, storagecatalog.ListFilter) ([]storagecatalog.Entry, error)
	InspectEntry(context.Context, string) (storagecatalog.EntryDetail, error)
	RegisterFilesystemObservation(context.Context, storagecatalog.RegisterFilesystemObservationInput) (storagecatalog.FilesystemObservation, error)
	RegisterFidelityFinding(context.Context, storagecatalog.RegisterFidelityFindingInput) (storagecatalog.FidelityFinding, error)
	ListFidelityFindings(context.Context, storagecatalog.FidelityFindingFilter) ([]storagecatalog.FidelityFinding, error)
}

type BackfillService struct {
	Catalog BackfillCatalog
	Now     func() time.Time
}

func NewBackfillService(catalog BackfillCatalog) BackfillService {
	return BackfillService{Catalog: catalog}
}

func (s BackfillService) Backfill(ctx context.Context, input BackfillInput) (BackfillResult, error) {
	if s.Catalog == nil {
		return BackfillResult{}, fmt.Errorf("storage fidelity backfill catalog is not configured")
	}
	now := s.now()
	normalized, err := normalizeBackfillInput(input)
	if err != nil {
		return BackfillResult{}, err
	}
	result := BackfillResult{
		Source:                 normalized.Source,
		DryRun:                 normalized.DryRun,
		Applied:                normalized.Apply && normalized.Yes,
		PayloadRewrites:        0,
		ExportRefreshRequested: false,
		GeneratedAt:            now,
		Notes: []string{
			"backfill records filesystem observations and fidelity findings only",
			"retained payload bytes are not rewritten",
			"storage views are not refreshed by this command",
		},
	}
	entries, err := s.listEntries(ctx, normalized)
	if err != nil {
		return BackfillResult{}, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		detail, err := s.Catalog.InspectEntry(ctx, entry.StorageEntryID)
		if err != nil {
			item := backfillItemForEntry(entry, now)
			item.Error = err.Error()
			item.Skipped = true
			result.Items = append(result.Items, item)
			result.Scanned++
			continue
		}
		item := s.planItem(detail, now)
		if result.Applied {
			if err := s.applyItem(ctx, &item, now); err != nil {
				item.Error = err.Error()
			}
		}
		result.Scanned++
		if item.Observed {
			result.Observed++
		}
		result.FindingsPlanned += len(item.Findings)
		result.FindingsRecorded += len(item.RecordedFindingIDs)
		result.FindingsAlreadyPresent += len(item.ExistingFindingKinds)
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (s BackfillService) listEntries(ctx context.Context, input BackfillInput) ([]storagecatalog.Entry, error) {
	areas := sourceAreasForBackfill(input.Source)
	limit := input.Limit
	if limit <= 0 {
		limit = 5000
	}
	var entries []storagecatalog.Entry
	for _, area := range areas {
		filter := storagecatalog.ListFilter{
			SourceArea:     area,
			IncludeDeleted: input.IncludeDeleted,
			Limit:          limit,
		}
		for {
			page, err := s.Catalog.ListEntries(ctx, filter)
			if err != nil {
				return nil, err
			}
			entries = append(entries, page...)
			if input.Limit > 0 && len(entries) >= input.Limit {
				return entries[:input.Limit], nil
			}
			if len(page) < filter.Limit {
				break
			}
			filter.Offset += len(page)
		}
	}
	return entries, nil
}

func (s BackfillService) planItem(detail storagecatalog.EntryDetail, now time.Time) BackfillItem {
	entry := detail.Entry
	item := backfillItemForEntry(entry, now)
	ref, ok := selectObservablePhysicalRef(detail.PhysicalRefs)
	if !ok {
		if observation, ok := filesystemObservationFromEntryMetadata(entry.Metadata); ok {
			item.Observed = true
			item.ObjectKind = observation.Kind
			item.SourceRef = metadataOnlySourceRefForEntry(entry)
			item.embeddedObservation = observation
			evalEntry := entry
			evalEntry.Metadata = mergeObservationMetadata(entry.Metadata, *observation)
			item.Evaluation = EvaluateEntry(evalEntry, detail.PhysicalRefs, now)
			item.Findings = append([]Finding(nil), item.Evaluation.Findings...)
			return item
		}
		item.Skipped = true
		item.Error = "no local observable physical ref is available"
		item.Findings = append(item.Evaluation.Findings, Finding{
			Severity: SeverityError,
			Kind:     "no_observable_physical_ref",
			Summary:  "no local physical ref was available for filesystem metadata backfill",
			Blocking: true,
		})
		return item
	}
	item.PhysicalRefID = ref.StoragePhysicalRefID
	item.PhysicalRefKind = ref.RefKind
	item.PhysicalURI = ref.URI
	item.SourceRef = sourceRefForEntry(entry, ref)
	physicalPath, ok := physicalPathFromURI(ref.URI)
	if !ok {
		item.Skipped = true
		item.Error = "physical ref URI is not a local path"
		item.Findings = append(item.Evaluation.Findings, Finding{
			Severity: SeverityError,
			Kind:     "physical_ref_not_local",
			Summary:  "selected physical ref cannot be inspected as a local filesystem path",
			Blocking: true,
		})
		return item
	}
	item.PhysicalPath = physicalPath
	observation, err := filesystemmeta.DetectPath(physicalPath, filesystemmeta.DetectOptions{
		RootPath:          filepath.Dir(physicalPath),
		IncludeXattrNames: true,
	})
	if err != nil {
		item.Skipped = true
		item.Error = err.Error()
		item.Findings = append(item.Evaluation.Findings, Finding{
			Severity: SeverityError,
			Kind:     "current_physical_ref_unreadable",
			Summary:  "selected physical ref could not be inspected",
			Blocking: true,
		})
		return item
	}
	item.Observed = true
	item.ObjectKind = observation.Kind
	evalEntry := entry
	evalEntry.Metadata = mergeObservationMetadata(entry.Metadata, observation)
	item.Evaluation = EvaluateEntry(evalEntry, detail.PhysicalRefs, now)
	item.Findings = append([]Finding(nil), item.Evaluation.Findings...)
	return item
}

func (s BackfillService) applyItem(ctx context.Context, item *BackfillItem, now time.Time) error {
	if !item.Observed {
		return s.recordFindings(ctx, item, "")
	}
	var observation filesystemmeta.Observation
	if item.embeddedObservation != nil {
		observation = *item.embeddedObservation
	} else {
		sourceModePath := item.PhysicalPath
		detected, err := filesystemmeta.DetectPath(sourceModePath, filesystemmeta.DetectOptions{
			RootPath:          filepath.Dir(sourceModePath),
			IncludeXattrNames: true,
		})
		if err != nil {
			return err
		}
		observation = detected
	}
	observedAt := now.UTC()
	registered, err := s.Catalog.RegisterFilesystemObservation(ctx, storagecatalog.FilesystemObservationInputFromMeta(storagecatalog.FilesystemObservationInputOptions{
		StorageEntryID: item.StorageEntryID,
		SourceArea:     item.SourceArea,
		SourceNodeKey:  item.NodeKey,
		SourceRef:      item.SourceRef,
		LogicalPath:    item.LogicalPath,
		ObservedAt:     observedAt,
	}, observation))
	if err != nil {
		return err
	}
	item.ObservationID = registered.StorageFilesystemObservationID
	item.Applied = true
	return s.recordFindings(ctx, item, registered.StorageFilesystemObservationID)
}

func (s BackfillService) recordFindings(ctx context.Context, item *BackfillItem, observationID string) error {
	if len(item.Findings) == 0 {
		return nil
	}
	existing, err := s.Catalog.ListFidelityFindings(ctx, storagecatalog.FidelityFindingFilter{
		StorageEntryID:  item.StorageEntryID,
		Status:          filesystemmeta.FindingStatusOpen,
		IncludeResolved: false,
		Limit:           5000,
	})
	if err != nil {
		return err
	}
	existingKinds := map[string]bool{}
	for _, finding := range existing {
		existingKinds[finding.FindingKind] = true
	}
	for _, finding := range item.Findings {
		if existingKinds[finding.Kind] {
			item.ExistingFindingKinds = append(item.ExistingFindingKinds, finding.Kind)
			continue
		}
		detail, _ := json.Marshal(map[string]any{
			"source":          "storage.fidelity.backfill",
			"physical_ref_id": item.PhysicalRefID,
			"physical_ref":    item.PhysicalURI,
			"physical_kind":   item.PhysicalRefKind,
			"decision":        item.Evaluation.Decision,
		})
		registered, err := s.Catalog.RegisterFidelityFinding(ctx, storagecatalog.RegisterFidelityFindingInput{
			StorageFilesystemObservationID: observationID,
			StorageEntryID:                 item.StorageEntryID,
			NodeKey:                        item.NodeKey,
			SourceArea:                     item.SourceArea,
			SourceRef:                      item.SourceRef,
			LogicalPath:                    item.LogicalPath,
			Severity:                       finding.Severity,
			FindingKind:                    finding.Kind,
			Summary:                        finding.Summary,
			DetailJSON:                     detail,
			Status:                         filesystemmeta.FindingStatusOpen,
		})
		if err != nil {
			return err
		}
		item.RecordedFindingIDs = append(item.RecordedFindingIDs, registered.StorageFidelityFindingID)
	}
	return nil
}

func (s BackfillService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeBackfillInput(input BackfillInput) (BackfillInput, error) {
	input.Source = strings.TrimSpace(input.Source)
	if input.Source == "" {
		input.Source = BackfillSourceAll
	}
	if len(sourceAreasForBackfill(input.Source)) == 0 {
		return BackfillInput{}, fmt.Errorf("unsupported storage fidelity backfill source %q", input.Source)
	}
	if input.Apply && !input.Yes {
		return BackfillInput{}, fmt.Errorf("storage fidelity backfill --apply requires --yes")
	}
	if input.Apply && input.Yes {
		input.DryRun = false
	} else {
		input.DryRun = true
		input.Apply = false
	}
	if input.Limit < 0 {
		return BackfillInput{}, fmt.Errorf("storage fidelity backfill limit cannot be negative")
	}
	return input, nil
}

func sourceAreasForBackfill(source string) []string {
	switch strings.TrimSpace(source) {
	case "", BackfillSourceAll:
		return []string{
			storagecatalog.SourceAreaProjects,
			storagecatalog.SourceAreaNotes,
			storagecatalog.SourceAreaDocuments,
			storagecatalog.SourceAreaExternalWatchedRoot,
			storagecatalog.SourceAreaMainDocuments,
			storagecatalog.SourceAreaDropzone,
			storagecatalog.SourceAreaLane,
			storagecatalog.SourceAreaMainArchive,
		}
	case BackfillSourceWatchedRoots:
		return []string{
			storagecatalog.SourceAreaProjects,
			storagecatalog.SourceAreaNotes,
			storagecatalog.SourceAreaDocuments,
			storagecatalog.SourceAreaExternalWatchedRoot,
		}
	case BackfillSourceMainDocuments:
		return []string{storagecatalog.SourceAreaMainDocuments}
	case BackfillSourceDropzone:
		return []string{storagecatalog.SourceAreaDropzone}
	case BackfillSourceLane:
		return []string{storagecatalog.SourceAreaLane}
	default:
		return nil
	}
}

func backfillItemForEntry(entry storagecatalog.Entry, now time.Time) BackfillItem {
	return BackfillItem{
		StorageEntryID: entry.StorageEntryID,
		SourceArea:     entry.SourceArea,
		NodeKey:        entry.OriginNodeKey,
		LogicalPath:    entry.LogicalPath,
		ViewPath:       firstNonEmpty(entry.CurrentViewPath, entry.LogicalPath),
		Evaluation:     EvaluateEntry(entry, nil, now),
	}
}

func selectObservablePhysicalRef(refs []storagecatalog.PhysicalRef) (storagecatalog.PhysicalRef, bool) {
	resolved := storagecatalog.ResolvePhysicalRefs(refs, storagecatalog.ResolvePhysicalRefOptions{
		LocalOnly:    true,
		ExcludeKinds: []string{storagecatalog.PhysicalRefKindObjectBlob},
	})
	if len(resolved) > 0 {
		return resolved[0], true
	}
	return storagecatalog.PhysicalRef{}, false
}

func physicalPathFromURI(raw string) (string, bool) {
	return storagecatalog.PhysicalRefLocalPath(storagecatalog.PhysicalRef{RefKind: storagecatalog.PhysicalRefKindLocalPath, URI: raw})
}

func sourceRefForEntry(entry storagecatalog.Entry, ref storagecatalog.PhysicalRef) string {
	if entry.SourceArea == storagecatalog.SourceAreaMainDocuments {
		return "main_documents"
	}
	for _, value := range []string{
		entry.WatchedRootKey,
		entry.DropzoneTransferID,
		stringPtrValue(entry.PrivateBackupOperationID),
		ref.StoragePhysicalRefID,
	} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return entry.SourceArea
}

func metadataOnlySourceRefForEntry(entry storagecatalog.Entry) string {
	if entry.SourceArea == storagecatalog.SourceAreaMainDocuments {
		return "main_documents"
	}
	for _, value := range []string{
		entry.WatchedRootKey,
		entry.DropzoneTransferID,
		stringPtrValue(entry.PrivateBackupOperationID),
	} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return entry.SourceArea
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func filesystemObservationFromEntryMetadata(raw json.RawMessage) (*filesystemmeta.Observation, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false
	}
	if observation, ok := filesystemObservationFromRaw(root["filesystem_observation"]); ok {
		return observation, true
	}
	for _, key := range []string{
		"client_item_metadata",
		"file_transfer_metadata",
		"dropzone_metadata",
		"lane_metadata",
		"main_document_metadata",
	} {
		nestedRaw := root[key]
		if len(nestedRaw) == 0 {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(nestedRaw, &nested); err != nil {
			continue
		}
		if observation, ok := filesystemObservationFromRaw(nested["filesystem_observation"]); ok {
			return observation, true
		}
	}
	return nil, false
}

func filesystemObservationFromRaw(raw json.RawMessage) (*filesystemmeta.Observation, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var observation filesystemmeta.Observation
	if err := json.Unmarshal(raw, &observation); err != nil {
		return nil, false
	}
	if observation.Kind == "" || !filesystemmeta.ValidObjectKind(observation.Kind) {
		return nil, false
	}
	return &observation, true
}

func mergeObservationMetadata(existing json.RawMessage, observation filesystemmeta.Observation) json.RawMessage {
	value := map[string]any{}
	_ = json.Unmarshal(existing, &value)
	value["filesystem_observation"] = observation
	payload, err := json.Marshal(value)
	if err != nil {
		return existing
	}
	return payload
}
