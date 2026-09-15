//go:build darwin

package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

func TestDisposableMacServiceRegistryLifecycle(t *testing.T) {
	if os.Getenv("LOOM_RUN_SERVICE_MAC_ACCEPTANCE") != "1" {
		t.Skip("set LOOM_RUN_SERVICE_MAC_ACCEPTANCE=1 to run disposable launchd acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	acceptanceRoot := filepath.Join(root, ".loom-acceptance", "service-registry")
	if err := os.MkdirAll(acceptanceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("local.loom.service-registry-%d", os.Getpid())
	allowlistKey := fmt.Sprintf("service-registry-%d", os.Getpid())
	binaryPath := filepath.Join(acceptanceRoot, label)
	pidPath := filepath.Join(acceptanceRoot, "fixture.pid")
	logPath := filepath.Join(acceptanceRoot, "fixture.log")
	plistPath := filepath.Join(acceptanceRoot, label+".plist")
	allowlistPath := filepath.Join(acceptanceRoot, "allowlist.yaml")
	sentinelPath := filepath.Join(acceptanceRoot, "unrelated.keep")
	port := freeLoopbackPort(t)

	runAcceptanceCommand(t, ctx, filepath.Join("..", ".."), "go", "build", "-o", binaryPath, "./cmd/loom-service-fixture")
	writeAcceptanceFile(t, sentinelPath, "registration must not delete this\n")
	writeAcceptanceFile(t, plistPath, launchAgentPlist(label, binaryPath, pidPath, logPath, port))
	writeAcceptanceFile(t, allowlistPath, acceptanceAllowlist(allowlistKey, label, []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationStop, serviceregistry.OperationRestart, serviceregistry.OperationLogs}))
	allowlist, err := serviceregistry.LoadAllowlist(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	resolver := serviceregistry.StaticAllowlistResolver{Allowlist: allowlist}

	domain := fmt.Sprintf("gui/%d", os.Getuid())
	target := domain + "/" + label
	runAcceptanceCommand(t, ctx, "", "/bin/launchctl", "bootstrap", domain, plistPath)
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_ = exec.Command("/bin/launchctl", "bootout", target).Run()
		}
	})
	firstPID := waitFixtureHealthy(t, ctx, port, pidPath, 0)

	registry := &acceptanceProviderRegistry{}
	registration := acceptanceRegistration(label)
	planInput := serviceregistry.ProjectRegistrationPlanInput{Registration: registration, ProviderKey: allowlistKey, ProjectSlug: "service-registry-acceptance", ScopeRef: "scope_acceptance"}
	plan, err := serviceregistry.PlanProjectRegistration(planInput)
	if err != nil || plan.Provisioning != "external" {
		t.Fatalf("registration plan = %#v, err=%v", plan, err)
	}
	firstRegistration, err := serviceregistry.RegisterProject(ctx, requestctx.Context{}, registry, resolver, planInput)
	if err != nil {
		t.Fatal(err)
	}
	secondRegistration, err := serviceregistry.RegisterProject(ctx, requestctx.Context{}, registry, resolver, planInput)
	if err != nil {
		t.Fatal(err)
	}
	if !firstRegistration.Created || secondRegistration.Created || firstRegistration.Provider.ProviderID != secondRegistration.Provider.ProviderID {
		t.Fatalf("registration was not idempotent: %#v %#v", firstRegistration, secondRegistration)
	}
	if got := readPID(t, pidPath); got != firstPID {
		t.Fatalf("registration changed launchd process: pid=%d want=%d", got, firstPID)
	}

	config := Config{NodeKey: "macbook", ServiceManager: ServiceManagerConfig{AllowlistPath: allowlistPath}}
	status := dispatchAcceptanceOperation(t, ctx, config, registry, serviceregistry.OperationStatus, `{}`)
	if status.ProcessState != serviceregistry.ProcessStateRunning {
		t.Fatalf("status = %#v", status)
	}
	dispatchAcceptanceOperation(t, ctx, config, registry, serviceregistry.OperationStop, `{}`)
	waitProcessExit(t, firstPID)
	dispatchAcceptanceOperation(t, ctx, config, registry, serviceregistry.OperationStart, `{}`)
	secondPID := waitFixtureHealthy(t, ctx, port, pidPath, firstPID)
	dispatchAcceptanceOperation(t, ctx, config, registry, serviceregistry.OperationRestart, `{}`)
	thirdPID := waitFixtureHealthy(t, ctx, port, pidPath, secondPID)
	if thirdPID == secondPID {
		t.Fatalf("restart retained pid %d", thirdPID)
	}

	logs := dispatchAcceptanceOperation(t, ctx, config, registry, serviceregistry.OperationLogs, `{"lines":5,"max_bytes":4096,"max_age_seconds":120}`)
	if len(logs.LogLines) > 5 {
		t.Fatalf("log lines exceeded request: %d", len(logs.LogLines))
	}
	joined := strings.Join(logs.LogLines, "\n") + logs.Message
	if strings.Contains(joined, "synthetic-acceptance-token") || strings.Contains(joined, "/private/acceptance-only") {
		t.Fatalf("manager logs retained synthetic sensitive text: %q", joined)
	}
	redacted, _, err := serviceregistry.RedactManagerResult(serviceregistry.ManagerResult{Operation: serviceregistry.OperationLogs, Success: true, ProcessState: serviceregistry.ProcessStateRunning, LogLines: []string{"token=synthetic-acceptance-token path:/private/acceptance-only"}}, serviceregistry.LogLimits{MaxLines: 5, MaxBytes: 4096, MaxLineBytes: 1024, MaxAgeSeconds: 120})
	if err != nil || strings.Contains(strings.Join(redacted.LogLines, ""), "synthetic-acceptance-token") || strings.Contains(strings.Join(redacted.LogLines, ""), "/private/acceptance-only") {
		t.Fatalf("synthetic log redaction failed: %#v err=%v", redacted, err)
	}

	tampered := persistedAcceptanceDispatch(t, registry, serviceregistry.OperationStatus, `{}`)
	tampered.RuntimeBinding.RuntimeConfigJSON = json.RawMessage(`{"allowlist_key":"unallowlisted","operation":"status"}`)
	if _, err := executeRuntimeBindingDispatch(ctx, config, State{NodeID: "node_acceptance"}, tampered); err == nil {
		t.Fatal("unallowlisted key was accepted")
	}
	writeAcceptanceFile(t, allowlistPath, acceptanceAllowlist(allowlistKey, label, []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationStop, serviceregistry.OperationLogs}))
	if _, err := executeRuntimeBindingDispatch(ctx, config, State{NodeID: "node_acceptance"}, persistedAcceptanceDispatch(t, registry, serviceregistry.OperationRestart, `{}`)); err == nil {
		t.Fatal("disallowed operation was accepted")
	}

	reconcile, err := serviceregistry.ReconcileProject(ctx, requestctx.Context{}, registry, resolver, "service-registry-acceptance", nil)
	if err != nil || len(reconcile.Disabled) != 1 || registry.provider.Status != capabilities.ProviderStatusDisabled {
		t.Fatalf("registration removal did not disable provider: %#v provider=%#v err=%v", reconcile, registry.provider, err)
	}
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("registration removal changed unrelated file: %v", err)
	}
	if got := waitFixtureHealthy(t, ctx, port, pidPath, 0); got == 0 {
		t.Fatal("provider disable unexpectedly removed externally provisioned process")
	}

	runAcceptanceCommand(t, ctx, "", "/bin/launchctl", "bootout", target)
	cleaned = true
	waitProcessExit(t, thirdPID)
	if output, err := exec.Command("/bin/launchctl", "print", target).CombinedOutput(); err == nil {
		t.Fatalf("launch agent remained loaded: %s", output)
	}
	if err := os.RemoveAll(acceptanceRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(acceptanceRoot); !os.IsNotExist(err) {
		t.Fatalf("fixture state remained after cleanup: %v", err)
	}
}

