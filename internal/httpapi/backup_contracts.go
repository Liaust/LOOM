package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/box"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

var errBackupContractResponseWritten = errors.New("backup contract response already written")

func (s Server) handleBackupContracts(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	resolved, directoryRelPath, err := s.resolveBackupContractsBox()
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.box_unavailable", "backup", "contracts", "Could not resolve the configured Box for backup contracts.", err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
		items, err := backupcontracts.NewStatusService(s.services.DB).List(r.Context(), resolved.RootPath, directoryRelPath, backupcontracts.ProtectedFolderFilter{Lifecycle: r.URL.Query().Get("status"), NodeRef: r.URL.Query().Get("node"), Limit: limit})
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "backup_contracts.list_failed", "backup", "contracts", "Could not list backup contracts.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
	case http.MethodPost:
		var input backupcontracts.CreateRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "backup", "contracts", "Request body is not valid backup contract JSON.", err)
			return
		}
		result, err := s.createBackupContract(w, r, resolved, directoryRelPath, input)
		if errors.Is(err, errBackupContractResponseWritten) {
			return
		}
		if err != nil {
			status := backupContractMutationErrorStatus(err)
			s.writeError(w, correlationID, status, "backup_contracts.create_failed", "backup", input.Key, "Could not create backup contract.", err)
			return
		}
		status := http.StatusOK
		if result.NodeApplyQueued {
			status = http.StatusAccepted
		}
		response.WriteJSON(w, status, response.Success(correlationID, result))
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleBackupContract(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	ref := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/backup/contracts/"), "/")
	if ref == "preflights" || strings.HasPrefix(ref, "preflights/") {
		s.handleBackupContractPreflights(w, r, ref)
		return
	}
	resolved, directoryRelPath, err := s.resolveBackupContractsBox()
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.box_unavailable", "backup", "contracts", "Could not resolve the configured Box for backup contracts.", err)
		return
	}
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "backup_contracts.not_found", "backup", "contracts", "Backup contract was not found.", nil)
		return
	}
	if ref == "migrate-ignore-policy" {
		s.handleBackupContractIgnorePolicyMigration(w, r, resolved, directoryRelPath)
		return
	}
	if strings.HasSuffix(ref, "/disable") {
		key := strings.TrimSuffix(ref, "/disable")
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input backupcontracts.DisableRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "backup", key, "Request body is not valid backup contract disable JSON.", err)
			return
		}
		result, err := s.disableBackupContract(w, r, resolved, directoryRelPath, key, input)
		if errors.Is(err, errBackupContractResponseWritten) {
			return
		}
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.disable_failed", "backup", key, "Could not disable backup contract.", err)
			return
		}
		status := http.StatusOK
		if result.NodeApplyQueued {
			status = http.StatusAccepted
		}
		response.WriteJSON(w, status, response.Success(correlationID, result))
		return
	}
	if strings.HasSuffix(ref, "/enable") {
		key := strings.TrimSuffix(ref, "/enable")
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input backupcontracts.EnableRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "backup", key, "Request body is not valid backup contract enable JSON.", err)
			return
		}
		result, err := s.enableBackupContract(w, r, resolved, directoryRelPath, key, input)
		if errors.Is(err, errBackupContractResponseWritten) {
			return
		}
		if err != nil {
			s.writeError(w, correlationID, backupContractMutationErrorStatus(err), "backup_contracts.enable_failed", "backup", key, "Could not enable backup contract.", err)
			return
		}
		status := http.StatusOK
		if result.NodeApplyQueued {
			status = http.StatusAccepted
		}
		response.WriteJSON(w, status, response.Success(correlationID, result))
		return
	}
	if strings.HasSuffix(ref, "/preflight") || strings.HasSuffix(ref, "/recheck") {
		suffix := "/preflight"
		if strings.HasSuffix(ref, "/recheck") {
			suffix = "/recheck"
		}
		key := strings.TrimSuffix(ref, suffix)
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		record, err := s.preflightBackupContract(r, resolved, directoryRelPath, key, suffix == "/recheck")
		if err != nil {
			s.writeError(w, correlationID, backupContractMutationErrorStatus(err), "backup_contracts.preflight_create_failed", "backup", key, "Could not request protected-folder preflight.", err)
			return
		}
		response.WriteJSON(w, http.StatusAccepted, response.Success(correlationID, record))
		return
	}
	if strings.HasSuffix(ref, "/retry-activation") {
		key := strings.TrimSuffix(ref, "/retry-activation")
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		path, err := backupcontracts.ContractFilePath(resolved.RootPath, directoryRelPath, key)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.invalid_key", "backup", key, "Backup contract key is invalid.", err)
			return
		}
		contract, _, err := backupcontracts.LoadFile(path)
		if err != nil {
			s.writeError(w, correlationID, http.StatusNotFound, "backup_contracts.not_found", "backup", key, "Backup contract was not found.", err)
			return
		}
		_, ctx := requestMeta(r)
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		queued, err := backupcontracts.NewReconcileService(s.services.DB).RetryNode(ctx, req, contract.OwnerNode)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.retry_activation_failed", "backup", key, "Could not retry protected-folder activation.", err)
			return
		}
		response.WriteJSON(w, http.StatusAccepted, response.Success(correlationID, queued))
		return
	}
	switch r.Method {
	case http.MethodGet:
		record, err := backupcontracts.NewStatusService(s.services.DB).Get(r.Context(), resolved.RootPath, directoryRelPath, ref)
		if err != nil {
			s.writeError(w, correlationID, http.StatusNotFound, "backup_contracts.not_found", "backup", ref, "Backup contract was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, record))
	case http.MethodDelete:
		var input backupcontracts.DeleteRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "backup", ref, "Request body is not valid backup contract delete JSON.", err)
			return
		}
		result, err := s.deleteBackupContract(w, r, resolved, directoryRelPath, ref, input)
		if errors.Is(err, errBackupContractResponseWritten) {
			return
		}
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.delete_failed", "backup", ref, "Could not delete backup contract.", err)
			return
		}
		status := http.StatusOK
		if result.NodeApplyQueued {
			status = http.StatusAccepted
		}
		response.WriteJSON(w, status, response.Success(correlationID, result))
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleBackupContractIgnorePolicyMigration(w http.ResponseWriter, r *http.Request, resolved box.Resolved, directoryRelPath string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input backupcontracts.MigrateIgnorePolicyRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "backup", "migrate-ignore-policy", "Request body is not valid backup contract migration JSON.", err)
		return
	}
	migrationInput := backupcontracts.MigrateIgnorePolicyInput{
		BoxRoot:          resolved.RootPath,
		DirectoryRelPath: directoryRelPath,
		DryRun:           input.DryRun,
		Apply:            input.Apply,
		Yes:              input.Yes,
	}
	if !input.Apply {
		result, err := backupcontracts.MigrateIgnorePolicy(migrationInput)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.migration_failed", "backup", "migrate-ignore-policy", "Could not plan backup contract ignore-policy migration.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, backupcontracts.MigrateIgnorePolicyResponse{Migration: result}))
		return
	}
	req, idemRecord, proceed, err := s.beginBackupContractMutation(w, r, ctx, correlationID, "backup.contract.migrate_ignore_policy", map[string]any{"input": input})
	if err != nil || !proceed {
		return
	}
	result, err := backupcontracts.MigrateIgnorePolicy(migrationInput)
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.migration_failed", "backup", "migrate-ignore-policy", "Could not apply backup contract ignore-policy migration.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, idemErr.Code, "backup", "migrate-ignore-policy", idemErr.Summary, err)
		return
	}
	applyResult, err := s.services.Box.ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Resolved: resolved})
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.apply_failed", "backup", "migrate-ignore-policy", "Contracts were migrated but watch-plan apply failed.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusInternalServerError, idemErr.Code, "backup", "migrate-ignore-policy", idemErr.Summary, err)
		return
	}
	output := backupcontracts.MigrateIgnorePolicyResponse{Migration: result, WatchedRoots: applyResult.Plan.WatchedRoots, Reconciled: true}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, output)
	s.completeIdempotency(ctx, idemRecord, "backup_contract_migration", "ignore-policy", envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) createBackupContract(w http.ResponseWriter, r *http.Request, resolved box.Resolved, directoryRelPath string, input backupcontracts.CreateRequest) (backupcontracts.LifecycleResult, error) {
	correlationID, ctx := requestMeta(r)
	contract := backupcontracts.ContractFromCreateRequest(input, resolved.OwnerNode)
	if strings.TrimSpace(input.PreflightID) != "" {
		if err := backupcontracts.NewPreflightService(s.services.DB).ValidateForContract(ctx, input.PreflightID, contract); err != nil {
			return backupcontracts.LifecycleResult{}, err
		}
	}
	mutation := backupcontracts.MutateInput{
		BoxRoot:          resolved.RootPath,
		DirectoryRelPath: directoryRelPath,
		Contract:         contract,
		Replace:          input.Replace,
		DryRun:           input.DryRun,
		Actor:            "loom backup contracts create",
	}
	if input.DryRun {
		result, err := backupcontracts.Create(mutation)
		if err != nil {
			return backupcontracts.LifecycleResult{}, err
		}
		return lifecycleResultFromMutation(result, plannedWatchedRootForCreate(result.Contract, result.Path, resolved)), nil
	}
	req, idemRecord, proceed, err := s.beginBackupContractMutation(w, r, ctx, correlationID, "backup.contract.create", map[string]any{"input": input})
	if err != nil || !proceed {
		return backupcontracts.LifecycleResult{}, errBackupContractResponseWritten
	}
	if err := prepareBoxForBackupContracts(resolved); err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.box_init_failed", "backup", resolved.RootPath, "Could not prepare Box metadata for backup contracts.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	result, err := backupcontracts.Create(mutation)
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.create_failed", "backup", input.Key, "Could not write backup contract.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	lifecycle, err := s.applyBackupContractWatchPlan(ctx, req, resolved, lifecycleResultFromMutation(result, nil))
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.apply_failed", "backup", input.Key, "Backup contract was written but watch-plan apply failed.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, lifecycle)
	s.completeIdempotency(ctx, idemRecord, "backup_contract", lifecycle.Contract.Key, envelope)
	return lifecycle, nil
}

