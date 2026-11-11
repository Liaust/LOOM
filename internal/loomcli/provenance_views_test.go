package loomcli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func TestProvenanceRoutineRecordSingleSerializationAndDetailFidelity(t *testing.T) {
	const id provenance.SemanticID = "bc789beb-8f72-4d1c-b6ec-79c50764346a"
	claim := "Historical Apache rather than Rclone WebDAV choice. " + strings.Repeat("The choice is source-declared. ", 10)
	context := "Captured local implementation; not deployment verification. " + strings.Repeat("No current health claim. ", 10)
	now := time.Date(2026, 9, 14, 21, 56, 45, 0, time.UTC)
	payload := json.RawMessage(`{"claim":"` + claim + `","record_context":"` + context + `"}`)
	record := provenance.RecordLifecycleProjection{SchemaVersion: "1.0", Record: provenance.Record{ID: id, Claim: claim, RecordContext: context, RecordKind: "decision", Domain: "LOOM", Visibility: "private", AssertionPosture: "source_claim", Temporal: provenance.TemporalInterpretation{Interpretation: "Historical; not current runtime health", Uncertainty: "Current deployment not checked", ObservedAt: &now}, Payload: payload, Anchors: &provenance.StructuralAnchors{Projects: []string{"project_main"}}}, SourceReferenceIDs: []provenance.SemanticID{id}, Events: []provenance.LedgerEvent{{ObjectID: id, Payload: payload}}}
	envelope := response.Success("corr_single_record", record)
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/provenance/records/"+string(id) {
			t.Fatalf("unexpected request: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(envelope)
	})
	defer stop()
	args := []string{"--socket", socket, "provenance", "record", "get", string(id)}
	routine, _, err := executeRootCommand(append(args, "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(routine, claim) != 1 || strings.Count(routine, context) != 1 || strings.Contains(routine, `"payload"`) || strings.Contains(routine, `"representation_events"`) {
		t.Fatalf("duplicated routine representation: %s", routine)
	}
	var view response.Envelope[provenanceExactView]
	if err := json.Unmarshal([]byte(routine), &view); err != nil {
		t.Fatal(err)
	}
	gotMeta, _ := json.Marshal(view.Meta)
	wantMeta, _ := json.Marshal(envelope.Meta)
	if view.Data.SchemaVersion != provenanceCLIViewPrefix+"record.v1" || !view.OK || string(gotMeta) != string(wantMeta) || view.Data.Temporal.Interpretation != record.Record.Temporal.Interpretation || view.Data.Relationships.Completeness != "not_loaded" {
		t.Fatalf("lost qualification/envelope: %#v", view)
	}
	human, _, err := executeRootCommand(args...)
	if err != nil {
		t.Fatal(err)
	}
	var humanView provenanceExactView
	if err := json.NewDecoder(strings.NewReader(human)).Decode(&humanView); err != nil || !reflect.DeepEqual(view.Data, humanView) || !strings.Contains(human, "Correlation: corr_single_record") {
		t.Fatalf("human/JSON fields diverged: %v\n%s", err, human)
	}
	detail, _, err := executeRootCommand(append(args, "--details", "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	assertProvenanceViewJSONEqual(t, detail, envelope)
	plain, _, err := executeRootCommand(append(args, "--details", "--plain")...)
	if err != nil || plain != string(id)+"\n" || calls != 4 {
		t.Fatalf("ID-only or call count changed: %q %v %d", plain, err, calls)
	}
	if len(routine) >= len(detail) {
		t.Fatalf("routine view expanded duplicate fixture: routine=%d detail=%d", len(routine), len(detail))
	}
	t.Logf("duplicate record fixture: routine=%d code points, detail=%d code points; claim/context once in routine", utf8.RuneCountInString(routine), utf8.RuneCountInString(detail))
}

func TestProvenanceRoutineCandidateEffectiveStateAndIncompleteFields(t *testing.T) {
	id := provenance.SemanticID("22222222-2222-4222-8222-222222222222")
	winner := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	registration := provenance.CandidateRegistration{Temporal: provenance.TemporalInterpretation{Interpretation: "Only during the stated experiment", Uncertainty: "End time unknown"}, Anchors: &provenance.StructuralAnchors{Projects: []string{"project_main"}}, Ambiguity: []string{"Not permission to deploy"}, SourceActor: &provenance.ActorReference{ActorID: "source-author", DisplayName: "Source Author", SourceReferenceID: &id}, ArtifactAuthor: &provenance.ActorReference{ActorID: "artifact-author", DisplayName: "Artifact Author"}, ApprovingActor: &provenance.ActorReference{ActorID: "approver", DisplayName: "Source-declared approver", SourceReferenceID: &id}}
	submitted, _ := json.Marshal(registration)
	for _, state := range []string{"pending", "accepted", "consolidated", "rejected"} {
		data := provenance.CandidateLifecycleProjection{Candidate: provenance.Candidate{ID: id, State: "pending", Claim: "Candidate claim", AssertionPosture: "source_claim", Submitted: submitted}, EffectiveState: state, Truncated: true, WinningOperationID: &winner, LatestDeferral: json.RawMessage(`{"reason_code":"needs_source","reason":"Read original evidence","needs_capability":"source.read"}`)}
		view := newProvenanceCandidateView(data, nil)
		if view.EffectiveState != state || view.AssertionPosture != "source_claim" || view.Temporal.Interpretation != registration.Temporal.Interpretation || view.SourceReferences.Completeness != "unknown" || view.Lineage.Completeness != "unknown" || view.Resolution.Items[0].Completeness != "unknown" || view.Deferral.NeedsCapability != "source.read" || !reflect.DeepEqual(view.SourceActor, registration.SourceActor) || !reflect.DeepEqual(view.ArtifactAuthor, registration.ArtifactAuthor) || !reflect.DeepEqual(view.ApprovingActor, registration.ApprovingActor) {
			t.Fatalf("invented lifecycle/completeness: %#v", view)
		}
	}
	for _, submitted := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{"temporal_interpretation":42}`)} {
		view := newProvenanceCandidateView(provenance.CandidateLifecycleProjection{Candidate: provenance.Candidate{ID: id, Submitted: submitted}, EffectiveState: "pending"}, nil)
		if view.SubmissionFields != "unavailable" || view.Temporal != nil || view.Scope != nil || view.EffectiveState != "pending" || view.SourceActor != nil || view.ArtifactAuthor != nil || view.ApprovingActor != nil {
			t.Fatalf("invalid submission invented context: %#v", view)
		}
	}
}

func TestProvenanceRoutineCollectionCompleteness(t *testing.T) {
	for _, partial := range []bool{false, true} {
		page := provenanceViewPage([]string{"source"}, partial, 8)
		want := "complete"
		if partial {
			want = "unknown"
		}
		if page.Completeness != want || page.Truncated {
			t.Fatalf("upstream uncertainty lost: %#v", page)
		}
	}
	id := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	data := provenance.RecordLifecycleProjection{Record: provenance.Record{ID: id}}
	for i := 0; i < 9; i++ {
		data.SourceReferenceIDs = append(data.SourceReferenceIDs, id)
	}
	view := newProvenanceRecordView(data, nil)
	if !view.Truncated || !view.SourceReferences.Truncated || view.SourceReferences.Completeness != "partial" || len(view.SourceReferences.Items) != 8 || len(data.SourceReferenceIDs) != 9 {
		t.Fatalf("local omission not marked: %#v", view)
	}
}

func TestProvenanceRoutineCaseBoundedResolutionAndMembership(t *testing.T) {
	id := provenance.SemanticID("33333333-3333-4333-8333-333333333333")
	data := provenance.ResolutionCaseLifecycleProjection{Case: provenance.ResolutionCase{ID: id, Issue: "Which source is authoritative?", Payload: json.RawMessage(`{"history":"not routine"}`)}, EffectiveState: "open", Truncated: true, Members: []provenance.CaseMember{{RecordID: &id}}}
	for i := 0; i < 6; i++ {
		data.Events = append(data.Events, provenance.CaseEvent{EventType: "clarification_requested", Summary: strings.Repeat("界", 400), EvidenceSourceIDs: []provenance.SemanticID{id}})
	}
	view := newProvenanceCaseView(data)
	if view.CaseID != id || view.EffectiveState != "open" || view.Members.Items[0].RecordID == nil || !view.Resolution.Truncated || view.Resolution.Completeness != "partial" || len(view.Resolution.Items) != 4 || len(view.SourceReferences.Items) != 1 || !view.Truncated {
		t.Fatalf("case context lost: %#v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "not routine") || strings.Contains(string(encoded), `"payload"`) {
		t.Fatalf("case leaked history: %s", encoded)
	}
}

func TestProvenanceRoutineUnicodeAndStoredSourcePartialness(t *testing.T) {
	id := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	locator := strings.Repeat("界", 1000)
	sources := provenance.LinkedSources{ParentKind: "record", ParentID: id, Posture: "stored_resolution", SourcesTruncated: false, IncompleteItems: 1}
	for i := 0; i < 6; i++ {
		sources.Items = append(sources.Items, provenance.LinkedSourceItem{SourceReferenceID: id, StoredSourceReceipt: &provenance.StoredSourceReceipt{CanonicalLocator: &locator, Status: "unresolved", VerificationPosture: "unverified"}})
	}
	sources.Items[0] = provenance.LinkedSourceItem{SourceReferenceID: id, ReceiptUnavailable: "item_exceeds_bound"}
	data := provenance.RecordLifecycleProjection{Record: provenance.Record{ID: id, Claim: strings.Repeat("界", 1000), Temporal: provenance.TemporalInterpretation{Interpretation: strings.Repeat("Historical; not current. ", 100)}}}
	view := newProvenanceRecordView(data, &sources)
	if utf8.RuneCountInString(view.Claim) > 800 || !utf8.ValidString(view.Claim) || len(view.TruncatedFields) != 2 || !view.Truncated || !view.Sources.SourcesTruncated || view.Sources.IncompleteItems != 1 || view.Sources.Returned != 4 {
		t.Fatalf("lost bounds/partialness: %#v", view)
	}
	receipt := view.Sources.Items[1]
	if receipt.Status != "unresolved" || receipt.VerificationPosture != "unverified" || utf8.RuneCountInString(*receipt.CanonicalLocator) > 300 || len(receipt.TruncatedFields) != 1 || receipt.ExcerptAvailability != "not_available_in_stored_receipt" || *sources.Items[1].CanonicalLocator != locator {
		t.Fatalf("receipt altered/upgraded: %#v", receipt)
	}
	if !strings.Contains(view.DetailCommand, "--sources") {
		t.Fatal("detail route dropped sources")
	}
}

func TestProvenanceRoutineSearchTypedSnapshotAndDetailFlags(t *testing.T) {
	id := provenance.SemanticID("22222222-2222-4222-8222-222222222222")
	match := provenance.SearchCompactMatch{Summary: provenance.SearchText{Text: "WebDAV purpose", Truncated: true}, Freshness: provenance.SearchFreshness{Currentness: "partial historical", CurrentnessTruncated: true}, AssertionPosture: "source_declared", Source: provenance.SearchSourceSummary{Kind: "project_state_projection", Truncated: true}, ProjectIDs: []string{"project_main"}, MatchExplanation: provenance.SearchText{Text: "detail-only diagnostic"}, CompactTruncated: true}
	data := provenance.SearchResponse{SchemaVersion: provenance.SearchSchemaVersion, Query: "WebDAV", Returned: 2, Truncated: true, NextCursor: "opaque-cursor", ProjectState: provenance.SearchCollectionPage[provenance.ProjectStateSearchResult]{Items: []provenance.ProjectStateSearchResult{{ProjectID: "project_main", SnapshotID: id, Match: match, ExactGet: provenance.SearchExactGet{ID: "project_main", Resource: "project", Path: "/v1/provenance/projects/project_main?snapshot_id=" + string(id)}}}, Truncated: true}, PendingCandidates: provenance.SearchCollectionPage[provenance.PendingCandidateSearchResult]{Items: []provenance.PendingCandidateSearchResult{{CandidateID: id, Match: match}}}}
	envelope := response.Success("corr_view", data)
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(envelope) })
	defer stop()
	for _, details := range []bool{false, true} {
		args := []string{"--socket", socket, "provenance", "search", "WebDAV", "--json"}
		if details {
			args = append(args, "--details")
		}
		stdout, _, err := executeRootCommand(args...)
		if err != nil {
			t.Fatal(err)
		}
		if details {
			assertProvenanceViewJSONEqual(t, stdout, envelope)
			continue
		}
		var routine response.Envelope[provenanceSearchView]
		if err := json.Unmarshal([]byte(stdout), &routine); err != nil {
			t.Fatal(err)
		}
		hit := routine.Data.ProjectState.Items[0]
		if routine.Data.SchemaVersion != provenanceCLIViewPrefix+"search.v1" || hit.SnapshotID != id || hit.ExactGet != data.ProjectState.Items[0].ExactGet || !hit.Match.Freshness.CurrentnessTruncated || !hit.Match.Source.Truncated || routine.Data.PendingCandidates.Items[0].CandidateID != id || routine.Data.NextCursor != data.NextCursor || !routine.Data.ProjectState.Truncated || strings.Contains(stdout, "detail-only diagnostic") {
			t.Fatalf("routine search qualification lost: %s", stdout)
		}
	}
	plain, _, err := executeRootCommand("--socket", socket, "provenance", "search", "WebDAV", "--plain", "--details")
	if err != nil || plain != string(id)+"\nproject_main\n" {
		t.Fatalf("plain search is not ID-only: %q %v", plain, err)
	}
}

func assertProvenanceViewJSONEqual(t *testing.T, actual string, expected any) {
	t.Helper()
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal([]byte(actual), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full JSON representation changed:\n%s\nwant: %s", actual, encoded)
	}
}

func TestProvenanceDetailCandidateCaseAndSourcesFidelity(t *testing.T) {
	id := provenance.SemanticID("22222222-2222-4222-8222-222222222222")
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"retained":"full history","claim":"do not discard"}`)
	candidate := provenance.CandidateLifecycleProjection{SchemaVersion: "1.0", Candidate: provenance.Candidate{ID: id, Claim: "qualified candidate", Submitted: payload, Payload: payload}, EffectiveState: "accepted", WinningOperationID: &id, Events: []provenance.LedgerEvent{{ObjectID: id, OccurredAt: now, Payload: payload}}}
	sources := provenance.LinkedSources{SchemaVersion: provenance.LinkedSourcesSchemaVersion, ParentKind: "candidate", ParentID: id, Posture: "stored_resolution", SourcesTruncated: true, IncompleteItems: 1, Returned: 1, Items: []provenance.LinkedSourceItem{{SourceReferenceID: id, ReceiptUnavailable: "item_exceeds_bound"}}}
	recordSources := sources
	recordSources.ParentKind = "record"
	for _, tc := range []struct {
		kind    string
		data    any
		sources bool
	}{
		{"candidate", candidate, false},
		{"candidate", provenance.CandidateWithSources{CandidateLifecycleProjection: candidate, Sources: sources}, true},
		{"record", provenance.RecordWithSources{RecordLifecycleProjection: provenance.RecordLifecycleProjection{SchemaVersion: "1.0", Record: provenance.Record{ID: id, Claim: "record claim", Payload: payload}, Truncated: true}, Sources: recordSources}, true},
		{"case", provenance.ResolutionCaseLifecycleProjection{SchemaVersion: "1.0", Case: provenance.ResolutionCase{ID: id, Issue: "resolution issue", Payload: payload}, EffectiveState: "closed", Events: []provenance.CaseEvent{{OccurredAt: now, Summary: "Resolved", Payload: payload}}}, false},
	} {
		name := tc.kind
		if tc.sources {
			name += "-sources"
		}
		t.Run(name, func(t *testing.T) {
			envelope := response.Success("corr_fidelity", tc.data)
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				if (r.URL.Query().Get("sources") == "true") != tc.sources {
					t.Fatalf("sources request changed: %s", r.URL)
				}
				_ = json.NewEncoder(w).Encode(envelope)
			})
			defer stop()
			args := []string{"--socket", socket, "provenance", tc.kind, "get", string(id), "--details", "--json"}
			if tc.sources {
				args = append(args, "--sources")
			}
			stdout, _, err := executeRootCommand(args...)
			if err != nil {
				t.Fatal(err)
			}
			assertProvenanceViewJSONEqual(t, stdout, envelope)
		})
	}
}
