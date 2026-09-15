package nodeagent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

// This gate owns a real disposable PostgreSQL database supplied by the smoke
// runner. It proves authenticated transport and exact lost-ack replay, not a
// Linux process: its deliberately unavailable helper yields a truthful failure.
func TestApplicationPersistedDispatchOutboxLostAckIntegration(t *testing.T) {
	url := os.Getenv("LOOM_SERVICE_REGISTRY_TEST_DB_URL")
	if url == "" {
		t.Skip("owned disposable PostgreSQL URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, e := migrations.Up(ctx, url, applicationReportMigrationsDir(t)); e != nil {
		t.Fatal(e)
	}
	sqlDB, e := db.OpenSQL(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	if _, e = bootstrap.NewService(sqlDB).EnsureDevBootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	req, e := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_application_e2")
	if e != nil {
		t.Fatal(e)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	nodeKey := "application-" + suffix
	nodeID := ids.NewNodeID()
	if _, e = sqlDB.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status,owner_actor_id,metadata) VALUES ($1,$2,'Application fixture','workspace','worker','workspace-full','active',$3,'{}')`, nodeID, nodeKey, req.ActorID); e != nil {
		t.Fatal(e)
	}
	if _, e = sqlDB.ExecContext(ctx, `INSERT INTO identity.actor_node_authorizations (authorization_id,actor_id,node_id,authorization_level,status,metadata) VALUES ($1,$2,$3,5,'active','{}')`, "auth_application_"+suffix, req.ActorID, nodeID); e != nil {
		t.Fatal(e)
	}
	cs := capabilities.NewService(sqlDB)
	base := "workspace/" + nodeKey + "@system"
	provider, e := cs.RegisterProvider(ctx, req, capabilities.RegisterProviderInput{ProviderKey: "system", CompactAddress: base, DisplayName: "Application fixture system", ProviderType: capabilities.ProviderTypeSystem, NodeRef: nodeID, ScopeRef: req.ScopeID, Status: capabilities.ProviderStatusActive})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = cs.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{HealthStatus: capabilities.HealthStatusOK, AvailabilityStatus: capabilities.AvailabilityStatusAvailable}); e != nil {
		t.Fatal(e)
	}
	for _, c := range applicationCapabilities(base) {
		class, _, e := cs.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{Namespace: c.ClassNamespace, Name: c.ClassName, DisplayName: c.DisplayName, Form: c.Form, DefaultRiskLevel: c.RiskLevel, InputSchemaJSON: c.InputSchemaJSON, OutputSchemaJSON: c.OutputSchemaJSON, Status: capabilities.CapabilityClassStatusActive})
		if e != nil {
			t.Fatal(e)
		}
		endpoint, e := cs.RegisterCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{ProviderRef: provider.ProviderID, CapabilityClassRef: class.CapabilityClassID, EndpointName: c.EndpointName, CompactAddress: c.CompactAddress, Form: c.Form, InputSchemaJSON: c.InputSchemaJSON, OutputSchemaJSON: c.OutputSchemaJSON, RiskLevel: c.RiskLevel, ExecutionAuthorizationLevel: c.ExecutionAuthorizationLevel, Status: capabilities.EndpointStatusActive})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = cs.RegisterEndpointVersion(ctx, req, capabilities.RegisterEndpointVersionInput{CapabilityEndpointRef: endpoint.CapabilityEndpointID, VersionLabel: "fixture-e2", ManifestJSON: c.ManifestJSON, InputSchemaJSON: c.InputSchemaJSON, OutputSchemaJSON: c.OutputSchemaJSON, RiskLevel: c.RiskLevel, ExecutionAuthorizationLevel: c.ExecutionAuthorizationLevel, Status: capabilities.EndpointVersionStatusActive}); e != nil {
			t.Fatal(e)
		}
	}
	credential, e := nodes.NewService(sqlDB).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: nodeID, Reason: "owned application fixture"})
	if e != nil {
		t.Fatal(e)
	}
	state := State{NodeID: nodeID, CredentialToken: credential.CredentialToken}
	config := Config{NodeKey: nodeKey, ServiceManager: ServiceManagerConfig{ApplicationSocketPath: t.TempDir() + "/missing-helper.sock"}}
	rs := routing.NewService(sqlDB)
	for _, origin := range []string{nodeID, "main"} {
		for _, op := range []string{"inspect", "apply", "retire"} {
			t.Run(origin+"_"+op, func(t *testing.T) {
				q := serviceregistry.ApplicationRuntimeRequest{SchemaVersion: serviceregistry.ApplicationRuntimeSchema, Operation: op, Owner: serviceregistry.ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: nodeID, Resource: "fixture"}, OperationToken: "operation-" + op, ExpectedRevision: "revision-fixture", PolicyRevision: "policy-fixture", LocationRevision: "location-fixture", CredentialRevisions: map[string]string{}, Data: map[string]serviceregistry.ApplicationDataRequest{}}
				raw, _ := json.Marshal(q)
				callInput := routing.CapabilityCallInput{Target: base + ".project.application." + op, OriginNodeRef: origin, ScopeRef: req.ScopeID, Input: raw}
				planned, e := rs.PlanRoute(ctx, req, callInput)
				if e != nil || planned.RouteKind != routing.RouteKindRemote || planned.RuntimeNodeID != nodeID {
					t.Fatalf("owner routing: %+v %v", planned, e)
				}
				outcome, e := rs.Call(ctx, req, callInput, "application-"+suffix+origin+op)
				if e != nil || outcome.Status != routing.CapabilityCallStatusDispatched {
					t.Fatalf("dispatch=%+v err=%v", outcome, e)
				}
				var messageRaw []byte
				var queuedNode string
				if e = sqlDB.QueryRowContext(ctx, `SELECT node_id,payload_json FROM communication.messages WHERE communication_message_id=$1`, outcome.DispatchMessageID).Scan(&queuedNode, &messageRaw); e != nil {
					t.Fatal(e)
				}
				if queuedNode != nodeID {
					t.Fatal("wrong queue node")
				}
				var dispatch routing.RemoteDispatchPayload
				if e = json.Unmarshal(messageRaw, &dispatch); e != nil {
					t.Fatal(e)
				}
				store := Store{DataDir: t.TempDir()}
				runtimeStore := noderuntime.NewStore(store.DataDir)
				message := communication.Message{CommunicationMessageID: outcome.DispatchMessageID, Kind: communication.KindCapabilityDispatch, PayloadJSON: messageRaw}
				processed, e := processPolledMessage(ctx, Client{}, store, runtimeStore, config, state, "application-correlation", message, false)
				if e != nil {
					t.Fatal(e)
				}
				frozen, e := runtimeStore.ReadApplicationResult(noderuntime.ApplicationResultKey(dispatch.CapabilityCallID))
				if e != nil {
					t.Fatal(e)
				}
				if processed.RuntimeCapabilityResultOutboxID != frozen.Outbox.LocalOutboxID || processed.RuntimeAckOutboxID == "" {
					t.Fatal("completed ACK lacked durable exact result")
				}
				if strings.Contains(string(frozen.Envelope), credential.CredentialToken) {
					t.Fatal("node credential persisted")
				}
				var result routing.RemoteResultInput
				if e = json.Unmarshal(frozen.Envelope, &result); e != nil {
					t.Fatal(e)
				}
				result.CredentialToken = credential.CredentialToken
				if result.Payload.ExecutionStatus != routing.CapabilityCallStatusFailed || result.Payload.ResultJSON == nil {
					t.Fatal("missing helper claimed applied")
				}
				// Commit, then discard the reply as if the result response was lost.
				if _, e = rs.IngestRemoteResult(ctx, req, result); e != nil {
					t.Fatal(e)
				}
				retry, e := buildCapabilityResultInput(ctx, config, state, store, message)
				if e != nil {
					t.Fatal(e)
				}
				original, _ := json.Marshal(result.Payload)
				replayed, _ := json.Marshal(retry.Payload)
				if string(original) != string(replayed) {
					t.Fatal("lost ACK changed timestamps/result payload")
				}
				again, e := rs.IngestRemoteResult(ctx, req, retry)
				if e != nil || again.Status != routing.CapabilityCallStatusFailed {
					t.Fatalf("exact result replay=%+v err=%v", again, e)
				}
				var count int
				if e = sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM communication.messages WHERE capability_call_id=$1 AND direction='node_to_main'`, dispatch.CapabilityCallID).Scan(&count); e != nil || count != 1 {
					t.Fatalf("result duplicated: %d %v", count, e)
				}
				ack := buildAckInput(state, message)
				if _, e = communication.NewService(sqlDB).Ack(ctx, req, ack); e != nil {
					t.Fatal(e)
				}
				if _, e = communication.NewService(sqlDB).Ack(ctx, req, ack); e != nil {
					t.Fatal("dispatch lost-ACK replay", e)
				}
			})
		}
	}
}

