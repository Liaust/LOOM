package knowledge

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/storagecatalog"
)

// Seed only authoritative sync observations, never derived Knowledge rows.
// The separate smoke exercises these observations through the real owner agent.
func boxSyncedFixture(t *testing.T) (*Service, []SourceRoot) {
	t.Helper()
	db, dbURL := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), dbURL, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	nodeID := ids.NewNodeID()
	if _, err := db.Exec(`INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES ($1,'sync-owner','Sync owner','server','main','native','active')`, nodeID); err != nil {
		t.Fatal(err)
	}
	s := NewService(db)
	setPipelinePolicyForTest(t, db, PipelinePolicy{})
	roots := []SourceRoot{}
	for _, kind := range []string{RootKindBoxNotes, RootKindBoxTopics, RootKindBoxLibrary} {
		area := strings.TrimPrefix(kind, "box_")
		root, err := s.PrepareSourceRoot(SourceRoot{RootKind: kind, NodeID: &nodeID, NodeKey: "sync-owner", BackendRootKey: "loom_box__" + area, RootRelativePath: area, SourcePath: t.TempDir(), Metadata: mustJSON(t, map[string]any{"box_id": "sync-fixture", "registration_metadata": map[string]any{"knowledge_source": map[string]any{"enabled": true, "root_kind": kind, "source_category": area, "root_relative_path": area, "include": []string{"**/*.md"}}}})})
		if err != nil {
			t.Fatal(err)
		}
		root, err = s.store.UpsertSourceRoot(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		scopeID := ids.NewScopeID()
		if _, err := db.Exec(`INSERT INTO scopes.scopes(scope_id,scope_type,scope_key,slug,display_name,home_node_id,status,metadata) VALUES ($1,'box_area',$2,$3,$3,$4,'active',$5)`, scopeID, "loom_box:sync-fixture:"+area, area, nodeID, mustJSON(t, map[string]any{"box_id": "sync-fixture", "box_area": area, "owner_node_id": nodeID, "owner_node_key": "sync-owner"})); err != nil {
			t.Fatal(err)
		}
		body := "# " + area + "\nA source about the unique cobalt observatory.\n"
		file := filepath.Join(root.SourcePath, "source.md")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		objectID, versionID, blobID := ids.NewObjectID(), ids.NewObjectVersionID(), ids.NewBlobID()
		hash := hashArtifactValue(body)
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,mime_type,status) VALUES ($1,'sha256',$2,$3,$4,$5,'text/markdown','verified')`, []any{blobID, strings.TrimPrefix(hash, "sha256:"), hash, len(body), file}},
			{`INSERT INTO objects.objects(object_id,object_type,name,home_scope_id,state_class,status) VALUES ($1,'file','source.md',$2,'canonical','active')`, []any{objectID, scopeID}},
			{`INSERT INTO objects.object_versions(object_version_id,object_id,version_number,blob_id,content_hash,source_node_id,source_path,size_bytes,mime_type,status,metadata) VALUES ($1,$2,1,$3,$4,$5,$6,$7,'text/markdown','active','{"file_class":"markdown"}')`, []any{versionID, objectID, blobID, hash, nodeID, "watched-root://" + root.BackendRootKey + "/source.md", len(body)}},
			{`INSERT INTO files.file_metadata(object_id,logical_name,mime_type,source_node_id,source_path,latest_version_id,index_policy) VALUES ($1,'source.md','text/markdown',$2,$3,$4,'text_later')`, []any{objectID, nodeID, "watched-root://" + root.BackendRootKey + "/source.md", versionID}},
			{`INSERT INTO objects.object_scope_links(object_scope_link_id,object_id,scope_id,relationship_type) VALUES ($1,$2,$3,'primary'),($4,$2,$3,'relevant')`, []any{ids.NewObjectScopeLinkID(), objectID, scopeID, ids.NewObjectScopeLinkID()}},
			{`INSERT INTO sync.replicas(replica_id,replicated_kind,replicated_id,source_node_id,replica_node_id,replica_mode,freshness_state,storage_ref) VALUES ($1,'object_version',$2,$3,$3,'main_snapshot','fresh',$4)`, []any{ids.NewReplicaID(), versionID, nodeID, hash}},
		}
		for _, stmt := range statements {
			if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
				t.Fatal(err)
			}
		}
		roots = append(roots, root)
	}
	return s, roots
}

func reconcileBoxSyncedFixture(t *testing.T, s *Service, roots []SourceRoot) []KnowledgeObject {
	t.Helper()
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{SourceRoots: roots, SyncedObjects: entries})
	if err != nil {
		t.Fatal(err)
	}
	return result.KnowledgeObjects
}

func advanceBoxSyncedFixture(t *testing.T, s *Service) int64 {
	t.Helper()
	var completed int64
	for range 30 {
		result, err := s.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "box-sync-fixture", Limit: 20, Now: time.Now().UTC()})
		if err != nil || result.Failed != 0 {
			t.Fatalf("synced pipeline: %+v %v", result, err)
		}
		completed += result.Completed
		if result.Claimed == 0 {
			return completed
		}
	}
	t.Fatal("pipeline did not converge")
	return 0
}

func TestBoxSyncedAdmissionPaginationAndReplayPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	var after [3]string
	seen := map[string]bool{}
	for page := 0; page < 5; page++ {
		entries, err := s.store.listSyncedNotesPage(t.Context(), "", after, 2)
		if err != nil || len(entries) > 2 {
			t.Fatalf("page: %+v %v", entries, err)
		}
		for _, e := range entries {
			if seen[e.NotesSourceRootID] {
				t.Fatal("duplicate scope relationships duplicated an admission")
			}
			seen[e.NotesSourceRootID] = true
			after = [3]string{e.NotesSourceRootID, e.ObjectVersionID, e.ReplicaID}
		}
		if len(entries) < 2 {
			break
		}
	}
	if len(seen) != 3 {
		t.Fatalf("admitted %d Box scopes", len(seen))
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	if len(objects) != 3 || advanceBoxSyncedFixture(t, s) == 0 {
		t.Fatal("no synced source pipeline work")
	}
	replay := reconcileBoxSyncedFixture(t, s, roots)
	if len(replay) != 3 || advanceBoxSyncedFixture(t, s) != 0 {
		t.Fatal("unchanged source repeated extraction")
	}
	for i, object := range objects {
		if object.KnowledgeObjectID != replay[i].KnowledgeObjectID || replay[i].ProjectID != nil {
			t.Fatal("Box replay changed identity or invented a project")
		}
	}
	// Custody arriving later must reuse the existing source/pipeline identity.
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		var e SyncedObjectEntry
		for _, candidate := range entries {
			if candidate.NotesSourceRootID == root.NotesSourceRootID {
				e = candidate
			}
		}
		area := storagecatalog.SourceAreaExternalWatchedRoot
		if root.RootKind == RootKindBoxNotes {
			area = storagecatalog.SourceAreaNotes
		}
		_, err := storagecatalog.NewService(s.store.db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: area, OriginNodeID: e.SourceNodeID, OriginNodeKey: e.SourceNodeKey, WatchedRootKey: root.BackendRootKey, LogicalPath: "source.md", OriginalSourcePath: "source.md", FileClass: e.FileClass, MimeType: e.MimeType, SizeBytes: e.SizeBytes, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(e.SourceHash, "sha256:"), AvailabilityState: storagecatalog.AvailabilityStateAvailable})
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		catalog, err := s.store.ListStorageEntriesForNotesRoots(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{SourceRoots: roots, StorageEntries: catalog, SyncedObjects: entries}); err != nil {
			t.Fatal(err)
		}
		if advanceBoxSyncedFixture(t, s) != 0 {
			t.Fatal("custody coexistence repeated extraction")
		}
	}
	var count int
	if err := s.store.db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_objects`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("logical source count %d %v", count, err)
	}
	if err := s.store.db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("pipeline count %d %v", count, err)
	}
}

func TestBoxSyncedCurrentSourceSelectionPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	db, ctx := s.store.db, t.Context()
	oldObjects := reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[0].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("initial source: %d %v", len(entries), err)
	}
	old := entries[0]
	var oldKnowledge KnowledgeObject
	for _, object := range oldObjects {
		if object.NotesSourceRootID == roots[0].NotesSourceRootID {
			oldKnowledge = object
		}
	}
	// Separate retained Objects identities, each with its own latest pointer,
	// claim the same source path. Replica refresh and file mtime are not order.
	body := "# Current source\nThe surviving quartz telescope decision.\n"
	file := filepath.Join(t.TempDir(), "current.md")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	newObject, newVersion, blob := ids.NewObjectID(), ids.NewObjectVersionID(), ids.NewBlobID()
	hash := hashArtifactValue(body)
	accepted := time.Now().UTC().Add(-time.Hour)
	replicaNode := ids.NewNodeID()
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE objects.object_versions SET created_at=$2,synced_at=now() WHERE object_version_id=$1`, []any{old.ObjectVersionID, accepted.Add(-time.Hour)}},
		{`INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,mime_type,status) VALUES ($1,'sha256',$2,$3,$4,$5,'text/markdown','verified')`, []any{blob, strings.TrimPrefix(hash, "sha256:"), hash, len(body), file}},
		{`INSERT INTO objects.objects(object_id,object_type,name,home_scope_id,state_class,status) SELECT $1,object_type,name,home_scope_id,state_class,status FROM objects.objects WHERE object_id=$2`, []any{newObject, old.ObjectID}},
		{`INSERT INTO objects.object_versions(object_version_id,object_id,version_number,blob_id,content_hash,source_node_id,source_path,size_bytes,mime_type,status,metadata,created_at) SELECT $1,$2,1,$3,$4,source_node_id,source_path,$5,mime_type,status,metadata,$6 FROM objects.object_versions WHERE object_version_id=$7`, []any{newVersion, newObject, blob, hash, len(body), accepted, old.ObjectVersionID}},
		{`INSERT INTO files.file_metadata(object_id,logical_name,mime_type,source_node_id,source_path,latest_version_id,index_policy,source_mtime) SELECT $1,logical_name,mime_type,source_node_id,source_path,$2,index_policy,$3 FROM files.file_metadata WHERE object_id=$4`, []any{newObject, newVersion, accepted.Add(-24 * time.Hour), old.ObjectID}},
		{`INSERT INTO objects.object_scope_links(object_scope_link_id,object_id,scope_id,relationship_type) SELECT $1,$2,home_scope_id,'primary' FROM objects.objects WHERE object_id=$2`, []any{ids.NewObjectScopeLinkID(), newObject}},
		{`INSERT INTO nodes.nodes(node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES ($1,'replica-peer','Replica peer','server','main','native','active')`, []any{replicaNode}},
		{`INSERT INTO sync.replicas(replica_id,replicated_kind,replicated_id,source_node_id,replica_node_id,replica_mode,freshness_state,storage_ref) VALUES ($1,'object_version',$2,$3,$3,'main_snapshot','fresh',$4),($5,'object_version',$2,$3,$6,'main_snapshot','fresh',$4)`, []any{ids.NewReplicaID(), newVersion, old.SourceNodeID, hash, ids.NewReplicaID(), replicaNode}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	assertSelection := func(want int) {
		t.Helper()
		selected, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[0].NotesSourceRootID)
		if err != nil || len(selected) != want {
			t.Fatalf("current source selection returned %d, want %d: %v", len(selected), want, err)
		}
		if want == 1 && (selected[0].ObjectID != newObject || selected[0].ObjectVersionID != newVersion) {
			t.Fatal("retained older source displaced current version")
		}
	}
	assertSelection(1)
	var preferredReplica string
	if _, err := s.readSyncedObjectSource(ctx, oldKnowledge, 1024); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical source remained readable after newer acceptance: %v", err)
	}
	if err := db.QueryRow(`SELECT min(replica_id) FROM sync.replicas WHERE replicated_id=$1`, newVersion).Scan(&preferredReplica); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		selected, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[0].NotesSourceRootID)
		if err != nil || len(selected) != 1 || selected[0].ReplicaID != preferredReplica {
			t.Fatalf("replica selection is not deterministic: %+v %v", selected, err)
		}
	}
	// Equal acceptance times use the version ID, not refresh order.
	if _, err := db.Exec(`UPDATE objects.object_versions SET created_at=$2 WHERE object_version_id=$1`, old.ObjectVersionID, accepted); err != nil {
		t.Fatal(err)
	}
	if newVersion <= old.ObjectVersionID {
		t.Fatal("fixture requires later generated version ID")
	}
	assertSelection(1)
	if _, err := db.Exec(`UPDATE objects.object_versions SET created_at=$2 WHERE object_version_id=$1`, old.ObjectVersionID, accepted.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []struct {
		name, change, restore   string
		changeArgs, restoreArgs []any
		want                    int
	}{
		{"owner", `UPDATE objects.object_versions SET source_node_id=$2 WHERE object_version_id=$1`, `UPDATE objects.object_versions SET source_node_id=$2 WHERE object_version_id=$1`, []any{newVersion, replicaNode}, []any{newVersion, old.SourceNodeID}, 1},
		{"path", `UPDATE objects.object_versions SET source_path=source_path||'.other.md' WHERE object_version_id=$1`, `UPDATE objects.object_versions SET source_path=$2 WHERE object_version_id=$1`, []any{newVersion}, []any{newVersion, old.SourcePath}, 2},
		{"scope", `UPDATE objects.object_scope_links SET relevance_status='inactive' WHERE object_id=$1`, `UPDATE objects.object_scope_links SET relevance_status='active' WHERE object_id=$1`, []any{newObject}, []any{newObject}, 1},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			if _, err := db.Exec(boundary.change, boundary.changeArgs...); err != nil {
				t.Fatal(err)
			}
			selected, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[0].NotesSourceRootID)
			foundOld := false
			for _, entry := range selected {
				foundOld = foundOld || entry.ObjectID == old.ObjectID
			}
			if err != nil || len(selected) != boundary.want || !foundOld {
				t.Fatalf("unrelated source displaced original: %+v %v", selected, err)
			}
			if _, err := db.Exec(boundary.restore, boundary.restoreArgs...); err != nil {
				t.Fatal(err)
			}
			assertSelection(1)
		})
	}
	var visible bool
	if err := db.QueryRow(`SELECT `+notesKnowledgeVisibilitySQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, oldKnowledge.KnowledgeObjectID).Scan(&visible); err != nil || visible {
		t.Fatalf("superseded source remains readable before admission: %t %v", visible, err)
	}
	// Ranking is before page boundaries: the older source cannot win a later
	// one-item page. Identical relative paths in other owned roots stay separate.
	var after [3]string
	seen := map[string]bool{}
	for range 6 {
		page, err := s.store.listSyncedNotesPage(ctx, "", after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		entry := page[0]
		if seen[entry.NotesSourceRootID] || entry.ObjectID == old.ObjectID {
			t.Fatal("pagination returned duplicate or historical source")
		}
		seen[entry.NotesSourceRootID] = true
		after = [3]string{entry.NotesSourceRootID, entry.ObjectVersionID, entry.ReplicaID}
	}
	if len(seen) != 3 {
		t.Fatalf("lost independent roots: %d", len(seen))
	}
	for name, change := range map[string][2]string{
		"private":     {`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=$1`, `UPDATE files.file_metadata SET index_policy='text_later' WHERE object_id=$1`},
		"disabled":    {`UPDATE objects.objects SET status='archived' WHERE object_id=$1`, `UPDATE objects.objects SET status='active' WHERE object_id=$1`},
		"version":     {`UPDATE objects.object_versions SET status='superseded' WHERE object_id=$1`, `UPDATE objects.object_versions SET status='active' WHERE object_id=$1`},
		"unavailable": {`UPDATE sync.replicas SET freshness_state='stale' WHERE replicated_id=(SELECT latest_version_id FROM files.file_metadata WHERE object_id=$1)`, `UPDATE sync.replicas SET freshness_state='fresh' WHERE replicated_id=(SELECT latest_version_id FROM files.file_metadata WHERE object_id=$1)`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(change[0], newObject); err != nil {
				t.Fatal(err)
			}
			assertSelection(0)
			if _, err := db.Exec(change[1], newObject); err != nil {
				t.Fatal(err)
			}
			assertSelection(1)
		})
	}
	// Drive the actual bounded admission page and one coordinator step each
	// tick, not a manual metadata edit or a forced pipeline retry.
	var cursor AdmissionCursor
	for range 14 {
		result, err := s.AdmitRegisteredNotesOnce(ctx, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		cursor = result.Cursor
		step, err := s.RunPipelineCoordinatorOnce(ctx, PipelineCoordinatorRunInput{WorkerRunID: "current-source-tick", Limit: 10, Now: time.Now().UTC()})
		if err != nil || step.Failed != 0 {
			t.Fatalf("ordinary tick: %+v %v", step, err)
		}
	}
	current, err := s.store.GetKnowledgeObject(ctx, oldKnowledge.KnowledgeObjectID)
	if err != nil || current.SourceHash != hash || current.ProcessingState != ProcessingStateIndexed {
		t.Fatalf("current source did not converge: %s %s %v", current.SourceHash, current.ProcessingState, err)
	}
	assertSyncedSearchBody(t, s, current, "quartz telescope")
	if result, err := s.SearchNotes(ctx, NotesSearchInput{Query: "cobalt observatory", NotesSourceRootID: current.NotesSourceRootID, Mode: NotesSearchModeLexical}); err != nil || len(result.Results) != 0 {
		t.Fatalf("old local bytes were indexed under new source hash: %d %v", len(result.Results), err)
	}
	var runs, versions int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1),(SELECT count(*) FROM knowledge.knowledge_object_versions WHERE knowledge_object_id=$1)`, current.KnowledgeObjectID).Scan(&runs, &versions); err != nil || runs != 2 || versions != 2 {
		t.Fatalf("unchanged ticks created work: runs=%d versions=%d %v", runs, versions, err)
	}
	if work := advanceBoxSyncedFixture(t, s); work != 0 {
		t.Fatalf("unchanged source still has %d stages", work)
	}
}

