package box

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
)

func TestOrdinaryBoxReportReapplyBoundaryPostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	node, err := nodes.NewService(db).GetNode(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	const boxID = "box_reapply_boundary"
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM box.watch_root_registrations WHERE box_id=$1`, boxID); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(`DELETE FROM watched_roots.roots WHERE root_key='box_reapply_topics' AND node_id=$1`, node.NodeID); err != nil {
			t.Error(err)
		}
	})
	root := t.TempDir()
	plan := WatchPlan{BoxID: boxID, RootPath: root, ContractPath: filepath.Join(root, ".loom", "box.yaml")}
	item := projectcontracts.ProjectWatchedRootItem{Key: "topics", BackendRootKey: "box_reapply_topics", ConfigHash: "current", ConfigJSON: json.RawMessage(`{}`), ActivationStatus: BoxWatchStatusPendingAgentApply, SourceKinds: []string{"box_topics"}, Metadata: map[string]any{}}
	service := NewService(db)
	upsert := func(item projectcontracts.ProjectWatchedRootItem, want string) WatchRootRegistration {
		t.Helper()
		got, err := service.upsertWatchRootRegistration(t.Context(), requestctx.Context{}, plan, node, item)
		if err != nil || got.ActivationStatus != want {
			t.Fatalf("pre-correlation upsert status=%s, want %s: %v", got.ActivationStatus, want, err)
		}
		return got
	}
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	upsert(item, BoxWatchStatusPendingAgentApply)
	execSQL(`INSERT INTO watched_roots.roots (watched_root_id,node_id,root_key,config_hash) VALUES ('watched_root_reapply',$1,$2,'current')`, node.NodeID, item.BackendRootKey)
	if err := service.correlateWatchRootReports(t.Context(), boxID, []string{item.BackendRootKey}); err != nil {
		t.Fatal(err)
	}
	// Inspect the returned durable upsert before any downstream correlator runs.
	// This catches the transient pending state that final Apply results hid.
	for i := 0; i < 3; i++ {
		got := upsert(item, BoxWatchStatusReported)
		if got.DesiredRevision != 1 || got.AppliedRevision != 0 {
			t.Fatal("ordinary reapply changed revision or fabricated backup acknowledgement")
		}
	}
	// Emulate Report's transaction while a reapply starts from the older
	// committed report. Correlation must use the replacement after waiting.
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE watched_roots.roots SET config_hash='replacement' WHERE root_key=$1 AND node_id=$2`, item.BackendRootKey, node.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE box.watch_root_registrations SET activation_status='pending_agent_apply' WHERE box_id=$1`, boxID); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- service.correlateWatchRootReports(waitCtx, boxID, []string{item.BackendRootKey})
		close(done)
	}()
	defer func() { cancel(); _ = tx.Rollback(); <-done }()
	waiting := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%UPDATE box.watch_root_registrations b%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("correlator did not reach the report transaction")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT activation_status FROM box.watch_root_registrations WHERE box_id=$1`, boxID).Scan(&status); err != nil || status != BoxWatchStatusStale {
		t.Fatalf("reapply correlated an obsolete report: %s %v", status, err)
	}
	execSQL(`UPDATE watched_roots.roots SET config_hash='current' WHERE root_key=$1 AND node_id=$2`, item.BackendRootKey, node.NodeID)
	upsert(item, BoxWatchStatusPendingAgentApply)
	if err := service.correlateWatchRootReports(t.Context(), boxID, []string{item.BackendRootKey}); err != nil {
		t.Fatal(err)
	}
	changed := item
	changed.ConfigHash = "changed"
	if got := upsert(changed, BoxWatchStatusPendingAgentApply); got.DesiredRevision != 2 {
		t.Fatal("changed policy did not advance revision")
	}
	for _, status := range []string{BoxWatchStatusDisabled, BoxWatchStatusStale, BoxWatchStatusBlocked} {
		execSQL(`UPDATE box.watch_root_registrations SET activation_status=$1 WHERE box_id=$2`, status, boxID)
		upsert(changed, BoxWatchStatusPendingAgentApply)
	}
	execSQL(`UPDATE box.watch_root_registrations SET activation_status='reported' WHERE box_id=$1`, boxID)
	disabled := changed
	disabled.ActivationStatus = BoxWatchStatusDisabled
	upsert(disabled, BoxWatchStatusDisabled)
	execSQL(`UPDATE watched_roots.roots SET config_hash='changed' WHERE node_id=$1 AND root_key=$2`, node.NodeID, item.BackendRootKey)
	if err := service.correlateWatchRootReports(t.Context(), boxID, []string{item.BackendRootKey}); err != nil {
		t.Fatal(err)
	}
	rows, err := service.listWatchRootRegistrations(t.Context(), boxID)
	if err != nil || len(rows) != 1 || rows[0].ActivationStatus != BoxWatchStatusDisabled {
		t.Fatal("correlation revived disabled policy", err)
	}
	upsert(changed, BoxWatchStatusPendingAgentApply)
	if err := service.markStaleWatchRootRegistrations(t.Context(), plan, node.NodeID, "box_policy", nil); err != nil {
		t.Fatal(err)
	}
	rows, err = service.listWatchRootRegistrations(t.Context(), boxID)
	if err != nil || len(rows) != 1 || rows[0].ActivationStatus != BoxWatchStatusStale {
		t.Fatal("removed policy was not stale", err)
	}
	protected := postgresBackupItem(t, root, "reapply", "main")
	upsert(protected, BoxWatchStatusPendingAgentApply)
	execSQL(`UPDATE box.watch_root_registrations SET activation_status='reported' WHERE box_id=$1 AND backend_root_key=$2`, boxID, protected.BackendRootKey)
	upsert(protected, BoxWatchStatusPendingAgentApply) // No exact acknowledgement.
	execSQL(`UPDATE box.watch_root_registrations SET activation_status='reported',applied_revision=desired_revision,applied_config_hash=desired_config_hash WHERE box_id=$1 AND backend_root_key=$2`, boxID, protected.BackendRootKey)
	upsert(protected, BoxWatchStatusReported)
	execSQL(`UPDATE box.watch_root_registrations SET applied_revision=desired_revision-1 WHERE box_id=$1 AND backend_root_key=$2`, boxID, protected.BackendRootKey)
	upsert(protected, BoxWatchStatusPendingAgentApply)
	execSQL(`UPDATE box.watch_root_registrations SET activation_status='reported',applied_revision=desired_revision,applied_config_hash='wrong' WHERE box_id=$1 AND backend_root_key=$2`, boxID, protected.BackendRootKey)
	upsert(protected, BoxWatchStatusPendingAgentApply)
}

