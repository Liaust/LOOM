package loomcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
)

const (
	storageHumanStateAccepted        = "accepted"
	storageHumanStatePending         = "pending"
	storageHumanStateRetrying        = "retrying"
	storageHumanStateFailed          = "failed"
	storageHumanStateIgnored         = "ignored"
	storageHumanStateSafeToDelete    = "safe_to_delete"
	storageHumanStateNotSafeToDelete = "not_safe_to_delete"
	storageHumanStateArchived        = "archived"
	storageHumanStateSuperseded      = "superseded"
	storageHumanStateMissingDeferred = "missing_deferred"
	storageHumanStateTombstoned      = "tombstoned"
	storageHumanStateUnknown         = "unknown"
)

type storageStatusReport struct {
	Input        string                               `json:"input"`
	Resolved     storageResolvedRef                   `json:"resolved"`
	State        string                               `json:"state"`
	Message      string                               `json:"message,omitempty"`
	SafeToDelete *storageretention.SafeToDeleteResult `json:"safe_to_delete,omitempty"`
	ViewEntry    *storageview.ViewEntry               `json:"view_entry,omitempty"`
	Entry        *storagecatalog.Entry                `json:"entry,omitempty"`
	PhysicalRefs []storagecatalog.PhysicalRef         `json:"physical_refs,omitempty"`
	MainImport   *mainstorage.FileStatus              `json:"main_import,omitempty"`
	Next         []string                             `json:"next,omitempty"`
	CheckedAt    time.Time                            `json:"checked_at"`
}

type storageResolvedRef struct {
	Input     string   `json:"input"`
	Kind      string   `json:"kind"`
	Ref       string   `json:"ref,omitempty"`
	ViewPath  string   `json:"view_path,omitempty"`
	LocalPath string   `json:"local_path,omitempty"`
	Notes     []string `json:"notes,omitempty"`
}

type storageVerificationReport struct {
	Status     storageStatusReport  `json:"status"`
	Checks     []storageVerifyCheck `json:"checks"`
	Verified   bool                 `json:"verified"`
	VerifiedAt time.Time            `json:"verified_at"`
}

type storageVerifyCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type storageFailuresReport struct {
	GeneratedAt         time.Time                `json:"generated_at"`
	Entries             []storagecatalog.Entry   `json:"entries,omitempty"`
	MainDocumentImports []mainstorage.FileStatus `json:"main_document_imports,omitempty"`
	FilesystemFindings  []filesystemFindingLite  `json:"filesystem_findings,omitempty"`
	TransferFailures    []filetransfer.Status    `json:"transfer_failures,omitempty"`
	Summary             map[string]int           `json:"summary"`
}

type filesystemFindingLite struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary"`
}

type storageTransfersReport struct {
	GeneratedAt         time.Time                `json:"generated_at"`
	Transfers           []filetransfer.Status    `json:"transfers,omitempty"`
	MainDocumentImports []mainstorage.FileStatus `json:"main_document_imports,omitempty"`
	CatalogTransfers    []storagecatalog.Entry   `json:"catalog_transfers,omitempty"`
	Summary             map[string]int           `json:"summary"`
}

func newStorageStatusCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <path>",
		Short: "Explain whether a storage file is accepted, pending, failed, ignored, or safe to delete",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			report, err := buildStorageStatusReport(ctx, commandCtx, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.status_failed", "storage", args[0], "Could not resolve storage status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.State)
				return nil
			}
			renderStorageStatusReport(cmd, report)
			return nil
		},
	}
	return cmd
}

func newStorageVerifyCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <path>",
		Short: "Verify catalog, physical-ref, safe-to-delete, and optional local checksum state for a storage path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			status, err := buildStorageStatusReport(ctx, commandCtx, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.verify_failed", "storage", args[0], "Could not verify storage path.", err))
			}
			report := buildStorageVerificationReport(status)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				if report.Verified {
					fmt.Fprintln(cmd.OutOrStdout(), "verified")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "not_verified")
				}
				return nil
			}
			renderStorageVerificationReport(cmd, report)
			return nil
		},
	}
	return cmd
}

func newStorageFailuresCommand(opts *options) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "failures",
		Short: "List storage failures across catalog, physical roots, main Documents import, and file transfers",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			report, err := buildStorageFailuresReport(ctx, commandCtx, limit)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.failures_failed", "storage", "failures", "Could not list storage failures.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\n", report.Summary["total"])
				return nil
			}
			renderStorageFailuresReport(cmd, report)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum rows per failure lane")
	return cmd
}

