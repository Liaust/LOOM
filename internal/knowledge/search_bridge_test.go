package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	lexical "loom.local/loom/internal/search"
	"loom.local/loom/internal/storagecatalog"
)

func TestNotesSearchFollowupWireCompatibility(t *testing.T) {
	input := NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: SourceLifecycleFilterArchived}
	wire, err := json.Marshal(map[string]any{"knowledge_object_id": input.KnowledgeObjectID, "knowledge_object_version_id": input.KnowledgeObjectVersionID, "knowledge_chunk_id": input.KnowledgeChunkID, "source_lifecycle": "archived", "source_kind": KnowledgeSearchSourceKind, "passage_followup": input})
	if err != nil {
		t.Fatal(err)
	}
	var result NotesSearchResult
	if err := json.Unmarshal(wire, &result); err != nil {
		t.Fatal(err)
	}
	roundtrip, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Followup *NotesPassageInput `json:"passage_followup"`
	}
	if err := json.Unmarshal(roundtrip, &got); err != nil || got.Followup == nil || *got.Followup != input {
		t.Fatalf("optional exact tuple did not survive typed wire: %s %v", roundtrip, err)
	}
	for _, old := range []string{`{}`, `{"results":[]}`, `{"knowledge_object_id":"legacy","knowledge_chunk_id":"old"}`} {
		var legacy NotesSearchResult
		if err := json.Unmarshal([]byte(old), &legacy); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(legacy)
		if err != nil || strings.Contains(string(encoded), "passage_followup") {
			t.Fatalf("old response gained a tuple: %s %v", encoded, err)
		}
	}
}

func TestNotesSearchExactLexicalVersionProjection(t *testing.T) {
	query, err := buildNotesSearchQuery(NotesSearchInput{Query: "cobalt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, "kov.source_hash, '') AS passage_source_hash") || !strings.Contains(query.SQL, "s.passage_source_hash") {
		t.Fatal("lexical result does not select the matched version hash")
	}
}

func notesFollowupResultFixture() NotesSearchResult {
	return NotesSearchResult{SourceKind: KnowledgeSearchSourceKind, KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), NotesCustodyContext: NotesCustodyContext{SourceLifecycle: SourceLifecycleActive}}
}

func TestNotesSearchFollowupEligibility(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, lifecycle := range []SourceLifecycle{SourceLifecycleActive, SourceLifecycleArchived} {
		result := notesFollowupResultFixture()
		result.SourceLifecycle = lifecycle
		result.PassageFollowup = notesSearchPassageFollowup(result, hash, false)
		if !result.ValidPassageFollowup() || result.PassageFollowup.SourceHash != hash || string(result.PassageFollowup.SourceLifecycle) != string(lifecycle) {
			t.Fatal("valid exact tuple omitted")
		}
	}
	for name, mutate := range map[string]func(*NotesSearchResult){
		"metadata-only":   func(r *NotesSearchResult) { r.MetadataOnly = true },
		"metadata-text":   func(r *NotesSearchResult) { r.TextSource = "metadata_text" },
		"metadata-kind":   func(r *NotesSearchResult) { r.SourceKind = KnowledgeMetadataSearchSourceKind },
		"missing-kind":    func(r *NotesSearchResult) { r.SourceKind = "" },
		"missing-version": func(r *NotesSearchResult) { r.KnowledgeObjectVersionID = "" },
		"missing-chunk":   func(r *NotesSearchResult) { r.KnowledgeChunkID = "" },
		"wrong-prefix":    func(r *NotesSearchResult) { r.KnowledgeObjectVersionID = r.KnowledgeObjectID },
		"path":            func(r *NotesSearchResult) { r.KnowledgeChunkID = "../private/file" },
		"control":         func(r *NotesSearchResult) { r.KnowledgeObjectID += "\n--flag" },
		"all":             func(r *NotesSearchResult) { r.SourceLifecycle = "all" },
		"unknown":         func(r *NotesSearchResult) { r.SourceLifecycle = "other" },
		"empty":           func(r *NotesSearchResult) { r.SourceLifecycle = "" },
	} {
		t.Run(name, func(t *testing.T) {
			result := notesFollowupResultFixture()
			mutate(&result)
			if got := notesSearchPassageFollowup(result, hash, false); got != nil {
				t.Fatalf("invalid row gained tuple: %+v", got)
			}
		})
	}
	for _, bad := range []string{"", " ", "sha256:wrong", hash + "\n", "/private/hash", "https://user:password@example.test", strings.Repeat("a", 4096)} {
		if notesSearchPassageFollowup(notesFollowupResultFixture(), bad, false) != nil {
			t.Fatal("invalid hash accepted")
		}
	}
	if notesSearchPassageFollowup(notesFollowupResultFixture(), hash, true) != nil {
		t.Fatal("SQL metadata extraction gained tuple")
	}
	for _, mutate := range []func(*NotesPassageInput){
		func(p *NotesPassageInput) { p.KnowledgeObjectID = ids.NewKnowledgeObjectID() },
		func(p *NotesPassageInput) { p.KnowledgeObjectVersionID = ids.NewKnowledgeObjectVersionID() },
		func(p *NotesPassageInput) { p.KnowledgeChunkID = ids.NewKnowledgeChunkID() },
		func(p *NotesPassageInput) { p.SourceLifecycle = SourceLifecycleFilterArchived },
		func(p *NotesPassageInput) { p.SourceLifecycle = SourceLifecycleFilterAll },
		func(p *NotesPassageInput) { p.SourceHash = hash + "\x1b[31m" },
	} {
		result := notesFollowupResultFixture()
		result.PassageFollowup = notesSearchPassageFollowup(result, hash, false)
		mutate(result.PassageFollowup)
		if result.ValidPassageFollowup() {
			t.Fatal("mismatched/unvalidated response binding accepted")
		}
	}
}

