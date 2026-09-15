package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
)

func (s Server) handleRetiredStorageTree(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	s.writeError(w, correlationID, http.StatusGone, "storage.tree_retired", "storage", "tree", "The generated storage tree is retired; use /v1/storage/entries or /v1/storage/filesystem/status.", nil)
}

func (s Server) handleStorageFilesystemStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	const limit = 500
	var entries []storagecatalog.Entry
	var err error
	if s.services.StorageCatalog.DB == nil {
		err = errors.New("storage catalog is not configured")
	} else {
		entries, err = s.services.StorageCatalog.ListEntries(ctx, storagecatalog.ListFilter{Limit: limit, IncludeDeleted: true})
	}
	status := storagedoctor.BuildFilesystemStatus(s.services.RuntimeConfig, entries, limit, err)
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleStorageEntries(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := storageEntryFilterFromRequest(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.invalid_filter", "storage", "filter", "Storage filter is invalid.", err)
		return
	}
	entries, err := s.services.StorageCatalog.ListEntries(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "storage.entries_failed", "storage", "entries", "Could not list storage catalog entries.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, entries))
}

func (s Server) handleStorageEntry(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if strings.HasSuffix(r.URL.Path, "/physical-refs") {
		s.handleStoragePhysicalRefs(w, r)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/storage/entries/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "storage.ref_required", "storage", "entry", "Storage entry reference is required.", nil)
		return
	}
	entry, err := s.services.StorageCatalog.InspectEntry(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "storage", ref, "Storage entry was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, entry))
}

func (s Server) handleStoragePhysicalRefs(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	refPath := strings.TrimSuffix(r.URL.Path, "/physical-refs")
	storageEntryID := pathRef(refPath, "/v1/storage/entries/")
	if storageEntryID == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "storage.ref_required", "storage", "entry", "Storage entry reference is required.", nil)
		return
	}
	var input storagecatalog.RegisterPhysicalRefInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.physical_ref_invalid", "storage", "physical_ref", "Storage physical ref request is invalid.", err)
		return
	}
	if strings.TrimSpace(input.StorageEntryID) == "" {
		input.StorageEntryID = storageEntryID
	}
	if strings.TrimSpace(input.StorageEntryID) != storageEntryID {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.physical_ref_entry_mismatch", "storage", storageEntryID, "Storage physical ref entry does not match the route.", nil)
		return
	}
	physicalRef, err := s.services.StorageCatalog.RegisterPhysicalRef(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.physical_ref_register_failed", "storage", storageEntryID, "Could not register storage physical ref.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, physicalRef))
}

