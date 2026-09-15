package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
)

func TestNotesSearchFollowupCommandRoundtrip(t *testing.T) {
	input := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: knowledge.SourceLifecycleFilterArchived}
	result := knowledge.NotesSearchResult{SourceKind: knowledge.KnowledgeSearchSourceKind, KnowledgeObjectID: input.KnowledgeObjectID, KnowledgeObjectVersionID: input.KnowledgeObjectVersionID, KnowledgeChunkID: input.KnowledgeChunkID, PassageFollowup: &input, NotesCustodyContext: knowledge.NotesCustodyContext{SourceLifecycle: knowledge.SourceLifecycleArchived}}
	line := notesSearchFollowupLine(result, 2)
	if len(line) > 512 || !strings.HasPrefix(line, "Follow-up [2]: loom ") {
		t.Fatalf("invalid follow-up: %q", line)
	}
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		q := req.URL.Query()
		if req.Method != http.MethodGet || req.URL.Path != "/v1/knowledge/notes/passages/"+input.KnowledgeChunkID || len(q) != 4 || q.Get("object_id") != input.KnowledgeObjectID || q.Get("version_id") != input.KnowledgeObjectVersionID || q.Get("source_hash") != input.SourceHash || q.Get("source_lifecycle") != "archived" {
			t.Errorf("follow-up lost custody: %s", req.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("followup", knowledge.NotesPassage{NotesPassageInput: input, Text: "original version", Historical: true}))
	})
	defer stop()
	args := append([]string{"--json", "--socket", socket}, strings.Fields(strings.TrimPrefix(line, "Follow-up [2]: loom "))...)
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("copyable command failed: %s %v", out.String(), err)
	}
	var got knowledge.NotesPassage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.NotesPassageInput != input || !got.Historical || calls != 1 {
		t.Fatalf("roundtrip: %+v calls=%d %v", got, calls, err)
	}
	for _, mutate := range []func(*knowledge.NotesSearchResult){
		func(r *knowledge.NotesSearchResult) { r.PassageFollowup = nil },
		func(r *knowledge.NotesSearchResult) { r.MetadataOnly = true },
		func(r *knowledge.NotesSearchResult) { r.TextSource = "metadata_text" },
		func(r *knowledge.NotesSearchResult) { r.KnowledgeObjectID = ids.NewKnowledgeObjectID() },
		func(r *knowledge.NotesSearchResult) { r.PassageFollowup.SourceLifecycle = "all" },
		func(r *knowledge.NotesSearchResult) { r.PassageFollowup.KnowledgeChunkID = "../private\n--flag" },
		func(r *knowledge.NotesSearchResult) { r.PassageFollowup.SourceHash = strings.Repeat("x", 4096) },
	} {
		bad := result
		copy := input
		bad.PassageFollowup = &copy
		mutate(&bad)
		if notesSearchFollowupLine(bad, 1) != "" {
			t.Fatal("invalid result generated command")
		}
	}
	if calls != 1 {
		t.Fatal("rendering performed transport")
	}
}

func TestNotesSearchFollowupHumanLine(t *testing.T) {
	for _, lifecycle := range []knowledge.SourceLifecycleFilter{knowledge.SourceLifecycleFilterActive, knowledge.SourceLifecycleFilterArchived} {
		input := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: lifecycle}
		wire, _ := json.Marshal(map[string]any{"knowledge_object_id": input.KnowledgeObjectID, "knowledge_object_version_id": input.KnowledgeObjectVersionID, "knowledge_chunk_id": input.KnowledgeChunkID, "source_lifecycle": lifecycle, "source_kind": knowledge.KnowledgeSearchSourceKind, "passage_followup": input})
		var result knowledge.NotesSearchResult
		if err := json.Unmarshal(wire, &result); err != nil {
			t.Fatal(err)
		}
		cmd, out, _ := bufferedCommand()
		renderNotesSearchResults(cmd, &options{}, knowledge.NotesSearchResultSet{Results: []knowledge.NotesSearchResult{result}})
		want := "Follow-up [1]: loom notes passage get " + input.KnowledgeChunkID + " --object " + input.KnowledgeObjectID + " --version " + input.KnowledgeObjectVersionID + " --source-hash " + input.SourceHash + " --source-lifecycle " + string(lifecycle) + "\n"
		if !strings.Contains(out.String(), want) || len(want) > 512 {
			t.Fatalf("missing complete bounded follow-up: %s", out.String())
		}
		out.Reset()
		renderNotesSearchResults(cmd, &options{plainOutput: true}, knowledge.NotesSearchResultSet{Results: []knowledge.NotesSearchResult{result}})
		if out.String() != input.KnowledgeObjectID+"\n" {
			t.Fatalf("plain changed: %q", out.String())
		}
	}
}

