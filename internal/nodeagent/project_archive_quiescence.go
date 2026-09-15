package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

const (
	projectArchiveQuiescenceHandler = "system.project.archive.quiesce"

	ProjectArchiveQuiescenceBeforeFence       = "before_fence"
	ProjectArchiveQuiescenceAfterFence        = "after_fence"
	ProjectArchiveQuiescenceBeforeReceiptMove = "before_receipt_rename"
	ProjectArchiveQuiescenceAfterReceiptMove  = "after_receipt_rename"
)

type projectArchiveManager interface {
	Execute(context.Context, serviceregistry.ManagerRequest) (serviceregistry.ManagerResult, error)
}

type ProjectArchiveQuiescenceService struct {
	Config      Config
	Store       Store
	Clock       func() time.Time
	Manager     projectArchiveManager
	FailureHook func(stage string, target *projectquiescence.Target) error
}

type ProjectArchiveQuiescenceError struct {
	Code string
}

func isProjectArchiveQuiescenceDispatch(config Config, dispatch routing.RemoteDispatchPayload) bool {
	address := capabilities.NodeSystemProviderAddress(addressSegment(config.NodeKey)) + ".project.archive.quiesce"
	return dispatch.CapabilityAddress == address && (dispatch.Operation == address || dispatch.Operation == "capability:"+address)
}

func executeProjectArchiveQuiescenceDispatch(ctx context.Context, config Config, store Store, dispatch routing.RemoteDispatchPayload) (json.RawMessage, error) {
	if !isProjectArchiveQuiescenceDispatch(config, dispatch) {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.invalid_endpoint")
	}
	request, err := projectquiescence.DecodeRequest(dispatch.Input)
	if err != nil {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.invalid_request")
	}
	receipt, err := (ProjectArchiveQuiescenceService{Config: config, Store: store}).Quiesce(ctx, request)
	if err != nil {
		return nil, err
	}
	raw, err := projectquiescence.CanonicalReceiptBytes(receipt)
	if err != nil {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.receipt_invalid")
	}
	return raw, nil
}

func projectArchiveQuiescenceFailure(err error) (string, string) {
	var typed ProjectArchiveQuiescenceError
	if errors.As(err, &typed) {
		return typed.Code, "project archive quiescence refused"
	}
	return "node_agent.project_archive_quiescence.failed", "project archive quiescence refused"
}

func (err ProjectArchiveQuiescenceError) Error() string {
	return err.Code
}

func (service ProjectArchiveQuiescenceService) Quiesce(ctx context.Context, request projectquiescence.Request) (projectquiescence.Receipt, error) {
	if err := projectquiescence.ValidateRequest(request); err != nil {
		return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.invalid_request")
	}
	if request.NodeKey != service.Config.NodeKey {
		return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.wrong_node")
	}
	runtimeStore := noderuntime.NewStore(service.Store.DataDir)
	release, err := runtimeStore.AcquireProjectArchiveOperationLock(ctx, request.OperationID)
	if err != nil {
		return projectquiescence.Receipt{}, quiescenceContextOr("node_agent.project_archive_quiescence.operation_lock_failed", err)
	}
	defer func() { _ = release() }()

	now := service.now()
	raw, _, err := runtimeStore.ReadProjectArchiveReceipt(request.OperationID)
	if err == nil {
		receipt, decodeErr := projectquiescence.DecodeReceipt(raw, request, time.Time{}, now)
		if decodeErr != nil {
			return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.receipt_invalid")
		}
		if err := service.reobserveReceipt(ctx, runtimeStore, request, receipt); err != nil {
			return projectquiescence.Receipt{}, err
		}
		return receipt, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.receipt_unreadable")
	}

	evidence := make([]projectquiescence.Evidence, 0, len(request.Targets))
	for index := range request.Targets {
		target := request.Targets[index]
		observed, observeErr := service.stopFenceAndObserve(ctx, runtimeStore, request, target)
		if observeErr != nil {
			return projectquiescence.Receipt{}, observeErr
		}
		evidence = append(evidence, observed)
	}
	if err := ctx.Err(); err != nil {
		return projectquiescence.Receipt{}, quiescenceContextOr("node_agent.project_archive_quiescence.cancelled", err)
	}
	receipt := projectquiescence.Receipt{Evidence: evidence}
	if err := projectquiescence.SealReceipt(request, &receipt); err != nil {
		return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.receipt_invalid")
	}
	receiptRaw, err := projectquiescence.CanonicalReceiptBytes(receipt)
	if err != nil {
		return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.receipt_invalid")
	}
	if err := service.fail(ProjectArchiveQuiescenceBeforeReceiptMove, nil); err != nil {
		return projectquiescence.Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return projectquiescence.Receipt{}, quiescenceContextOr("node_agent.project_archive_quiescence.cancelled", err)
	}
	if err := runtimeStore.PublishProjectArchiveReceipt(request.OperationID, receiptRaw); err != nil {
		return projectquiescence.Receipt{}, quiescenceFailure("node_agent.project_archive_quiescence.receipt_publish_failed")
	}
	if err := service.fail(ProjectArchiveQuiescenceAfterReceiptMove, nil); err != nil {
		return projectquiescence.Receipt{}, err
	}
	return receipt, nil
}