func TestBoxSourceLiteralIdentifiersAndQuotedPhrases(t *testing.T) {
	if got := notesSearchTerms(NotesSearchInput{Query: "SYNTHETIC_NOT_A_SECRET caf\u00e9"}); !reflect.DeepEqual(got, []string{"synthetic_not_a_secret", "caf\u00e9"}) {
		t.Fatalf("identifier terms: %v", got)
	}
	input, err := normalizeNotesSearchTemporalInput(NotesSearchInput{Query: `"write contention"`})
	if err != nil || !reflect.DeepEqual(input.Phrases, []string{"write contention"}) {
		t.Fatalf("quoted query: %#v %v", input, err)
	}
	query, err := buildNotesSearchQuery(input)
	if err != nil || !strings.Contains(query.SQL, "NOT EXISTS (SELECT 1 FROM query_phrases") || strings.Contains(query.SQL, "LIKE '%' || qt.term") {
		t.Fatalf("nonliteral query: %v", err)
	}
}

func TestKnowledgeSearchDocumentMetadataIncludesSourceFieldsAndTags(t *testing.T) {
	projectID := ids.NewProjectID()
	nodeID := ids.NewNodeID()
	sourceModified := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	version := KnowledgeObjectVersion{
		KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(),
		SourceModifiedAt:         &sourceModified,
		RecencyAt:                sourceModified,
		RecencyBasis:             AbsoluteTimeBasisSourceFilesystemMtime,
		AbsoluteTimeMetadata:     mustJSON(t, map[string]any{"selected": "source_modified_at"}),
		Metadata: mustJSON(t, map[string]any{
			"frontmatter": map[string]any{
				"tags": []any{"OSINT", "#loom", "osint"},
			},
		}),
	}
	chunk := KnowledgeChunk{
		KnowledgeChunkID: ids.NewKnowledgeChunkID(),
		ChunkIndex:       2,
		StructuralPath:   "Runtime / Network",
	}
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		SourceNodeID:      &nodeID,
		SourceNodeKey:     "main",
		ProjectID:         &projectID,
		RelativePath:      "daily.md",
		SourcePath:        "/srv/loom-box/notes/daily.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}

	raw := knowledgeSearchDocumentMetadata(object, version, chunk)

	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if metadata["source_kind"] != KnowledgeSearchSourceKind ||
		metadata["knowledge_object_id"] != object.KnowledgeObjectID ||
		metadata["notes_source_root_id"] != object.NotesSourceRootID ||
		metadata["relative_path"] != object.RelativePath {
		t.Fatalf("metadata source fields = %#v", metadata)
	}
	if metadata["source_modified_at"] != "2020-02-03T04:05:06Z" ||
		metadata["recency_at"] != "2020-02-03T04:05:06Z" ||
		metadata["recency_basis"] != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("metadata absolute time fields = %#v", metadata)
	}
	tags, ok := metadata["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "osint" || tags[1] != "loom" {
		t.Fatalf("metadata tags = %#v, want normalized unique tags", metadata["tags"])
	}
}

