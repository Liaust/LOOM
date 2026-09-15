package nodeagent

import (
	"context"
	"encoding/json"
	"loom.local/loom/internal/communication"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/routing"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationOutboxFreezesExactEnvelopeBeforeAck(t *testing.T) {
	dir := t.TempDir()
	store := Store{DataDir: dir}
	cfg := Config{NodeKey: "fixture"}
	state := State{NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", CredentialToken: "synthetic-transport-credential"}
	address := "workspace/fixture@system.project.application.inspect"
	d := routing.RemoteDispatchPayload{RouteID: "route-fixture", CapabilityCallID: "call-fixture", TargetNodeID: state.NodeID, ProviderID: "provider-fixture", ProviderAddress: "workspace/fixture@system", CapabilityEndpointID: "endpoint-fixture", CapabilityAddress: address, Operation: address, Input: json.RawMessage(`{}`)}
	raw, _ := json.Marshal(d)
	message := communication.Message{CommunicationMessageID: "message-fixture", Kind: communication.KindCapabilityDispatch, PayloadJSON: raw}
	input, e := buildCapabilityResultInput(context.Background(), cfg, state, store, message)
	if e != nil {
		t.Fatal(e)
	}
	if input.Payload.ExecutionStatus != routing.CapabilityCallStatusFailed {
		t.Fatal("invalid input claimed success")
	}
	rs := noderuntime.NewStore(dir)
	frozen, e := rs.ReadApplicationResult(noderuntime.ApplicationResultKey(d.CapabilityCallID))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(frozen.Envelope), state.CredentialToken) {
		t.Fatal("transport secret persisted")
	}
	again, e := buildCapabilityResultInput(context.Background(), cfg, state, store, message)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(input)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("retry changed full result envelope")
	}
	queued, e := queueApplicationResult(rs, input)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = queueApplicationResult(rs, input); e != nil {
		t.Fatal(e)
	}
	// Simulate loss at the generic pending/inflight rename window, after frozen
	// publication. Recovery requeues identical bytes, without a helper execution.
	if e = os.Remove(filepath.Join(dir, "outbox", noderuntime.OutboxStatusPending, queued.LocalOutboxID+".json")); e != nil {
		t.Fatal(e)
	}
	if e = rs.RestoreApplicationResults(128); e != nil {
		t.Fatal(e)
	}
	changed := message
	d.Input = json.RawMessage(`{"different":true}`)
	changed.PayloadJSON, _ = json.Marshal(d)
	if _, e = buildCapabilityResultInput(context.Background(), cfg, state, store, changed); e == nil {
		t.Fatal("same call changed dispatch")
	}
}
