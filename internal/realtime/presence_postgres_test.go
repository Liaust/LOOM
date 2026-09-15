package realtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
)

func presenceDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
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
		t.Fatal("presence tests require a local disposable PostgreSQL endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "presence_" + strings.ToLower(ids.NewEventID())
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
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "corr_presence_expiry_test")
	if err != nil {
		t.Fatal(err)
	}
	return db, req
}

type presenceFixture struct {
	db       *sql.DB
	req      requestctx.Context
	node     string
	token    string
	beat     nodes.Heartbeat
	presence realtime.Presence
}

func newPresenceFixture(t *testing.T, db *sql.DB, req requestctx.Context) presenceFixture {
	t.Helper()
	f := presenceFixture{db: db, req: req, node: ids.NewNodeID()}
	presenceExec(t, db, `INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status)
		VALUES ($1,$1,'Disposable presence owner','workstation','workspace','workspace','active')`, f.node)
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: f.node, Reason: "disposable expiry test"})
	if err != nil {
		t.Fatal(err)
	}
	f.token = credential.CredentialToken
	f.beat = f.heartbeat(t)
	f.presence = f.project(t, f.beat)
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `DELETE FROM realtime.presence WHERE presence_id=$1`, f.presence.PresenceID); err != nil {
			t.Error(err)
		}
	})
	return f
}

func presenceExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f presenceFixture) heartbeat(t *testing.T) nodes.Heartbeat {
	t.Helper()
	b, err := nodes.NewService(f.db).IngestHeartbeat(t.Context(), f.req, nodes.HeartbeatInput{NodeRef: f.node, CredentialToken: f.token, ReportedStatus: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f presenceFixture) project(t *testing.T, b nodes.Heartbeat) realtime.Presence {
	t.Helper()
	p, err := realtime.NewService(f.db).ProjectNodeHeartbeat(t.Context(), f.req, realtime.NodePresenceInput{
		NodeID: f.node, HeartbeatID: b.NodeHeartbeatID, State: b.PresenceState, LastSeenAt: b.ReceivedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (f presenceFixture) assertState(t *testing.T, nodeState, realtimeState string, transitions int) {
	t.Helper()
	health, err := nodes.NewService(f.db).GetNodeHealth(t.Context(), f.node)
	if err != nil || health.Node.PresenceState != nodeState {
		t.Fatalf("node state=%s, want %s: %v", health.Node.PresenceState, nodeState, err)
	}
	p, err := realtime.NewService(f.db).GetPresence(t.Context(), f.presence.PresenceID)
	if err != nil || p.State != realtimeState {
		t.Fatalf("realtime state=%s, want %s: %v", p.State, realtimeState, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM nodes.node_status_history WHERE node_id=$1 AND reason_code='heartbeat_expired'`,
		`SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='node.presence.changed' AND payload->>'reason_code'='heartbeat_expired'`,
	} {
		var count int
		if err := f.db.QueryRowContext(t.Context(), query, f.node).Scan(&count); err != nil || count != transitions {
			t.Fatalf("expiry transitions=%d, want %d: %v", count, transitions, err)
		}
	}
}

func TestNodePresenceExpiryPostgres(t *testing.T) {
	db, req := presenceDatabase(t)
	t.Run("boundary replay history and authenticated recovery", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		service := realtime.NewService(db)
		before, err := communication.NewService(db).Health(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		out, err := service.MarkStalePresence(t.Context(), req, f.presence.ExpiresAt.Add(-time.Microsecond))
		if err != nil || out.PresenceMarkedStale != 0 {
			t.Fatalf("premature expiry: %+v %v", out, err)
		}
		f.assertState(t, "online", "online", 0)
		for i := 0; i < 2; i++ {
			out, err = service.MarkStalePresence(t.Context(), req, f.presence.ExpiresAt.In(time.FixedZone("fixture", 3600)))
			if err != nil || out.PresenceMarkedStale != 1-i {
				t.Fatalf("expiry/replay: %+v %v", out, err)
			}
			f.assertState(t, "offline", "stale", 1)
		}
		after, err := communication.NewService(db).Health(t.Context())
		if err != nil || after.OnlineNodes != before.OnlineNodes-1 || after.OfflineNodes != before.OfflineNodes+1 {
			t.Fatalf("communication presence counts: before=%+v after=%+v err=%v", before, after, err)
		}
		health, err := nodes.NewService(db).GetNodeHealth(t.Context(), f.node)
		if err != nil || health.LastHeartbeat == nil || health.LastHeartbeat.NodeHeartbeatID != f.beat.NodeHeartbeatID ||
			health.LastHeartbeat.PresenceState != "online" || !health.LastHeartbeat.ReceivedAt.Equal(f.beat.ReceivedAt) ||
			health.Node.LastSeenAt == nil || !health.Node.LastSeenAt.Equal(f.beat.ReceivedAt) {
			t.Fatalf("expiry rewrote heartbeat evidence: %+v %v", health, err)
		}
		fresh := f.heartbeat(t)
		f.project(t, fresh)
		f.assertState(t, "online", "online", 1)
	})

	t.Run("new heartbeat committed before realtime projection", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		fresh := f.heartbeat(t)
		if _, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt); err != nil {
			t.Fatal(err)
		}
		f.assertState(t, "online", "stale", 0)
		f.project(t, fresh)
		f.assertState(t, "online", "online", 0)
	})
	t.Run("fresh projection before expiry", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		fresh := f.heartbeat(t)
		f.project(t, fresh)
		out, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt)
		if err != nil || out.PresenceMarkedStale != 0 {
			t.Fatalf("expired new projection: %+v %v", out, err)
		}
		f.assertState(t, "online", "online", 0)
	})

	for _, tc := range []struct{ name, query, want string }{
		{"disabled", `UPDATE nodes.nodes SET status='disabled' WHERE node_id=$1`, "online"},
		{"retired", `UPDATE nodes.nodes SET status='retired',presence_state='revoked' WHERE node_id=$1`, "revoked"},
		{"quarantined", `UPDATE nodes.nodes SET status='quarantined',presence_state='quarantined' WHERE node_id=$1`, "quarantined"},
		{"revoked credential", `UPDATE nodes.nodes SET credential_status='revoked' WHERE node_id=$1`, "online"},
		{"revoked enrollment", `UPDATE nodes.nodes SET enrollment_status='revoked' WHERE node_id=$1`, "online"},
		{"terminal presence", `UPDATE nodes.nodes SET presence_state='revoked' WHERE node_id=$1`, "revoked"},
		{"wrong heartbeat", `UPDATE realtime.presence SET source_ref='node_heartbeat_wrong' WHERE node_id=$1`, "online"},
		{"wrong subject", `UPDATE realtime.presence SET subject_ref='node_other' WHERE node_id=$1`, "online"},
		{"non-node subject", `UPDATE realtime.presence SET subject_kind='actor' WHERE node_id=$1`, "online"},
		{"non-heartbeat source", `UPDATE realtime.presence SET source_kind='manual' WHERE node_id=$1`, "online"},
		{"wrong observation time", `UPDATE realtime.presence SET last_seen_at=last_seen_at-interval '1 microsecond' WHERE node_id=$1`, "online"},
		{"missing node binding", `UPDATE realtime.presence SET node_id=NULL WHERE node_id=$1`, "online"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPresenceFixture(t, db, req)
			presenceExec(t, db, tc.query, f.node)
			if _, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt); err != nil {
				t.Fatal(err)
			}
			f.assertState(t, tc.want, "stale", 0)
		})
	}
	t.Run("different owner and ambiguous equal timestamps", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		other := newPresenceFixture(t, db, req)
		presenceExec(t, db, `UPDATE realtime.presence SET source_ref=$2 WHERE presence_id=$1`, f.presence.PresenceID, other.beat.NodeHeartbeatID)
		if _, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt); err != nil {
			t.Fatal(err)
		}
		f.assertState(t, "online", "stale", 0)
		f.project(t, f.beat)
		presenceExec(t, db, `INSERT INTO nodes.heartbeats (node_heartbeat_id,node_id,reported_status,presence_state,received_at) VALUES ($1,$2,'ok','online',$3)`, ids.NewNodeHeartbeatID(), f.node, f.beat.ReceivedAt)
		if _, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt); err != nil {
			t.Fatal(err)
		}
		f.assertState(t, "online", "stale", 0)
	})

	t.Run("audit failure rolls back both views and history", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		presenceExec(t, db, `ALTER TABLE events.events ADD CONSTRAINT test_presence_audit_failure CHECK (status IS DISTINCT FROM 'stale') NOT VALID`)
		t.Cleanup(func() {
			if _, err := db.ExecContext(context.Background(), `ALTER TABLE events.events DROP CONSTRAINT IF EXISTS test_presence_audit_failure`); err != nil {
				t.Error(err)
			}
		})
		if _, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt); err == nil {
			t.Fatal("injected audit failure did not refuse expiry")
		}
		f.assertState(t, "online", "online", 0)
		presenceExec(t, db, `ALTER TABLE events.events DROP CONSTRAINT test_presence_audit_failure`)
		if _, err := realtime.NewService(db).MarkStalePresence(t.Context(), req, f.presence.ExpiresAt); err != nil {
			t.Fatal(err)
		}
		f.assertState(t, "offline", "stale", 1)
	})

	t.Run("concurrent expiry has one transition", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `SELECT presence_id FROM realtime.presence WHERE presence_id=$1 FOR UPDATE`, f.presence.PresenceID); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				_, err := realtime.NewService(db).MarkStalePresence(ctx, req, f.presence.ExpiresAt)
				results <- err
			}()
		}
		waitPresenceLocks(t, db, "realtime.presence", 2)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		f.assertState(t, "offline", "stale", 1)
	})

	t.Run("newer heartbeat wins while expiry waits for node", func(t *testing.T) {
		f := newPresenceFixture(t, db, req)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_id=$1 FOR NO KEY UPDATE`, f.node); err != nil {
			t.Fatal(err)
		}
		type heartbeatResult struct {
			beat nodes.Heartbeat
			err  error
		}
		beats := make(chan heartbeatResult, 1)
		go func() {
			b, err := nodes.NewService(db).IngestHeartbeat(ctx, req, nodes.HeartbeatInput{NodeRef: f.node, CredentialToken: f.token, ReportedStatus: "ok"})
			beats <- heartbeatResult{b, err}
		}()
		waitPresenceLocks(t, db, "nodes.nodes", 1)
		expired := make(chan error, 1)
		go func() {
			_, err := realtime.NewService(db).MarkStalePresence(ctx, req, f.presence.ExpiresAt)
			expired <- err
		}()
		waitPresenceLocks(t, db, "nodes.nodes", 2)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		fresh := <-beats
		if fresh.err != nil {
			t.Fatal(fresh.err)
		}
		if err := <-expired; err != nil {
			t.Fatal(err)
		}
		f.assertState(t, "online", "stale", 0)
		f.project(t, fresh.beat)
		f.assertState(t, "online", "online", 0)
	})

	for _, change := range []string{"lifecycle", "equal-time heartbeat"} {
		t.Run("revalidate after node lock wait/"+change, func(t *testing.T) {
			f := newPresenceFixture(t, db, req)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_id=$1 FOR UPDATE`, f.node); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := realtime.NewService(db).MarkStalePresence(ctx, req, f.presence.ExpiresAt)
				done <- err
			}()
			waitPresenceLocks(t, db, "nodes.nodes", 1)
			want := "online"
			if change == "lifecycle" {
				want = "quarantined"
				_, err = tx.ExecContext(ctx, `UPDATE nodes.nodes SET status='quarantined',presence_state='quarantined' WHERE node_id=$1`, f.node)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO nodes.heartbeats (node_heartbeat_id,node_id,reported_status,presence_state,received_at)
					VALUES ($1,$2,'ok','online',$3)`, ids.NewNodeHeartbeatID(), f.node, f.beat.ReceivedAt)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			f.assertState(t, want, "stale", 0)
		})
	}
}

func waitPresenceLocks(t *testing.T, db *sql.DB, queryFragment string, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
			AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1`, "%"+queryFragment+"%").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("did not observe %d blocked %s operations", want, queryFragment))
}
