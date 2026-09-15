package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/communication"
)

const applicationPrerequisiteEndpoint = "project.application.prerequisites"

var prerequisiteDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ApplicationPrerequisiteEvidence references existing durable records. It is an
// internal read result, not a grant, public DTO or current-health observation.
type ApplicationPrerequisiteEvidence struct {
	CallID, RouteID, DispatchMessageID, ResultMessageID string
	EndpointVersionID, ResultPayloadHash                string
	NodeID                                              string
	ReceivedAt                                          time.Time
}

func prerequisiteError(reason string) error {
	return fmt.Errorf("application.prerequisites.%s", reason)
}

func isApplicationPrerequisiteOperation(operation string) bool {
	address, err := capabilities.ParseAddress(strings.TrimPrefix(operation, capabilityOperationPrefix))
	return err == nil && strings.HasPrefix(address.ScopePath, "workspace/") && address.CompactAddress == capabilities.NodeSystemProviderAddress(strings.TrimPrefix(address.ScopePath, "workspace/"))+"."+applicationPrerequisiteEndpoint
}

// ReadApplicationPrerequisiteReport reads associated raw transport evidence in
// a consistent snapshot. It does not validate typed prerequisite facts. The
// serviceregistry reader owns that validation; the caller owns read scope and
// execution authority. This never collects new node facts.
func (s Service) ReadApplicationPrerequisiteReport(ctx context.Context, callID string, expectedQuery json.RawMessage) (json.RawMessage, ApplicationPrerequisiteEvidence, error) {
	var empty json.RawMessage
	var evidence ApplicationPrerequisiteEvidence
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return empty, evidence, err
	}
	defer tx.Rollback()
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL()+` WHERE capability_call_id=$1`, callID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, evidence, prerequisiteError("call_missing")
	}
	if err != nil {
		return empty, evidence, err
	}
	if !isApplicationPrerequisiteOperation(call.Operation) {
		return empty, evidence, prerequisiteError("endpoint_mismatch")
	}
	if call.Status != CapabilityCallStatusCompleted {
		return empty, evidence, prerequisiteError("call_" + call.Status)
	}
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL()+` WHERE route_id=$1`, call.RouteID))
	if err != nil {
		return empty, evidence, err
	}
	if route.Status != RouteStatusCompleted {
		return empty, evidence, prerequisiteError("route_incomplete")
	}
	var refs struct {
		ResultMessageID string `json:"result_message_id"`
	}
	if json.Unmarshal(call.ResultRefsJSON, &refs) != nil || refs.ResultMessageID == "" {
		return empty, evidence, prerequisiteError("result_missing")
	}
	message, err := readApplicationMessage(ctx, tx, `communication_message_id=$1`, refs.ResultMessageID)
	if err != nil {
		return empty, evidence, err
	}
	if !applicationMessageMatches(message, route, call, communication.DirectionNodeToMain, communication.KindCapabilityResult) || message.Status != communication.StatusAcked || message.PayloadHash == nil || !prerequisiteDigest.MatchString(*message.PayloadHash) {
		return empty, evidence, prerequisiteError("result_association")
	}
	var result RemoteResultPayload
	if json.Unmarshal(message.PayloadJSON, &result) != nil || result.ExecutionStatus != CapabilityCallStatusCompleted || !sameJSONValue(result.ResultJSON, call.ResultJSON) || !sameJSONValue(result.ResultJSON, message.ResultJSON) {
		return empty, evidence, prerequisiteError("result_conflict")
	}
	snapshot, dispatch, err := validateApplicationPrerequisiteResult(ctx, tx, route, call, result)
	if err != nil {
		return empty, evidence, err
	}
	if !sameJSONValue(call.InputJSON, expectedQuery) {
		return empty, evidence, prerequisiteError("query_mismatch")
	}
	var selected struct {
		Version string `json:"active_endpoint_version_id"`
	}
	_ = json.Unmarshal(route.SelectedPathJSON, &selected)
	evidence = ApplicationPrerequisiteEvidence{CallID: call.CapabilityCallID, RouteID: route.RouteID, DispatchMessageID: dispatch.CommunicationMessageID, ResultMessageID: message.CommunicationMessageID, EndpointVersionID: selected.Version, ResultPayloadHash: *message.PayloadHash, NodeID: call.TargetNodeID, ReceivedAt: message.CreatedAt}
	if err = tx.Commit(); err != nil {
		return empty, ApplicationPrerequisiteEvidence{}, err
	}
	return snapshot, evidence, nil
}