func TestBuildNotesSearchQueryIncludesFilters(t *testing.T) {
	query, err := buildNotesSearchQuery(NotesSearchInput{
		Query:             "capability routing",
		ProjectID:         ids.NewProjectID(),
		SourceNodeKey:     "main",
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		FileClass:         storagecatalog.FileClassMarkdown,
		Path:              "daily",
		Tags:              []string{"OSINT", "#loom"},
		Limit:             7,
	})
	if err != nil {
		t.Fatalf("buildNotesSearchQuery returned error: %v", err)
	}
	for _, want := range []string{
		"query_terms AS",
		"search.lexical_terms",
		"bm25_scores AS",
		"source_kind IN ('knowledge_chunk', 'knowledge_object_metadata')",
		"loom.notes.yaml",
		"WHEN 'title' THEN 3.0",
		"WHEN 'tag' THEN 2.5",
		"ko.project_id =",
		"ko.source_node_key =",
		"ko.notes_source_root_id =",
		"ko.file_class =",
		"sd.metadata->>'text_source'",
		"sd.metadata->>'extraction_status'",
		"metadata_only",
		"ko.relative_path ILIKE",
		"(sd.metadata->'tags') ?",
		"ORDER BY final_score DESC",
		"LIMIT $11",
	} {
		if !strings.Contains(query.SQL, want) {
			t.Fatalf("query SQL missing %q:\n%s", want, query.SQL)
		}
	}
	if len(query.Args) != 11 {
		t.Fatalf("args len = %d, want 11: %#v", len(query.Args), query.Args)
	}
	termsJSON, ok := query.Args[1].(string)
	if !ok || !strings.Contains(termsJSON, "capability") || !strings.Contains(termsJSON, "osint") || !strings.Contains(termsJSON, "daily") {
		t.Fatalf("terms arg = %#v, want query/filter terms", query.Args[1])
	}
	if query.Args[9] != "loom" || query.Args[10] != 56 {
		t.Fatalf("tail args = %#v, want normalized tag and candidate limit", query.Args[9:])
	}
}