func TestNotesArchiveLifecycleCLIControls(t *testing.T) {
	for _, command := range []string{"search", "overview", "roots list", "objects list", "objects show", "passage get"} {
		root := newNotesCommand(&options{})
		cmd, _, err := root.Find(strings.Fields(command))
		if err != nil || cmd.Flags().Lookup("source-lifecycle") == nil {
			t.Fatalf("missing lifecycle on %s", command)
		}
		for _, value := range []string{"active", "archived", "all"} {
			if err := cmd.Flags().Set("source-lifecycle", value); err != nil {
				t.Fatal(err)
			}
		}
		for _, value := range []string{"unknown", "Active", " all"} {
			if err := cmd.Flags().Set("source-lifecycle", value); err == nil {
				t.Fatalf("accepted %q", value)
			}
		}
	}
	parsed := knowledge.ParseNotesSearchInput("cobalt lifecycle:archived")
	if got := mergeNotesSearchCommandInput(parsed, knowledge.NotesSearchInput{}); got.SourceLifecycle != knowledge.SourceLifecycleFilterArchived {
		t.Fatal("empty flags erased explicit query")
	}
	if got := mergeNotesSearchCommandInput(parsed, knowledge.NotesSearchInput{SourceLifecycle: knowledge.SourceLifecycleFilterActive}); got.SourceLifecycle != knowledge.SourceLifecycleFilterActive {
		t.Fatal("explicit flag did not win")
	}
	root := newNotesCommand(&options{})
	cmd, _, _ := root.Find([]string{"roots", "list"})
	if err := cmd.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	if cmd.Flags().Lookup("source-lifecycle").Value.String() != "" {
		t.Fatal("--all enabled archive reads")
	}
}

