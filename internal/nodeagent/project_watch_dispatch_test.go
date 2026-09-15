package nodeagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

func declarationDispatchDatabase(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires disposable LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && !(u.Hostname() == "" && strings.HasPrefix(u.Query().Get("host"), "/tmp/")) {
		t.Fatal("requires local disposable PostgreSQL")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "decl_dispatch_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	result, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations"))
	if err != nil || result.CurrentVersion != 71 {
		t.Fatalf("migrate through 71: %+v %v", result, err)
	}
	if _, err = bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}
func TestProjectDeclarationMessageDispatchPostgres(t *testing.T) {
	db := declarationDispatchDatabase(t)
	ctx := t.Context()
	req, err := requestctx.ResolveBootstrap(ctx, db, "dispatch-fixture")
	if err != nil {
		t.Fatal(err)
	}
	store, cfg, state := projectWatchNodeFixture(t)
	state.NodeID = req.OriginNodeID
	_, root := projectWatchFixturePayload(t, cfg, state, "dispatch-fixture", 1)
	credential, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: state.NodeID, Reason: "owned declaration mailbox fixture"})
	if err != nil {
		t.Fatal(err)
	}
	state.CredentialToken = credential.CredentialToken
	compose := func() *projectapply.Service {
		resolver := projectapply.NewLocalResolver(db, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: cfg.BoxRootPath}, nil })
		watch := projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
		return projectapply.NewService(db, resolver, projectapply.NewCurrentAuthority(db), map[pc.DeclarationOwner]projectapply.Owner{pc.DeclarationOwnerProjects: projectapply.ProjectsOwner{Resolver: resolver, Projects: projects.NewService(db)}, pc.DeclarationOwnerKnowledge: watch, pc.DeclarationOwnerProtection: watch})
	}
	principal := projectapply.Principal{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID}
	service := compose()
	plan, err := service.Plan(ctx, principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	apply := pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root, PlanID: plan.PlanID, IdempotencyKey: "dispatch-original", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}}
	pending, err := service.Apply(ctx, principal, apply)
	var failure *projectapply.Failure
	if !errors.As(err, &failure) || failure.Cause != "owner_pending" {
		t.Fatalf("pending=%+v %v", pending, err)
	}
	var messageID string
	if err = db.QueryRow(`SELECT message_id FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, pending.OperationID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	communicationService := communication.NewService(db)
	message, err := communicationService.GetMessage(ctx, messageID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err = runtimeStore.Ensure(); err != nil {
		t.Fatal(err)
	}
	processed, err := processPolledMessage(ctx, Client{}, store, runtimeStore, cfg, state, "mailbox", message, false)
	if err != nil || processed.AckStatus != communication.AckStatusCompleted || processed.RuntimeAckOutboxStatus != noderuntime.OutboxStatusPending {
		t.Fatalf("mailbox=%+v %v", processed, err)
	}
	items, err := runtimeStore.ListOutbox([]string{noderuntime.OutboxStatusPending}, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("ACK outbox=%d %v", len(items), err)
	}
	var ack communication.AckInput
	if json.Unmarshal(items[0].PayloadJSON, &ack) != nil {
		t.Fatal("invalid durable ACK")
	}
	actual := assertProjectWatchAck(t, ack)
	if actual.OperationID != pending.OperationID || actual.Evidence.Outcome != communication.ControlOutcomeCompleted || ack.CredentialToken != "[redacted]" {
		t.Fatal("wrong operation ACK or persisted credential")
	}
	// The first response is lost after Main records the authenticated ACK. A new
	// local Store flushes the original outbox item with the exact idempotency key.
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/ack" {
			t.Errorf("wrong ACK route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var input communication.AckInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("ACK decode")
			w.WriteHeader(400)
			return
		}
		if input.CredentialToken != credential.CredentialToken || input.IdempotencyKey != ack.IdempotencyKey || input.CommunicationMessageRef != messageID {
			t.Error("authenticated caller or original token lost")
			w.WriteHeader(400)
			return
		}
		result, err := communicationService.Ack(r.Context(), req, input)
		if err != nil {
			t.Errorf("real ACK failed: %v", err)
			w.WriteHeader(400)
			return
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(503)
			return
		}
		response.WriteJSON(w, 200, response.Success("ack", result))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL)
	lost := flushOutboxItem(ctx, client, store, runtimeStore, state, items[0])
	if lost.Status == noderuntime.OutboxStatusDone {
		t.Fatal("lost response incorrectly marked done")
	}
	restartedRuntime := noderuntime.NewStore(store.DataDir)
	flushed := flushOutboxItem(ctx, client, store, restartedRuntime, state, items[0])
	if flushed.Status != noderuntime.OutboxStatusDone || attempts != 2 {
		t.Fatalf("ACK replay status=%s attempts=%d", flushed.Status, attempts)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM communication.message_acks WHERE communication_message_id=$1`, messageID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ACK replay duplicated rows: %d %v", count, err)
	}
	apply.OperationID = pending.OperationID
	done, err := compose().Apply(ctx, principal, apply)
	if err != nil || done.State != pc.DeclarationOperationSucceeded || done.Readiness.Verified.State != pc.DeclarationUnknown {
		t.Fatalf("real receipt not consumed: %+v %v", done, err)
	}
}
func TestProjectDeclarationMessageDispatchRejectsMalformed(t *testing.T) {
	store, cfg, state := projectWatchNodeFixture(t)
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.Ensure(); err != nil {
		t.Fatal(err)
	}
	message := communication.Message{CommunicationMessageID: ids.NewCommunicationMessageID(), NodeID: state.NodeID, Direction: communication.DirectionMainToNode, Kind: communication.KindProjectWatchReconcile, PayloadJSON: json.RawMessage(`{"unexpected":"private"}`)}
	processed, err := processPolledMessage(t.Context(), Client{}, store, runtimeStore, cfg, state, "malformed", message, false)
	if err != nil || processed.AckStatus == communication.AckStatusCompleted {
		t.Fatalf("malformed project watch accepted: %+v %v", processed, err)
	}
	items, err := runtimeStore.ListOutbox([]string{noderuntime.OutboxStatusPending}, 10)
	if err != nil || len(items) != 1 {
		t.Fatal("rejection ACK not durable")
	}
	if strings.Contains(string(items[0].PayloadJSON), "private") {
		t.Fatal("untrusted input leaked into ACK")
	}
}
