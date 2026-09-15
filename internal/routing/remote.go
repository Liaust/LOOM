package routing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
)

func (s Service) dispatchRemote(ctx context.Context, req requestctx.Context, plan RoutePlan, route Route, call CapabilityCall, input json.RawMessage, idempotencyKey string, eventIDs []string) (Route, CapabilityCall, string, []string, error) {
	if plan.RouteKind != RouteKindRemote {
		return Route{}, CapabilityCall{}, "", eventIDs, fmt.Errorf("route is not remote")
	}
	snapshot, err := s.runtimeBindingSnapshotForPlan(ctx, plan)
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	runtimeMetadata := remoteDispatchRuntimeMetadata(snapshot)
	payload := RemoteDispatchPayload{
		RouteID:              route.RouteID,
		CapabilityCallID:     call.CapabilityCallID,
		TargetNodeID:         route.TargetNodeID,
		ProviderID:           route.ProviderID,
		ProviderAddress:      plan.ProviderAddress,
		CapabilityEndpointID: route.CapabilityEndpointID,
		CapabilityAddress:    plan.CapabilityAddress,
		Operation:            call.Operation,
		ActorID:              route.ActorID,
		OriginNodeID:         route.OriginNodeID,
		ScopeID:              pointerValue(route.OriginScopeID),
		PolicyDecisionID:     pointerValue(route.PolicyDecisionID),
		GrantID:              pointerValue(route.GrantID),
		Input:                objectOrDefault(input),
		IdempotencyKey:       strings.TrimSpace(idempotencyKey),
		RequestedAt:          time.Now().UTC(),
		RuntimeBinding:       snapshot,
		Metadata:             objectOrDefault(mustJSON(runtimeMetadata)),
	}
	payloadJSON := objectOrDefault(mustJSON(payload))
	dispatchKey := "capability.dispatch." + call.CapabilityCallID

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	defer tx.Rollback()

	messageID, messageCreated, err := insertCommunicationMessageTx(ctx, tx, communicationMessageInsert{
		NodeID:           route.TargetNodeID,
		Direction:        communication.DirectionMainToNode,
		Kind:             communication.KindCapabilityDispatch,
		Status:           communication.StatusAvailable,
		IdempotencyKey:   dispatchKey,
		CorrelationID:    route.CorrelationID,
		RouteID:          route.RouteID,
		CapabilityCallID: call.CapabilityCallID,
		PayloadJSON:      payloadJSON,
		Metadata: objectOrDefault(mustJSON(mergeStringAnyMaps(map[string]any{
			"source":             "routing.remote_dispatch",
			"capability_address": plan.CapabilityAddress,
			"provider_address":   plan.ProviderAddress,
		}, runtimeMetadata))),
	})
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	if messageCreated {
		event, err := appendCommunicationMessageCreatedEvent(ctx, tx, req, messageID, route.TargetNodeID, communication.DirectionMainToNode, communication.KindCapabilityDispatch, communication.StatusAvailable, route.RouteID, call.CapabilityCallID)
		if err != nil {
			return Route{}, CapabilityCall{}, "", eventIDs, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}

	resultRefs := objectOrDefault(mustJSON(mergeStringAnyMaps(map[string]any{
		"dispatch_message_id": messageID,
		"dispatch_kind":       communication.KindCapabilityDispatch,
	}, runtimeMetadata)))
	route, err = scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'dispatched',
		    dispatched_at = COALESCE(dispatched_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), route.RouteID))
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	call, err = scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'dispatched',
		    result_refs_json = $2,
		    updated_at = now()
		WHERE capability_call_id = $1
	`), call.CapabilityCallID, resultRefs))
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}

	eventReq := routeEventRequest(req, route)
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteDispatched, route.Status, "remote_dispatch_enqueued", map[string]any{
		"communication_message_id": messageID,
	})
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallDispatched, call.Status, "remote_dispatch_enqueued", map[string]any{
		"communication_message_id": messageID,
	})
	if err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)

	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, "", eventIDs, err
	}
	return route, call, messageID, eventIDs, nil
}

func remoteDispatchRuntimeMetadata(snapshot *RuntimeBindingSnapshot) map[string]any {
	metadata := map[string]any{
		"source":     "routing.remote_dispatch",
		"slice":      "11_part_2",
		"route_kind": RouteKindRemote,
	}
	if snapshot != nil {
		metadata["runtime_kind"] = snapshot.RuntimeKind
		metadata["runtime_binding_id"] = snapshot.RuntimeBindingID
		metadata["runtime_snapshot_hash"] = snapshot.SnapshotHash
	}
	return metadata
}

func mergeStringAnyMaps(base map[string]any, extra map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func (s Service) runtimeBindingSnapshotForPlan(ctx context.Context, plan RoutePlan) (*RuntimeBindingSnapshot, error) {
	if strings.TrimSpace(plan.ActiveEndpointVersionID) == "" {
		return nil, nil
	}
	inspection, err := capabilities.NewService(s.DB).InspectRuntimeBinding(ctx, plan.CapabilityAddress)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load runtime binding snapshot: %w", err)
	}
	snapshot := RuntimeBindingSnapshot{
		RuntimeBindingID:            inspection.Binding.RuntimeBindingID,
		CapabilityEndpointVersionID: inspection.Binding.CapabilityEndpointVersionID,
		RuntimeKind:                 inspection.Binding.RuntimeKind,
		RuntimeConfigJSON:           objectOrDefault(inspection.Binding.RuntimeConfigJSON),
		InputMappingJSON:            objectOrDefault(inspection.Binding.InputMappingJSON),
		OutputMappingJSON:           objectOrDefault(inspection.Binding.OutputMappingJSON),
		Status:                      inspection.Binding.Status,
		VersionLabel:                inspection.EndpointVersion.VersionLabel,
		CapturedAt:                  time.Now().UTC(),
	}
	if inspection.EndpointVersion.ImplementationHash != nil {
		snapshot.ImplementationHash = *inspection.EndpointVersion.ImplementationHash
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	snapshot.SnapshotHash = "sha256:" + hex.EncodeToString(sum[:])
	return &snapshot, nil
}

func (s Service) IngestRemoteResult(ctx context.Context, req requestctx.Context, input RemoteResultInput) (RemoteResultOutcome, error) {
	input = normalizeRemoteResultInput(input)
	if input.NodeRef == "" {
		return RemoteResultOutcome{}, fmt.Errorf("node_ref is required")
	}
	if input.CredentialToken == "" {
		return RemoteResultOutcome{}, fmt.Errorf("credential_token is required")
	}
	if input.Payload.RouteID == "" {
		return RemoteResultOutcome{}, fmt.Errorf("payload.route_id is required")
	}
	if input.Payload.CapabilityCallID == "" {
		return RemoteResultOutcome{}, fmt.Errorf("payload.capability_call_id is required")
	}
	if input.Payload.ExecutionStatus != CapabilityCallStatusCompleted && input.Payload.ExecutionStatus != CapabilityCallStatusFailed {
		return RemoteResultOutcome{}, fmt.Errorf("unsupported execution status: %s", input.Payload.ExecutionStatus)
	}
	if input.Payload.ExecutionStatus == CapabilityCallStatusFailed && input.Payload.ErrorCode == "" {
		return RemoteResultOutcome{}, fmt.Errorf("failed capability result requires error_code")
	}
	if err := validateJSONObject(input.Payload.ResultJSON, "payload.result"); err != nil {
		return RemoteResultOutcome{}, err
	}
	if err := validateJSONObject(input.Payload.ResultRefsJSON, "payload.result_refs"); err != nil {
		return RemoteResultOutcome{}, err
	}
	if err := validateJSONObject(input.Payload.RuntimeMetadataJSON, "payload.runtime_metadata"); err != nil {
		return RemoteResultOutcome{}, err
	}
	if err := validateJSONObject(input.Metadata, "metadata"); err != nil {
		return RemoteResultOutcome{}, err
	}

	nodeService := nodes.NewService(s.DB)
	node, err := nodeService.GetNode(ctx, input.NodeRef)
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	_, credentialNode, err := nodeService.AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	if credentialNode.NodeID != node.NodeID {
		return RemoteResultOutcome{}, fmt.Errorf("credential does not belong to node %s", node.NodeID)
	}
	if input.Payload.NodeID != "" && input.Payload.NodeID != node.NodeID {
		return RemoteResultOutcome{}, fmt.Errorf("payload node_id does not match authenticated node")
	}

	payloadJSON := objectOrDefault(mustJSON(input.Payload))
	payloadHash := hashRoutingPayload(payloadJSON)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	defer tx.Rollback()

	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL()+`
		WHERE route_id = $1
		FOR UPDATE
	`, input.Payload.RouteID))
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL()+`
		WHERE capability_call_id = $1
		FOR UPDATE
	`, input.Payload.CapabilityCallID))
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	if call.RouteID != route.RouteID {
		return RemoteResultOutcome{}, fmt.Errorf("capability call does not belong to route")
	}
	if route.RouteKind != RouteKindRemote {
		return RemoteResultOutcome{}, fmt.Errorf("route is not remote")
	}
	if route.TargetNodeID != node.NodeID || call.TargetNodeID != node.NodeID {
		return RemoteResultOutcome{}, fmt.Errorf("remote result does not belong to authenticated node")
	}
	if input.Payload.ProviderID != "" && input.Payload.ProviderID != route.ProviderID {
		return RemoteResultOutcome{}, fmt.Errorf("payload provider_id does not match route")
	}
	if input.Payload.CapabilityEndpointID != "" && input.Payload.CapabilityEndpointID != route.CapabilityEndpointID {
		return RemoteResultOutcome{}, fmt.Errorf("payload capability_endpoint_id does not match route")
	}

	if isApplicationPrerequisiteOperation(call.Operation) {
		if _, _, err := validateApplicationPrerequisiteResult(ctx, tx, route, call, input.Payload); err != nil {
			return RemoteResultOutcome{}, err
		}
	}

	messageID, messageCreated, err := insertCommunicationMessageTx(ctx, tx, communicationMessageInsert{
		NodeID:              node.NodeID,
		Direction:           communication.DirectionNodeToMain,
		Kind:                communication.KindCapabilityResult,
		Status:              communication.StatusAcked,
		IdempotencyKey:      input.IdempotencyKey,
		CorrelationID:       route.CorrelationID,
		RouteID:             route.RouteID,
		CapabilityCallID:    call.CapabilityCallID,
		PayloadJSON:         payloadJSON,
		ResultJSON:          objectOrDefault(input.Payload.ResultJSON),
		ErrorJSON:           remoteErrorJSON(input.Payload),
		Metadata:            objectOrDefault(input.Metadata),
		ExistingPayloadHash: payloadHash,
	})
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	eventIDs := []string{}
	if messageCreated {
		event, err := appendCommunicationMessageCreatedEvent(ctx, tx, routeEventRequest(req, route), messageID, node.NodeID, communication.DirectionNodeToMain, communication.KindCapabilityResult, communication.StatusAcked, route.RouteID, call.CapabilityCallID)
		if err != nil {
			return RemoteResultOutcome{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}

	if call.Status == CapabilityCallStatusCompleted || call.Status == CapabilityCallStatusFailed {
		if call.Status != input.Payload.ExecutionStatus || !sameJSONValue([]byte(objectOrDefault(call.ResultJSON)), input.Payload.ResultJSON) {
			return RemoteResultOutcome{}, fmt.Errorf("capability call already has a conflicting terminal result")
		}
		if err := tx.Commit(); err != nil {
			return RemoteResultOutcome{}, err
		}
		if call.Status == CapabilityCallStatusCompleted {
			observedAt := time.Now().UTC()
			if input.Payload.CompletedAt != nil {
				observedAt = input.Payload.CompletedAt.UTC()
			}
			if err := s.persistServiceManagerObservation(ctx, req, route.ProviderID, route.CapabilityEndpointID, objectOrDefault(call.ResultJSON), observedAt); err != nil {
				return RemoteResultOutcome{}, err
			}
		}
		return RemoteResultOutcome{
			MessageID:      messageID,
			Route:          route,
			CapabilityCall: call,
			EventIDs:       eventIDs,
			Status:         call.Status,
			Result:         objectOrDefault(call.ResultJSON),
			ResultRefs:     objectOrDefault(call.ResultRefsJSON),
			ErrorCode:      pointerValue(call.ErrorCode),
			ErrorMessage:   pointerValue(call.ErrorMessage),
			Idempotent:     true,
		}, nil
	}
	if call.Status != CapabilityCallStatusDispatched && call.Status != CapabilityCallStatusExecuting {
		return RemoteResultOutcome{}, fmt.Errorf("capability call is not awaiting a remote result: %s", call.Status)
	}

	resultRefs := mergeResultRefs(input.Payload.ResultRefsJSON, map[string]any{
		"result_message_id": messageID,
		"result_kind":       communication.KindCapabilityResult,
		"runtime_metadata":  jsonObject(input.Payload.RuntimeMetadataJSON),
	})
	if input.Payload.ExecutionStatus == CapabilityCallStatusCompleted {
		route, call, eventIDs, err = markRemoteCompletedTx(ctx, tx, routeEventRequest(req, route), route, call, objectOrDefault(input.Payload.ResultJSON), resultRefs, input.Payload.CompletedAt, eventIDs)
	} else {
		route, call, eventIDs, err = markRemoteFailedTx(ctx, tx, routeEventRequest(req, route), route, call, strings.TrimSpace(input.Payload.ErrorCode), strings.TrimSpace(input.Payload.ErrorMessage), objectOrDefault(input.Payload.ResultJSON), resultRefs, input.Payload.CompletedAt, eventIDs)
	}
	if err != nil {
		return RemoteResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return RemoteResultOutcome{}, err
	}
	if call.Status == CapabilityCallStatusCompleted {
		observedAt := time.Now().UTC()
		if input.Payload.CompletedAt != nil {
			observedAt = input.Payload.CompletedAt.UTC()
		}
		if err := s.persistServiceManagerObservation(ctx, req, route.ProviderID, route.CapabilityEndpointID, objectOrDefault(call.ResultJSON), observedAt); err != nil {
			return RemoteResultOutcome{}, err
		}
	}
	return RemoteResultOutcome{
		MessageID:      messageID,
		Route:          route,
		CapabilityCall: call,
		EventIDs:       eventIDs,
		Status:         call.Status,
		Result:         objectOrDefault(call.ResultJSON),
		ResultRefs:     objectOrDefault(call.ResultRefsJSON),
		ErrorCode:      pointerValue(call.ErrorCode),
		ErrorMessage:   pointerValue(call.ErrorMessage),
	}, nil
}

type communicationMessageInsert struct {
	NodeID              string
	Direction           string
	Kind                string
	Status              string
	IdempotencyKey      string
	CorrelationID       string
	RouteID             string
	CapabilityCallID    string
	PayloadJSON         json.RawMessage
	ResultJSON          json.RawMessage
	ErrorJSON           json.RawMessage
	Metadata            json.RawMessage
	ExistingPayloadHash string
}

func insertCommunicationMessageTx(ctx context.Context, tx *sql.Tx, input communicationMessageInsert) (messageID string, created bool, err error) {
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.PayloadJSON = objectOrDefault(input.PayloadJSON)
	input.ResultJSON = objectOrDefault(input.ResultJSON)
	input.ErrorJSON = objectOrDefault(input.ErrorJSON)
	input.Metadata = objectOrDefault(input.Metadata)
	payloadHash := strings.TrimSpace(input.ExistingPayloadHash)
	if payloadHash == "" {
		payloadHash = hashRoutingPayload(input.PayloadJSON)
	}
	if input.IdempotencyKey != "" {
		var existingID, existingPayloadHash string
		var existingPayload []byte
		err := tx.QueryRowContext(ctx, `
			SELECT communication_message_id, COALESCE(payload_hash, ''), payload_json
			FROM communication.messages
			WHERE node_id = $1 AND direction = $2 AND idempotency_key = $3
		`, input.NodeID, input.Direction, input.IdempotencyKey).Scan(&existingID, &existingPayloadHash, &existingPayload)
		if err == nil {
			if existingPayloadHash != payloadHash && !sameJSONValue(existingPayload, input.PayloadJSON) {
				return "", false, fmt.Errorf("idempotency conflict for communication message")
			}
			return existingID, false, nil
		}
		if err != sql.ErrNoRows {
			return "", false, err
		}
	}
	messageID = ids.NewCommunicationMessageID()
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO communication.messages (
			communication_message_id, node_id, direction, kind, status,
			idempotency_key, correlation_id, route_id, capability_call_id,
			payload_json, payload_hash, result_json, error_json, metadata
		)
		VALUES ($1, $2, $3, $4, $5,
		        nullif($6, ''), nullif($7, ''), nullif($8, ''), nullif($9, ''),
		        $10, $11, $12, $13, $14)
		RETURNING communication_message_id
	`, messageID, input.NodeID, input.Direction, input.Kind, input.Status,
		input.IdempotencyKey, input.CorrelationID, input.RouteID, input.CapabilityCallID,
		input.PayloadJSON, payloadHash, input.ResultJSON, input.ErrorJSON, input.Metadata).Scan(&messageID); err != nil {
		return "", false, err
	}
	return messageID, true, nil
}

func appendCommunicationMessageCreatedEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, messageID, nodeID, direction, kind, status, routeID, callID string) (events.Event, error) {
	return events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeCommunicationMessageCreated,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      "communication_message",
		TargetID:        messageID,
		RouteID:         routeID,
		Status:          status,
		Result:          "created",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"communication_message_id": messageID,
			"node_id":                  nodeID,
			"direction":                direction,
			"kind":                     kind,
			"status":                   status,
			"route_id":                 routeID,
			"capability_call_id":       callID,
		},
	})
}

func markRemoteCompletedTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, route Route, call CapabilityCall, result, resultRefs json.RawMessage, completedAt *time.Time, eventIDs []string) (Route, CapabilityCall, []string, error) {
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'completed',
		    completed_at = COALESCE($2, completed_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), route.RouteID, completedAt))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err = scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'completed',
		    result_json = $2,
		    result_refs_json = $3,
		    completed_at = COALESCE($4, completed_at, now()),
		    updated_at = now()
		WHERE capability_call_id = $1
	`), call.CapabilityCallID, objectOrDefault(result), objectOrDefault(resultRefs), completedAt))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	event, err := appendRouteEvent(ctx, tx, req, route, events.TypeRouteCompleted, route.Status, "remote_result_completed", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, req, route, call, events.TypeCapabilityCallCompleted, call.Status, "remote_result_completed", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	return route, call, eventIDs, nil
}

func markRemoteFailedTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, route Route, call CapabilityCall, code, message string, result, resultRefs json.RawMessage, completedAt *time.Time, eventIDs []string) (Route, CapabilityCall, []string, error) {
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'failed',
		    failure_code = nullif($2, ''),
		    failure_message = nullif($3, ''),
		    failed_at = COALESCE($4, failed_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), route.RouteID, code, message, completedAt))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err = scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'failed',
		    error_code = nullif($2, ''),
		    error_message = nullif($3, ''),
		    result_json = $4,
		    result_refs_json = $5,
		    failed_at = COALESCE($6, failed_at, now()),
		    updated_at = now()
		WHERE capability_call_id = $1
	`), call.CapabilityCallID, code, message, objectOrDefault(result), objectOrDefault(resultRefs), completedAt))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	event, err := appendRouteEvent(ctx, tx, req, route, events.TypeRouteFailed, route.Status, code, map[string]any{"message": message})
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, req, route, call, events.TypeCapabilityCallFailed, call.Status, code, map[string]any{"message": message})
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	return route, call, eventIDs, nil
}