func prepareBoxForBackupContracts(resolved box.Resolved) error {
	status := box.Inspect(resolved)
	if status.Initialized && status.ContractState == "valid" {
		return nil
	}
	_, err := box.Init(box.InitInput{Resolved: resolved})
	return err
}

func (s Server) handleBackupContractPreflights(w http.ResponseWriter, r *http.Request, ref string) {
	correlationID, ctx := requestMeta(r)
	service := backupcontracts.NewPreflightService(s.services.DB)
	trimmed := strings.TrimPrefix(ref, "preflights")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		switch r.Method {
		case http.MethodPost:
			var input backupcontracts.PreflightCreateRequest
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "backup", "preflights", "Request body is not valid protected-folder preflight JSON.", err)
				return
			}
			if input.IdempotencyKey == "" {
				input.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			}
			req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
			if err != nil {
				s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
				return
			}
			record, err := service.Create(ctx, req, input)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.preflight_create_failed", "backup", "preflights", "Could not request protected-folder preflight.", err)
				return
			}
			status := http.StatusOK
			if record.Status == backupcontracts.PreflightStatusPending {
				status = http.StatusAccepted
			}
			response.WriteJSON(w, status, response.Success(correlationID, record))
		case http.MethodGet:
			limit := 50
			if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
				if parsed, err := strconv.Atoi(raw); err == nil {
					limit = parsed
				}
			}
			records, err := service.List(ctx, backupcontracts.PreflightFilter{NodeRef: r.URL.Query().Get("node"), Status: r.URL.Query().Get("status"), Limit: limit})
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.preflight_list_failed", "backup", "preflights", "Could not list protected-folder preflights.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, records))
		default:
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
		}
		return
	}
	if strings.HasSuffix(trimmed, "/retry") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		preflightID := strings.TrimSuffix(trimmed, "/retry")
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		record, err := service.Retry(ctx, req, preflightID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "backup_contracts.preflight_retry_failed", "backup", preflightID, "Could not retry protected-folder preflight.", err)
			return
		}
		response.WriteJSON(w, http.StatusAccepted, response.Success(correlationID, record))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	record, err := service.Get(ctx, trimmed)
	if err != nil {
		s.writeError(w, correlationID, http.StatusNotFound, "backup_contracts.preflight_not_found", "backup", trimmed, "Protected-folder preflight was not found.", err)
		return
	}
	status := http.StatusOK
	if record.Status == backupcontracts.PreflightStatusPending {
		status = http.StatusAccepted
	}
	response.WriteJSON(w, status, response.Success(correlationID, record))
}