func newStorageTransfersCommand(opts *options) *cobra.Command {
	filter := filetransfer.ListFilter{Limit: 50}
	cmd := &cobra.Command{
		Use:   "transfers",
		Short: "List file transfer and transfer-like storage status for Dropzone, watched roots, and main Documents",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			report, err := buildStorageTransfersReport(ctx, commandCtx, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.transfers_failed", "storage", "transfers", "Could not list storage transfers.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\n", report.Summary["total"])
				return nil
			}
			renderStorageTransfersReport(cmd, report)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter file transfers by status")
	cmd.Flags().StringVar(&filter.TransferKind, "kind", "", "filter by transfer kind")
	cmd.Flags().StringVar(&filter.SourceNodeKey, "node", "", "filter by source node key")
	cmd.Flags().StringVar(&filter.SourceRootKey, "root", "", "filter by source root key")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum file transfers to list")
	return cmd
}

func buildStorageStatusReport(ctx context.Context, commandCtx commandContext, input string) (storageStatusReport, error) {
	resolved := resolveStorageInput(commandCtx.Config, input)
	report := storageStatusReport{Input: strings.TrimSpace(input), Resolved: resolved, State: storageHumanStateUnknown, CheckedAt: time.Now().UTC()}
	if strings.TrimSpace(input) == "" {
		report.Message = "storage path is required"
		return report, nil
	}

	if isStorageEntryID(resolved.Ref) {
		envelope, err := commandCtx.Client.InspectStorageEntry(ctx, commandCtx.CorrelationID, resolved.Ref)
		if err != nil {
			report.Message = err.Error()
			report.Next = append(report.Next, "Run loom storage list to find an accepted storage entry.")
			return report, nil
		}
		return completeStorageStatusReport(ctx, commandCtx, report, envelope.Data, nil), nil
	}

	if resolved.ViewPath != "" {
		envelope, err := commandCtx.Client.ResolveStoragePath(ctx, commandCtx.CorrelationID, resolved.ViewPath)
		if err == nil && envelope.Data.EntryDetail != nil {
			return completeStorageStatusReport(ctx, commandCtx, report, *envelope.Data.EntryDetail, &envelope.Data.ViewEntry), nil
		}
		if mainImport, ok := lookupMainDocumentImport(ctx, commandCtx, resolved.ViewPath); ok {
			report.MainImport = &mainImport
			report.State = humanStateForMainImport(mainImport.State)
			report.Message = firstNonEmpty(mainImport.Error, mainImport.DelayReason, mainImport.IgnoredReason, "main/Documents import has not produced an accepted catalog entry yet")
			report.Next = append(report.Next, nextStepsForMainDocumentImport(mainImport.State)...)
			return report, nil
		}
		if strings.HasPrefix(strings.Trim(resolved.ViewPath, "/"), "main/Documents/") {
			report.Message = "No accepted catalog entry matched this main/Documents path."
			if status, ok := loadMainDocumentsStatusForStatus(ctx, commandCtx); ok {
				switch {
				case status.FilesMissingCataloged > 0:
					report.Message += fmt.Sprintf(" Main Documents currently has %d missing active catalog row(s), usually after files were moved or deleted through SMB.", status.FilesMissingCataloged)
				case status.FilesFailed > 0:
					report.Message += fmt.Sprintf(" Main Documents currently has %d failed import(s).", status.FilesFailed)
				case status.FilesDelayed > 0 || status.FilesRemaining > 0:
					report.State = storageHumanStatePending
					report.Message += " The importer still has delayed or remaining files; the file may still be stabilizing after an SMB copy."
				}
			}
			report.Next = append(report.Next,
				"Run loom storage main-documents status to inspect recent main/Documents import state.",
				"Run loom storage main-documents reconcile --dry-run if this path was moved or deleted through SMB.",
				"Run loom storage main-documents reconcile --yes only after confirming retained bytes and cloud backup state.",
			)
			return report, nil
		}
		report.Message = "No accepted catalog entry matched this path."
		report.Next = append(report.Next, "Run loom storage tree to browse accepted storage paths.")
		if strings.Contains(resolved.ViewPath, "/Backups/") {
			report.Next = append(report.Next, "Run loom watched-roots status or loom watched-roots failures for source watcher state.")
		}
		return report, nil
	}

	report.Message = "Could not map this path to a LOOM storage view path."
	report.Next = append(report.Next, "Use a storage entry ID, a path under /Volumes/loom-storage, or a logical path such as main/Documents/file.pdf.")
	return report, nil
}

func completeStorageStatusReport(ctx context.Context, commandCtx commandContext, report storageStatusReport, detail storagecatalog.EntryDetail, viewEntry *storageview.ViewEntry) storageStatusReport {
	report.Entry = &detail.Entry
	report.PhysicalRefs = detail.PhysicalRefs
	if viewEntry != nil {
		report.ViewEntry = viewEntry
		report.Resolved.ViewPath = firstNonEmpty(report.Resolved.ViewPath, viewEntry.ViewPath)
	}
	report.Resolved.Ref = firstNonEmpty(report.Resolved.Ref, detail.Entry.StorageEntryID)
	report.State = humanStateForEntry(detail.Entry)
	if safe, err := commandCtx.Client.CheckStorageSafeToDelete(ctx, commandCtx.CorrelationID, storageretention.SafeToDeleteInput{Ref: detail.Entry.StorageEntryID}); err == nil {
		report.SafeToDelete = &safe.Data
		if safe.Data.Safe {
			report.State = storageHumanStateSafeToDelete
		} else if report.State == storageHumanStateAccepted {
			report.State = storageHumanStateNotSafeToDelete
		}
	}
	report.Message = storageStatusMessage(report)
	return report
}

func buildStorageVerificationReport(status storageStatusReport) storageVerificationReport {
	checks := []storageVerifyCheck{}
	add := func(name string, ok bool, message string) {
		state := "ok"
		if !ok {
			state = "failed"
		}
		checks = append(checks, storageVerifyCheck{Name: name, Status: state, Message: message})
	}
	add("resolved", status.Entry != nil || status.MainImport != nil, status.Message)
	if status.Entry != nil {
		add("catalog_entry", status.Entry.StorageEntryID != "", status.Entry.StorageEntryID)
		add("availability", status.Entry.AvailabilityState == storagecatalog.AvailabilityStateAvailable || status.Entry.AvailabilityState == storagecatalog.AvailabilityStateArchived || status.Entry.AvailabilityState == storagecatalog.AvailabilityStateSuperseded, status.Entry.AvailabilityState)
		add("processing", status.Entry.ProcessingState != storagecatalog.ProcessingStateFailed && status.Entry.ProcessingState != storagecatalog.ProcessingStateExcluded, status.Entry.ProcessingState)
		add("physical_ref", hasAvailablePhysicalRef(status.PhysicalRefs), fmt.Sprintf("%d physical ref(s)", len(status.PhysicalRefs)))
		if status.Entry.ChecksumHex != "" && status.Resolved.LocalPath != "" {
			if checksum, err := hashLocalFile(status.Resolved.LocalPath); err == nil {
				add("local_checksum", strings.EqualFold(checksum, status.Entry.ChecksumHex), "sha256:"+checksum)
			} else if !errors.Is(err, errStorageVerifyNotRegularFile) {
				add("local_checksum", false, err.Error())
			}
		}
	}
	if status.SafeToDelete != nil {
		add("safe_to_delete", status.SafeToDelete.Safe, status.SafeToDelete.Decision)
	}
	verified := true
	for _, check := range checks {
		if check.Status != "ok" {
			verified = false
			break
		}
	}
	return storageVerificationReport{Status: status, Checks: checks, Verified: verified, VerifiedAt: time.Now().UTC()}
}

func buildStorageFailuresReport(ctx context.Context, commandCtx commandContext, limit int) (storageFailuresReport, error) {
	if limit <= 0 {
		limit = 50
	}
	report := storageFailuresReport{GeneratedAt: time.Now().UTC(), Summary: map[string]int{}}
	entriesEnvelope, err := commandCtx.Client.ListStorageEntries(ctx, commandCtx.CorrelationID, storagecatalog.ListFilter{Limit: 5000, IncludeDeleted: true})
	if err != nil {
		return report, err
	}
	for _, entry := range entriesEnvelope.Data {
		if entry.AvailabilityState == storagecatalog.AvailabilityStateFailed || entry.ProcessingState == storagecatalog.ProcessingStateFailed || entry.ProcessingState == storagecatalog.ProcessingStateExcluded {
			report.Entries = append(report.Entries, entry)
		}
	}
	safetyCounts := storageCatalogSafetyCounts(entriesEnvelope.Data)
	sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].UpdatedAt.After(report.Entries[j].UpdatedAt) })
	if len(report.Entries) > limit {
		report.Entries = report.Entries[:limit]
	}
	if envelope, err := commandCtx.Client.GetMainDocumentsStatus(ctx, commandCtx.CorrelationID); err == nil {
		for _, item := range envelope.Data.Imports {
			switch item.State {
			case mainstorage.StateIgnored:
				if routineIgnoredMainDocument(item) {
					report.Summary["ignored_routine"]++
					continue
				}
				report.MainDocumentImports = append(report.MainDocumentImports, item)
			case mainstorage.StateFailedImport, mainstorage.StateMissingDeferred, mainstorage.StateTombstoned:
				report.MainDocumentImports = append(report.MainDocumentImports, item)
			}
		}
	}
	if envelope, err := commandCtx.Client.GetStorageFilesystemStatus(ctx, commandCtx.CorrelationID); err == nil {
		for _, root := range envelope.Data.Roots {
			if root.Status == "warning" || root.Status == "error" {
				report.FilesystemFindings = append(report.FilesystemFindings, filesystemFindingLite{Severity: root.Status, Kind: "physical_root", Path: root.Path, Summary: root.Key + ": " + root.Message})
			}
		}
	}
	if envelope, err := commandCtx.Client.ListFileTransfers(ctx, commandCtx.CorrelationID, filetransfer.ListFilter{Status: filetransfer.StatusFailed, Limit: limit}); err == nil {
		report.TransferFailures = envelope.Data
	}
	report.Summary["catalog"] = len(report.Entries)
	report.Summary["main_documents"] = len(report.MainDocumentImports)
	report.Summary["filesystem"] = len(report.FilesystemFindings)
	report.Summary["transfers"] = len(report.TransferFailures)
	report.Summary["total"] = report.Summary["catalog"] + report.Summary["main_documents"] + report.Summary["filesystem"] + report.Summary["transfers"]
	report.Summary["pending_backup"] = safetyCounts.PendingBackup
	report.Summary["failed_backup"] = safetyCounts.FailedBackup
	report.Summary["skipped_too_large"] = safetyCounts.SkippedTooLarge
	report.Summary["retained_or_snapshot"] = safetyCounts.RetainedOrSnapshot
	return report, nil
}

