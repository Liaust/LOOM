package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

type projectArchiveTestManager struct {
	mu    sync.Mutex
	ops   []serviceregistry.Operation
	state serviceregistry.ObservedProcessState
}

type projectArchiveBlockingWatchedRootRunner struct {
	started chan struct{}
	release chan struct{}
}

func (*projectArchiveBlockingWatchedRootRunner) Kind() string { return noderuntime.KindWatchedRoot }
func (runner *projectArchiveBlockingWatchedRootRunner) RunOnce(ctx context.Context, _ noderuntime.Store, _ noderuntime.Env, _ noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	runner.started <- struct{}{}
	select {
	case <-runner.release:
		return noderuntime.RunResult{Status: noderuntime.RunStatusSucceeded, HealthStatus: noderuntime.WorkerStatusHealthy}, nil
	case <-ctx.Done():
		return noderuntime.RunResult{}, ctx.Err()
	}
}

func (manager *projectArchiveTestManager) Execute(_ context.Context, request serviceregistry.ManagerRequest) (serviceregistry.ManagerResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.ops = append(manager.ops, request.Operation)
	if request.Operation == serviceregistry.OperationStop {
		manager.state = serviceregistry.ProcessStateStopped
	}
	return serviceregistry.ManagerResult{Operation: request.Operation, Success: true, ProcessState: manager.state}, nil
}