func (s Server) disableBackupContract(w http.ResponseWriter, r *http.Request, resolved box.Resolved, directoryRelPath string, key string, input backupcontracts.DisableRequest) (backupcontracts.LifecycleResult, error) {
	correlationID, ctx := requestMeta(r)
	mutation := backupcontracts.DisableInput{BoxRoot: resolved.RootPath, DirectoryRelPath: directoryRelPath, Key: key, DryRun: input.DryRun, Actor: "loom backup contracts disable"}
	if input.DryRun {
		result, err := backupcontracts.Disable(mutation)
		return lifecycleResultFromMutation(result, nil), err
	}
	req, idemRecord, proceed, err := s.beginBackupContractMutation(w, r, ctx, correlationID, "backup.contract.disable", map[string]any{"key": key})
	if err != nil || !proceed {
		return backupcontracts.LifecycleResult{}, errBackupContractResponseWritten
	}
	result, err := backupcontracts.Disable(mutation)
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.disable_failed", "backup", key, "Could not disable backup contract.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	lifecycle, err := s.applyBackupContractWatchPlan(ctx, req, resolved, lifecycleResultFromMutation(result, nil))
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.apply_failed", "backup", key, "Backup contract was disabled but watch-plan apply failed.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	if err := s.recordBackupContractLifecycleEvent(ctx, req, events.TypeBackupContractDisabled, key, lifecycle); err != nil {
		return backupcontracts.LifecycleResult{}, err
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, lifecycle)
	s.completeIdempotency(ctx, idemRecord, "backup_contract", lifecycle.Contract.Key, envelope)
	return lifecycle, nil
}

