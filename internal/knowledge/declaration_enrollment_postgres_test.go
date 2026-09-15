package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	localwatch "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/watchedroots"
)

type declarationEnrollmentDBFixture struct {
	citation   NotesPassageInput
	sourceHash string
	db         *sql.DB
	service    *Service
	projects   projects.Service
	req        requestctx.Context
	analysis   projectcontracts.Analysis
	input      projects.UpsertProjectWatchedRootRegistrationInput
	report     func(projectcontracts.ProjectWatchedRootItem)
}

func declarationEnrollmentDB(t *testing.T) *declarationEnrollmentDBFixture {
	t.Helper()
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	f := &declarationEnrollmentDBFixture{db: db, service: NewService(db), projects: projects.NewService(db), req: requestctx.Context{ActorID: ids.NewActorID()}, analysis: declarationKnowledgeAnalysis(t)}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO identity.actors(actor_id,actor_key,display_name,actor_kind,status) VALUES($1,'d2-fixture','D2 fixture','human','active')`, f.req.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO nodes.nodes(node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES($1,'main','Fixture Main','server','main','native','active')`, ids.NewNodeID()); err != nil {
		t.Fatal(err)
	}
	node, err := nodes.NewService(db).GetNode(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	f.req.OriginNodeID, f.req.OriginNodeKey = node.NodeID, node.NodeKey
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), f.req, nodes.IssueNodeCredentialInput{NodeRef: node.NodeID, Reason: "disposable D2 acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	f.report = func(item projectcontracts.ProjectWatchedRootItem) {
		t.Helper()
		if _, err := watchedroots.NewService(db).Report(t.Context(), watchedroots.ReportInput{NodeRef: node.NodeID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: watchedroots.StatusHealthy}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := projectregistration.BuildInput(f.analysis, "declaration.enrollment.test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.projects.RegisterProjectContract(t.Context(), f.req, input)
	if err != nil {
		t.Fatal(err)
	}
	item := f.analysis.Report.WatchedRoots[0]
	f.input = projects.UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: result.Detail.Registration.ProjectContractRegistrationID, ProjectID: f.analysis.Report.Project.ID, NodeID: node.NodeID, OwnerNodeKey: "main", LocalRootKey: item.Key, BackendRootKey: item.BackendRootKey, SourceKinds: mustJSON(t, item.SourceKinds), SafeRootKey: item.SafeRootKey, RootRelativePath: item.RootRelativePath, DisplayName: item.DisplayName, SyncMode: item.SyncMode, BackupMode: item.BackupMode, IndexMode: item.IndexMode, DeleteMode: item.DeleteMode, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, ActivationStatus: "pending_agent_apply", Metadata: mustJSON(t, item.Metadata)}
	f.upsert(t, f.input)
	return f
}
func (f *declarationEnrollmentDBFixture) upsert(t *testing.T, input projects.UpsertProjectWatchedRootRegistrationInput) {
	t.Helper()
	if _, err := f.projects.UpsertProjectWatchedRootRegistration(t.Context(), f.req, input); err != nil {
		t.Fatal(err)
	}
}
func (f *declarationEnrollmentDBFixture) reconcile(t *testing.T, status string) SourceRoot {
	t.Helper()
	result, err := f.service.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{DiscoverFromStore: true})
	if err != nil || len(result.SourceRoots) != 1 || result.SourceRoots[0].Status != status {
		t.Fatalf("root want %s: %+v %v", status, result, err)
	}
	return result.SourceRoots[0]
}
func (f *declarationEnrollmentDBFixture) correlate(t *testing.T) {
	t.Helper()
	if err := f.projects.CorrelateProjectWatchedRootReports(t.Context(), f.input.ProjectID); err != nil {
		t.Fatal(err)
	}
}
func (f *declarationEnrollmentDBFixture) admit(t *testing.T) {
	t.Helper()
	cursor := AdmissionCursor{}
	for page := 0; page < 20; page++ {
		r, err := f.service.AdmitRegisteredNotesOnce(t.Context(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if r.CatalogObserved > 2 || r.SyncedObserved > 2 {
			t.Fatal("unbounded page")
		}
		if !r.MoreWork {
			return
		}
		cursor = r.Cursor
	}
	t.Fatal("admission did not converge")
}
func (f *declarationEnrollmentDBFixture) publish(t *testing.T) KnowledgeObject {
	t.Helper()
	root := f.reconcile(t, SourceRootStatusActive)
	catalog := storagecatalog.NewService(f.db)
	for _, name := range []string{"source.md", "credentials/secret.md", "loom.notes.yaml"} {
		body := []byte("# Cobalt observatory\nThe cobalt observatory measures the journal.\n")
		file := filepath.Join(root.SourcePath, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		size := int64(len(body))
		if _, err := catalog.RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaProjects, OriginNodeID: f.input.NodeID, OriginNodeKey: "main", ProjectID: f.input.ProjectID, WatchedRootKey: root.BackendRootKey, LogicalPath: name, OriginalSourcePath: name, SizeBytes: &size, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(hashArtifactValue(string(body)), "sha256:"), MimeType: "text/markdown", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: storagecatalog.AvailabilityStateAvailable}); err != nil {
			t.Fatal(err)
		}
	}
	// Ordinary siblings have no declared owner and must never enter admission.
	for _, folder := range []string{"notes", "repos", "unrelated"} {
		if _, err := catalog.RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaProjects, OriginNodeID: f.input.NodeID, OriginNodeKey: "main", ProjectID: f.input.ProjectID, WatchedRootKey: folder, LogicalPath: "inert.md", FileClass: storagecatalog.FileClassMarkdown}); err != nil {
			t.Fatal(err)
		}
	}
	f.admit(t)
	objects, err := f.service.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{})
	if err != nil || len(objects) != 1 || objects[0].RelativePath != "source.md" || objects[0].Declaration != "notes" {
		t.Fatalf("admission count=%d err=%v", len(objects), err)
	}
	f.sourceHash = objects[0].SourceHash
	for step := 0; step < 30; step++ {
		r, err := f.service.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "d2-fixture", Limit: 10, Now: time.Now().UTC()})
		if err != nil || r.Failed != 0 {
			t.Fatalf("pipeline: %+v %v", r, err)
		}
		if r.Claimed == 0 {
			return objects[0]
		}
	}
	t.Fatal("pipeline did not converge")
	return KnowledgeObject{}
}
func (f *declarationEnrollmentDBFixture) visible(t *testing.T, want int) {
	t.Helper()
	r, err := f.service.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", SourceCategory: "projects", Mode: NotesSearchModeLexical})
	if err != nil || len(r.Results) != want {
		t.Fatalf("search want %d: %+v %v", want, r, err)
	}
	if want == 1 {
		hit := r.Results[0]
		f.citation = NotesPassageInput{KnowledgeObjectID: hit.KnowledgeObjectID, KnowledgeObjectVersionID: hit.KnowledgeObjectVersionID, KnowledgeChunkID: hit.KnowledgeChunkID, SourceHash: f.sourceHash}
	}
	if f.citation.KnowledgeChunkID != "" {
		passage, err := f.service.GetNotesPassage(t.Context(), f.citation)
		if want == 1 && (err != nil || !strings.Contains(passage.Text, "cobalt observatory")) {
			t.Fatalf("bound citation unavailable: %v", err)
		}
		if want == 0 && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("stale citation exposed: %v", err)
		}
		if want == 0 {
			archiveRead := f.citation
			archiveRead.SourceLifecycle = SourceLifecycleFilterAll
			if _, err := f.service.GetNotesPassage(t.Context(), archiveRead); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("archive read bypassed declaration membership: %v", err)
			}
		}
	}
	projection, err := f.service.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(projection) != want {
		t.Fatalf("projection want %d: %+v %v", want, projection, err)
	}
}
func TestDeclarationEnrollmentRegisteredAdmissionPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	f.reconcile(t, SourceRootStatusBlocked)
	applied := f.input
	applied.ActivationStatus = "applied"
	f.upsert(t, applied)
	f.reconcile(t, SourceRootStatusBlocked)
	// A same-key owner report with the wrong configuration cannot activate it.
	wrong := f.analysis.Report.WatchedRoots[0]
	wrong.ConfigHash = "sha256:" + strings.Repeat("0", 64)
	f.report(wrong)
	f.correlate(t)
	f.reconcile(t, SourceRootStatusBlocked)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	root := f.reconcile(t, SourceRootStatusActive)
	if root.RootRelativePath != "journal" || root.SourcePath != filepath.Join(f.analysis.Report.ProjectRoot, "journal") {
		t.Fatal("category rewrote location")
	}
	object := f.publish(t)
	f.visible(t, 1)
	f.admit(t)
	replay := f.reconcile(t, SourceRootStatusActive)
	objects, err := f.service.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{})
	if err != nil || len(objects) != 1 || objects[0].KnowledgeObjectID != object.KnowledgeObjectID || replay.NotesSourceRootID != root.NotesSourceRootID {
		t.Fatal("replay lost identity")
	}
}
func TestDeclarationEnrollmentLifecyclePostgres(t *testing.T) {
	t.Run("physical_archive_restore", func(t *testing.T) {
		testNotesArchiveProjectCompletionMode(t, true, true, func(f *notesArchivePostgresFixture, object KnowledgeObject, lifecycle SourceLifecycle) {
			assertDeclarationEnrollmentArchiveReads(t, f, object, lifecycle)
		})
	})
	f := declarationEnrollmentDB(t)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	object := f.publish(t)
	f.visible(t, 1)
	for _, field := range []string{"path", "metadata", "owner", "config_json", "disabled", "stale"} {
		t.Run(field, func(t *testing.T) {
			changed := f.input
			changed.ActivationStatus = "reported"
			switch field {
			case "path":
				changed.RootRelativePath = "unrelated"
			case "metadata":
				m := jsonObject(changed.Metadata)
				m["knowledge_source"].(map[string]any)["source_hash"] = "sha256:" + strings.Repeat("0", 64)
				changed.Metadata = mustJSON(t, m)
			case "owner":
				changed.OwnerNodeKey = "other"
			case "config_json":
				var c map[string]any
				_ = json.Unmarshal(changed.ConfigJSON, &c)
				c["root_relative_path"] = "unrelated"
				changed.ConfigJSON = mustJSON(t, c)
			case "disabled", "stale":
				changed.ActivationStatus = field
			}
			f.upsert(t, changed)
			// Read fence must close immediately, before reconciliation.
			f.visible(t, 0)
			status := SourceRootStatusBlocked
			if field == "disabled" {
				status = SourceRootStatusDisabled
			}
			if field == "stale" {
				status = SourceRootStatusStale
			}
			if field != "owner" {
				f.reconcile(t, status)
			} // changed owner is a distinct historical root identity
			f.upsert(t, f.input)
			f.correlate(t)
			f.reconcile(t, SourceRootStatusActive)
			f.visible(t, 1)
		})
	}
	// A new source revision invalidates the old root even if its config is unchanged.
	sourcePath := f.analysis.Loaded.ContractPath
	revise := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(sourcePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		a := projectcontracts.Analyze(f.analysis.Report.ProjectRoot)
		input, err := projectregistration.BuildInput(a, "declaration.lifecycle.test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.projects.RegisterProjectContract(t.Context(), f.req, input); err != nil {
			t.Fatal(err)
		}
	}
	revise(append(append([]byte{}, f.analysis.Loaded.Raw...), []byte("# new source revision\n")...))
	f.visible(t, 0)
	f.reconcile(t, SourceRootStatusBlocked)
	revise(f.analysis.Loaded.Raw)
	f.correlate(t)
	f.reconcile(t, SourceRootStatusActive)
	f.visible(t, 1)
	removed := strings.Replace(string(f.analysis.Loaded.Raw), "resources:\n  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes}\n", "resources: {}\n", 1)
	revise([]byte(removed))
	f.visible(t, 0)
	if err := f.projects.MarkStaleProjectWatchedRoots(t.Context(), f.req, f.input.ProjectID, f.input.ProjectContractRegistrationID, nil); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, SourceRootStatusStale)
	f.admit(t)
	revise(f.analysis.Loaded.Raw)
	f.upsert(t, f.input)
	f.correlate(t)
	f.reconcile(t, SourceRootStatusActive)
	f.visible(t, 1)
	archived := strings.Replace(string(f.analysis.Loaded.Raw), "  owner_node: main", "  owner_node: main\n  status: archived", 1)
	revise([]byte(archived))
	f.visible(t, 0)
	f.reconcile(t, SourceRootStatusBlocked)
	var count int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("history removed: %d %v", count, err)
	}
	if _, err := os.Stat(filepath.Join(f.analysis.Report.ProjectRoot, "journal/source.md")); err != nil {
		t.Fatal("payload removed")
	}
}