func TestNotesArchiveLifecycleCLIHumanContext(t *testing.T) {
	cmd, out, _ := bufferedCommand()
	when := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	custody := knowledge.NotesCustodyContext{SourceLifecycle: knowledge.SourceLifecycleArchived, OriginalPath: "Box/original", CanonicalPath: "Archive/canonical", ArchiveOperationID: "archive_exact", ArchivedAt: &when}
	renderNotesSearchResults(cmd, &options{}, knowledge.NotesSearchResultSet{SourceLifecycle: knowledge.SourceLifecycleFilterAll,
		LifecycleGroups: []knowledge.NotesSearchLifecycleGroup{{SourceLifecycle: knowledge.SourceLifecycleArchived, Offset: 0, ResultCount: 1}},
		Results:         []knowledge.NotesSearchResult{{NotesCustodyContext: custody, KnowledgeObjectID: "object_exact", Snippet: "retained passage"}}})
	for _, want := range []string{"[archived Notes]", "Archive/canonical", "Box/original", "archive_exact", "archived_at=2026-09-10T01:02:03Z"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	renderNotesSearchResults(cmd, &options{}, knowledge.NotesSearchResultSet{SourceLifecycle: knowledge.SourceLifecycleFilterActive, ArchivedMatchesOmitted: 500, ArchivedMatchesOmittedTruncated: true})
	if !strings.Contains(out.String(), "archived matches omitted=500 truncated=true") || strings.Contains(out.String(), "retained passage") {
		t.Fatal("omitted count truth")
	}
}

func TestNotesPassageCLIRequiresExactCitationBeforeTransport(t *testing.T) {
	for _, args := range [][]string{
		{"notes", "passage", "get", ids.NewKnowledgeChunkID()},
		{"notes", "passage", "get", "latest", "--object", ids.NewKnowledgeObjectID(), "--version", ids.NewKnowledgeObjectVersionID(), "--source-hash", "sha256:" + strings.Repeat("a", 64)},
	} {
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--json", "--socket", "/not-a-live-loom-socket"}, args...))
		if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(out.String(), "notes.invalid_passage") || strings.Contains(out.String(), "transport.unavailable") {
			t.Fatalf("invalid citation reached transport: %s %v", out.String(), err)
		}
	}
}

func TestBoxSourceCLIContextAndCategoryFlags(t *testing.T) {
	opts := &options{}
	for _, command := range []string{"search", "overview", "roots list", "objects list"} {
		root := newNotesCommand(opts)
		cmd, _, err := root.Find(strings.Fields(command))
		if err != nil || cmd.Flags().Lookup("category") == nil {
			t.Fatalf("missing category on %s", command)
		}
	}
	parsed := mergeNotesSearchCommandInput(knowledge.ParseNotesSearchInput("brainstorm"), knowledge.NotesSearchInput{SourceCategory: "topics"})
	if parsed.SourceCategory != "topics" {
		t.Fatal("category lost")
	}
	cmd, out, _ := bufferedCommand()
	renderNotesSearchResults(cmd, opts, knowledge.NotesSearchResultSet{Results: []knowledge.NotesSearchResult{{SourceContext: knowledge.SourceContext{SourceCategory: "topics", SourcePosture: "draft"}}}})
	if !strings.Contains(out.String(), "topics") || !strings.Contains(out.String(), "draft") {
		t.Fatal("source posture hidden from human output")
	}
}

func TestRenderNotesOverview(t *testing.T) {
	cmd, stdout, _ := bufferedCommand()
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	rebuildAt := now.Add(-2 * time.Hour)
	indexedAt := now.Add(-30 * time.Minute)
	pipelineAt := now.Add(-20 * time.Minute)

	renderNotesOverview(cmd, &options{}, knowledge.NotesOverview{
		GeneratedAt: now,
		Totals: knowledge.NotesOverviewTotals{
			RootCount:           2,
			ActiveRootCount:     2,
			ObjectCount:         4,
			FileCount:           3,
			DirectoryCount:      1,
			SizeBytes:           4096,
			SearchDocumentCount: 5,
		},
		FileClasses: []knowledge.NotesFileClassCount{
			{FileClass: knowledge.NotesFileClassBucketMarkdown, Count: 2, SizeBytes: 2048},
			{FileClass: knowledge.NotesFileClassBucketImage, Count: 1, SizeBytes: 1024},
		},
		ProcessingStates: []knowledge.NotesProcessingStateCount{
			{ProcessingState: knowledge.ProcessingStateChunked, Count: 2},
		},
		ExtractionStates: []knowledge.NotesExtractionStateCount{
			{ExtractionStatus: knowledge.ExtractionStatusExtracted, Count: 2},
			{ExtractionStatus: knowledge.ExtractionStatusTooLarge, Count: 1},
		},
		Projection: knowledge.NotesProjectionOverview{
			ProjectionRoot: "/srv/loom/loom-notes",
			Exists:         true,
			LastRebuildAt:  &rebuildAt,
			Entries:        3,
			Materialized:   3,
			ReadOnly:       true,
		},
		IndexHealth: knowledge.NotesIndexHealth{
			SearchDocumentCount:  5,
			Complete:             3,
			Failed:               1,
			LastIndexedAt:        &indexedAt,
			LastPipelineUpdateAt: &pipelineAt,
		},
		Nodes: []knowledge.NotesNodeOverview{{
			NodeKey: "main",
			Totals:  knowledge.NotesOverviewTotals{RootCount: 2, FileCount: 3, SearchDocumentCount: 5},
			Roots: []knowledge.NotesRootOverview{
				{
					RootKind:         knowledge.RootKindBoxNotes,
					DisplayName:      "Box Notes",
					Status:           knowledge.SourceRootStatusActive,
					RootRelativePath: "Notes",
					Totals:           knowledge.NotesOverviewTotals{FileCount: 2, ObjectCount: 3, SearchDocumentCount: 4},
				},
				{
					RootKind:         knowledge.RootKindProjectNotes,
					ProjectSlug:      "osint-tools",
					DisplayName:      "Project Notes",
					Status:           knowledge.SourceRootStatusActive,
					RootRelativePath: "Projects/osint-tools/notes",
					Totals:           knowledge.NotesOverviewTotals{FileCount: 1, ObjectCount: 1, SearchDocumentCount: 1},
				},
			},
		}},
	})

	output := stdout.String()
	for _, fragment := range []string{
		"LOOM notes",
		"Roots: 2 active=2",
		"Objects: 4 files=3 directories=1 bytes=4096",
		"File types",
		"markdown",
		"Extraction",
		"too_large",
		"Projection",
		"Entries: 3 materialized=3 missing=0 skipped=0 findings=0",
		"Search health",
		"Pipeline: queued=0 processing=0 complete=3 failed=1 skipped_unsupported=0",
		"Notes across network",
		"Node main: roots=2 files=3 search_documents=5",
		"osint-tools",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("overview output missing %q:\n%s", fragment, output)
		}
	}
}

