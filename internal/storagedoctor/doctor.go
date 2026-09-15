package storagedoctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageview"
)

const (
	defaultMaxEntries     = 5000
	defaultMaxInspections = 500
)

type Client interface {
	ListStorageEntries(context.Context, string, storagecatalog.ListFilter) (response.Envelope[[]storagecatalog.Entry], error)
	InspectStorageEntry(context.Context, string, string) (response.Envelope[storagecatalog.EntryDetail], error)
	GetStorageFilesystemStatus(context.Context, string) (response.Envelope[FilesystemStatus], error)
	GetMainDocumentsStatus(context.Context, string) (response.Envelope[mainstorage.Status], error)
	GetStorageRetentionStatus(context.Context, string) (response.Envelope[storagecatalog.RetentionStatus], error)
	ListProjects(context.Context, string, int) (response.Envelope[[]projects.Project], error)
	InspectProjectArchive(context.Context, string, string) (response.Envelope[storagearchive.ProjectArchiveInspectResult], error)
}

func Run(ctx context.Context, client Client, correlationID string, opts Options) (Report, error) {
	opts = normalizeOptions(opts)
	builder := reportBuilder{now: time.Now().UTC()}

	entriesEnvelope, entriesErr := client.ListStorageEntries(ctx, correlationID, storagecatalog.ListFilter{
		Limit:          opts.MaxEntries,
		IncludeDeleted: true,
	})
	var entries []storagecatalog.Entry
	if entriesErr != nil {
		builder.add(Check{
			ID:         "catalog.list",
			Title:      "Catalog Listing",
			Status:     StatusError,
			Summary:    "Could not list storage catalog entries.",
			RepairHint: "Check loomd, database connectivity, and storage catalog migrations.",
			Findings: []Finding{{
				Severity:   SeverityError,
				Kind:       "catalog_list_failed",
				Message:    entriesErr.Error(),
				RepairHint: "Run loom health and loom storage doctor again after fixing daemon/database access.",
			}},
		})
	} else {
		entries = entriesEnvelope.Data
		builder.add(Check{
			ID:      "catalog.list",
			Title:   "Catalog Listing",
			Status:  StatusOK,
			Summary: fmt.Sprintf("Catalog returned %d entries.", len(entries)),
		})
	}

	filesystemStatus, filesystemErr := loadFilesystemStatus(ctx, client, correlationID)
	retentionStatus, retentionErr := loadRetentionStatus(ctx, client, correlationID)
	mainDocumentsStatus, mainDocumentsErr := loadMainDocumentsStatus(ctx, client, correlationID)

	builder.add(checkCatalogPhysicalRefs(ctx, client, correlationID, entries, opts.MaxInspections))
	builder.add(checkCatalogPaths(entries))
	builder.add(checkFilesystem(filesystemStatus, filesystemErr))
	builder.add(checkMainDocuments(entries, mainDocumentsStatus, mainDocumentsErr))
	builder.add(checkDropzoneEntries(entries))
	builder.add(checkWatchedRootBackups(entries))
	builder.add(checkRetention(entries, retentionStatus, retentionErr))
	builder.add(checkArchiveEntries(ctx, client, correlationID, entries, opts.MaxInspections))
	builder.add(checkProjectRuntimeArchives(ctx, client, correlationID))
	if opts.IncludeFidelity {
		builder.add(checkFidelity(entries))
	}

	if opts.IncludeMount {
		mount := RunMountPreflight(MountOptions{})
		builder.add(checkMountStatus(mount))
		mainDocumentsBind := RunMainDocumentsBindPreflight(MainDocumentsBindOptions{})
		builder.add(checkMainDocumentsBindStatus(mainDocumentsBind))
	}

	return builder.report(), nil
}