func (s Server) enableBackupContract(w http.ResponseWriter, r *http.Request, resolved box.Resolved, directoryRelPath string, key string, input backupcontracts.EnableRequest) (backupcontracts.LifecycleResult, error) {
	correlationID, ctx := requestMeta(r)
	mutation := backupcontracts.EnableInput{BoxRoot: resolved.RootPath, DirectoryRelPath: directoryRelPath, Key: key, DryRun: input.DryRun, Actor: "loom backup contracts enable"}
	if input.DryRun {
		result, err := backupcontracts.Enable(mutation)
		return lifecycleResultFromMutation(result, nil), err
	}
	req, idemRecord, proceed, err := s.beginBackupContractMutation(w, r, ctx, correlationID, "backup.contract.enable", map[string]any{"key": key})
	if err != nil || !proceed {
		return backupcontracts.LifecycleResult{}, errBackupContractResponseWritten
	}
	result, err := backupcontracts.Enable(mutation)
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.enable_failed", "backup", key, "Could not enable backup contract.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	lifecycle, err := s.applyBackupContractWatchPlan(ctx, req, resolved, lifecycleResultFromMutation(result, nil))
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.apply_failed", "backup", key, "Backup contract was enabled but node work could not be queued.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	if err := s.recordBackupContractLifecycleEvent(ctx, req, events.TypeBackupContractEnabled, key, lifecycle); err != nil {
		return backupcontracts.LifecycleResult{}, err
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, lifecycle)
	s.completeIdempotency(ctx, idemRecord, "backup_contract", lifecycle.Contract.Key, envelope)
	return lifecycle, nil
}