func TestBuildNotesSearchQueryRequiresRealMatchBeforeBoost(t *testing.T) {
	query, err := buildNotesSearchQuery(NotesSearchInput{
		Query: "quartz lantern",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("buildNotesSearchQuery returned error: %v", err)
	}
	if strings.Contains(query.SQL, "OR s.boost_score > 0") {
		t.Fatalf("query SQL allows generic boost-only matches:\n%s", query.SQL)
	}
	for _, want := range []string{
		"OR s.title_match",
		"OR s.heading_match",
		"OR s.path_match",
		"OR s.tag_match",
		"OR s.body_match",
		"OR s.phrase_match",
	} {
		if !strings.Contains(query.SQL, want) {
			t.Fatalf("query SQL missing real-match predicate %q:\n%s", want, query.SQL)
		}
	}
}

func TestParseNotesSearchInputParsesFiltersQuotesAndUnknownTokens(t *testing.T) {
	input := ParseNotesSearchInput(`project:osint-tools node:main tag:#osint path:"field reports" class:markdown root:notes author:alice "exact phrase" threat intel`)

	if input.Query != "author:alice exact phrase threat intel" {
		t.Fatalf("query = %q, want unknown token and phrase preserved as query terms", input.Query)
	}
	if input.ProjectRef != "osint-tools" || input.SourceNodeKey != "main" || input.Path != "field reports" || input.FileClass != "markdown" || input.RootRef != "notes" {
		t.Fatalf("unexpected filters: %#v", input)
	}
	if len(input.Tags) != 1 || input.Tags[0] != "osint" {
		t.Fatalf("tags = %#v, want osint", input.Tags)
	}
	if len(input.Phrases) != 1 || input.Phrases[0] != "exact phrase" {
		t.Fatalf("phrases = %#v, want exact phrase", input.Phrases)
	}
}

func TestParseNotesSearchQueryReturnsLexicalTerms(t *testing.T) {
	parsed := ParseNotesSearchQuery(`tag:Research "Threat Intel" threat-intel reports`)

	if parsed.Query != "Threat Intel threat-intel reports" {
		t.Fatalf("query = %q", parsed.Query)
	}
	if strings.Join(parsed.Terms, ",") != "threat,intel,reports" {
		t.Fatalf("terms = %#v, want unique normalized lexical terms", parsed.Terms)
	}
	if parsed.Input.Tags[0] != "research" {
		t.Fatalf("tags = %#v, want normalized research", parsed.Input.Tags)
	}
}

func TestNotesSearchResultCarriesScoreContractFields(t *testing.T) {
	result := NotesSearchResult{
		SourceKind:       KnowledgeSearchSourceKind,
		TextSource:       TextSourceEmbeddedText,
		ExtractionStatus: ExtractionStatusExtracted,
		RankScore:        0.4,
		BM25Score:        1.2,
		FTSScore:         0.4,
		BoostScore:       0.7,
		FinalScore:       2.3,
		MatchReasons:     []string{"title", "body"},
	}

	if result.BM25Score == 0 || result.FTSScore == 0 || result.BoostScore == 0 || result.FinalScore == 0 {
		t.Fatalf("score fields not populated: %#v", result)
	}
	if result.TextSource != TextSourceEmbeddedText || result.ExtractionStatus != ExtractionStatusExtracted {
		t.Fatalf("provenance fields not populated: %#v", result)
	}
	if strings.Join(result.MatchReasons, ",") != "title,body" {
		t.Fatalf("match reasons = %#v", result.MatchReasons)
	}
}

func TestGroupNotesSearchResultsKeepsBestCandidatePerObject(t *testing.T) {
	results := groupNotesSearchResults([]NotesSearchResult{
		{SearchDocumentID: "search_document_best", KnowledgeObjectID: "knowledge_object_a", KnowledgeChunkID: "chunk_best", FinalScore: 4},
		{SearchDocumentID: "search_document_duplicate", KnowledgeObjectID: "knowledge_object_a", KnowledgeChunkID: "chunk_other", FinalScore: 3},
		{SearchDocumentID: "search_document_b", KnowledgeObjectID: "knowledge_object_b", FinalScore: 2},
	}, 10)

	if len(results) != 2 {
		t.Fatalf("grouped len = %d, want 2: %#v", len(results), results)
	}
	if results[0].SearchDocumentID != "search_document_best" || results[0].KnowledgeChunkID != "chunk_best" {
		t.Fatalf("first grouped result = %#v, want best chunk for object a", results[0])
	}
}

func TestNotesSearchMatchReasonsAreStableAndReadable(t *testing.T) {
	reasons := notesSearchMatchReasons(notesSearchMatchFlags{
		Title:  true,
		Path:   true,
		Body:   true,
		Phrase: true,
	})

	if strings.Join(reasons, ",") != "title,path,body,phrase" {
		t.Fatalf("reasons = %#v", reasons)
	}
}

func TestNotesSearchCitationHandlesMetadataOnlyObjects(t *testing.T) {
	citation := notesSearchCitation(NotesSearchResult{
		KnowledgeObjectID: "knowledge_object_pdf",
		RelativePath:      "captures/map.pdf",
		SourceKind:        KnowledgeMetadataSearchSourceKind,
	})

	if citation.Label != "captures/map.pdf" || citation.SourceRef != "knowledge_object_pdf" {
		t.Fatalf("citation = %#v, want object-only source ref", citation)
	}
}

func TestTagsFromVersionMetadata(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"frontmatter": map[string]any{
			"tag":  "daily, #loom",
			"tags": []any{"OSINT", "loom"},
		},
	})

	tags := tagsFromVersionMetadata(raw)

	if strings.Join(tags, ",") != "osint,loom,daily" {
		t.Fatalf("tags = %#v, want stable normalized order", tags)
	}
}