func normalizeRemoteResultInput(input RemoteResultInput) RemoteResultInput {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Payload.RouteID = strings.TrimSpace(input.Payload.RouteID)
	input.Payload.CapabilityCallID = strings.TrimSpace(input.Payload.CapabilityCallID)
	input.Payload.NodeID = strings.TrimSpace(input.Payload.NodeID)
	input.Payload.ProviderID = strings.TrimSpace(input.Payload.ProviderID)
	input.Payload.ProviderAddress = strings.TrimSpace(input.Payload.ProviderAddress)
	input.Payload.CapabilityEndpointID = strings.TrimSpace(input.Payload.CapabilityEndpointID)
	input.Payload.CapabilityAddress = strings.TrimSpace(input.Payload.CapabilityAddress)
	input.Payload.Operation = strings.TrimSpace(input.Payload.Operation)
	input.Payload.ExecutionStatus = strings.TrimSpace(input.Payload.ExecutionStatus)
	input.Payload.ErrorCode = strings.TrimSpace(input.Payload.ErrorCode)
	input.Payload.ErrorMessage = strings.TrimSpace(input.Payload.ErrorMessage)
	input.Payload.ResultJSON = objectOrDefault(input.Payload.ResultJSON)
	input.Payload.ResultRefsJSON = objectOrDefault(input.Payload.ResultRefsJSON)
	input.Payload.RuntimeMetadataJSON = objectOrDefault(input.Payload.RuntimeMetadataJSON)
	input.Metadata = objectOrDefault(input.Metadata)
	if input.IdempotencyKey == "" && input.Payload.CapabilityCallID != "" {
		input.IdempotencyKey = "capability.result." + input.Payload.CapabilityCallID
	}
	return input
}

