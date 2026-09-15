package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"loom.local/loom/internal/backup"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/localclient"
)

// This is the actual registered HTTP route with the actual cloud backend and
// service authority configuration. Its fixture is created by the shell smoke;
// there is no fake restore handler or configurable production connection.
func TestCloudFetchFailureDisposableAcceptance(t *testing.T) {
	root := os.Getenv("LOOM_TEST_FETCH_FAILURE_ROOT")
	if root == "" {
		t.Skip("requires owned Borg/PostgreSQL smoke")
	}
	if !filepath.IsAbs(root) || filepath.Base(root) != ".loom-acceptance" || !strings.HasPrefix(filepath.Base(filepath.Dir(root)), "loom-fetch-failure-") {
		t.Fatal("not a fixture")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The smoke records a short, private socket root independently of TMPDIR.
	// Refuse a replaced or symlinked directory before connecting to PostgreSQL
	// or creating either fixture listener.
	var custody struct {
		Path      string `json:"path"`
		Parent    string `json:"parent"`
		ParentDev uint64 `json:"parent_dev"`
		ParentIno uint64 `json:"parent_ino"`
		Dev       uint64 `json:"dev"`
		Ino       uint64 `json:"ino"`
		UID       uint32 `json:"uid"`
	}
	custodyRaw, err := os.ReadFile(filepath.Join(root, "socket-root.json"))
	if err != nil || len(custodyRaw) > 4096 {
		t.Fatal("missing bounded socket custody receipt")
	}
	if err := json.Unmarshal(custodyRaw, &custody); err != nil {
		t.Fatal(err)
	}
	shortParent, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(custody.Path)
	if len(name) != 20 || !strings.HasPrefix(name, "lff-") || strings.Trim(name[4:], "0123456789abcdef") != "" || custody.Parent != shortParent || custody.Path != filepath.Join(shortParent, name) || len(custody.Path+"/.s.PGSQL.5432") >= 104 {
		t.Fatal("invalid short socket root")
	}
	for _, expected := range []struct {
		path     string
		dev, ino uint64
	}{{shortParent, custody.ParentDev, custody.ParentIno}, {custody.Path, custody.Dev, custody.Ino}} {
		info, err := os.Lstat(expected.path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("socket custody directory is not real")
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint64(st.Dev) != expected.dev || uint64(st.Ino) != expected.ino {
			t.Fatal("socket custody directory was replaced")
		}
		if expected.path == custody.Path && (st.Uid != custody.UID || st.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0700) {
			t.Fatal("socket custody owner or mode changed")
		}
	}
	pgSocket := custody.Path
	// Unix-only, fresh smoke-owned cluster. No environment database URL accepted.
	conn, err := pgx.Connect(ctx, "host="+pgSocket+" user=postgres dbname=postgres sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var dataDir, listen string
	if err := conn.QueryRow(ctx, "SHOW data_directory").Scan(&dataDir); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SHOW listen_addresses").Scan(&listen); err != nil {
		t.Fatal(err)
	}
	if dataDir != filepath.Join(root, "postgres") || listen != "" {
		t.Fatal("cluster not owned Unix-only fixture")
	}
	snapshot := func() string {
		rows, err := conn.Query(ctx, "SELECT datname, oid::text, datdba::text FROM pg_database ORDER BY datname")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var n, o, d string
			if err := rows.Scan(&n, &o, &d); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, n+":"+o+":"+d)
		}
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}
		return strings.Join(lines, "\n")
	}
	before := snapshot()
	configPath := filepath.Join(root, "cloud.json")
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Ref         string `json:"ref"`
		Archive     string `json:"archive"`
		Manifest    string `json:"manifest_sha256"`
		Bytes       int64  `json:"source_bytes"`
		Entries     int    `json:"source_entries"`
		PayloadRoot string `json:"payload_root"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Bytes < 2<<20 || fixture.Bytes > 8<<20 || fixture.Entries > 128 {
		t.Fatal("fixture exceeded bounded input")
	}
	if loaded.Config.Snapshots.Borg.Repository != filepath.Join(root, "repository") {
		t.Fatal("wrong repository")
	}
	if !filepath.IsLocal(fixture.PayloadRoot) || !strings.HasPrefix(fixture.PayloadRoot, strings.TrimPrefix(filepath.Dir(root), "/")+"/go-tmp/") {
		t.Fatal("unsafe payload fixture path")
	}
	realBorg := loaded.Config.Snapshots.Borg.Binary
	cfg := loaded.Config
	wrapper := filepath.Join(root, "borg-extraction-fixture")
	cfg.Snapshots.Borg.Binary = wrapper
	// No arbitrary supplied script or command args. Only one fixed extraction
	// path is made unreplaceable; Borg itself produces the nonzero result.
	script := `#!/bin/sh