func buildStorageTransfersReport(ctx context.Context, commandCtx commandContext, filter filetransfer.ListFilter) (storageTransfersReport, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	report := storageTransfersReport{GeneratedAt: time.Now().UTC(), Summary: map[string]int{}}
	if envelope, err := commandCtx.Client.ListFileTransfers(ctx, commandCtx.CorrelationID, filter); err == nil {
		report.Transfers = envelope.Data
	}
	if envelope, err := commandCtx.Client.GetMainDocumentsStatus(ctx, commandCtx.CorrelationID); err == nil {
		report.MainDocumentImports = envelope.Data.Imports
	}
	entriesEnvelope, err := commandCtx.Client.ListStorageEntries(ctx, commandCtx.CorrelationID, storagecatalog.ListFilter{Limit: 5000})
	if err != nil {
		return report, err
	}
	for _, entry := range entriesEnvelope.Data {
		if entry.DropzoneTransferID != "" || entry.StorageClass == storagecatalog.StorageClassDropzoneCustody || entry.SourceArea == storagecatalog.SourceAreaDropzone || entry.StorageClass == storagecatalog.StorageClassMainDocument || entry.SourceArea == storagecatalog.SourceAreaMainDocuments {
			report.CatalogTransfers = append(report.CatalogTransfers, entry)
		}
	}
	report.Summary["file_transfers"] = len(report.Transfers)
	report.Summary["main_documents"] = len(report.MainDocumentImports)
	report.Summary["catalog_transfers"] = len(report.CatalogTransfers)
	report.Summary["total"] = report.Summary["file_transfers"] + report.Summary["main_documents"] + report.Summary["catalog_transfers"]
	return report, nil
}