func TestBoxSyncedAdmissionVisibilityRefusalsPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	if len(objects) != 3 {
		t.Fatalf("objects: %+v", objects)
	}
	for name, change := range map[string][2]string{
		"scope-key":        {`UPDATE scopes.scopes SET scope_key=scope_key||':wrong'`, `UPDATE scopes.scopes SET scope_key=replace(scope_key,':wrong','')`},
		"object-status":    {`UPDATE objects.objects SET status='archived'`, `UPDATE objects.objects SET status='active'`},
		"object-type":      {`UPDATE objects.objects SET object_type='artifact'`, `UPDATE objects.objects SET object_type='file'`},
		"scope-status":     {`UPDATE scopes.scopes SET status='disabled'`, `UPDATE scopes.scopes SET status='active'`},
		"scope-owner":      {`UPDATE scopes.scopes SET home_node_id=NULL`, `UPDATE scopes.scopes SET home_node_id=(SELECT node_id FROM nodes.nodes)`},
		"scope-box":        {`UPDATE scopes.scopes SET metadata=jsonb_set(metadata,'{box_id}','"wrong"')`, `UPDATE scopes.scopes SET metadata=jsonb_set(metadata,'{box_id}','"sync-fixture"')`},
		"scope-owner-key":  {`UPDATE scopes.scopes SET metadata=jsonb_set(metadata,'{owner_node_key}','"wrong"')`, `UPDATE scopes.scopes SET metadata=jsonb_set(metadata,'{owner_node_key}','"sync-owner"')`},
		"scope-link":       {`UPDATE objects.object_scope_links SET relevance_status='inactive'`, `UPDATE objects.object_scope_links SET relevance_status='active'`},
		"root":             {`UPDATE knowledge.notes_source_roots SET status='blocked'`, `UPDATE knowledge.notes_source_roots SET status='active'`},
		"owner":            {`UPDATE nodes.nodes SET status='disabled'`, `UPDATE nodes.nodes SET status='active'`},
		"private":          {`UPDATE files.file_metadata SET index_policy='private_no_index'`, `UPDATE files.file_metadata SET index_policy='text_later'`},
		"metadata-private": {`UPDATE files.file_metadata SET metadata='{"private_no_index":true}'`, `UPDATE files.file_metadata SET metadata='{}'`},
		"version":          {`UPDATE objects.object_versions SET status='superseded'`, `UPDATE objects.object_versions SET status='active'`},
		"replica":          {`UPDATE sync.replicas SET freshness_state='stale'`, `UPDATE sync.replicas SET freshness_state='fresh'`},
		"replica-kind":     {`UPDATE sync.replicas SET replicated_kind='other'`, `UPDATE sync.replicas SET replicated_kind='object_version'`},
		"path-wildcard":    {`UPDATE objects.object_versions SET source_path=replace(source_path,'loom_box__','loomXboxXX')`, `UPDATE objects.object_versions SET source_path=replace(source_path,'loomXboxXX','loom_box__')`},
		"file-owner":       {`UPDATE files.file_metadata SET source_node_id=NULL`, `UPDATE files.file_metadata SET source_node_id=(SELECT node_id FROM nodes.nodes)`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.store.db.Exec(change[0]); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.store.db.Exec(change[1]); err != nil {
					t.Fatal(err)
				}
			}()
			current, err := s.store.ListSourceRoots(t.Context(), SourceRootFilter{IncludeInactive: true})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), "")
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := s.BuildKnowledgeObjectCandidates(current, nil, entries); len(got) != 0 {
				t.Fatalf("ineligible admission: %+v", got)
			}
			for _, object := range objects {
				if _, err := s.store.GetKnowledgeObject(t.Context(), object.KnowledgeObjectID); err != sql.ErrNoRows {
					t.Fatalf("ineligible read: %v", err)
				}
				var visible bool
				if err := s.store.db.QueryRow(`SELECT `+notesKnowledgeVisibilitySQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&visible); err != nil || visible {
					t.Fatalf("strict visibility %v %v", visible, err)
				}
			}
		})
	}
}