func (service ProjectArchiveQuiescenceService) stopFenceAndObserve(ctx context.Context, runtimeStore noderuntime.Store, request projectquiescence.Request, target projectquiescence.Target) (projectquiescence.Evidence, error) {
	release, err := service.acquireTargetLock(ctx, runtimeStore, target)
	if err != nil {
		return projectquiescence.Evidence{}, err
	}
	defer func() { _ = release() }()

	existingFence, exists, err := loadExactProjectArchiveFence(runtimeStore, request, target, service.now())
	if err != nil {
		return projectquiescence.Evidence{}, err
	}
	if err := service.stopTarget(ctx, runtimeStore, target); err != nil {
		return projectquiescence.Evidence{}, err
	}
	if !exists {
		if err := service.fail(ProjectArchiveQuiescenceBeforeFence, &target); err != nil {
			return projectquiescence.Evidence{}, err
		}
		if err := ctx.Err(); err != nil {
			return projectquiescence.Evidence{}, quiescenceContextOr("node_agent.project_archive_quiescence.cancelled", err)
		}
		fence, fenceErr := projectquiescence.NewFence(request, target, service.now())
		if fenceErr != nil {
			return projectquiescence.Evidence{}, quiescenceFailure("node_agent.project_archive_quiescence.fence_invalid")
		}
		fenceRaw, _ := projectquiescence.CanonicalFenceBytes(fence)
		identity, _ := projectquiescence.TargetLockIdentity(target)
		identity = strings.TrimPrefix(identity, target.Kind+":")
		if err := runtimeStore.PublishProjectArchiveFence(target.Kind, identity, fenceRaw); err != nil {
			return projectquiescence.Evidence{}, quiescenceFailure("node_agent.project_archive_quiescence.fence_publish_failed")
		}
		existingFence = fence
		if err := service.fail(ProjectArchiveQuiescenceAfterFence, &target); err != nil {
			return projectquiescence.Evidence{}, err
		}
	}
	if err := projectquiescence.ValidateFence(request, target, existingFence, service.now()); err != nil {
		return projectquiescence.Evidence{}, quiescenceFailure("node_agent.project_archive_quiescence.fence_conflict")
	}
	identity, err := service.observeTarget(ctx, runtimeStore, target)
	if err != nil {
		return projectquiescence.Evidence{}, err
	}
	return projectquiescence.Evidence{
		ProjectRuntimeQuiescenceTarget: target,
		State:                          projectquiescence.TargetStateStopped, FenceState: projectquiescence.FenceStateActive,
		TargetReceiptID: identity, ObservedAt: service.now(),
	}, nil
}

func (service ProjectArchiveQuiescenceService) reobserveReceipt(ctx context.Context, runtimeStore noderuntime.Store, request projectquiescence.Request, receipt projectquiescence.Receipt) error {
	for index, target := range request.Targets {
		release, err := service.acquireTargetLock(ctx, runtimeStore, target)
		if err != nil {
			return err
		}
		_, exists, fenceErr := loadExactProjectArchiveFence(runtimeStore, request, target, service.now())
		if fenceErr != nil || !exists {
			_ = release()
			return quiescenceFailure("node_agent.project_archive_quiescence.fence_drift")
		}
		identity, observeErr := service.observeTarget(ctx, runtimeStore, target)
		releaseErr := release()
		if observeErr != nil {
			return observeErr
		}
		if releaseErr != nil {
			return quiescenceFailure("node_agent.project_archive_quiescence.target_unlock_failed")
		}
		if identity != receipt.Evidence[index].TargetReceiptID {
			return quiescenceFailure("node_agent.project_archive_quiescence.target_drift")
		}
	}
	return nil
}

