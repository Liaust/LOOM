package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/config"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/restoreauthority"
	"loom.local/loom/internal/workers"
)

func (s Server) handleCloudStatusLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudStatusLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud status JSON.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	mode := cloudstorage.StatusModeLive
	if input.Cached {
		mode = cloudstorage.StatusModeCached
	}
	report, err := cloudstorage.Status(ctx, cloudstorage.StatusInput{
		ConfigPath: configPath,
		Mode:       mode,
		ForceLive:  input.ForceLive,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.status_failed", "cloud", "status", "Could not read live cloud status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, report))
}

func (s Server) handleCloudDoctorLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudDoctorLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud doctor JSON.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	report, err := cloudstorage.Doctor(ctx, cloudstorage.DoctorInput{
		ConfigPath:    configPath,
		RuntimeConfig: s.services.RuntimeConfig,
		Live:          true,
		ForceLive:     input.ForceLive,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.doctor_failed", "cloud", "doctor", "Could not run live cloud diagnostics.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, report))
}

func (s Server) handleCloudSnapshotStatusLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudSnapshotStatusLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot status JSON.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	status, err := cloudstorage.Status(ctx, cloudstorage.StatusInput{ConfigPath: configPath, Mode: cloudstorage.StatusModeLive, ForceLive: input.ForceLive})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.status_failed", "cloud", "status", "Could not read live cloud status.", err)
		return
	}
	producer, err := s.services.Maintenance.CloudProtectionStatus(ctx)
	if err != nil {
		if !errors.Is(err, maintenance.ErrInvalid) {
			s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.snapshot_producer_status_failed", "cloud", "producer", "Could not read cloud snapshot producer status.", err)
			return
		}
		producer = maintenance.CloudProtectionStatus{Available: false, WorkerState: "unavailable", Verification: "unknown", UpdatedAt: time.Now().UTC()}
	}
	report := struct {
		Cloud     cloudstorage.StatusReport         `json:"cloud"`
		Snapshots *cloudstorage.SnapshotListResult  `json:"snapshots,omitempty"`
		Producer  maintenance.CloudProtectionStatus `json:"producer"`
	}{Cloud: status, Producer: producer}
	if loaded.Config.Enabled && status.Status == "reachable" {
		list, err := cloudstorage.ListSnapshots(ctx, cloudstorage.SnapshotListInput{
			Config: loaded.Config,
			NodeID: firstNonEmptyHTTPCloud(input.NodeID, s.services.RuntimeConfig.NodeID),
		})
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.snapshot_list_failed", "cloud", "snapshots", "Could not list cloud snapshots.", err)
			return
		}
		report.Snapshots = &list
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, report))
}