func TestProjectArchiveQuiescenceStopsFencesAndFreshlyReplays(t *testing.T) {
	environment := newProjectArchiveQuiescenceEnvironment(t)
	receipt, err := environment.service.Quiesce(context.Background(), environment.request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ReceiptID == "" || len(receipt.Evidence) != 2 {
		t.Fatalf("receipt = %#v", receipt)
	}
	instance, err := environment.runtime.LoadInstance(environment.watched.WorkerKey)
	if err != nil || instance.Enabled {
		t.Fatalf("watched-root worker was not disabled: %#v %v", instance, err)
	}
	for _, target := range environment.request.Targets {
		identity, _ := projectquiescence.TargetLockIdentity(target)
		identity = strings.TrimPrefix(identity, target.Kind+":")
		fenced, err := environment.runtime.ProjectArchiveFenceActive(target.Kind, identity)
		if err != nil || !fenced {
			t.Fatalf("target fence kind=%s identity=%s active=%t err=%v", target.Kind, identity, fenced, err)
		}
	}
	reopen := instance
	reopen.Enabled = true
	if err := environment.runtime.SaveInstance(reopen); !errors.Is(err, noderuntime.ErrProjectArchiveFenced) {
		t.Fatalf("watched-root add/apply reopened fenced worker: %v", err)
	}
	nonTarget := noderuntime.WorkerInstance{WorkerKey: noderuntime.WatchedRootWorkerKey("other"), Kind: noderuntime.KindWatchedRoot, Enabled: true}
	if err := environment.runtime.SaveInstance(nonTarget); err != nil {
		t.Fatalf("non-target worker was blocked: %v", err)
	}

	environment.now = environment.now.Add(time.Minute)
	replayed, err := environment.service.Quiesce(context.Background(), environment.request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ReceiptID != receipt.ReceiptID || !replayed.Evidence[0].ObservedAt.Equal(receipt.Evidence[0].ObservedAt) {
		t.Fatalf("replay changed receipt identity or observation: first=%#v replay=%#v", receipt, replayed)
	}
	environment.manager.mu.Lock()
	ops := append([]serviceregistry.Operation(nil), environment.manager.ops...)
	environment.manager.mu.Unlock()
	if got := strings.Join(serviceOperationsStrings(ops), ","); got != "stop,status,status" {
		t.Fatalf("service operations = %s, want stop,status,status", got)
	}
}

func TestProjectArchiveQuiescenceWaitsForActiveWatchedRootRunBeforeReceipt(t *testing.T) {
	environment := newProjectArchiveQuiescenceEnvironment(t)
	environment.request.Targets = []projectquiescence.Target{environment.watched}
	if err := projectquiescence.SealRequest(&environment.request); err != nil {
		t.Fatal(err)
	}
	runner := &projectArchiveBlockingWatchedRootRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	runDone := make(chan error, 1)
	go func() {
		_, err := noderuntime.NewRegistry(runner).RunOnce(context.Background(), environment.runtime, noderuntime.Env{}, environment.watched.WorkerKey, "corr-active")
		runDone <- err
	}()
	<-runner.started
	quiesceDone := make(chan error, 1)
	go func() {
		_, err := environment.service.Quiesce(context.Background(), environment.request)
		quiesceDone <- err
	}()
	select {
	case err := <-quiesceDone:
		t.Fatalf("quiescence did not wait for active scan: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, _, err := environment.runtime.ReadProjectArchiveReceipt(environment.request.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt appeared while active scan was running: %v", err)
	}
	close(runner.release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if err := <-quiesceDone; err != nil {
		t.Fatal(err)
	}
	instance, err := environment.runtime.LoadInstance(environment.watched.WorkerKey)
	if err != nil || instance.Enabled {
		t.Fatalf("worker remained enabled after drained quiescence: %#v %v", instance, err)
	}
}

func TestProjectArchiveQuiescenceFenceWinsConcurrentWatchedRootEnable(t *testing.T) {
	environment := newProjectArchiveQuiescenceEnvironment(t)
	environment.request.Targets = []projectquiescence.Target{environment.watched}
	if err := projectquiescence.SealRequest(&environment.request); err != nil {
		t.Fatal(err)
	}
	beforeFence := make(chan struct{})
	continueFence := make(chan struct{})
	environment.service.FailureHook = func(stage string, _ *projectquiescence.Target) error {
		if stage == ProjectArchiveQuiescenceBeforeFence {
			close(beforeFence)
			<-continueFence
		}
		return nil
	}
	quiesceDone := make(chan error, 1)
	go func() {
		_, err := environment.service.Quiesce(context.Background(), environment.request)
		quiesceDone <- err
	}()
	<-beforeFence
	stale, err := environment.runtime.LoadInstance(environment.watched.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	stale.Enabled = true
	enableDone := make(chan error, 1)
	go func() { enableDone <- environment.runtime.SaveInstance(stale) }()
	select {
	case err := <-enableDone:
		t.Fatalf("concurrent enable bypassed quiescence lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(continueFence)
	if err := <-quiesceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-enableDone; !errors.Is(err, noderuntime.ErrProjectArchiveFenced) {
		t.Fatalf("concurrent enable was not refused by durable fence: %v", err)
	}
}

func TestProjectArchiveQuiescenceServiceFenceRejectsStartAndRestart(t *testing.T) {
	for _, operation := range []serviceregistry.Operation{serviceregistry.OperationStart, serviceregistry.OperationRestart} {
		t.Run(string(operation), func(t *testing.T) {
			environment := newProjectArchiveQuiescenceEnvironment(t)
			if _, err := environment.service.Quiesce(context.Background(), environment.request); err != nil {
				t.Fatal(err)
			}
			address := "workspace/node-a@service-one.service." + string(operation)
			dispatch := routing.RemoteDispatchPayload{
				CapabilityAddress: address, Operation: "capability:" + address, Input: json.RawMessage(`{}`),
				RuntimeBinding: &routing.RuntimeBindingSnapshot{RuntimeKind: capabilities.RuntimeKindServiceManager, RuntimeConfigJSON: json.RawMessage(`{"allowlist_key":"service-one","operation":"` + string(operation) + `"}`), Status: capabilities.RuntimeBindingStatusActive},
			}
			_, err := executeRuntimeBindingDispatch(context.Background(), environment.service.Config, State{}, dispatch, environment.service.Store)
			if err == nil {
				t.Fatalf("service %s succeeded while fenced", operation)
			}
			code, _, ok := routing.RuntimeFailureCode(err)
			if !ok || code != "node_agent.service_project_archive_fenced" {
				t.Fatalf("service %s refusal = %v code=%s", operation, err, code)
			}
		})
	}
}

func TestProjectArchiveQuiescenceDedicatedDispatchIsExactAndSecretFree(t *testing.T) {
	environment := newProjectArchiveQuiescenceEnvironment(t)
	environment.request.Targets = []projectquiescence.Target{environment.watched}
	if err := projectquiescence.SealRequest(&environment.request); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(environment.request)
	address := "workspace/node-a@system.project.archive.quiesce"
	dispatch := routing.RemoteDispatchPayload{CapabilityAddress: address, Operation: "capability:" + address, Input: raw}
	result, err := executeProjectArchiveQuiescenceDispatch(context.Background(), environment.service.Config, environment.service.Store, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectquiescence.DecodeReceipt(result, environment.request, time.Time{}, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("dedicated dispatch receipt = %s, %v", result, err)
	}
	dispatch.CapabilityAddress = "workspace/other@system.project.archive.quiesce"
	if _, err := executeProjectArchiveQuiescenceDispatch(context.Background(), environment.service.Config, environment.service.Store, dispatch); err == nil || strings.Contains(err.Error(), environment.request.ProjectID) {
		t.Fatalf("inexact endpoint refusal exposed input: %v", err)
	}
}

func TestProjectArchiveQuiescenceRejectsNodeWorkerAndServiceSubstitution(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*projectArchiveQuiescenceEnvironment)
		reseal bool
	}{
		{name: "node", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.NodeKey = "other"
			for index := range environment.request.Targets {
				environment.request.Targets[index].OwnerNode = "other"
			}
		}},
		{name: "allowlist", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].AllowlistKey = "other"
		}},
		{name: "manager", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].Manager = "launchd"
		}},
		{name: "unit", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].Unit = "loom-other.service"
		}},
		{name: "provider address", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].ProviderAddress = "workspace/node-a@other"
		}},
		{name: "provider key and address", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].ProviderKey = "other"
			environment.request.Targets[0].ProviderAddress = "node-a@other"
		}},
		{name: "provider ID binding", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].ProviderID = ids.NewProviderID()
		}},
		{name: "runtime profile digest binding", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets[0].RuntimeProfileDigest = nodeAgentProjectArchiveDigest("e")
		}},
		{name: "worker", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets = []projectquiescence.Target{environment.watched}
			environment.request.Targets[0].WorkerKey = noderuntime.WatchedRootWorkerKey("other")
		}},
		{name: "local root", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets = []projectquiescence.Target{environment.watched}
			environment.request.Targets[0].LocalRootKey = "other"
		}},
		{name: "worker config", reseal: true, mutate: func(environment *projectArchiveQuiescenceEnvironment) {
			environment.request.Targets = []projectquiescence.Target{environment.watched}
			environment.request.Targets[0].ConfigHash = nodeAgentProjectArchiveDigest("e")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newProjectArchiveQuiescenceEnvironment(t)
			test.mutate(environment)
			if test.reseal {
				if err := projectquiescence.SealRequest(&environment.request); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := environment.service.Quiesce(context.Background(), environment.request); err == nil {
				t.Fatal("substituted quiescence target was accepted")
			}
			environment.manager.mu.Lock()
			defer environment.manager.mu.Unlock()
			if len(environment.manager.ops) != 0 {
				t.Fatalf("substitution reached ManagerService: %v", environment.manager.ops)
			}
		})
	}
}