func dispatchAcceptanceOperation(t *testing.T, ctx context.Context, config Config, registry *acceptanceProviderRegistry, operation serviceregistry.Operation, input string) serviceregistry.ManagerResult {
	t.Helper()
	result, err := executeRuntimeBindingDispatch(ctx, config, State{NodeID: "node_acceptance"}, persistedAcceptanceDispatch(t, registry, operation, input))
	if err != nil {
		t.Fatalf("dispatch %s: %v", operation, err)
	}
	var managerResult serviceregistry.ManagerResult
	if err := json.Unmarshal(result.Result, &managerResult); err != nil {
		t.Fatalf("decode %s result %s: %v", operation, result.Result, err)
	}
	if !managerResult.Success {
		t.Fatalf("%s result = %#v", operation, managerResult)
	}
	return managerResult
}

func persistedAcceptanceDispatch(t *testing.T, registry *acceptanceProviderRegistry, operation serviceregistry.Operation, input string) routing.RemoteDispatchPayload {
	t.Helper()
	address := registry.provider.CompactAddress + ".service." + string(operation)
	endpoint, ok := registry.endpoints[address]
	if !ok || endpoint.Status != capabilities.EndpointStatusActive || endpoint.ActiveEndpointVersionID == nil {
		t.Fatalf("persisted endpoint unavailable for %s: %#v", address, endpoint)
	}
	binding, ok := registry.bindings[*endpoint.ActiveEndpointVersionID]
	if !ok || binding.Status != capabilities.RuntimeBindingStatusActive {
		t.Fatalf("persisted runtime binding unavailable for %s", address)
	}
	return routing.RemoteDispatchPayload{
		CapabilityCallID: ids.NewCapabilityCallID(), CapabilityEndpointID: endpoint.CapabilityEndpointID,
		CapabilityAddress: endpoint.CompactAddress, ProviderID: registry.provider.ProviderID, ProviderAddress: registry.provider.CompactAddress,
		Operation: "capability:" + endpoint.CompactAddress, Input: json.RawMessage(input),
		RuntimeBinding: &routing.RuntimeBindingSnapshot{RuntimeBindingID: binding.RuntimeBindingID, CapabilityEndpointVersionID: binding.CapabilityEndpointVersionID, RuntimeKind: binding.RuntimeKind, RuntimeConfigJSON: binding.RuntimeConfigJSON, InputMappingJSON: binding.InputMappingJSON, OutputMappingJSON: binding.OutputMappingJSON, Status: binding.Status},
	}
}