func TestApplyWatchPolicyRoutesMixedOwnersPostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO nodes.nodes (node_id, node_key, display_name, node_kind, node_role, runtime_class, status) VALUES ('node_pf_mac', 'macbook', 'Mac', 'workstation', 'workspace', 'workspace', 'active') ON CONFLICT (node_id) DO NOTHING`,
		`DELETE FROM box.watch_root_registrations WHERE box_id = 'box_pf_mixed'`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var mainNodeID string
	if err := db.QueryRowContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_key = 'main'`).Scan(&mainNodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM box.watch_root_registrations WHERE box_id = 'box_pf_mixed'`)
		_, _ = db.ExecContext(ctx, `DELETE FROM nodes.nodes WHERE node_id = 'node_pf_mac'`)
	})

	root := t.TempDir()
	mainItem := postgresBackupItem(t, root, "main-archive", "main")
	macItem := postgresBackupItem(t, root, "mac-archive", "macbook")
	plan := WatchPlan{
		SchemaVersion: SchemaVersion,
		RootPath:      root,
		ProjectRoot:   root,
		OwnerNode:     "main",
		BoxID:         "box_pf_mixed",
		ContractPath:  filepath.Join(root, ".loom", "box.yaml"),
		WatchedRoots:  []projectcontracts.ProjectWatchedRootItem{mainItem, macItem},
	}
	result, err := NewService(db).ApplyWatchPolicy(ctx, requestctx.Context{}, WatchApplyInput{Plan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 || len(result.Registrations) != 2 {
		t.Fatalf("mixed result = %#v", result)
	}
	owners := map[string]string{}
	for _, registration := range result.Registrations {
		owners[registration.SourceContractKey] = registration.NodeID
		if registration.AppliedRevision != 0 || registration.ActivationStatus != BoxWatchStatusPendingAgentApply {
			t.Fatalf("registration falsely claims node application: %#v", registration)
		}
	}
	if owners["main-archive"] != mainNodeID || owners["mac-archive"] != "node_pf_mac" {
		t.Fatalf("contract owner routing = %#v", owners)
	}

	repeated, err := NewService(db).ApplyWatchPolicy(ctx, requestctx.Context{}, WatchApplyInput{Plan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range repeated.Registrations {
		if registration.DesiredRevision != 1 {
			t.Fatalf("unchanged reapply advanced revision: %#v", registration)
		}
	}

	if _, err := db.ExecContext(ctx, `UPDATE box.watch_root_registrations SET activation_status = 'disabled', applied_revision = desired_revision, applied_config_hash = '' WHERE box_id = 'box_pf_mixed' AND source_contract_key = 'main-archive'`); err != nil {
		t.Fatal(err)
	}
	reenabled, err := NewService(db).ApplyWatchPolicy(ctx, requestctx.Context{}, WatchApplyInput{Plan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range reenabled.Registrations {
		if registration.SourceContractKey == "main-archive" && registration.ActivationStatus != BoxWatchStatusPendingAgentApply {
			t.Fatalf("re-enabled protected root became %q instead of pending node apply", registration.ActivationStatus)
		}
	}
}

func TestApplyWatchPolicyRejectsUnknownOwnerBeforeRegistrationPostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	item := postgresBackupItem(t, root, "unknown-owner", "does-not-exist")
	plan := WatchPlan{SchemaVersion: SchemaVersion, RootPath: root, ProjectRoot: root, OwnerNode: "does-not-exist", BoxID: "box_pf_unknown", ContractPath: filepath.Join(root, ".loom", "box.yaml"), WatchedRoots: []projectcontracts.ProjectWatchedRootItem{item}}
	if _, err := NewService(db).ApplyWatchPolicy(context.Background(), requestctx.Context{}, WatchApplyInput{Plan: &plan}); err == nil {
		t.Fatal("expected unknown owner-node failure")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM box.watch_root_registrations WHERE box_id = 'box_pf_unknown'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unknown owner wrote %d registrations", count)
	}
}

func TestCorrelateWatchRootReportsKeepsLegacyRootsIndependentFromProtectedFolderAcknowledgementsPostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	var nodeID string
	if err := db.QueryRowContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_key = 'main'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}

	const (
		boxID         = "box_report_correlation"
		ordinaryRoot  = "box_notes_report_correlation"
		protectedRoot = "backup_photos_report_correlation"
	)
	cleanup := func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM box.watch_root_registrations WHERE box_id = $1`, boxID)
		_, _ = db.ExecContext(ctx, `DELETE FROM watched_roots.roots WHERE node_id = $1 AND root_key IN ($2, $3)`, nodeID, ordinaryRoot, protectedRoot)
	}
	cleanup()
	t.Cleanup(cleanup)

	for _, root := range []struct {
		id, key, hash string
	}{
		{"watched_root_report_correlation_notes", ordinaryRoot, "ordinary-hash"},
		{"watched_root_report_correlation_photos", protectedRoot, "protected-hash"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO watched_roots.roots (watched_root_id, node_id, root_key, config_hash)
			VALUES ($1, $2, $3, $4)`, root.id, nodeID, root.key, root.hash); err != nil {
			t.Fatal(err)
		}
	}
	for _, registration := range []struct {
		id, area, localKey, backendKey, sourceKind, configHash, desiredHash string
	}{
		{"box_watch_root_registration_report_correlation_notes", "notes", "notes", ordinaryRoot, "box_policy", "ordinary-hash", "unrelated-generation-hash"},
		{"box_watch_root_registration_report_correlation_photos", "backup_photos", "photos", protectedRoot, backupcontracts.SourceKindBoxBackupContract, "protected-hash", "protected-hash"},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO box.watch_root_registrations (
				box_watch_root_registration_id, box_id, box_root_path, box_contract_path,
				node_id, area_key, local_root_key, backend_root_key, activation_status,
				config_hash, source_kind, source_contract_key, desired_revision,
				applied_revision, desired_config_hash, applied_config_hash
			) VALUES ($1, $2, '/fixture/box', '/fixture/box/.loom/box.yaml', $3, $4, $5, $6,
				'pending_agent_apply', $7, $8, $5, 3, 0, $9, '')`,
			registration.id, boxID, nodeID, registration.area, registration.localKey,
			registration.backendKey, registration.configHash, registration.sourceKind,
			registration.desiredHash); err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(db)
	if err := service.correlateWatchRootReports(ctx, boxID, []string{ordinaryRoot, protectedRoot}); err != nil {
		t.Fatal(err)
	}
	assertRegistrationStatus := func(rootKey, want string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(ctx, `SELECT activation_status FROM box.watch_root_registrations WHERE box_id = $1 AND backend_root_key = $2`, boxID, rootKey).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("registration %s status = %q, want %q", rootKey, got, want)
		}
	}
	assertRegistrationStatus(ordinaryRoot, BoxWatchStatusReported)
	assertRegistrationStatus(protectedRoot, BoxWatchStatusPendingAgentApply)

	if _, err := db.ExecContext(ctx, `
		UPDATE box.watch_root_registrations
		SET desired_revision = 99, desired_config_hash = 'another-generation-hash'
		WHERE box_id = $1 AND backend_root_key = $2`, boxID, ordinaryRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE box.watch_root_registrations
		SET applied_revision = desired_revision, applied_config_hash = desired_config_hash
		WHERE box_id = $1 AND backend_root_key = $2`, boxID, protectedRoot); err != nil {
		t.Fatal(err)
	}
	if err := service.correlateWatchRootReports(ctx, boxID, []string{ordinaryRoot, protectedRoot}); err != nil {
		t.Fatal(err)
	}
	assertRegistrationStatus(ordinaryRoot, BoxWatchStatusReported)
	assertRegistrationStatus(protectedRoot, BoxWatchStatusReported)
}

func postgresBackupItem(t *testing.T, root, key, owner string) projectcontracts.ProjectWatchedRootItem {
	t.Helper()
	contract := validContractWithBackupTarget(key, root)
	contract.OwnerNode = owner
	contract.Target.Path = filepath.Join(root, key)
	item, err := backupcontracts.WatchedRootItem(contract, filepath.Join(root, key+".yaml"), backupcontracts.WatchPlanOptions{BoxRoot: root, BoxID: "box_pf_mixed", OwnerNode: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
