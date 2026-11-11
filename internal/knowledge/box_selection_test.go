package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
)

func boxSearchInput() NotesSearchInput {
	return NotesSearchInput{Query: "matching title", Limit: 10, SourceLifecycle: SourceLifecycleFilterActive,
		boxSelection: &boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}}}
}

func assertBoxQuery(t *testing.T, query notesSearchQuery) {
	t.Helper()
	if !strings.Contains(query.SQL, "home_box_id") || !strings.Contains(query.SQL, "metadata->>'box_id'") {
		t.Fatal("missing project/direct Box intersection")
	}
	found := false
	for _, arg := range query.Args {
		if arg == `["box_company"]` {
			found = true
		}
	}
	if !found {
		t.Fatal("missing bound Box parameter")
	}
	max := 0
	for _, match := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(query.SQL, -1) {
		n, _ := strconv.Atoi(match[1])
		if n > max {
			max = n
		}
	}
	if max != len(query.Args) {
		t.Fatalf("placeholder/argument mismatch: %d %d", max, len(query.Args))
	}
}

func TestBoxSelectionScopesEverySearchLaneBeforeLimits(t *testing.T) {
	input := boxSearchInput()
	variants := notesLexicalVariants(input)
	builders := map[string]func() (notesSearchQuery, error){
		"lexical": func() (notesSearchQuery, error) { return buildNotesSearchQuery(input) },
		"indexed": func() (notesSearchQuery, error) { return buildIndexedNotesLexicalCandidates(input, variants) },
		"bm25":    func() (notesSearchQuery, error) { return buildBM25NotesLexicalCandidates(input, variants) },
		"semantic": func() (notesSearchQuery, error) {
			return buildSemanticNotesSearchQuery(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3})
		},
		"indexed_semantic": func() (notesSearchQuery, error) {
			return buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, variants)
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			query, err := build()
			if err != nil {
				t.Fatal(err)
			}
			assertBoxQuery(t, query)
			if name == "indexed_semantic" {
				annEnd := strings.Index(query.SQL, "), archived_objects AS MATERIALIZED")
				if annEnd < 0 || !strings.Contains(query.SQL[:annEnd], "ann_root") || !strings.Contains(query.SQL[:annEnd], "home_box_id") || !strings.Contains(query.SQL, "FROM ann ce") {
					t.Fatal("Company membership must be inside the ANN pool, before its distance limit")
				}
			}
			if name == "bm25" && strings.Index(query.SQL, "home_box_id") > strings.Index(query.SQL, "ORDER BY pdb.score(d.id)") {
				t.Fatal("BM25 Box filter followed Top-K")
			}
			if name == "indexed" && !strings.Contains(query.SQL, "scoped_documents AS NOT MATERIALIZED") {
				t.Fatal("literal/fallback candidate pool was global")
			}
		})
	}
	if notesIndexedExactScope(input) || !notesIndexedHasScope(input) {
		t.Fatal("Box binding must restrict discovery without becoming an exact/count scope")
	}
}

func TestBoxSelectionMetadataAndExactReadDoNotBroaden(t *testing.T) {
	ctx := boxregistry.WithSelection(t.Context(), *boxSearchInput().boxSelection)
	for _, ref := range []string{"", "same/file.md", "object_foreign"} {
		query := metadataFileCandidatesQuery(ctx, NotesSearchInput{Query: "matching title"}, ref, 2)
		assertBoxQuery(t, query)
		if strings.Index(query.SQL, "home_box_id") > strings.Index(query.SQL, "ORDER BY f.object_id") {
			t.Fatal("metadata scope followed limit")
		}
	}
	args := []any{"same/file.md"}
	root := notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args)
	query := notesSearchQuery{SQL: fileReadTargetSQL(root), Args: args}
	assertBoxQuery(t, query)
	if strings.Index(query.SQL, "home_box_id") > strings.Index(query.SQL, "SELECT COALESCE(ko.canonical_file_id") {
		t.Fatal("exact source reference resolved globally")
	}
	for _, selection := range []*boxregistry.ResolvedSelection{nil, {AllBoxes: true}, {}} {
		args := []any{}
		predicate := notesSelectedRootSQL(selection, "r", &args)
		want := "true"
		if selection != nil && !selection.AllBoxes {
			want = "false"
		}
		if predicate != want || len(args) != 0 {
			t.Fatalf("unexpected global/empty selection: %s %v", predicate, args)
		}
	}
	if notesBoxSelection(context.Background()) != nil {
		t.Fatal("internal callers lost global scope")
	}
}