set -eu
operation="$3"
printf '%s\n' "$operation" >> ` + shellQuoteFetch(filepath.Join(root, "fetch-commands")) + `
case "$operation" in
 list|info|check) exec ` + shellQuoteFetch(realBorg) + ` "$@" ;;
 extract)
  case " $* " in *" --stdout "*) exec ` + shellQuoteFetch(realBorg) + ` "$@" ;; esac
  mode=$(cat ` + shellQuoteFetch(filepath.Join(root, "failure-mode")) + `)
  if [ "$mode" = conflict ]; then
   mkdir -p ` + shellQuoteFetch(filepath.Join(fixture.PayloadRoot, "Documents", "payload.txt")) + `
   printf fixture > ` + shellQuoteFetch(filepath.Join(fixture.PayloadRoot, "Documents", "payload.txt", "obstruction")) + `
  fi
  set +e
  ` + shellQuoteFetch(realBorg) + ` "$@" --noxattrs
  status=$?
  set -e
  printf '%s\n' "$status" >> ` + shellQuoteFetch(filepath.Join(root, "native-extract-exits")) + `
  printf 'private path /fixture/private, postgresql://owner:canary-URL@host/db, token=canary-TOKEN\n' >&2
  if [ "$mode" = post_success ]; then
   if [ "$status" != 0 ]; then exit "$status"; fi
   exit 2
  fi
  exit "$status"
 ;;
 *) exit 97 ;;