func resolveStorageInput(cfg config.Config, input string) storageResolvedRef {
	input = strings.TrimSpace(input)
	resolved := storageResolvedRef{Input: input}
	if input == "" {
		return resolved
	}
	if isStorageEntryID(input) {
		resolved.Kind = "storage_entry_id"
		resolved.Ref = input
		return resolved
	}
	normalized := filepath.ToSlash(input)
	if filepath.IsAbs(input) {
		resolved.LocalPath = filepath.Clean(input)
		if viewPath, ok := stripStoragePathPrefix(normalized, filepath.ToSlash(cfg.StorageExport)); ok {
			resolved.Kind = "storage_export_path"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			return resolved
		}
		if viewPath, ok := stripStoragePathPrefix(normalized, "/Volumes/loom-storage"); ok {
			resolved.Kind = "smb_mount_path"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			return resolved
		}
		if viewPath, ok := stripStoragePathPrefix(normalized, "/Volumes/LOOM-Main"); ok {
			resolved.Kind = "smb_mount_path"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			return resolved
		}
		if viewPath, ok := stripStoragePathPrefix(normalized, "/Volumes/LOOM Main"); ok {
			resolved.Kind = "smb_mount_path"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			return resolved
		}
		if viewPath, ok := stripPathAfterMarker(normalized, "/loom-storage/"); ok {
			resolved.Kind = "human_storage_link"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			return resolved
		}
		if viewPath, ok := stripPathAfterMarker(normalized, "/LOOM Storage/"); ok {
			resolved.Kind = "human_storage_link"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			return resolved
		}
		if rel, ok := stripStoragePathPrefix(normalized, filepath.ToSlash(cfg.MainDocuments)); ok {
			resolved.Kind = "main_documents_backing_path"
			resolved.ViewPath = path.Join("main", "Documents", rel)
			resolved.Ref = resolved.ViewPath
			return resolved
		}
		if viewPath, note, ok := resolveBoxStoragePath(cfg, normalized); ok {
			resolved.Kind = "loom_box_path"
			resolved.ViewPath = viewPath
			resolved.Ref = viewPath
			if note != "" {
				resolved.Notes = append(resolved.Notes, note)
			}
			return resolved
		}
	}
	viewPath := strings.Trim(strings.ReplaceAll(input, "\\", "/"), "/")
	if viewPath != "" {
		resolved.Kind = "storage_view_path"
		resolved.ViewPath = viewPath
		resolved.Ref = viewPath
	}
	return resolved
}

