package provenance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/httpapi"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/storagecatalog"
)

// This is source-citation acceptance, not unattended ingestion acceptance.
// The smoke owns both databases. Only ordinary catalog/admission/coordinator
// services create derived knowledge; no version or chunk rows are seeded.
func TestBoxSourceCitationHistoricalExactGetPostgres(t *testing.T) {
	root := os.Getenv("LOOM_BOX_CITATION_FIXTURE_ROOT")
	if root == "" {
		t.Skip("run tests/smoke/v2_box_knowledge_sources_local.sh with its owned PostgreSQL cluster")
	}
	if !strings.HasPrefix(root, "/tmp/loom-box-citation-") || filepath.Dir(root) != "/tmp" {
		t.Fatal("citation probe requires its short smoke-owned /tmp root")
	}
	mainURL := boxCitationDatabaseURL(t, root, "LOOM_BOX_CITATION_MAIN_URL", "loom_box_citation", "postgres")
	provenanceURL := boxCitationDatabaseURL(t, root, "LOOM_BOX_CITATION_PROVENANCE_URL", "loom_provenance", "loom_provenance")
	ctx := t.Context()
	if _, err := migrations.Up(ctx, mainURL, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", mainURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	nodeID := ids.NewNodeID()
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes
		(node_id,node_key,display_name,node_kind,node_role,runtime_class,status)
		VALUES ($1,'main','Disposable citation owner','server','main','native','active')`, nodeID); err != nil {
		t.Fatal(err)
	}
	boxRoot := filepath.Join(root, "Box")
	if err := os.MkdirAll(filepath.Join(boxRoot, "Notes"), 0700); err != nil {
		t.Fatal(err)
	}
	notes := knowledge.NewService(db)
	roots, err := notes.ReconcileSourceRoots(ctx, knowledge.SourceRootReconcileInput{
		BoxRegistrations: []knowledge.BoxWatchRootRegistration{{
			BoxRootPath: boxRoot, NodeID: nodeID, OwnerNodeKey: "main", AreaKey: "notes",
			LocalRootKey: "notes", BackendRootKey: "loom_box__notes", RootRelativePath: "Notes",
			SourceKinds: json.RawMessage(`["box_notes"]`), ActivationStatus: "active",
		}, {
			BoxRootPath: boxRoot, NodeID: nodeID, OwnerNodeKey: "main", AreaKey: "topics",
			LocalRootKey: "topics", BackendRootKey: "loom_box__topics", RootRelativePath: "Topics",
			SourceKinds: json.RawMessage(`["box_topics"]`), ActivationStatus: "reported",
			Metadata: json.RawMessage(`{"knowledge_source":{"enabled":true,"root_kind":"box_topics","root_relative_path":"Topics","category":"topics","include":["**/*.md"],"exclude":[]}}`),
		}},
	})
	if err != nil || len(roots.SourceRoots) != 2 {
		t.Fatalf("register fixture Notes root: %v %#v", err, roots)
	}
	versions := boxCitationFrozenVersions(t)
	entry := storagecatalog.RegisterEntryInput{
		StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaNotes,
		OriginNodeID: nodeID, OriginNodeKey: "main", WatchedRootKey: "loom_box__notes",
		LogicalPath: "cadence.md", OriginalSourcePath: "cadence.md", FileClass: "text",
		MimeType: "text/markdown", ChecksumAlgorithm: "sha256", AvailabilityState: storagecatalog.AvailabilityStateAvailable,
	}
	publishSource := func(entry *storagecatalog.RegisterEntryInput, area, heading, text, query string) (knowledge.NotesSearchResult, string) {
		t.Helper()
		body := []byte("# " + heading + "\n\n" + text + "\n")
		file := filepath.Join(boxRoot, area, entry.LogicalPath)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		size := int64(len(body))
		entry.SizeBytes = &size
		entry.ChecksumHex = fmt.Sprintf("%x", sha256.Sum256(body))
		registered, err := storagecatalog.NewService(db).RegisterEntry(ctx, *entry)
		if err != nil {
			t.Fatal(err)
		}
		entry.StorageEntryID = registered.StorageEntryID
		admitted, err := notes.AdmitRegisteredNotesOnce(ctx, knowledge.AdmissionCursor{}, 10)
		if err != nil || admitted.MoreWork {
			t.Fatalf("bounded admission: %#v %v", admitted, err)
		}
		for stage := 0; ; stage++ {
			if stage == 20 {
				t.Fatal("native coordinator did not converge")
			}
			result, err := notes.RunPipelineCoordinatorOnce(ctx, knowledge.PipelineCoordinatorRunInput{
				WorkerRunID: "box-citation-probe", Limit: 10, Now: time.Now().UTC(),
			})
			if err != nil || result.Failed != 0 {
				t.Fatalf("native extraction: %#v %v", result, err)
			}
			if result.Claimed == 0 {
				break
			}
		}
		result, err := notes.SearchNotes(ctx, knowledge.NotesSearchInput{Query: query, Mode: "lexical", Limit: 10})
		if err != nil || len(result.Results) != 1 || result.Results[0].KnowledgeObjectVersionID == "" || result.Results[0].KnowledgeChunkID == "" {
			t.Fatalf("real native passage required: %#v %v", result, err)
		}
		return result.Results[0], "sha256:" + entry.ChecksumHex
	}
	publish := func(text string) (knowledge.NotesSearchResult, string) {
		return publishSource(&entry, "Notes", "Cadence", text, "pilot")
	}
	old, oldHash := publish(versions[0])
	if !strings.Contains(old.StructuralPath, "Cadence") || !strings.Contains(old.Snippet, "03:15") {
		t.Fatalf("frozen note-p1 was not extracted with its heading: %#v", old)
	}
	openProvenance := func(migrate bool) (*provenance.Runtime, *provenance.FoundationAPI) {
		t.Helper()
		runtime, _, err := provenance.OpenRuntime(ctx, provenanceURL, migrate)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Close)
		api, err := provenance.NewFoundationAPI(runtime)
		if err != nil {
			t.Fatal(err)
		}
		return runtime, api
	}
	runtime, api := openProvenance(true)
	locator := boxCitationLocator(old, oldHash)
	version := old.KnowledgeObjectVersionID
	citation, err := json.Marshal(map[string]any{
		"knowledge_object_id": old.KnowledgeObjectID, "knowledge_object_version_id": version,
		"knowledge_chunk_id": old.KnowledgeChunkID, "source_hash": oldHash,
		"passage_locator": old.StructuralPath, "source_category": old.SourceCategory,
		"owner_node": old.SourceNodeKey, "source_posture": old.SourcePosture,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := provenance.CandidateRegistrationRequest{Candidates: []provenance.CandidateRegistration{{
		SchemaVersion: provenance.SchemaVersion, Claim: "The fixture note describes a daily 03:15 pilot.",
		RecordKind: "observation", RecordContext: "Disposable source-citation probe; not a confirmed user decision.",
		Domain: "box-citation-probe", Visibility: "private", AssertionPosture: "source_claim",
		Temporal: provenance.TemporalInterpretation{Interpretation: "source observation"},
		Producer: provenance.ProducerIdentity{ProducerID: "box-citation-test", ProducerKind: "working_agent", TaskID: "slice-4-probe"},
		Sources: []provenance.SourceRegistration{{
			Kind: "notes_passage", Status: "resolved", Verification: "content_verified",
			ResolverName: "disposable_fixture_reader", ResolverVersion: "1",
			CanonicalLocator: &locator, VersionAddress: &version, ContentDigest: &oldHash,
			Submitted: citation, Details: citation,
		}},
	}}}
	registered, err := api.RegisterCandidates(ctx, "box-citation-v1", request)
	if err != nil || len(registered.Receipts) != 1 {
		t.Fatalf("candidate registration: %#v %v", registered, err)
	}
	candidateID := registered.Receipts[0].CandidateID
	before, err := api.GetCandidate(ctx, candidateID, 50)
	if err != nil || before.EffectiveState != "pending" {
		t.Fatalf("pending exact-get: %#v %v", before, err)
	}
	current, currentHash := publish(versions[1])
	if old.KnowledgeObjectID != current.KnowledgeObjectID || version == current.KnowledgeObjectVersionID || oldHash == currentHash {
		t.Fatal("source edit did not preserve object and advance version/hash")
	}
	runtime.Close()
	_, api = openProvenance(false)
	replay, err := api.RegisterCandidates(ctx, "box-citation-v1", request)
	if err != nil || !replay.Replayed || replay.Receipts[0].CandidateID != candidateID {
		t.Fatalf("restart/replay: %#v %v", replay, err)
	}
	after, err := api.GetCandidate(ctx, candidateID, 50)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("edit/restart changed cited candidate: %v", err)
	}
	var payload struct {
		SourceResults []provenance.SourceReference `json:"source_results"`
	}
	if err := json.Unmarshal(after.Candidate.Payload, &payload); err != nil || len(payload.SourceResults) != 1 ||
		payload.SourceResults[0].VersionAddress == nil || *payload.SourceResults[0].VersionAddress != version ||
		payload.SourceResults[0].ContentDigest == nil || *payload.SourceResults[0].ContentDigest != oldHash {
		t.Fatalf("exact-get lost original citation: %#v %v", payload, err)
	}
	records, err := api.ListRecords(ctx, provenance.PageRequest{Limit: 50})
	if err != nil || len(records.Items) != 0 {
		t.Fatalf("source ingestion promoted a semantic record: %#v %v", records, err)
	}
	retained, err := notes.Store().GetKnowledgeChunk(ctx, old.KnowledgeChunkID)
	if err != nil || !strings.Contains(retained.ChunkText, versions[0]) {
		t.Fatalf("historical derived passage not retained internally: %#v %v", retained, err)
	}
	t.Log("PASS: frozen note-p1/p2 derived normally; stable object/new version; pinned pending citation survives edit, runtime reopen and registration replay; zero accepted records")

	// The exact passage surface is distinct from current-object metadata and
	// intentionally text-redacted operational pipeline diagnostics.
	socket := filepath.Join(root, "notes.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: httpapi.NewServer(httpapi.Services{DB: db}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler(), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Errorf("fixture HTTP shutdown: %v", err)
		}
	})
	client := localclient.New(socket)
	object, err := client.GetKnowledgeNotesObject(ctx, "citation-current", old.KnowledgeObjectID)
	if err != nil || object.Data.SourceHash != currentHash {
		t.Fatalf("current object exact-get: %#v %v", object, err)
	}
	t.Log("PASS: supported object exact-get now returns the current v2 source hash, not the cited v1 hash")
	runs, err := client.ListKnowledgeNotesPipelines(ctx, "citation-pipelines", knowledge.PipelineListInput{ObjectID: old.KnowledgeObjectID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	historicalPipeline := false
	for _, run := range runs.Data {
		if run.KnowledgeObjectVersionID != version {
			continue
		}
		inspection, err := client.GetKnowledgeNotesPipeline(ctx, "citation-inspect", run.KnowledgePipelineRunID)
		if err != nil || len(inspection.Data.Artifacts) == 0 {
			t.Fatalf("historical pipeline inspection: %#v %v", inspection, err)
		}
		for _, artifact := range inspection.Data.Artifacts {
			if artifact.TextContent != nil || artifact.PayloadRef != "" {
				t.Fatal("pipeline diagnostics unexpectedly expose source content; reassess the citation boundary")
			}
		}
		historicalPipeline = true
	}
	if !historicalPipeline {
		t.Fatal("no historical pipeline to check as a possible existing Notes read")
	}
	t.Log("PASS: supported historical pipeline inspection retains identity but intentionally strips artifact text/payload references; it is not a passage-read substitute")
	read := func(input knowledge.NotesPassageInput, want string, historical bool, status int) {
		t.Helper()
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		defer transport.CloseIdleConnections()
		httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		query := url.Values{"object_id": {input.KnowledgeObjectID}, "version_id": {input.KnowledgeObjectVersionID}, "source_hash": {input.SourceHash}}
		response, err := httpClient.Get("http://loom/v1/knowledge/notes/passages/" + input.KnowledgeChunkID + "?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		passage, clientErr := client.GetKnowledgeNotesPassage(ctx, "citation-historical", input)
		command := loomcli.NewRootCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs([]string{"--json", "--socket", socket, "notes", "passage", "get", input.KnowledgeChunkID,
			"--object", input.KnowledgeObjectID, "--version", input.KnowledgeObjectVersionID, "--source-hash", input.SourceHash})
		cliErr := command.ExecuteContext(ctx)
		if response.StatusCode != status {
			t.Fatalf("passage API status=%d want=%d body=%s", response.StatusCode, status, body)
		}
		if status != http.StatusOK {
			if clientErr == nil || cliErr == nil || !bytes.Contains(body, []byte("knowledge.notes_passage_not_found")) || bytes.Contains(body, []byte(versions[0])) || bytes.Contains(body, []byte(versions[1])) {
				t.Fatalf("passage refusal parity/content leak: %s %v %v", body, clientErr, cliErr)
			}
			return
		}
		var cliPassage knowledge.NotesPassage
		if clientErr != nil || cliErr != nil || json.Unmarshal(output.Bytes(), &cliPassage) != nil || !reflect.DeepEqual(passage.Data, cliPassage) ||
			passage.Data.NotesPassageInput != input || passage.Data.Historical != historical || !strings.Contains(passage.Data.Text, want) ||
			!bytes.Contains(body, []byte(want)) || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("bound passage parity: API=%s client=%#v/%v CLI=%s/%v", body, passage, clientErr, output.String(), cliErr)
		}
	}
	oldInput := knowledge.NotesPassageInput{KnowledgeObjectID: old.KnowledgeObjectID, KnowledgeObjectVersionID: version, KnowledgeChunkID: old.KnowledgeChunkID, SourceHash: oldHash}
	currentInput := knowledge.NotesPassageInput{KnowledgeObjectID: current.KnowledgeObjectID, KnowledgeObjectVersionID: current.KnowledgeObjectVersionID, KnowledgeChunkID: current.KnowledgeChunkID, SourceHash: currentHash}
	read(oldInput, versions[0], true, http.StatusOK)
	read(currentInput, versions[1], false, http.StatusOK)
	// A newly observed source can precede creation of its next derived version.
	// The retained current chunk must already report that it is historical.
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET source_hash=$2 WHERE knowledge_object_id=$1`, current.KnowledgeObjectID, "sha256:"+strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	read(currentInput, versions[1], true, http.StatusOK)
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET source_hash=$2 WHERE knowledge_object_id=$1`, current.KnowledgeObjectID, currentHash); err != nil {
		t.Fatal(err)
	}
	// Corruption is not a fallback to another chunk, source version or raw file.
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.knowledge_chunks SET chunk_text=chunk_text||'corrupt' WHERE knowledge_chunk_id=$1`, old.KnowledgeChunkID); err != nil {
		t.Fatal(err)
	}
	read(oldInput, "", true, http.StatusNotFound)
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.knowledge_chunks SET chunk_text=$2 WHERE knowledge_chunk_id=$1`, old.KnowledgeChunkID, retained.ChunkText); err != nil {
		t.Fatal(err)
	}
	read(oldInput, versions[0], true, http.StatusOK)
	var notesRootID string
	for _, root := range roots.SourceRoots {
		if root.RootKind == knowledge.RootKindBoxNotes {
			notesRootID = root.NotesSourceRootID
		}
	}
	for _, mismatch := range []knowledge.NotesPassageInput{
		{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: version, KnowledgeChunkID: old.KnowledgeChunkID, SourceHash: oldHash},
		{KnowledgeObjectID: old.KnowledgeObjectID, KnowledgeObjectVersionID: current.KnowledgeObjectVersionID, KnowledgeChunkID: old.KnowledgeChunkID, SourceHash: oldHash},
		{KnowledgeObjectID: old.KnowledgeObjectID, KnowledgeObjectVersionID: version, KnowledgeChunkID: old.KnowledgeChunkID, SourceHash: currentHash},
	} {
		read(mismatch, "", false, http.StatusNotFound)
	}
	// These mutations only change fixture-owned current access evidence. Neither
	// the derived historical passage nor the frozen citation is rewritten.
	for _, mutation := range []struct {
		name, change, undo string
		arg                any
	}{
		{"root disabled", `UPDATE knowledge.notes_source_roots SET status='disabled' WHERE notes_source_root_id=$1`, `UPDATE knowledge.notes_source_roots SET status='active' WHERE notes_source_root_id=$1`, notesRootID},
		{"owner disabled", `UPDATE nodes.nodes SET status='disabled' WHERE node_id=$1`, `UPDATE nodes.nodes SET status='active' WHERE node_id=$1`, nodeID},
		{"catalog privacy", `UPDATE storage.storage_entries SET metadata='{"private_no_index":true}' WHERE storage_entry_id=$1`, `UPDATE storage.storage_entries SET metadata='{}' WHERE storage_entry_id=$1`, entry.StorageEntryID},
		{"source removed", `UPDATE storage.storage_entries SET deleted_at=now() WHERE storage_entry_id=$1`, `UPDATE storage.storage_entries SET deleted_at=NULL WHERE storage_entry_id=$1`, entry.StorageEntryID},
		{"object deleted", `UPDATE knowledge.knowledge_objects SET deleted_at=now() WHERE knowledge_object_id=$1`, `UPDATE knowledge.knowledge_objects SET deleted_at=NULL WHERE knowledge_object_id=$1`, old.KnowledgeObjectID},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, mutation.change, mutation.arg); err != nil {
				t.Fatal(err)
			}
			read(oldInput, "", true, http.StatusNotFound)
			read(currentInput, "", false, http.StatusNotFound)
			if _, err := db.ExecContext(ctx, mutation.undo, mutation.arg); err != nil {
				t.Fatal(err)
			}
			read(oldInput, versions[0], true, http.StatusOK)
		})
	}
	end, err := api.GetCandidate(ctx, candidateID, 50)
	if err != nil || !reflect.DeepEqual(before, end) {
		t.Fatalf("access/read changed immutable citation: %v", err)
	}
	t.Log("PASS: exact historical/current API, client and CLI reads; identity mismatches and five current-access revocations fail closed; citation remains unchanged")

	topicEntry := storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaExternalWatchedRoot, OriginNodeID: nodeID, OriginNodeKey: "main", WatchedRootKey: "loom_box__topics", LogicalPath: "atlas/ideas.md", OriginalSourcePath: "atlas/ideas.md", FileClass: "text", MimeType: "text/markdown", ChecksumAlgorithm: "sha256", AvailabilityState: storagecatalog.AvailabilityStateAvailable}
	topicText := boxCitationFrozenText(t, "topic", "topic-v1")
	topic, topicHash := publishSource(&topicEntry, "Topics", "Cadence", topicText, "brainstorm")
	read(knowledge.NotesPassageInput{KnowledgeObjectID: topic.KnowledgeObjectID, KnowledgeObjectVersionID: topic.KnowledgeObjectVersionID, KnowledgeChunkID: topic.KnowledgeChunkID, SourceHash: topicHash}, topicText, false, 200)
	read(knowledge.NotesPassageInput{KnowledgeObjectID: old.KnowledgeObjectID, KnowledgeObjectVersionID: topic.KnowledgeObjectVersionID, KnowledgeChunkID: topic.KnowledgeChunkID, SourceHash: topicHash}, "", false, 404)
	boxCitationSemanticAcceptance(t, api, request.Candidates[0], candidateID, registered.Receipts[0].SourceResults[0].ID, topic, topicHash)
	read(oldInput, versions[0], true, 200)
}

func boxCitationLocator(hit knowledge.NotesSearchResult, hash string) string {
	return "/v1/knowledge/notes/passages/" + hit.KnowledgeChunkID + "?" + url.Values{"object_id": {hit.KnowledgeObjectID}, "version_id": {hit.KnowledgeObjectVersionID}, "source_hash": {hash}}.Encode()
}

func boxCitationSemanticAcceptance(t *testing.T, api *provenance.FoundationAPI, original provenance.CandidateRegistration, candidateID, sourceID provenance.SemanticID, topic knowledge.NotesSearchResult, topicHash string) {
	t.Helper()
	ctx := t.Context()
	accepted := original
	accepted.Claim = boxCitationFrozenSemanticText(t, "accepted-cadence")
	accepted.RecordKind = "decision"
	accepted.RecordContext = "Explicit synthetic reviewer decision; not inferred from a source."
	accepted.AssertionPosture = "accepted_decision"
	pending := original
	pending.Claim = boxCitationFrozenSemanticText(t, "pending-hourly")
	pending.RecordKind = "proposal"
	pending.AssertionPosture = "unaccepted_proposal"
	locator, version := boxCitationLocator(topic, topicHash), topic.KnowledgeObjectVersionID
	details, _ := json.Marshal(map[string]any{"knowledge_object_id": topic.KnowledgeObjectID, "knowledge_object_version_id": version, "knowledge_chunk_id": topic.KnowledgeChunkID, "source_hash": topicHash, "passage_locator": topic.StructuralPath, "source_category": topic.SourceCategory, "source_posture": topic.SourcePosture, "owner_node": topic.SourceNodeKey})
	pending.Sources = []provenance.SourceRegistration{{Kind: "notes_passage", Status: "resolved", Verification: "content_verified", ResolverName: "disposable_fixture_reader", ResolverVersion: "1", CanonicalLocator: &locator, VersionAddress: &version, ContentDigest: &topicHash, Submitted: details, Details: details}}
	registered, err := api.RegisterCandidates(ctx, "box-citation-semantic-review", provenance.CandidateRegistrationRequest{Candidates: []provenance.CandidateRegistration{pending}})
	if err != nil || len(registered.Receipts) != 1 {
		t.Fatalf("semantic fixture registration: %#v %v", registered, err)
	}
	before, err := api.Search(ctx, provenance.SearchRequest{Query: "decision"})
	if err != nil || before.Returned != 0 {
		t.Fatalf("registration is not acceptance: %#v %v", before, err)
	}
	reviewer := provenance.ProducerIdentity{ProducerID: "fixture-explicit-reviewer", ProducerKind: "working_agent", TaskID: "slice-4-reviewed-operation"}
	op := provenance.AcceptCandidateOperation{
		OperationMetadata: provenance.OperationMetadata{SchemaVersion: provenance.SchemaVersion, OperationID: provenance.SemanticID("66666666-6666-4666-8666-666666666641"), OccurredAt: time.Now().UTC(), Producer: reviewer, EvidenceSourceIDs: []provenance.SemanticID{sourceID}},
		CandidateID:       candidateID,
		Accepted:          provenance.AcceptedRecord{Record: provenance.Record{ID: provenance.SemanticID("66666666-6666-4666-8666-666666666642"), SchemaVersion: provenance.SchemaVersion, Claim: accepted.Claim, RecordKind: accepted.RecordKind, RecordContext: accepted.RecordContext, Domain: accepted.Domain, Visibility: accepted.Visibility, AssertionPosture: accepted.AssertionPosture, Temporal: accepted.Temporal, CreatedAt: time.Now().UTC()}, SourceReferenceIDs: []provenance.SemanticID{sourceID}, ProducerHistory: []provenance.ProducerIdentity{accepted.Producer, reviewer}},
	}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["operation_type"] = json.RawMessage(`"accept_candidate"`)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	batch := provenance.ManualOperationsRequest{Scope: provenance.LifecycleScope{Domain: accepted.Domain, Visibility: accepted.Visibility}, Producer: reviewer, Operations: []json.RawMessage{raw}}
	if _, err := api.ApplyManualOperations(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if replay, err := api.ApplyManualOperations(ctx, batch); err != nil || !replay.Replayed {
		t.Fatalf("review replay: %#v %v", replay, err)
	}
	for _, includePending := range []bool{false, true} {
		found, err := api.Search(ctx, provenance.SearchRequest{Query: "decision", IncludePending: includePending})
		wantPending := 0
		if includePending {
			wantPending = 1
		}
		if err != nil || len(found.AcceptedRecords.Items) != 1 || len(found.PendingCandidates.Items) != wantPending || found.AcceptedRecords.Items[0].RecordID != op.Accepted.Record.ID || found.Returned != 1+wantPending {
			t.Fatalf("typed frozen semantic result: %#v %v", found, err)
		}
		if includePending && found.PendingCandidates.Items[0].CandidateID != registered.Receipts[0].CandidateID {
			t.Fatal("wrong pending identity")
		}
	}
	record, err := api.GetRecord(ctx, op.Accepted.Record.ID, 50)
	if err != nil || len(record.SourceReferenceIDs) != 1 || record.SourceReferenceIDs[0] != sourceID || record.Record.Claim != accepted.Claim {
		t.Fatalf("accepted source link lost: %#v %v", record, err)
	}
	clue, err := api.GetCandidate(ctx, registered.Receipts[0].CandidateID, 50)
	if err != nil || clue.EffectiveState != "pending" {
		t.Fatal("conflicting proposal promoted")
	}
	t.Log("PASS: frozen accepted-cadence and pending-hourly remain distinct; only explicit review accepts; operation replay and cited source identity preserved")
}

func boxCitationFrozenText(t *testing.T, sourceKey, versionKey string) string {
	t.Helper()
	var corpus struct {
		Sources []struct {
			Key      string `json:"key"`
			Versions []struct {
				Key  string `json:"key"`
				Text string `json:"fixture_text"`
			} `json:"versions"`
		} `json:"sources"`
	}
	boxCitationReadCorpus(t, &corpus)
	for _, source := range corpus.Sources {
		for _, v := range source.Versions {
			if source.Key == sourceKey && v.Key == versionKey {
				return v.Text
			}
		}
	}
	t.Fatal("frozen source missing")
	return ""
}

func boxCitationFrozenSemanticText(t *testing.T, key string) string {
	t.Helper()
	var corpus struct {
		Records []struct {
			Key  string `json:"key"`
			Text string `json:"text"`
		} `json:"semantic_records"`
	}
	boxCitationReadCorpus(t, &corpus)
	for _, record := range corpus.Records {
		if record.Key == key {
			return record.Text
		}
	}
	t.Fatal("frozen semantic record missing")
	return ""
}

func boxCitationReadCorpus(t *testing.T, destination any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "knowledge", "testdata", "box_sources", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		t.Fatal(err)
	}
}

func boxCitationDatabaseURL(t *testing.T, root, key, database, role string) string {
	t.Helper()
	raw := os.Getenv(key)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgresql" || u.Host != "" || u.User == nil || u.User.Username() != role || u.Path != "/"+database || u.Query().Get("host") != filepath.Join(root, "socket") {
		t.Fatalf("%s must refer to the smoke-owned local socket/database/role", key)
	}
	return raw
}

func boxCitationFrozenVersions(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "knowledge", "testdata", "box_sources", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Sources []struct {
			Key      string `json:"key"`
			Versions []struct {
				Key  string `json:"key"`
				Text string `json:"fixture_text"`
			} `json:"versions"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, source := range corpus.Sources {
		if source.Key == "note" && len(source.Versions) == 2 && source.Versions[0].Key == "note-v1" && source.Versions[1].Key == "note-v2" {
			return []string{source.Versions[0].Text, source.Versions[1].Text}
		}
	}
	t.Fatal("frozen two-version note is absent")
	return nil
}