func (s Server) preflightBackupContract(r *http.Request, resolved box.Resolved, directoryRelPath, key string, recheck bool) (backupcontracts.PreflightRecord, error) {
	correlationID, ctx := requestMeta(r)
	path, err := backupcontracts.ContractFilePath(resolved.RootPath, directoryRelPath, key)
	if err != nil {
		return backupcontracts.PreflightRecord{}, err
	}
	contract, _, err := backupcontracts.LoadFile(path)
	if err != nil {
		return backupcontracts.PreflightRecord{}, err
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		return backupcontracts.PreflightRecord{}, err
	}
	input := backupcontracts.PreflightCreateRequest{
		NodeRef: contract.OwnerNode, Path: contract.Target.Path, Ignore: contract.Ignore,
		Include: contract.Include, Exclude: contract.Exclude,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	}
	if recheck {
		identity := backupcontracts.RecheckIdentityForContractKey(contract.Key)
		input.Recheck = &identity
	}
	return backupcontracts.NewPreflightService(s.services.DB).Create(ctx, req, input)
}

func backupContractMutationErrorStatus(err error) int {
	value := strings.ToLower(err.Error())
	if strings.Contains(value, "overlap") || strings.Contains(value, "conflict") || strings.Contains(value, "unsafe") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func (s Server) recordBackupContractLifecycleEvent(ctx context.Context, req requestctx.Context, eventType, key string, lifecycle backupcontracts.LifecycleResult) error {
	_, err := events.NewService(s.services.DB).Append(ctx, events.AppendInput{EventType: eventType, EventLevel: "audit", Request: req, TargetKind: "backup_contract", TargetID: key, Status: lifecycle.Action, Payload: map[string]any{"node_apply_queued": lifecycle.NodeApplyQueued, "desired_revision": lifecycle.DesiredRevision}})
	return err
}

func (s Server) deleteBackupContract(w http.ResponseWriter, r *http.Request, resolved box.Resolved, directoryRelPath string, key string, input backupcontracts.DeleteRequest) (backupcontracts.LifecycleResult, error) {
	correlationID, ctx := requestMeta(r)
	mutation := backupcontracts.DeleteInput{BoxRoot: resolved.RootPath, DirectoryRelPath: directoryRelPath, Key: key, DryRun: input.DryRun}
	if input.DryRun {
		result, err := backupcontracts.Delete(mutation)
		return lifecycleResultFromDelete(result, key), err
	}
	req, idemRecord, proceed, err := s.beginBackupContractMutation(w, r, ctx, correlationID, "backup.contract.delete", map[string]any{"key": key})
	if err != nil || !proceed {
		return backupcontracts.LifecycleResult{}, errBackupContractResponseWritten
	}
	result, err := backupcontracts.Delete(mutation)
	lifecycle := lifecycleResultFromDelete(result, key)
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.delete_failed", "backup", key, "Could not delete backup contract.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	if err := s.services.Box.MarkBackupContractDeleted(ctx, resolved.RootPath, key); err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.delete_tracking_failed", "backup", key, "Backup contract was deleted but deletion intent could not be recorded.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	lifecycle, err = s.applyBackupContractWatchPlan(ctx, req, resolved, lifecycle)
	if err != nil {
		idemErr := loomerrors.Wrap("backup_contracts.apply_failed", "backup", key, "Backup contract was deleted but watch-plan apply failed.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		return backupcontracts.LifecycleResult{}, err
	}
	if err := s.recordBackupContractLifecycleEvent(ctx, req, events.TypeBackupContractDeleted, key, lifecycle); err != nil {
		return backupcontracts.LifecycleResult{}, err
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, lifecycle)
	s.completeIdempotency(ctx, idemRecord, "backup_contract", key, envelope)
	return lifecycle, nil
}

func (s Server) resolveBackupContractsBox() (box.Resolved, string, error) {
	if strings.TrimSpace(s.services.RuntimeConfig.BoxPath) == "" {
		return box.Resolved{}, "", fmt.Errorf("LOOM_BOX_PATH is not configured")
	}
	home, _ := os.UserHomeDir()
	resolved, err := box.Resolve(box.ResolveInput{
		ConfiguredPath:   s.services.RuntimeConfig.BoxPath,
		RuntimeStateRoot: s.services.RuntimeConfig.BoxStateRoot,
		ConfigProfile:    s.services.RuntimeConfig.BoxProfile,
		NodeID:           s.services.RuntimeConfig.NodeID,
		NodeRole:         s.services.RuntimeConfig.NodeRole,
		HomeDir:          home,
	})
	if err != nil {
		return box.Resolved{}, "", err
	}
	directoryRelPath := backupcontracts.DefaultDirectoryRelPath
	status := box.Inspect(resolved)
	if status.Contract != nil && strings.TrimSpace(status.Contract.Policies[box.PolicyBackupContracts]) != "" {
		directoryRelPath = status.Contract.Policies[box.PolicyBackupContracts]
	}
	return resolved, directoryRelPath, nil
}

func (s Server) beginBackupContractMutation(w http.ResponseWriter, r *http.Request, ctx context.Context, correlationID string, operation string, payload map[string]any) (requestctx.Context, idempotency.Record, bool, error) {
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return requestctx.Context{}, idempotency.Record{}, false, err
	}
	record, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, operation, payload)
	return req, record, proceed, nil
}