esac
`
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(cfg)
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	runtime := cloudRestoreRuntimeConfig()
	runtime.RestoreAuthoritySocketPath = filepath.Join(pgSocket, "authority.sock")
	listener, err := net.Listen("unix", runtime.RestoreAuthoritySocketPath)
	if err != nil {
		t.Fatal(err)
	}
	var authorityConnections atomic.Int64
	authorityDone := make(chan struct{})
	go func() {
		defer close(authorityDone)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			authorityConnections.Add(1)
			c.Close()
		}
	}()
	defer func() { listener.Close(); <-authorityDone }()
	runtime.RestoreOperationalDatabase = "loom_active_fixture"
	runtime.RestoreProvenanceDatabase = "loom_provenance_active_fixture"
	socket := filepath.Join(pgSocket, "http.sock")
	httpListener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	service := NewServer(Services{RuntimeConfig: runtime, AllowCloudConfigOverride: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Private server seam supplies fixture-owned service configuration. The
	// actual cleanup HTTP request still accepts selectors only.
	service.cloudRestoreCleanupConfig = func() (cloudstorage.Config, error) { return cfg, nil }
	server := http.Server{Handler: service.Handler()}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(httpListener) }()
	defer func() { server.Close(); <-done }()
	client := localclient.New(socket)
	var receipts []map[string]any
	for _, mode := range []string{"conflict", "post_success"} {
		if err := os.WriteFile(filepath.Join(root, "failure-mode"), []byte(mode), 0600); err != nil {
			t.Fatal(err)
		}
		target := "loom_restore_drill_" + mode
		provenance := "loom_provenance_restore_drill_" + mode
		_, err := client.CloudSnapshotRestoreDrillLive(ctx, "corr_fetch_"+mode, localclient.CloudSnapshotRestoreDrillInput{ConfigPath: configPath, NodeID: "loom-main", Ref: fixture.Ref, TargetDatabase: target, ProvenanceTargetDatabase: provenance})
		var requestErr *localclient.RequestError
		if !errors.As(err, &requestErr) || requestErr.LoomError().Code != "cloud.fetch_extraction_failed" {
			t.Fatalf("routing error=%v", err)
		}
		if strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), root) {
			t.Fatal("private error escaped")
		}
		matches, err := filepath.Glob(filepath.Join(root, "state", "restore-drills", "*", cloudstorage.RestoreFailureReceiptFile))
		if err != nil {
			t.Fatal(err)
		}
		var receipt cloudstorage.RestoreFailureReceipt
		found := ""
		for _, path := range matches {
			b, _ := os.ReadFile(path)
			var r cloudstorage.RestoreFailureReceipt
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			if r.TargetDatabase == target {
				receipt = r
				found = path
				raw = b
			}
		}
		if found == "" || receipt.FetchFailure == nil || receipt.Schema != cloudstorage.RestoreFetchFailureReceiptSchema || receipt.Archive != fixture.Archive || receipt.DirectArchiveManifestSHA256 != fixture.Manifest || receipt.FetchFailure.ProvenanceTarget != provenance || receipt.FetchFailure.BorgOperation != "extract" || receipt.FetchFailure.ExitCode == nil || *receipt.FetchFailure.ExitCode == 0 {
			t.Fatalf("missing failure identity: %+v", receipt)
		}
		if receipt.FetchFailure.DatabaseOperations != "not_started" || receipt.FetchFailure.AuthorityOperations != "not_started" || receipt.CleanupAttempted || receipt.CleanupSucceeded || receipt.CleanupStatus != "not_attempted" || receipt.FetchFailure.PayloadDisposition != "retained" {
			t.Fatal("false mutation/cleanup truth")
		}
		if len(raw) > 4096 || strings.Contains(string(raw), "canary") || strings.Contains(string(raw), root) {
			t.Fatal("unsafe durable evidence")
		}
		info, err := os.Lstat(found)
		if err != nil || info.Mode().Perm() != 0600 || !info.Mode().IsRegular() {
			t.Fatal("receipt custody")
		}
		payload := filepath.Join(filepath.Dir(found), "backup", fixture.PayloadRoot, "Documents", "megabytes.bin")
		if info, err := os.Stat(payload); err != nil || info.Size() != 2<<20 {
			t.Fatalf("partial extraction payload not retained: %v", err)
		}
		if mode == "post_success" {
			// All extracted bytes can still verify. The nonzero process remains failure.
			plan, err := cloudFetchFailureVerifyExtracted(ctx, filepath.Join(filepath.Dir(found), "backup"), fixture.Manifest, cfg.Snapshots.Borg.Repository, fixture.Archive, fixture.PayloadRoot)
			if err != nil || plan != "succeeded" {
				t.Fatalf("post-success bytes should verify: %s %v", plan, err)
			}
		}
		if snapshot() != before || authorityConnections.Load() != 0 {
			t.Fatal("database or authority mutation")
		}
		// Actual local observations, kept separate from the failure receipt's
		// historical not_started claim. No production state is inspected.
		originalFailure := append([]byte(nil), raw...)
		attemptRoot := filepath.Dir(found)
		attempt := filepath.Base(attemptRoot)
		info, err = os.Stat(attemptRoot)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		catalog := snapshot()
		if strings.Contains(catalog, target+":") || strings.Contains(catalog, provenance+":") {
			t.Fatal("fixture targets exist")
		}
		commandBefore, err := os.ReadFile(filepath.Join(root, "fetch-commands"))
		if err != nil {
			t.Fatal(err)
		}
		observations := map[string][]byte{
			"inactive":  []byte("synchronous owned restore request returned; Borg child exited; " + attempt),
			"databases": []byte(catalog),
			"health":    []byte("owned HTTP fixture available; no fixture backup job scheduled; synchronous Borg child ended; " + attempt),
		}
		digest := func(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
		for name, source := range observations {
			if err := os.WriteFile(filepath.Join(root, "cleanup-"+mode+"-"+name+".txt"), source, 0600); err != nil {
				t.Fatal(err)
			}
		}
		now := time.Now().UTC()
		preflight := cloudstorage.RestoreCleanupPreflight{Schema: "loom.cloud_restore_cleanup_preflight.v1", Attempt: attempt, Device: uint64(st.Dev), Inode: uint64(st.Ino), OperationalTarget: target, ProvenanceTarget: provenance,
			Inactive:        cloudstorage.RestoreCleanupObservation{Status: "inactive", ObservedAt: now, SourceSHA256: digest(observations["inactive"])},
			DatabaseAbsence: cloudstorage.RestoreCleanupObservation{Status: "both_absent", ObservedAt: now, SourceSHA256: digest(observations["databases"])},
			HealthQuiet:     cloudstorage.RestoreCleanupObservation{Status: "healthy_no_restore_borg_backup", ObservedAt: now, SourceSHA256: digest(observations["health"])},
		}
		preflightRaw, _ := json.Marshal(preflight)
		if err := os.WriteFile(filepath.Join(attemptRoot, cloudstorage.RestoreCleanupPreflightFile), preflightRaw, 0600); err != nil {
			t.Fatal(err)
		}
		externalRefused := false
		if mode == "post_success" {
			// The extracted fidelity payload has a real internal pair. An exact
			// extra fixture-owned sibling alias makes it genuinely unaccounted.
			source := filepath.Join(attemptRoot, "backup", fixture.PayloadRoot, "Documents", "payload.txt")
			linked, e := os.Stat(source)
			if e != nil || linked.Sys().(*syscall.Stat_t).Nlink != 2 {
				t.Fatal("missing real internal pair", e)
			}
			outside := filepath.Join(attemptRoot, "fixture-external-hardlink")
			if e = os.Link(source, outside); e != nil {
				t.Fatal(e)
			}
			_, e = client.CloudRestoreCleanupPlan(ctx, "corr_cleanup_external_"+mode, cloudstorage.RestoreCleanupPlanInput{Attempt: attempt})
			if !errors.As(e, &requestErr) || requestErr.LoomError().Code != "cloud.restore_cleanup_refused" {
				t.Fatal("external alias accepted", e)
			}
			for _, name := range []string{cloudstorage.RestoreCleanupPlanFile, cloudstorage.RestoreCleanupReceiptFile, ".restore-cleanup-private"} {
				if _, e := os.Lstat(filepath.Join(attemptRoot, name)); !os.IsNotExist(e) {
					t.Fatal("external refusal mutated cleanup state")
				}
			}
			preserved, readErr := os.ReadFile(found)
			commands, commandErr := os.ReadFile(filepath.Join(root, "fetch-commands"))
			retained, statErr := os.Stat(source)
			if readErr != nil || commandErr != nil || statErr != nil || retained.Sys().(*syscall.Stat_t).Nlink != 3 || !bytes.Equal(originalFailure, preserved) || !bytes.Equal(commandBefore, commands) || snapshot() != before || authorityConnections.Load() != 0 {
				t.Fatal("external refusal changed payload/evidence/state")
			}
			externalRefused = true
			// Fixture setup only: removing this exact external name is not a
			// cleanup action. A fresh reviewed plan now sees both internal names.
			if e = os.Remove(outside); e != nil {
				t.Fatal(e)
			}
			linked, e = os.Stat(source)
			if e != nil || linked.Sys().(*syscall.Stat_t).Nlink != 2 {
				t.Fatal("internal group not restored", e)
			}
		}
		cleanupPlan, err := client.CloudRestoreCleanupPlan(ctx, "corr_cleanup_plan_"+mode, cloudstorage.RestoreCleanupPlanInput{Attempt: attempt})
		if err != nil || cleanupPlan.Data.Status != "planned" {
			t.Fatalf("cleanup plan: %+v %v", cleanupPlan, err)
		}
		apply := cloudstorage.RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: cleanupPlan.Data.PlanDigest, DryRun: true}
		dry, err := client.CloudRestoreCleanupApply(ctx, "corr_cleanup_dry_"+mode, apply)
		if err != nil || !dry.Data.DryRun {
			t.Fatalf("cleanup dry-run: %+v %v", dry, err)
		}
		if info, err := os.Stat(payload); err != nil || info.Size() != 2<<20 {
			t.Fatal("dry-run changed payload")
		}
		apply.DryRun, apply.Yes = false, true
		cleaned, err := client.CloudRestoreCleanupApply(ctx, "corr_cleanup_apply_"+mode, apply)
		if err != nil || cleaned.Data.Status != "completed" || cleaned.Data.ConfirmedRemoved != cleanupPlan.Data.Entries {
			t.Fatalf("cleanup apply: %+v %v", cleaned, err)
		}
		if _, err := os.Lstat(filepath.Join(attemptRoot, "backup")); !os.IsNotExist(err) {
			t.Fatal("payload remains")
		}
		retained, err := os.ReadFile(found)
		if err != nil || !bytes.Equal(retained, originalFailure) {
			t.Fatal("original failure changed")
		}
		journalPath := filepath.Join(attemptRoot, cloudstorage.RestoreCleanupReceiptFile)
		journal, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "post_success" {
			remaining := uint64(2)
			for _, line := range bytes.Split(bytes.TrimSpace(journal), []byte("\n")) {
				var event struct {
					Status string `json:"status"`
					Entry  *struct {
						Identity struct {
							Links uint64 `json:"links"`
						} `json:"identity"`
					} `json:"entry"`
					After *struct {
						Identity struct {
							Links uint64 `json:"links"`
						} `json:"identity"`
					} `json:"after"`
				}
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if event.Status == "removed" && event.Entry != nil {
					if remaining == 0 || event.After == nil || event.Entry.Identity.Links != remaining || event.After.Identity.Links != remaining-1 {
						t.Fatal("real internal pair has an invalid removal transition")
					}
					remaining--
				}
			}
			if remaining != 0 {
				t.Fatal("real internal pair did not acknowledge both inode transitions")
			}
		}
		for _, name := range []string{cloudstorage.RestoreCleanupPlanFile, cloudstorage.RestoreCleanupReceiptFile} {
			info, err := os.Lstat(filepath.Join(attemptRoot, name))
			if err != nil || info.Mode().Perm() != 0600 || !info.Mode().IsRegular() {
				t.Fatal("cleanup control custody")
			}
		}
		replay, err := client.CloudRestoreCleanupApply(ctx, "corr_cleanup_replay_"+mode, apply)
		if err != nil || replay.Data.Status != "completed" {
			t.Fatalf("cleanup replay: %+v %v", replay, err)
		}
		afterJournal, err := os.ReadFile(journalPath)
		afterCommands, commandErr := os.ReadFile(filepath.Join(root, "fetch-commands"))
		if err != nil || commandErr != nil || !bytes.Equal(journal, afterJournal) || !bytes.Equal(commandBefore, afterCommands) || snapshot() != before || authorityConnections.Load() != 0 {
			t.Fatal("cleanup/replay changed evidence, ran Borg, or changed database/authority state")
		}
		receipts = append(receipts, map[string]any{"external_hardlink_refused": externalRefused, "internal_hardlink_cleaned": mode == "post_success", "external_refusal_payload_retained": externalRefused, "cleanup_status": cleaned.Data.Status, "cleanup_entries": cleaned.Data.ConfirmedRemoved, "original_failure_preserved": true, "cleanup_replay_unchanged": true, "cleanup_borg_commands": 0, "mode": mode, "receipt": filepath.Base(filepath.Dir(found)) + "/" + cloudstorage.RestoreFailureReceiptFile, "native_exit": *receipt.FetchFailure.ExitCode, "authority_connections": authorityConnections.Load(), "database_catalog_unchanged": true, "source_bytes": fixture.Bytes})
	}
	exits, err := os.ReadFile(filepath.Join(root, "native-extract-exits"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(exits))
	if len(lines) != 2 || lines[0] == "0" || lines[1] != "0" {
		t.Fatalf("native failure was not exercised: %s", exits)
	}
	// A pg_restore trap is supplied by the smoke; neither case may execute it.
	if _, err := os.Lstat(filepath.Join(root, "pg-restore-called")); !os.IsNotExist(err) {
		t.Fatal("pg_restore ran")
	}
	raw, _ = json.MarshalIndent(receipts, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "acceptance.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Borg 1.4.3 native extraction refusal and nonzero-after-complete-extraction passed; source %d bytes; payload cleanup, exact replay, external hardlink refusal and internal pair cleanup passed; zero authority/pg_restore/database mutation", fixture.Bytes)

}

func shellQuoteFetch(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func cloudFetchFailureVerifyExtracted(ctx context.Context, root, digest, repository, archive, payloadRoot string) (string, error) {
	plan, err := backup.PlanDirectArchiveRestoreDrill(ctx, backup.DirectArchiveRestoreDrillInput{ArchiveRoot: root, ExpectedManifestSHA256: digest, ExpectedRepository: repository, ExpectedArchive: archive, V2UserSymlinkTargets: map[string]string{filepath.Join(payloadRoot, "Documents", "payload-link"): "payload.txt"}, OperationalTarget: "loom_restore_drill_verification"})
	return plan.Extraction.Status, err
}