func stripStoragePathPrefix(value, prefix string) (string, bool) {
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == prefix {
		return "", true
	}
	if strings.HasPrefix(value, prefix+"/") {
		return strings.Trim(strings.TrimPrefix(value, prefix+"/"), "/"), true
	}
	return "", false
}

func stripPathAfterMarker(value, marker string) (string, bool) {
	idx := strings.LastIndex(value, marker)
	if idx < 0 {
		return "", false
	}
	return strings.Trim(value[idx+len(marker):], "/"), true
}

func resolveBoxStoragePath(cfg config.Config, normalizedPath string) (string, string, bool) {
	boxPath := strings.TrimSpace(cfg.BoxPath)
	if boxPath == "" {
		return "", "", false
	}
	rel, ok := stripStoragePathPrefix(normalizedPath, filepath.ToSlash(boxPath))
	if !ok {
		return "", "", false
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return "", "Box root paths need a concrete Notes or Documents file to map into main backups.", false
	}
	nodeKey := strings.TrimSpace(cfg.NodeID)
	if nodeKey == "" {
		nodeKey = "unknown-node"
	}
	switch strings.ToLower(parts[0]) {
	case "documents":
		return path.Join(nodeKey, "Backups", "Documents", "current", path.Join(parts[1:]...)), "Mapped from local Box Documents backup policy.", true
	case "notes":
		return path.Join(nodeKey, "Backups", "Notes", "current", path.Join(parts[1:]...)), "Mapped from local Box Notes backup policy.", true
	default:
		return "", "", false
	}
}

