package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/serviceobservation"
)

func (s Service) persistServiceManagerObservation(ctx context.Context, req requestctx.Context, providerID, endpointID string, resultJSON json.RawMessage, observedAt time.Time) error {
	var providerType, endpointStatus, endpointName, activeVersionID, endpointVersionStatus, bindingID, bindingKind, bindingStatus string
	var metadata json.RawMessage
	if err := s.DB.QueryRowContext(ctx, `
		SELECT p.provider_type, e.status, e.endpoint_name, e.metadata,
		       COALESCE(e.active_endpoint_version_id, ''),
		       COALESCE(ev.status, ''),
		       COALESCE(b.runtime_binding_id, ''),
		       COALESCE(b.runtime_kind, ''),
		       COALESCE(b.status, '')
		FROM capabilities.capability_endpoints e
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		LEFT JOIN capabilities.endpoint_versions ev
			ON ev.capability_endpoint_version_id = e.active_endpoint_version_id
		LEFT JOIN capabilities.endpoint_runtime_bindings b
			ON b.capability_endpoint_version_id = e.active_endpoint_version_id
		WHERE p.provider_id = $1 AND e.capability_endpoint_id = $2
	`, providerID, endpointID).Scan(&providerType, &endpointStatus, &endpointName, &metadata, &activeVersionID, &endpointVersionStatus, &bindingID, &bindingKind, &bindingStatus); err != nil {
		return err
	}
	target := plannedTarget{
		RoutePlan:             RoutePlan{ActiveEndpointVersionID: activeVersionID},
		EndpointStatus:        endpointStatus,
		ProviderType:          providerType,
		EndpointName:          endpointName,
		EndpointMetadata:      metadata,
		EndpointVersionStatus: endpointVersionStatus,
		RuntimeBindingID:      bindingID,
		RuntimeBindingKind:    bindingKind,
		RuntimeBindingStatus:  bindingStatus,
	}
	operation, ok := serviceManagementEndpointOperation(target)
	if !ok {
		return nil
	}
	result, err := serviceobservation.Decode(resultJSON)
	if err != nil {
		return fmt.Errorf("decode service manager observation: %w", err)
	}
	if result.Operation != operation {
		return fmt.Errorf("service manager observation operation mismatch")
	}
	health, err := serviceobservation.ProviderHealth(result, observedAt)
	if err != nil {
		return err
	}
	_, err = capabilities.NewService(s.DB).UpsertProviderHealth(ctx, req, providerID, health)
	return err
}