func checkFidelity(entries []storagecatalog.Entry) Check {
	report := storagefidelity.ReportForCatalogEntries("", "", entries, time.Now().UTC())
	check := Check{
		ID:         "storage.fidelity",
		Title:      "Storage Fidelity",
		Status:     StatusOK,
		Summary:    fmt.Sprintf("Found %d fidelity finding group(s): errors=%d warnings=%d.", report.Summary.Items, report.Summary.Errors, report.Summary.Warnings),
		RepairHint: "Use loom storage fidelity report for path-level details before deleting source data.",
	}
	for _, item := range report.Items {
		for _, finding := range item.Findings {
			severity := SeverityInfo
			if finding.Severity == storagefidelity.SeverityError {
				severity = SeverityError
			} else if finding.Severity == storagefidelity.SeverityWarning {
				severity = SeverityWarning
			}
			check.Findings = append(check.Findings, Finding{
				Severity: severity,
				Kind:     "fidelity_" + finding.Kind,
				Target:   firstNonEmpty(item.ViewPath, item.StorageEntryID),
				Message:  finding.Summary,
				RepairHint: func() string {
					if finding.Blocking {
						return "Do not delete the source path until LOOM has retained payload bytes and blocking fidelity findings are resolved."
					}
					return "Use faithful restore mode or explicitly accept metadata loss before deleting the source path."
				}(),
			})
		}
	}
	return finalizeCheck(check)
}

func RepairCatalogPlan(source string, dryRun bool) RepairPlan {
	source = strings.TrimSpace(source)
	now := time.Now().UTC()
	plan := RepairPlan{
		Status:      StatusWarning,
		Source:      source,
		DryRun:      dryRun,
		GeneratedAt: now,
	}
	switch source {
	case "dropzone":
		plan.Actions = []string{
			"Run loom box dropzone status on the source node and confirm transfers are accepted.",
			"Run the dropzone accept/import worker on main for any pending transfer sessions.",
			"Run loom storage doctor again and confirm dropzone custody entries have available physical refs.",
		}
	case "watched-roots":
		plan.Actions = []string{
			"Run the node-agent watched-root scan on the source node.",
			"Run the private backup/sync worker and confirm pushed items are accepted by main.",
			"Run loom storage doctor again and confirm watched-root backup entries and physical refs appear in the catalog.",
		}
	case "main-documents":
		plan.Actions = []string{
			"Run loom storage main-documents reconcile --dry-run to compare the deprecated legacy root with canonical Box Documents and active main_documents catalog rows.",
			"After confirming retained bytes and cloud backup state, run loom storage main-documents reconcile --yes to tombstone missing current rows while preserving retention snapshots.",
			"Move legacy files only through the separately reviewed filesystem migration cutover; reconcile does not move payload bytes.",
		}
	default:
		plan.Status = StatusError
		plan.Warnings = append(plan.Warnings, "unsupported catalog repair source; use dropzone, watched-roots, or main-documents")
		return plan
	}
	if !dryRun {
		if source == "main-documents" {
			plan.Warnings = append(plan.Warnings, "main-documents repair is applied by loom storage main-documents reconcile --yes")
		} else {
			plan.Warnings = append(plan.Warnings, "catalog repair is currently diagnostic only; LOOM does not yet expose a safe worker replay mutation API")
		}
	}
	return plan
}

func RepairRetentionPlan(status storagecatalog.RetentionStatus, dryRun bool) RepairPlan {
	plan := RepairPlan{
		Status:      StatusOK,
		Source:      "retention",
		DryRun:      dryRun,
		GeneratedAt: time.Now().UTC(),
	}
	if status.Failed == 0 {
		plan.Actions = []string{"No failed retention entries were reported."}
		return plan
	}
	plan.Status = StatusWarning
	plan.Actions = []string{
		"Inspect failed retention entries through loom storage list --availability-state failed.",
		"Fetch or restore affected entries from their available physical refs before marking anything safe-to-delete.",
		"Run loom storage doctor again after resolving failed retention entries.",
	}
	if !dryRun {
		plan.Warnings = append(plan.Warnings, "retention repair is currently diagnostic only; LOOM does not yet expose a tombstone/retention replay mutation API")
	}
	return plan
}

func normalizeOptions(opts Options) Options {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = defaultMaxEntries
	}
	if opts.MaxInspections <= 0 {
		opts.MaxInspections = defaultMaxInspections
	}
	if opts.MaxInspections > opts.MaxEntries {
		opts.MaxInspections = opts.MaxEntries
	}
	return opts
}

func loadFilesystemStatus(ctx context.Context, client Client, correlationID string) (FilesystemStatus, error) {
	envelope, err := client.GetStorageFilesystemStatus(ctx, correlationID)
	if err != nil {
		return FilesystemStatus{}, err
	}
	return envelope.Data, nil
}