func (s Server) handleCloudSnapshotRun(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input workers.RunOnceInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot run JSON.", err)
		return
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
	}
	if strings.TrimSpace(input.Reason) == "" {
		input.Reason = "manual direct Borg archive request"
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "cloud", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "cloud.snapshot.run", input)
	if !proceed {
		return
	}
	result, err := s.services.Workers.RunOnce(ctx, req, "main.cloud_snapshot_upload", input)
	if err != nil && result.Run.WorkerRunID == "" {
		status := http.StatusBadRequest
		if errors.Is(err, workers.ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, workers.ErrNotFound) {
			status = http.StatusNotFound
		}
		idemErr := loomerrors.Wrap("cloud.snapshot_run_failed", "cloud", "snapshot", "Could not run the cloud snapshot producer.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, status, idemErr.Code, "cloud", "snapshot", idemErr.Summary, err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleCloudSnapshotListLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudSnapshotListLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot list JSON.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	result, err := cloudstorage.ListSnapshots(ctx, cloudstorage.SnapshotListInput{
		Config: loaded.Config,
		NodeID: firstNonEmptyHTTPCloud(input.NodeID, s.services.RuntimeConfig.NodeID),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.snapshot_list_failed", "cloud", "snapshots", "Could not list cloud snapshots.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleCloudSnapshotVerifyLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.SnapshotVerifyLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot verify JSON.", err)
		return
	}
	ref := strings.TrimSpace(input.Ref)
	options, err := cloudstorage.NormalizeSnapshotVerifyOptions(ref, input.SnapshotVerifyOptions)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.snapshot_verify_profile_invalid", "cloud", "profile", "Cloud snapshot verify profile is invalid.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	if err := cloudstorage.ValidateSnapshotVerifyBackend(loaded.Config.Snapshots.Backend, options); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.snapshot_verify_profile_invalid", "cloud", "profile", "Cloud snapshot verify profile is invalid for the configured backend.", err)
		return
	}
	result, err := cloudstorage.VerifySnapshot(ctx, cloudstorage.SnapshotVerifyInput{
		Config:                loaded.Config,
		NodeID:                firstNonEmptyHTTPCloud(input.NodeID, s.services.RuntimeConfig.NodeID),
		Ref:                   ref,
		StateDir:              firstNonEmptyHTTPCloud(input.StateDir, loaded.Config.StateDir),
		SnapshotVerifyOptions: options,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.snapshot_verify_failed", "cloud", ref, "Could not verify cloud snapshot.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

// This wire shape intentionally excludes runtime paths, connection strings,
// owners, active databases, socket settings and keep-database authority.
type cloudSnapshotRestoreDrillRequest struct {
	ConfigPath               string `json:"config_path,omitempty"`
	NodeID                   string `json:"node_id,omitempty"`
	Ref                      string `json:"ref"`
	TargetDatabase           string `json:"target_database,omitempty"`
	ProvenanceTargetDatabase string `json:"provenance_target_database,omitempty"`
	DryRun                   bool   `json:"dry_run"`
}

func (s Server) handleCloudSnapshotRestoreDrillLive(w http.ResponseWriter, r *http.Request) {
	s.cloudSnapshotRestoreDrillLive(w, r, cloudstorage.RestoreDrill)
}

func (s Server) cloudSnapshotRestoreDrillLive(w http.ResponseWriter, r *http.Request, run func(context.Context, cloudstorage.CloudRestoreDrillInput) (cloudstorage.CloudRestoreDrillResult, error)) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", "restore-drill", "Method is not allowed.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	var input cloudSnapshotRestoreDrillRequest
	if r.Body == nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud restore drill JSON.", nil)
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud restore drill JSON.", nil)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body must contain exactly one cloud restore drill request.", nil)
		return
	}
	if strings.TrimSpace(input.Ref) == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.restore_ref_required", "cloud", "ref", "Select a cloud snapshot ref for the restore drill.", nil)
		return
	}
	if input.ConfigPath != "" && input.ConfigPath != cloudstorage.DefaultConfigPath && !s.services.AllowCloudConfigOverride {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", nil)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", nil)
		return
	}
	runtimeConfig := s.services.RuntimeConfig
	for _, target := range []struct {
		kind restoreauthority.Kind
		name string
	}{
		{restoreauthority.KindOperational, input.TargetDatabase},
		{restoreauthority.KindProvenance, input.ProvenanceTargetDatabase},
	} {
		if target.name != "" {
			if err := restoreauthority.ValidateDisposableDatabase(target.kind, target.name, runtimeConfig.RestoreOperationalDatabase, runtimeConfig.RestoreProvenanceDatabase); err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "cloud.restore_target_invalid", "cloud", "target_database", "Restore targets must be disposable databases outside both active databases.", nil)
				return
			}
		}
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", nil)
		return
	}
	restoreInput := cloudstorage.CloudRestoreDrillInput{
		Config: loaded.Config,
		NodeID: firstNonEmptyHTTPCloud(input.NodeID, runtimeConfig.NodeID),
		Ref:    input.Ref, StateDir: loaded.Config.StateDir,
		TargetDatabase: input.TargetDatabase, DryRun: input.DryRun,
	}
	if err := configureCloudRestoreDrillAuthority(&restoreInput, runtimeConfig, input.ProvenanceTargetDatabase); err != nil {
		s.writeCloudRestoreDrillError(w, correlationID, err)
		return
	}
	result, err := run(ctx, restoreInput)
	if err != nil {
		s.writeCloudRestoreDrillError(w, correlationID, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func configureCloudRestoreDrillAuthority(input *cloudstorage.CloudRestoreDrillInput, runtimeConfig config.Config, provenanceTarget string) error {
	input.ActiveDatabase = runtimeConfig.RestoreOperationalDatabase
	input.Owner = runtimeConfig.RestoreOperationalOwner
	input.ProvenanceDatabaseURL = runtimeConfig.ProvenanceDBURL
	input.ProvenanceTargetDatabase = strings.TrimSpace(provenanceTarget)
	input.ProvenanceActiveDatabase = runtimeConfig.RestoreProvenanceDatabase
	input.ProvenanceOwner = runtimeConfig.RestoreProvenanceOwner
	if input.DryRun {
		return nil
	}
	socketUID, err := restoreauthority.ResolveUserID(runtimeConfig.RestoreAuthoritySocketOwner)
	if err != nil {
		return err
	}
	socketGID, err := restoreauthority.ResolveGroupID(runtimeConfig.RestoreAuthoritySocketGroup)
	if err != nil {
		return err
	}
	activatorUID, err := restoreauthority.ResolveUserID(runtimeConfig.RestoreActivatorUser)
	if err != nil {
		return err
	}
	authority, err := restoreauthority.NewClient(restoreauthority.ClientConfig{
		SocketPath: runtimeConfig.RestoreAuthoritySocketPath,
		SocketUID:  socketUID, SocketGID: socketGID, SocketActivatorUID: &activatorUID,
	})
	if err != nil {
		return err
	}
	input.RestoreAuthority = authority
	return nil
}

func (s Server) writeCloudRestoreDrillError(w http.ResponseWriter, correlationID string, err error) {
	code, domain, target, summary := "cloud.snapshot_restore_drill_failed", "cloud", "restore-drill", "Could not complete the cloud restore drill."
	var typed *loomerrors.Error
	if errors.As(err, &typed) {
		code, domain, target, summary = typed.Code, typed.Domain, typed.Target, typed.Summary
	}
	// Preserve reviewed failure truth without logging/serializing raw causes,
	// which may contain service credential references or PostgreSQL details.
	s.writeError(w, correlationID, http.StatusInternalServerError, code, domain, target, summary, nil)
}

func (s Server) handleCloudSnapshotRetentionPlanLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudSnapshotRetentionPlanLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot retention plan JSON.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	plan, err := cloudstorage.PlanSnapshotRetention(ctx, cloudstorage.SnapshotRetentionInput{
		Config:     loaded.Config,
		NodeID:     firstNonEmptyHTTPCloud(input.NodeID, s.services.RuntimeConfig.NodeID),
		StateDir:   firstNonEmptyHTTPCloud(input.StateDir, loaded.Config.StateDir),
		KeepLatest: input.KeepLatest,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.retention_plan_failed", "cloud", "retention", "Could not plan cloud snapshot retention.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, plan))
}

func (s Server) handleCloudSnapshotRetentionApplyLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudSnapshotRetentionApplyLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot retention apply JSON.", err)
		return
	}
	if !input.Confirm {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.retention_confirm_required", "cloud", "confirm", "Pass --confirm to apply cloud snapshot retention.", nil)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	if loaded.Config.Snapshots.Backend == cloudstorage.SnapshotBackendBorg && (input.Plan.PlanDigest == "" || input.ConfirmDigest != input.Plan.PlanDigest) {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.retention_plan_confirmation_required", "cloud", "confirm_digest", "Borg retention requires the exact digest from a reviewed plan.", nil)
		return
	}
	result, err := cloudstorage.ApplySnapshotRetention(ctx, cloudstorage.SnapshotRetentionApplyInput{
		SnapshotRetentionInput: cloudstorage.SnapshotRetentionInput{
			Config:     loaded.Config,
			NodeID:     firstNonEmptyHTTPCloud(input.NodeID, s.services.RuntimeConfig.NodeID),
			StateDir:   firstNonEmptyHTTPCloud(input.StateDir, loaded.Config.StateDir),
			KeepLatest: input.KeepLatest,
		},
		Plan: input.Plan, Confirm: true, ConfirmDigest: input.ConfirmDigest, Compact: input.Compact,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.retention_apply_failed", "cloud", "retention", "Could not apply cloud snapshot retention.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleCloudSnapshotBackendStatusLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudSnapshotBackendStatusLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot backend status JSON.", err)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	result, err := cloudstorage.SnapshotBackendStatus(ctx, cloudstorage.SnapshotBackendStatusInput{
		Config:    loaded.Config,
		Live:      !input.Cached,
		ForceLive: input.ForceLive,
		UseCache:  input.Cached,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.snapshot_backend_status_failed", "cloud", "snapshot_backend", "Could not read live cloud snapshot backend status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleCloudSnapshotBackendInitLive(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input cloudstorage.CloudSnapshotBackendInitLiveInput
	if err := decodeStorageOperationInput(r, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request body is not valid cloud snapshot backend init JSON.", err)
		return
	}
	if !input.Confirm {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.snapshot_backend_init_confirm_required", "cloud", "confirm", "Pass --confirm to initialize the cloud snapshot backend.", nil)
		return
	}
	configPath, err := s.cloudConfigPath(input.ConfigPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode.", err)
		return
	}
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load cloud config.", err)
		return
	}
	result, err := cloudstorage.InitializeSnapshotBackend(ctx, cloudstorage.SnapshotBackendInitInput{
		Config:  loaded.Config,
		Confirm: true,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.snapshot_backend_init_failed", "cloud", "snapshot_backend", "Could not initialize cloud snapshot backend.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) cloudConfigPath(inputPath string) (string, error) {
	inputPath = strings.TrimSpace(inputPath)
	if inputPath == "" || inputPath == cloudstorage.DefaultConfigPath {
		return inputPath, nil
	}
	if s.services.AllowCloudConfigOverride {
		return inputPath, nil
	}
	return "", fmt.Errorf("cloud config override %q requires explicit local mode", inputPath)
}

func firstNonEmptyHTTPCloud(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s Server) handleCloudRestoreCleanupPlan(w http.ResponseWriter, r *http.Request) {
	s.cloudRestoreCleanup(w, r, false)
}
func (s Server) handleCloudRestoreCleanupApply(w http.ResponseWriter, r *http.Request) {
	s.cloudRestoreCleanup(w, r, true)
}
func (s Server) cloudRestoreCleanup(w http.ResponseWriter, r *http.Request, apply bool) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "cloud", "restore-cleanup", "Method is not allowed.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	var input cloudstorage.RestoreCleanupApplyInput
	valid := func() bool {
		if r.Body == nil {
			return false
		}
		defer r.Body.Close()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		token, err := d.Token()
		if err != nil || token != json.Delim('{') {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			name, ok := token.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			switch name {
			case "attempt":
				err = d.Decode(&input.Attempt)
			case "confirm_digest":
				if !apply {
					return false
				}
				err = d.Decode(&input.ConfirmDigest)
			case "yes":
				if !apply {
					return false
				}
				err = d.Decode(&input.Yes)
			case "dry_run":
				if !apply {
					return false
				}
				err = d.Decode(&input.DryRun)
			default:
				return false
			}
			if err != nil {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim('}') && d.Decode(new(any)) == io.EOF && input.Attempt != ""
	}()
	if !valid {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "cloud", "body", "Request must contain one bounded selector-only cleanup object.", nil)
		return
	}
	var cfg cloudstorage.Config
	var err error
	if s.cloudRestoreCleanupConfig != nil {
		cfg, err = s.cloudRestoreCleanupConfig()
	} else {
		var loaded cloudstorage.LoadResult
		loaded, err = cloudstorage.LoadConfig(cloudstorage.DefaultConfigPath)
		cfg = loaded.Config
	}
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "cloud.config_failed", "cloud", "config", "Could not load daemon cloud configuration.", nil)
		return
	}
	var result cloudstorage.RestoreCleanupResult
	if apply {
		result, err = cloudstorage.ApplyRestoreCleanup(ctx, cfg, input)
	} else {
		result, err = cloudstorage.PlanRestoreCleanup(ctx, cfg, input.Attempt)
	}
	if err != nil {
		s.writeError(w, correlationID, http.StatusConflict, "cloud.restore_cleanup_refused", "cloud", "restore-cleanup", "Cleanup refused. Preserve the attempt and review its confirmation, lifecycle, directory custody and private evidence.", nil)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}