func TestBoxSelectionProjectAndApplicationSourcesInheritHome(t *testing.T) {
	service := NewService(nil)
	registration := ProjectWatchedRootRegistration{HomeBoxID: "box_company", ProjectID: ids.NewProjectID(), NodeID: ids.NewNodeID(), OwnerNodeKey: "main", ProjectRoot: "/box/company/Projects/app", LocalRootKey: "notes", BackendRootKey: "app__notes", RootRelativePath: "notes", SourceKinds: json.RawMessage(`["notes_contract"]`), ActivationStatus: "reported", Metadata: json.RawMessage(`{"box_id":"old_claim","declaration_adapter":{"application_data":{"path":"/var/lib/application/data","subpath":"documents"}}}`)}
	candidate, ok, reason := service.projectSourceRootCandidate(registration)
	if !ok || reason != "" || candidate.Root.SourcePath != "/var/lib/application/data/documents" || stringMapValue(jsonObject(candidate.Root.Metadata), "box_id") != "box_company" {
		t.Fatalf("allocation membership: %+v %s", candidate, reason)
	}
	registration.HomeBoxID = ""
	registration.Metadata = json.RawMessage(`{}`)
	candidate, ok, _ = service.projectSourceRootCandidate(registration)
	if !ok || stringMapValue(jsonObject(candidate.Root.Metadata), "box_id") != "" {
		t.Fatal("unknown source ownership became Personal")
	}
	metadata := sourceRootMetadata(json.RawMessage(`{"box_id":"box_company"}`), map[string]any{"box_id": ""})
	if stringMapValue(jsonObject(metadata), "box_id") != "box_company" {
		t.Fatal("existing Box metadata lost")
	}
}

func TestBoxSelectionEnrichmentMembershipChangeInvalidatesPreview(t *testing.T) {
	object := enrichmentTestObject(t, "pdf")
	object.BoxMembershipDigest = boxregistry.Digest("box_company", "root", "revision_1")
	host := PipelinePolicy{PDFOCREnabled: true}
	intent := enrichmentTestIntent(t, object, host, EnrichmentStages{OCR: true})
	changed := object
	changed.BoxMembershipDigest = boxregistry.Digest("box_personal", "root", "revision_2")
	if pipelineAdmissionMatches(object, changed) {
		t.Fatal("execution ignored reassociation at unchanged allocation path")
	}
	if _, err := compileEnrichmentPlan(changed, host, intent); !errors.Is(err, ErrConflict) {
		t.Fatalf("preview not invalidated: %v", err)
	}
	legacy := object
	legacy.BoxMembershipDigest = ""
	if !pipelineAdmissionMatches(legacy, object) {
		t.Fatal("first ownership label invalidated existing unbound work")
	}
	oldIntent := enrichmentTestIntent(t, legacy, host, EnrichmentStages{OCR: true})
	if _, err := compileEnrichmentPlan(object, host, oldIntent); err != nil {
		t.Fatalf("legacy intent disrupted by metadata-only repair: %v", err)
	}
}

func TestBoxSelectionSourceRepairRejectsUnboundedOrStaleApply(t *testing.T) {
	service := NewService(nil)
	for _, input := range []SourceBoxMembershipInput{{}, {SourceRootIDs: make([]string, 101)}, {SourceRootIDs: []string{"root"}, Apply: true, CatalogDigest: "stale"}} {
		if _, err := service.ReconcileSourceBoxMembership(t.Context(), boxregistry.Catalog{}, input); err == nil {
			t.Fatal(fmt.Sprintf("invalid batch reached DB: %+v", input))
		}
	}
}

func TestBoxSelectionEnrichmentCursorIsBoundToSelection(t *testing.T) {
	input := EnrichmentSelection{Ref: "/shared/allocation", Kind: "folder", Recursive: true}
	ctx := boxregistry.WithSelection(t.Context(), *boxSearchInput().boxSelection)
	binding := EnrichmentBinding{ObjectID: "object", MembershipDigest: "membership"}
	input.After = encodeEnrichmentCursor(ctx, input, binding)
	decoded, err := decodeEnrichmentCursor(ctx, input)
	if err != nil || decoded.ObjectID != binding.ObjectID || decoded.Membership != binding.MembershipDigest {
		t.Fatalf("cursor: %+v %v", decoded, err)
	}
	other := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_personal"}})
	if _, err := decodeEnrichmentCursor(other, input); !errors.Is(err, ErrConflict) {
		t.Fatal("cross-Box cursor accepted")
	}
	input.Ref = "/other/allocation"
	if _, err := decodeEnrichmentCursor(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatal("cursor broadened folder selection")
	}
}