func acceptanceRegistration(label string) serviceregistry.ProjectRegistrationInput {
	return serviceregistry.ProjectRegistrationInput{
		ProjectID: ids.NewProjectID(), ScopeKey: "projects/service-registry-acceptance", TargetNode: "macbook", SourcePath: ".loom/contracts/services/fixture.yaml",
		Service: serviceregistry.ServiceIdentity{Key: "fixture", DisplayName: "Disposable Service Fixture", Class: serviceregistry.ServiceClassProject},
		Runtime: serviceregistry.RuntimeProfileInput{Manager: serviceregistry.ManagerLaunchd, Unit: label, ServiceClass: serviceregistry.ServiceClassProject, Operations: serviceregistry.StandardOperations(), Health: serviceregistry.RuntimeHealth{Kind: serviceregistry.HealthKindManager}},
	}
}

type acceptanceProviderRegistry struct {
	provider  capabilities.Provider
	health    *capabilities.ProviderHealth
	classes   map[string]capabilities.CapabilityClass
	endpoints map[string]capabilities.CapabilityEndpoint
	versions  map[string]capabilities.EndpointVersion
	bindings  map[string]capabilities.EndpointRuntimeBinding
}

func (r *acceptanceProviderRegistry) EnsureProvider(_ context.Context, _ requestctx.Context, input capabilities.RegisterProviderInput) (capabilities.Provider, bool, error) {
	created := r.provider.ProviderID == ""
	if created {
		r.provider.ProviderID = ids.NewProviderID()
	}
	r.provider.ProviderKey, r.provider.CompactAddress, r.provider.DisplayName, r.provider.Description = input.ProviderKey, input.CompactAddress, input.DisplayName, input.Description
	r.provider.ProviderType, r.provider.NodeID, r.provider.ScopeID, r.provider.Version, r.provider.Status = input.ProviderType, input.NodeRef, input.ScopeRef, input.Version, input.Status
	r.provider.RuntimeProfileJSON, r.provider.DocumentationRefsJSON, r.provider.Metadata = input.RuntimeProfileJSON, input.DocumentationRefsJSON, input.Metadata
	return r.provider, created, nil
}