func TestRenderNotesOverviewPlain(t *testing.T) {
	cmd, stdout, _ := bufferedCommand()
	renderNotesOverview(cmd, &options{plainOutput: true}, knowledge.NotesOverview{
		Totals: knowledge.NotesOverviewTotals{
			RootCount:           1,
			ActiveRootCount:     1,
			ObjectCount:         2,
			FileCount:           2,
			SizeBytes:           128,
			SearchDocumentCount: 2,
		},
	})

	want := "roots=1 active_roots=1 objects=2 files=2 directories=0 bytes=128 search_documents=2\n"
	if got := stdout.String(); got != want {
		t.Fatalf("plain overview output = %q, want %q", got, want)
	}
}

func TestRenderNotesSearchResultsShowsGroupedReadableFields(t *testing.T) {
	cmd, stdout, _ := bufferedCommand()
	selectedAt := time.Date(2026, 7, 3, 9, 30, 0, 0, time.UTC)

	renderNotesSearchResults(cmd, &options{}, knowledge.NotesSearchResultSet{
		Query:          "capability routing",
		Mode:           knowledge.NotesSearchModeHybrid,
		FallbackReason: "embedding_runtime_unavailable",
		Results: []knowledge.NotesSearchResult{{
			FinalScore:       4.25,
			SourceNodeKey:    "main",
			ProjectID:        "project_osint",
			FileClass:        "markdown",
			TextSource:       knowledge.TextSourceEmbeddedText,
			ExtractionStatus: knowledge.ExtractionStatusExtracted,
			RelativePath:     "reports/daily.md",
			StructuralPath:   "Summary",
			Snippet:          "Capability routing uses typed provider calls.",
			MatchReasons:     []string{"title", "path", "body"},
			KnowledgeChunkID: "knowledge_chunk_test",
			RecencyAt:        selectedAt,
		}},
	})

	output := stdout.String()
	for _, want := range []string{
		"SCORE",
		"DATE",
		"2026-07-03",
		"MATCH",
		"SOURCE",
		"embedded_text",
		"extracted",
		"mode=hybrid",
		"fallback=embedding_runtime_unavailable",
		"title,path,body",
		"reports/daily.md > Summary",
		"Capability routing uses typed provider calls.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("search output missing %q:\n%s", want, output)
		}
	}
}