func (service ProjectArchiveQuiescenceService) acquireTargetLock(ctx context.Context, store noderuntime.Store, target projectquiescence.Target) (func() error, error) {
	switch target.Kind {
	case projectquiescence.TargetKindWatchedRoot:
		release, err := store.AcquireWorkerExecutionLock(ctx, target.WorkerKey)
		if err != nil {
			return nil, quiescenceContextOr("node_agent.project_archive_quiescence.worker_lock_failed", err)
		}
		return release, nil
	case projectquiescence.TargetKindService:
		release, err := store.AcquireServiceExecutionLock(ctx, target.AllowlistKey)
		if err != nil {
			return nil, quiescenceContextOr("node_agent.project_archive_quiescence.service_lock_failed", err)
		}
		return release, nil
	default:
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.invalid_target")
	}
}

func (service ProjectArchiveQuiescenceService) stopTarget(ctx context.Context, runtimeStore noderuntime.Store, target projectquiescence.Target) error {
	switch target.Kind {
	case projectquiescence.TargetKindWatchedRoot:
		instance, root, err := service.loadExactWatchedRoot(runtimeStore, target)
		if err != nil {
			return err
		}
		if instance.Enabled {
			instance.Enabled = false
			if err := runtimeStore.SaveInstanceWhileLocked(instance); err != nil {
				return quiescenceFailure("node_agent.project_archive_quiescence.worker_disable_failed")
			}
		}
		if root.RootKey != target.BackendRootRef {
			return quiescenceFailure("node_agent.project_archive_quiescence.worker_identity_mismatch")
		}
		return nil
	case projectquiescence.TargetKindService:
		manager, err := service.exactServiceManager(target)
		if err != nil {
			return err
		}
		if _, err := manager.Execute(ctx, serviceregistry.ManagerRequest{AllowlistKey: target.AllowlistKey, Operation: serviceregistry.OperationStop}); err != nil {
			return quiescenceContextOr("node_agent.project_archive_quiescence.service_stop_failed", err)
		}
		return nil
	default:
		return quiescenceFailure("node_agent.project_archive_quiescence.invalid_target")
	}
}

func (service ProjectArchiveQuiescenceService) observeTarget(ctx context.Context, runtimeStore noderuntime.Store, target projectquiescence.Target) (string, error) {
	switch target.Kind {
	case projectquiescence.TargetKindWatchedRoot:
		instance, _, err := service.loadExactWatchedRoot(runtimeStore, target)
		if err != nil {
			return "", err
		}
		if instance.Enabled {
			return "", quiescenceFailure("node_agent.project_archive_quiescence.worker_not_stopped")
		}
		return "worker:" + instance.WorkerKey + ":" + instance.ConfigHash, nil
	case projectquiescence.TargetKindService:
		manager, err := service.exactServiceManager(target)
		if err != nil {
			return "", err
		}
		result, err := manager.Execute(ctx, serviceregistry.ManagerRequest{AllowlistKey: target.AllowlistKey, Operation: serviceregistry.OperationStatus})
		if err != nil {
			return "", quiescenceContextOr("node_agent.project_archive_quiescence.service_status_failed", err)
		}
		if !result.Success || result.ProcessState != serviceregistry.ProcessStateStopped {
			return "", quiescenceFailure("node_agent.project_archive_quiescence.service_not_stopped")
		}
		return "service:" + target.AllowlistKey + ":" + target.Manager + ":" + target.Unit, nil
	default:
		return "", quiescenceFailure("node_agent.project_archive_quiescence.invalid_target")
	}
}

