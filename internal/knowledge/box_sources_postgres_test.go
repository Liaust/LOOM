package knowledge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/watchedroots"
)

// Each test owns a new database; the supplied local admin database is never migrated.

func TestBoxSyncedMetadataObservationPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	db, ctx := s.store.db, t.Context()
	// The admission helper uses synthetic lowercase root labels. This test also
	// exercises projection, whose declared Box area paths are case-sensitive.
	for i := range roots {
		area := map[string]string{RootKindBoxNotes: "Notes", RootKindBoxTopics: "Topics", RootKindBoxLibrary: "Library"}[roots[i].RootKind]
		var metadata map[string]any
		if err := json.Unmarshal(roots[i].Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		metadata["registration_metadata"].(map[string]any)["knowledge_source"].(map[string]any)["root_relative_path"] = area
		roots[i].RootRelativePath, roots[i].Metadata = area, mustJSON(t, metadata)
		root, err := s.store.UpsertSourceRoot(ctx, roots[i])
		if err != nil {
			t.Fatal(err)
		}
		roots[i] = root
	}
	old := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	initial := mustJSON(t, map[string]any{"source_mtime": old, "source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime})
	for _, query := range []string{`UPDATE objects.objects SET metadata=metadata || $1::jsonb`,
		`UPDATE objects.object_versions SET metadata=metadata || $1::jsonb`, `UPDATE files.file_metadata SET metadata=metadata || $1::jsonb`} {
		if _, err := db.Exec(query, initial); err != nil {
			t.Fatal(err)
		}
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	if len(objects) != 3 || advanceBoxSyncedFixture(t, s) == 0 {
		t.Fatal("initial source processing absent")
	}
	counts := func() [4]int {
		t.Helper()
		var count [4]int
		for i, table := range []string{"knowledge.knowledge_object_versions", "knowledge.pipeline_runs", "knowledge.derived_artifacts", "knowledge.knowledge_chunks"} {
			if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count[i]); err != nil {
				t.Fatal(err)
			}
		}
		return count
	}
	before := counts()
	projectionRoot := filepath.Join(t.TempDir(), "generated")
	t.Cleanup(func() {
		if err := filepath.WalkDir(projectionRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	projection := notesprojection.NewService(s, projectionRoot)
	if changed, err := projection.Refresh(ctx); err != nil || !changed {
		t.Fatalf("initial projection %t %v", changed, err)
	}
	generated := map[string]os.FileInfo{}
	if err := filepath.WalkDir(projectionRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		generated[path] = info
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, mtime := range []time.Time{old.Add(time.Hour), old.Add(-time.Hour), old} {
		// Change only authoritative current-source observations. Receiver transport
		// has its own real-DB test; the external smoke exercises the owner end to end.
		patch := mustJSON(t, map[string]any{"source_mtime": mtime, "source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime,
			"source_metadata_sequence": i + 1, "source_metadata_owner": *roots[0].NodeID})
		if _, err := db.Exec(`UPDATE files.file_metadata SET metadata=metadata || $1::jsonb,source_mtime=$2`, patch, mtime); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE objects.objects SET metadata=metadata || $1::jsonb`, patch); err != nil {
			t.Fatal(err)
		}
		for _, object := range objects {
			if _, err := s.store.GetKnowledgeObject(ctx, object.KnowledgeObjectID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("stale observation visible: %v", err)
			}
		}
		current := reconcileBoxSyncedFixture(t, s, roots)
		for j, object := range current {
			if object.KnowledgeObjectID != objects[j].KnowledgeObjectID || object.SourceRevision != objects[j].SourceRevision ||
				object.SourceModifiedAt == nil || !object.SourceModifiedAt.Equal(mtime) || !object.RecencyAt.Equal(mtime) {
				t.Fatalf("current metadata not refreshed: %+v", object)
			}
		}
		if got := advanceBoxSyncedFixture(t, s); got != 0 {
			t.Fatalf("metadata caused %d processing stages", got)
		}
		if after := counts(); after != before {
			t.Fatalf("metadata created content work %v -> %v", before, after)
		}
		if changed, err := projection.Refresh(ctx); err != nil || changed {
			t.Fatalf("metadata recopied projection %t %v", changed, err)
		}
		for path, before := range generated {
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
				t.Fatalf("metadata changed generated entry %s: %v", path, err)
			}
		}
		var changed int
		if err := db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_object_versions WHERE source_modified_at IS DISTINCT FROM $1`, old).Scan(&changed); err != nil || changed != 0 {
			t.Fatalf("immutable version time changed %d %v", changed, err)
		}
		reconcileBoxSyncedFixture(t, s, roots)
		if advanceBoxSyncedFixture(t, s) != 0 || counts() != before {
			t.Fatal("unchanged replay repeated derived work")
		}
	}
}

func boxSourcesDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local disposable PostgreSQL administrator LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	socket := u.Query().Get("host")
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "" && strings.HasPrefix(socket, "/tmp/")) {
		t.Fatal("Box source acceptance requires a local disposable database endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "box_sources_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
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
	return db, u.String()
}

func TestBoxSourcesMigrationUpgradeAndLegacyReplayPostgres(t *testing.T) {
	db, _ := boxSourcesDatabase(t)
	dir := filepath.Join("..", "..", "migrations")
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(t.Context(), db, dir, 63); err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	legacy, err := service.PrepareSourceRoot(SourceRoot{RootKind: RootKindBoxNotes, NodeKey: "main", BackendRootKey: "loom_box__notes", SourcePath: "/fixture/Notes"})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err = service.store.UpsertSourceRoot(t.Context(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(t.Context(), db, dir, 64); err != nil {
		t.Fatal(err)
	}
	replay := legacy
	replay.NotesSourceRootID = ids.NewNotesSourceRootID()
	replay, err = service.store.UpsertSourceRoot(t.Context(), replay)
	if err != nil || replay.NotesSourceRootID != legacy.NotesSourceRootID || !replay.CreatedAt.Equal(legacy.CreatedAt) {
		t.Fatalf("legacy identity changed: %#v %v", replay, err)
	}
	if _, err := db.Exec(`UPDATE knowledge.notes_source_roots SET root_kind='project_material' WHERE notes_source_root_id=$1`, legacy.NotesSourceRootID); err == nil {
		t.Fatal("accepted unowned project material")
	}
	if _, err := db.Exec(`UPDATE knowledge.notes_source_roots SET root_kind='box_topics' WHERE notes_source_root_id=$1`, legacy.NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownContext(t.Context(), db, dir); err == nil {
		t.Fatal("downgrade discarded expanded source support")
	}
	var kind string
	if err := db.QueryRow(`SELECT root_kind FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1`, legacy.NotesSourceRootID).Scan(&kind); err != nil || kind != RootKindBoxTopics {
		t.Fatalf("failed downgrade changed history: %s %v", kind, err)
	}
}

func TestBoxSourcesRegisteredAdmissionPostgres(t *testing.T) {
	runBoxSourcesRegisteredAdmission(t, nil)
}

func TestBoxSyncedSearchProjectionPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	for _, object := range objects {
		result, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", SourceCategory: object.SourceCategory, Mode: NotesSearchModeLexical})
		if err != nil || len(result.Results) != 1 || result.Results[0].KnowledgeObjectID != object.KnowledgeObjectID {
			t.Fatalf("category search: %+v %v", result, err)
		}
	}
	projection, err := s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(projection) != 3 {
		t.Fatalf("projection: %+v %v", projection, err)
	}
	if _, err := s.store.db.Exec(`UPDATE sync.replicas SET freshness_state='stale'`); err != nil {
		t.Fatal(err)
	}
	result, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", Mode: NotesSearchModeLexical})
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("stale search: %+v %v", result, err)
	}
	projection, err = s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(projection) != 0 {
		t.Fatalf("stale projection: %+v %v", projection, err)
	}
}

func runBoxSourcesRegisteredAdmission(t *testing.T, exercise func(*sql.DB, string, *Service, []SourceRoot)) {
	db, dbURL := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), dbURL, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	ctx, req := t.Context(), requestctx.Context{ActorID: ids.NewActorID()}
	if _, err := db.ExecContext(ctx, `INSERT INTO identity.actors (actor_id,actor_key,display_name,actor_kind,status) VALUES ($1,'box-source-test','Disposable operator','human','active')`, req.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES ($1,'main','Disposable Main','server','main','native','active')`, ids.NewNodeID()); err != nil {
		t.Fatal(err)
	}
	node, err := nodes.NewService(db).GetNode(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	req.OriginNodeID, req.OriginNodeKey = node.NodeID, node.NodeKey
	credential, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: node.NodeID, Reason: "disposable Box source acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := box.Resolved{RootPath: filepath.Join(t.TempDir(), "Box"), Profile: box.ProfileMain, OwnerNode: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	contract, err := box.LoadContract(box.ContractPath(resolved.RootPath))
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, area := range []string{"topics", "library"} {
		contract.Policies[area] = ".loom/policies/" + area + ".yaml"
		write(filepath.Join(resolved.RootPath, contract.Policies[area]), []byte("schema_version: loom.box.watch_policy.v0.6\narea: "+area+"\nmode: watched_root\nenabled: true\ntext:\n  enabled: true\n"))
	}
	raw, err := yaml.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	write(box.ContractPath(resolved.RootPath), raw)
	boxService := box.NewService(db)
	applied, err := boxService.ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Resolved: resolved})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	first, err := service.ReconcileSourceRoots(ctx, SourceRootReconcileInput{DiscoverFromStore: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range first.SourceRoots {
		if (root.RootKind == RootKindBoxTopics || root.RootKind == RootKindBoxLibrary) && root.Status != SourceRootStatusBlocked {
			t.Fatal("unreported root became active")
		}
	}
	report := func(item projectcontracts.ProjectWatchedRootItem) {
		t.Helper()
		if _, err := watchedroots.NewService(db).Report(ctx, watchedroots.ReportInput{NodeRef: node.NodeID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: watchedroots.StatusHealthy}); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range applied.Plan.WatchedRoots {
		report(item)
	}
	reported, err := service.ReconcileSourceRoots(ctx, SourceRootReconcileInput{DiscoverFromStore: true})
	if err != nil {
		t.Fatal(err)
	}
	activeBoxRoots := 0
	for _, root := range reported.SourceRoots {
		if root.RootKind == RootKindBoxNotes || root.RootKind == RootKindBoxTopics || root.RootKind == RootKindBoxLibrary {
			if root.Status != SourceRootStatusActive {
				t.Fatalf("ordinary owner report alone did not activate %s: %s", root.RootKind, root.Status)
			}
			activeBoxRoots++
		}
	}
	if activeBoxRoots != 3 {
		t.Fatalf("reported Box roots = %d, want 3", activeBoxRoots)
	}

	project, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{Name: "Box Sources", Slug: "box-sources", OwnerNode: "main", Preset: projectcontracts.PresetResearch, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(project.ProjectRoot, ".loom/contracts/notes.yaml"), []byte("kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes:\n  status: draft\n  sync: true\n  index: true\nmaterial:\n  - key: docs\n    category: docs\n    path: docs\n    enabled: true\n  - key: research\n    category: research\n    path: research\n    enabled: true\n"))
	analysis := projectcontracts.Analyze(project.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatal(analysis.Report.Diagnostics)
	}
	projectService := projects.NewService(db)
	registered, err := projectService.RegisterProjectContract(ctx, req, projects.RegisterProjectContractInput{
		ProjectRoot: project.ProjectRoot, ContractPath: analysis.Loaded.ContractPath, ContractHash: hashArtifactValue(string(analysis.Loaded.Raw)), ContractSchemaVersion: analysis.Loaded.Contract.SchemaVersion,
		Contract: mustJSON(t, analysis.Loaded.Contract), ValidationReport: mustJSON(t, analysis.Report), RegistrationPlan: mustJSON(t, analysis.Plan),
		Project: projects.ProjectContractProjectInput{ID: analysis.Loaded.Contract.Project.ID, Slug: "box-sources", Name: "Box Sources", OwnerNode: "main", Status: analysis.Loaded.Contract.Project.Status},
		Facets:  []projects.ProjectContractFacetInput{{Key: "notes", Enabled: true, Present: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectID := registered.Detail.Project.Project.ProjectID
	materialInputs := []projects.UpsertProjectWatchedRootRegistrationInput{}
	for _, item := range analysis.Report.WatchedRoots {
		if !containsSourceKind(item.SourceKinds, RootKindProjectMaterial) {
			continue
		}
		input := projects.UpsertProjectWatchedRootRegistrationInput{ProjectContractRegistrationID: registered.Detail.Registration.ProjectContractRegistrationID, ProjectID: projectID, NodeID: node.NodeID, OwnerNodeKey: "main", LocalRootKey: item.Key, BackendRootKey: item.BackendRootKey, SourceKinds: mustJSON(t, item.SourceKinds), SafeRootKey: item.SafeRootKey, RootRelativePath: item.RootRelativePath, DisplayName: item.DisplayName, SyncMode: item.SyncMode, BackupMode: item.BackupMode, IndexMode: item.IndexMode, DeleteMode: item.DeleteMode, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, ActivationStatus: "pending_agent_apply", Metadata: mustJSON(t, item.Metadata)}
		if _, err := projectService.UpsertProjectWatchedRootRegistration(ctx, req, input); err != nil {
			t.Fatal(err)
		}
		materialInputs = append(materialInputs, input)
		report(item)
	}
	if len(materialInputs) != 2 {
		t.Fatalf("missing project declarations: %#v", analysis.Report)
	}
	if err := projectService.CorrelateProjectWatchedRootReports(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReconcileSourceRoots(ctx, SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
		t.Fatal(err)
	}
	roots, err := service.store.ListSourceRoots(ctx, SourceRootFilter{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.NewService(db)
	expanded := []SourceRoot{}
	for _, root := range roots {
		if root.RootKind == RootKindBoxNotes {
			continue
		}
		expanded = append(expanded, root)
		area := storagecatalog.SourceAreaExternalWatchedRoot
		if root.RootKind == RootKindProjectMaterial {
			area = storagecatalog.SourceAreaProjects
		}
		for _, name := range []string{"source.md", "credentials/secret.md"} {
			body := []byte("# Disposable narrative source\n")
			write(filepath.Join(root.SourcePath, name), body)
			size := int64(len(body))
			if _, err := catalog.RegisterEntry(ctx, storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: area, OriginNodeID: node.NodeID, OriginNodeKey: "main", ProjectID: valueOrEmpty(root.ProjectID), WatchedRootKey: root.BackendRootKey, LogicalPath: name, OriginalSourcePath: name, SizeBytes: &size, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(hashArtifactValue(string(body)), "sha256:"), MimeType: "text/markdown", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: storagecatalog.AvailabilityStateAvailable}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(expanded) != 4 {
		t.Fatalf("want Topics, Library, docs, research: %#v", roots)
	}
	// An unrelated external root must not enter the bounded candidate page at all.
	if _, err := catalog.RegisterEntry(ctx, storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaExternalWatchedRoot, OriginNodeID: node.NodeID, OriginNodeKey: "main", WatchedRootKey: "unregistered", LogicalPath: "ignore.md", FileClass: storagecatalog.FileClassMarkdown}); err != nil {
		t.Fatal(err)
	}
	for _, noise := range []storagecatalog.RegisterEntryInput{
		{OriginNodeID: node.NodeID, OriginNodeKey: "main", ProjectID: projectID, LogicalPath: "wrong-project.md"},
		{OriginNodeID: node.NodeID, OriginNodeKey: "other", LogicalPath: "wrong-node-key.md"},
		{OriginNodeKey: "main", LogicalPath: "missing-node-id.md"},
	} {
		noise.StorageClass, noise.SourceArea = storagecatalog.StorageClassObjectBlob, storagecatalog.SourceAreaExternalWatchedRoot
		noise.WatchedRootKey, noise.FileClass = "loom_box__topics", storagecatalog.FileClassMarkdown
		if _, err := catalog.RegisterEntry(ctx, noise); err != nil {
			t.Fatal(err)
		}
	}
	admissionPass := 0
	admit := func() {
		t.Helper()
		admissionPass++
		observed, applied := 0, 0
		cursor := AdmissionCursor{}
		for page := 0; page < 20; page++ {
			result, err := service.AdmitRegisteredNotesOnce(ctx, cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			if result.CatalogObserved > 2 || result.SyncedObserved > 2 {
				t.Fatal("unbounded admission")
			}
			observed += result.CatalogObserved
			applied += result.Applied
			if !result.MoreWork {
				if admissionPass <= 2 && (observed != 8 || applied != 4) {
					t.Fatalf("membership page saw/applied %d/%d, want 8/4", observed, applied)
				}
				if admissionPass > 2 && applied != 0 {
					t.Fatal("stale roots admitted work")
				}
				return
			}
			cursor = result.Cursor
		}
		t.Fatal("admission failed to converge")
	}
	admit()
	objects, err := service.store.ListKnowledgeObjects(ctx, KnowledgeObjectFilter{})
	if err != nil || len(objects) != 4 {
		t.Fatalf("admission: %#v %v", objects, err)
	}
	identities := map[string]string{}
	for _, object := range objects {
		if object.RelativePath != "source.md" {
			t.Fatal("private source admitted")
		}
		identities[object.NotesSourceRootID] = object.KnowledgeObjectID
	}
	admit()
	for _, root := range expanded {
		list, err := service.store.ListKnowledgeObjects(ctx, KnowledgeObjectFilter{NotesSourceRootID: root.NotesSourceRootID})
		if err != nil || len(list) != 1 || list[0].KnowledgeObjectID != identities[root.NotesSourceRootID] {
			t.Fatalf("replay lost identity: %#v %v", list, err)
		}
		var runs int
		if err := db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1`, list[0].KnowledgeObjectID).Scan(&runs); err != nil || runs != 1 {
			t.Fatalf("pipeline replay %d %v", runs, err)
		}
	}
	if exercise != nil {
		exercise(db, dbURL, service, expanded)
	}
	for _, area := range []string{"topics", "library"} {
		delete(contract.Policies, area)
	}
	raw, err = yaml.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	write(box.ContractPath(resolved.RootPath), raw)
	if _, err := boxService.ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Resolved: resolved}); err != nil {
		t.Fatal(err)
	}
	if err := projectService.MarkStaleProjectWatchedRoots(ctx, req, projectID, registered.Detail.Registration.ProjectContractRegistrationID, nil); err != nil {
		t.Fatal(err)
	}
	admit()
	for _, root := range expanded {
		retained, err := service.store.ListSourceRoots(ctx, SourceRootFilter{NotesSourceRootID: root.NotesSourceRootID, IncludeInactive: true})
		if err != nil || len(retained) != 1 || retained[0].Status != SourceRootStatusStale {
			t.Fatalf("removed policy remained active: %#v %v", retained, err)
		}
	}
}
func TestBoxSourceVersionSearchObservationPostgres(t *testing.T) {
	runBoxSourcesRegisteredAdmission(t, func(db *sql.DB, dbURL string, service *Service, roots []SourceRoot) {
		ctx := t.Context()
		var root SourceRoot
		for _, candidate := range roots {
			if candidate.RootKind == RootKindBoxTopics {
				root = candidate
			}
		}
		if root.NotesSourceRootID == "" {
			t.Fatal("missing registered fixture root")
		}
		var versions []boxContractVersion
		for _, source := range loadBoxSourcesContract(t).Corpus.Sources {
			if source.Key == "note" {
				versions = source.Versions
			}
		}
		if len(versions) != 2 || versions[0].Key != "note-v1" || versions[1].Key != "note-v2" {
			t.Fatal("missing frozen note revisions")
		}
		catalog := storagecatalog.NewService(db)
		entry := storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob,
			SourceArea: storagecatalog.SourceAreaExternalWatchedRoot, OriginNodeID: valueOrEmpty(root.NodeID),
			OriginNodeKey: root.NodeKey, WatchedRootKey: root.BackendRootKey, LogicalPath: "cadence.md",
			OriginalSourcePath: "cadence.md", FileClass: storagecatalog.FileClassMarkdown, MimeType: "text/markdown",
			AvailabilityState: storagecatalog.AvailabilityStateAvailable, ChecksumAlgorithm: "sha256"}
		publish := func(version boxContractVersion) string {
			t.Helper()
			body := []byte("# Cadence\n\n" + version.FixtureText + "\n")
			if err := os.WriteFile(filepath.Join(root.SourcePath, "cadence.md"), body, 0600); err != nil {
				t.Fatal(err)
			}
			size := int64(len(body))
			hash := hashArtifactValue(string(body))
			entry.SizeBytes, entry.ChecksumHex = &size, strings.TrimPrefix(hash, "sha256:")
			registered, err := catalog.RegisterEntry(ctx, entry)
			if err != nil {
				t.Fatal(err)
			}
			entry.StorageEntryID = registered.StorageEntryID
			cursor := AdmissionCursor{}
			for page := 0; page < 30; page++ {
				result, err := service.AdmitRegisteredNotesOnce(ctx, cursor, 50)
				if err != nil {
					t.Fatal(err)
				}
				if !result.MoreWork {
					break
				}
				cursor = result.Cursor
			}
			for step := 0; step < 40; step++ {
				result, err := service.RunPipelineCoordinatorOnce(ctx, PipelineCoordinatorRunInput{WorkerRunID: "box-query-observation", Limit: 50, Now: time.Now().UTC()})
				if err != nil || result.Failed != 0 {
					t.Fatalf("coordinator %#v %v", result, err)
				}
				if result.Claimed == 0 {
					return hash
				}
			}
			t.Fatal("query fixture did not converge")
			return ""
		}
		surfaces := boxSourceSurfaceSearch(t, dbURL)
		search := func(query string) NotesSearchResultSet {
			t.Helper()
			input := NotesSearchInput{Query: query, Path: "cadence.md", Mode: NotesSearchModeLexical, Limit: 50}
			result, err := service.SearchNotes(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if surfaces != nil {
				surfaces(input, result)
			}
			return result
		}
		oldHash := publish(versions[0])
		old := search(`"daily snapshots"`)
		if len(old.Results) != 1 || len(search(`"source-to-search latency"`).Results) != 0 {
			t.Fatal("v1 exact-phrase observation is not isolated")
		}
		// The existing broad path match is not evidence that new body text exists.
		t.Logf("unquoted path-filtered new-text results before edit: %d", len(search("source-to-search latency").Results))
		newHash := publish(versions[1])
		current := search(`"source-to-search latency"`)
		if len(current.Results) != 1 || len(search(`"daily snapshots"`).Results) != 0 {
			t.Fatal("v2 exact-phrase observation exposed the old body")
		}
		a, b := old.Results[0], current.Results[0]
		if a.KnowledgeObjectID != b.KnowledgeObjectID || a.KnowledgeObjectVersionID == b.KnowledgeObjectVersionID || b.KnowledgeChunkID == "" {
			t.Fatal("content replacement identity mismatch")
		}
		for i, hit := range []NotesSearchResult{a, b} {
			hash := []string{oldHash, newHash}[i]
			passage, err := service.GetNotesPassage(ctx, NotesPassageInput{KnowledgeObjectID: hit.KnowledgeObjectID,
				KnowledgeObjectVersionID: hit.KnowledgeObjectVersionID, KnowledgeChunkID: hit.KnowledgeChunkID, SourceHash: hash})
			if err != nil || passage.Historical != (i == 0) || !strings.Contains(passage.Text, versions[i].FixtureText) {
				t.Fatalf("version-bound passage %d: %#v %v", i, passage, err)
			}
		}
	})
}

func TestBoxSourcesNativeRetrievalPostgres(t *testing.T) {
	performance := newBoxPerformance(t)
	runBoxSourcesRegisteredAdmission(t, func(db *sql.DB, dbURL string, service *Service, roots []SourceRoot) {
		ctx := t.Context()
		fixture := loadBoxSourcesContract(t)
		rootByKey := map[string]SourceRoot{}
		for _, root := range roots {
			switch root.RootKind {
			case RootKindBoxTopics:
				rootByKey["main-topics"] = root
			case RootKindBoxLibrary:
				rootByKey["main-library"] = root
			case RootKindProjectMaterial:
				rootByKey["atlas-"+root.Declaration] = root
			}
		}
		entries := map[string]storagecatalog.RegisterEntryInput{}
		objects := map[string]KnowledgeObject{}
		sources := map[string]boxContractSource{}
		catalog := storagecatalog.NewService(db)
		var sourceBytes int64
		for _, source := range fixture.Corpus.Sources {
			key := source.RootKey
			if key == "atlas-material" {
				key = "atlas-" + source.Declaration
			}
			root, ok := rootByKey[key]
			if !ok || source.Lifecycle != "active" || source.Selection != "eligible" {
				continue
			}
			relative := source.RelativePath
			if root.RootKind == RootKindProjectMaterial {
				relative = strings.TrimPrefix(relative, root.RootRelativePath+"/")
			}
			body, class, mime := boxSourceMediaBytes(t, source)
			file := filepath.Join(root.SourcePath, relative)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, body, 0600); err != nil {
				t.Fatal(err)
			}
			size := int64(len(body))
			sourceBytes += size
			area := storagecatalog.SourceAreaExternalWatchedRoot
			if root.RootKind == RootKindProjectMaterial {
				area = storagecatalog.SourceAreaProjects
			}
			entry := storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: area, OriginNodeID: valueOrEmpty(root.NodeID), OriginNodeKey: root.NodeKey, ProjectID: valueOrEmpty(root.ProjectID), WatchedRootKey: root.BackendRootKey, LogicalPath: relative, OriginalSourcePath: relative, FileClass: class, MimeType: mime, SizeBytes: &size, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(hashArtifactValue(string(body)), "sha256:"), AvailabilityState: storagecatalog.AvailabilityStateAvailable}
			registeredEntry, err := catalog.RegisterEntry(ctx, entry)
			if err != nil {
				t.Fatal(err)
			}
			entry.StorageEntryID = registeredEntry.StorageEntryID
			entries[source.Key], sources[source.Key] = entry, source
		}
		admit := func() {
			t.Helper()
			cursor := AdmissionCursor{}
			for page := 0; page < 30; page++ {
				result, err := service.AdmitRegisteredNotesOnce(ctx, cursor, 2)
				if err != nil {
					t.Fatal(err)
				}
				if !result.MoreWork {
					return
				}
				cursor = result.Cursor
			}
			t.Fatal("admission did not converge")
		}
		admit()
		advance := func() int64 {
			t.Helper()
			var completed int64
			for step := 0; step < 40; step++ {
				result, err := service.RunPipelineCoordinatorOnce(ctx, PipelineCoordinatorRunInput{WorkerRunID: "box-source-native", Limit: 50, Now: time.Now().UTC()})
				if err != nil || result.Failed != 0 {
					t.Fatalf("coordinator %#v %v", result, err)
				}
				completed += result.Completed
				if result.Claimed == 0 {
					return completed
				}
			}
			t.Fatal("native pipeline did not converge")
			return 0
		}
		start := time.Now()
		cpu := func() time.Duration {
			var total time.Duration
			for _, who := range []int{unix.RUSAGE_SELF, unix.RUSAGE_CHILDREN} {
				var usage unix.Rusage
				if err := unix.Getrusage(who, &usage); err != nil {
					t.Fatal(err)
				}
				total += time.Duration(usage.Utime.Sec+usage.Stime.Sec)*time.Second + time.Duration(usage.Utime.Usec+usage.Stime.Usec)*time.Microsecond
			}
			return total
		}
		cpuStart := cpu()
		completed := performance.measureWork(t, db, "initial_extraction", advance)
		nativeCPU := cpu() - cpuStart
		duration := time.Since(start)
		for key, entry := range entries {
			list, err := service.store.ListKnowledgeObjects(ctx, KnowledgeObjectFilter{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range list {
				if object.RelativePath == entry.LogicalPath && object.SourceCategory == sourceCategory(rootByKey[sources[key].RootKey].RootKind) {
					objects[key] = object
				}
			}
			if objects[key].KnowledgeObjectID == "" {
				for _, object := range list {
					if object.RelativePath == entry.LogicalPath && object.Declaration == sources[key].Declaration && object.SourceCategory == "projects" {
						objects[key] = object
					}
				}
			}
			object := objects[key]
			if object.KnowledgeObjectID == "" || object.SourcePosture != sources[key].Posture {
				t.Fatalf("missing typed source %s: %#v", key, object)
			}
			if sources[key].Extraction != "native_text" {
				var chunks int
				if err := db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_chunks WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&chunks); err != nil || chunks != 0 {
					t.Fatalf("metadata-only source has passages %s %d %v", key, chunks, err)
				}
				if objectExtractionStatus(object.Metadata) == "extracted" {
					t.Fatal("unsupported source claims body extraction")
				}
			}
		}
		queries := 0
		citations := map[string]NotesPassageInput{}
		surfaceSearch := boxSourceSurfaceSearch(t, dbURL, performance.observeSurface)
		var performanceCases []boxPerformanceCase
		for _, query := range fixture.Queries.Queries {
			switch query.Key {
			case "topic-draft", "library-native-pdf", "library-conflicting-claims", "library-office", "library-scanned", "library-image", "declared-project-doc", "declared-project-research", "duplicate-project-isolation", "undeclared-repository", "documents-default", "privacy-exclusions":
			default:
				continue
			}
			input := NotesSearchInput{Query: query.Request.Query, SourceCategory: query.Filters.SourceCategory, Mode: NotesSearchModeLexical, Limit: 50}
			if query.Filters.ProjectKey == "atlas" {
				input.ProjectID = valueOrEmpty(rootByKey["atlas-docs"].ProjectID)
			}
			result, err := service.SearchNotes(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			queries++
			if surfaceSearch != nil {
				surfaceSearch(input, result)
			}
			if len(result.Results) != len(query.Expected) {
				var actual []string
				for _, hit := range result.Results {
					actual = append(actual, hit.RelativePath+"#"+hit.StructuralPath)
				}
				t.Fatalf("frozen %s: got %v, expected %v", query.Key, actual, query.Expected)
			}
			for _, want := range query.Expected {
				found := false
				for _, hit := range result.Results {
					for key, source := range sources {
						if hit.KnowledgeObjectID != objects[key].KnowledgeObjectID {
							continue
						}
						if want == key && hit.MetadataOnly && hit.KnowledgeChunkID == "" {
							found = true
						}
						for _, v := range source.Versions {
							for _, p := range v.Passages {
								if p.Key == want && hit.KnowledgeChunkID != "" && hit.KnowledgeObjectVersionID != "" && hit.SourcePosture == source.Posture {
									citation := NotesPassageInput{KnowledgeObjectID: hit.KnowledgeObjectID, KnowledgeObjectVersionID: hit.KnowledgeObjectVersionID, KnowledgeChunkID: hit.KnowledgeChunkID, SourceHash: objects[key].SourceHash}
									passage, err := service.GetNotesPassage(ctx, citation)
									if err != nil || !strings.Contains(passage.Text, p.Text) || passage.NotesPassageInput != citation || passage.CurrentSourceContext.SourcePosture != source.Posture {
										t.Fatalf("wrong bound passage %s: %#v %v", want, passage, err)
									}
									citations[key] = citation
									if p.Locator.Kind == "page" && hit.StructuralPath != fmt.Sprintf("page:%d", p.Locator.Page) {
										t.Fatalf("wrong page citation %s: %q", want, hit.StructuralPath)
									}
									found = true
								}
							}
						}
					}
				}
				if !found {
					t.Fatalf("frozen passage %s missing or untyped", want)
				}
			}
			performanceCases = append(performanceCases, boxPerformanceCase{query.Key, input, result})
		}
		if queries != 12 {
			t.Fatalf("too few native queries: %d", queries)
		}
		performance.measureQueries(t, db, service, "before_reconciliation", performanceCases, surfaceSearch)
		for _, category := range []string{"topics", "library", "projects"} {
			filtered, err := service.store.ListKnowledgeObjects(ctx, KnowledgeObjectFilter{SourceCategory: category, Limit: 100})
			if err != nil || len(filtered) == 0 {
				t.Fatalf("category objects %s: %v", category, err)
			}
			for _, object := range filtered {
				if object.SourceCategory != category {
					t.Fatal("cross-category object")
				}
			}
			overview, err := service.GetNotesOverview(ctx, NotesOverviewInput{SourceCategory: category})
			if err != nil || overview.Totals.ObjectCount != len(filtered) {
				t.Fatalf("overview category %s: %#v %v", category, overview.Totals, err)
			}
		}
		projectionRoot := t.TempDir()
		t.Cleanup(func() {
			if err := filepath.WalkDir(projectionRoot, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return os.Chmod(path, 0700)
				}
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
		projection := notesprojection.NewService(service, projectionRoot)
		projected, err := projection.Rebuild(ctx, notesprojection.RebuildInput{})
		if err != nil || projected.Manifest.Counts.Materialized < len(objects) {
			t.Fatalf("projection %#v %v", projected, err)
		}
		for _, entry := range projected.Manifest.Entries {
			if entry.SourceCategory == "" || entry.SourcePosture == "" {
				t.Fatal("projection lost source context")
			}
		}
		var runsBefore, artifactsBefore int
		if err := db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs`).Scan(&runsBefore); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts`).Scan(&artifactsBefore); err != nil {
			t.Fatal(err)
		}
		admit()
		if got := performance.measureWork(t, db, "unchanged_replay", advance); got != 0 {
			t.Fatalf("unchanged replay did %d stage jobs", got)
		}
		// Metadata changes hide prior evidence until ordinary readmission, but do not extract unchanged bytes.
		entry := entries["topic"]
		entry.Metadata = json.RawMessage(`{"label":"reviewed metadata"}`)
		if _, err := catalog.RegisterEntry(ctx, entry); err != nil {
			t.Fatal(err)
		}
		if result, err := service.SearchNotes(ctx, NotesSearchInput{Query: "brainstorm", SourceCategory: "topics", Mode: NotesSearchModeLexical}); err != nil || len(result.Results) != 0 {
			t.Fatalf("stale metadata visible: %#v %v", result, err)
		}
		admit()
		if got := performance.measureWork(t, db, "metadata_replay", advance); got != 0 {
			t.Fatalf("metadata-only edit re-extracted: %d", got)
		}
		var runsAfter, artifactsAfter int
		_ = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs`).Scan(&runsAfter)
		_ = db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts`).Scan(&artifactsAfter)
		if runsAfter != runsBefore || artifactsAfter != artifactsBefore {
			t.Fatal("replay created new derived work")
		}
		performance.measureQueries(t, db, service, "after_metadata_reconciliation", performanceCases, surfaceSearch)
		// Two content observations supersede an already claimed old generation.
		changeContent := func(body string) {
			t.Helper()
			if err := os.WriteFile(objects["topic"].SourcePath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			size := int64(len(body))
			entry.SizeBytes = &size
			entry.ChecksumHex = strings.TrimPrefix(hashArtifactValue(body), "sha256:")
			if _, err := catalog.RegisterEntry(ctx, entry); err != nil {
				t.Fatal(err)
			}
			if _, err := service.store.GetKnowledgeObject(ctx, objects["topic"].KnowledgeObjectID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("outdated source remained visible: %v", err)
			}
			admit()
		}
		changeContent("# Revision\n\nIntermediate revision.\n")
		claimed, err := service.ClaimPipelineRuns(ctx, PipelineExecutionCoordinator, "obsolete-box-claim", PipelineClaimOptions{Limit: 1, Now: time.Now().UTC(), LeaseDuration: time.Minute})
		if err != nil || len(claimed) != 1 {
			t.Fatalf("content claim %#v %v", claimed, err)
		}
		changeContent("# Revision\n\nFinal quiescentrevision.\n")
		if _, err := service.executeCoordinatorStage(ctx, claimed[0], PipelineCoordinatorRunInput{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("obsolete publication accepted: %v", err)
		}
		contentJobs := performance.measureWork(t, db, "content_replacement", advance)
		if contentJobs == 0 {
			t.Fatal("content edit did not extract")
		}
		current, err := service.SearchNotes(ctx, NotesSearchInput{Query: "quiescentrevision", SourceCategory: "topics", Mode: NotesSearchModeLexical})
		if err != nil || len(current.Results) != 1 || current.Results[0].KnowledgeObjectID != objects["topic"].KnowledgeObjectID || current.Results[0].KnowledgeObjectVersionID == "" {
			t.Fatalf("content identity %#v %v", current, err)
		}
		if prior, err := service.SearchNotes(ctx, NotesSearchInput{Query: "brainstorm", SourceCategory: "topics", Mode: NotesSearchModeLexical}); err != nil || len(prior.Results) != 0 {
			t.Fatalf("old content visible %#v %v", prior, err)
		}
		// An owner-reported permission failure suppresses current retrieval without
		// scanning live filesystem permissions from a search request.
		permissionEntry := entries["project-doc"]
		permissionEntry.AvailabilityState = storagecatalog.AvailabilityStateFailed
		permissionEntry.Metadata = json.RawMessage(`{"read_error":"permission_denied"}`)
		if _, err := catalog.RegisterEntry(ctx, permissionEntry); err != nil {
			t.Fatal(err)
		}
		if _, err := service.store.GetKnowledgeObject(ctx, objects["project-doc"].KnowledgeObjectID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("permission-changed source visible: %v", err)
		}
		if _, err := service.GetNotesPassage(ctx, citations["project-doc"]); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("permission-changed passage visible: %v", err)
		}
		if _, err := catalog.RegisterEntry(ctx, entries["project-doc"]); err != nil {
			t.Fatal(err)
		}
		// A removed declaration is a visibility fence even while old chunks exist.
		root := rootByKey["atlas-docs"]
		stale := root
		stale.Status = SourceRootStatusStale
		if _, err := service.store.UpsertSourceRoot(ctx, stale); err != nil {
			t.Fatal(err)
		}
		if _, err := service.store.GetKnowledgeObject(ctx, objects["project-doc"].KnowledgeObjectID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("removed declaration exposed old object: %v", err)
		}
		if _, err := service.GetNotesPassage(ctx, citations["project-doc"]); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("removed declaration exposed passage: %v", err)
		}
		if _, err := service.store.UpsertSourceRoot(ctx, root); err != nil {
			t.Fatal(err)
		}
		// A privacy change refuses exact retrieval and projection even before re-admission.
		entry.Metadata = json.RawMessage(`{"private_no_index":true}`)
		if _, err := catalog.RegisterEntry(ctx, entry); err != nil {
			t.Fatal(err)
		}
		admit()
		if _, err := service.store.GetKnowledgeObject(ctx, objects["topic"].KnowledgeObjectID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("private exact get: %v", err)
		}
		if _, err := service.GetNotesPassage(ctx, citations["topic"]); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("private historical topic passage visible: %v", err)
		}
		visible, err := service.ListProjectionSources(ctx, notesprojection.SourceListInput{})
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range visible {
			if source.KnowledgeObjectID == objects["topic"].KnowledgeObjectID {
				t.Fatal("private projection")
			}
		}
		var retained int
		_ = db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_object_versions WHERE knowledge_object_id=$1`, objects["topic"].KnowledgeObjectID).Scan(&retained)
		if retained != 3 {
			t.Fatal("privacy removed history")
		}
		var artifactBytes int64
		if err := db.QueryRow(`SELECT COALESCE(sum(octet_length(text_content)),0) FROM knowledge.derived_artifacts`).Scan(&artifactBytes); err != nil {
			t.Fatal(err)
		}
		t.Logf("native sources=%d source_bytes=%d stage_jobs=%d duration=%s client_and_extractor_cpu=%s replay_jobs=0 metadata_edit_jobs=0 content_edit_jobs=%d pipeline_runs_before_edit=%d artifacts_before_edit=%d final_artifact_text_bytes=%d frozen_queries=%d", len(objects), sourceBytes, completed, duration, nativeCPU, contentJobs, runsBefore, artifactsBefore, artifactBytes, queries)
	})
}

// The separately compiled HTTP package helper avoids a knowledge/httpapi import
// cycle. It exposes only this already-migrated disposable database, with no daemon
// supervisor, scheduling, or filesystem service configured.
func boxSourceSurfaceSearch(t *testing.T, dbURL string, observers ...func(string, time.Duration)) func(NotesSearchInput, NotesSearchResultSet) {
	t.Helper()
	helper, cli := os.Getenv("LOOM_BOX_SURFACE_HELPER"), os.Getenv("LOOM_BOX_SURFACE_CLI")
	if helper == "" && cli == "" {
		t.Log("API/client/CLI corpus requires LOOM_BOX_SURFACE_HELPER and LOOM_BOX_SURFACE_CLI")
		return nil
	}
	if helper == "" || cli == "" {
		t.Fatal("both surface binaries are required")
	}
	root, err := os.MkdirTemp("/tmp", "loom-box-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(root, "api.sock")
	log, err := os.Create(filepath.Join(root, "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	process := exec.Command(helper, "-test.run=^TestKnowledgeBoxSourceSurfaceHelper$", "-test.timeout=5m")
	process.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "LOOM_BOX_SURFACE_DB_URL=" + dbURL, "LOOM_BOX_SURFACE_SOCKET=" + socket}
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- process.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("surface helper exit: %v", err)
			}
		case <-time.After(8 * time.Second):
			_ = process.Process.Kill()
			<-done
			t.Error("surface helper did not stop")
		}
		_ = log.Close()
	})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get("http://loom/v1/knowledge/notes/roots")
		if err == nil {
			response.Body.Close()
			ready = response.StatusCode == 200
			if ready {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		body, _ := os.ReadFile(filepath.Join(root, "helper.log"))
		t.Fatalf("surface helper not ready: %s", body)
	}
	return func(input NotesSearchInput, expected NotesSearchResultSet) {
		t.Helper()
		payload, _ := json.Marshal(input)
		started := time.Now()
		response, err := client.Post("http://loom/v1/knowledge/notes/search", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		response.Body.Close()
		httpDuration := time.Since(started)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("API search: status=%d %s %v", response.StatusCode, body, err)
		}
		var envelope struct {
			Data NotesSearchResultSet `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		args := []string{"--socket", socket, "--json", "notes", "search", input.Query, "--mode", "lexical", "--limit", "50"}
		if input.SourceCategory != "" {
			args = append(args, "--category", input.SourceCategory)
		}
		if input.ProjectID != "" {
			args = append(args, "--project", input.ProjectID)
		}
		if input.Path != "" {
			args = append(args, "--path", input.Path)
		}
		command := exec.Command(cli, args...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}
		started = time.Now()
		out, err := command.CombinedOutput()
		cliDuration := time.Since(started)
		if err != nil {
			t.Fatalf("CLI search: %v %s", err, out)
		}
		var result NotesSearchResultSet
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("CLI JSON: %v %s", err, out)
		}
		if boxPerformanceIdentity(expected) != boxPerformanceIdentity(envelope.Data) || boxPerformanceIdentity(expected) != boxPerformanceIdentity(result) {
			t.Fatalf("API/client/CLI citation or context parity failed for %s", input.Query)
		}
		for _, observe := range observers {
			observe("http", httpDuration)
			observe("cli", cliDuration)
		}
	}
}

func boxSourceMediaBytes(t *testing.T, source boxContractSource) ([]byte, string, string) {
	t.Helper()
	version := source.Versions[len(source.Versions)-1]
	switch source.Media {
	case "markdown":
		return []byte("# Cadence\n\n" + version.FixtureText + "\n"), storagecatalog.FileClassMarkdown, "text/markdown"
	case "docx":
		file := writeTestDOCX(t, map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Cadence</w:t></w:r></w:p><w:p><w:r><w:t>` + version.FixtureText + `</w:t></w:r></w:p></w:body></w:document>`})
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return body, storagecatalog.FileClassOfficeDocument, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "pdf_native", "pdf_scanned":
		pages := strings.Split(version.FixtureText, "\n")
		if source.Media == "pdf_scanned" {
			pages = []string{""}
		}
		return boxSourcePDF(pages), storagecatalog.FileClassPDF, "application/pdf"
	case "image":
		var body bytes.Buffer
		if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
			t.Fatal(err)
		}
		return body.Bytes(), storagecatalog.FileClassImage, "image/png"
	case "unsupported":
		return []byte{0, 1, 2, 3}, storagecatalog.FileClassBinary, "application/octet-stream"
	default:
		t.Fatalf("unsupported fixture media %s", source.Media)
		return nil, "", ""
	}
}

func boxSourcePDF(pages []string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var kids []string
	for _, page := range pages {
		id := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", id))
		text := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(page)
		stream := "BT /F1 10 Tf 20 700 Td (" + text + ") Tj ET\n"
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 800 800] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", id+1), fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " "))
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}