func TestNotesSearchRequiresQueryJSON(t *testing.T) {
	stdout, stderr, err := executeRootCommand("--json", "notes", "search")
	if err == nil {
		t.Fatal("notes search without a query should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("json error should write a response envelope to stdout")
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error envelope: %v output=%s", decodeErr, stdout)
	}
	if envelope.OK || envelope.Error.Code != "notes.search_query_required" {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func TestRenderNotesObjectsShowsExtractionState(t *testing.T) {
	cmd, stdout, _ := bufferedCommand()
	projectID := "project_osint"
	renderNotesObjects(cmd, &options{}, []knowledge.KnowledgeObject{{
		KnowledgeObjectID: "knowledge_object_pdf",
		NotesSourceRootID: "notes_root_project",
		SourceNodeKey:     "main",
		ProjectID:         &projectID,
		RelativePath:      "notes/report.pdf",
		FileClass:         "pdf",
		ProcessingState:   knowledge.ProcessingStateMetadataOnly,
		Metadata:          json.RawMessage(`{"extraction":{"status":"password_required"}}`),
	}})

	output := stdout.String()
	for _, want := range []string{"EXTRACTION", "password_required", "notes/report.pdf"} {
		if !strings.Contains(output, want) {
			t.Fatalf("objects output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderNotesObjectShowsExtractionSummary(t *testing.T) {
	cmd, stdout, _ := bufferedCommand()
	size := int64(2048)
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	renderNotesObject(cmd, knowledge.KnowledgeObject{
		KnowledgeObjectID: "knowledge_object_docx",
		NotesSourceRootID: "notes_root_project",
		SourceNodeKey:     "main",
		RelativePath:      "notes/brief.docx",
		FileClass:         "office_document",
		SizeBytes:         &size,
		ProcessingState:   knowledge.ProcessingStateChunked,
		PipelineKey:       knowledge.KnowledgeObjectPipelineNotesFileExtraction,
		PipelineVersion:   knowledge.KnowledgeFileExtractionPipelineVersion,
		LastSeenAt:        now,
		Metadata: json.RawMessage(`{"extraction":{
			"status":"extracted",
			"extractor_key":"docx",
			"extractor_version":"v1",
			"text_section_count":2,
			"chunk_count":2,
			"link_count":1,
			"heading_count":1,
			"warnings":["minor"]
		}}`),
	})

	output := stdout.String()
	for _, want := range []string{"Knowledge object: knowledge_object_docx", "Extraction: status=extracted", "extractor=docx", "chunks=2", "warnings=1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("object output missing %q:\n%s", want, output)
		}
	}
}

func TestMergeNotesSearchCommandInputKeepsParsedFiltersAndAppliesFlags(t *testing.T) {
	merged := mergeNotesSearchCommandInput(
		knowledge.ParseNotesSearchInput(`tag:osint "exact phrase" threat intel`),
		knowledge.NotesSearchInput{
			ProjectRef:    "osint-tools",
			SourceNodeKey: "main",
			Tags:          []string{"loom"},
			Mode:          knowledge.NotesSearchModeSemantic,
			After:         "2026-01-01",
			Before:        "2026-02-01",
			Sort:          knowledge.NotesSearchSortNewest,
			Limit:         7,
		},
	)

	if merged.Query != "exact phrase threat intel" || merged.ProjectRef != "osint-tools" || merged.SourceNodeKey != "main" || merged.Mode != knowledge.NotesSearchModeSemantic {
		t.Fatalf("merged input = %#v", merged)
	}
	if len(merged.Phrases) != 1 || merged.Phrases[0] != "exact phrase" {
		t.Fatalf("phrases = %#v", merged.Phrases)
	}
	if strings.Join(merged.Tags, ",") != "osint,loom" {
		t.Fatalf("tags = %#v, want parsed and flag tags", merged.Tags)
	}
	if merged.Limit != 7 {
		t.Fatalf("limit = %d, want 7", merged.Limit)
	}
	if merged.After != "2026-01-01" || merged.Before != "2026-02-01" || merged.Sort != knowledge.NotesSearchSortNewest {
		t.Fatalf("absolute-time controls = %#v", merged)
	}
}

func TestRenderNotesEmbeddingStatus(t *testing.T) {
	cmd, stdout, _ := bufferedCommand()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	renderNotesEmbeddingStatus(cmd, knowledge.EmbeddingStatus{
		Settings: knowledge.EmbeddingSettings{
			Enabled:    true,
			RuntimeKey: knowledge.EmbeddingRuntimeOllama,
			ModelKey:   knowledge.EmbeddingModelMXBAIEmbedLarge,
			OllamaURL:  "http://127.0.0.1:11434",
			Dimensions: knowledge.DefaultEmbeddingDimensions,
		},
		Queue:           knowledge.EmbeddingQueueCounts{Queued: 3, Ready: 2, Processing: 1, Failed: 1},
		Objects:         knowledge.EmbeddingObjectCounts{Queued: 3, Processing: 1, Complete: 4, Failed: 1},
		ActiveVectors:   7,
		Historical:      2,
		ReusableVectors: 1,
		GeneratedAt:     now,
	})

	output := stdout.String()
	for _, want := range []string{
		"Notes embeddings: ON",
		"Runtime: ollama model=mxbai-embed-large endpoint=http://127.0.0.1:11434 dimensions=1024",
		"Queue: queued=3 ready=2 processing=1 failed=1",
		"Objects: queued=3 processing=1 complete=4 failed=1",
		"Vectors: active=7 historical=2 reusable=1",
		"Generated: 2026-07-05T12:00:00Z",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("embedding status output missing %q:\n%s", want, output)
		}
	}
}

func TestNotesCommandsExposeUnifiedPipelineAndWorkerSurface(t *testing.T) {
	root := newNotesCommand(&options{})
	for _, path := range [][]string{{"pipelines", "status"}, {"pipelines", "list"}, {"pipelines", "inspect"}, {"pipelines", "failures"}, {"pipelines", "retry"}, {"pipelines", "policy", "status"}, {"pipelines", "policy", "set"}, {"pipelines", "backfill"}, {"run", "coordinator"}, {"run", "heavy"}} {
		command, _, err := root.Find(path)
		if err != nil || command == nil || command.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
	}
}
