package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/watchedroots"
)

type notesArchivePostgresFixture struct {
	s       *Service
	p       NotesArchiveProjector
	root    SourceRoot
	objects []KnowledgeObject
	plan    storagearchive.WorkspaceArchivePlan
	actor   string
	before  string
}

func notesArchiveTestFixture(t *testing.T) *notesArchivePostgresFixture {
	t.Helper()
	s, roots := boxSyncedFixture(t)
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
	for _, relative := range []string{"topic-one/second.md", "neighbour/other.md"} {
		seedNotesArchiveSibling(t, s, root, relative, string(body)+relative)
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	if len(objects) != 5 || advanceBoxSyncedFixture(t, s) == 0 {
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

func seedNotesArchiveSibling(t *testing.T, s *Service, root SourceRoot, relative, body string) {
	t.Helper()
	path := filepath.Join(root.SourcePath, filepath.FromSlash(relative))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var scope string
	var err error
	if root.ProjectID != nil {
		err = s.store.db.QueryRow(`SELECT project_scope_id FROM projects.projects WHERE project_id=$1`, *root.ProjectID).Scan(&scope)
	} else {
		err = s.store.db.QueryRow(`SELECT scope_id FROM scopes.scopes WHERE scope_key=$1`, "loom_box:sync-fixture:"+strings.TrimPrefix(root.RootKind, "box_")).Scan(&scope)
	}
	if err != nil {
		t.Fatal(err)
	}
	object, version, blob := ids.NewObjectID(), ids.NewObjectVersionID(), ids.NewBlobID()
	hash := hashArtifactValue(body)
	uri := "watched-root://" + root.BackendRootKey + "/" + relative
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,mime_type,status) VALUES ($1,'sha256',$2,$3,$4,$5,'text/markdown','verified')`, []any{blob, strings.TrimPrefix(hash, "sha256:"), hash, len(body), path}},
		{`INSERT INTO objects.objects(object_id,object_type,name,home_scope_id,state_class,status) VALUES ($1,'file',$2,$3,'canonical','active')`, []any{object, filepath.Base(relative), scope}},
		{`INSERT INTO objects.object_versions(object_version_id,object_id,version_number,blob_id,content_hash,source_node_id,source_path,size_bytes,mime_type,status,metadata) VALUES ($1,$2,1,$3,$4,$5,$6,$7,'text/markdown','active','{"file_class":"markdown"}')`, []any{version, object, blob, hash, *root.NodeID, uri, len(body)}},
		{`INSERT INTO files.file_metadata(object_id,logical_name,mime_type,source_node_id,source_path,latest_version_id,index_policy) VALUES ($1,$2,'text/markdown',$3,$4,$5,'text_later')`, []any{object, filepath.Base(relative), *root.NodeID, uri, version}},
		{`INSERT INTO objects.object_scope_links(object_scope_link_id,object_id,scope_id,relationship_type) VALUES ($1,$2,$3,'primary')`, []any{ids.NewObjectScopeLinkID(), object, scope}},
		{`INSERT INTO sync.replicas(replica_id,replicated_kind,replicated_id,source_node_id,replica_node_id,replica_mode,freshness_state,storage_ref) VALUES ($1,'object_version',$2,$3,$3,'main_snapshot','fresh',$4)`, []any{ids.NewReplicaID(), version, *root.NodeID, hash}},
	}
	for _, statement := range statements {
		if _, err := s.store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *notesArchivePostgresFixture) processingSnapshot(t *testing.T) string {
	t.Helper()
	var snapshot string
	if err := f.s.store.db.QueryRow(`SELECT jsonb_build_array(
	 (SELECT jsonb_agg(to_jsonb(o) ORDER BY knowledge_object_id) FROM knowledge.knowledge_objects o),
	 (SELECT jsonb_agg(to_jsonb(v) ORDER BY knowledge_object_version_id) FROM knowledge.knowledge_object_versions v),
	 (SELECT jsonb_agg(to_jsonb(c) ORDER BY knowledge_chunk_id) FROM knowledge.knowledge_chunks c),
	 (SELECT jsonb_agg(to_jsonb(p) ORDER BY knowledge_pipeline_run_id) FROM knowledge.pipeline_runs p),
	 (SELECT count(*) FROM knowledge.pipeline_stage_runs),
	 (SELECT count(*) FROM knowledge.chunk_embeddings),
	 (SELECT count(*) FROM knowledge.embedding_work_items),
	 (SELECT jsonb_agg(to_jsonb(r) ORDER BY notes_source_root_id) FROM knowledge.notes_source_roots r))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f *notesArchivePostgresFixture) archive(t *testing.T) {
	t.Helper()
	if result, err := f.p.Workspace.ApplyArchive(t.Context(), f.plan, f.plan.PlanDigest); err != nil || result.Operation.Phase != storagearchive.PhaseArchiveComplete {
		t.Fatalf("archive: %+v %v", result, err)
	}
}

func (f *notesArchivePostgresFixture) restore(t *testing.T) storagearchive.WorkspaceArchivePlan {
	t.Helper()
	plan, err := f.p.Workspace.PlanRestore(t.Context(), storagearchive.WorkspaceRestorePlanInput{ArchiveOperationID: f.plan.OperationID, ActorID: f.actor, Reason: "disposable Notes restore"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.p.Workspace.ApplyRestore(t.Context(), plan, plan.PlanDigest); err != nil || result.Operation.Phase != storagearchive.PhaseRestoreComplete {
		t.Fatalf("restore: %+v %v", result, err)
	}
	return plan
}

func (f *notesArchivePostgresFixture) assertCustody(t *testing.T, state string, history int) {
	t.Helper()
	var affected, neighbours, transitions int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_object_custody WHERE workspace_object_id='topic_notes_one' AND source_lifecycle=$1`, state).Scan(&affected); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_object_custody WHERE workspace_object_id IS NULL AND source_lifecycle='active'`).Scan(&neighbours); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_transitions`).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if affected != 2 || neighbours != 3 || transitions != history {
		t.Fatalf("custody: %d affected %d neighbours %d history", affected, neighbours, transitions)
	}
	if got := f.processingSnapshot(t); got != f.before {
		t.Fatal("custody changed source identity, versions, roots or processing")
	}
}

func TestNotesArchiveProjectionReplayPostgres(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(fmt.Sprint("delayed=", delayed), func(t *testing.T) {
			f := notesArchiveTestFixture(t)
			if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err == nil {
				t.Fatal("uncommitted plan accepted")
			}
			f.archive(t)
			var restore storagearchive.WorkspaceArchivePlan
			if delayed {
				restore = f.restore(t)
				if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err == nil {
					t.Fatal("restore without predecessor accepted")
				}
			}
			result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
			if err != nil || result.Projected != 2 || result.Replayed != 0 {
				t.Fatalf("projection: %+v %v", result, err)
			}
			f.assertCustody(t, "archived", 2)
			if !delayed {
				restore = f.restore(t)
			}
			result, err = f.p.ProjectOperation(t.Context(), restore.OperationID)
			if err != nil || result.Projected != 2 {
				t.Fatalf("restore projection: %+v %v", result, err)
			}
			f.assertCustody(t, "active", 4)
			for _, operation := range []string{f.plan.OperationID, restore.OperationID, f.plan.OperationID} {
				result, err = f.p.ProjectOperation(t.Context(), operation)
				if err != nil || result.Projected != 0 || result.Replayed != 2 {
					t.Fatalf("replay: %+v %v", result, err)
				}
				f.assertCustody(t, "active", 4)
			}
		})
	}
}

func TestNotesArchiveProjectionEvidenceRefusalPostgres(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"hash", `UPDATE knowledge.knowledge_objects SET source_hash='sha256:' || repeat('a',64) WHERE relative_path='topic-one/second.md'`},
		{"version", `UPDATE objects.object_versions SET status='superseded' WHERE source_path LIKE '%/topic-one/second.md'`},
		{"replica", `UPDATE sync.replicas SET freshness_state='stale' WHERE replicated_id IN (SELECT object_version_id FROM objects.object_versions WHERE source_path LIKE '%/topic-one/second.md')`},
		{"node", `UPDATE knowledge.knowledge_objects SET source_node_key='another-node' WHERE relative_path='topic-one/second.md'`},
		{"declaration", `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{registration_metadata,knowledge_source,enabled}','false') WHERE root_kind='box_topics'`},
		{"missing_inventory", `UPDATE knowledge.knowledge_objects SET source_path=replace(source_path,'second.md','missing.md'),relative_path='topic-one/missing.md' WHERE relative_path='topic-one/second.md'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := notesArchiveTestFixture(t)
			f.archive(t)
			if _, err := f.s.store.db.Exec(test.sql); err != nil {
				t.Fatal(err)
			}
			result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
			if err == nil || result.Projected != 0 {
				t.Fatalf("bad evidence accepted: %+v %v", result, err)
			}
			var count int
			if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_transitions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial transaction: %d %v", count, err)
			}
		})
	}
}

func TestNotesArchiveProjectionConcurrentPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	var wg sync.WaitGroup
	results := make(chan NotesArchiveProjectionResult, 4)
	errorsOut := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "40001" {
					continue
				}
				results <- result
				errorsOut <- err
				return
			}
			errorsOut <- fmt.Errorf("serialization did not converge")
		}()
	}
	wg.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	projected, replayed := 0, 0
	for result := range results {
		projected += result.Projected
		replayed += result.Replayed
	}
	if projected != 2 || replayed != 6 {
		t.Fatalf("concurrent truth: %d %d", projected, replayed)
	}
	f.assertCustody(t, "archived", 2)
}

func TestNotesArchiveProjectionRollbackPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	_, err := f.s.store.db.Exec(`CREATE FUNCTION knowledge.fail_notes_custody_test() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN IF EXISTS(SELECT 1 FROM knowledge.notes_custody_transitions) THEN RAISE EXCEPTION 'disposable second row failure'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER fail_notes_custody_test BEFORE INSERT ON knowledge.notes_custody_transitions FOR EACH ROW EXECUTE FUNCTION knowledge.fail_notes_custody_test()`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
	if err == nil || result.Projected != 0 {
		t.Fatalf("failed transaction reported writes: %+v %v", result, err)
	}
	var count int
	if err := f.s.store.db.QueryRow(`SELECT (SELECT count(*) FROM knowledge.notes_custody_transitions)+(SELECT count(*) FROM knowledge.notes_current_custody)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback: %d %v", count, err)
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("failed transaction changed Notes processing")
	}
}

func TestNotesArchiveProjectionIgnoresPostArchiveAdmissionPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	f.restore(t)
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET created_at=$1 WHERE relative_path='topic-one/second.md'`, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
	if err != nil || result.Projected != 1 {
		t.Fatalf("late admission: %+v %v", result, err)
	}
}

// Transport is local in this Notes fixture. The real node agent still stops
// the fixture watcher, publishes its fence and seals fresh quiescence evidence.
type notesArchiveLocalQuiescence struct {
	node nodeagent.ProjectArchiveQuiescenceService
}

func (q notesArchiveLocalQuiescence) VerifyProjectArchiveRuntimeQuiescence(ctx context.Context, _ requestctx.Context, request storagearchive.ProjectRuntimeQuiescenceRequest) (storagearchive.ProjectRuntimeQuiescenceReceipt, error) {
	aggregate := storagearchive.ProjectRuntimeQuiescenceReceipt{SchemaVersion: request.SchemaVersion,
		ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug, OperationID: request.OperationID,
		PlanDigest: request.PlanDigest, Evidence: []projectquiescence.Evidence{}}
	if len(request.Targets) == 0 {
		return aggregate, nil
	}
	for _, target := range request.Targets {
		if target.OwnerNode != q.node.Config.NodeKey || target.Kind != projectquiescence.TargetKindWatchedRoot {
			return aggregate, fmt.Errorf("unexpected disposable quiescence target")
		}
	}
	request.NodeKey = q.node.Config.NodeKey
	if err := projectquiescence.SealRequest(&request); err != nil {
		return aggregate, err
	}
	receipt, err := q.node.Quiesce(ctx, request)
	if err != nil {
		return aggregate, err
	}
	for _, evidence := range receipt.Evidence {
		evidence.ReceiptID = receipt.ReceiptID
		aggregate.Evidence = append(aggregate.Evidence, evidence)
	}
	return aggregate, nil
}

func TestNotesArchiveProjectionProjectCompletionPostgres(t *testing.T) {
	for _, material := range []bool{false, true} {
		t.Run(fmt.Sprint("material=", material), func(t *testing.T) { testNotesArchiveProjectCompletion(t, material, nil) })
	}
}

func testNotesArchiveProjectCompletion(t *testing.T, material bool, observe func(*notesArchivePostgresFixture, KnowledgeObject, SourceLifecycle)) {
	testNotesArchiveProjectCompletionMode(t, material, false, observe)
}

// declaration mode changes only the project source fixture; legacy defaults and
// all existing archive/restore/custody assertions remain shared.
func testNotesArchiveProjectCompletionMode(t *testing.T, material, declaration bool, observe func(*notesArchivePostgresFixture, KnowledgeObject, SourceLifecycle)) {
	testNotesArchiveProjectCompletionBoundMode(t, material, declaration, false, observe)
}

func testNotesArchiveProjectCompletionBoundMode(t *testing.T, material, declaration, bound bool, observe func(*notesArchivePostgresFixture, KnowledgeObject, SourceLifecycle)) {
	f := notesArchiveTestFixture(t)
	mainID := ids.NewNodeID()
	if _, err := f.s.store.db.Exec(`INSERT INTO nodes.nodes(node_id,node_key,display_name,node_kind,node_role,runtime_class,status)
	 VALUES ($1,'main','Disposable Main','server','main','native','active')`, mainID); err != nil {
		t.Fatal(err)
	}
	f.root.NodeID, f.root.NodeKey, f.p.NodeKey = &mainID, "main", "main"
	projectID, slug := ids.NewProjectID(), "project-notes-test"
	active := filepath.Join(f.p.Workspace.Roots.BoxRoot, "Projects", slug)
	for _, dir := range []string{filepath.Join(active, ".loom", "contracts"), filepath.Join(active, "notes"), filepath.Join(active, "docs"), filepath.Join(f.p.Workspace.Roots.StorageRoot, "archive", "projects")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contract := fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject:\n  id: %s\n  slug: %s\n  name: Notes custody acceptance\n  owner_node: %s\n  status: active\nfacets:\n  notes: true\n", projectID, slug, f.root.NodeKey)
	if declaration {
		contract = fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: %s\n  slug: %s\n  name: Notes custody acceptance\n  owner_node: %s\n  status: active\nresources:\n  notes:\n    kind: knowledge\n    knowledge: {path: notes, category: notes}\n  docs:\n    kind: knowledge\n    knowledge: {path: docs, category: docs}\n", projectID, slug, f.root.NodeKey)
	}
	if err := os.WriteFile(filepath.Join(active, ".loom", "project.yaml"), []byte(contract), 0o600); err != nil {
		t.Fatal(err)
	}
	notesContract := "kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes:\n  status: draft\n  sync: true\n  index: true\n  backup: false\n"
	if material {
		notesContract += "material:\n  - key: docs\n    category: docs\n    path: docs\n    enabled: true\n"
	}
	if err := os.WriteFile(filepath.Join(active, ".loom", "contracts", "notes.yaml"), []byte(notesContract), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := projectregistration.BuildInput(projectcontracts.Analyze(active), "notes-archive-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	req := requestctx.Context{ActorID: f.actor, ActorKey: f.actor, OriginNodeID: *f.root.NodeID, OriginNodeKey: f.root.NodeKey, CorrelationID: "corr_notes_custody_project", FreshnessMode: "live_required", Source: "disposable-acceptance"}
	project := projects.NewService(f.s.store.db)
	nodeRoot := t.TempDir()
	if bound {
		setupBoundPhysicalDeclaration(t, f, req, active, nodeRoot)
	} else {
		if _, err := project.RegisterProjectContract(t.Context(), req, input); err != nil {
			t.Fatal(err)
		}
		watch := projectwatch.NewService(projectwatch.Deps{Projects: project, Nodes: nodes.NewService(f.s.store.db)})
		if _, err := watch.ApplyDesiredState(t.Context(), req, projectID, projects.ApplyProjectWatchPolicyInput{UseRegisteredSnapshot: true}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := project.GetProjectRegistrationStatus(t.Context(), projectID)
	wantRoots := 1
	if material {
		wantRoots = 2
	}
	if err != nil || len(detail.WatchedRootRegistrations) != wantRoots {
		t.Fatalf("disposable watcher registration: %+v %v", detail, err)
	}
	registration := detail.WatchedRootRegistrations[0]
	nodeStore := noderuntime.NewStore(nodeRoot)
	for _, item := range detail.WatchedRootRegistrations {
		if bound {
			if item.SafeRootKey == "project" {
				t.Fatal("physical fixture did not exercise the bound adapter")
			}
			continue
		}
		if err := nodeStore.SaveInstance(noderuntime.WorkerInstance{WorkerKey: item.WorkerKey,
			Kind: noderuntime.KindWatchedRoot, Enabled: true, LocalRootKey: item.LocalRootKey,
			ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON}); err != nil {
			t.Fatal(err)
		}
	}
	if material {
		credential, err := nodes.NewService(f.s.store.db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: mainID, Reason: "disposable Notes material owner report"})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range detail.WatchedRootRegistrations {
			if _, err := watchedroots.NewService(f.s.store.db).Report(t.Context(), watchedroots.ReportInput{NodeRef: mainID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: watchedroots.StatusHealthy}); err != nil {
				t.Fatal(err)
			}
		}
		if err := project.CorrelateProjectWatchedRootReports(t.Context(), projectID); err != nil {
			t.Fatal(err)
		}
	}
	service := storagearchive.NewProjectRuntimeService(storagearchive.ProjectRuntimeDeps{Projects: project, RepositoryState: project,
		WorkspaceMove: &f.p.Workspace, WorkspaceRoots: f.p.Workspace.Roots,
		Activation: projectactivation.NewService(projectactivation.Deps{Projects: project}),
		RuntimeQuiescence: notesArchiveLocalQuiescence{node: nodeagent.ProjectArchiveQuiescenceService{
			Config: nodeagent.Config{NodeKey: "main"}, Store: nodeagent.Store{DataDir: nodeRoot}}}})
	_, req.ScopeID, req.ScopeKey, err = service.ProjectPhysicalScope(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
		t.Fatal(err)
	}
	roots, err := f.s.store.ListSourceRoots(t.Context(), SourceRootFilter{ProjectID: projectID})
	if err != nil || len(roots) != wantRoots || roots[0].ProjectWatchedRootRegistrationID == nil {
		t.Fatalf("real project Notes registration: %+v %v", roots, err)
	}
	root := roots[0]
	if material {
		for _, item := range roots {
			if item.RootKind == RootKindProjectMaterial && (!declaration || item.Declaration == "docs") {
				root = item
			}
		}
		if root.RootKind != RootKindProjectMaterial || sourceDeclaration(root) == nil || root.Status != SourceRootStatusActive {
			t.Fatalf("material declaration not active: %+v", root)
		}
	}
	seedNotesArchiveSibling(t, f.s, root, "decision.md", "# Project decision\nKeep this source and its citation.\n")
	objects := reconcileBoxSyncedFixture(t, f.s, []SourceRoot{root})
	if len(objects) != 1 {
		t.Fatalf("project admission: %d", len(objects))
	}
	advanceBoxSyncedFixture(t, f.s)
	before := f.processingSnapshot(t)
	review, err := service.ReviewProjectPhysicalArchive(t.Context(), req, projectID, storagearchive.ProjectPhysicalArchivePlanInput{Reason: "Notes project custody proof"})
	if err != nil {
		_, diagnostic := service.PlanProjectPhysicalArchive(t.Context(), req, projectID, storagearchive.ProjectPhysicalArchivePlanInput{Reason: "Notes project custody proof"})
		t.Fatalf("project review: %v; local fixture diagnostic: %v", err, diagnostic)
	}
	service.ArchiveFailureHook = func(at storagearchive.ProjectPhysicalArchiveFailureBoundary) error {
		if at == storagearchive.ProjectArchiveBoundaryBeforeProjectStateCommit {
			return errors.New("disposable project commit stop")
		}
		return nil
	}
	_, stoppedApplyErr := service.ApplyReviewedProjectPhysicalPlan(t.Context(), req, projectID, storagearchive.ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true})
	if stoppedApplyErr == nil {
		t.Fatal("project stop not reached")
	}
	if _, err := f.p.Workspace.ReadLifecycleEvidence(t.Context(), review.Workspace.OperationID); err != nil {
		t.Fatalf("kernel completion not reached: %v; disposable apply: %v", err, stoppedApplyErr)
	}
	if _, err := f.p.ProjectOperation(t.Context(), review.Workspace.OperationID); err == nil {
		t.Fatal("generic completion accepted before project completion")
	}
	service.ArchiveFailureHook = nil
	if _, err := service.RecoverReviewedProjectPhysicalPlan(t.Context(), req, projectID, storagearchive.ProjectPhysicalRecoverRequest{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if material {
		if _, err := f.s.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
			t.Fatal(err)
		}
		if processingWithoutRoots(t, f.processingSnapshot(t)) != processingWithoutRoots(t, before) {
			t.Fatal("source reconciliation changed processing")
		}
		before = f.processingSnapshot(t)
	}
	result, err := f.p.ProjectOperation(t.Context(), review.Workspace.OperationID)
	if err != nil || result.Projected != 1 {
		t.Fatalf("completed project: %+v %v", result, err)
	}
	assertNotesCustodyReadable(t, f.s, objects[0].KnowledgeObjectID, true)
	if observe != nil {
		observe(f, objects[0], SourceLifecycleArchived)
	}
	restore, err := service.ReviewProjectPhysicalRestore(t.Context(), req, projectID, storagearchive.ProjectPhysicalRestorePlanInput{Reason: "Notes project inactive restore"})
	if err != nil {
		t.Fatal(err)
	}
	service.RestoreFailureHook = func(at storagearchive.ProjectPhysicalRestoreFailureBoundary) error {
		if at == storagearchive.ProjectRestoreBoundaryBeforeProjectCommit {
			return errors.New("disposable restore commit stop")
		}
		return nil
	}
	if _, err := service.ApplyReviewedProjectPhysicalPlan(t.Context(), req, projectID, storagearchive.ProjectPhysicalApplyRequest{Plan: restore, PlanDigest: restore.PlanDigest, Confirm: true}); err == nil {
		t.Fatal("restore stop not reached")
	}
	if _, err := f.p.Workspace.ReadLifecycleEvidence(t.Context(), restore.Workspace.OperationID); err != nil {
		t.Fatalf("kernel restore completion not reached: %v", err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), restore.Workspace.OperationID); err == nil {
		t.Fatal("generic restore accepted before project completion")
	}
	service.RestoreFailureHook = nil
	if _, err := service.RecoverReviewedProjectPhysicalPlan(t.Context(), req, projectID, storagearchive.ProjectPhysicalRecoverRequest{OperationID: restore.Workspace.OperationID, PlanDigest: restore.PlanDigest, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	result, err = f.p.ProjectOperation(t.Context(), restore.Workspace.OperationID)
	if err != nil || result.Projected != 1 {
		t.Fatalf("completed project restore: %+v %v", result, err)
	}
	assertNotesCustodyReadable(t, f.s, objects[0].KnowledgeObjectID, true)
	if observe != nil {
		observe(f, objects[0], SourceLifecycleActive)
	}
	result, err = f.p.ProjectOperation(t.Context(), review.Workspace.OperationID)
	if err != nil || result.Replayed != 1 || result.Projected != 0 {
		t.Fatalf("late project archive replay: %+v %v", result, err)
	}
	if f.processingSnapshot(t) != before {
		t.Fatal("project custody changed Notes identities/processing")
	}
	var count int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_transitions WHERE project_event_id IS NOT NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("project event binding: %d %v", count, err)
	}
	var writerStatus, cachedRootStatus string
	wantCached := "active"
	if material {
		wantCached = "disabled"
	}
	if err := f.s.store.db.QueryRow(`SELECT p.activation_status,r.status FROM projects.project_watched_root_registrations p
	 JOIN knowledge.notes_source_roots r USING(project_watched_root_registration_id) WHERE r.notes_source_root_id=$1`, root.NotesSourceRootID).Scan(&writerStatus, &cachedRootStatus); err != nil || writerStatus != "disabled" || cachedRootStatus != wantCached {
		t.Fatalf("restored writer/cache posture: %s/%s %v", writerStatus, cachedRootStatus, err)
	}
	if _, err := f.s.store.UpsertKnowledgeObject(t.Context(), objects[0]); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("restored project reactivated admission through cached root: %v", err)
	}
	policy, err := f.s.GetPipelinePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.EnsurePipelineRun(t.Context(), objects[0], policy.Policy, true, 1); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("restored project reactivated processing: %v", err)
	}
	if f.processingSnapshot(t) != before {
		t.Fatal("disabled restored project changed Notes processing")
	}
	instance, err := nodeStore.LoadInstance(registration.WorkerKey)
	if err != nil || instance.Enabled {
		t.Fatalf("restored project resumed its owner-node watcher: %+v %v", instance, err)
	}
	if _, err := f.s.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
		t.Fatal(err)
	}
	assertNotesCustodyReadable(t, f.s, objects[0].KnowledgeObjectID, true)
	assertNotesCustodyReadRevocations(t, f.s, objects[0], map[string]string{
		"wrong_actor":          `UPDATE projects.projects SET archive_state=jsonb_set(archive_state,'{actor_id}','"different_actor"') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"wrong_operation":      `UPDATE projects.projects SET archive_state=jsonb_set(archive_state,'{operation_id}','"different_operation"') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"wrong_digest":         `UPDATE projects.projects SET archive_state=jsonb_set(archive_state,'{workspace_plan_digest}','"different_digest"') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"early_stop":           `UPDATE projects.projects SET archive_state=jsonb_set(archive_state,'{started_at}','"2099-01-01T00:00:00Z"') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"root_blocked":         `UPDATE knowledge.notes_source_roots SET status='blocked' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"registration_binding": `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{project_contract_registration_id}','"another_registration"') WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"explicit_declaration": `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{registration_metadata,knowledge_source}','{"enabled":false}') WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"private_source":       `UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"later_writer_disable": `UPDATE projects.project_watched_root_registrations SET last_applied_at=now(),updated_at=now() WHERE project_watched_root_registration_id=(SELECT r.project_watched_root_registration_id FROM knowledge.knowledge_objects o JOIN knowledge.notes_source_roots r USING(notes_source_root_id) WHERE o.knowledge_object_id=$1)`,
		"changed_reason":       `UPDATE projects.project_watched_root_registrations SET metadata=jsonb_set(metadata,'{reason}','"unrelated stop"') WHERE project_watched_root_registration_id=(SELECT r.project_watched_root_registration_id FROM knowledge.knowledge_objects o JOIN knowledge.notes_source_roots r USING(notes_source_root_id) WHERE o.knowledge_object_id=$1)`,
		"changed_project_path": `UPDATE projects.projects SET archive_state=jsonb_set(archive_state,'{active_path}','"/different/project"') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
	})
}

func setupBoundPhysicalDeclaration(t *testing.T, f *notesArchivePostgresFixture, req requestctx.Context, root, nodeRoot string) {
	t.Helper()
	db := f.s.store.db
	if _, err := db.Exec(`INSERT INTO identity.actor_node_authorizations(authorization_id,actor_id,node_id,authorization_level,status) VALUES ($1,$2,$3,5,'active')`, ids.NewActorNodeAuthorizationID(), req.ActorID, req.OriginNodeID); err != nil {
		t.Fatal(err)
	}
	resolver := projectapply.NewLocalResolver(db, func() (config.Config, error) {
		return config.Config{NodeID: "main", BoxPath: f.p.Workspace.Roots.BoxRoot}, nil
	})
	owner := projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	service := projectapply.NewService(db, resolver, projectapply.NewCurrentAuthority(db), map[projectcontracts.DeclarationOwner]projectapply.Owner{
		projectcontracts.DeclarationOwnerProjects:   projectapply.ProjectsOwner{Resolver: resolver, Projects: projects.NewService(db)},
		projectcontracts.DeclarationOwnerKnowledge:  owner,
		projectcontracts.DeclarationOwnerProtection: owner,
	})
	principal := projectapply.Principal{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID}
	plan, err := service.Plan(t.Context(), principal, projectcontracts.DeclarationPlanRequest{SchemaVersion: projectcontracts.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	apply := projectcontracts.DeclarationApplyRequest{SchemaVersion: projectcontracts.DeclarationRequestSchemaV05, ProjectRef: root, PlanID: plan.PlanID, IdempotencyKey: "bound-physical", Effects: []projectcontracts.DeclarationEffect{projectcontracts.DeclarationReconcile}}
	pending, err := service.Apply(t.Context(), principal, apply)
	if failure, ok := err.(*projectapply.Failure); !ok || failure.Cause != "owner_pending" || pending.State != projectcontracts.DeclarationOperationPartial {
		t.Fatalf("bound physical pending owner: %+v %v", pending, err)
	}
	var messageID string
	if err := db.QueryRow(`SELECT message_id FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, pending.OperationID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	message, err := communication.NewService(db).GetMessage(t.Context(), messageID)
	if err != nil {
		t.Fatal(err)
	}
	store := nodeagent.Store{ConfigPath: filepath.Join(nodeRoot, "config.json"), StatePath: filepath.Join(nodeRoot, "state.json"), DataDir: nodeRoot}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(nodeagent.Config{MainURL: "http://main.test", NodeKey: "main", DisplayName: "Main", BoxRootPath: f.p.Workspace.Roots.BoxRoot}); err != nil {
		t.Fatal(err)
	}
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: req.OriginNodeID, Reason: "disposable bound physical acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	ack := nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, nodeagent.State{NodeID: req.OriginNodeID}, message)
	if ack.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("bound physical node ACK: %s", ack.ResultJSON)
	}
	ack.CredentialToken = credential.CredentialToken
	if _, err := communication.NewService(db).Ack(t.Context(), req, ack); err != nil {
		t.Fatal(err)
	}
	apply.OperationID = pending.OperationID
	done, err := service.Apply(t.Context(), principal, apply)
	if err != nil || done.State != projectcontracts.DeclarationOperationSucceeded {
		t.Fatalf("bound physical completion: %+v %v", done, err)
	}
}

func TestNotesArchiveProjectionCatalogPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	var original KnowledgeObject
	for _, o := range f.objects {
		if o.RelativePath == "topic-one/source.md" {
			original = o
		}
	}
	entry, err := storagecatalog.NewService(f.s.store.db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{
		StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaExternalWatchedRoot,
		OriginNodeID: *f.root.NodeID, OriginNodeKey: f.root.NodeKey, WatchedRootKey: f.root.BackendRootKey,
		LogicalPath: original.RelativePath, OriginalSourcePath: original.SourcePath, FileClass: original.FileClass, MimeType: original.MimeType,
		SizeBytes: original.SizeBytes, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(original.SourceHash, "sha256:"), AvailabilityState: storagecatalog.AvailabilityStateAvailable})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := f.s.store.ListStorageEntriesForNotesRoots(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{SourceRoots: []SourceRoot{f.root}, StorageEntries: catalog}); err != nil {
		t.Fatal(err)
	}
	var observed string
	if err := f.s.store.db.QueryRow(`SELECT storage_entry_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, original.KnowledgeObjectID).Scan(&observed); err != nil || observed != entry.StorageEntryID {
		t.Fatalf("catalog admission: %s %v", observed, err)
	}
	if advanceBoxSyncedFixture(t, f.s) != 0 {
		t.Fatal("catalog coexistence reran source extraction")
	}
	f.before = f.processingSnapshot(t)
	f.plan, err = f.p.Workspace.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_notes_one", Slug: "topic-one", ActorID: f.actor, Reason: "catalog and native custody proof"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.plan.CatalogRebinds) == 0 {
		t.Fatal("catalog fixture has no exact reviewed rebind")
	}
	f.archive(t)
	result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
	if err != nil || result.Projected != 2 {
		t.Fatalf("catalog archive: %+v %v", result, err)
	}
	f.assertCustody(t, "archived", 2)
	assertNotesCustodyReadable(t, f.s, original.KnowledgeObjectID, true)
	assertNotesCustodyReadRevocations(t, f.s, original, map[string]string{
		"catalog_metadata": `UPDATE storage.storage_entries SET metadata='{"private_no_index":true}' WHERE storage_entry_id=(SELECT storage_entry_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"catalog_deleted":  `UPDATE storage.storage_entries SET deleted_at=now() WHERE storage_entry_id=(SELECT storage_entry_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
	})
	restore := f.restore(t)
	result, err = f.p.ProjectOperation(t.Context(), restore.OperationID)
	if err != nil || result.Projected != 2 {
		t.Fatalf("catalog restore: %+v %v", result, err)
	}
	f.assertCustody(t, "active", 4)
}

func TestNotesArchiveProjectionBrokenPointerPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.notes_current_custody c SET workspace_lifecycle_event_id=t.workspace_lifecycle_event_id
	 FROM knowledge.notes_custody_transitions t WHERE t.knowledge_object_id=c.knowledge_object_id AND t.previous_event_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err == nil {
		t.Fatal("rolled-back current pointer accepted as a healthy replay")
	}
	if _, err := f.s.store.db.Exec(`DELETE FROM knowledge.notes_current_custody`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err == nil {
		t.Fatal("missing current pointer accepted as a healthy replay")
	}
}

func TestNotesArchiveProjectionSingleConnectionPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	f.s.store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := f.p.ProjectOperation(ctx, f.plan.OperationID)
	if err != nil || result.Projected != 2 {
		t.Fatalf("single-connection projection: %+v %v", result, err)
	}
	f.assertCustody(t, "archived", 2)
}