func (s Server) applyBackupContractWatchPlan(ctx context.Context, req requestctx.Context, resolved box.Resolved, lifecycle backupcontracts.LifecycleResult) (backupcontracts.LifecycleResult, error) {
	applyResult, err := s.services.Box.ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Resolved: resolved})
	if err != nil {
		return lifecycle, err
	}
	lifecycle.WatchedRoots = applyResult.Plan.WatchedRoots
	lifecycle.DesiredStateRegistered = len(applyResult.Registrations) > 0
	nodeRefs := make([]string, 0, len(applyResult.Nodes))
	for _, node := range applyResult.Nodes {
		nodeRefs = append(nodeRefs, node.NodeID)
	}
	queued, err := backupcontracts.NewReconcileService(s.services.DB).QueueNodes(ctx, req, nodeRefs)
	if err != nil {
		return lifecycle, err
	}
	lifecycle.NodeApplyQueued = len(queued) > 0
	if len(queued) == 1 {
		lifecycle.DesiredRevision = queued[0].DesiredRevision
	}
	lifecycle.NodeApplied = false
	lifecycle.Applied = false
	return lifecycle, nil
}

func lifecycleResultFromMutation(result backupcontracts.MutateResult, roots []projectcontracts.ProjectWatchedRootItem) backupcontracts.LifecycleResult {
	return backupcontracts.LifecycleResult{
		Action:        result.Action,
		DryRun:        result.DryRun,
		Path:          result.Path,
		DirectoryPath: result.DirectoryPath,
		Contract:      result.Contract,
		RenderedYAML:  result.RenderedYAML,
		WatchedRoots:  roots,
		Applied:       false,
	}
}

func lifecycleResultFromDelete(result backupcontracts.MutateResult, key string) backupcontracts.LifecycleResult {
	return backupcontracts.LifecycleResult{
		Action:        result.Action,
		DryRun:        result.DryRun,
		Path:          result.Path,
		DirectoryPath: result.DirectoryPath,
		Contract:      backupcontracts.Contract{Key: strings.ToLower(strings.TrimSpace(key))},
		Applied:       false,
	}
}

func plannedWatchedRootForCreate(contract backupcontracts.Contract, path string, resolved box.Resolved) []projectcontracts.ProjectWatchedRootItem {
	item, err := backupcontracts.WatchedRootItem(contract, path, backupcontracts.WatchPlanOptions{BoxRoot: resolved.RootPath, BoxID: "box_dry_run", OwnerNode: resolved.OwnerNode})
	if err != nil {
		return nil
	}
	return []projectcontracts.ProjectWatchedRootItem{item}
}