func loadRetentionStatus(ctx context.Context, client Client, correlationID string) (storagecatalog.RetentionStatus, error) {
	envelope, err := client.GetStorageRetentionStatus(ctx, correlationID)
	if err != nil {
		return storagecatalog.RetentionStatus{}, err
	}
	return envelope.Data, nil
}

func loadMainDocumentsStatus(ctx context.Context, client Client, correlationID string) (mainstorage.Status, error) {
	envelope, err := client.GetMainDocumentsStatus(ctx, correlationID)
	if err != nil {
		return mainstorage.Status{}, err
	}
	return envelope.Data, nil
}

func checkCatalogPhysicalRefs(ctx context.Context, client Client, correlationID string, entries []storagecatalog.Entry, maxInspections int) Check {
	check := Check{
		ID:     "catalog.physical_refs",
		Title:  "Catalog Physical Refs",
		Status: StatusOK,
	}
	inspected := 0
	for _, entry := range entries {
		if !entryShouldHavePhysicalRef(entry) {
			continue
		}
		if inspected >= maxInspections {
			check.Findings = append(check.Findings, Finding{
				Severity: SeverityWarning,
				Kind:     "inspection_limit_reached",
				Message:  fmt.Sprintf("Stopped after %d physical-ref inspections.", maxInspections),
			})
			break
		}
		inspected++
		envelope, err := client.InspectStorageEntry(ctx, correlationID, entry.StorageEntryID)
		if err != nil {
			check.Findings = append(check.Findings, Finding{
				Severity:   SeverityError,
				Kind:       "entry_inspect_failed",
				Target:     entry.StorageEntryID,
				Message:    err.Error(),
				RepairHint: "Run loom storage inspect for this entry and check catalog/database health.",
			})
			continue
		}
		if !hasUsablePhysicalRef(envelope.Data.PhysicalRefs) {
			check.Findings = append(check.Findings, Finding{
				Severity:   SeverityError,
				Kind:       "missing_available_physical_ref",
				Target:     entry.StorageEntryID,
				Message:    "Entry requires retained bytes but has no available physical ref.",
				RepairHint: "Replay the relevant ingestion lane or restore the physical bytes before marking the source safe-to-delete.",
			})
		}
	}
	check.Summary = fmt.Sprintf("Inspected %d retained entries for usable physical refs.", inspected)
	check.RepairHint = "Use loom storage repair catalog --source dropzone or --source watched-roots after identifying the source lane."
	return finalizeCheck(check)
}

func checkCatalogPaths(entries []storagecatalog.Entry) Check {
	check := Check{ID: "catalog.paths", Title: "Catalog Paths", Status: StatusOK, RepairHint: "Inspect invalid catalog rows before migration or restore."}
	active := 0
	for _, entry := range entries {
		if !entryShouldAppearInView(entry) {
			continue
		}
		active++
		if _, err := storageview.CatalogPathForEntry(entry); err != nil {
			check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "invalid_catalog_path", Target: entry.StorageEntryID, Message: err.Error()})
		}
	}
	check.Summary = fmt.Sprintf("Validated %d active catalog paths without constructing a generated tree.", active)
	return finalizeCheck(check)
}

func checkFilesystem(status FilesystemStatus, err error) Check {
	check := Check{ID: "filesystem.roots", Title: "Canonical Physical Roots", Status: StatusOK, RepairHint: "Run loom storage filesystem status and verify the configured canonical roots."}
	if err != nil {
		check.Summary = "Could not read canonical filesystem status."
		check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "filesystem_status_failed", Message: err.Error()})
		return finalizeCheck(check)
	}
	for _, root := range status.Roots {
		if root.Status == StatusError || root.Status == StatusWarning {
			severity := SeverityWarning
			if root.Status == StatusError {
				severity = SeverityError
			}
			check.Findings = append(check.Findings, Finding{Severity: severity, Kind: "physical_root_" + root.Status, Target: root.Path, Message: root.Key + ": " + root.Message})
		}
	}
	if status.Catalog.Failed > 0 {
		check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "catalog_failed_entries", Message: fmt.Sprintf("Sample contains %d failed catalog entries.", status.Catalog.Failed)})
	}
	check.Summary = fmt.Sprintf("Checked %d canonical roots and %d bounded catalog rows.", len(status.Roots), status.Catalog.Returned)
	return finalizeCheck(check)
}