func TestDeclarationBoundEnrollmentPhysicalLifecyclePostgres(t *testing.T) {
	testNotesArchiveProjectCompletionBoundMode(t, true, true, true, func(f *notesArchivePostgresFixture, object KnowledgeObject, lifecycle SourceLifecycle) {
		assertDeclarationEnrollmentArchiveReads(t, f, object, lifecycle)
	})
}

// Shared physical fixture owns real reviewed move/quiescence/recovery/projector
// services and its original privacy/identity/replay assertions. These assertions
// add v0.5 current source membership and supported read surfaces at both stops.
func assertDeclarationEnrollmentArchiveReads(t *testing.T, f *notesArchivePostgresFixture, object KnowledgeObject, lifecycle SourceLifecycle) {
	t.Helper()
	for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
		included, _ := SourceLifecycleIncluded(filter, lifecycle)
		want := 0
		if included {
			want = 1
		}
		objects, err := f.s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{SourceLifecycle: filter, ProjectID: *object.ProjectID})
		if err != nil || len(objects) != want {
			t.Fatalf("physical objects %s/%s: %d %v", lifecycle, filter, len(objects), err)
		}
		roots, err := f.s.store.ListReadableSourceRoots(t.Context(), SourceRootFilter{SourceLifecycle: filter, ProjectID: *object.ProjectID})
		if err != nil || len(roots) != want {
			t.Fatalf("physical roots %s/%s: %d %v", lifecycle, filter, len(roots), err)
		}
		overview, err := f.s.GetNotesOverview(t.Context(), NotesOverviewInput{SourceLifecycle: filter, ProjectID: *object.ProjectID})
		if err != nil || overview.Totals.ObjectCount != want {
			t.Fatalf("physical overview %s/%s: %+v %v", lifecycle, filter, overview.Totals, err)
		}
		search, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "Project decision", ProjectID: *object.ProjectID, Mode: NotesSearchModeLexical, SourceLifecycle: filter})
		if err != nil || search.ResultCount != want {
			t.Fatalf("physical search %s/%s: %+v %v", lifecycle, filter, search, err)
		}
		citation := archiveSurfaceCitation(t, f, object)
		citation.SourceLifecycle = filter
		passage, err := f.s.GetNotesPassage(t.Context(), citation)
		if included {
			if err != nil || passage.Custody.SourceLifecycle != lifecycle || passage.CurrentSourceContext.Declaration != "docs" {
				t.Fatalf("physical citation %s/%s: %+v %v", lifecycle, filter, passage, err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("physical citation inclusion: %v", err)
		}
	}
	projection, err := f.s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range projection {
		if item.KnowledgeObjectID == object.KnowledgeObjectID {
			t.Fatal("inactive writer materialized projection")
		}
	}
	assertNotesCustodyReadRevocations(t, f.s, object, map[string]string{
		"v05_source_revision":     `UPDATE projects.project_contract_registrations SET contract_hash='sha256:'||repeat('0',64) WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"v05_source_removed":      `UPDATE projects.project_contract_registrations SET contract_json=jsonb_set(contract_json,'{resources}','{}') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"v05_owner_plan_removed":  `UPDATE projects.project_contract_registrations SET registration_plan_json=jsonb_set(registration_plan_json,'{watched_roots}','[]') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"v05_config_substitution": `UPDATE projects.project_watched_root_registrations SET config_json=jsonb_set(config_json,'{root_relative_path}','"unrelated"') WHERE project_watched_root_registration_id=(SELECT r.project_watched_root_registration_id FROM knowledge.knowledge_objects o JOIN knowledge.notes_source_roots r USING(notes_source_root_id) WHERE o.knowledge_object_id=$1)`,
		"v05_report_removed":      `UPDATE projects.project_contract_registrations SET validation_report_json=jsonb_set(validation_report_json,'{watched_roots}','[]') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"v05_downgrade":           `UPDATE projects.project_contract_registrations SET contract_schema_version='project.contract.v0.4' WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
	})
}

// D3b integration starts at the actual declaration resolver and owner journal,
// then consumes the real typed node ACK/report before knowledge reconciliation.
func declarationBoundEnrollmentDB(t *testing.T, shared ...*declarationEnrollmentDBFixture) *declarationEnrollmentDBFixture {
	t.Helper()
	var db *sql.DB
	if len(shared) > 0 {
		db = shared[0].db
	} else {
		var url string
		db, url = boxSourcesDatabase(t)
		if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
			t.Fatal(err)
		}
		if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "D3b bound enrollment fixture")
	if err != nil {
		t.Fatal(err)
	}
	f := &declarationEnrollmentDBFixture{db: db, service: NewService(db), projects: projects.NewService(db), req: req, analysis: declarationKnowledgeAnalysis(t)}
	if len(shared) > 0 {
		raw := strings.Replace(string(f.analysis.Loaded.Raw), "slug: declaration-proof", "slug: declaration-proof-b", 1)
		if err = os.WriteFile(f.analysis.Loaded.ContractPath, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		f.analysis = projectcontracts.Analyze(f.analysis.Report.ProjectRoot)
	}
	root := f.analysis.Report.ProjectRoot
	resolver := projectapply.NewLocalResolver(db, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: filepath.Dir(root)}, nil })
	owner := projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	service := projectapply.NewService(db, resolver, projectapply.NewCurrentAuthority(db), map[projectcontracts.DeclarationOwner]projectapply.Owner{projectcontracts.DeclarationOwnerProjects: projectapply.ProjectsOwner{Resolver: resolver, Projects: f.projects}, projectcontracts.DeclarationOwnerKnowledge: owner, projectcontracts.DeclarationOwnerProtection: owner})
	principal := projectapply.Principal{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID}
	plan, err := service.Plan(t.Context(), principal, projectcontracts.DeclarationPlanRequest{SchemaVersion: projectcontracts.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	apply := projectcontracts.DeclarationApplyRequest{SchemaVersion: projectcontracts.DeclarationRequestSchemaV05, ProjectRef: root, PlanID: plan.PlanID, IdempotencyKey: "bound-consumer:" + f.analysis.Report.Project.ID, Effects: []projectcontracts.DeclarationEffect{projectcontracts.DeclarationReconcile}}
	pending, err := service.Apply(t.Context(), principal, apply)
	if failure, ok := err.(*projectapply.Failure); !ok || failure.Cause != "owner_pending" || pending.State != projectcontracts.DeclarationOperationPartial {
		t.Fatalf("pending owner=%+v %v", pending, err)
	}
	var messageID string
	if err = db.QueryRow(`SELECT message_id FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, pending.OperationID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	message, err := communication.NewService(db).GetMessage(t.Context(), messageID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := projectwatch.DecodeDeclarationWatchPayload(message.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store := nodeagent.Store{ConfigPath: filepath.Join(directory, "config.json"), StatePath: filepath.Join(directory, "state.json"), DataDir: filepath.Join(directory, "data")}
	if err = store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveConfig(nodeagent.Config{MainURL: "http://main.test", NodeKey: "main", DisplayName: "Main", BoxRootPath: filepath.Dir(root)}); err != nil {
		t.Fatal(err)
	}
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: payload.Group.NodeID, Reason: "disposable D3b consumer proof"})
	if err != nil {
		t.Fatal(err)
	}
	ack := nodeagent.BuildProjectWatchReconcileAck(t.Context(), store, nodeagent.State{NodeID: payload.Group.NodeID}, message)
	if ack.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("node ACK failed: %s", ack.ResultJSON)
	}
	ack.CredentialToken = credential.CredentialToken
	if _, err = communication.NewService(db).Ack(t.Context(), req, ack); err != nil {
		t.Fatal(err)
	}
	apply.OperationID = pending.OperationID
	done, err := service.Apply(t.Context(), principal, apply)
	if err != nil || done.State != projectcontracts.DeclarationOperationSucceeded {
		t.Fatalf("ack completion=%+v %v", done, err)
	}
	item := payload.Group.Roots[0]
	if _, err = watchedroots.NewService(db).Report(t.Context(), watchedroots.ReportInput{NodeRef: payload.Group.NodeID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: watchedroots.StatusHealthy}); err != nil {
		t.Fatal(err)
	}
	if err = f.projects.CorrelateProjectWatchedRootReports(t.Context(), payload.Group.ProjectID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.projects.ListProjectWatchedRootRegistrations(t.Context(), payload.Group.ProjectID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("producer rows: %v", err)
	}
	r := rows[0]
	f.input = projects.UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: r.ProjectContractRegistrationID, ProjectID: r.ProjectID, NodeID: r.NodeID, OwnerNodeKey: r.OwnerNodeKey, LocalRootKey: r.LocalRootKey, BackendRootKey: r.BackendRootKey, WorkerKey: r.WorkerKey, SourceKinds: r.SourceKinds, SafeRootKey: r.SafeRootKey, RootRelativePath: r.RootRelativePath, DisplayName: r.DisplayName, SyncMode: r.SyncMode, BackupMode: r.BackupMode, IndexMode: r.IndexMode, DeleteMode: r.DeleteMode, ConfigHash: r.ConfigHash, ConfigJSON: r.ConfigJSON, CommandJSON: r.CommandJSON, ActivationStatus: r.ActivationStatus, Metadata: r.Metadata}
	return f
}
func TestDeclarationBoundEnrollmentAdmissionPostgres(t *testing.T) {
	f := declarationBoundEnrollmentDB(t)
	object := f.publish(t)
	f.visible(t, 1)
	original := f.input
	for _, name := range []string{"foreign_key", "extra_config", "compiler_hash", "effective_hash", "missing_sources", "changed_sources", "missing_adapter", "disabled", "stale", "report"} {
		t.Run(name, func(t *testing.T) {
			changed := original
			switch name {
			case "foreign_key":
				changed.SafeRootKey = "declaration_" + strings.ToLower(ids.NewProjectID())
			case "extra_config":
				var value map[string]json.RawMessage
				_ = json.Unmarshal(changed.ConfigJSON, &value)
				value["display_name"] = json.RawMessage(`"forged"`)
				changed.ConfigJSON = mustJSON(t, value)
			case "compiler_hash", "effective_hash":
				var outer map[string]json.RawMessage
				_ = json.Unmarshal(changed.Metadata, &outer)
				var adapter map[string]json.RawMessage
				_ = json.Unmarshal(outer["declaration_adapter"], &adapter)
				key := "compiler_config_hash"
				if name == "effective_hash" {
					key = "effective_config_hash"
				}
				adapter[key] = mustJSON(t, "sha256:"+strings.Repeat("0", 64))
				outer["declaration_adapter"] = mustJSON(t, adapter)
				changed.Metadata = mustJSON(t, outer)
			case "missing_sources", "changed_sources", "missing_adapter":
				var outer map[string]json.RawMessage
				_ = json.Unmarshal(changed.Metadata, &outer)
				if name == "missing_adapter" {
					delete(outer, "declaration_adapter")
				} else if name == "changed_sources" {
					var sources []projectcontracts.DeclarationSourceSnapshot
					if err := json.Unmarshal(outer["declaration_sources"], &sources); err != nil || len(sources) == 0 {
						t.Fatal("source fixture missing")
					}
					sources[0].Raw = append(sources[0].Raw, ' ')
					outer["declaration_sources"] = mustJSON(t, sources)
				} else {
					outer["declaration_sources"] = json.RawMessage(`[]`)
				}
				changed.Metadata = mustJSON(t, outer)
			case "disabled", "stale":
				changed.ActivationStatus = name
			case "report":
				if _, err := f.db.Exec(`UPDATE watched_roots.roots SET config_hash=$2 WHERE root_key=$1`, changed.BackendRootKey, "sha256:"+strings.Repeat("0", 64)); err != nil {
					t.Fatal(err)
				}
			}
			if name != "report" {
				f.upsert(t, changed)
			}
			f.visible(t, 0)
			f.admit(t)
			if _, err := f.db.Exec(`UPDATE watched_roots.roots SET config_hash=$2 WHERE root_key=$1`, original.BackendRootKey, original.ConfigHash); err != nil {
				t.Fatal(err)
			}
			f.upsert(t, original)
			f.correlate(t)
			f.reconcile(t, SourceRootStatusActive)
			f.visible(t, 1)
		})
	}
	if _, err := f.db.Exec(`UPDATE projects.projects SET status='archived' WHERE project_id=$1`, f.input.ProjectID); err != nil {
		t.Fatal(err)
	}
	f.visible(t, 0)
	var retained int
	if err := f.db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("retained object=%d %v", retained, err)
	}
}

