package httpapi

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
)

func TestNodePresenceExpiryHTTPPostgres(t *testing.T) {
	db, req := nodePresenceHTTPDatabase(t)
	ctx := t.Context()
	owner := ids.NewNodeID()
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status)
		VALUES ($1,$1,'Disposable API owner','workstation','workspace','workspace','active')`, owner); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(Services{
		DB: db, Nodes: nodes.NewService(db), Realtime: realtime.NewService(db), Communication: communication.NewService(db),
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := client.IssueNodeCredential(ctx, req.CorrelationID, owner, nodes.IssueNodeCredentialInput{Reason: "disposable API expiry proof"})
	if err != nil {
		t.Fatal(err)
	}
	input := nodes.HeartbeatInput{NodeRef: owner, CredentialToken: credential.Data.CredentialToken, ReportedStatus: "ok"}
	heartbeat, err := agent.Heartbeat(ctx, req.CorrelationID, input)
	if err != nil {
		t.Fatal(err)
	}
	presence, err := client.GetRealtimePresence(ctx, req.CorrelationID, owner)
	if err != nil || presence.Data.SourceRef != heartbeat.Data.NodeHeartbeatID {
		t.Fatalf("heartbeat projection: %v", err)
	}
	assertViews := func(wantNode, wantRealtime string, beat nodes.Heartbeat) {
		t.Helper()
		health, err := client.GetNodeHealth(ctx, req.CorrelationID, owner)
		if err != nil || health.Data.Node.PresenceState != wantNode || health.Data.LastHeartbeat == nil ||
			health.Data.LastHeartbeat.NodeHeartbeatID != beat.NodeHeartbeatID || health.Data.LastHeartbeat.PresenceState != "online" {
			t.Fatalf("node health current/history mismatch: %+v %v", health.Data, err)
		}
		node, err := client.GetNode(ctx, req.CorrelationID, owner)
		if err != nil || node.Data.PresenceState != wantNode {
			t.Fatalf("node inspect: %+v %v", node.Data, err)
		}
		list, err := client.ListNodes(ctx, req.CorrelationID, 50)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range list.Data {
			if n.NodeID == owner {
				found = true
				if n.PresenceState != wantNode {
					t.Fatalf("node list presence=%s", n.PresenceState)
				}
			}
		}
		if !found {
			t.Fatal("node absent from list")
		}
		p, err := client.GetRealtimePresence(ctx, req.CorrelationID, owner)
		if err != nil || p.Data.State != wantRealtime {
			t.Fatalf("realtime presence: %+v %v", p.Data, err)
		}
	}
	assertViews("online", "online", heartbeat.Data)
	for i := 0; i < 2; i++ {
		result, err := realtime.NewService(db).MarkStalePresence(ctx, req, presence.Data.ExpiresAt)
		if err != nil || result.PresenceMarkedStale != 1-i {
			t.Fatalf("expiry/replay: %+v %v", result, err)
		}
		assertViews("offline", "stale", heartbeat.Data)
	}
	input.CredentialToken = "deliberately-invalid-disposable-token"
	if _, err := agent.Heartbeat(ctx, req.CorrelationID, input); err == nil {
		t.Fatal("unauthenticated heartbeat revived owner")
	}
	assertViews("offline", "stale", heartbeat.Data)
	input.CredentialToken = credential.Data.CredentialToken
	fresh, err := agent.Heartbeat(ctx, req.CorrelationID, input)
	if err != nil {
		t.Fatal(err)
	}
	assertViews("online", "online", fresh.Data)
}

func nodePresenceHTTPDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local disposable PostgreSQL administrator LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, socket := u.Hostname(), u.Query().Get("host")
	if !((socket == "" && (host == "localhost" || host == "127.0.0.1" || host == "::1")) ||
		(host == "" && strings.HasPrefix(socket, "/tmp/"))) {
		t.Fatal("presence API tests require a local disposable PostgreSQL endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "presence_http_" + strings.ToLower(ids.NewEventID())
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *sql.DB
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	u.Path = "/" + name
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "corr_node_presence_http_test")
	if err != nil {
		t.Fatal(err)
	}
	return db, req
}