func TestProjectArchiveQuiescenceRequiresNodeAuthoritativeServiceIdentity(t *testing.T) {
	environment := newProjectArchiveQuiescenceEnvironment(t)
	var serviceTarget projectquiescence.Target
	for _, target := range environment.request.Targets {
		if target.Kind == projectquiescence.TargetKindService {
			serviceTarget = target
		}
	}
	environment.request.Targets = []projectquiescence.Target{serviceTarget}
	if err := projectquiescence.SealRequest(&environment.request); err != nil {
		t.Fatal(err)
	}
	allowlistWithoutIdentity := "records:\n  - schema_version: loom.service_allowlist.v1\n    key: service-one\n    node_key: node-a\n    manager: systemd\n    unit: loom-one.service\n    operations: [status, stop]\n    lifecycle_policy: service_operations\n    health: {kind: manager}\n    log_limits: {}\n"
	if err := os.WriteFile(environment.service.Config.ServiceManager.AllowlistPath, []byte(allowlistWithoutIdentity), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.service.Quiesce(context.Background(), environment.request); err == nil {
		t.Fatal("service quiescence accepted a caller-only identity")
	}
	environment.manager.mu.Lock()
	defer environment.manager.mu.Unlock()
	if len(environment.manager.ops) != 0 {
		t.Fatalf("missing node identity reached ManagerService: %v", environment.manager.ops)
	}
}

func TestProjectArchiveQuiescenceCrashRecoveryConverges(t *testing.T) {
	for _, stage := range []string{
		ProjectArchiveQuiescenceBeforeFence,
		ProjectArchiveQuiescenceAfterFence,
		ProjectArchiveQuiescenceBeforeReceiptMove,
		ProjectArchiveQuiescenceAfterReceiptMove,
	} {
		t.Run(stage, func(t *testing.T) {
			environment := newProjectArchiveQuiescenceEnvironment(t)
			failed := false
			environment.service.FailureHook = func(current string, _ *projectquiescence.Target) error {
				if current == stage && !failed {
					failed = true
					return errors.New("injected")
				}
				return nil
			}
			if _, err := environment.service.Quiesce(context.Background(), environment.request); err == nil {
				t.Fatal("injected crash did not interrupt quiescence")
			}
			environment.service.FailureHook = nil
			receipt, err := environment.service.Quiesce(context.Background(), environment.request)
			if err != nil || receipt.ReceiptID == "" {
				t.Fatalf("recovery = %#v, %v", receipt, err)
			}
			replay, err := environment.service.Quiesce(context.Background(), environment.request)
			if err != nil || replay.ReceiptID != receipt.ReceiptID {
				t.Fatalf("replay = %#v, %v", replay, err)
			}
		})
	}
}

func TestProjectArchiveQuiescenceReceiptTamperingRefusesReplay(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(t *testing.T, path string, receipt projectquiescence.Receipt, request projectquiescence.Request)
	}{
		{name: "symlink", tamper: func(t *testing.T, path string, _ projectquiescence.Receipt, _ projectquiescence.Request) {
			target := path + ".target"
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hard link", tamper: func(t *testing.T, path string, _ projectquiescence.Receipt, _ projectquiescence.Request) {
			if err := os.Link(path, path+".hard"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mode", tamper: func(t *testing.T, path string, _ projectquiescence.Receipt, _ projectquiescence.Request) {
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "bytes", tamper: func(t *testing.T, path string, _ projectquiescence.Receipt, _ projectquiescence.Request) {
			if err := os.WriteFile(path, []byte(`{"tampered":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "size", tamper: func(t *testing.T, path string, _ projectquiescence.Receipt, _ projectquiescence.Request) {
			if err := os.WriteFile(path, make([]byte, projectquiescence.MaxReceiptBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "time", tamper: func(t *testing.T, path string, receipt projectquiescence.Receipt, request projectquiescence.Request) {
			receipt.Evidence[0].ObservedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := projectquiescence.SealReceipt(request, &receipt); err != nil {
				t.Fatal(err)
			}
			raw, _ := projectquiescence.CanonicalReceiptBytes(receipt)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "target set", tamper: func(t *testing.T, path string, receipt projectquiescence.Receipt, request projectquiescence.Request) {
			receipt.Evidence[0].ProviderKey = "other-service"
			raw, _ := projectquiescence.CanonicalReceiptBytes(receipt)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newProjectArchiveQuiescenceEnvironment(t)
			receipt, err := environment.service.Quiesce(context.Background(), environment.request)
			if err != nil {
				t.Fatal(err)
			}
			_, identity, err := environment.runtime.ReadProjectArchiveReceipt(environment.request.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			test.tamper(t, identity.Path, receipt, environment.request)
			if _, err := environment.service.Quiesce(context.Background(), environment.request); err == nil {
				t.Fatal("tampered receipt replay succeeded")
			}
		})
	}
}

func TestProjectArchiveQuiescenceConflictAndCancellationPublishNoReceipt(t *testing.T) {
	environment := newProjectArchiveQuiescenceEnvironment(t)
	if _, err := environment.service.Quiesce(context.Background(), environment.request); err != nil {
		t.Fatal(err)
	}
	conflict := environment.request
	conflict.PlanDigest = nodeAgentProjectArchiveDigest("e")
	if err := projectquiescence.SealRequest(&conflict); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.service.Quiesce(context.Background(), conflict); err == nil {
		t.Fatal("conflicting request reused receipt location")
	}

	cancelled := newProjectArchiveQuiescenceEnvironment(t)
	release, err := cancelled.runtime.AcquireWorkerExecutionLock(context.Background(), cancelled.watched.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = cancelled.service.Quiesce(ctx, cancelled.request)
	_ = release()
	if err == nil {
		t.Fatal("cancelled quiescence wait succeeded")
	}
	if _, _, err := cancelled.runtime.ReadProjectArchiveReceipt(cancelled.request.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled quiescence published receipt: %v", err)
	}

	serviceCancelled := newProjectArchiveQuiescenceEnvironment(t)
	releaseService, err := serviceCancelled.runtime.AcquireServiceExecutionLock(context.Background(), "service-one")
	if err != nil {
		t.Fatal(err)
	}
	serviceCtx, serviceCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer serviceCancel()
	_, err = serviceCancelled.service.Quiesce(serviceCtx, serviceCancelled.request)
	_ = releaseService()
	if err == nil {
		t.Fatal("cancelled service-lock wait succeeded")
	}
	if _, _, err := serviceCancelled.runtime.ReadProjectArchiveReceipt(serviceCancelled.request.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled service-lock wait published receipt: %v", err)
	}

	for _, stage := range []string{ProjectArchiveQuiescenceBeforeFence, ProjectArchiveQuiescenceBeforeReceiptMove} {
		t.Run("cancel_"+stage, func(t *testing.T) {
			publicationCancelled := newProjectArchiveQuiescenceEnvironment(t)
			publicationCancelled.request.Targets = []projectquiescence.Target{publicationCancelled.watched}
			if err := projectquiescence.SealRequest(&publicationCancelled.request); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			publicationCancelled.service.FailureHook = func(current string, _ *projectquiescence.Target) error {
				if current == stage {
					cancel()
				}
				return nil
			}
			if _, err := publicationCancelled.service.Quiesce(ctx, publicationCancelled.request); err == nil {
				t.Fatalf("cancellation at %s published success", stage)
			}
			if _, _, err := publicationCancelled.runtime.ReadProjectArchiveReceipt(publicationCancelled.request.OperationID); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancellation at %s published receipt: %v", stage, err)
			}
		})
	}
}

type projectArchiveQuiescenceEnvironment struct {
	service ProjectArchiveQuiescenceService
	request projectquiescence.Request
	runtime noderuntime.Store
	manager *projectArchiveTestManager
	watched projectquiescence.Target
	now     time.Time
}

func newProjectArchiveQuiescenceEnvironment(t *testing.T) *projectArchiveQuiescenceEnvironment {
	t.Helper()
	dataDir := t.TempDir()
	allowlistPath := filepath.Join(dataDir, "allowlist.yaml")
	providerID := ids.NewProviderID()
	runtimeProfileDigest := nodeAgentProjectArchiveDigest("c")
	allowlist := fmt.Sprintf("records:\n  - schema_version: loom.service_allowlist.v1\n    key: service-one\n    node_key: node-a\n    manager: systemd\n    unit: loom-one.service\n    operations: [status, stop, start, restart]\n    lifecycle_policy: service_operations\n    health: {kind: manager}\n    log_limits: {}\n    project_archive_identity:\n      provider_key: service-one\n      provider_address: node-a@service-one\n      provider_id: %s\n      runtime_profile_digest: %s\n", providerID, runtimeProfileDigest)
	if err := os.WriteFile(allowlistPath, []byte(allowlist), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeStore := noderuntime.NewStore(dataDir)
	rootConfig := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "backend"})
	configRaw, _ := json.Marshal(rootConfig)
	configHash := watchedroots.ConfigHash(rootConfig)
	workerKey := noderuntime.WatchedRootWorkerKey(rootConfig.RootKey)
	if err := runtimeStore.SaveInstance(noderuntime.WorkerInstance{WorkerKey: workerKey, Kind: noderuntime.KindWatchedRoot, Enabled: true, LocalRootKey: "project", ConfigHash: configHash, ConfigJSON: configRaw}); err != nil {
		t.Fatal(err)
	}
	watched := projectquiescence.Target{Facet: projectquiescence.FacetWatchedRoots, Kind: projectquiescence.TargetKindWatchedRoot, LocalRootKey: "project", BackendRootRef: "backend", OwnerNode: "node-a", WorkerKey: workerKey, ConfigHash: configHash}
	serviceTarget := projectquiescence.Target{Facet: projectquiescence.FacetServices, Kind: projectquiescence.TargetKindService, ProviderKey: "service-one", ProviderAddress: "node-a@service-one", OwnerNode: "node-a", ProviderID: providerID, RuntimeProfileDigest: runtimeProfileDigest, AllowlistKey: "service-one", Manager: "systemd", Unit: "loom-one.service"}
	request := projectquiescence.Request{ProjectID: "project_test", ProjectSlug: "test", OperationID: "archive_test", PlanDigest: nodeAgentProjectArchiveDigest("a"), NodeKey: "node-a", Targets: []projectquiescence.Target{watched, serviceTarget}}
	if err := projectquiescence.SealRequest(&request); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	manager := &projectArchiveTestManager{state: serviceregistry.ProcessStateRunning}
	environment := &projectArchiveQuiescenceEnvironment{request: request, runtime: runtimeStore, manager: manager, watched: watched, now: now}
	environment.service = ProjectArchiveQuiescenceService{Config: Config{NodeKey: "node-a", ServiceManager: ServiceManagerConfig{AllowlistPath: allowlistPath}}, Store: Store{DataDir: dataDir}, Manager: manager, Clock: func() time.Time { return environment.now }}
	return environment
}

func nodeAgentProjectArchiveDigest(value string) string { return "sha256:" + strings.Repeat(value, 64) }

func serviceOperationsStrings(values []serviceregistry.Operation) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}
