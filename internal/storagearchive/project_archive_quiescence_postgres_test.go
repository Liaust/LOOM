package storagearchive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/capabilities"
	database "loom.local/loom/internal/db"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

func TestProjectArchiveRoutedQuiescencePostgresResultRoundTrip(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("LOOM_TEST_DB_URL"))
	if databaseURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set; routed quiescence JSONB acceptance requires a dedicated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	preflight, err := database.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	var databaseName string
	if err := preflight.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		_ = preflight.Close()
		t.Fatal(err)
	}
	if err := preflight.Close(); err != nil {
		t.Fatal(err)
	}
	if databaseName == "postgres" || databaseName == "template0" || databaseName == "template1" || databaseName == "loom" || databaseName == "loom_main" {
		t.Fatalf("LOOM_TEST_DB_URL points to non-disposable database %q", databaseName)
	}

	migrationsDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(ctx, databaseURL, migrationsDir); err != nil {
		t.Fatalf("migrate disposable PostgreSQL database: %v", err)
	}
	db, err := database.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_project_archive_quiescence_pg_"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	capabilityService := capabilities.NewService(db)
	providerKey := "pg-quiescence-" + suffix
	provider, _, err := capabilityService.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
		ProviderKey: providerKey, CompactAddress: "main@" + providerKey, DisplayName: "PostgreSQL routed quiescence test",
		ProviderType: capabilities.ProviderTypeSystem, NodeRef: req.OriginNodeID, ScopeRef: req.ScopeID,
		Version: "1.0.0", Status: capabilities.ProviderStatusActive, RuntimeProfileJSON: json.RawMessage(`{}`),
		DocumentationRefsJSON: json.RawMessage(`[]`), Metadata: json.RawMessage(`{"source":"project_archive_quiescence_postgres_test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	class, _, err := capabilityService.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{
		Namespace: "system", Name: "project_archive_quiesce_" + suffix, Version: "1.0.0", DisplayName: "Project archive quiescence PostgreSQL test",
		Form: capabilities.CapabilityFormCommand, InputSchemaJSON: json.RawMessage(`{"type":"object"}`), OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		DefaultRiskLevel: capabilities.RiskLevelLow, DefaultPolicyRequirementsJSON: json.RawMessage(`{}`), Status: capabilities.CapabilityClassStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _, err := capabilityService.EnsureCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{
		ProviderRef: provider.ProviderID, CapabilityClassRef: class.CapabilityClassID, EndpointName: "quiesce", CompactAddress: provider.CompactAddress + ".quiesce",
		Form: capabilities.CapabilityFormCommand, InputSchemaJSON: json.RawMessage(`{"type":"object"}`), OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		RiskLevel: capabilities.RiskLevelLow, ExecutionAuthorizationLevel: 1, SideEffectsJSON: json.RawMessage(`{}`), PolicyRequirementsJSON: json.RawMessage(`{}`),
		CredentialRequirementsJSON: json.RawMessage(`{}`), ApprovalRequirementsJSON: json.RawMessage(`{}`), JobBehaviorJSON: json.RawMessage(`{}`),
		SessionBehaviorJSON: json.RawMessage(`{}`), StreamBehaviorJSON: json.RawMessage(`{}`), LeaseBehaviorJSON: json.RawMessage(`{}`), Status: capabilities.EndpointStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: req.OriginNodeID, Reason: "disposable routed quiescence PostgreSQL test"})
	if err != nil {
		t.Fatal(err)
	}

	request := ProjectRuntimeQuiescenceRequest{
		ProjectID: "project_pg_" + suffix, ProjectSlug: "pg-" + suffix, OperationID: "workspace_archive_operation_pg_" + suffix,
		PlanDigest: "sha256:" + strings.Repeat("a", 64), NodeKey: req.OriginNodeKey,
		Targets: []ProjectRuntimeQuiescenceTarget{{
			Facet: projectquiescence.FacetServices, Kind: projectquiescence.TargetKindService, OwnerNode: req.OriginNodeKey,
			ProviderKey: provider.ProviderKey, ProviderAddress: provider.CompactAddress, ProviderID: provider.ProviderID,
			RuntimeProfileDigest: "sha256:" + strings.Repeat("b", 64), AllowlistKey: provider.ProviderKey, Manager: "systemd", Unit: "loom-pg-quiescence.service",
		}},
	}
	if err := projectquiescence.SealRequest(&request); err != nil {
		t.Fatal(err)
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	routingService := routing.NewService(db)
	now := time.Now().UTC().Add(-time.Minute)

	for _, test := range []struct {
		name    string
		result  func(ProjectRuntimeQuiescenceReceipt) json.RawMessage
		wantErr bool
	}{
		{name: "valid JSONB-normalized receipt", result: func(receipt ProjectRuntimeQuiescenceReceipt) json.RawMessage {
			raw, _ := json.Marshal(receipt)
			return raw
		}},
		{name: "tampered receipt", wantErr: true, result: func(receipt ProjectRuntimeQuiescenceReceipt) json.RawMessage {
			receipt.PlanDigest = "sha256:" + strings.Repeat("c", 64)
			raw, _ := json.Marshal(receipt)
			return raw
		}},
		{name: "oversized receipt", wantErr: true, result: func(ProjectRuntimeQuiescenceReceipt) json.RawMessage {
			return json.RawMessage(`{"padding":"` + strings.Repeat("x", projectquiescence.MaxReceiptBytes) + `"}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			attemptID := "attempt-pg-" + ids.NewIdempotencyID()
			idempotencyKey := "project.archive.quiescence." + attemptID + "." + request.NodeKey
			metadata, err := json.Marshal(map[string]any{
				"source": "project.archive.quiescence", "attempt_id": attemptID,
				"project_id": request.ProjectID, "project_slug": request.ProjectSlug,
				"project_scope_id": req.ScopeID, "project_scope_key": req.ScopeKey,
				"operation_id": request.OperationID, "plan_digest": request.PlanDigest,
				"node_key": request.NodeKey, "request_digest": request.RequestDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			route, call := insertProjectArchivePostgresRoutedCall(t, ctx, db, req, provider, class, endpoint, requestJSON, metadata, idempotencyKey)

			receipt := ProjectRuntimeQuiescenceReceipt{Evidence: []ProjectRuntimeQuiescenceEvidence{{
				ProjectRuntimeQuiescenceTarget: request.Targets[0], State: projectquiescence.TargetStateStopped, FenceState: projectquiescence.FenceStateActive,
				TargetReceiptID: "target-pg-" + suffix, ObservedAt: now,
			}}}
			if err := projectquiescence.SealReceipt(request, &receipt); err != nil {
				t.Fatal(err)
			}
			canonicalReceipt, _ := json.Marshal(receipt)
			resultJSON := test.result(receipt)
			if _, err := routingService.IngestRemoteResult(ctx, req, routing.RemoteResultInput{
				NodeRef: req.OriginNodeID, CredentialToken: credential.CredentialToken, IdempotencyKey: "result-" + ids.NewIdempotencyID(),
				Payload: routing.RemoteResultPayload{
					RouteID: route.RouteID, CapabilityCallID: call.CapabilityCallID, NodeID: req.OriginNodeID,
					ProviderID: provider.ProviderID, CapabilityEndpointID: endpoint.CapabilityEndpointID,
					ExecutionStatus: routing.CapabilityCallStatusCompleted, ResultJSON: resultJSON, ResultRefsJSON: json.RawMessage(`{}`),
					CompletedAt: &now, RuntimeMetadataJSON: json.RawMessage(`{}`),
				}, Metadata: json.RawMessage(`{"source":"project_archive_quiescence_postgres_test"}`),
			}); err != nil {
				t.Fatal(err)
			}
			persisted, err := routingService.GetCapabilityCall(ctx, call.CapabilityCallID)
			if err != nil {
				t.Fatal(err)
			}
			if !test.wantErr && bytes.Equal(persisted.ResultJSON, canonicalReceipt) {
				t.Fatalf("PostgreSQL result unexpectedly retained canonical input bytes: %s", persisted.ResultJSON)
			}
			verifier := &RoutedProjectRuntimeQuiescenceVerifier{Routing: routingService, Now: func() time.Time { return now.Add(time.Minute) }}
			_, err = verifier.waitForNodeReceipt(ctx, req, endpoint.CompactAddress, request, metadata, idempotencyKey, route, call)
			if test.wantErr && err == nil {
				t.Fatal("invalid persisted receipt was accepted")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("valid persisted receipt was refused: %v", err)
			}
		})
	}
}