func checkMainDocuments(entries []storagecatalog.Entry, status mainstorage.Status, err error) Check {
	check := Check{
		ID:         "main_documents.catalog",
		Title:      "Main Documents Catalog",
		Status:     StatusOK,
		RepairHint: "Run loom storage main-documents status, then reconcile deleted/moved SMB paths with loom storage main-documents reconcile --dry-run.",
	}
	if err != nil {
		check.Summary = "Could not read main Documents import status."
		check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "main_documents_status_failed", Message: err.Error()})
		return finalizeCheck(check)
	}
	cataloged := countEntries(entries, func(entry storagecatalog.Entry) bool {
		return entry.SourceArea == storagecatalog.SourceAreaMainDocuments || entry.StorageClass == storagecatalog.StorageClassMainDocument
	})
	if status.FilesAccepted > 0 && cataloged == 0 {
		check.Findings = append(check.Findings, Finding{
			Severity:   SeverityError,
			Kind:       "accepted_files_not_cataloged",
			Target:     status.BackingRoot,
			Message:    fmt.Sprintf("Main Documents status reports %d accepted files but no main document catalog entries were returned.", status.FilesAccepted),
			RepairHint: "Run the main Documents import worker and verify storage catalog writes.",
		})
	}
	if status.FilesFailed > 0 {
		check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "main_documents_import_failures", Target: status.BackingRoot, Message: fmt.Sprintf("%d main Documents imports failed.", status.FilesFailed)})
	}
	if status.FilesMissingCataloged > 0 {
		check.Findings = append(check.Findings, Finding{
			Severity:   SeverityWarning,
			Kind:       "main_documents_missing_cataloged",
			Target:     status.BackingRoot,
			Message:    fmt.Sprintf("%d active main Documents catalog rows are missing from the backing filesystem.", status.FilesMissingCataloged),
			RepairHint: "Run loom storage main-documents reconcile --dry-run. If the paths were intentionally deleted or moved through SMB, run reconcile --yes after verifying retained bytes and cloud backup.",
		})
	}
	if status.FilesTombstoned > 0 {
		check.Findings = append(check.Findings, Finding{
			Severity:   SeverityInfo,
			Kind:       "main_documents_tombstoned",
			Target:     status.BackingRoot,
			Message:    fmt.Sprintf("%d main Documents catalog rows were tombstoned by reconciliation.", status.FilesTombstoned),
			RepairHint: "Run loom storage main-documents status to confirm tombstoned current paths no longer appear as active canonical Documents rows.",
		})
	}
	if !status.Exists {
		check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "main_documents_root_missing", Target: status.BackingRoot, Message: "Main Documents backing root does not exist."})
	}
	check.Summary = fmt.Sprintf("Main Documents accepted=%d failed=%d missing=%d tombstoned=%d cataloged=%d.", status.FilesAccepted, status.FilesFailed, status.FilesMissingCataloged, status.FilesTombstoned, cataloged)
	return finalizeCheck(check)
}

func checkDropzoneEntries(entries []storagecatalog.Entry) Check {
	check := Check{
		ID:         "dropzone.catalog",
		Title:      "Dropzone Custody Catalog",
		Status:     StatusOK,
		RepairHint: "Run loom storage repair catalog --source dropzone after confirming transfer sessions.",
	}
	count := 0
	for _, entry := range entries {
		if entry.SourceArea != storagecatalog.SourceAreaDropzone && entry.StorageClass != storagecatalog.StorageClassDropzoneCustody {
			continue
		}
		count++
		if entry.StorageClass != storagecatalog.StorageClassDropzoneCustody {
			check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "dropzone_wrong_storage_class", Target: entry.StorageEntryID, Message: "Dropzone entry is not marked as dropzone_custody."})
		}
		if entry.AvailabilityState != storagecatalog.AvailabilityStateAvailable {
			check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "dropzone_not_available", Target: entry.StorageEntryID, Message: "Dropzone custody entry is not available."})
		}
	}
	check.Summary = fmt.Sprintf("Found %d dropzone custody catalog entries.", count)
	return finalizeCheck(check)
}