func readApplicationMessage(ctx context.Context, tx *sql.Tx, where string, args ...any) (communication.Message, error) {
	var m communication.Message
	err := tx.QueryRowContext(ctx, `SELECT communication_message_id,node_id,direction,kind,status,route_id,capability_call_id,payload_json,payload_hash,result_json,created_at FROM communication.messages WHERE `+where, args...).Scan(&m.CommunicationMessageID, &m.NodeID, &m.Direction, &m.Kind, &m.Status, &m.RouteID, &m.CapabilityCallID, &m.PayloadJSON, &m.PayloadHash, &m.ResultJSON, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = prerequisiteError("message_missing")
	}
	return m, err
}
func applicationMessageMatches(m communication.Message, r Route, c CapabilityCall, direction, kind string) bool {
	return m.NodeID == r.TargetNodeID && m.Direction == direction && m.Kind == kind && pointerValue(m.RouteID) == r.RouteID && pointerValue(m.CapabilityCallID) == c.CapabilityCallID
}

// Used before ingestion writes and again by the pure recorded-result reader.
func validateApplicationPrerequisiteResult(ctx context.Context, tx *sql.Tx, r Route, c CapabilityCall, p RemoteResultPayload) (json.RawMessage, communication.Message, error) {
	var empty json.RawMessage
	var noMessage communication.Message
	fail := func(reason string) (json.RawMessage, communication.Message, error) {
		return empty, noMessage, prerequisiteError(reason)
	}
	if r.RouteKind != RouteKindRemote || c.RouteID != r.RouteID || c.TargetNodeID != r.TargetNodeID || c.ProviderID != r.ProviderID || c.CapabilityEndpointID != r.CapabilityEndpointID || c.ActorID != r.ActorID || c.OriginNodeID != r.OriginNodeID || pointerValue(c.ScopeID) != pointerValue(r.OriginScopeID) {
		return fail("call_association")
	}
	m, err := readApplicationMessage(ctx, tx, `node_id=$1 AND direction=$2 AND kind=$3 AND idempotency_key=$4`, r.TargetNodeID, communication.DirectionMainToNode, communication.KindCapabilityDispatch, "capability.dispatch."+c.CapabilityCallID)
	if err != nil {
		return empty, noMessage, err
	}
	if !applicationMessageMatches(m, r, c, communication.DirectionMainToNode, communication.KindCapabilityDispatch) {
		return fail("dispatch_association")
	}
	var d RemoteDispatchPayload
	if json.Unmarshal(m.PayloadJSON, &d) != nil || d.RuntimeBinding != nil || d.RouteID != r.RouteID || d.CapabilityCallID != c.CapabilityCallID || d.TargetNodeID != r.TargetNodeID || d.ProviderID != r.ProviderID || d.CapabilityEndpointID != r.CapabilityEndpointID || d.Operation != c.Operation || d.ActorID != c.ActorID || d.OriginNodeID != c.OriginNodeID || d.ScopeID != pointerValue(c.ScopeID) || !sameJSONValue(d.Input, c.InputJSON) {
		return fail("dispatch_association")
	}
	var selected struct {
		ProviderID      string `json:"provider_id"`
		EndpointID      string `json:"capability_endpoint_id"`
		ProviderAddress string `json:"provider_address"`
		Address         string `json:"capability_address"`
		Version         string `json:"active_endpoint_version_id"`
		NodeID          string `json:"target_node_id"`
	}
	if json.Unmarshal(r.SelectedPathJSON, &selected) != nil || selected.ProviderID != d.ProviderID || selected.EndpointID != d.CapabilityEndpointID || selected.ProviderAddress != d.ProviderAddress || selected.Address != d.CapabilityAddress || selected.NodeID != d.TargetNodeID || selected.Version == "" {
		return fail("endpoint_mismatch")
	}
	address, err := capabilities.ParseAddress(d.CapabilityAddress)
	provider := capabilities.NodeSystemProviderAddress(strings.TrimPrefix(address.ScopePath, "workspace/"))
	if err != nil || address.CapabilityName != applicationPrerequisiteEndpoint || !strings.HasPrefix(address.ScopePath, "workspace/") || d.ProviderAddress != provider || d.CapabilityAddress != provider+"."+applicationPrerequisiteEndpoint || (d.Operation != d.CapabilityAddress && d.Operation != capabilityOperationPrefix+d.CapabilityAddress) {
		return fail("endpoint_mismatch")
	}
	if p.RouteID != d.RouteID || p.CapabilityCallID != d.CapabilityCallID || p.NodeID == "" || p.NodeID != d.TargetNodeID || p.ProviderID == "" || p.ProviderID != d.ProviderID || p.CapabilityEndpointID == "" || p.CapabilityEndpointID != d.CapabilityEndpointID || p.ProviderAddress != d.ProviderAddress || p.CapabilityAddress != d.CapabilityAddress || p.Operation != d.Operation {
		return fail("result_association")
	}
	if p.ExecutionStatus == CapabilityCallStatusFailed {
		return empty, m, nil
	}
	if p.ExecutionStatus != CapabilityCallStatusCompleted || p.ErrorCode != "" || p.ErrorMessage != "" {
		return fail("result_status")
	}
	return p.ResultJSON, m, nil
}