// Receiver-only acceptance runs on Darwin with real PostgreSQL and synthetic
// authenticated node responses. It does not claim helper/socket execution.
func TestApplicationPrerequisitesRecordedReceiverIntegration(t *testing.T) {
	f := newApplicationReportFixture(t)
	for _, origin := range []string{f.state.NodeID, "main"} {
		for _, multiple := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_bindings_%v", origin, multiple), func(t *testing.T) {
				q, snapshot := applicationReportSample(f.state.NodeID, multiple)
				call, message, dispatch := f.dispatch(t, origin, "prerequisites", mustApplicationReportJSON(t, q))
				if _, _, err := (serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}).Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", ""); err == nil {
					t.Fatal("queued report accepted")
				}
				result := f.result(dispatch, mustApplicationReportJSON(t, snapshot))
				for name, mutate := range map[string]func(*routing.RemoteResultInput){
					"wrong_node":       func(x *routing.RemoteResultInput) { x.Payload.NodeID = "other" },
					"missing_node":     func(x *routing.RemoteResultInput) { x.Payload.NodeID = "" },
					"missing_provider": func(x *routing.RemoteResultInput) { x.Payload.ProviderID = "" },
					"missing_endpoint": func(x *routing.RemoteResultInput) { x.Payload.CapabilityEndpointID = "" },
					"wrong_operation":  func(x *routing.RemoteResultInput) { x.Payload.Operation = "other" },
					"wrong_address": func(x *routing.RemoteResultInput) {
						x.Payload.CapabilityAddress = "workspace/other@system.project.application.prerequisites"
					},
					"missing_provider_address": func(x *routing.RemoteResultInput) { x.Payload.ProviderAddress = "" },
					"wrong_credential":         func(x *routing.RemoteResultInput) { x.CredentialToken = "unissued" },
				} {
					t.Run(name, func(t *testing.T) {
						before := f.durableState(t)
						x := result
						mutate(&x)
						if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, x); err == nil {
							t.Fatal("invalid association accepted")
						}
						if after := f.durableState(t); before != after {
							t.Fatal("rejected result wrote durable state")
						}
					})
				}
				accepted, err := f.routing.IngestRemoteResult(t.Context(), f.req, result)
				if err != nil {
					t.Fatal(err)
				}
				first := f.durableState(t)
				if _, err = f.routing.IngestRemoteResult(t.Context(), f.req, result); err != nil {
					t.Fatal(err)
				}
				if first != f.durableState(t) {
					t.Fatal("same-key replay changed committed state")
				}
				reader := serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}
				pureBefore := f.pureReadState(t)
				for i := 0; i < 5; i++ {
					got, evidence, err := reader.Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, snapshot.RepositoryID, snapshot.IdentityRevision)
					if err != nil {
						t.Fatal(err)
					}
					if string(mustApplicationReportJSON(t, got)) != string(mustApplicationReportJSON(t, snapshot)) || evidence.DispatchMessageID != message.CommunicationMessageID || evidence.ResultMessageID != accepted.MessageID || evidence.EndpointVersionID == "" || evidence.ReceivedAt.IsZero() {
						t.Fatal("recorded report/evidence changed")
					}
				}
				if pureBefore != f.pureReadState(t) {
					t.Fatal("pure projection wrote state")
				}
				for name, change := range map[string]func(*serviceregistry.ApplicationPrerequisiteQuery, *string, *string){
					"owner": func(q *serviceregistry.ApplicationPrerequisiteQuery, r, i *string) { q.Owner.Resource = "other" },
					"repository": func(q *serviceregistry.ApplicationPrerequisiteQuery, r, i *string) {
						*r = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAX"
					},
					"stale": func(q *serviceregistry.ApplicationPrerequisiteQuery, r, i *string) {
						*i = "sha256:" + strings.Repeat("9", 64)
					},
				} {
					t.Run(name, func(t *testing.T) {
						v := q
						r := snapshot.RepositoryID
						i := snapshot.IdentityRevision
						change(&v, &r, &i)
						if _, _, err := reader.Read(t.Context(), call.CapabilityCall.CapabilityCallID, v, r, i); err == nil {
							t.Fatal("wrong report basis accepted")
						}
					})
				}
				for _, key := range []string{result.IdempotencyKey, result.IdempotencyKey + ".different"} {
					before := f.durableState(t)
					conflict := result
					conflict.IdempotencyKey = key
					changed := snapshot
					changed.IdentityRevision = "sha256:" + strings.Repeat("3", 64)
					conflict.Payload.ResultJSON = mustApplicationReportJSON(t, changed)
					if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, conflict); err == nil {
						t.Fatal("conflicting report accepted")
					}
					if before != f.durableState(t) {
						t.Fatal("conflict did not roll back message/event/call/route")
					}
				}
				var calls, dispatches, results int
				if err := f.db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM routing.capability_calls WHERE capability_call_id=$1),(SELECT count(*) FROM communication.messages WHERE capability_call_id=$1 AND direction='main_to_node'),(SELECT count(*) FROM communication.messages WHERE capability_call_id=$1 AND direction='node_to_main')`, dispatch.CapabilityCallID).Scan(&calls, &dispatches, &results); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || dispatches != 1 || results != 1 {
					t.Fatalf("counts=%d/%d/%d", calls, dispatches, results)
				}
				t.Logf("calls=%d dispatches=%d results=%d helper_calls=0 application_effects=0 pure_reads=5", calls, dispatches, results)
			})
		}
	}
	t.Run("incomplete_conflicting_recorded_evidence", func(t *testing.T) {
		q, snapshot := applicationReportSample(f.state.NodeID, false)
		call, _, d := f.dispatch(t, "main", "prerequisites", mustApplicationReportJSON(t, q))
		result := f.result(d, mustApplicationReportJSON(t, snapshot))
		accepted, err := f.routing.IngestRemoteResult(t.Context(), f.req, result)
		if err != nil {
			t.Fatal(err)
		}
		reader := serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}
		for name, edit := range map[string]struct {
			table, col, idcol, id string
			value                 any
		}{
			"missing_message":    {"routing.capability_calls", "result_refs_json", "capability_call_id", d.CapabilityCallID, `{}`},
			"incomplete_route":   {"routing.routes", "status", "route_id", d.RouteID, "dispatched"},
			"conflicting_call":   {"routing.capability_calls", "result_json", "capability_call_id", d.CapabilityCallID, `{}`},
			"wrong_message_node": {"communication.messages", "node_id", "communication_message_id", accepted.MessageID, f.req.OriginNodeID},
			"wrong_message_body": {"communication.messages", "result_json", "communication_message_id", accepted.MessageID, `{}`},
		} {
			t.Run(name, func(t *testing.T) {
				var old string
				query := `SELECT ` + edit.col + `::text FROM ` + edit.table + ` WHERE ` + edit.idcol + `=$1`
				if err := f.db.QueryRowContext(t.Context(), query, edit.id).Scan(&old); err != nil {
					t.Fatal(err)
				}
				update := `UPDATE ` + edit.table + ` SET ` + edit.col + `=$1 WHERE ` + edit.idcol + `=$2`
				if _, err := f.db.ExecContext(t.Context(), update, edit.value, edit.id); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := f.db.ExecContext(t.Context(), update, old, edit.id); err != nil {
						t.Error(err)
					}
				}()
				if _, _, err := reader.Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", ""); err == nil {
					t.Fatal("unassociated facts accepted")
				}
			})
		}
		if _, _, err := reader.Read(t.Context(), "missing-call", q, "", ""); err == nil {
			t.Fatal("missing call accepted")
		}
		if _, _, err := reader.Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", ""); err != nil {
			t.Fatal("restored report invalid", err)
		}
	})
	t.Run("query_owner_node_must_match_transport", func(t *testing.T) {
		q, snapshot := applicationReportSample("node_01ARZ3NDEKTSV4RRFFQ69G5FAV", false)
		call, _, d := f.dispatch(t, "main", "prerequisites", mustApplicationReportJSON(t, q))
		result := f.result(d, mustApplicationReportJSON(t, snapshot))
		if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, result); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}).Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", ""); err == nil {
			t.Fatal("node authenticated another owner's facts")
		}
	})

	t.Run("invalid_typed_reports_remain_unavailable", func(t *testing.T) {
		q, snapshot := applicationReportSample(f.state.NodeID, false)
		for name, body := range map[string]json.RawMessage{"missing": json.RawMessage(`{}`), "unknown": json.RawMessage(strings.Replace(string(mustApplicationReportJSON(t, snapshot)), `"data":`, `"private_path":"/secret","data":`, 1)), "wrong_owner": json.RawMessage(strings.Replace(string(mustApplicationReportJSON(t, snapshot)), `"resource":"app"`, `"resource":"other"`, 1))} {
			t.Run(name, func(t *testing.T) {
				call, _, d := f.dispatch(t, "main", "prerequisites", mustApplicationReportJSON(t, q))
				r := f.result(d, body)
				if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, r); err != nil {
					t.Fatal("transport result rejected", err)
				}
				if _, _, err := (serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}).Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", ""); err == nil {
					t.Fatal("malformed typed facts accepted")
				}
			})
		}
		call, _, d := f.dispatch(t, "main", "prerequisites", mustApplicationReportJSON(t, q))
		r := f.result(d, json.RawMessage(`{}`))
		r.Payload.ExecutionStatus = routing.CapabilityCallStatusFailed
		r.Payload.ErrorCode = "application.prerequisites.uncertain"
		if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, r); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}).Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", ""); err == nil {
			t.Fatal("failed report became facts")
		}
	})
}

// Exercise the actual terminal-result transaction with generic JSON too, so
// numeric semantics are not accidentally narrowed to a prerequisite fixture.
func TestApplicationPrerequisitesTerminalJSONIntegration(t *testing.T) {
	f := newApplicationReportFixture(t)
	first := json.RawMessage(`{"nested":[null,true,{"u":18446744073709551615,"i":-9223372036854775808,"n":9007199254740992,"d":1.25}]}`)
	equivalent := json.RawMessage(` { "nested": [null,true,{"d":125e-2,"n":900719925474099200e-2,"i":-92233720368547758080e-1,"u":184467440737095516150e-1}] } `)
	_, _, d := f.dispatch(t, "main", "inspect", json.RawMessage(`{}`))
	r := f.result(d, first)
	if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, r); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]json.RawMessage{
		"adjacent_2pow53": json.RawMessage(strings.Replace(string(first), "9007199254740992", "9007199254740993", 1)),
		"uint64_max":      json.RawMessage(strings.Replace(string(first), "18446744073709551615", "18446744073709551614", 1)),
		"int64_min":       json.RawMessage(strings.Replace(string(first), "-9223372036854775808", "-9223372036854775807", 1)),
		"nested_null":     json.RawMessage(strings.Replace(string(first), "null", "false", 1)),
		"array_order":     json.RawMessage(strings.Replace(string(first), "null,true", "true,null", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			for keyName, key := range map[string]string{"same_key": r.IdempotencyKey, "different_key": r.IdempotencyKey + "." + name} {
				t.Run(keyName, func(t *testing.T) {
					before := f.durableState(t)
					x := r
					x.IdempotencyKey = key
					x.Payload.ResultJSON = payload
					if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, x); err == nil {
						t.Fatal("conflicting exact terminal value accepted")
					}
					if before != f.durableState(t) {
						t.Fatal("conflict left message/event/terminal mutations")
					}
				})
			}
		})
	}
	r.IdempotencyKey += ".equivalent"
	r.Payload.ResultJSON = equivalent
	outcome, err := f.routing.IngestRemoteResult(t.Context(), f.req, r)
	if err != nil || !outcome.Idempotent {
		t.Fatalf("equivalent JSONB replay: %v %+v", err, outcome)
	}
	var messages int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM communication.messages WHERE capability_call_id=$1 AND direction='node_to_main'`, d.CapabilityCallID).Scan(&messages); err != nil || messages != 2 {
		t.Fatalf("messages=%d err=%v", messages, err)
	}
	if !t.Failed() {
		t.Log("calls=1 dispatches=1 accepted_results=2 conflicting_replays=10 rollback_verified=true helper_calls=0 application_effects=0")
	}
}