func (service ProjectArchiveQuiescenceService) loadExactWatchedRoot(runtimeStore noderuntime.Store, target projectquiescence.Target) (noderuntime.WorkerInstance, watchedroots.RootConfig, error) {
	instance, err := runtimeStore.LoadInstance(target.WorkerKey)
	if err != nil {
		return noderuntime.WorkerInstance{}, watchedroots.RootConfig{}, quiescenceFailure("node_agent.project_archive_quiescence.worker_unavailable")
	}
	if instance.WorkerKey != target.WorkerKey || instance.Kind != noderuntime.KindWatchedRoot || instance.LocalRootKey != target.LocalRootKey || instance.ConfigHash != target.ConfigHash || noderuntime.WatchedRootWorkerKey(target.BackendRootRef) != target.WorkerKey {
		return noderuntime.WorkerInstance{}, watchedroots.RootConfig{}, quiescenceFailure("node_agent.project_archive_quiescence.worker_identity_mismatch")
	}
	root, err := watchedRootConfigFromInstance(instance)
	if err != nil || root.RootKey != target.BackendRootRef || watchedroots.ConfigHash(root) != target.ConfigHash {
		return noderuntime.WorkerInstance{}, watchedroots.RootConfig{}, quiescenceFailure("node_agent.project_archive_quiescence.worker_identity_mismatch")
	}
	return instance, root, nil
}

func (service ProjectArchiveQuiescenceService) exactServiceManager(target projectquiescence.Target) (projectArchiveManager, error) {
	if serviceregistry.IsApplicationUnitBinding(target.AllowlistKey, target.Unit) {
		if target.Manager != "systemd" {
			return nil, quiescenceFailure("node_agent.project_archive_quiescence.service_identity_mismatch")
		}
		return applicationArchiveManager{client: serviceregistry.ApplicationHelperClient{SocketPath: service.Config.ServiceManager.ApplicationSocketPath}, target: target}, nil
	}
	allowlist, err := serviceregistry.LoadAllowlist(service.Config.ServiceManager.AllowlistPath)
	if err != nil {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.service_allowlist_unavailable")
	}
	record, ok := allowlist.Lookup(target.AllowlistKey)
	if !ok || record.NodeKey != service.Config.NodeKey || string(record.Manager) != target.Manager || record.Unit != target.Unit || !allowsServiceOperation(record.Operations, serviceregistry.OperationStop) || !allowsServiceOperation(record.Operations, serviceregistry.OperationStatus) {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.service_identity_mismatch")
	}
	identity := record.ProjectArchiveIdentity
	if identity == nil || identity.ProviderKey != target.ProviderKey || identity.ProviderAddress != target.ProviderAddress || identity.ProviderID != target.ProviderID || identity.RuntimeProfileDigest != target.RuntimeProfileDigest {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.service_identity_mismatch")
	}
	providerAddress, err := capabilities.ParseProviderAddress(target.ProviderAddress)
	if err != nil || providerAddress.CompactAddress != target.ProviderAddress || providerAddress.ScopePath != target.OwnerNode || providerAddress.ProviderKey != target.ProviderKey {
		return nil, quiescenceFailure("node_agent.project_archive_quiescence.service_identity_mismatch")
	}
	if service.Manager != nil {
		return service.Manager, nil
	}
	return serviceregistry.ManagerService{
		Allowlist: allowlist,
		Launchd:   serviceregistry.LaunchdUserRunner{},
		Systemd:   serviceregistry.HelperClientRunner{Path: service.Config.ServiceManager.HelperPath, AllowlistPath: service.Config.ServiceManager.AllowlistPath},
	}, nil
}

func allowsServiceOperation(values []serviceregistry.Operation, want serviceregistry.Operation) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func loadExactProjectArchiveFence(store noderuntime.Store, request projectquiescence.Request, target projectquiescence.Target, now time.Time) (projectquiescence.Fence, bool, error) {
	identity, _ := projectquiescence.TargetLockIdentity(target)
	identity = strings.TrimPrefix(identity, target.Kind+":")
	raw, _, err := store.ReadProjectArchiveFence(target.Kind, identity)
	if errors.Is(err, fs.ErrNotExist) {
		return projectquiescence.Fence{}, false, nil
	}
	if err != nil {
		return projectquiescence.Fence{}, false, quiescenceFailure("node_agent.project_archive_quiescence.fence_unreadable")
	}
	fence, err := projectquiescence.DecodeFence(raw, request, target, now)
	if err != nil {
		return projectquiescence.Fence{}, false, quiescenceFailure("node_agent.project_archive_quiescence.fence_conflict")
	}
	return fence, true, nil
}

func (service ProjectArchiveQuiescenceService) now() time.Time {
	if service.Clock != nil {
		return service.Clock().UTC()
	}
	return time.Now().UTC()
}

