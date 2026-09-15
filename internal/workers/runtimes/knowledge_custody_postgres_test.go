package runtimes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workers"
)

func knowledgeCustodyDatabase(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires a local disposable PostgreSQL administrator")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "" && strings.HasPrefix(u.Query().Get("host"), "/tmp/")) {
		t.Fatal("custody acceptance requires a local disposable database endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "notes_runtime_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		_ = admin.Close()
	})
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestKnowledgeCustodyUnattendedArchiveRestorePostgres(t *testing.T) {
	db, ctx := knowledgeCustodyDatabase(t), t.Context()
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_notes_custody_runtime")
	if err != nil {
		t.Fatal(err)
	}
	var nodeKey string
	if err := db.QueryRow(`UPDATE nodes.nodes SET node_role='main' WHERE node_id=$1 RETURNING node_key`, req.OriginNodeID).Scan(&nodeKey); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	roots := storagearchive.TrustedWorkspaceRoots{BoxRoot: filepath.Join(base, "box"), StorageRoot: filepath.Join(base, "storage")}
	for _, path := range []string{filepath.Join(roots.BoxRoot, "Topics", "first"), filepath.Join(roots.BoxRoot, "Topics", "second"), filepath.Join(roots.StorageRoot, "archive", "topics")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	metadata := json.RawMessage(`{"knowledge_source":{"enabled":true,"root_kind":"box_topics","category":"topics","root_relative_path":"Topics","include":["**/*.md"],"exclude":[]}}`)
	if _, err := db.Exec(`INSERT INTO box.watch_root_registrations
	 (box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,owner_node_key,
	 area_key,local_root_key,backend_root_key,root_relative_path,activation_status,source_kinds_json,metadata)
	 VALUES ($1,$2,$3,$4,$5,$6,'topics','topics','loom_box__topics','Topics','reported','["box_topics"]',$7)`,
		ids.NewBoxWatchRootRegistrationID(), ids.NewBoxID(), roots.BoxRoot, filepath.Join(roots.BoxRoot, ".loom", "box.yaml"), req.OriginNodeID, nodeKey, metadata); err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.NewService(db)
	for _, slug := range []string{"first", "second"} {
		body := "# " + slug + "\nStable custody source.\n"
		path := filepath.Join(roots.BoxRoot, "Topics", slug, "source.md")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		hash, size := sha256.Sum256([]byte(body)), int64(len(body))
		if _, err := catalog.RegisterEntry(ctx, storagecatalog.RegisterEntryInput{
			StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaExternalWatchedRoot,
			OriginNodeID: req.OriginNodeID, OriginNodeKey: nodeKey, WatchedRootKey: "loom_box__topics",
			LogicalPath: slug + "/source.md", OriginalSourcePath: path, SizeBytes: &size,
			ChecksumAlgorithm: "sha256", ChecksumHex: hex.EncodeToString(hash[:]),
			MimeType: "text/markdown", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		}); err != nil {
			t.Fatal(err)
		}
	}
	owner := storagearchive.WorkspaceMoveService{Roots: roots, Catalog: catalog, Journal: catalog, ManifestKeyID: "notes.runtime.test",
		ManifestKey: func(context.Context, string) ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil }}
	var injectedConsumer notesCustodyConsumer
	open := func() workers.Service {
		runtime := NewKnowledgeIndexerRuntime(knowledge.NewService(db)).WithArchiveService(&owner, nodeKey)
		if injectedConsumer != nil {
			runtime.Custody = injectedConsumer
		}
		registry := workers.NewRegistry()
		if err := registry.Register(runtime); err != nil {
			t.Fatal(err)
		}
		return workers.NewService(db, registry, nil)
	}
	worker := open()
	if _, err := worker.SeedBuiltins(ctx, req, nodeKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workers.worker_instances SET config_json='{"batch_size":1,"max_objects_per_run":1}' WHERE worker_key='main.knowledge_indexer'`); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := knowledge.NewService(db).UpdatePipelinePolicy(ctx, knowledge.PipelinePolicyUpdate{PDFOCREnabled: &off, ImageDescriptionsEnabled: &off, EmbeddingsEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	injectedConsumer = custodyConsumerFunc(func(context.Context, string, int) (knowledge.NotesCustodyBatchResult, error) {
		return knowledge.NotesCustodyBatchResult{}, errors.New("private-fixture-path-and-database-cause")
	})
	failed, err := open().RunOnce(ctx, req, "main.knowledge_indexer", workers.RunOnceInput{Reason: "disposable consumer failure", IdempotencyKey: ids.NewIdempotencyID()})
	if err == nil || failed.Run.RunStatus != workers.RunStatusFailed || strings.Contains(string(failed.Run.ErrorJSON), "private-fixture") {
		t.Fatalf("global custody failure truth: %+v %v", failed.Run, err)
	}
	var earlyWrites int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM knowledge.knowledge_objects)+(SELECT count(*) FROM workers.worker_checkpoints)`).Scan(&earlyWrites); err != nil || earlyWrites != 0 {
		t.Fatalf("global custody error ran admission or saved progress: %d %v", earlyWrites, err)
	}
	injectedConsumer = nil
	tick := func() workers.RunOnceResult {
		t.Helper()
		result, err := open().RunOnce(ctx, req, "main.knowledge_indexer", workers.RunOnceInput{Reason: "disposable custody tick", IdempotencyKey: ids.NewIdempotencyID()})
		if err != nil || result.Run.RunStatus != workers.RunStatusSucceeded {
			t.Fatalf("custody runtime: %+v %v", result.Run, err)
		}
		return result
	}
	for range 20 {
		tick()
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT jsonb_build_object(
		 'objects',(SELECT jsonb_agg(jsonb_build_array(knowledge_object_id,notes_source_root_id,source_hash,source_revision) ORDER BY knowledge_object_id) FROM knowledge.knowledge_objects),
		 'versions',(SELECT count(*) FROM knowledge.knowledge_object_versions),
		 'chunks',(SELECT count(*) FROM knowledge.knowledge_chunks),
		 'runs',(SELECT count(*) FROM knowledge.pipeline_runs))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	var objects, chunks int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM knowledge.knowledge_objects),(SELECT count(*) FROM knowledge.knowledge_chunks)`).Scan(&objects, &chunks); err != nil || objects != 2 || chunks != 2 {
		t.Fatalf("initial admission/extraction: %d/%d %v", objects, chunks, err)
	}
	before := snapshot()
	plans := make([]storagearchive.WorkspaceArchivePlan, 0, 2)
	for _, slug := range []string{"first", "second"} {
		plan, err := owner.PlanArchive(ctx, storagearchive.WorkspaceArchivePlanInput{Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_custody_" + slug, Slug: slug, ActorID: req.ActorID, Reason: "disposable runtime custody"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ApplyArchive(ctx, plan, plan.PlanDigest); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	// An authenticated event with conflicting source evidence must not starve
	// the next event. No forged event or substitute archive is introduced.
	var originalHash string
	if err := db.QueryRow(`SELECT source_hash FROM knowledge.knowledge_objects WHERE relative_path='first/source.md'`).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge.knowledge_objects SET source_hash=$1 WHERE relative_path='first/source.md'`, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	first := tick()
	var counters map[string]int64
	if err := json.Unmarshal(first.Run.CountersJSON, &counters); err != nil || counters["custody_findings"] != 1 || counters["custody_events_observed"] != 1 {
		t.Fatalf("typed per-event refusal: %s %v", first.Run.CountersJSON, err)
	}
	var summary struct {
		MoreWork bool                              `json:"more_work"`
		Custody  knowledge.NotesCustodyBatchResult `json:"custody"`
	}
	if err := json.Unmarshal(first.Run.ResultSummaryJSON, &summary); err != nil || !summary.MoreWork || len(summary.Custody.Items) != 1 || summary.Custody.Items[0].Finding != knowledge.NotesCustodySourceConflict {
		t.Fatalf("per-event finding/remaining-work truth: %s %v", first.Run.ResultSummaryJSON, err)
	}
	var saved notesCustodyCheckpoint
	if err := db.QueryRow(`SELECT checkpoint_json FROM workers.worker_checkpoints WHERE checkpoint_key='custody'`).Scan(new(json.RawMessage)); err != nil {
		t.Fatal("custody checkpoint was not durable", err)
	}
	for _, cp := range first.Checkpoints {
		if cp.CheckpointKey == "custody" {
			if err := json.Unmarshal(cp.CheckpointJSON, &saved); err != nil || saved.EventID == "" {
				t.Fatalf("failed-event cursor was not advanced: %+v %v", saved, err)
			}
		}
		if cp.CheckpointKey == "default" {
			var checkpoint struct {
				MoreWork bool `json:"more_work"`
			}
			if err := json.Unmarshal(cp.CheckpointJSON, &checkpoint); err != nil || !checkpoint.MoreWork {
				t.Fatal("default checkpoint lost custody pending-work truth", err)
			}
		}
	}
	if saved.EventID == "" {
		t.Fatal("no custody checkpoint in completed worker result")
	}
	tick()
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_projection_receipts`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("failed first event starved second: %d %v", receipts, err)
	}
	if _, err := db.Exec(`UPDATE knowledge.knowledge_objects SET source_hash=$1 WHERE relative_path='first/source.md'`, originalHash); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		tick()
	}
	if err := db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_projection_receipts`).Scan(&receipts); err != nil || receipts != 2 || snapshot() != before {
		t.Fatalf("cursor wrap/replay changed processing: %d %v", receipts, err)
	}
	restore, err := owner.PlanRestore(ctx, storagearchive.WorkspaceRestorePlanInput{ArchiveOperationID: plans[0].OperationID, ActorID: req.ActorID, Reason: "disposable runtime restore"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyRestore(ctx, restore, restore.PlanDigest); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		tick()
	}
	var active, archived int
	if err := db.QueryRow(`SELECT count(*) FILTER(WHERE source_lifecycle='active'),count(*) FILTER(WHERE source_lifecycle='archived') FROM knowledge.notes_object_custody`).Scan(&active, &archived); err != nil || active != 1 || archived != 1 || snapshot() != before {
		t.Fatalf("restored runtime custody/identity: %d/%d %v", active, archived, err)
	}
}