type applicationReportFixture struct {
	db      *sql.DB
	req     requestctx.Context
	state   State
	routing routing.Service
	base    string
}

func newApplicationReportFixture(t *testing.T) applicationReportFixture {
	t.Helper()
	url := os.Getenv("LOOM_SERVICE_REGISTRY_TEST_DB_URL")
	if url == "" {
		t.Skip("owned disposable PostgreSQL URL required")
	}
	dir := applicationReportMigrationsDir(t)
	if _, err := migrations.Up(t.Context(), url, dir); err != nil {
		t.Fatal(err)
	}
	database, err := db.OpenSQL(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := bootstrap.NewService(database).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), database, "corr_application_e3b")
	if err != nil {
		t.Fatal(err)
	}
	key := "application-report-" + fmt.Sprint(time.Now().UnixNano())
	nodeID := ids.NewNodeID()
	if _, err := database.ExecContext(t.Context(), `INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status,owner_actor_id,metadata) VALUES ($1,$2,'Report fixture','workspace','worker','workspace-full','active',$3,'{}')`, nodeID, key, req.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `INSERT INTO identity.actor_node_authorizations (authorization_id,actor_id,node_id,authorization_level,status,metadata) VALUES ($1,$2,$3,5,'active','{}')`, "auth_report_"+key, req.ActorID, nodeID); err != nil {
		t.Fatal(err)
	}
	cs := capabilities.NewService(database)
	base := "workspace/" + key + "@system"
	provider, err := cs.RegisterProvider(t.Context(), req, capabilities.RegisterProviderInput{ProviderKey: "system", CompactAddress: base, DisplayName: "Report fixture", ProviderType: capabilities.ProviderTypeSystem, NodeRef: nodeID, ScopeRef: req.ScopeID, Status: capabilities.ProviderStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.UpsertProviderHealth(t.Context(), req, provider.ProviderID, capabilities.ProviderHealthInput{HealthStatus: capabilities.HealthStatusOK, AvailabilityStatus: capabilities.AvailabilityStatusAvailable}); err != nil {
		t.Fatal(err)
	}
	for _, c := range applicationCapabilities(base) {
		class, _, err := cs.EnsureCapabilityClass(t.Context(), req, capabilities.RegisterCapabilityClassInput{Namespace: c.ClassNamespace, Name: c.ClassName, DisplayName: c.DisplayName, Form: c.Form, DefaultRiskLevel: c.RiskLevel, InputSchemaJSON: c.InputSchemaJSON, OutputSchemaJSON: c.OutputSchemaJSON, Status: capabilities.CapabilityClassStatusActive})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := cs.RegisterCapabilityEndpoint(t.Context(), req, capabilities.RegisterCapabilityEndpointInput{ProviderRef: provider.ProviderID, CapabilityClassRef: class.CapabilityClassID, EndpointName: c.EndpointName, CompactAddress: c.CompactAddress, Form: c.Form, InputSchemaJSON: c.InputSchemaJSON, OutputSchemaJSON: c.OutputSchemaJSON, RiskLevel: c.RiskLevel, ExecutionAuthorizationLevel: c.ExecutionAuthorizationLevel, Status: capabilities.EndpointStatusActive})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cs.RegisterEndpointVersion(t.Context(), req, capabilities.RegisterEndpointVersionInput{CapabilityEndpointRef: endpoint.CapabilityEndpointID, VersionLabel: "fixture-e3b", ManifestJSON: c.ManifestJSON, InputSchemaJSON: c.InputSchemaJSON, OutputSchemaJSON: c.OutputSchemaJSON, RiskLevel: c.RiskLevel, ExecutionAuthorizationLevel: c.ExecutionAuthorizationLevel, Status: capabilities.EndpointVersionStatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	credential, err := nodes.NewService(database).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: nodeID, Reason: "owned report fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return applicationReportFixture{db: database, req: req, state: State{NodeID: nodeID, CredentialToken: credential.CredentialToken}, routing: routing.NewService(database), base: base}
}
func (f applicationReportFixture) dispatch(t *testing.T, origin, op string, input json.RawMessage) (routing.CapabilityCallOutcome, communication.Message, routing.RemoteDispatchPayload) {
	t.Helper()
	call, err := f.routing.Call(t.Context(), f.req, routing.CapabilityCallInput{Target: f.base + ".project.application." + op, OriginNodeRef: origin, ScopeRef: f.req.ScopeID, Input: input}, "report-"+fmt.Sprint(time.Now().UnixNano()))
	if err != nil || call.Status != routing.CapabilityCallStatusDispatched || call.Route.TargetNodeID != f.state.NodeID || call.Route.RouteKind != routing.RouteKindRemote {
		t.Fatalf("actual owner dispatch: %v %+v", err, call)
	}
	polled, err := communication.NewService(f.db).Poll(t.Context(), f.req, communication.PollInput{NodeRef: f.state.NodeID, CredentialToken: f.state.CredentialToken, MaxMessages: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range polled.Messages {
		if m.CommunicationMessageID == call.DispatchMessageID {
			var d routing.RemoteDispatchPayload
			if json.Unmarshal(m.PayloadJSON, &d) != nil {
				t.Fatal("dispatch JSON")
			}
			return call, m, d
		}
	}
	t.Fatal("dispatch not delivered by communication owner")
	return call, communication.Message{}, routing.RemoteDispatchPayload{}
}
func (f applicationReportFixture) result(d routing.RemoteDispatchPayload, body json.RawMessage) routing.RemoteResultInput {
	now := time.Now().UTC()
	return routing.RemoteResultInput{NodeRef: f.state.NodeID, CredentialToken: f.state.CredentialToken, IdempotencyKey: "capability.result." + f.state.NodeID + "." + d.CapabilityCallID, Payload: routing.RemoteResultPayload{RouteID: d.RouteID, CapabilityCallID: d.CapabilityCallID, NodeID: f.state.NodeID, ProviderID: d.ProviderID, ProviderAddress: d.ProviderAddress, CapabilityEndpointID: d.CapabilityEndpointID, CapabilityAddress: d.CapabilityAddress, Operation: d.Operation, ExecutionStatus: routing.CapabilityCallStatusCompleted, ResultJSON: body, StartedAt: &now, CompletedAt: &now}}
}
func (f applicationReportFixture) durableState(t *testing.T) string {
	t.Helper()
	out := ""
	for _, table := range []string{"routing.routes", "routing.capability_calls", "communication.messages", "communication.message_acks", "events.events", "policy.decisions", "policy.grants"} {
		var raw string
		err := f.db.QueryRowContext(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text,'[]') FROM `+table+` t`).Scan(&raw)
		if err != nil {
			t.Fatal(table, err)
		}
		out += table + raw
	}
	return out
}
func mustApplicationReportJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func applicationReportSample(node string, multiple bool) (serviceregistry.ApplicationPrerequisiteQuery, serviceregistry.ApplicationPrerequisiteSnapshot) {
	q := serviceregistry.ApplicationPrerequisiteQuery{SchemaVersion: serviceregistry.ApplicationPrerequisiteSchema, Owner: serviceregistry.ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: node, Resource: "app"}}
	s := serviceregistry.ApplicationPrerequisiteSnapshot{SchemaVersion: q.SchemaVersion, Owner: q.Owner, IdentityRevision: "sha256:" + strings.Repeat("1", 64), CollectedAt: time.Date(2026, 9, 13, 12, 0, 0, 123456789, time.UTC), PolicyRevision: "policy-1", LocationRevision: "location-1", RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", Platform: "x86_64-linux", Data: map[string]serviceregistry.ApplicationPrerequisiteData{}, Credentials: map[string]serviceregistry.ApplicationPrerequisiteCredential{}, Observation: &serviceregistry.ApplicationPrerequisiteObservation{ObservedAt: time.Date(2026, 9, 12, 1, 0, 0, 0, time.UTC), State: "observed", ProcessState: "inactive", Readiness: serviceregistry.ApplicationReadiness{Process: "stopped", Protocol: "pending", Ingress: "not_applicable", Client: "unknown", Protection: "pending"}, ManagementPresent: true}}
	if multiple {
		s.Data["files"] = serviceregistry.ApplicationPrerequisiteData{Availability: "available", Custody: "matches", PoolIdentity: "sha256:" + strings.Repeat("2", 64), Identity: &serviceregistry.ApplicationPrerequisiteDataIdentity{Inode: 9007199254740993, PoolInode: 18446744073709551615, UID: 1234, GID: 1234, Mode: 0700}}
		s.Data["cache"] = serviceregistry.ApplicationPrerequisiteData{Availability: "missing", Custody: "unknown"}
		s.Credentials["app.first"] = serviceregistry.ApplicationPrerequisiteCredential{Revision: "credential-1", Availability: "available"}
		s.Credentials["app.second"] = serviceregistry.ApplicationPrerequisiteCredential{Revision: "credential-2", Availability: "missing"}
	}
	return q, s
}

// Integrator Linux gate: a fixture-owned server returns synthetic E3a facts
// through the actual unmodified root-custody ApplicationHelperClient. This proves
// transport/outbox composition, not root metadata collection (E3a proves that).
func TestApplicationPrerequisitesRecordedReportIntegration(t *testing.T) {
	root := os.Getenv("LOOM_APPLICATION_REPORT_FIXTURE")
	if runtime.GOOS != "linux" || root == "" {
		t.Skip("integrator-owned Linux root socket fixture required")
	}
	if os.Geteuid() != 0 || !strings.HasPrefix(root, "/run/loom-application-acceptance-") || filepath.Clean(root) != root {
		t.Fatal("private root-owned /run fixture required")
	}
	socketRoot, err := os.MkdirTemp(root, "e3b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socket := filepath.Join(socketRoot, "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	var helperCalls atomic.Int64
	done := make(chan struct{})
	serverErrors := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var envelope serviceregistry.ApplicationHelperEnvelope
				decoder := json.NewDecoder(io.LimitReader(conn, 1024*1024))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&envelope); err != nil {
					select {
					case serverErrors <- err:
					default:
					}
					return
				}
				if decoder.Decode(new(any)) != io.EOF || envelope.Prerequisites == nil || envelope.Request != nil || envelope.Publication != nil || envelope.Archive != nil {
					select {
					case serverErrors <- fmt.Errorf("unexpected helper envelope"):
					default:
					}
					return
				}
				helperCalls.Add(1)
				_, snapshot := applicationReportSample(envelope.Prerequisites.Owner.NodeID, envelope.Prerequisites.Owner.Resource == "multi")
				snapshot.Owner = envelope.Prerequisites.Owner
				snapshot.CollectedAt = time.Now().UTC()
				if err := json.NewEncoder(conn).Encode(serviceregistry.ApplicationHelperResponse{Prerequisites: &snapshot}); err != nil {
					select {
					case serverErrors <- err:
					default:
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		select {
		case err := <-serverErrors:
			t.Error(err)
		default:
		}
	})
	f := newApplicationReportFixture(t)
	config := Config{NodeKey: strings.TrimSuffix(strings.TrimPrefix(f.base, "workspace/"), "@system"), ServiceManager: ServiceManagerConfig{ApplicationSocketPath: socket}}
	for _, origin := range []string{f.state.NodeID, "main"} {
		for _, resource := range []string{"app", "multi"} {
			t.Run(origin+"_"+resource, func(t *testing.T) {
				q, _ := applicationReportSample(f.state.NodeID, resource == "multi")
				q.Owner.Resource = resource
				call, message, d := f.dispatch(t, origin, "prerequisites", mustApplicationReportJSON(t, q))
				caseRoot, err := os.MkdirTemp(socketRoot, "node-")
				if err != nil {
					t.Fatal(err)
				}
				store := Store{DataDir: caseRoot}
				rs := noderuntime.NewStore(caseRoot)
				beforeCalls := helperCalls.Load()
				processed, err := processPolledMessage(t.Context(), Client{}, store, rs, config, f.state, "e3b", message, false)
				if err != nil {
					t.Fatal(err)
				}
				frozen, err := rs.ReadApplicationResult(noderuntime.ApplicationResultKey(d.CapabilityCallID))
				if err != nil {
					t.Fatal(err)
				}
				if processed.RuntimeCapabilityResultOutboxID != frozen.Outbox.LocalOutboxID || processed.RuntimeAckOutboxID == "" {
					t.Fatal("ACK lacked durable result barrier")
				}
				var result routing.RemoteResultInput
				if json.Unmarshal(frozen.Envelope, &result) != nil {
					t.Fatal("frozen result")
				}
				result.CredentialToken = f.state.CredentialToken
				if result.Payload.ExecutionStatus != routing.CapabilityCallStatusCompleted {
					t.Fatalf("actual helper client did not succeed: %s", result.Payload.ErrorCode)
				}
				if strings.Contains(string(frozen.Envelope), f.state.CredentialToken) {
					t.Fatal("credential persisted")
				}
				// Simulate missing queued result after durable publication, then restart.
				queued, err := queueApplicationResult(rs, result)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(caseRoot, "outbox", noderuntime.OutboxStatusPending, queued.LocalOutboxID+".json")); err != nil {
					t.Fatal(err)
				}
				restarted := noderuntime.NewStore(caseRoot)
				if err := restarted.RestoreApplicationResults(128); err != nil {
					t.Fatal(err)
				}
				retry, err := buildCapabilityResultInput(t.Context(), config, f.state, Store{DataDir: caseRoot}, message)
				if err != nil {
					t.Fatal(err)
				}
				if string(mustApplicationReportJSON(t, result)) != string(mustApplicationReportJSON(t, retry)) {
					t.Fatal("restart recollected/changed frozen envelope")
				}
				if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, result); err != nil {
					t.Fatal(err)
				}
				// Lose the result response, rotate only the synthetic node transport credential.
				credential, err := nodes.NewService(f.db).IssueNodeCredential(t.Context(), f.req, nodes.IssueNodeCredentialInput{NodeRef: f.state.NodeID, Reason: "owned report rotation"})
				if err != nil {
					t.Fatal(err)
				}
				rotated := f.state
				rotated.CredentialToken = credential.CredentialToken
				retry, err = buildCapabilityResultInput(t.Context(), config, rotated, Store{DataDir: caseRoot}, message)
				if err != nil {
					t.Fatal(err)
				}
				if retry.CredentialToken != rotated.CredentialToken || string(mustApplicationReportJSON(t, retry.Payload)) != string(mustApplicationReportJSON(t, result.Payload)) {
					t.Fatal("rotation changed payload")
				}
				if _, err := f.routing.IngestRemoteResult(t.Context(), f.req, retry); err != nil {
					t.Fatal(err)
				}
				ack := buildAckInput(rotated, message)
				for i := 0; i < 2; i++ {
					if _, err := communication.NewService(f.db).Ack(t.Context(), f.req, ack); err != nil {
						t.Fatal(err)
					}
				}
				changed := message
				d.Input = json.RawMessage(`{"changed":true}`)
				changed.PayloadJSON = mustApplicationReportJSON(t, d)
				if _, err := buildCapabilityResultInput(t.Context(), config, rotated, store, changed); err == nil {
					t.Fatal("same call changed dispatch accepted")
				}
				before := f.pureReadState(t)
				nodeBefore := applicationReportTree(t, caseRoot)
				var typed serviceregistry.ApplicationPrerequisiteSnapshot
				for i := 0; i < 5; i++ {
					typed, _, err = (serviceregistry.ApplicationPrerequisiteReportReader{Routing: f.routing}).Read(t.Context(), call.CapabilityCall.CapabilityCallID, q, "", "")
					if err != nil {
						t.Fatal(err)
					}
				}
				if before != f.pureReadState(t) || nodeBefore != applicationReportTree(t, caseRoot) || helperCalls.Load() != beforeCalls+1 {
					t.Fatal("pure read/replay wrote state or recollected")
				}
				if resource == "multi" && (typed.Data["files"].Identity.Inode != 9007199254740993 || typed.Data["files"].Identity.PoolInode != 18446744073709551615 || len(typed.Credentials) != 2) {
					t.Fatal("typed bindings/integers changed")
				}
				var counts [3]int
				if err := f.db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM routing.capability_calls WHERE capability_call_id=$1),(SELECT count(*) FROM communication.messages WHERE capability_call_id=$1 AND direction='main_to_node'),(SELECT count(*) FROM communication.messages WHERE capability_call_id=$1 AND direction='node_to_main')`, d.CapabilityCallID).Scan(&counts[0], &counts[1], &counts[2]); err != nil || counts != [3]int{1, 1, 1} {
					t.Fatalf("counts=%v err=%v", counts, err)
				}
				t.Log("calls=1 dispatches=1 results=1 helper_queries=1 application_effects=0 duplicate_ACK=1 restart=1 credential_rotation=1 pure_reads=5")
			})
		}
	}
}
func applicationReportTree(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&out, "%s:%v:%v", path, info.Mode(), info.ModTime())
		if e.Type().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, "%x", sha256.Sum256(raw))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// Node authentication deliberately updates last_used_at before result ingestion.
// Rollback assertions cover the result transaction; pure reads also freeze that
// separate authentication metadata to prove they never authenticate/refresh.
func (f applicationReportFixture) pureReadState(t *testing.T) string {
	t.Helper()
	var auth string
	if err := f.db.QueryRowContext(t.Context(), `SELECT md5(coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text,'[]')) FROM security.node_auth_credentials t`).Scan(&auth); err != nil {
		t.Fatal(err)
	}
	return f.durableState(t) + auth
}

func applicationReportMigrationsDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("LOOM_APPLICATION_REPORT_MIGRATIONS")
	if dir == "" {
		return serviceRegistryMigrationsDir(t)
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		t.Fatal("absolute fixture migrations required")
	}
	if runtime.GOOS == "linux" && !strings.HasPrefix(dir, "/run/loom-application-acceptance-") {
		t.Fatal("Linux migration fixture must be under owned /run acceptance root")
	}
	return dir
}