func remoteErrorJSON(payload RemoteResultPayload) json.RawMessage {
	if payload.ExecutionStatus != CapabilityCallStatusFailed {
		return json.RawMessage(`{}`)
	}
	return objectOrDefault(mustJSON(map[string]any{
		"code":    payload.ErrorCode,
		"message": payload.ErrorMessage,
	}))
}

func mergeResultRefs(base json.RawMessage, additions map[string]any) json.RawMessage {
	out := jsonObject(base)
	for key, value := range additions {
		out[key] = value
	}
	return objectOrDefault(mustJSON(out))
}

func jsonObject(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(objectOrDefault(raw), &out)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func hashRoutingPayload(payload json.RawMessage) string {
	sum := sha256.Sum256(objectOrDefault(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Compare JSONB round trips without converting exact numbers to float64.
func sameJSONValue(left []byte, right json.RawMessage) bool {
	decode := func(raw []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var value any
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if d.Decode(new(any)) != io.EOF {
			return nil, errors.New("multiple JSON values")
		}
		return value, nil
	}
	a, err := decode(left)
	if err != nil {
		return false
	}
	b, err := decode(right)
	if err != nil {
		return false
	}
	return equalJSONValue(a, b)
}

func equalJSONValue(a, b any) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool:
		v, ok := b.(bool)
		return ok && a == v
	case string:
		v, ok := b.(string)
		return ok && a == v
	case json.Number:
		v, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, validA := exactJSONNumber(a)
		y, validB := exactJSONNumber(v)
		return validA && validB && x == y
	case []any:
		v, ok := b.([]any)
		if !ok || len(a) != len(v) {
			return false
		}
		for i := range a {
			if !equalJSONValue(a[i], v[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		v, ok := b.(map[string]any)
		if !ok || len(a) != len(v) {
			return false
		}
		for k, x := range a {
			y, ok := v[k]
			if !ok || !equalJSONValue(x, y) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Keep the significand and decimal exponent separate. Even very large
// exponents never allocate a correspondingly large power of ten.
func exactJSONNumber(n json.Number) (string, bool) {
	value := string(n)
	sign := ""
	if strings.HasPrefix(value, "-") {
		sign = "-"
		value = value[1:]
	}
	exponent := new(big.Int)
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exp := value[i+1:]
		// Bound work on hostile exponent lexemes, not their numeric magnitude.
		if len(exp) > 1024 {
			return "", false
		}
		if _, ok := exponent.SetString(exp, 10); !ok {
			return "", false
		}
		value = value[:i]
	}
	if i := strings.IndexByte(value, '.'); i >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(value)-i-1)))
		value = value[:i] + value[i+1:]
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0", true
	}
	trimmed := strings.TrimRight(value, "0")
	exponent.Add(exponent, big.NewInt(int64(len(value)-len(trimmed))))
	return sign + trimmed + "e" + exponent.String(), true
}