func checkWatchedRootBackups(entries []storagecatalog.Entry) Check {
	check := Check{
		ID:         "watched_roots.backups",
		Title:      "Watched-Root Backup Views",
		Status:     StatusOK,
		RepairHint: "Run loom storage repair catalog --source watched-roots, then inspect typed catalog and backup status.",
	}
	total := 0
	for _, entry := range entries {
		if !watchedRootSource(entry.SourceArea) || !entryShouldAppearInView(entry) {
			continue
		}
		total++
		if _, err := storageview.CatalogPathForEntry(entry); err != nil {
			check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "watched_root_invalid_catalog_path", Target: entry.StorageEntryID, Message: err.Error()})
		}
	}
	check.Summary = fmt.Sprintf("Checked %d active watched-root backup catalog entries.", total)
	return finalizeCheck(check)
}

func checkRetention(entries []storagecatalog.Entry, status storagecatalog.RetentionStatus, err error) Check {
	check := Check{
		ID:         "retention.tombstones",
		Title:      "Retention And Tombstones",
		Status:     StatusOK,
		RepairHint: "Run loom storage repair retention and inspect failed entries before deleting local sources.",
	}
	if err != nil {
		check.Summary = "Could not read retention status."
		check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "retention_status_failed", Message: err.Error()})
		return finalizeCheck(check)
	}
	if status.Failed > 0 {
		check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "retention_failures", Message: fmt.Sprintf("%d retention entries failed.", status.Failed)})
	}
	if status.UnsafeCandidates > 0 {
		check.Findings = append(check.Findings, Finding{Severity: SeverityInfo, Kind: "unsafe_delete_candidates", Message: fmt.Sprintf("%d entries are not safe to delete.", status.UnsafeCandidates)})
	}
	tombstoned := countEntries(entries, func(entry storagecatalog.Entry) bool {
		return entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned
	})
	if status.Tombstoned > 0 && tombstoned == 0 {
		check.Findings = append(check.Findings, Finding{Severity: SeverityInfo, Kind: "tombstone_count_mismatch", Message: "Retention status reports tombstones outside the current doctor catalog sample."})
	}
	check.Summary = fmt.Sprintf("Retention entries=%d tombstoned=%d failed=%d unsafe=%d.", status.Entries, status.Tombstoned, status.Failed, status.UnsafeCandidates)
	return finalizeCheck(check)
}

func checkArchiveEntries(ctx context.Context, client Client, correlationID string, entries []storagecatalog.Entry, maxInspections int) Check {
	check := Check{
		ID:         "archive.manifests",
		Title:      "Archive Manifest Refs",
		Status:     StatusOK,
		RepairHint: "Inspect archive manifests and restore missing archive bytes before deleting source material.",
	}
	archiveEntries := 0
	inspected := 0
	for _, entry := range entries {
		if entry.StorageClass != storagecatalog.StorageClassArchiveEntry && entry.SourceArea != storagecatalog.SourceAreaMainArchive {
			continue
		}
		archiveEntries++
		if entry.ArchiveManifestID == nil || strings.TrimSpace(*entry.ArchiveManifestID) == "" {
			check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "archive_entry_without_manifest", Target: entry.StorageEntryID, Message: "Archive entry does not point to an archive manifest."})
		}
		if inspected >= maxInspections {
			continue
		}
		inspected++
		envelope, err := client.InspectStorageEntry(ctx, correlationID, entry.StorageEntryID)
		if err != nil {
			check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "archive_entry_inspect_failed", Target: entry.StorageEntryID, Message: err.Error()})
			continue
		}
		if !hasUsablePhysicalRef(envelope.Data.PhysicalRefs) {
			check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "archive_entry_missing_bytes", Target: entry.StorageEntryID, Message: "Archive entry has no available archive bytes."})
		}
	}
	check.Summary = fmt.Sprintf("Checked %d archive catalog entries.", archiveEntries)
	return finalizeCheck(check)
}