func TestDeclarationBoundEnrollmentTwoProjectsPostgres(t *testing.T) {
	a := declarationBoundEnrollmentDB(t)
	b := declarationBoundEnrollmentDB(t, a)
	result, err := a.service.ReconcileSourceRoots(t.Context(), SourceRootReconcileInput{DiscoverFromStore: true})
	if err != nil || len(result.SourceRoots) != 2 {
		t.Fatalf("two project roots: %+v %v", result, err)
	}
	for _, root := range result.SourceRoots {
		if root.Status != SourceRootStatusActive || root.ProjectID == nil {
			t.Fatalf("bound root: %+v", root)
		}
		body := []byte("Cobalt observatory measures the shared relative journal.\n")
		if err = os.WriteFile(filepath.Join(root.SourcePath, "source.md"), body, 0600); err != nil {
			t.Fatal(err)
		}
		size := int64(len(body))
		if _, err = storagecatalog.NewService(a.db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaProjects, OriginNodeID: a.input.NodeID, OriginNodeKey: "main", ProjectID: *root.ProjectID, WatchedRootKey: root.BackendRootKey, LogicalPath: "source.md", OriginalSourcePath: "source.md", SizeBytes: &size, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(hashArtifactValue(string(body)), "sha256:"), MimeType: "text/markdown", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: storagecatalog.AvailabilityStateAvailable}); err != nil {
			t.Fatal(err)
		}
	}
	a.admit(t)
	for step := 0; step < 30; step++ {
		r, err := a.service.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "bound-two-projects", Limit: 10, Now: time.Now().UTC()})
		if err != nil || r.Failed != 0 {
			t.Fatalf("pipeline: %+v %v", r, err)
		}
		if r.Claimed == 0 {
			break
		}
		if step == 29 {
			t.Fatal("pipeline did not converge")
		}
	}
	for _, f := range []*declarationEnrollmentDBFixture{a, b} {
		search, err := f.service.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", SourceCategory: "projects", ProjectID: f.input.ProjectID, Mode: NotesSearchModeLexical})
		if err != nil || len(search.Results) != 1 {
			t.Fatalf("project search: %+v %v", search, err)
		}
		objects, err := f.service.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{ProjectID: f.input.ProjectID})
		if err != nil || len(objects) != 1 {
			t.Fatalf("project objects: %+v %v", objects, err)
		}
		hit := search.Results[0]
		if hit.KnowledgeObjectID != objects[0].KnowledgeObjectID {
			t.Fatal("cross-project search admission")
		}
		citation := NotesPassageInput{KnowledgeObjectID: hit.KnowledgeObjectID, KnowledgeObjectVersionID: hit.KnowledgeObjectVersionID, KnowledgeChunkID: hit.KnowledgeChunkID, SourceHash: objects[0].SourceHash}
		if _, err = f.service.GetNotesPassage(t.Context(), citation); err != nil {
			t.Fatal(err)
		}
	}
	// A foreign scoped key closes only that project's current visibility.
	if _, err = a.db.Exec(`UPDATE projects.project_watched_root_registrations SET safe_root_key=$2 WHERE project_id=$1`, a.input.ProjectID, b.input.SafeRootKey); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*declarationEnrollmentDBFixture{a, b} {
		search, err := f.service.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", ProjectID: f.input.ProjectID, Mode: NotesSearchModeLexical})
		want := 1
		if f == a {
			want = 0
		}
		if err != nil || len(search.Results) != want {
			t.Fatalf("project isolation want %d: %+v %v", want, search, err)
		}
	}
}