func insertProjectArchivePostgresRoutedCall(t *testing.T, ctx context.Context, db *sql.DB, req requestctx.Context, provider capabilities.Provider, class capabilities.CapabilityClass, endpoint capabilities.CapabilityEndpoint, requestJSON, metadata json.RawMessage, idempotencyKey string) (routing.Route, routing.CapabilityCall) {
	t.Helper()
	routeID := ids.NewRouteID()
	callID := ids.NewCapabilityCallID()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO routing.routes (
			route_id, route_key, correlation_id, actor_id, origin_node_id, origin_scope_id, runtime_node_id,
			target_node_id, provider_id, capability_endpoint_id, capability_class_id, route_kind, execution_mode,
			selected_path_json, request_summary_json, result_target_json, status, dispatched_at, metadata
		) VALUES ($1,$1,$2,$3,$4,$5,$4,$4,$6,$7,$8,'remote','immediate','{}','{}','{}','dispatched',now(),$9)
	`, routeID, req.CorrelationID, req.ActorID, req.OriginNodeID, req.ScopeID, provider.ProviderID, endpoint.CapabilityEndpointID, class.CapabilityClassID, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO routing.capability_calls (
			capability_call_id, capability_call_key, route_id, correlation_id, idempotency_key, actor_id, origin_node_id,
			scope_id, target_node_id, provider_id, capability_endpoint_id, operation, execution_mode, status,
			input_json, input_summary_json, result_json, result_refs_json, metadata
		) VALUES ($1,$1,$2,$3,$4,$5,$6,$7,$6,$8,$9,$10,'immediate','dispatched',$11,'{}','{}','{}',$12)
	`, callID, routeID, req.CorrelationID, idempotencyKey, req.ActorID, req.OriginNodeID, req.ScopeID, provider.ProviderID, endpoint.CapabilityEndpointID, "capability:"+endpoint.CompactAddress, requestJSON, metadata); err != nil {
		t.Fatal(err)
	}
	routingService := routing.NewService(db)
	route, err := routingService.GetRoute(ctx, routeID)
	if err != nil {
		t.Fatal(err)
	}
	call, err := routingService.GetCapabilityCall(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	return route, call
}