func (s Server) handleStorageResolve(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	query := r.URL.Query()
	pathValue := strings.TrimSpace(query.Get("path"))
	if pathValue == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.path_required", "storage", "path", "Storage view path is required.", nil)
		return
	}
	includeDeleted, _ := strconv.ParseBool(strings.TrimSpace(query.Get("include_deleted")))
	detail, err := s.services.StorageCatalog.InspectByViewPath(ctx, pathValue, storagecatalog.InspectOptions{IncludeDeleted: includeDeleted})
	if err != nil {
		s.writeLookupError(w, correlationID, "storage", pathValue, "Storage path was not found.", err)
		return
	}
	viewEntry, err := storageview.CatalogViewEntry(detail.Entry)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "storage.path_invalid", "storage", pathValue, "Storage path could not be represented.", err)
		return
	}
	result := storageview.ResolveResult{Path: pathValue, ViewEntry: viewEntry, EntryDetail: &detail}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageInspectPath(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	pathValue := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathValue == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.path_required", "storage", "path", "Storage view path is required.", nil)
		return
	}
	if strings.HasPrefix(strings.Trim(pathValue, "/"), "main/Documents/") {
		result, err := s.services.StorageRetention.MainDocumentProtection(ctx, storagecatalog.MainDocumentProtectionInput{ViewPath: pathValue})
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "storage.inspect_path_failed", "storage", pathValue, "Could not inspect storage path.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
		return
	}
	detail, err := s.services.StorageCatalog.InspectByViewPath(ctx, pathValue, storagecatalog.InspectOptions{IncludeDeleted: true})
	if err != nil {
		s.writeLookupError(w, correlationID, "storage", pathValue, "Storage path was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleMainDocumentsStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.MainStorage.Status(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "storage.main_documents_status_failed", "storage", "main_documents", "Could not read main Documents import status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleMainDocumentsReconcile(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input mainstorage.ReconcileInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.main_documents_reconcile_invalid", "storage", "main_documents", "Main Documents reconcile request is invalid.", err)
		return
	}
	result, err := s.services.MainStorage.Reconcile(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "storage.main_documents_reconcile_failed", "storage", "main_documents", "Could not reconcile main Documents catalog state.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleMainDocumentsRetentionBackfill(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input mainstorage.RetentionBackfillInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.main_documents_retention_backfill_invalid", "storage", "main_documents", "Main Documents retention backfill request is invalid.", err)
		return
	}
	result, err := s.services.MainStorage.BackfillRetention(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "storage.main_documents_retention_backfill_failed", "storage", "main_documents", "Could not backfill main Documents retention payloads.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleMainDocumentsProtection(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storagecatalog.MainDocumentProtectionInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.main_documents_protection_invalid", "storage", "main_documents", "Main Documents protection request is invalid.", err)
		return
	}
	result, err := s.services.StorageRetention.MainDocumentProtection(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.main_documents_protection_failed", "storage", "main_documents", "Could not inspect main Documents protection state.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleMainDocumentsSafeDelete(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storagecatalog.MainDocumentProtectionInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.main_documents_safe_delete_invalid", "storage", "main_documents", "Main Documents safe-delete request is invalid.", err)
		return
	}
	result, err := s.services.StorageRetention.MainDocumentProtection(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.main_documents_safe_delete_failed", "storage", "main_documents", "Could not inspect main Documents safe-delete state.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageExportStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status := storagedoctor.BuildFilesystemStatus(s.services.RuntimeConfig, nil, 1, nil).Export
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleStorageExportRefresh(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	if r.Method == http.MethodGet {
		status := storagedoctor.BuildFilesystemStatus(s.services.RuntimeConfig, nil, 1, nil).Export
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
		return
	}
	s.writeError(w, correlationID, http.StatusGone, "storage.export_retired", "storage", "export", "Storage export refresh is retired and cannot mutate filesystem state.", nil)
}

func (s Server) handleLaneAccept(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input lane.AcceptInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.lane_accept_invalid", "storage", "lane", "Lane accept request is invalid.", err)
		return
	}
	// Final custody is an installation contract, never a caller-selected path.
	input = trustedLaneAcceptInput(input, s.services.RuntimeConfig.ImportsRoot, s.services.RuntimeConfig.DataDir)
	result, err := lane.Accept(ctx, s.services.StorageCatalog, input)
	if err != nil {
		code := laneAcceptErrorCode(err)
		s.writeError(w, correlationID, http.StatusBadRequest, code, "storage", "lane", "Could not accept LOOM Lane batch.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func trustedLaneAcceptInput(input lane.AcceptInput, importsRoot, dataDir string) lane.AcceptInput {
	input.ImportsRoot = importsRoot
	if strings.TrimSpace(dataDir) == "" {
		input.RemoteRoot = lane.DefaultRemoteRoot
	} else {
		input.RemoteRoot = filepath.Join(dataDir, "lane")
	}
	input.TrustedRemoteRoot = true
	return input
}

func laneAcceptErrorCode(err error) string {
	code := "storage.lane_accept_failed"
	var phaseErr *lane.AcceptPhaseError
	if errors.As(err, &phaseErr) {
		code = "storage.lane_" + phaseErr.Phase + "_failed"
	}
	return code
}

func (s Server) handleStorageExportRebuild(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	s.writeError(w, correlationID, http.StatusGone, "storage.export_retired", "storage", "export", "Storage export rebuild is retired and cannot mutate filesystem state.", nil)
}

func (s Server) handleStorageRetentionStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.StorageRetention.Status(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "storage.retention_status_failed", "storage", "retention", "Could not read storage retention status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleStorageFidelityBackfill(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	service := s.services.StorageFidelity
	if service.Catalog == nil {
		service = storagefidelity.NewBackfillService(s.services.StorageCatalog)
	}
	var input storagefidelity.BackfillInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.fidelity_backfill_invalid", "storage", "fidelity", "Storage fidelity backfill request is invalid.", err)
		return
	}
	result, err := service.Backfill(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.fidelity_backfill_failed", "storage", "fidelity", "Could not backfill storage fidelity metadata.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageArchive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storagearchive.ArchiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.archive_invalid", "storage", "archive", "Storage archive request is invalid.", err)
		return
	}
	result, err := s.services.StorageArchive.Archive(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.archive_failed", "storage", "archive", "Could not archive storage subtree.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageSafeToDelete(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storageretention.SafeToDeleteInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.safe_to_delete_invalid", "storage", "safe-to-delete", "Safe-to-delete request is invalid.", err)
		return
	}
	result, err := s.services.StorageRetention.SafeToDelete(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.safe_to_delete_failed", "storage", "safe-to-delete", "Could not check safe-to-delete state.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageFetch(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storageretention.FetchInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.fetch_invalid", "storage", "fetch", "Storage fetch request is invalid.", err)
		return
	}
	result, err := s.services.StorageRetention.Fetch(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.fetch_failed", "storage", "fetch", "Could not fetch retained storage file.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageRestore(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storageretention.RestoreInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.restore_invalid", "storage", "restore", "Storage restore request is invalid.", err)
		return
	}
	result, err := s.services.StorageRetention.Restore(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.restore_failed", "storage", "restore", "Could not restore retained storage file.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleStorageTombstone(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "storage", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storageretention.RecordTombstoneInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.tombstone_invalid", "storage", "tombstone", "Storage tombstone request is invalid.", err)
		return
	}
	result, err := s.services.StorageRetention.RecordTombstone(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "storage.tombstone_failed", "storage", "tombstone", "Could not create storage tombstone.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func decodeStorageOperationInput(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func storageEntryFilterFromRequest(r *http.Request, defaultLimit int) (storagecatalog.ListFilter, error) {
	limit, err := parseLimit(r, defaultLimit)
	if err != nil {
		return storagecatalog.ListFilter{}, err
	}
	query := r.URL.Query()
	return storagecatalog.ListFilter{
		StorageClass:      firstQuery(query.Get("storage_class"), query.Get("class")),
		SourceArea:        firstQuery(query.Get("source_area"), query.Get("source")),
		OriginNodeKey:     firstQuery(query.Get("origin_node"), query.Get("node")),
		FileClass:         query.Get("file_class"),
		ProcessingState:   query.Get("processing_state"),
		AvailabilityState: query.Get("availability_state"),
		IncludeDeleted:    parseBoolQuery(query, "include_deleted"),
		Limit:             limit,
	}, nil
}

func firstQuery(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