func TestKnowledgeChunkLexicalDocumentInputCoversSearchFields(t *testing.T) {
	version := KnowledgeObjectVersion{
		KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(),
		Metadata: mustJSON(t, map[string]any{
			"frontmatter": map[string]any{"tags": []any{"OSINT", "research"}},
		}),
	}
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "reports/daily.md",
		SourcePath:        "/srv/loom-box/Notes/reports/daily.md",
		Title:             "Daily Report",
		FileClass:         storagecatalog.FileClassMarkdown,
	}
	chunk := KnowledgeChunk{
		KnowledgeChunkID: ids.NewKnowledgeChunkID(),
		StructuralPath:   "Network / Routing",
		ChunkText:        "Capability routing needs stronger notes search.",
	}

	input := knowledgeChunkLexicalDocumentInput("search_document_test", object, version, chunk)
	document, err := lexical.BuildLexicalDocument(input)
	if err != nil {
		t.Fatalf("BuildLexicalDocument returned error: %v", err)
	}

	if document.SourceKind != KnowledgeSearchSourceKind || document.ObjectID != object.KnowledgeObjectID {
		t.Fatalf("lexical identity = %#v", document)
	}
	if document.FieldLengths[lexical.LexicalFieldBody] == 0 ||
		document.FieldLengths[lexical.LexicalFieldHeading] == 0 ||
		document.FieldLengths[lexical.LexicalFieldPath] == 0 ||
		document.FieldLengths[lexical.LexicalFieldTag] == 0 {
		t.Fatalf("field lengths = %#v, want body/heading/path/tag fields", document.FieldLengths)
	}
	for _, want := range []struct {
		field string
		term  string
	}{
		{lexical.LexicalFieldTitle, "daily"},
		{lexical.LexicalFieldHeading, "routing"},
		{lexical.LexicalFieldPath, "reports"},
		{lexical.LexicalFieldTag, "osint"},
		{lexical.LexicalFieldBody, "capability"},
	} {
		if !hasLexicalTerm(document.Terms, want.field, want.term) {
			t.Fatalf("terms = %#v, missing %s:%s", document.Terms, want.field, want.term)
		}
	}
}

func TestMetadataOnlyKnowledgeObjectBuildsSearchableLexicalInput(t *testing.T) {
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		SourceNodeKey:     "main",
		RelativePath:      "captures/network-map.pdf",
		SourcePath:        "/srv/loom-box/Notes/captures/network-map.pdf",
		Title:             "network-map.pdf",
		FileClass:         storagecatalog.FileClassPDF,
		MimeType:          "application/pdf",
		ProcessingState:   ProcessingStateMetadataOnly,
		PipelineKey:       KnowledgeObjectPipelineMetadata,
	}

	if !shouldIndexKnowledgeObjectMetadata(object) {
		t.Fatalf("pdf object should be indexed through metadata")
	}
	input := knowledgeMetadataLexicalDocumentInput("search_document_metadata_test", object)
	document, err := lexical.BuildLexicalDocument(input)
	if err != nil {
		t.Fatalf("BuildLexicalDocument returned error: %v", err)
	}
	if document.SourceKind != KnowledgeMetadataSearchSourceKind || document.SourceID != object.KnowledgeObjectID {
		t.Fatalf("metadata lexical identity = %#v", document)
	}
	for _, want := range []struct {
		field string
		term  string
	}{
		{lexical.LexicalFieldTitle, "network"},
		{lexical.LexicalFieldPath, "captures"},
		{lexical.LexicalFieldBody, "pdf"},
	} {
		if !hasLexicalTerm(document.Terms, want.field, want.term) {
			t.Fatalf("metadata terms = %#v, missing %s:%s", document.Terms, want.field, want.term)
		}
	}

	var metadata map[string]any
	if err := json.Unmarshal(knowledgeMetadataSearchDocumentMetadata(object), &metadata); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if metadata["source_kind"] != KnowledgeMetadataSearchSourceKind || metadata["metadata_only"] != true || metadata["body_is_metadata"] != true {
		t.Fatalf("metadata payload = %#v", metadata)
	}
}