func lookupMainDocumentImport(ctx context.Context, commandCtx commandContext, viewPath string) (mainstorage.FileStatus, bool) {
	if !strings.HasPrefix(strings.Trim(viewPath, "/"), "main/Documents/") {
		return mainstorage.FileStatus{}, false
	}
	rel := strings.TrimPrefix(strings.Trim(viewPath, "/"), "main/Documents/")
	envelope, err := commandCtx.Client.GetMainDocumentsStatus(ctx, commandCtx.CorrelationID)
	if err != nil {
		return mainstorage.FileStatus{}, false
	}
	for _, item := range envelope.Data.Imports {
		if strings.Trim(item.RelativePath, "/") == rel {
			return item, true
		}
	}
	return mainstorage.FileStatus{}, false
}

func loadMainDocumentsStatusForStatus(ctx context.Context, commandCtx commandContext) (mainstorage.Status, bool) {
	envelope, err := commandCtx.Client.GetMainDocumentsStatus(ctx, commandCtx.CorrelationID)
	if err != nil {
		return mainstorage.Status{}, false
	}
	return envelope.Data, true
}

func nextStepsForMainDocumentImport(state string) []string {
	switch state {
	case mainstorage.StatePendingImport:
		return []string{
			"Wait for the main Documents importer stable-file window, then run loom storage status again.",
			"Run loom storage main-documents status to inspect recent delayed imports.",
		}
	case mainstorage.StateFailedImport:
		return []string{
			"Run loom storage failures to inspect the import error.",
			"After correcting the source file or policy, rerun the main Documents importer.",
		}
	case mainstorage.StateMissingDeferred:
		return []string{
			"Run loom storage main-documents reconcile --dry-run to review the missing current row.",
			"Run loom storage main-documents reconcile --yes only after confirming retained bytes and cloud backup state.",
		}
	case mainstorage.StateTombstoned:
		return []string{
			"Run loom storage tree to confirm this current path no longer appears in the SMB view.",
			"Use retention/history or restore commands if you need to retrieve the prior bytes.",
		}
	default:
		return nil
	}
}

func isStorageEntryID(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), ids.StorageEntryPrefix+"_")
}

func humanStateForEntry(entry storagecatalog.Entry) string {
	switch entry.AvailabilityState {
	case storagecatalog.AvailabilityStateArchived:
		return storageHumanStateArchived
	case storagecatalog.AvailabilityStateSuperseded:
		return storageHumanStateSuperseded
	case storagecatalog.AvailabilityStateFailed:
		return storageHumanStateFailed
	case storagecatalog.AvailabilityStatePending, storagecatalog.AvailabilityStateDiscovered:
		return storageHumanStatePending
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateFailed {
		return storageHumanStateFailed
	}
	if entry.ProcessingState == storagecatalog.ProcessingStateExcluded {
		return storageHumanStateIgnored
	}
	if entry.AvailabilityState == storagecatalog.AvailabilityStateAvailable {
		return storageHumanStateAccepted
	}
	return storageHumanStateUnknown
}

func humanStateForMainImport(state string) string {
	switch state {
	case mainstorage.StateAccepted:
		return storageHumanStateAccepted
	case mainstorage.StatePendingImport:
		return storageHumanStatePending
	case mainstorage.StateFailedImport:
		return storageHumanStateFailed
	case mainstorage.StateIgnored:
		return storageHumanStateIgnored
	case mainstorage.StateMissingDeferred:
		return storageHumanStateMissingDeferred
	case mainstorage.StateTombstoned:
		return storageHumanStateTombstoned
	default:
		return storageHumanStateUnknown
	}
}

type storageSafetyCounts struct {
	PendingBackup      int
	FailedBackup       int
	SkippedTooLarge    int
	RetainedOrSnapshot int
}

func storageCatalogSafetyCounts(entries []storagecatalog.Entry) storageSafetyCounts {
	var counts storageSafetyCounts
	for _, entry := range entries {
		if entry.RetentionState == storagecatalog.RetentionStateRetained || entry.RetentionState == storagecatalog.RetentionStateSnapshot {
			counts.RetainedOrSnapshot++
		}
		if entry.StorageClass != storagecatalog.StorageClassPrivateBackup {
			continue
		}
		if entry.AvailabilityState == storagecatalog.AvailabilityStatePending || entry.AvailabilityState == storagecatalog.AvailabilityStateDiscovered {
			counts.PendingBackup++
		}
		if entry.AvailabilityState == storagecatalog.AvailabilityStateFailed || entry.ProcessingState == storagecatalog.ProcessingStateFailed {
			counts.FailedBackup++
		}
		if entry.ProcessingState == storagecatalog.ProcessingStateExcluded && strings.Contains(string(entry.Metadata), "backup_file_too_large") {
			counts.SkippedTooLarge++
		}
	}
	return counts
}

func routineIgnoredMainDocument(item mainstorage.FileStatus) bool {
	if item.State != mainstorage.StateIgnored {
		return false
	}
	reason := strings.ToLower(strings.TrimSpace(item.IgnoredReason))
	switch {
	case strings.Contains(reason, "appledouble"):
		return true
	case strings.Contains(reason, "finder metadata"):
		return true
	case strings.Contains(reason, "platform metadata"):
		return true
	case strings.Contains(reason, "office lock"):
		return true
	case strings.Contains(reason, "temporary"):
		return true
	case strings.Contains(reason, "placeholder sidecar"):
		return true
	}
	base := path.Base(strings.TrimSpace(item.RelativePath))
	lower := strings.ToLower(base)
	switch {
	case base == ".DS_Store":
		return true
	case strings.HasPrefix(base, "._"):
		return true
	case strings.HasPrefix(base, "~$") || strings.HasPrefix(base, ".~lock."):
		return true
	case strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".download") || strings.HasSuffix(lower, ".crdownload"):
		return true
	case strings.HasSuffix(lower, ".icloud"):
		return true
	case strings.HasPrefix(lower, ".nfs") || strings.HasPrefix(lower, ".smbdelete"):
		return true
	default:
		return false
	}
}