type legacyNotesObservedOwner struct {
	projectapply.Owner
	after func(projectapply.ActionCall)
}

func (o legacyNotesObservedOwner) Apply(ctx context.Context, call projectapply.ActionCall) (projectapply.Observation, error) {
	observed, err := o.Owner.Apply(ctx, call)
	if err == nil && o.after != nil {
		o.after(call)
	}
	return observed, err
}

func TestDeclarationLegacyNotesContinuityPostgres(t *testing.T) {
	for _, status := range []string{"healthy", "active"} {
		t.Run(status, func(t *testing.T) { testDeclarationLegacyNotesContinuity(t, status) })
	}
}
func testDeclarationLegacyNotesContinuity(t *testing.T, predecessorStatus string) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "D4e Notes continuity fixture")
	if err != nil {
		t.Fatal(err)
	}
	box := t.TempDir()
	root := filepath.Join(box, "legacy")
	id := ids.NewProjectID()
	write := func(ref, raw string) {
		t.Helper()
		path := filepath.Join(root, ref)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject: {id: %s, slug: legacy, name: Legacy, owner_node: main, status: active}\nfacets: {notes: true, backup_policy: true}\n", id)
	notes := "kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes: {status: active, sync: true, index: true, backup: true, root_key: notes, path: ., include: ['**/*'], exclude: ['credentials/**']}\n"
	backup := "kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup:\n  enabled: true\n  defaults: {max_file_bytes: 4096, max_batch_bytes: 8192, max_pending_bytes: 9007199254740993}\n  roots:\n    - {key: notes, path: notes}\n    - {key: controls, path: ., include: ['.loom/**']}\n"
	write(projectcontracts.CanonicalRootContractPath, old)
	write(".loom/contracts/notes.yaml", notes)
	write(".loom/contracts/backup.yaml", backup)
	write("notes/.fixture", "owned")
	f := &declarationEnrollmentDBFixture{db: db, service: NewService(db), projects: projects.NewService(db), req: req, analysis: projectcontracts.Analyze(root)}
	input, err := projectregistration.BuildInput(f.analysis, "legacy-notes-fixture")
	if err != nil {
		t.Fatal(err)
	}
	registered, err := f.projects.RegisterProjectContract(t.Context(), req, input)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: req.OriginNodeID, Reason: "owned D4e fixture"})
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	nodeStore := nodeagent.Store{ConfigPath: filepath.Join(base, "config.json"), StatePath: filepath.Join(base, "state.json"), DataDir: filepath.Join(base, "data")}
	if err = nodeStore.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	safe := filesystemconnector.DefaultSafeRoot("project", root)
	safe.MaxFileBytes = 1024 * 1024 * 1024
	safe.IncludeHiddenDefault = true
	if err = nodeStore.SaveConfig(nodeagent.Config{NodeKey: "main", DisplayName: "Main", MainURL: "http://main.test", BoxRootPath: box, Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{safe}}}); err != nil {
		t.Fatal(err)
	}
	for _, w := range f.analysis.Plan.WatchedRoots {
		row := projects.UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: registered.Detail.Registration.ProjectContractRegistrationID, ProjectID: id, NodeID: req.OriginNodeID, OwnerNodeKey: "main", LocalRootKey: w.Key, BackendRootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, SourceKinds: mustJSON(t, w.SourceKinds), SafeRootKey: w.SafeRootKey, RootRelativePath: w.RootRelativePath, DisplayName: w.DisplayName, SyncMode: w.SyncMode, BackupMode: w.BackupMode, IndexMode: w.IndexMode, DeleteMode: w.DeleteMode, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CommandJSON: json.RawMessage(`[]`), ActivationStatus: "applied", Metadata: mustJSON(t, w.Metadata)}
		f.upsert(t, row)
		if w.Key == "notes" {
			f.input = row
		}
		if _, err = watchedroots.NewService(db).Report(t.Context(), watchedroots.ReportInput{NodeRef: req.OriginNodeID, CredentialToken: credential.CredentialToken, RootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, DisplayName: w.DisplayName, SafeRootKey: w.SafeRootKey, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, Status: predecessorStatus}); err != nil {
			t.Fatal(err)
		}
		if err = noderuntime.NewStore(nodeStore.DataDir).SaveInstance(noderuntime.WorkerInstance{WorkerKey: w.WorkerKey, Kind: noderuntime.KindWatchedRoot, DisplayName: w.DisplayName, Enabled: true, IntervalSeconds: 83, LeaseTimeoutSeconds: 157, LocalRootKey: w.Key, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON}); err != nil {
			t.Fatal(err)
		}
		if err = localwatch.NewStore(nodeStore.DataDir).EnsureRoot(w.BackendRootKey); err != nil {
			t.Fatal(err)
		}
	}
	f.correlate(t)
	object := publishLegacyNotesFixture(t, f)
	f.visible(t, 1)
	originalRoot := f.reconcile(t, SourceRootStatusActive)
	originalCitation := f.citation
	snapshot := func() string {
		t.Helper()
		var raw []byte
		err := db.QueryRow(`SELECT jsonb_build_object('objects',(SELECT jsonb_agg(to_jsonb(o) ORDER BY knowledge_object_id) FROM knowledge.knowledge_objects o),'versions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY knowledge_object_version_id) FROM knowledge.knowledge_object_versions v),'custody',(SELECT jsonb_agg(to_jsonb(c) ORDER BY knowledge_object_id) FROM knowledge.notes_current_custody c),'transitions',(SELECT jsonb_agg(to_jsonb(c)) FROM knowledge.notes_custody_transitions c))`).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()
	check := func(stage string) {
		t.Helper()
		t.Log("continuity phase:", stage)
		f.visible(t, 1)
		assertNotesCustodyReadable(t, f.service, object.KnowledgeObjectID, true)
		afterRoot := f.reconcile(t, SourceRootStatusActive)
		if afterRoot.NotesSourceRootID != originalRoot.NotesSourceRootID || afterRoot.RootKind != RootKindProjectNotes || afterRoot.BackendRootKey != originalRoot.BackendRootKey || afterRoot.SourcePath != originalRoot.SourcePath || *afterRoot.ProjectWatchedRootRegistrationID != *originalRoot.ProjectWatchedRootRegistrationID {
			t.Fatal("Notes owner identity changed")
		}
		f.visible(t, 1)
		assertNotesCustodyReadable(t, f.service, object.KnowledgeObjectID, true)
		if f.citation != originalCitation || snapshot() != before {
			t.Fatal("existing object/version/citation/custody changed")
		}
	}
	hash := func(s string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(s))) }
	write(".loom/contracts/retained.yaml", old)
	write(projectcontracts.CanonicalRootContractPath, fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: %s, slug: legacy, name: Legacy, owner_node: main, status: active}\nresources:\n  notes_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: notes}}\n  controls_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: .}}\nlegacy_contracts:\n  project: {ref: .loom/contracts/retained.yaml, schema_version: project.contract.v0.4, digest: %s}\n  notes: {key: imported_notes, ref: .loom/contracts/notes.yaml, schema_version: notes.contract.v0.3, digest: %s, protection: notes_backup}\n", id, hash(old), hash(notes)))
	resolver := projectapply.NewLocalResolver(db, func() (config.Config, error) { return config.Config{NodeID: "main", BoxPath: box}, nil })
	watch := projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	hook := func(call projectapply.ActionCall) { check(string(call.Action.ID)) }
	service := projectapply.NewService(db, resolver, projectapply.NewCurrentAuthority(db), map[projectcontracts.DeclarationOwner]projectapply.Owner{projectcontracts.DeclarationOwnerProjects: legacyNotesObservedOwner{projectapply.ProjectsOwner{Resolver: resolver, Projects: f.projects}, hook}, projectcontracts.DeclarationOwnerKnowledge: legacyNotesObservedOwner{watch, hook}, projectcontracts.DeclarationOwnerProtection: legacyNotesObservedOwner{watch, hook}})
	principal := projectapply.Principal{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID}
	plan, err := service.Plan(t.Context(), principal, projectcontracts.DeclarationPlanRequest{SchemaVersion: projectcontracts.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	apply := projectcontracts.DeclarationApplyRequest{SchemaVersion: projectcontracts.DeclarationRequestSchemaV05, ProjectRef: root, PlanID: plan.PlanID, IdempotencyKey: "legacy-notes", Effects: []projectcontracts.DeclarationEffect{projectcontracts.DeclarationReconcile}}
	pending, err := service.Apply(t.Context(), principal, apply)
	if failure, ok := err.(*projectapply.Failure); !ok || failure.Cause != "owner_pending" {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	check("pending delivery")
	var messageID string
	if err = db.QueryRow(`SELECT message_id FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, pending.OperationID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	message, err := communication.NewService(db).GetMessage(t.Context(), messageID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := projectwatch.DecodeDeclarationWatchPayload(message.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	interruptedContext := legacyNotesInterruptAfterWorker{Context: t.Context(), store: noderuntime.NewStore(nodeStore.DataDir), workerKey: payload.Group.Roots[0].WorkerKey, successorHash: payload.Group.Roots[0].ConfigHash}
	interrupted := nodeagent.BuildProjectWatchReconcileAck(interruptedContext, nodeStore, nodeagent.State{NodeID: req.OriginNodeID}, message)
	if interrupted.AckStatus != communication.AckStatusFailedRetryable {
		t.Fatalf("partial node interruption: %s", interrupted.ResultJSON)
	}
	pendingLocal, err := localwatch.NewStore(nodeStore.DataDir).LoadProjectWatchReconcileState(id)
	if err != nil || pendingLocal.Legacy == nil || pendingLocal.PendingRevision != payload.Evidence.DesiredRevision || pendingLocal.AppliedRevision != 0 {
		t.Fatalf("pending local proof: %+v %v", pendingLocal, err)
	}
	changed := 0
	for _, item := range payload.Group.Roots {
		actual, e := noderuntime.NewStore(nodeStore.DataDir).LoadInstance(item.WorkerKey)
		if e != nil {
			t.Fatal(e)
		}
		if actual.ConfigHash == item.ConfigHash {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("expected one actual successor, got %d", changed)
	}
	check("mixed node application, exact pending proof")
	localRaw, _ := json.MarshalIndent(pendingLocal, "", "  ")
	t.Logf("full node pending bytes=%d; control bytes=%d", len(localRaw)+1, len(message.PayloadJSON))

	ack := nodeagent.BuildProjectWatchReconcileAck(t.Context(), nodeStore, nodeagent.State{NodeID: req.OriginNodeID}, message)
	if ack.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("node: %s", ack.ResultJSON)
	}
	check("node configured, ACK lost")
	ack.CredentialToken = credential.CredentialToken
	if _, err = communication.NewService(db).Ack(t.Context(), req, ack); err != nil {
		t.Fatal(err)
	}
	done, err := service.Apply(t.Context(), principal, apply)
	if err != nil || done.State != projectcontracts.DeclarationOperationSucceeded {
		t.Fatalf("completion: %+v %v", done, err)
	}
	check("completed ACK, old report")
	for _, w := range payload.Group.Roots {
		var cfg localwatch.RootConfig
		if err = json.Unmarshal(w.ConfigJSON, &cfg); err != nil {
			t.Fatal(err)
		}
		if _, err = watchedroots.NewService(db).Report(t.Context(), watchedroots.ReportInput{NodeRef: req.OriginNodeID, CredentialToken: credential.CredentialToken, RootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, DisplayName: cfg.DisplayName, SafeRootKey: cfg.SafeRootKey, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, Status: watchedroots.StatusHealthy, Metadata: mustJSON(t, map[string]any{"source": "loom-node-agent", "runtime_worker": w.WorkerKey, "root_reachable": true, "safe_root_key": cfg.SafeRootKey, "root_relative_path": cfg.RootRelativePath})}); err != nil {
			t.Fatal(err)
		}
	}
	f.correlate(t)
	check("successor reported")
	assertNotesCustodyReadRevocations(t, f.service, object, map[string]string{
		"registration_source":   `UPDATE projects.project_contract_registrations SET contract_hash='sha256:'||repeat('f',64) WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"declaration_intent":    `UPDATE projects.project_contract_registrations SET contract_json=jsonb_set(contract_json,'{project,name}','"forged"'::jsonb) WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"forged_registerable":   `UPDATE projects.project_contract_registrations SET validation_report_json=jsonb_set(validation_report_json,'{registerable}','true'::jsonb) WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"plan_watch_set":        `UPDATE projects.project_contract_registrations SET registration_plan_json=jsonb_set(registration_plan_json,'{watched_roots}','[]'::jsonb) WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"duplicate_sources":     `UPDATE projects.project_contract_registrations SET registration_plan_json=jsonb_set(registration_plan_json,'{declaration,sources,1}',registration_plan_json#>'{declaration,sources,0}'),validation_report_json=jsonb_set(validation_report_json,'{declaration,sources,1}',validation_report_json#>'{declaration,sources,0}') WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"retained_sources":      `UPDATE projects.project_contract_registrations SET registration_plan_json=jsonb_set(registration_plan_json,'{declaration,sources}','[]'::jsonb),validation_report_json=jsonb_set(validation_report_json,'{declaration,sources}','[]'::jsonb) WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"root_kind":             `UPDATE knowledge.notes_source_roots SET root_kind='project_material' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"root_path":             `UPDATE knowledge.notes_source_roots SET source_path=source_path||'/other' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"root_blocked":          `UPDATE knowledge.notes_source_roots SET status='blocked' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"disabled_owner":        `UPDATE projects.project_watched_root_registrations SET activation_status='disabled' WHERE project_id=(SELECT project_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"report_status":         `UPDATE watched_roots.roots SET status='degraded' WHERE node_id=(SELECT source_node_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"report_generation":     `UPDATE watched_roots.roots SET last_reported_at='2001-01-01'::timestamptz WHERE node_id=(SELECT source_node_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"report_config":         `UPDATE watched_roots.roots SET config_hash='sha256:'||repeat('f',64) WHERE node_id=(SELECT source_node_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"report_root_reachable": `UPDATE watched_roots.roots SET metadata=metadata||'{"root_reachable":false}'::jsonb WHERE node_id=(SELECT source_node_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
	})

}

func publishLegacyNotesFixture(t *testing.T, f *declarationEnrollmentDBFixture) KnowledgeObject {
	t.Helper()
	root := f.reconcile(t, SourceRootStatusActive)
	body := []byte("# Cobalt observatory\nThe cobalt observatory measures the journal.\n")
	if err := os.WriteFile(filepath.Join(root.SourcePath, "source.md"), body, 0600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(body))
	if _, err := storagecatalog.NewService(f.db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaProjects, OriginNodeID: f.input.NodeID, OriginNodeKey: "main", ProjectID: f.input.ProjectID, WatchedRootKey: root.BackendRootKey, LogicalPath: "source.md", OriginalSourcePath: "source.md", SizeBytes: &size, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(hashArtifactValue(string(body)), "sha256:"), MimeType: "text/markdown", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: storagecatalog.AvailabilityStateAvailable}); err != nil {
		t.Fatal(err)
	}
	f.admit(t)
	objects, err := f.service.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{})
	if err != nil || len(objects) != 1 {
		t.Fatalf("legacy admission: %+v %v", objects, err)
	}
	f.sourceHash = objects[0].SourceHash
	for i := 0; i < 30; i++ {
		r, err := f.service.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "d4e-fixture", Limit: 10, Now: time.Now().UTC()})
		if err != nil || r.Failed != 0 {
			t.Fatalf("legacy pipeline: %+v %v", r, err)
		}
		if r.Claimed == 0 {
			return objects[0]
		}
	}
	t.Fatal("legacy pipeline did not converge")
	return KnowledgeObject{}
}

// Cancel between real worker writes, after the first selected worker has been
// persisted. No control, worker or pending record is fabricated for recovery.
type legacyNotesInterruptAfterWorker struct {
	context.Context
	store                    noderuntime.Store
	workerKey, successorHash string
}

func (c legacyNotesInterruptAfterWorker) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	w, err := c.store.LoadInstance(c.workerKey)
	if err == nil && w.ConfigHash == c.successorHash {
		return context.Canceled
	}
	return nil
}