func (r *acceptanceProviderRegistry) UpsertProviderHealth(_ context.Context, _ requestctx.Context, providerID string, input capabilities.ProviderHealthInput) (capabilities.ProviderHealth, error) {
	health := capabilities.ProviderHealth{ProviderID: providerID, HealthStatus: input.HealthStatus, AvailabilityStatus: input.AvailabilityStatus, LastCheckedAt: input.LastCheckedAt, LastOKAt: input.LastOKAt, Message: input.Message, DetailsJSON: input.DetailsJSON}
	r.health = &health
	return health, nil
}

func (r *acceptanceProviderRegistry) ListProviders(context.Context, capabilities.ProviderFilter) ([]capabilities.ProviderListItem, error) {
	if r.provider.ProviderID == "" {
		return nil, nil
	}
	return []capabilities.ProviderListItem{{Provider: r.provider}}, nil
}

func (r *acceptanceProviderRegistry) EnsureCapabilityClass(_ context.Context, _ requestctx.Context, input capabilities.RegisterCapabilityClassInput) (capabilities.CapabilityClass, bool, error) {
	if r.classes == nil {
		r.classes = map[string]capabilities.CapabilityClass{}
	}
	key := input.Namespace + "." + input.Name
	class, found := r.classes[key]
	if !found {
		class.CapabilityClassID = ids.NewCapabilityClassID()
	}
	class.Namespace, class.Name, class.Status = input.Namespace, input.Name, input.Status
	r.classes[key] = class
	return class, !found, nil
}

func (r *acceptanceProviderRegistry) EnsureCapabilityEndpoint(_ context.Context, _ requestctx.Context, input capabilities.RegisterCapabilityEndpointInput) (capabilities.CapabilityEndpoint, bool, error) {
	if r.endpoints == nil {
		r.endpoints = map[string]capabilities.CapabilityEndpoint{}
	}
	endpoint, found := r.endpoints[input.CompactAddress]
	if !found {
		endpoint.CapabilityEndpointID = ids.NewCapabilityEndpointID()
	}
	endpoint.ProviderID, endpoint.CapabilityClassID, endpoint.EndpointName, endpoint.CompactAddress = input.ProviderRef, input.CapabilityClassRef, input.EndpointName, input.CompactAddress
	endpoint.Form, endpoint.InputSchemaJSON, endpoint.OutputSchemaJSON = input.Form, input.InputSchemaJSON, input.OutputSchemaJSON
	endpoint.RiskLevel, endpoint.ExecutionAuthorizationLevel, endpoint.Status, endpoint.Metadata = input.RiskLevel, input.ExecutionAuthorizationLevel, input.Status, input.Metadata
	r.endpoints[input.CompactAddress] = endpoint
	return endpoint, !found, nil
}

func (r *acceptanceProviderRegistry) EnsureEndpointVersion(_ context.Context, _ requestctx.Context, input capabilities.RegisterEndpointVersionInput) (capabilities.EndpointVersion, bool, error) {
	if r.versions == nil {
		r.versions = map[string]capabilities.EndpointVersion{}
	}
	version, found := r.versions[input.CapabilityEndpointRef]
	if !found {
		version.CapabilityEndpointVersionID = ids.NewCapabilityEndpointVersionID()
	}
	version.CapabilityEndpointID, version.VersionLabel, version.Status = input.CapabilityEndpointRef, input.VersionLabel, input.Status
	r.versions[input.CapabilityEndpointRef] = version
	for address, endpoint := range r.endpoints {
		if endpoint.CapabilityEndpointID == input.CapabilityEndpointRef {
			activeID := version.CapabilityEndpointVersionID
			endpoint.ActiveEndpointVersionID = &activeID
			r.endpoints[address] = endpoint
		}
	}
	return version, !found, nil
}

func (r *acceptanceProviderRegistry) RegisterRuntimeBinding(_ context.Context, _ requestctx.Context, input capabilities.RegisterRuntimeBindingInput) (capabilities.RuntimeBindingInspection, error) {
	if r.bindings == nil {
		r.bindings = map[string]capabilities.EndpointRuntimeBinding{}
	}
	binding, found := r.bindings[input.EndpointVersionRef]
	if !found {
		binding.RuntimeBindingID = ids.NewCapabilityRuntimeBindingID()
	}
	binding.CapabilityEndpointVersionID, binding.RuntimeKind, binding.RuntimeConfigJSON = input.EndpointVersionRef, input.RuntimeKind, input.RuntimeConfigJSON
	binding.InputMappingJSON, binding.OutputMappingJSON, binding.Status = input.InputMappingJSON, input.OutputMappingJSON, input.Status
	r.bindings[input.EndpointVersionRef] = binding
	return capabilities.RuntimeBindingInspection{Binding: binding}, nil
}

