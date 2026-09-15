package runtimes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workers"
)

func TestKnowledgeAdmissionUnattendedPostgres(t *testing.T) {
	url := os.Getenv("LOOM_TEST_DB_URL")
	if url == "" {
		t.Skip("requires a dedicated migrated disposable LOOM_TEST_DB_URL")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	var nodeID, nodeKey string
	if err := db.QueryRowContext(ctx, `SELECT node_id,node_key FROM nodes.nodes ORDER BY node_id LIMIT 1`).Scan(&nodeID, &nodeKey); err != nil {
		t.Fatal(err)
	}
	boxRoot := t.TempDir()
	notes := filepath.Join(boxRoot, "Notes")
	if err := os.Mkdir(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	key := "admission_" + ids.NewKnowledgeObjectID()
	registrationID := ids.NewBoxWatchRootRegistrationID()
	if _, err := db.ExecContext(ctx, `INSERT INTO box.watch_root_registrations
		(box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,owner_node_key,
		area_key,local_root_key,backend_root_key,root_relative_path,activation_status,source_kinds_json)
		VALUES ($1,$2,$3,$4,$5,$6,'notes','notes',$7,'Notes','reported','["box_notes"]')`,
		registrationID, ids.NewBoxID(), boxRoot, filepath.Join(boxRoot, ".loom", "box.yaml"), nodeID, nodeKey, key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, _ := sql.Open("pgx", url)
		if cleanup == nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(`DELETE FROM knowledge.notes_source_roots WHERE backend_root_key=$1`, key)
		_, _ = cleanup.Exec(`DELETE FROM storage.storage_entries WHERE watched_root_key IN ($1,$2)`, key, key+"_unrelated")
		_, _ = cleanup.Exec(`DELETE FROM box.watch_root_registrations WHERE box_watch_root_registration_id=$1`, registrationID)
	})
	service := knowledge.NewService(db)
	disabled := false
	if _, err := service.UpdatePipelinePolicy(ctx, knowledge.PipelinePolicyUpdate{
		PDFOCREnabled: &disabled, ImageDescriptionsEnabled: &disabled, EmbeddingsEnabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.NewService(db)
	register := func(name, body, watched, entryID, availability string) storagecatalog.Entry {
		t.Helper()
		if err := os.WriteFile(filepath.Join(notes, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		size := int64(len(body))
		hash := sha256.Sum256([]byte(body))
		entry, err := catalog.RegisterEntry(ctx, storagecatalog.RegisterEntryInput{
			StorageEntryID: entryID, StorageClass: storagecatalog.StorageClassObjectBlob,
			SourceArea: storagecatalog.SourceAreaNotes, OriginNodeID: nodeID, OriginNodeKey: nodeKey,
			WatchedRootKey: watched, LogicalPath: name, OriginalSourcePath: name, SizeBytes: &size,
			ChecksumAlgorithm: "sha256", ChecksumHex: hex.EncodeToString(hash[:]),
			MimeType: "text/markdown", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: availability,
		})
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	const token = "unattendedadmissionneedle"
	first := register("one.md", "# Source\n"+token+"\n", key, "", storagecatalog.AvailabilityStateAvailable)
	register("two.md", "second admission source", key, "", storagecatalog.AvailabilityStateAvailable)
	register("three.md", "third admission source", key, "", storagecatalog.AvailabilityStateAvailable)
	register("unrelated.md", "unregisteredfailedsource", key+"_unrelated", "", storagecatalog.AvailabilityStateFailed)
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_objects WHERE source_path LIKE $1`, notes+"/%").Scan(&before); err != nil || before != 0 {
		t.Fatalf("premature admission: %d, %v", before, err)
	}
	projection := filepath.Join(boxRoot, "generated")
	t.Cleanup(func() {
		_ = filepath.WalkDir(projection, func(p string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
	checkpoints := map[string]workers.WorkerCheckpoint{}
	tick := func() workers.RunResult {
		t.Helper()
		// Reconstruct the runtime every tick, carrying only durable checkpoints.
		runtime := NewKnowledgeIndexerRuntime(knowledge.NewService(db)).WithProjectionRoot(projection)
		result, err := runtime.RunOnce(ctx, workers.RunContext{
			Instance: workers.WorkerInstance{ConfigJSON: json.RawMessage(`{"batch_size":2,"max_objects_per_run":2}`), TickPolicyJSON: json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`)},
			Run:      workers.WorkerRun{WorkerRunID: ids.NewWorkerRunID()}, Checkpoints: checkpoints,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Counters["catalog_observed"] > 2 || result.Counters["synced_observed"] > 2 {
			t.Fatalf("unbounded admission: %v", result.Counters)
		}
		for _, cp := range result.CheckpointUpdates {
			checkpoints[cp.Key] = workers.WorkerCheckpoint{CheckpointJSON: cp.Value, SchemaVersion: cp.SchemaVersion}
		}
		return result
	}
	for range 24 {
		tick()
	}
	results, err := service.SearchNotes(ctx, knowledge.NotesSearchInput{Query: token, Mode: knowledge.NotesSearchModeLexical, SourceNodeKey: nodeKey})
	if err != nil || len(results.Results) != 1 || results.Results[0].KnowledgeChunkID == "" {
		t.Fatalf("native lexical result: %#v %v", results, err)
	}
	objectID, versionID, chunkID := results.Results[0].KnowledgeObjectID, results.Results[0].KnowledgeObjectVersionID, results.Results[0].KnowledgeChunkID
	projected := filepath.Join(projection, "nodes", nodeKey, "Notes", "one.md")
	info, err := os.Stat(projected)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("generated mode: %o", info.Mode())
	}
	var runCount int
	if err := db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1`, objectID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		tick()
	}
	again, err := service.SearchNotes(ctx, knowledge.NotesSearchInput{Query: token, Mode: knowledge.NotesSearchModeLexical, SourceNodeKey: nodeKey})
	if err != nil || len(again.Results) != 1 || again.Results[0].KnowledgeChunkID != chunkID || again.Results[0].KnowledgeObjectVersionID != versionID {
		t.Fatalf("replay changed retrieval identity: %#v %v", again, err)
	}
	afterInfo, err := os.Stat(projected)
	if err != nil || !os.SameFile(info, afterInfo) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatalf("unchanged file was recopied: %v", err)
	}
	var afterRuns, unrelated int
	if err := db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1`, objectID).Scan(&afterRuns); err != nil || runCount != afterRuns {
		t.Fatalf("unchanged replay created runs: %d/%d %v", runCount, afterRuns, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_objects WHERE relative_path='unrelated.md' AND source_node_key=$1`, nodeKey).Scan(&unrelated); err != nil || unrelated != 0 {
		t.Fatalf("unregistered failed upload admitted: %d %v", unrelated, err)
	}
	register("one.md", "# Changed\nchangedadmissionneedle\n", key, first.StorageEntryID, storagecatalog.AvailabilityStateAvailable)
	for range 16 {
		tick()
	}
	changed, err := service.SearchNotes(ctx, knowledge.NotesSearchInput{Query: "changedadmissionneedle", Mode: knowledge.NotesSearchModeLexical, SourceNodeKey: nodeKey})
	if err != nil || len(changed.Results) != 1 || changed.Results[0].KnowledgeObjectID != objectID || changed.Results[0].KnowledgeObjectVersionID == versionID {
		t.Fatalf("content version not advanced: %#v %v", changed, err)
	}
	old, err := service.SearchNotes(ctx, knowledge.NotesSearchInput{Query: token, Mode: knowledge.NotesSearchModeLexical, SourceNodeKey: nodeKey})
	if err != nil || len(old.Results) != 0 {
		t.Fatalf("obsolete body remains searchable: %#v %v", old, err)
	}
	t.Logf("unattended admission/search/projection passed; original %s version %s; no manual knowledge reconciliation", objectID, versionID)
}
