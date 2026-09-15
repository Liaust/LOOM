package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"loom.local/loom/internal/capabilities"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
	"loom.local/loom/internal/version"
)

func applicationDispatchOperation(config Config, d routing.RemoteDispatchPayload) string {
	provider := capabilities.NodeSystemProviderAddress(addressSegment(config.NodeKey))
	for _, op := range []string{"inspect", "apply", "retire", "prerequisites"} {
		address := provider + ".project.application." + op
		if d.CapabilityAddress == address && (d.Operation == address || d.Operation == "capability:"+address) && d.ProviderAddress == provider && d.RuntimeBinding == nil {
			return op
		}
	}
	return ""
}
func executeApplicationDispatch(ctx context.Context, config Config, state State, store Store, d routing.RemoteDispatchPayload) (json.RawMessage, error) {
	op := applicationDispatchOperation(config, d)
	if op == "" {
		return nil, errors.New("application.dispatch.endpoint")
	}
	if op == "prerequisites" {
		q, err := serviceregistry.DecodeApplicationPrerequisiteQuery(d.Input)
		if err != nil {
			return nil, err
		}
		if d.TargetNodeID != state.NodeID || q.Owner.NodeID != state.NodeID || d.ProviderID == "" || d.CapabilityEndpointID == "" {
			return nil, errors.New("application.prerequisites.scope")
		}
		snapshot, err := (serviceregistry.ApplicationHelperClient{SocketPath: config.ServiceManager.ApplicationSocketPath}).QueryPrerequisites(ctx, q)
		if err != nil {
			return nil, err
		}
		return json.Marshal(snapshot)
	}
	q, e := serviceregistry.DecodeApplicationRuntimeRequest(d.Input)
	if e != nil {
		return nil, e
	}
	if q.Operation != op || q.Owner.NodeID != state.NodeID {
		return nil, errors.New("application.dispatch.scope")
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	release, e := runtimeStore.AcquireServiceExecutionLock(ctx, q.Owner.AllowlistKey())
	if e != nil {
		return nil, errors.New("application.dispatch.lock")
	}
	defer release()
	if op == "apply" {
		fenced, e := runtimeStore.ProjectArchiveFenceActive(projectquiescence.TargetKindService, q.Owner.AllowlistKey())
		if e != nil || fenced {
			return nil, errors.New("application.dispatch.archive_fenced")
		}
	}
	receipt, e := (serviceregistry.ApplicationHelperClient{SocketPath: config.ServiceManager.ApplicationSocketPath}).Execute(ctx, q)
	raw, marshalErr := json.Marshal(receipt)
	if marshalErr != nil {
		return nil, errors.New("application.dispatch.receipt")
	}
	return raw, e
}
func applicationCapabilities(address string) []capabilities.AdvertisedCapability {
	out := []capabilities.AdvertisedCapability{}
	for _, op := range []string{"inspect", "apply", "retire", "prerequisites"} {
		level := 5
		form := capabilities.CapabilityFormCommand
		risk := capabilities.RiskLevelHigh
		if op == "inspect" || op == "prerequisites" {
			level = 1
			form = capabilities.CapabilityFormQuery
			risk = capabilities.RiskLevelLow
		}
		name := "project.application." + op
		effects := json.RawMessage(`{"side_effects":[]}`)
		if op == "apply" {
			effects = json.RawMessage(`{"side_effects":["application.install","data.allocate","credential.load","application.restart","application.edge.configure","application.receipt.write"]}`)
		} else if op == "retire" {
			effects = json.RawMessage(`{"side_effects":["application.fence","application.stop","application.management.remove","application.receipt.write"]}`)
		}
		out = append(out, capabilities.AdvertisedCapability{EndpointName: name, CompactAddress: address + "." + name, ClassNamespace: "system", ClassName: name, DisplayName: "Project Application " + op, Description: "Inspect or change one exactly authorized published application on its owner node.", Form: form, InputSchemaJSON: json.RawMessage(`{"type":"object","required":["schema_version","operation","owner","operation_token","expected_revision","expected_installation_revision","policy_revision","location_revision","credential_revisions"],"properties":{"schema_version":{"const":"application.runtime.v1"},"operation":{"const":"` + op + `"},"owner":{"type":"object"},"operation_token":{"type":"string"},"expected_revision":{"type":"string"},"expected_installation_revision":{"type":"string"},"policy_revision":{"type":"string"},"location_revision":{"type":"string"},"credential_revisions":{"type":"object"},"descriptor_digest":{"type":"string"},"manifest":{"type":"object"},"data":{"type":"object"},"rollback":{"type":"boolean"}},"additionalProperties":false}`), OutputSchemaJSON: json.RawMessage(`{"type":"object"}`), RiskLevel: risk, ExecutionAuthorizationLevel: level, SideEffectsJSON: effects, VersionLabel: version.Current().Version, ManifestJSON: objectJSON(map[string]any{"source": "loom-node-agent", "handler": "system." + name, "execution": "remote_node", "explicit_authorization": true, "slice_enabled": true})})
		if op == "prerequisites" {
			out[len(out)-1].Description = "Collect scoped existing application prerequisites on the exact owner node."
			out[len(out)-1].InputSchemaJSON = json.RawMessage(`{"type":"object","required":["schema_version","owner"],"properties":{"schema_version":{"const":"application.prerequisites.v1"},"owner":{"type":"object","required":["project_id","node_id","resource"],"properties":{"project_id":{"type":"string"},"node_id":{"type":"string"},"resource":{"type":"string"}},"additionalProperties":false}},"additionalProperties":false}`)
		}
	}
	return out
}
func applicationDispatchFailure(e error) (string, string) {
	if strings.HasPrefix(e.Error(), "application.") && !strings.ContainsAny(e.Error(), " \n/") {
		return e.Error(), "application operation did not complete"
	}
	return "application.dispatch.failed", "application operation did not complete"
}

// The existing archive path still owns the node-local service lock and fence.
// Its application branch adds the root fence for a helper surviving caller loss.
type applicationArchiveManager struct {
	client serviceregistry.ApplicationHelperClient
	target projectquiescence.Target
}

func (m applicationArchiveManager) Execute(ctx context.Context, q serviceregistry.ManagerRequest) (serviceregistry.ManagerResult, error) {
	if q.AllowlistKey != m.target.AllowlistKey {
		return serviceregistry.ManagerResult{}, errors.New("application.archive.identity")
	}
	return m.client.Archive(ctx, serviceregistry.ApplicationArchiveControl{Target: m.target, Operation: q.Operation})
}