func (*acceptanceProviderRegistry) EnsureUsageDocument(context.Context, requestctx.Context, capabilities.RegisterUsageDocumentInput) (capabilities.UsageDocument, bool, error) {
	return capabilities.UsageDocument{}, true, nil
}

func (r *acceptanceProviderRegistry) InspectProvider(context.Context, string) (capabilities.ProviderInspection, error) {
	inspection := capabilities.ProviderInspection{Provider: r.provider, Health: r.health, Endpoints: []capabilities.CapabilityEndpoint{}}
	for _, endpoint := range r.endpoints {
		inspection.Endpoints = append(inspection.Endpoints, endpoint)
	}
	return inspection, nil
}

func (r *acceptanceProviderRegistry) InspectCapability(_ context.Context, ref string) (capabilities.CapabilityInspection, error) {
	for _, endpoint := range r.endpoints {
		if endpoint.CapabilityEndpointID != ref {
			continue
		}
		inspection := capabilities.CapabilityInspection{Provider: r.provider, Endpoint: endpoint}
		if endpoint.ActiveEndpointVersionID != nil {
			binding := r.bindings[*endpoint.ActiveEndpointVersionID]
			inspection.RuntimeBinding = &binding
		}
		return inspection, nil
	}
	return capabilities.CapabilityInspection{}, fmt.Errorf("endpoint not found: %s", ref)
}

func (r *acceptanceProviderRegistry) DisableCapabilityEndpoint(_ context.Context, _ requestctx.Context, ref string, _ json.RawMessage) (capabilities.CapabilityEndpoint, error) {
	for address, endpoint := range r.endpoints {
		if endpoint.CapabilityEndpointID == ref {
			endpoint.Status = capabilities.EndpointStatusDisabled
			r.endpoints[address] = endpoint
			return endpoint, nil
		}
	}
	return capabilities.CapabilityEndpoint{}, fmt.Errorf("endpoint not found: %s", ref)
}

func (r *acceptanceProviderRegistry) DisableRuntimeBinding(_ context.Context, _ requestctx.Context, ref string, _ json.RawMessage) (capabilities.EndpointRuntimeBinding, error) {
	for versionID, binding := range r.bindings {
		if binding.RuntimeBindingID == ref {
			binding.Status = capabilities.RuntimeBindingStatusDisabled
			r.bindings[versionID] = binding
			return binding, nil
		}
	}
	return capabilities.EndpointRuntimeBinding{}, fmt.Errorf("runtime binding not found: %s", ref)
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitFixtureHealthy(t *testing.T, ctx context.Context, port int, pidPath string, previousPID int) int {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		pid, err := readPIDIfPresent(pidPath)
		if err == nil && pid != 0 && pid != previousPID {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return pid
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("fixture did not become healthy on port %d", port)
	return 0
}

func waitProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("process %d did not exit", pid)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	pid, err := readPIDIfPresent(path)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func readPIDIfPresent(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

func runAcceptanceCommand(t *testing.T, ctx context.Context, dir, name string, args ...string) {
	t.Helper()
	command := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		command.Dir = dir
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func writeAcceptanceFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func launchAgentPlist(label, binaryPath, pidPath, logPath string, port int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>--listen</string><string>127.0.0.1:%d</string><string>--pid-file</string><string>%s</string><string>--log-file</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><false/>
</dict></plist>
`, label, binaryPath, port, pidPath, logPath)
}

func acceptanceAllowlist(key, label string, operations []serviceregistry.Operation) string {
	values := make([]string, 0, len(operations))
	for _, operation := range operations {
		values = append(values, string(operation))
	}
	return fmt.Sprintf("records:\n  - schema_version: loom.service_allowlist.v1\n    key: %s\n    node_key: macbook\n    manager: launchd\n    unit: %s\n    operations: [%s]\n    lifecycle_policy: service_operations\n    health:\n      kind: manager\n    log_limits:\n      max_lines: 10\n      max_bytes: 8192\n      max_line_bytes: 1024\n      max_age_seconds: 300\n", key, label, strings.Join(values, ", "))
}
