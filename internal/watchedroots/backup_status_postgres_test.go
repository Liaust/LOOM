package watchedroots_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/httpapi"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/watchedroots"
)

func backupStatusDiagnosticDatabase(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	raw := os.Getenv("LOOM_F2E_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires the owned F2e socket-only PostgreSQL fixture")
	}
	root := os.Getenv("LOOM_F2E_FIXTURE_ROOT")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(root, "/tmp/loom-f2e-status-") || filepath.Dir(root) != "/tmp" || u.Host != "" || u.User.Username() != "postgres" || u.Path != "/postgres" || u.Query().Get("host") != filepath.Join(root, "socket") || u.Query().Get("sslmode") != "disable" {
		t.Fatal("refusing an unowned/shared PostgreSQL endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "f2e_status_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		_ = admin.Close()
	})
	u.Path = "/" + name
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, u.String(), root
}
func backupStatusDiagnosticLedger(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT schemaname,tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY schemaname,tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, [2]string{a, b})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, table := range tables {
		var count int
		var hash string
		// Names are system-catalog identifiers, quoted, never user selectors.
		quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
		q := `SELECT count(*),md5(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text,'[]')) FROM ` + quote(table[0]) + `.` + quote(table[1]) + ` t`
		if err := db.QueryRowContext(t.Context(), q).Scan(&count, &hash); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s.%s:%d:%s\n", table[0], table[1], count, hash)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
func TestWatchedRootBackupStatusDiagnosticPostgres(t *testing.T) {
	db, dbURL, fixtureRoot := backupStatusDiagnosticDatabase(t)
	ctx := t.Context()
	cli, err := filepath.Abs("../../.loom-acceptance/f2e-backup-status/loom")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LOOM_F2E_CLI") != cli {
		t.Fatal("required CLI must be the exact source-built F2e fixture binary")
	}
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	req := requestctx.Context{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID(), OriginNodeKey: "f2e-owner", CorrelationID: "corr_fixture_setup"}
	execSQL(`INSERT INTO identity.actors(actor_id,actor_key,display_name,actor_kind,status) VALUES($1,'f2e-actor','Disposable F2e actor','human','active')`, req.ActorID)
	execSQL(`INSERT INTO nodes.nodes(node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES($1,'f2e-owner','Disposable F2e owner','server','main','native','active')`, req.OriginNodeID)
	credential, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: req.OriginNodeID, Reason: "disposable F2e report fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.NewService(db).CreateProject(ctx, req, projects.CreateInput{Name: "Known without enrollment", Slug: "known-no-roots", HomeNodeRef: req.OriginNodeID}); err != nil {
		t.Fatal(err)
	}
	service := watchedroots.NewService(db) // Empty BackupRoot: report metadata only, no payload/custody files.
	for _, root := range []string{"empty-root", "accepted-root", "failed-root"} {
		if _, err := service.Report(ctx, watchedroots.ReportInput{CredentialToken: credential.CredentialToken, NodeRef: req.OriginNodeID, RootKey: root, Status: watchedroots.StatusHealthy, ConfigHash: "fixture-config"}); err != nil {
			t.Fatal(err)
		}
	}
	modified := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	for _, state := range []string{"accepted", "failed"} {
		input := watchedroots.BackupBatchInput{CredentialToken: credential.CredentialToken, NodeRef: req.OriginNodeID, RootKey: state + "-root", Metadata: json.RawMessage(`{"local_batch_id":"local_backup_batch_test"}`), Items: []watchedroots.BackupBatchItemInput{{LocalItemRef: "local_backup_item_test", ItemKind: watchedroots.BackupItemKindDirectory, Status: state, RelativePath: ".loom-acceptance", ContentHashURI: "sha256:" + strings.Repeat("a", 64), SizeBytes: 160, ModifiedAt: &modified, Metadata: json.RawMessage(`{"source":"loom-node-agent","local_batch_id":"local_backup_batch_test","local_item_id":"local_backup_item_test","filesystem_observation":{"kind":"directory","logical_size_bytes":160}}`)}}}
		if state == "failed" {
			input.Items[0].ErrorCode = "synthetic_failure"
			input.Items[0].ErrorMessage = "controlled metadata report failure"
		}
		got, err := service.RecordBackupBatch(ctx, input)
		if err != nil || got.Batch.Status != state {
			t.Fatalf("owner-reported %s batch: status=%s err=%v", state, got.Batch.Status, err)
		}
	}
	readURL, _ := url.Parse(dbURL)
	values := readURL.Query()
	values.Set("options", "-c default_transaction_read_only=on")
	readURL.RawQuery = values.Encode()
	readDB, err := sql.Open("pgx", readURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer readDB.Close()
	reader := watchedroots.NewService(readDB)
	handler := httpapi.NewServer(httpapi.Services{WatchedRoots: reader}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	surfaceRoot, err := os.MkdirTemp(fixtureRoot, "surface-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(surfaceRoot) })
	socket := filepath.Join(surfaceRoot, "loom.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()
	defer func() { _ = server.Close(); <-done }()
	client := localclient.New(socket)
	config := filepath.Join(surfaceRoot, "loom.env")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	runCLI := func(format string, filter watchedroots.BackupFilter) (string, string, error) {
		t.Helper()
		args := []string{"--config", config, "--socket", socket, "--correlation-id", "corr_f2e_status", "watched-roots", "backups", "status"}
		for _, pair := range [][2]string{{"--node", filter.NodeRef}, {"--root", filter.RootKey}, {"--project", filter.ProjectRef}} {
			if pair[1] != "" {
				args = append(args, pair[0], pair[1])
			}
		}
		if format != "human" {
			args = append(args, "--"+format)
		}
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Env = []string{"HOME=" + surfaceRoot, "PATH=/usr/bin:/bin", "TMPDIR=" + surfaceRoot}
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		err := cmd.Run()
		return out.String(), stderr.String(), err
	}
	before := backupStatusDiagnosticLedger(t, db)
	missing := []struct {
		name   string
		filter watchedroots.BackupFilter
	}{{"no_matching_root", watchedroots.BackupFilter{RootKey: "not-reported"}}, {"unknown_node", watchedroots.BackupFilter{NodeRef: "unknown-node"}}, {"unknown_project", watchedroots.BackupFilter{ProjectRef: "unknown-project"}}, {"known_project_no_roots", watchedroots.BackupFilter{ProjectRef: "known-no-roots"}}}
	for _, tc := range missing {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reader.GetBackupStatus(ctx, tc.filter); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("owner sentinel changed: %v", err)
			}
			_, err := client.GetWatchedRootBackupStatus(ctx, "corr_f2e_status", tc.filter)
			var failure *localclient.RequestError
			if !errors.As(err, &failure) || failure.StatusCode != 404 || failure.Envelope.Error.Code != "watched_roots.backup_target_not_found" || failure.Envelope.Error.Hint == "" || failure.Envelope.Error.CorrelationID != "corr_f2e_status" {
				t.Fatalf("HTTP/client missing-target contract: %#v %v", failure, err)
			}
			for _, format := range []string{"json", "human", "plain"} {
				stdout, stderr, err := runCLI(format, tc.filter)
				if err == nil {
					t.Fatal("CLI missing target exited zero")
				}
				if format == "json" {
					var got response.ErrorEnvelope
					if json.Unmarshal([]byte(stdout), &got) != nil || got.Error != failure.Envelope.Error || got.Meta.CorrelationID != "corr_f2e_status" {
						t.Fatalf("lost JSON envelope: %s", stdout)
					}
				} else if stdout != "" || !strings.Contains(stderr, failure.Envelope.Error.Hint) || !strings.Contains(stderr, "corr_f2e_status") {
					t.Fatalf("lost CLI human hint: %s", stderr)
				}
			}
		})
	}
	for _, tc := range []struct {
		root, state                  string
		batchCount, accepted, failed int
	}{{"empty-root", "unknown", 0, 0, 0}, {"accepted-root", "healthy", 1, 1, 0}, {"failed-root", "degraded", 1, 0, 1}} {
		t.Run(tc.root, func(t *testing.T) {
			filter := watchedroots.BackupFilter{NodeRef: req.OriginNodeID, RootKey: tc.root}
			owner, err := reader.GetBackupStatus(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			if owner.Status != tc.state || owner.BatchCount != tc.batchCount || owner.AcceptedCount != tc.accepted || owner.FailedCount != tc.failed || (tc.batchCount == 0 && owner.LatestBatch != nil) {
				t.Fatalf("owner health changed: %#v", owner)
			}
			wire, err := client.GetWatchedRootBackupStatus(ctx, "corr_f2e_status", filter)
			if err != nil || !backupStatusDiagnosticJSONEqual(owner, wire.Data) {
				t.Fatalf("success changed: %#v %v", wire.Data, err)
			}
			for _, format := range []string{"json", "human", "plain"} {
				stdout, stderr, err := runCLI(format, filter)
				if err != nil || stderr != "" {
					t.Fatalf("success CLI failed: %v %s", err, stderr)
				}
				switch format {
				case "plain":
					if stdout != tc.root+"\n" {
						t.Fatalf("plain changed: %q", stdout)
					}
				case "human":
					if !strings.Contains(stdout, "status="+tc.state) {
						t.Fatalf("human status changed: %s", stdout)
					}
				case "json":
					var got response.Envelope[watchedroots.BackupStatus]
					if json.Unmarshal([]byte(stdout), &got) != nil || !backupStatusDiagnosticJSONEqual(got.Data, owner) {
						t.Fatalf("JSON success changed: %s", stdout)
					}
				}
			}
		})
	}
	if after := backupStatusDiagnosticLedger(t, db); before != after {
		t.Fatalf("status mutated registration/correlation/events/batches/jobs/payload rows: %s -> %s", before, after)
	}
	// Controlled database-read failure only: close the fixture's reader pool.
	// No normal owner state is rewritten to synthesize this failure.
	if err := readDB.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = client.GetWatchedRootBackupStatus(ctx, "corr_f2e_status", watchedroots.BackupFilter{RootKey: "empty-root"})
	var failure *localclient.RequestError
	if !errors.As(err, &failure) || failure.StatusCode != 500 || failure.Envelope.Error.Code != "watched_roots.backup_status_failed" {
		t.Fatalf("database failure became empty success/not-found: %#v %v", failure, err)
	}
	for _, format := range []string{"json", "human"} {
		stdout, stderr, err := runCLI(format, watchedroots.BackupFilter{RootKey: "empty-root"})
		if err == nil || !strings.Contains(stdout+stderr, "watched_roots.backup_status_failed") || strings.Contains(stdout+stderr, "database is closed") {
			t.Fatalf("unsafe database failure: %s %s %v", stdout, stderr, err)
		}
	}
	if after := backupStatusDiagnosticLedger(t, db); before != after {
		t.Fatal("failed status mutated fixture ledger")
	}
	if !t.Failed() {
		t.Log("PASS: source-built CLI, actual HTTP/client and owner; missing selectors, unknown without batch, accepted/failed controls; all-table counts/hashes unchanged")
	}
}

func backupStatusDiagnosticJSONEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}