func storageStatusMessage(report storageStatusReport) string {
	if report.SafeToDelete != nil && report.SafeToDelete.Safe {
		return "Accepted by main and safe to delete from the source side."
	}
	if report.SafeToDelete != nil && !report.SafeToDelete.Safe && len(report.SafeToDelete.Blockers) > 0 {
		return strings.Join(report.SafeToDelete.Blockers, "; ")
	}
	if report.Entry != nil {
		return fmt.Sprintf("Catalog entry is %s/%s.", report.Entry.AvailabilityState, report.Entry.ProcessingState)
	}
	return report.Message
}

func hasAvailablePhysicalRef(refs []storagecatalog.PhysicalRef) bool {
	for _, ref := range refs {
		if ref.Status == "" || ref.Status == storagecatalog.PhysicalRefStatusAvailable {
			return true
		}
	}
	return false
}

var errStorageVerifyNotRegularFile = errors.New("not a regular file")

func hashLocalFile(localPath string) (string, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errStorageVerifyNotRegularFile
	}
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func renderStorageStatusReport(cmd *cobra.Command, report storageStatusReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage status: %s\n", report.State)
	fmt.Fprintf(cmd.OutOrStdout(), "Input: %s\n", report.Input)
	fmt.Fprintf(cmd.OutOrStdout(), "Resolved: %s", dashIfEmpty(report.Resolved.Ref))
	if report.Resolved.Kind != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " (%s)", report.Resolved.Kind)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	if report.Resolved.LocalPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Local path: %s\n", report.Resolved.LocalPath)
	}
	if report.Entry != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Storage entry: %s\n", report.Entry.StorageEntryID)
		fmt.Fprintf(cmd.OutOrStdout(), "View path: %s\n", firstNonEmpty(report.Entry.CurrentViewPath, report.Resolved.ViewPath, report.Entry.LogicalPath))
		fmt.Fprintf(cmd.OutOrStdout(), "Class: %s source=%s node=%s\n", report.Entry.StorageClass, report.Entry.SourceArea, dashIfEmpty(report.Entry.OriginNodeKey))
		fmt.Fprintf(cmd.OutOrStdout(), "State: availability=%s processing=%s retention=%s\n", report.Entry.AvailabilityState, report.Entry.ProcessingState, report.Entry.RetentionState)
		fmt.Fprintf(cmd.OutOrStdout(), "Physical refs: %d\n", len(report.PhysicalRefs))
	}
	if report.MainImport != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Main import: %s %s\n", report.MainImport.State, report.MainImport.RelativePath)
	}
	if report.SafeToDelete != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Safe to delete: %s\n", report.SafeToDelete.Decision)
	}
	if report.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", report.Message)
	}
	for _, note := range report.Resolved.Notes {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: %s\n", note)
	}
	if len(report.Next) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Next:")
		for _, next := range report.Next {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", next)
		}
	}
}

