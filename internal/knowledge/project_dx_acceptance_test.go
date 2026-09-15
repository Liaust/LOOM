package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	dx "loom.local/loom/tests/acceptance/project_dx"
)

// Only authoritative object/sync observations are seeded. All derived objects,
// versions and chunks are produced by the actual admission/coordinator services.
func dxObserve(t *testing.T, s *Service, root SourceRoot, object KnowledgeObject, body string) {
	t.Helper()
	entries, e := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), root.NotesSourceRootID)
	if e != nil {
		t.Fatal(e)
	}
	var old SyncedObjectEntry
	// Keep the established observation's object and source path, advancing only
	// its blob/version. This is not a direct write of a derived Notes answer.
	var found bool
	for _, entry := range entries {
		var id string
		if e = s.store.db.QueryRow(`SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&id); e != nil {
			t.Fatal(e)
		}
		if entry.ObjectID == id {
			old = entry
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing authoritative observation")
	}
	var number int
	if e = s.store.db.QueryRow(`SELECT max(version_number)+1 FROM objects.object_versions WHERE object_id=$1`, old.ObjectID).Scan(&number); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "observation.md")
	if e = os.WriteFile(file, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	// Archive's source bytes must follow the authoritative observation as well.
	relative := strings.TrimPrefix(old.SourcePath, "watched-root://"+root.BackendRootKey+"/")
	if e = os.WriteFile(filepath.Join(root.SourcePath, relative), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	blob, version, hash := ids.NewBlobID(), ids.NewObjectVersionID(), hashArtifactValue(body)
	for _, stmt := range []struct {
		q string
		a []any
	}{
		{`INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,status) VALUES ($1,'sha256',$2,$3,$4,$5,'verified')`, []any{blob, strings.TrimPrefix(hash, "sha256:"), hash, len(body), file}},
		{`INSERT INTO objects.object_versions(object_version_id,object_id,version_number,blob_id,content_hash,source_node_id,source_path,size_bytes,mime_type,status,metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'text/markdown','active','{"file_class":"markdown"}')`, []any{version, old.ObjectID, number, blob, hash, old.SourceNodeID, old.SourcePath, len(body)}},
		{`UPDATE files.file_metadata SET latest_version_id=$1 WHERE object_id=$2`, []any{version, old.ObjectID}},
		{`UPDATE objects.object_versions SET status='superseded' WHERE object_version_id=$1`, []any{old.ObjectVersionID}},
		{`UPDATE sync.replicas SET replicated_id=$1,storage_ref=$2 WHERE replica_id=$3`, []any{version, hash, old.ReplicaID}},
	} {
		if _, e = s.store.db.Exec(stmt.q, stmt.a...); e != nil {
			t.Fatal(e)
		}
	}
}
func TestProjectDXNotesFixture(t *testing.T) {
	f := dx.Open(t, "C")
	archive := dxNotesArchiveFixture(t)
	s := archive.s
	roots, e := s.store.ListSourceRoots(t.Context(), SourceRootFilter{})
	if e != nil {
		t.Fatal(e)
	}
	selected := map[string]KnowledgeObject{}
	rootByID := map[string]SourceRoot{}
	for _, r := range roots {
		rootByID[r.NotesSourceRootID] = r
	}
	for _, o := range archive.objects {
		switch o.SourceCategory {
		case "notes":
			selected["A"] = o
		case "library":
			selected["C"] = o
		case "topics":
			if strings.HasSuffix(o.SourcePath, "/topic-one/source.md") {
				selected["B"] = o
			}
		}
	}
	if len(selected) != 3 {
		t.Fatalf("three sources required: %+v", archive.objects)
	}

	archive.plan, e = archive.p.Workspace.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_notes_one", Slug: "topic-one", ActorID: archive.actor, Reason: "disposable DX archived source"})
	if e != nil {
		t.Fatal(e)
	}
	archive.archive(t)
	if _, e = archive.p.ProjectOperation(t.Context(), archive.plan.OperationID); e != nil {
		t.Fatal(e)
	}
	tuples := map[string]NotesPassageInput{}
	oracle := map[string]NotesPassage{}
	for key, o := range selected {
		lifecycle := SourceLifecycleFilterActive
		if key == "B" {
			lifecycle = SourceLifecycleFilterArchived
		}
		tuples[key] = notesFollowupExpected(t, s, o.KnowledgeObjectID, lifecycle)
		p, e := s.GetNotesPassage(t.Context(), tuples[key])
		if e != nil {
			t.Fatal(e)
		}
		source := key
		if key == "A" {
			source = "A_v1"
		}
		if p.SourceHash != hashArtifactValue(dx.Source(t, "C", source)) || p.Text != strings.TrimSpace(dx.Source(t, "C", source)) {
			t.Fatal("raw corpus hash/text did not survive actual extraction")
		}
		oracle[key] = p
	}
	f.Write(t, "oracle-before.json", oracle)
	before := dx.Snapshot(t, s.store.db)
	var database string
	if e = s.store.db.QueryRow(`SELECT current_database()`).Scan(&database); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(os.Getenv("LOOM_TEST_DB_URL"))
	u.Path = "/" + database
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	child := exec.Command(os.Getenv("LOOM_DX_HTTP_TEST"), "-test.run=^TestProjectDXNotesHTTPHelper$", "-test.v", "-test.timeout=12m")
	child.Env = append(os.Environ(), "LOOM_DX_NOTES_HTTP=1", "LOOM_DX_NOTES_DB="+u.String(), "LOOM_DX_SERVE=1")
	log, e := os.Create(filepath.Join(f.Evidence, "http-helper.log"))
	if e != nil {
		t.Fatal(e)
	}
	child.Stdout = log
	child.Stderr = log
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(f.Root, "C.stop"), nil, 0600)
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(5 * time.Second):
			_ = child.Process.Kill()
			<-done
			t.Error("Notes HTTP helper needed forced shutdown")
		}
		_ = log.Close()
	})
	wait := func(path string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, e = os.Stat(path); e == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", path)
	}
	wait(f.Socket)
	barrier := func() {
		// HTTP helper has already durably logged the unmodified real search result.
		wait(filepath.Join(f.Root, "C.query"))
		raw, e := os.ReadFile(filepath.Join(f.Root, "C.query"))
		if e != nil {
			t.Fatal(e)
		}
		var response struct {
			Data NotesSearchResultSet `json:"data"`
		}
		if e = json.Unmarshal(raw, &response); e != nil {
			t.Fatal(e)
		}
		if len(response.Data.Results) != 3 {
			t.Fatalf("real query did not return three matches: %s", raw)
		}
		for _, r := range response.Data.Results {
			matched := false
			for _, tuple := range tuples {
				if r.PassageFollowup != nil && *r.PassageFollowup == tuple {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("query tuple outside independently derived oracle: %+v", r)
			}
		}
		if dx.Snapshot(t, s.store.db) != before {
			t.Fatal("query changed stable phase database")
		}
		dxObserve(t, s, rootByID[selected["A"].NotesSourceRootID], selected["A"], dx.Source(t, "C", "A_v2"))
		reconcileBoxSyncedFixture(t, s, roots)
		advanceBoxSyncedFixture(t, s)
		if _, e = s.store.db.Exec(`UPDATE objects.object_versions SET source_path=source_path||'.moved' WHERE object_version_id=(SELECT metadata->'synced_object'->>'object_version_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, selected["C"].KnowledgeObjectID); e != nil {
			t.Fatal(e)
		}
		p, e := s.GetNotesPassage(t.Context(), tuples["A"])
		if e != nil || !p.Historical || p.Text != oracle["A"].Text {
			t.Fatalf("historical V1 oracle: %+v %v", p, e)
		}
		p, e = s.GetNotesPassage(t.Context(), tuples["B"])
		if e != nil || p.Text != oracle["B"].Text {
			t.Fatalf("archived oracle: %+v %v", p, e)
		}
		if _, e = s.GetNotesPassage(t.Context(), tuples["C"]); !errors.Is(e, sql.ErrNoRows) {
			t.Fatalf("current access refusal: %v", e)
		}
		after := dx.Snapshot(t, s.store.db)
		f.Write(t, "barrier.json", map[string]any{"before": before, "after": after, "actual_query": json.RawMessage(raw), "A_v1": oracle["A"], "A_v2_hash": hashArtifactValue(dx.Source(t, "C", "A_v2")), "C_refused": true})
		before = after
		if e = os.WriteFile(filepath.Join(f.Root, "C.barrier.done"), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if os.Getenv("LOOM_DX_SERVE") == "1" {
		f.Write(t, "seed-ready.json", map[string]bool{"ready": true})
		barrier()
		f.Hold(t, nil)
	} else {
		result := make(chan error, 1)
		go func() {
			_, err := f.Command(t, "notes", "search", "harbor-lamp", "--mode", "lexical", "--source-lifecycle", "all")
			result <- err
		}()
		barrier()
		if e = <-result; e != nil {
			t.Fatal(e)
		}
		for key, p := range tuples {
			body, err := f.Command(t, "notes", "passage", "get", p.KnowledgeChunkID, "--object", p.KnowledgeObjectID, "--version", p.KnowledgeObjectVersionID, "--source-hash", p.SourceHash, "--source-lifecycle", string(p.SourceLifecycle))
			if key == "C" {
				if err == nil {
					t.Fatalf("CLI failed to refuse: %s", body)
				}
			} else {
				if err != nil {
					t.Fatalf("CLI exact %s: %s %v", key, body, err)
				}
				var r NotesPassage
				if e = json.Unmarshal(body, &r); e != nil || r.Text != oracle[key].Text {
					t.Fatalf("exact CLI text differs: %s %v", body, e)
				}
			}
		}
	}
	after := dx.Snapshot(t, s.store.db)
	if after != before {
		t.Fatal("exact follow-up phase mutated database")
	}
	f.Write(t, "stable-read.json", map[string]string{"before": before, "after": after})
	t.Log(fmt.Sprintf("PASS actual Notes search barrier, historical V1, archived exact read, current refusal; %d independently pinned tuples", len(tuples)))
}

// Scoped three-source archive fixture; no extra indexed neighbours.
func dxNotesArchiveFixture(t *testing.T) *notesArchivePostgresFixture {
	t.Helper()
	s, roots := boxSyncedFixture(t)
	// Replace only unadmitted authoritative seed bytes before deriving V1.
	for i, key := range []string{"A_v1", "B", "C"} {
		body := dx.Source(t, "C", key)
		hash := hashArtifactValue(body)
		path := filepath.Join(roots[i].SourcePath, "source.md")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		uri := "watched-root://" + roots[i].BackendRootKey + "/source.md"
		if _, err := s.store.db.Exec(`UPDATE files.blobs SET hash_hex=$1, hash_uri=$2, size_bytes=$3 WHERE storage_path=$4`, strings.TrimPrefix(hash, "sha256:"), hash, len(body), path); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET content_hash=$1,size_bytes=$2 WHERE source_path=$3`, hash, len(body), uri); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE sync.replicas SET storage_ref=$1 WHERE replicated_id IN (SELECT object_version_id FROM objects.object_versions WHERE source_path=$2)`, hash, uri); err != nil {
			t.Fatal(err)
		}
	}
	base := t.TempDir()
	workspaceRoots := storagearchive.TrustedWorkspaceRoots{BoxRoot: filepath.Join(base, "box"), StorageRoot: filepath.Join(base, "storage")}
	for _, path := range []string{filepath.Join(workspaceRoots.BoxRoot, "Topics", "topic-one"), filepath.Join(workspaceRoots.BoxRoot, "Topics", "neighbour"), filepath.Join(workspaceRoots.StorageRoot, "archive", "topics")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root := roots[1]
	body, err := os.ReadFile(filepath.Join(root.SourcePath, "source.md"))
	if err != nil {
		t.Fatal(err)
	}
	root.SourcePath = filepath.Join(workspaceRoots.BoxRoot, "Topics")
	root.RootRelativePath = "Topics"
	metadata := jsonObject(root.Metadata)
	metadata["registration_metadata"].(map[string]any)["knowledge_source"].(map[string]any)["root_relative_path"] = "Topics"
	root.Metadata = mustJSON(t, metadata)
	root, err = s.store.UpsertSourceRoot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	roots[1] = root
	path := filepath.Join(root.SourcePath, "topic-one", "source.md")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	oldURI, newURI := "watched-root://"+root.BackendRootKey+"/source.md", "watched-root://"+root.BackendRootKey+"/topic-one/source.md"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE objects.object_versions SET source_path=$2 WHERE source_path=$1`, []any{oldURI, newURI}},
		{`UPDATE files.file_metadata SET source_path=$2 WHERE source_path=$1`, []any{oldURI, newURI}},
		{`UPDATE files.blobs SET storage_path=$1 WHERE blob_id IN (SELECT blob_id FROM objects.object_versions WHERE source_path=$2)`, []any{path, newURI}},
	} {
		if _, err := s.store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	objects := reconcileBoxSyncedFixture(t, s, roots)
	if len(objects) != 3 || advanceBoxSyncedFixture(t, s) == 0 {
		t.Fatalf("initial admission: %d objects", len(objects))
	}
	actor := ids.NewActorID()
	if _, err := s.store.db.Exec(`INSERT INTO identity.actors(actor_id,actor_key,actor_kind,display_name,status) VALUES ($1,$1,'human','Notes archive acceptance','active')`, actor); err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.NewService(s.store.db)
	owner := storagearchive.WorkspaceMoveService{Roots: workspaceRoots, Catalog: catalog, Journal: catalog, ManifestKeyID: "notes.archive.test",
		ManifestKey: func(context.Context, string) ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil }}
	plan, err := owner.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_notes_one", Slug: "topic-one", ActorID: actor, Reason: "disposable Notes custody proof"})
	if err != nil {
		t.Fatal(err)
	}
	f := &notesArchivePostgresFixture{s: s, p: NotesArchiveProjector{DB: s.store.db, Workspace: owner, NodeKey: root.NodeKey}, root: root, objects: objects, plan: plan, actor: actor}
	f.before = f.processingSnapshot(t)
	return f
}