func TestShouldIndexKnowledgeObjectMetadataSkipsTextAndDeletedObjects(t *testing.T) {
	markdown := KnowledgeObject{FileClass: storagecatalog.FileClassMarkdown, ProcessingState: ProcessingStateMetadataOnly}
	if shouldIndexKnowledgeObjectMetadata(markdown) {
		t.Fatal("markdown should wait for text chunk indexing instead of metadata-only indexing")
	}
	sourceUnavailableMarkdown := KnowledgeObject{
		FileClass:       storagecatalog.FileClassMarkdown,
		ProcessingState: ProcessingStateMetadataOnly,
		Metadata:        json.RawMessage(`{"extraction":{"status":"source_unavailable"}}`),
	}
	if !shouldIndexKnowledgeObjectMetadata(sourceUnavailableMarkdown) {
		t.Fatal("metadata-only markdown extraction should be metadata indexed")
	}
	deletedAt := markdown.UpdatedAt
	image := KnowledgeObject{FileClass: storagecatalog.FileClassImage, ProcessingState: ProcessingStateDeleted, DeletedAt: &deletedAt}
	if shouldIndexKnowledgeObjectMetadata(image) {
		t.Fatal("deleted metadata object should not be indexed")
	}
}

func hasLexicalTerm(terms []lexical.LexicalTerm, field, term string) bool {
	for _, item := range terms {
		if item.FieldKey == field && item.Term == term && item.TermFrequency > 0 {
			return true
		}
	}
	return false
}
func TestBoxSourceContextAndCategoryFilters(t *testing.T) {
	for _, tc := range []struct{ kind, root, file, category, posture, topic, collection string }{
		{RootKindBoxNotes, "Notes", "daily.md", "notes", "source_material", "", ""},
		{RootKindProjectNotes, "Notes", "daily.md", "projects", "source_material", "", ""},
		{RootKindBoxTopics, "Topics", "atlas/ideas.md", "topics", "draft", "atlas", ""},
		{RootKindBoxTopics, "Topics/atlas", "ideas.md", "topics", "draft", "atlas", ""},
		{RootKindBoxTopics, "Topics", "ideas.md", "topics", "draft", "", ""},
		{RootKindBoxLibrary, "Library/cadence", "source.pdf", "library", "attributed_claim", "", "cadence"},
	} {
		got := sourceContextForRoot(SourceRoot{RootKind: tc.kind, RootRelativePath: tc.root}, tc.file)
		if got.SourceCategory != tc.category || got.SourcePosture != tc.posture || got.TopicKey != tc.topic || got.CollectionKey != tc.collection {
			t.Fatalf("context %s/%s: %#v", tc.root, tc.file, got)
		}
	}
	parsed := ParseNotesSearchInput("brainstorm category:topics")
	if parsed.Query != "brainstorm" || parsed.SourceCategory != "topics" {
		t.Fatalf("parsed: %#v", parsed)
	}
	for _, value := range []string{"notes", "topics", "library", "projects"} {
		query, err := buildNotesSearchQuery(NotesSearchInput{Query: "source", SourceCategory: value})
		if err != nil || !strings.Contains(query.SQL, "visibility_root.status = 'active'") || !strings.Contains(query.SQL, "visibility_entry.metadata") {
			t.Fatalf("category/visibility query: %v %s", err, query.SQL)
		}
	}
	if err := ValidateNotesSearchInput(NotesSearchInput{Query: "source", SourceCategory: "documents"}); err == nil {
		t.Fatal("undeclared category accepted")
	}
}