func renderStorageVerificationReport(cmd *cobra.Command, report storageVerificationReport) {
	status := "not_verified"
	if report.Verified {
		status = "verified"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Storage verification: %s\n", status)
	renderStorageStatusReport(cmd, report.Status)
	fmt.Fprintln(cmd.OutOrStdout(), "Checks:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tCHECK\tMESSAGE")
	for _, check := range report.Checks {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Status, check.Name, dashIfEmpty(check.Message))
	}
	_ = writer.Flush()
}

func renderStorageFailuresReport(cmd *cobra.Command, report storageFailuresReport) {
	fmt.Fprintln(cmd.OutOrStdout(), "LOOM storage failures")
	fmt.Fprintf(cmd.OutOrStdout(), "Total: %d catalog=%d main_documents=%d filesystem=%d transfers=%d\n",
		report.Summary["total"], report.Summary["catalog"], report.Summary["main_documents"], report.Summary["filesystem"], report.Summary["transfers"])
	fmt.Fprintf(cmd.OutOrStdout(), "Coverage: pending_backup=%d failed_backup=%d skipped_too_large=%d retained_or_snapshot=%d\n",
		report.Summary["pending_backup"],
		report.Summary["failed_backup"],
		report.Summary["skipped_too_large"],
		report.Summary["retained_or_snapshot"],
	)
	if report.Summary["ignored_routine"] > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Hidden routine ignored files: %d\n", report.Summary["ignored_routine"])
	}
	if len(report.Entries) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Catalog:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATE\tENTRY\tCLASS\tPATH")
		for _, entry := range report.Entries {
			fmt.Fprintf(writer, "%s/%s\t%s\t%s\t%s\n", entry.AvailabilityState, entry.ProcessingState, entry.StorageEntryID, entry.StorageClass, firstNonEmpty(entry.CurrentViewPath, entry.LogicalPath))
		}
		_ = writer.Flush()
	}
	if len(report.MainDocumentImports) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Main Documents imports:")
		for _, item := range report.MainDocumentImports {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s\n", item.State, item.RelativePath, firstNonEmpty(item.Error, item.IgnoredReason, item.DelayReason))
		}
	}
	if len(report.FilesystemFindings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Canonical filesystem findings:")
		for _, finding := range report.FilesystemFindings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s %s\n", finding.Severity, finding.Kind, dashIfEmpty(finding.Path), finding.Summary)
		}
	}
	if len(report.TransferFailures) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "File transfers:")
		for _, transfer := range report.TransferFailures {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s\n", transfer.Manifest.Status, transfer.Manifest.TransferID, firstNonEmpty(transfer.Manifest.LastErrorMessage, transfer.Manifest.SourceRelativePath))
		}
	}
}

func renderStorageTransfersReport(cmd *cobra.Command, report storageTransfersReport) {
	fmt.Fprintln(cmd.OutOrStdout(), "LOOM storage transfers")
	fmt.Fprintf(cmd.OutOrStdout(), "Total: %d file_transfers=%d main_documents=%d catalog_transfers=%d\n",
		report.Summary["total"], report.Summary["file_transfers"], report.Summary["main_documents"], report.Summary["catalog_transfers"])
	if len(report.Transfers) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "File transfers:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATUS\tTRANSFER\tKIND\tNODE\tPATH\tCHUNKS")
		for _, transfer := range report.Transfers {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%d/%d\n",
				transfer.Manifest.Status,
				transfer.Manifest.TransferID,
				transfer.Manifest.TransferKind,
				dashIfEmpty(transfer.Manifest.SourceNodeKey),
				firstNonEmpty(transfer.Manifest.SourceRelativePath, transfer.Manifest.DestinationLogicalPath),
				transfer.AcceptedChunks,
				transfer.Manifest.ChunkCount,
			)
		}
		_ = writer.Flush()
	}
	if len(report.MainDocumentImports) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Recent main/Documents imports:")
		for _, item := range report.MainDocumentImports {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s\n", item.State, item.RelativePath, firstNonEmpty(item.StorageEntryID, item.DelayReason, item.IgnoredReason, item.Error))
		}
	}
	if len(report.CatalogTransfers) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Accepted catalog transfer entries:")
		for _, entry := range report.CatalogTransfers {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s\n", humanStateForEntry(entry), entry.StorageEntryID, firstNonEmpty(entry.CurrentViewPath, entry.LogicalPath))
		}
	}
}