func checkProjectRuntimeArchives(ctx context.Context, client Client, correlationID string) Check {
	check := Check{
		ID:         "project.runtime_archives",
		Title:      "Project Runtime Archives",
		Status:     StatusOK,
		RepairHint: "Run loom project archive inspect <project>. Recover the exact incomplete physical operation; restored runtime remains inactive. Historical manifests are read-only evidence.",
	}
	envelope, err := client.ListProjects(ctx, correlationID, 250)
	if err != nil {
		check.Summary = "Could not list projects for runtime archive checks."
		check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "project_list_failed", Message: err.Error()})
		return finalizeCheck(check)
	}
	archived := 0
	for _, project := range envelope.Data {
		if !projectLooksArchived(project) {
			continue
		}
		archived++
		inspect, err := client.InspectProjectArchive(ctx, correlationID, project.Slug)
		if err != nil {
			check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "project_archive_inspect_failed", Target: project.Slug, Message: err.Error()})
			continue
		}
		if physical := inspect.Data.Physical; physical != nil {
			if physical.ProjectID != project.ProjectID || !physical.MutationBlocked {
				check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "project_physical_archive_conflict", Target: project.Slug, Message: "Physical archive inspection does not match the project identity or runtime guard."})
				continue
			}
			switch physical.EvidenceStatus {
			case "verified":
				if physical.NextAction == "activation_not_available" {
					check.Findings = append(check.Findings, Finding{Severity: SeverityInfo, Kind: "project_restored_inactive", Target: project.Slug, Message: "Project custody is restored; runtime activation and fence release remain disabled."})
				}
			case "pending":
				check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "project_physical_archive_incomplete", Target: project.Slug, Message: "Physical archive or restore has an incomplete project phase. Inspect and recover its exact operation."})
			case "unavailable":
				check.Findings = append(check.Findings, Finding{Severity: SeverityWarning, Kind: "project_physical_archive_unverified", Target: project.Slug, Message: "Current physical custody could not be independently inspected. Do not infer completion or activate runtime."})
			default:
				check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "project_physical_archive_conflict", Target: project.Slug, Message: "Physical custody evidence contradicts project state. Preserve both and inspect the exact operation."})
			}
			continue
		}
		if inspect.Data.RuntimeManifest == nil {
			check.Findings = append(check.Findings, Finding{Severity: SeverityError, Kind: "project_runtime_manifest_missing", Target: project.Slug, Message: "Archived project has no runtime archive manifest."})
		}
	}
	if archived == 0 {
		check.Summary = "No archived projects were returned."
		return check
	}
	check.Summary = fmt.Sprintf("Checked %d project archive/restore states and their current or historical evidence.", archived)
	return finalizeCheck(check)
}

func checkMountStatus(status MountStatus) Check {
	protocol := strings.ToUpper(status.Protocol)
	check := Check{
		ID:         "mount.preflight",
		Title:      protocol + " Mount Preflight",
		Status:     status.Status,
		Summary:    fmt.Sprintf("%s mount path %s status=%s.", protocol, status.MountPath, status.Status),
		RepairHint: "Run scripts/loom-macbook smb-preflight or loom storage mount-status --doctor --protocol smb on the MacBook node. Use --protocol rclone for the explicit rclone path.",
	}
	for _, mountCheck := range status.Checks {
		if mountCheck.Status == StatusOK {
			continue
		}
		severity := SeverityWarning
		if mountCheck.Status == StatusError {
			severity = SeverityError
		}
		check.Findings = append(check.Findings, Finding{
			Severity: severity,
			Kind:     "mount_" + mountCheck.ID,
			Target:   mountCheck.Detail,
			Message:  mountCheck.Summary,
		})
	}
	return finalizeCheck(check)
}

func checkMainDocumentsBindStatus(status MainDocumentsBindStatus) Check {
	check := Check{
		ID:         "main_documents.canonical_layout",
		Title:      "Canonical Box Documents",
		Status:     status.Status,
		Summary:    fmt.Sprintf("canonical Box Documents %s status=%s.", status.TargetPath, status.Status),
		RepairHint: "Check the configured LOOM Box Documents directory; it must be a writable real directory and not a separate or bind mount.",
	}
	for _, mountCheck := range status.Checks {
		if mountCheck.Status == StatusOK || mountCheck.Status == StatusSkipped {
			continue
		}
		severity := SeverityWarning
		if mountCheck.Status == StatusError {
			severity = SeverityError
		}
		check.Findings = append(check.Findings, Finding{
			Severity: severity,
			Kind:     "main_documents_" + mountCheck.ID,
			Target:   mountCheck.Detail,
			Message:  mountCheck.Summary,
		})
	}
	return finalizeCheck(check)
}

