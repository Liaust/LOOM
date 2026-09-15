package knowledge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

type notesArchivePublicReader struct {
	socket string
	root   string
	cli    string
	http   *http.Client
	stop   func()
}

func startNotesArchivePublicReader(t *testing.T, f *notesArchivePostgresFixture) *notesArchivePublicReader {
	t.Helper()
	helper, cli := os.Getenv("LOOM_BOX_SURFACE_HELPER"), os.Getenv("LOOM_BOX_SURFACE_CLI")
	if helper == "" || cli == "" {
		t.Fatal("acceptance requires both compiled surface binaries")
	}
	var dbName string
	if err := f.s.store.db.QueryRow(`SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("LOOM_TEST_DB_URL"))
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Query().Get("host"), "/tmp/") || !strings.HasPrefix(dbName, "box_sources_") {
		t.Fatal("refused non-disposable public acceptance database")
	}
	u.Path = "/" + dbName
	root, err := os.MkdirTemp("/tmp", "loom-box-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Error("disposable public reader root remains")
		}
	})
	reader := &notesArchivePublicReader{root: root, socket: filepath.Join(root, "api.sock"), cli: cli}
	log, err := os.Create(filepath.Join(root, "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	process := exec.Command(helper, "-test.run=^TestKnowledgeBoxSourceSurfaceHelper$", "-test.timeout=5m")
	process.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + root,
		"LOOM_BOX_SURFACE_DB_URL=" + u.String(), "LOOM_BOX_SURFACE_SOCKET=" + reader.socket}
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	stopped := false
	reader.stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = process.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- process.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("public reader shutdown: %v", err)
			}
		case <-time.After(8 * time.Second):
			_ = process.Process.Kill()
			<-done
			t.Error("public reader shutdown timed out")
		}
		_ = log.Close()
	}
	t.Cleanup(reader.stop)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", reader.socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	reader.http = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		response, err := reader.http.Get("http://loom/v1/knowledge/notes/roots")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return reader
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	body, _ := os.ReadFile(filepath.Join(root, "helper.log"))
	t.Fatalf("public reader not ready: %s", body)
	return nil
}

func (r *notesArchivePublicReader) api(t *testing.T, path string, input any, status int, output any) {
	t.Helper()
	method := http.MethodGet
	var body []byte
	if input != nil {
		method = http.MethodPost
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequestWithContext(t.Context(), method, "http://loom/v1/knowledge/notes/"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != status {
		t.Fatalf("API %s: %d %s %v", path, response.StatusCode, body, err)
	}
	if output != nil {
		var envelope struct{ Data json.RawMessage }
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Data, output); err != nil {
			t.Fatalf("API data: %s: %v", body, err)
		}
	}
}

func (r *notesArchivePublicReader) command(t *testing.T, args []string, success bool, output any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, r.cli, append([]string{"--socket", r.socket, "--json", "notes"}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.root, "XDG_CONFIG_HOME=" + r.root}
	body, err := command.CombinedOutput()
	if (err == nil) != success {
		t.Fatalf("CLI %v: %s: %v", args, body, err)
	}
	if success && output != nil {
		if err := json.Unmarshal(body, output); err != nil {
			t.Fatalf("CLI data %s: %v", body, err)
		}
	}
}

func TestNotesArchiveAcceptancePublicPostgres(t *testing.T) {
	if os.Getenv("LOOM_BOX_SURFACE_HELPER") == "" && os.Getenv("LOOM_BOX_SURFACE_CLI") == "" {
		t.Skip("run the independent smoke for API process and CLI acceptance")
	}
	f := notesArchiveTestFixture(t)
	reader := startNotesArchivePublicReader(t, f)
	object := notesFenceObject(t, f, "topic-one/source.md")
	citation := archiveSurfaceCitation(t, f, object)
	before, err := f.s.GetNotesPassage(t.Context(), citation)
	if err != nil {
		t.Fatal(err)
	}
	observe := func(archived bool) {
		t.Helper()
		for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
			query := url.Values{}
			flags := []string{}
			if filter != "" {
				query.Set("source_lifecycle", string(filter))
				flags = []string{"--source-lifecycle", string(filter)}
			}
			want := map[string]SourceLifecycle{}
			for _, object := range f.objects {
				state := SourceLifecycleActive
				if archived && strings.HasPrefix(object.RelativePath, "topic-one/") {
					state = SourceLifecycleArchived
				}
				included := filter == SourceLifecycleFilterAll || (filter == SourceLifecycleFilterArchived) == (state == SourceLifecycleArchived)
				if included {
					want[object.KnowledgeObjectID] = state
				}
			}
			var apiResult, cliResult NotesSearchResultSet
			reader.api(t, "search", NotesSearchInput{Query: "cobalt observatory", Mode: NotesSearchModeLexical, SourceLifecycle: filter, Limit: 10}, 200, &apiResult)
			reader.command(t, append([]string{"search", "cobalt observatory", "--mode", "lexical", "--limit", "10"}, flags...), true, &cliResult)
			// Sequential requests have different wall clocks. Recency is a live
			// score, not source/citation identity; every other field and order
			// must agree exactly in this lexical comparison.
			for _, results := range [][]NotesSearchResult{apiResult.Results, cliResult.Results} {
				for i := range results {
					if results[i].RecencyScore < 0 || results[i].RecencyScore > 1 {
						t.Fatal("invalid live recency score")
					}
					results[i].RecencyScore = 0
				}
			}
			if !reflect.DeepEqual(apiResult.Results, cliResult.Results) || !reflect.DeepEqual(apiResult.LifecycleGroups, cliResult.LifecycleGroups) {
				t.Fatal("API/CLI result identity or order differs")
			}
			if apiResult.ResultCount != len(want) || len(apiResult.Results) != len(want) {
				t.Fatalf("search inclusion: got %d want %d", apiResult.ResultCount, len(want))
			}
			seen := map[string]bool{}
			for _, result := range apiResult.Results {
				if seen[result.KnowledgeObjectID] || want[result.KnowledgeObjectID] != result.SourceLifecycle || result.OriginalPath != result.SourcePath || result.Citation.SourceRef == "" {
					t.Fatalf("contaminated or unbound result: %+v", result)
				}
				seen[result.KnowledgeObjectID] = true
				if result.SourceLifecycle == SourceLifecycleArchived && (result.ArchiveOperationID != f.plan.OperationID || result.CanonicalPath == result.SourcePath || result.ArchivedAt == nil) {
					t.Fatal("archive attribution missing")
				}
			}
			offset := 0
			for _, group := range apiResult.LifecycleGroups {
				if group.Offset != offset || offset+group.ResultCount > len(apiResult.Results) {
					t.Fatal("invalid group bounds")
				}
				for _, item := range apiResult.Results[offset : offset+group.ResultCount] {
					if item.SourceLifecycle != group.SourceLifecycle {
						t.Fatal("blended lifecycle group")
					}
				}
				offset += group.ResultCount
			}
			if offset != len(want) {
				t.Fatal("ungrouped context")
			}
			omitted := 0
			if archived && (filter == "" || filter == SourceLifecycleFilterActive) {
				omitted = 2
			}
			if apiResult.ArchivedMatchesOmitted != omitted || cliResult.ArchivedMatchesOmitted != omitted || apiResult.ArchivedMatchesOmittedTruncated || cliResult.ArchivedMatchesOmittedTruncated {
				t.Fatal("omitted-count truth differs")
			}
			var apiObjects, cliObjects []KnowledgeObject
			reader.api(t, "objects?"+query.Encode(), nil, 200, &apiObjects)
			reader.command(t, append([]string{"objects", "list"}, flags...), true, &cliObjects)
			if !reflect.DeepEqual(apiObjects, cliObjects) || len(apiObjects) != len(want) {
				t.Fatal("object lifecycle parity")
			}
			var apiOverview, cliOverview NotesOverview
			reader.api(t, "overview?"+query.Encode(), nil, 200, &apiOverview)
			reader.command(t, append([]string{"overview"}, flags...), true, &cliOverview)
			if apiOverview.Totals.ObjectCount != len(want) || !reflect.DeepEqual(apiOverview.Totals, cliOverview.Totals) {
				t.Fatal("overview lifecycle parity")
			}
			var apiRoots, cliRoots []SourceRoot
			reader.api(t, "roots?"+query.Encode(), nil, 200, &apiRoots)
			reader.command(t, append([]string{"roots", "list"}, flags...), true, &cliRoots)
			if !reflect.DeepEqual(apiRoots, cliRoots) {
				t.Fatal("root lifecycle parity")
			}
			included := want[object.KnowledgeObjectID] != ""
			status := http.StatusNotFound
			if included {
				status = http.StatusOK
			}
			reader.api(t, "objects/"+object.KnowledgeObjectID+"?"+query.Encode(), nil, status, nil)
			reader.command(t, append([]string{"objects", "show", object.KnowledgeObjectID}, flags...), included, nil)
			query.Set("object_id", citation.KnowledgeObjectID)
			query.Set("version_id", citation.KnowledgeObjectVersionID)
			query.Set("source_hash", citation.SourceHash)
			var apiPassage, cliPassage NotesPassage
			var target any
			if included {
				target = &apiPassage
			}
			reader.api(t, "passages/"+citation.KnowledgeChunkID+"?"+query.Encode(), nil, status, target)
			reader.command(t, append([]string{"passage", "get", citation.KnowledgeChunkID, "--object", citation.KnowledgeObjectID, "--version", citation.KnowledgeObjectVersionID, "--source-hash", citation.SourceHash}, flags...), included, &cliPassage)
			if included && (apiPassage.Text != before.Text || apiPassage.ChunkHash != before.ChunkHash || apiPassage.Historical || !reflect.DeepEqual(apiPassage, cliPassage)) {
				t.Fatal("retained citation changed across public surfaces")
			}
		}
	}
	observe(false)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	reader.stop()
	reader = startNotesArchivePublicReader(t, f)
	observe(true)
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index'
	 WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	reader.api(t, "objects/"+object.KnowledgeObjectID+"?source_lifecycle=all", nil, 404, nil)
	var privateSearch NotesSearchResultSet
	reader.api(t, "search", NotesSearchInput{Query: "cobalt observatory", Mode: NotesSearchModeLexical, SourceLifecycle: SourceLifecycleFilterArchived, Limit: 10}, 200, &privateSearch)
	if privateSearch.ResultCount != 1 || privateSearch.Results[0].KnowledgeObjectID == object.KnowledgeObjectID {
		t.Fatal("current private source leaked through explicit archived search")
	}
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='text_later'
	 WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	var samples []time.Duration
	for i := 0; i < 25; i++ {
		start := time.Now()
		reader.api(t, "search", NotesSearchInput{Query: "cobalt observatory", Mode: NotesSearchModeLexical, SourceLifecycle: SourceLifecycleFilterAll, Limit: 10}, 200, nil)
		if i >= 5 {
			samples = append(samples, time.Since(start))
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("five-object public lexical/all search: 5 warmups, 20 samples, p95=%s max=%s; not a production-scale benchmark", samples[18], samples[19])
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil || result.Replayed != 2 || result.Projected != 0 {
		t.Fatalf("old-event replay: %+v %v", result, err)
	}
	reader.stop()
	reader = startNotesArchivePublicReader(t, f)
	observe(false)
	if f.processingSnapshot(t) != f.before {
		t.Fatal("public reads/restart/replay changed processing or source identity")
	}
}

func TestNotesArchiveAcceptanceProjectPublicPostgres(t *testing.T) {
	if os.Getenv("LOOM_BOX_SURFACE_HELPER") == "" && os.Getenv("LOOM_BOX_SURFACE_CLI") == "" {
		t.Skip("run the independent smoke for API process and CLI acceptance")
	}
	for _, material := range []bool{false, true} {
		testNotesArchiveProjectCompletion(t, material, func(f *notesArchivePostgresFixture, object KnowledgeObject, state SourceLifecycle) {
			reader := startNotesArchivePublicReader(t, f)
			defer reader.stop()
			for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
				input := NotesSearchInput{Query: "Project decision", Mode: NotesSearchModeLexical, SourceLifecycle: filter,
					ProjectRef: "project-notes-test", RootRef: object.NotesSourceRootID}
				var apiResult, cliResult NotesSearchResultSet
				reader.api(t, "search", input, 200, &apiResult)
				args := []string{"search", input.Query, "--mode", input.Mode, "--project", input.ProjectRef, "--root", input.RootRef}
				if filter != "" {
					args = append(args, "--source-lifecycle", string(filter))
				}
				reader.command(t, args, true, &cliResult)
				included := filter == SourceLifecycleFilterAll || (filter == SourceLifecycleFilterArchived) == (state == SourceLifecycleArchived)
				want := 0
				if included {
					want = 1
				}
				for _, result := range []NotesSearchResultSet{apiResult, cliResult} {
					if result.ResultCount != want || len(result.Results) != want {
						t.Fatalf("project public inclusion %s/%s: %+v", state, filter, result)
					}
					if included {
						item := result.Results[0]
						if item.KnowledgeObjectID != object.KnowledgeObjectID || item.SourceLifecycle != state || item.OriginalPath != object.SourcePath || item.Citation.SourceRef == "" || (material && item.Declaration != "docs") {
							t.Fatalf("project public attribution: %+v", item)
						}
					}
				}
			}
		})
	}
}

func TestNotesArchiveAcceptanceLibraryPosturesPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	base := t.TempDir()
	trusted := storagearchive.TrustedWorkspaceRoots{BoxRoot: filepath.Join(base, "box"), StorageRoot: filepath.Join(base, "storage")}
	root := roots[2]
	root.SourcePath = filepath.Join(trusted.BoxRoot, "Library")
	root.RootRelativePath = "Library"
	metadata := jsonObject(root.Metadata)
	declaration := metadata["registration_metadata"].(map[string]any)["knowledge_source"].(map[string]any)
	declaration["root_relative_path"], declaration["include"] = "Library", []string{"old-book/**", "old-metadata/**"}
	root.Metadata = mustJSON(t, metadata)
	var err error
	root, err = s.store.UpsertSourceRoot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"Library/old-book", "Library/old-metadata"} {
		if err := os.MkdirAll(filepath.Join(trusted.BoxRoot, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(trusted.StorageRoot, "archive/library"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Both are new immutable source identities, admitted through the ordinary
	// pipeline: malformed PDF fails extraction; binary remains metadata-only.
	for _, input := range []struct{ path, mime, class string }{{"old-book/source.pdf", "application/pdf", "pdf"}, {"old-metadata/source.bin", "application/octet-stream", "binary"}} {
		seedNotesArchiveSibling(t, s, root, input.path, "cobalt observatory "+input.path)
		uri := "watched-root://" + root.BackendRootKey + "/" + input.path
		if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET mime_type=$2,metadata=jsonb_build_object('file_class',$3::text) WHERE source_path=$1`, uri, input.mime, input.class); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET mime_type=$2 WHERE source_path=$1`, uri, input.mime); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.db.Exec(`UPDATE files.blobs SET mime_type=$2 WHERE blob_id IN (SELECT blob_id FROM objects.object_versions WHERE source_path=$1)`, uri, input.mime); err != nil {
			t.Fatal(err)
		}
	}
	objects := reconcileBoxSyncedFixture(t, s, []SourceRoot{root})
	if len(objects) != 2 {
		t.Fatalf("Library admission: %d", len(objects))
	}
	failed := int64(0)
	converged := false
	for range 20 {
		result, err := s.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "notes-library-postures", Limit: 10, Now: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		failed += result.Failed
		if result.Claimed == 0 {
			converged = true
			break
		}
	}
	if !converged || failed != 1 {
		t.Fatalf("expected one bounded PDF failure: converged=%v failed=%d", converged, failed)
	}
	var failedRuns int
	if err := s.store.db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs r
	 JOIN knowledge.pipeline_stage_runs s USING(knowledge_pipeline_run_id)
	 WHERE r.status='waiting_coordinator' AND r.last_error_code='knowledge_processing_failed'
	 AND s.stage_key='native_text' AND s.status='failed_retryable'`).Scan(&failedRuns); err != nil || failedRuns != 1 {
		t.Fatalf("failed PDF trajectory evidence: %d %v", failedRuns, err)
	}
	actor := ids.NewActorID()
	if _, err := s.store.db.Exec(`INSERT INTO identity.actors(actor_id,actor_key,actor_kind,display_name,status) VALUES ($1,$1,'human','Library archive acceptance','active')`, actor); err != nil {
		t.Fatal(err)
	}
	catalog := storagecatalog.NewService(s.store.db)
	owner := storagearchive.WorkspaceMoveService{Roots: trusted, Catalog: catalog, Journal: catalog, ManifestKeyID: "notes.archive.test",
		ManifestKey: func(context.Context, string) ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil }}
	f := &notesArchivePostgresFixture{s: s, root: root, objects: objects, actor: actor,
		p: NotesArchiveProjector{DB: s.store.db, Workspace: owner, NodeKey: root.NodeKey}}
	f.before = f.processingSnapshot(t)
	for _, slug := range []string{"old-book", "old-metadata"} {
		plan, err := owner.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{Kind: storagearchive.WorkspaceKindLibraryItem,
			ObjectID: strings.ReplaceAll(slug, "-", "_"), Slug: slug, ActorID: actor, Reason: "disposable Library posture acceptance"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ApplyArchive(t.Context(), plan, plan.PlanDigest); err != nil {
			t.Fatal(err)
		}
		if result, err := f.p.ProjectOperation(t.Context(), plan.OperationID); err != nil || result.Projected != 1 {
			t.Fatalf("Library projection: %+v %v", result, err)
		}
	}
	archived, err := s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{SourceLifecycle: SourceLifecycleFilterArchived})
	if err != nil || len(archived) != 2 {
		t.Fatalf("Library reads: %d %v", len(archived), err)
	}
	for _, object := range archived {
		var passages int
		if err := s.store.db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_chunks WHERE knowledge_object_id=$1
		 AND COALESCE(metadata->>'text_source','')<>'metadata_text'`, object.KnowledgeObjectID).Scan(&passages); err != nil {
			t.Fatal(err)
		}
		state := ProcessingStateIndexed
		if strings.HasSuffix(object.RelativePath, ".pdf") {
			// A retryable extraction failure remains explicit in its stage/run;
			// the existing object cache calls unpublished output stale.
			state = ProcessingStateStale
		} else if jsonObject(object.Metadata)["indexing_mode"] != "metadata_only" || object.PipelineKey != "notes_metadata_only" {
			t.Fatal("binary source lost its metadata-only pipeline posture")
		}
		if object.ProcessingState != state || object.SourceLifecycle != SourceLifecycleArchived || passages != 0 {
			t.Fatalf("custody conflated with extraction: %s lifecycle=%s processing=%s passages=%d error=%s/%s", object.RelativePath, object.SourceLifecycle, object.ProcessingState, passages, object.LastErrorCode, object.LastErrorMessage)
		}
	}
	if os.Getenv("LOOM_BOX_SURFACE_HELPER") != "" || os.Getenv("LOOM_BOX_SURFACE_CLI") != "" {
		reader := startNotesArchivePublicReader(t, f)
		var apiObjects, cliObjects []KnowledgeObject
		reader.api(t, "objects?source_lifecycle=archived", nil, 200, &apiObjects)
		reader.command(t, []string{"objects", "list", "--source-lifecycle", "archived"}, true, &cliObjects)
		// Compare the complete public encoding, not driver's time.Location
		// pointers or whitespace inside PostgreSQL's raw JSON values.
		wantJSON, _ := json.Marshal(archived)
		apiJSON, _ := json.Marshal(apiObjects)
		cliJSON, _ := json.Marshal(cliObjects)
		if !bytes.Equal(apiJSON, wantJSON) || !bytes.Equal(cliJSON, wantJSON) {
			t.Fatal("public Library extraction/lifecycle identity mismatch")
		}
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("Library archive/read created processing work")
	}
}

func TestNotesArchiveAcceptanceProjectionLagPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	object := notesFenceObject(t, f, "topic-one/source.md")
	citation := archiveSurfaceCitation(t, f, object)
	assertNoUnprojected := func() {
		t.Helper()
		for _, lifecycle := range []SourceLifecycleFilter{"", SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
			objects, err := f.s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{SourceLifecycle: lifecycle})
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range objects {
				if object.RelativePath == "topic-one/source.md" || object.RelativePath == "topic-one/second.md" {
					t.Fatalf("unprojected physical custody is readable as %s via %s", object.SourceLifecycle, lifecycle)
				}
			}
			want := 3
			if lifecycle == SourceLifecycleFilterArchived {
				want = 0
			}
			if len(objects) != want {
				t.Fatalf("unrelated object visibility changed: %d != %d", len(objects), want)
			}
			if _, err := f.s.store.GetKnowledgeObjectWithLifecycle(t.Context(), object.KnowledgeObjectID, lifecycle); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unprojected exact object: %v", err)
			}
			citation.SourceLifecycle = lifecycle
			if _, err := f.s.GetNotesPassage(t.Context(), citation); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unprojected passage: %v", err)
			}
			result, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", Mode: NotesSearchModeLexical, SourceLifecycle: lifecycle, Limit: 10})
			if err != nil || result.ResultCount != want || result.ArchivedMatchesOmitted != 0 {
				t.Fatalf("unprojected search: %+v %v", result, err)
			}
			overview, err := f.s.GetNotesOverview(t.Context(), NotesOverviewInput{SourceLifecycle: lifecycle})
			if err != nil || overview.Totals.ObjectCount != want {
				t.Fatalf("unprojected overview: %+v %v", overview.Totals, err)
			}
		}
	}
	f.p.Workspace.FailureHook = func(at storagearchive.WorkspaceMoveBoundary) error {
		if at == storagearchive.BoundaryAfterIntent {
			return errors.New("acceptance stop after intent")
		}
		return nil
	}
	if _, err := f.p.Workspace.ApplyArchive(t.Context(), f.plan, f.plan.PlanDigest); err == nil {
		t.Fatal("pending boundary not exercised")
	}
	assertNoUnprojected()
	f.p.Workspace.FailureHook = nil
	if _, err := f.p.Workspace.RecoverOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	assertNoUnprojected()
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	restore := f.restore(t)
	assertNoUnprojected()
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("custody reads changed source or processing identity")
	}
	// A genuinely new post-restore source is not a member of an old intent.
	seedNotesArchiveSibling(t, f.s, f.root, "topic-one/new.md", "New post-restore source.")
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	objects, err := f.s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.RelativePath == "topic-one/new.md" {
			if object.SourceLifecycle != SourceLifecycleActive {
				t.Fatal("new source inherited old custody")
			}
			return
		}
	}
	t.Fatal("new post-restore source was hidden by old intents")
}