func TestBoxSyncedProjectScopeCompatibilityPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	actor := ids.NewActorID()
	if _, err := s.store.db.Exec(`INSERT INTO identity.actors(actor_id,actor_key,display_name,actor_kind,status) VALUES ($1,'project-operator','Project operator','human','active')`, actor); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{RootKindProjectNotes, RootKindProjectMaterial} {
		root := roots[i]
		project := ids.NewProjectID()
		if _, err := s.store.db.Exec(`UPDATE scopes.scopes SET scope_type='project' WHERE scope_key=$1`, "loom_box:sync-fixture:"+strings.TrimPrefix(root.RootKind, "box_")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`INSERT INTO projects.projects(project_id,project_scope_id,slug,name,owner_actor_id,created_by_actor_id,status) SELECT $1,scope_id,$2,'Project',$3,$3,'active' FROM scopes.scopes WHERE scope_key=$4`, project, fmt.Sprintf("project-%d", i), actor, "loom_box:sync-fixture:"+strings.TrimPrefix(root.RootKind, "box_")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE knowledge.notes_source_roots SET root_kind=$1,project_id=$2 WHERE notes_source_root_id=$3`, kind, project, root.NotesSourceRootID); err != nil {
			t.Fatal(err)
		}
		current, err := s.store.ListSourceRoots(t.Context(), SourceRootFilter{NotesSourceRootID: root.NotesSourceRootID})
		if err != nil || len(current) != 1 {
			t.Fatalf("project root: %+v %v", current, err)
		}
		entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), root.NotesSourceRootID)
		if err != nil || len(entries) != 1 || entries[0].ProjectID != project {
			t.Fatalf("project sync: %+v %v", entries, err)
		}
		result, err := s.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{SourceRoots: current, SyncedObjects: entries})
		if err != nil || len(result.KnowledgeObjects) != 1 {
			t.Fatalf("project admission: %+v %v", result, err)
		}
		object := result.KnowledgeObjects[0]
		var visible bool
		if err := s.store.db.QueryRow(`SELECT `+notesKnowledgeVisibilitySQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&visible); err != nil || !visible {
			t.Fatalf("project exact read %v %v", visible, err)
		}
		if _, err := s.store.db.Exec(`UPDATE projects.projects SET status='archived' WHERE project_id=$1`, project); err != nil {
			t.Fatal(err)
		}
		entries, err = s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), root.NotesSourceRootID)
		if err != nil || len(entries) != 0 {
			t.Fatalf("archived project admitted: %+v %v", entries, err)
		}
		if err := s.store.db.QueryRow(`SELECT `+notesKnowledgeVisibilitySQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&visible); err != nil || visible {
			t.Fatalf("archived project exact read %v %v", visible, err)
		}
	}
}

func TestNotesAdmissionBounds(t *testing.T) {
	for _, limit := range []int{-1, 0, 201} {
		if _, err := NewService(nil).AdmitRegisteredNotesOnce(t.Context(), AdmissionCursor{}, limit); err == nil {
			t.Fatalf("accepted invalid admission: %d", limit)
		}
	}
}

func TestNotesCatalogPreferredPaginationPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	svc := NewService(db)
	key := "page_" + ids.NewKnowledgeObjectID()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM storage.storage_entries WHERE watched_root_key=$1`, key); err != nil {
			t.Error(err)
		}
	})
	catalog := storagecatalog.NewService(db)
	create := func(path, state string) storagecatalog.Entry {
		t.Helper()
		entry, err := catalog.RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{
			StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaNotes,
			WatchedRootKey: key, OriginNodeKey: key, LogicalPath: path, OriginalSourcePath: path,
			FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: state,
		})
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	want := map[string]bool{}
	for i := range 5 {
		entry := create(fmt.Sprintf("file-%d.md", i), storagecatalog.AvailabilityStateAvailable)
		want[entry.StorageEntryID] = true
	}
	live := create("same.md", storagecatalog.AvailabilityStateAvailable)
	history := create("same.md", storagecatalog.AvailabilityStateArchived)
	want[live.StorageEntryID] = true
	seen := map[string]bool{}
	after := ""
	for pages := 0; pages < 100; pages++ {
		entries, more, err := svc.store.notesCatalogPage(t.Context(), after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) > 2 {
			t.Fatal("unbounded catalog page")
		}
		for _, entry := range entries {
			if seen[entry.StorageEntryID] {
				t.Fatal("duplicate across cursor boundary")
			}
			seen[entry.StorageEntryID] = true
		}
		if !more {
			break
		}
		after = entries[len(entries)-1].StorageEntryID
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("missing preferred live entry: %s", id)
		}
	}
	if seen[history.StorageEntryID] {
		t.Fatal("later archived representation displaced live source")
	}
}