func entryShouldHavePhysicalRef(entry storagecatalog.Entry) bool {
	if entry.DeletedAt != nil {
		return false
	}
	if !entryRequiresRetainedPayload(entry) {
		return false
	}
	switch entry.AvailabilityState {
	case storagecatalog.AvailabilityStateAvailable, storagecatalog.AvailabilityStateArchived, storagecatalog.AvailabilityStateSuperseded:
		return true
	default:
		return false
	}
}

func entryRequiresRetainedPayload(entry storagecatalog.Entry) bool {
	if entry.FileClass == storagecatalog.FileClassDirectory || entry.FileClass == storagecatalog.FileClassGeneratedMetadata {
		return false
	}
	if entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned {
		return false
	}
	if entry.SizeBytes == nil {
		return true
	}
	return *entry.SizeBytes > 0
}

func entryShouldAppearInView(entry storagecatalog.Entry) bool {
	if entry.DeletedAt != nil {
		return false
	}
	switch entry.AvailabilityState {
	case storagecatalog.AvailabilityStateDeleted, storagecatalog.AvailabilityStateFailed, storagecatalog.AvailabilityStateTombstoned:
		return false
	default:
		return true
	}
}

func hasUsablePhysicalRef(refs []storagecatalog.PhysicalRef) bool {
	for _, ref := range refs {
		switch ref.Status {
		case storagecatalog.PhysicalRefStatusAvailable, storagecatalog.PhysicalRefStatusSuperseded:
			if strings.TrimSpace(ref.URI) != "" {
				return true
			}
		}
	}
	return false
}

func watchedRootSource(sourceArea string) bool {
	switch sourceArea {
	case storagecatalog.SourceAreaProjects, storagecatalog.SourceAreaNotes, storagecatalog.SourceAreaDocuments, storagecatalog.SourceAreaExternalWatchedRoot:
		return true
	default:
		return false
	}
}

func countEntries(entries []storagecatalog.Entry, predicate func(storagecatalog.Entry) bool) int {
	count := 0
	for _, entry := range entries {
		if predicate(entry) {
			count++
		}
	}
	return count
}

func projectLooksArchived(project projects.Project) bool {
	if state, ok := projects.ParseProjectPhysicalArchiveState(project.ArchiveState); ok {
		return state.MutationBlocked
	}
	if project.Status == projects.ProjectRegistrationStatusArchived || project.Status == "archived" {
		return true
	}
	if len(project.ArchiveState) == 0 || string(project.ArchiveState) == "null" {
		return false
	}
	var state map[string]any
	if err := json.Unmarshal(project.ArchiveState, &state); err != nil {
		return true
	}
	if schema, _ := state["schema_version"].(string); strings.HasPrefix(schema, "project.physical_") {
		return true
	}
	status, _ := state["status"].(string)
	return status == "archived"
}

func finalizeCheck(check Check) Check {
	hasWarnings := false
	hasErrors := false
	for i := range check.Findings {
		check.Findings[i].CheckID = check.ID
		switch check.Findings[i].Severity {
		case SeverityError:
			hasErrors = true
		case SeverityWarning:
			hasWarnings = true
		}
	}
	switch {
	case hasErrors:
		check.Status = StatusError
	case hasWarnings:
		check.Status = StatusWarning
	case check.Status == "":
		check.Status = StatusOK
	}
	if check.Summary == "" {
		check.Summary = "No issues found."
	}
	return check
}

type reportBuilder struct {
	now    time.Time
	checks []Check
}

func (b *reportBuilder) add(check Check) {
	if check.ID == "" {
		return
	}
	b.checks = append(b.checks, check)
}

func (b reportBuilder) report() Report {
	report := Report{
		Status:      StatusOK,
		Checks:      append([]Check{}, b.checks...),
		GeneratedAt: b.now,
	}
	for _, check := range report.Checks {
		report.Summary.Checks++
		switch check.Status {
		case StatusError:
			report.Summary.Errors++
			report.Status = StatusError
		case StatusWarning:
			report.Summary.Warnings++
			if report.Status != StatusError {
				report.Status = StatusWarning
			}
		case StatusSkipped:
			report.Summary.Skipped++
		default:
			report.Summary.OK++
		}
		report.Findings = append(report.Findings, check.Findings...)
	}
	return report
}
