package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	"loom.local/loom/internal/communication"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/routing"
)

// Keep partial typed receipts and exact integers. The helper returns only
// bounded evidence, never config or credential bytes; strip transport auth by
// typed assignment, without float64-based recursive JSON sanitization.
func buildApplicationResultInput(ctx context.Context, config Config, state State, store Store, message communication.Message, d routing.RemoteDispatchPayload) (routing.RemoteResultInput, error) {
	runtimeStore := noderuntime.NewStore(store.DataDir)
	key := noderuntime.ApplicationResultKey(d.CapabilityCallID)
	release, e := runtimeStore.AcquireServiceExecutionLock(ctx, key)
	if e != nil {
		return routing.RemoteResultInput{}, e
	}
	defer release()
	digestBytes := sha256.Sum256(message.PayloadJSON)
	digest := hex.EncodeToString(digestBytes[:])
	if old, e := runtimeStore.ReadApplicationResult(key); e == nil {
		if old.DispatchDigest != digest {
			return routing.RemoteResultInput{}, errors.New("application.dispatch.call_conflict")
		}
		var input routing.RemoteResultInput
		if e = json.Unmarshal(old.Envelope, &input); e != nil {
			return input, e
		}
		input.CredentialToken = state.CredentialToken
		return input, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return routing.RemoteResultInput{}, e
	}
	started := time.Now().UTC()
	result, executeErr := executeApplicationDispatch(ctx, config, state, store, d)
	completed := time.Now().UTC()
	payload := routing.RemoteResultPayload{RouteID: d.RouteID, CapabilityCallID: d.CapabilityCallID, NodeID: state.NodeID, ProviderID: d.ProviderID, ProviderAddress: d.ProviderAddress, CapabilityEndpointID: d.CapabilityEndpointID, CapabilityAddress: d.CapabilityAddress, Operation: d.Operation, ExecutionStatus: routing.CapabilityCallStatusCompleted, StartedAt: &started, CompletedAt: &completed, ResultJSON: objectOrEmpty(result), ResultRefsJSON: json.RawMessage(`{}`), RuntimeMetadataJSON: runtimeDispatchMetadata(config, state, message.CommunicationMessageID, d)}
	if executeErr != nil {
		payload.ExecutionStatus = routing.CapabilityCallStatusFailed
		payload.ErrorCode, payload.ErrorMessage = applicationDispatchFailure(executeErr)
	}
	input := routing.RemoteResultInput{NodeRef: state.NodeID, IdempotencyKey: "capability.result." + state.NodeID + "." + d.CapabilityCallID, Payload: payload, Metadata: objectJSON(map[string]any{"source": "loom-node-agent", "dispatch_message_id": message.CommunicationMessageID})}
	envelope, e := json.Marshal(input)
	if e != nil {
		return input, e
	}
	record := noderuntime.ApplicationResultRecord{DispatchDigest: digest, Envelope: envelope, Outbox: noderuntime.NewApplicationResultOutbox(d.CapabilityCallID, input.IdempotencyKey, nodeAgentMessageCorrelationID("", message), message.CommunicationMessageID, envelope, completed)}
	if e = runtimeStore.PublishApplicationResult(key, record); e != nil {
		return routing.RemoteResultInput{}, e
	}
	input.CredentialToken = state.CredentialToken
	return input, nil
}
func queueApplicationResult(store noderuntime.Store, input routing.RemoteResultInput) (noderuntime.OutboxItem, error) {
	record, e := store.ReadApplicationResult(noderuntime.ApplicationResultKey(input.Payload.CapabilityCallID))
	if e != nil {
		return noderuntime.OutboxItem{}, e
	}
	return store.QueueApplicationResult(record)
}