func (service ProjectArchiveQuiescenceService) fail(stage string, target *projectquiescence.Target) error {
	if service.FailureHook == nil {
		return nil
	}
	if err := service.FailureHook(stage, target); err != nil {
		return quiescenceFailure("node_agent.project_archive_quiescence.injected_failure")
	}
	return nil
}

func quiescenceContextOr(code string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return quiescenceFailure("node_agent.project_archive_quiescence.cancelled")
	}
	return quiescenceFailure(code)
}

func quiescenceFailure(code string) error {
	return ProjectArchiveQuiescenceError{Code: code}
}

func projectArchiveQuiescenceInputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"schema_version":{"const":"project.quiescence_request.v1"},"project_id":{"type":"string","maxLength":256},"project_slug":{"type":"string","maxLength":128},"operation_id":{"type":"string","maxLength":256},"plan_digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},"node_key":{"type":"string","maxLength":256},"targets":{"type":"array","minItems":1,"maxItems":256,"items":{"oneOf":[{"type":"object","additionalProperties":false,"properties":{"facet":{"const":"watched_roots"},"kind":{"const":"watched_root_supervisor"},"owner_node":{"type":"string","maxLength":256},"local_root_key":{"type":"string","maxLength":256},"backend_root_ref":{"type":"string","maxLength":256},"worker_key":{"type":"string","maxLength":256},"config_hash":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"}},"required":["facet","kind","owner_node","local_root_key","backend_root_ref","worker_key","config_hash"]},{"type":"object","additionalProperties":false,"properties":{"facet":{"const":"services"},"kind":{"const":"service_process"},"owner_node":{"type":"string","maxLength":256},"provider_key":{"type":"string","maxLength":256},"provider_address":{"type":"string","maxLength":256},"provider_id":{"type":"string","maxLength":256},"runtime_profile_digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},"allowlist_key":{"type":"string","maxLength":256},"manager":{"enum":["systemd","launchd"]},"unit":{"type":"string","maxLength":256}},"required":["facet","kind","owner_node","provider_key","provider_address","provider_id","runtime_profile_digest","allowlist_key","manager","unit"]}]}},"request_digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"}},"required":["schema_version","project_id","project_slug","operation_id","plan_digest","node_key","targets","request_digest"]}`)
}

func projectArchiveQuiescenceOutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"schema_version":{"const":"project.quiescence_receipt.v1"},"request_digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},"node_key":{"type":"string","maxLength":256},"project_id":{"type":"string","maxLength":256},"project_slug":{"type":"string","maxLength":128},"operation_id":{"type":"string","maxLength":256},"plan_digest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},"evidence":{"type":"array","minItems":1,"maxItems":256,"items":{"oneOf":[{"type":"object","additionalProperties":false,"properties":{"facet":{"const":"watched_roots"},"kind":{"const":"watched_root_supervisor"},"owner_node":{"type":"string"},"local_root_key":{"type":"string"},"backend_root_ref":{"type":"string"},"worker_key":{"type":"string"},"config_hash":{"type":"string"},"state":{"const":"stopped"},"fence_state":{"const":"active"},"target_receipt_id":{"type":"string"},"observed_at":{"type":"string","format":"date-time"}},"required":["facet","kind","owner_node","local_root_key","backend_root_ref","worker_key","config_hash","state","fence_state","target_receipt_id","observed_at"]},{"type":"object","additionalProperties":false,"properties":{"facet":{"const":"services"},"kind":{"const":"service_process"},"owner_node":{"type":"string"},"provider_key":{"type":"string"},"provider_address":{"type":"string"},"provider_id":{"type":"string"},"runtime_profile_digest":{"type":"string"},"allowlist_key":{"type":"string"},"manager":{"enum":["systemd","launchd"]},"unit":{"type":"string"},"state":{"const":"stopped"},"fence_state":{"const":"active"},"target_receipt_id":{"type":"string"},"observed_at":{"type":"string","format":"date-time"}},"required":["facet","kind","owner_node","provider_key","provider_address","provider_id","runtime_profile_digest","allowlist_key","manager","unit","state","fence_state","target_receipt_id","observed_at"]}]}},"receipt_id":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"}},"required":["schema_version","request_digest","node_key","project_id","project_slug","operation_id","plan_digest","evidence","receipt_id"]}`)
}
