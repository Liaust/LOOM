package provenance

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// These fixtures are deliberately embedded only by tests. Slice 1 freezes an
// independent acceptance contract; it does not add a production search index,
// ranking implementation, projection, transport, command, or worker.
//
//go:embed testdata/search/*.json
var searchContractFS embed.FS

const searchFixtureSchema = "loom.provenance_search_fixture.v1"

type searchFixtureCorpus struct {
	SchemaVersion string                    `json:"schema_version"`
	FrozenAt      string                    `json:"frozen_at"`
	Projects      []searchFixtureProject    `json:"projects"`
	Repositories  []searchFixtureRepository `json:"repositories"`
	Items         []searchFixtureItem       `json:"items"`
	Relationships []searchFixtureRelation   `json:"relationships"`
}

type searchFixtureProject struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Lifecycle     string `json:"lifecycle"`
	NavigationRef string `json:"navigation_ref"`
}

type searchFixtureRepository struct {
	RepositoryID          string                         `json:"repository_id"`
	Name                  string                         `json:"name"`
	Aliases               []string                       `json:"aliases"`
	OwningProject         searchFixtureOwningProject     `json:"owning_project"`
	Role                  string                         `json:"role"`
	Purpose               string                         `json:"purpose"`
	Topics                []string                       `json:"topics"`
	CurrentState          string                         `json:"current_state"`
	ActiveFocus           string                         `json:"active_focus"`
	RecentOutcomes        []string                       `json:"recent_outcomes"`
	NextPriorities        []string                       `json:"next_priorities"`
	Blockers              []string                       `json:"blockers"`
	AcceptedContext       []searchFixtureAcceptedContext `json:"accepted_context"`
	Freshness             searchFixtureFreshness         `json:"freshness"`
	PortableNavigationRef string                         `json:"portable_navigation_ref"`
	ResolvedPath          *string                        `json:"resolved_path,omitempty"`
	TrackingStatus        string                         `json:"tracking_status"`
	Diagnostics           []string                       `json:"diagnostics"`
	FieldSources          map[string]searchFixtureSource `json:"field_sources"`
	ScenarioTags          []string                       `json:"scenario_tags"`
}

type searchFixtureOwningProject struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Lifecycle     string `json:"lifecycle"`
	NavigationRef string `json:"navigation_ref"`
}

type searchFixtureAcceptedContext struct {
	RecordID      string `json:"record_id"`
	Summary       string `json:"summary"`
	Qualification string `json:"qualification"`
}

type searchFixtureFreshness struct {
	Posture         string  `json:"posture"`
	ObservedAt      *string `json:"observed_at"`
	SourceVersion   *string `json:"source_version"`
	SourceDigest    *string `json:"source_digest"`
	ObservedCommit  *string `json:"observed_commit"`
	SourceBranch    *string `json:"source_branch"`
	FrontierPosture string  `json:"frontier_posture"`
}

type searchFixtureSource struct {
	Source  string `json:"source"`
	Posture string `json:"posture"`
}

type searchFixtureItem struct {
	ID               string                `json:"id"`
	Collection       string                `json:"collection"`
	SourceEngine     string                `json:"source_engine"`
	Lifecycle        string                `json:"lifecycle"`
	ProjectID        *string               `json:"project_id"`
	RepositoryID     *string               `json:"repository_id"`
	Title            string                `json:"title"`
	Text             string                `json:"text"`
	AssertionPosture string                `json:"assertion_posture"`
	TechnicalOnly    bool                  `json:"technical_only"`
	Temporal         searchFixtureTemporal `json:"temporal"`
	ScenarioTags     []string              `json:"scenario_tags"`
}

type searchFixtureTemporal struct {
	ObservedAt  string  `json:"observed_at"`
	ValidFrom   *string `json:"valid_from"`
	ValidUntil  *string `json:"valid_until"`
	Currentness string  `json:"currentness"`
}

type searchFixtureRelation struct {
	RelationshipType string  `json:"relationship_type"`
	FromID           string  `json:"from_id"`
	ToID             string  `json:"to_id"`
	CaseID           *string `json:"case_id,omitempty"`
	Posture          string  `json:"posture"`
}

type searchQueryContract struct {
	SchemaVersion string                   `json:"schema_version"`
	Queries       []searchQueryExpectation `json:"queries"`
}

type searchQueryExpectation struct {
	QueryID             string                `json:"query_id"`
	Query               string                `json:"query"`
	ExpectedFirstEngine string                `json:"expected_first_engine"`
	Request             searchFixtureRequest  `json:"request"`
	Expected            searchFixtureExpected `json:"expected"`
}

type searchFixtureRequest struct {
	Surface        string            `json:"surface"`
	IncludePending bool              `json:"include_pending"`
	Filters        map[string]string `json:"filters"`
}

type searchFixtureExpected struct {
	Collections             []searchFixtureCollection    `json:"collections"`
	ExactGet                searchFixtureExactGet        `json:"exact_get"`
	Currentness             searchFixtureCurrentness     `json:"currentness"`
	ProhibitedContamination []searchFixtureContamination `json:"prohibited_contamination"`
	RepoMatchSignals        []string                     `json:"repo_match_signals"`
}

type searchFixtureCollection struct {
	Collection string   `json:"collection"`
	IDs        []string `json:"ids"`
}

type searchFixtureExactGet struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Command    string `json:"command"`
}

type searchFixtureCurrentness struct {
	Posture     string `json:"posture"`
	Explanation string `json:"explanation"`
}

type searchFixtureContamination struct {
	ID         string `json:"id"`
	Collection string `json:"collection,omitempty"`
	Reason     string `json:"reason"`
}

type repositoryCardContract struct {
	SchemaVersion                  string              `json:"schema_version"`
	SemanticSurface                string              `json:"semantic_surface"`
	ExactGetSurface                string              `json:"exact_get_surface"`
	TechnicalRegistrySurface       string              `json:"technical_registry_surface"`
	RequiredTopLevelFields         []string            `json:"required_top_level_fields"`
	OptionalTopLevelFields         []string            `json:"optional_top_level_fields"`
	RequiredNestedFields           map[string][]string `json:"required_nested_fields"`
	FieldPostureGroups             map[string][]string `json:"field_posture_groups"`
	TrackingStatuses               []string            `json:"tracking_statuses"`
	RegisteredRepositoryVisibility string              `json:"registered_repository_visibility"`
	ForbiddenTechnicalFields       []string            `json:"forbidden_technical_fields"`
	FrontierRule                   string              `json:"frontier_rule"`
}

type searchLimitsContract struct {
	SchemaVersion      string                   `json:"schema_version"`
	ResultLimits       searchResultLimits       `json:"result_limits"`
	ContextLimits      searchContextLimits      `json:"context_limits"`
	LatencyMeasurement searchLatencyMeasurement `json:"latency_measurement"`
}

type searchResultLimits struct {
	DefaultTotal           int  `json:"default_total"`
	MaximumRequestedTotal  int  `json:"maximum_requested_total"`
	MaximumPerCollection   int  `json:"maximum_per_collection"`
	MaximumRepositoryCards int  `json:"maximum_repository_cards"`
	StableOrderingRequired bool `json:"stable_ordering_required"`
}

type searchContextLimits struct {
	Unit                                  string `json:"unit"`
	MaximumMatchExplanation               int    `json:"maximum_match_explanation"`
	MaximumCompactResult                  int    `json:"maximum_compact_result"`
	MaximumCompactResponse                int    `json:"maximum_compact_response"`
	MaximumRepositoryAcceptedContextItems int    `json:"maximum_repository_accepted_context_items"`
	ExactGetMaximumSources                int    `json:"exact_get_maximum_sources"`
	ExactGetMaximumRelationships          int    `json:"exact_get_maximum_relationships"`
	ExactGetMaximumLifecycleEvents        int    `json:"exact_get_maximum_lifecycle_events"`
	ExactGetMaximumQualifications         int    `json:"exact_get_maximum_qualifications"`
	TruncationMarkerRequired              bool   `json:"truncation_marker_required"`
}

type searchLatencyMeasurement struct {
	Environment                 string   `json:"environment"`
	Clock                       string   `json:"clock"`
	FixtureScope                string   `json:"fixture_scope"`
	WarmupIterationsPerQuery    int      `json:"warmup_iterations_per_query"`
	MeasuredIterationsPerQuery  int      `json:"measured_iterations_per_query"`
	IndependentProcessRuns      int      `json:"independent_process_runs"`
	MeasureFrom                 string   `json:"measure_from"`
	MeasureUntil                string   `json:"measure_until"`
	IncludedSteps               []string `json:"included_steps"`
	ExcludedSteps               []string `json:"excluded_steps"`
	ReportedStatistics          []string `json:"reported_statistics"`
	AggregationRule             string   `json:"aggregation_rule"`
	LocalP95BudgetMilliseconds  int      `json:"local_p95_budget_milliseconds"`
	PerQueryTimeoutMilliseconds int      `json:"per_query_timeout_milliseconds"`
	MutationPolicy              string   `json:"mutation_policy"`
}

func loadSearchContract[T any](t *testing.T, name string) T {
	t.Helper()
	payload := loadSearchContractPayload(t, name)
	value, err := decodeSearchContractPayload[T](payload)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return value
}

func loadSearchContractPayload(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := searchContractFS.ReadFile("testdata/search/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return payload
}

func decodeSearchContractPayload[T any](payload []byte) (T, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return value, fmt.Errorf("trailing content: %w", err)
	}
	return value, nil
}

func TestIndependentRetrievalCorpusContract(t *testing.T) {
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	if corpus.SchemaVersion != searchFixtureSchema {
		t.Fatalf("unexpected corpus schema %q", corpus.SchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, corpus.FrozenAt); err != nil {
		t.Fatalf("frozen_at is not RFC3339: %v", err)
	}
	if len(corpus.Projects) < 3 || len(corpus.Repositories) < 3 || len(corpus.Items) < 12 {
		t.Fatalf("corpus is too small to be independent: %d projects, %d repositories, %d items", len(corpus.Projects), len(corpus.Repositories), len(corpus.Items))
	}

	ids, collections := fixtureIdentityIndex(t, corpus)
	requiredTags := []string{
		"old_note", "superseded_decision", "compatible_temporal_update",
		"unresolved_contradiction", "pending_candidate", "stale_project_projection",
		"newer_repo_state", "failed_object_extraction", "cross_project_ambiguity",
	}
	tagCounts := map[string]int{}
	for _, repository := range corpus.Repositories {
		for _, tag := range repository.ScenarioTags {
			tagCounts[tag]++
		}
	}
	for _, item := range corpus.Items {
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Text) == "" || strings.TrimSpace(item.AssertionPosture) == "" {
			t.Fatalf("fixture item is not self-contained: %#v", item)
		}
		if _, err := time.Parse(time.RFC3339, item.Temporal.ObservedAt); err != nil {
			t.Fatalf("item %s observed_at is invalid: %v", item.ID, err)
		}
		for _, value := range []*string{item.Temporal.ValidFrom, item.Temporal.ValidUntil} {
			if value != nil {
				if _, err := time.Parse(time.RFC3339, *value); err != nil {
					t.Fatalf("item %s temporal bound is invalid: %v", item.ID, err)
				}
			}
		}
		for _, tag := range item.ScenarioTags {
			tagCounts[tag]++
		}
		if isSemanticCollection(item.Collection) {
			if _, err := ParseSemanticID(item.ID); err != nil {
				t.Fatalf("item %s in %s lacks a stable semantic ID: %v", item.ID, item.Collection, err)
			}
		}
		if expectedEngine := engineForFixtureCollection(item.Collection); item.SourceEngine != expectedEngine {
			t.Fatalf("item %s in %s is assigned to %s instead of %s", item.ID, item.Collection, item.SourceEngine, expectedEngine)
		}
	}
	for _, tag := range requiredTags {
		if tagCounts[tag] == 0 {
			t.Errorf("required scenario %q is missing", tag)
		}
	}

	relationTypes := map[string]bool{}
	for _, relation := range corpus.Relationships {
		relationTypes[relation.RelationshipType] = true
		if !ids[relation.FromID] || !ids[relation.ToID] || strings.TrimSpace(relation.Posture) == "" {
			t.Errorf("relationship has missing target or posture: %#v", relation)
		}
		if relation.CaseID != nil && (!ids[*relation.CaseID] || collections[*relation.CaseID] != "unresolved_cases") {
			t.Errorf("relationship case target is not an unresolved case: %#v", relation)
		}
	}
	for _, relationshipType := range []string{"supersedes", "refines", "contested_by_case"} {
		if !relationTypes[relationshipType] {
			t.Errorf("missing lifecycle relationship %q", relationshipType)
		}
	}
	assertTaggedItemContract(t, corpus.Items, "old_note", "notes", "source_material", "historical_source", false)
	assertTaggedItemContract(t, corpus.Items, "pending_candidate", "pending_candidates", "pending", "unaccepted_candidate", false)
	assertTaggedItemContract(t, corpus.Items, "unresolved_contradiction", "unresolved_cases", "open", "unresolved", false)
	assertTaggedItemContract(t, corpus.Items, "failed_object_extraction", "objects", "extraction_failed", "technical_failure", true)

	stale := itemWithTag(t, corpus.Items, "stale_project_projection")
	current := itemWithTag(t, corpus.Items, "newer_repo_state")
	staleAt, _ := time.Parse(time.RFC3339, stale.Temporal.ObservedAt)
	currentAt, _ := time.Parse(time.RFC3339, current.Temporal.ObservedAt)
	if stale.RepositoryID == nil || current.RepositoryID == nil || *stale.RepositoryID != *current.RepositoryID || !currentAt.After(staleAt) {
		t.Fatalf("stale project/newer repository fixture is not temporally comparable: stale=%#v current=%#v", stale, current)
	}
}

func TestTypedRetrievalExpectationsContract(t *testing.T) {
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	queries := loadSearchContract[searchQueryContract](t, "queries.json")
	limits := loadSearchContract[searchLimitsContract](t, "limits.json")
	if queries.SchemaVersion != "loom.provenance_search_queries.v1" {
		t.Fatalf("unexpected query schema %q", queries.SchemaVersion)
	}
	ids, collections := fixtureIdentityIndex(t, corpus)
	allowedEngines := map[string]bool{"provenance": true, "notes": true, "objects": true, "project_registry": true}
	seenQueries := map[string]bool{}
	seenEngines := map[string]bool{}
	repoSignals := map[string]bool{}
	var pendingDefault, pendingExplicit *searchQueryExpectation

	for index := range queries.Queries {
		query := &queries.Queries[index]
		if query.QueryID == "" || query.Query == "" || seenQueries[query.QueryID] {
			t.Fatalf("query identity is empty or duplicated: %#v", query)
		}
		seenQueries[query.QueryID] = true
		if !allowedEngines[query.ExpectedFirstEngine] {
			t.Errorf("query %s has unknown first engine %q", query.QueryID, query.ExpectedFirstEngine)
		}
		seenEngines[query.ExpectedFirstEngine] = true
		if query.Request.Surface == "" || len(query.Expected.Collections) == 0 {
			t.Errorf("query %s lacks a surface or typed result collection", query.QueryID)
		}
		for filter, value := range query.Request.Filters {
			switch filter {
			case "project":
				if !ids[value] || collections[value] != "technical_project_registry" {
					t.Errorf("query %s has unknown project filter %s", query.QueryID, value)
				}
			case "repo":
				if !ids[value] || collections[value] != "repo_state" {
					t.Errorf("query %s has unknown repository filter %s", query.QueryID, value)
				}
			}
		}
		if query.Expected.ExactGet.ID == "" || query.Expected.ExactGet.Collection == "" || query.Expected.ExactGet.Command == "" {
			t.Errorf("query %s lacks an exact-get target", query.QueryID)
		} else if !ids[query.Expected.ExactGet.ID] || collections[query.Expected.ExactGet.ID] != query.Expected.ExactGet.Collection {
			t.Errorf("query %s exact-get target is absent or mistyped: %#v", query.QueryID, query.Expected.ExactGet)
		}
		if !strings.HasPrefix(query.Expected.ExactGet.Command, exactGetPrefix(query.Expected.ExactGet.Collection)) {
			t.Errorf("query %s exact-get command does not match collection: %q", query.QueryID, query.Expected.ExactGet.Command)
		}
		if query.Expected.Currentness.Posture == "" || query.Expected.Currentness.Explanation == "" {
			t.Errorf("query %s lacks currentness posture or explanation", query.QueryID)
		}
		if utf8.RuneCountInString(query.Expected.Currentness.Explanation) > limits.ContextLimits.MaximumMatchExplanation {
			t.Errorf("query %s currentness explanation exceeds compact explanation limit", query.QueryID)
		}
		if len(query.Expected.ProhibitedContamination) == 0 {
			t.Errorf("query %s has no prohibited-contamination expectation", query.QueryID)
		}

		totalResults := 0
		expectedByCollection := map[string]map[string]bool{}
		for _, collection := range query.Expected.Collections {
			if collection.Collection == "pending_candidates" && !query.Request.IncludePending {
				t.Errorf("query %s leaks pending candidates without explicit inclusion", query.QueryID)
			}
			if len(collection.IDs) == 0 || len(collection.IDs) > limits.ResultLimits.MaximumPerCollection {
				t.Errorf("query %s has invalid %s result count %d", query.QueryID, collection.Collection, len(collection.IDs))
			}
			expectedByCollection[collection.Collection] = map[string]bool{}
			for _, id := range collection.IDs {
				totalResults++
				if !ids[id] || collections[id] != collection.Collection {
					t.Errorf("query %s expects absent or mistyped %s result %s", query.QueryID, collection.Collection, id)
				}
				expectedByCollection[collection.Collection][id] = true
			}
		}
		if totalResults > limits.ResultLimits.DefaultTotal {
			t.Errorf("query %s fixture result count %d exceeds the default total %d", query.QueryID, totalResults, limits.ResultLimits.DefaultTotal)
		}
		for _, prohibited := range query.Expected.ProhibitedContamination {
			if !ids[prohibited.ID] || prohibited.Reason == "" {
				t.Errorf("query %s has an invalid contamination control: %#v", query.QueryID, prohibited)
			}
			if prohibited.Collection == "" {
				for collection, expectedIDs := range expectedByCollection {
					if expectedIDs[prohibited.ID] {
						t.Errorf("query %s both expects and unconditionally prohibits %s in %s", query.QueryID, prohibited.ID, collection)
					}
				}
			} else if expectedByCollection[prohibited.Collection][prohibited.ID] {
				t.Errorf("query %s expects prohibited %s in %s", query.QueryID, prohibited.ID, prohibited.Collection)
			}
		}
		for _, signal := range query.Expected.RepoMatchSignals {
			repoSignals[signal] = true
		}
		if query.QueryID == "pending-default-exclusion" {
			pendingDefault = query
		}
		if query.QueryID == "pending-explicit-inclusion" {
			pendingExplicit = query
		}
	}

	for _, engine := range []string{"provenance", "notes", "objects", "project_registry"} {
		if !seenEngines[engine] {
			t.Errorf("no routing fixture selects %s first", engine)
		}
	}
	for _, signal := range []string{"alias", "purpose", "topics", "current_focus", "owning_project_context", "tracking_status"} {
		if !repoSignals[signal] {
			t.Errorf("fuzzy repository fixtures never require %s", signal)
		}
	}
	if pendingDefault == nil || pendingExplicit == nil || pendingDefault.Query != pendingExplicit.Query {
		t.Fatal("pending default/explicit controls are missing or not comparable")
	}
	if pendingDefault.Request.IncludePending || !pendingExplicit.Request.IncludePending {
		t.Fatal("pending default/explicit controls do not freeze opt-in behavior")
	}
	if !collectionContains(pendingExplicit.Expected.Collections, "pending_candidates", "22222222-2222-4222-8222-222222222001") ||
		collectionContains(pendingExplicit.Expected.Collections, "accepted_records", "22222222-2222-4222-8222-222222222001") {
		t.Fatal("explicit pending result is not structurally separate from accepted records")
	}
}

func TestSemanticRepositoryAwarenessCardContract(t *testing.T) {
	corpusPayload := loadSearchContractPayload(t, "corpus.json")
	contractPayload := loadSearchContractPayload(t, "repository_card.json")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	contract := loadSearchContract[repositoryCardContract](t, "repository_card.json")
	limits := loadSearchContract[searchLimitsContract](t, "limits.json")
	if err := validateRepositoryCardNestedAndJoinContract(corpusPayload, contractPayload); err != nil {
		t.Fatalf("nested repository-card and semantic-join contract: %v", err)
	}
	if contract.SchemaVersion != "loom.provenance_repository_card_contract.v1" {
		t.Fatalf("unexpected repository-card schema %q", contract.SchemaVersion)
	}
	if contract.SemanticSurface != "loom provenance repo list" || contract.ExactGetSurface != "loom provenance repo get" || contract.TechnicalRegistrySurface != "loom project" {
		t.Fatalf("semantic/technical surface boundary changed: %#v", contract)
	}
	if contract.RegisteredRepositoryVisibility != "visible_with_typed_status_and_diagnostics" || contract.FrontierRule == "" {
		t.Fatal("repository visibility or frontier posture is not frozen")
	}
	for _, required := range []string{"repository_id", "owning_project", "accepted_context", "freshness", "portable_navigation_ref", "tracking_status", "diagnostics", "field_sources"} {
		if !containsString(contract.RequiredTopLevelFields, required) {
			t.Errorf("repository card no longer requires %s", required)
		}
	}
	if !containsString(contract.OptionalTopLevelFields, "resolved_path") {
		t.Error("authorized current-node resolved_path is not preserved as optional")
	}

	raw := loadRawSearchObject(t, "corpus.json")
	var rawRepositories []map[string]json.RawMessage
	if err := json.Unmarshal(raw["repositories"], &rawRepositories); err != nil {
		t.Fatalf("decode raw repositories: %v", err)
	}
	if len(rawRepositories) != len(corpus.Repositories) {
		t.Fatal("typed and raw repository fixture counts differ")
	}
	allowedTracking := stringSet(contract.TrackingStatuses)
	for index, repository := range corpus.Repositories {
		if !validTypedID(repository.RepositoryID, "repo_") || !validTypedID(repository.OwningProject.ProjectID, "project_") {
			t.Errorf("repository card has invalid stable identity: %#v", repository)
		}
		if repository.Name == "" || len(repository.Aliases) == 0 || repository.Purpose == "" || len(repository.Topics) == 0 || repository.ActiveFocus == "" {
			t.Errorf("repository card lacks semantic finder fields: %s", repository.RepositoryID)
		}
		if !allowedTracking[repository.TrackingStatus] {
			t.Errorf("repository %s has unknown tracking status %q", repository.RepositoryID, repository.TrackingStatus)
		}
		if repository.TrackingStatus != "valid" && len(repository.Diagnostics) == 0 {
			t.Errorf("repository %s disappears behind an unqualified tracking status", repository.RepositoryID)
		}
		if len(repository.AcceptedContext) > limits.ContextLimits.MaximumRepositoryAcceptedContextItems {
			t.Errorf("repository %s exceeds accepted-context bound", repository.RepositoryID)
		}
		if repository.Freshness.ObservedAt != nil {
			if _, err := time.Parse(time.RFC3339, *repository.Freshness.ObservedAt); err != nil {
				t.Errorf("repository %s has invalid freshness time: %v", repository.RepositoryID, err)
			}
		}
		if repository.Freshness.ObservedCommit != nil || repository.Freshness.SourceBranch != nil {
			if repository.Freshness.FrontierPosture != "observed_not_accepted_frontier" {
				t.Errorf("repository %s turns observation into accepted frontier posture", repository.RepositoryID)
			}
		}
		for posture, fields := range contract.FieldPostureGroups {
			for _, field := range fields {
				source, ok := repository.FieldSources[field]
				if !ok || source.Source == "" || source.Posture != posture {
					t.Errorf("repository %s field %s lacks %s source posture: %#v", repository.RepositoryID, field, posture, source)
				}
			}
		}
		for _, required := range contract.RequiredTopLevelFields {
			if _, ok := rawRepositories[index][required]; !ok {
				t.Errorf("repository %s lacks required card field %s", repository.RepositoryID, required)
			}
		}
		for _, forbidden := range contract.ForbiddenTechnicalFields {
			if _, ok := rawRepositories[index][forbidden]; ok {
				t.Errorf("repository %s leaks technical registry field %s", repository.RepositoryID, forbidden)
			}
		}
	}
}

func TestSemanticRepositoryAwarenessCardMutationContract(t *testing.T) {
	corpusPayload := loadSearchContractPayload(t, "corpus.json")
	contractPayload := loadSearchContractPayload(t, "repository_card.json")
	type mutation func(*testing.T, map[string]any)
	tests := []struct {
		name           string
		mutateCorpus   mutation
		mutateContract mutation
		want           string
	}{
		{
			name: "missing nullable freshness field",
			mutateCorpus: func(t *testing.T, document map[string]any) {
				repositories := mustJSONArray(t, document["repositories"], "repositories")
				freshness := mustJSONObject(t, mustJSONObject(t, repositories[0], "repository 0")["freshness"], "repository 0 freshness")
				delete(freshness, "source_version")
			},
			want: "freshness missing required nested field source_version",
		},
		{
			name: "missing accepted context qualification",
			mutateCorpus: func(t *testing.T, document map[string]any) {
				repositories := mustJSONArray(t, document["repositories"], "repositories")
				contexts := mustJSONArray(t, mustJSONObject(t, repositories[0], "repository 0")["accepted_context"], "repository 0 accepted_context")
				delete(mustJSONObject(t, contexts[0], "repository 0 accepted_context 0"), "qualification")
			},
			want: "accepted_context[0] missing required nested field qualification",
		},
		{
			name: "empty accepted context still requires explicit item shape",
			mutateCorpus: func(t *testing.T, document map[string]any) {
				for _, rawRepository := range mustJSONArray(t, document["repositories"], "repositories") {
					mustJSONObject(t, rawRepository, "repository")["accepted_context"] = []any{}
				}
			},
			mutateContract: func(t *testing.T, document map[string]any) {
				nested := mustJSONObject(t, document["required_nested_fields"], "required_nested_fields")
				nested["accepted_context"] = []any{"record_id", "summary"}
			},
			want: "accepted_context nested field contract",
		},
		{
			name: "owning project must match canonical project",
			mutateCorpus: func(t *testing.T, document map[string]any) {
				repositories := mustJSONArray(t, document["repositories"], "repositories")
				owner := mustJSONObject(t, mustJSONObject(t, repositories[0], "repository 0")["owning_project"], "repository 0 owning_project")
				owner["name"] = "Not LOOM"
			},
			want: "owning_project does not match canonical project",
		},
		{
			name: "accepted context must join accepted record",
			mutateCorpus: func(t *testing.T, document map[string]any) {
				repositories := mustJSONArray(t, document["repositories"], "repositories")
				contexts := mustJSONArray(t, mustJSONObject(t, repositories[0], "repository 0")["accepted_context"], "repository 0 accepted_context")
				mustJSONObject(t, contexts[0], "repository 0 accepted_context 0")["record_id"] = "22222222-2222-4222-8222-222222222001"
			},
			want: "does not resolve to accepted_records",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedCorpus := mutateSearchContractPayload(t, corpusPayload, test.mutateCorpus)
			mutatedContract := mutateSearchContractPayload(t, contractPayload, test.mutateContract)
			err := validateRepositoryCardNestedAndJoinContract(mutatedCorpus, mutatedContract)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestRetrievalBoundsAndLatencyMethodContract(t *testing.T) {
	limits := loadSearchContract[searchLimitsContract](t, "limits.json")
	if limits.SchemaVersion != "loom.provenance_search_limits.v1" {
		t.Fatalf("unexpected limits schema %q", limits.SchemaVersion)
	}
	result := limits.ResultLimits
	if result.DefaultTotal < 1 || result.MaximumRequestedTotal < result.DefaultTotal || result.MaximumPerCollection > result.MaximumRequestedTotal || result.MaximumRepositoryCards > result.MaximumRequestedTotal || !result.StableOrderingRequired {
		t.Fatalf("result limits are incomplete or internally inconsistent: %#v", result)
	}
	context := limits.ContextLimits
	if context.Unit != "unicode_code_points" || context.MaximumMatchExplanation < 1 || context.MaximumCompactResult < context.MaximumMatchExplanation || context.MaximumCompactResponse < context.MaximumCompactResult || context.MaximumRepositoryAcceptedContextItems < 1 || !context.TruncationMarkerRequired {
		t.Fatalf("compact context limits are incomplete: %#v", context)
	}
	for name, value := range map[string]int{
		"sources":          context.ExactGetMaximumSources,
		"relationships":    context.ExactGetMaximumRelationships,
		"lifecycle_events": context.ExactGetMaximumLifecycleEvents,
		"qualifications":   context.ExactGetMaximumQualifications,
	} {
		if value < 1 {
			t.Errorf("exact-get %s limit is not positive", name)
		}
	}

	latency := limits.LatencyMeasurement
	if latency.Environment != "disposable_local_in_process_corpus" || latency.Clock != "monotonic" || latency.FixtureScope != "all_contract_queries" {
		t.Fatalf("latency environment, clock, or corpus scope changed: %#v", latency)
	}
	if latency.WarmupIterationsPerQuery < 1 || latency.MeasuredIterationsPerQuery < 30 || latency.IndependentProcessRuns < 3 {
		t.Fatalf("latency sample is not independently repeatable: %#v", latency)
	}
	if latency.MeasureFrom == "" || latency.MeasureUntil == "" || latency.LocalP95BudgetMilliseconds < 1 || latency.PerQueryTimeoutMilliseconds <= latency.LocalP95BudgetMilliseconds {
		t.Fatalf("latency boundary or budget is invalid: %#v", latency)
	}
	for _, step := range []string{"query_normalization", "collection_retrieval", "ranking", "stable_ordering", "compact_projection", "response_encoding"} {
		if !containsString(latency.IncludedSteps, step) {
			t.Errorf("latency method omits %s", step)
		}
	}
	for _, excluded := range latency.ExcludedSteps {
		if containsString(latency.IncludedSteps, excluded) {
			t.Errorf("latency step %s is both included and excluded", excluded)
		}
	}
	for _, statistic := range []string{"sample_count", "p50_milliseconds", "p95_milliseconds", "maximum_milliseconds"} {
		if !containsString(latency.ReportedStatistics, statistic) {
			t.Errorf("latency report omits %s", statistic)
		}
	}
	if latency.AggregationRule == "" || latency.MutationPolicy != "read_only_frozen_corpus_reset_before_each_process_run" {
		t.Fatal("latency aggregation or read-only reset policy is not frozen")
	}
}

type repositoryCardNestedShape struct {
	container string
	kind      string
	fields    []string
}

func validateRepositoryCardNestedAndJoinContract(corpusPayload, contractPayload []byte) error {
	corpus, err := decodeSearchContractPayload[searchFixtureCorpus](corpusPayload)
	if err != nil {
		return fmt.Errorf("decode corpus: %w", err)
	}
	contract, err := decodeSearchContractPayload[repositoryCardContract](contractPayload)
	if err != nil {
		return fmt.Errorf("decode repository card contract: %w", err)
	}
	shapes := []repositoryCardNestedShape{
		{container: "owning_project", kind: "object", fields: []string{"project_id", "name", "lifecycle", "navigation_ref"}},
		{container: "accepted_context", kind: "array", fields: []string{"record_id", "summary", "qualification"}},
		{container: "freshness", kind: "object", fields: []string{"posture", "observed_at", "source_version", "source_digest", "observed_commit", "source_branch", "frontier_posture"}},
		{container: "field_sources", kind: "object_values", fields: []string{"source", "posture"}},
	}
	if len(contract.RequiredNestedFields) != len(shapes) {
		return fmt.Errorf("required_nested_fields must declare exactly %d containers, got %d", len(shapes), len(contract.RequiredNestedFields))
	}
	for _, shape := range shapes {
		fields, ok := contract.RequiredNestedFields[shape.container]
		if !ok || !sameStringSet(fields, shape.fields) {
			return fmt.Errorf("%s nested field contract must be exactly %v, got %v", shape.container, shape.fields, fields)
		}
	}

	var rawDocument map[string]json.RawMessage
	if err := json.Unmarshal(corpusPayload, &rawDocument); err != nil {
		return fmt.Errorf("decode raw corpus: %w", err)
	}
	var rawRepositories []map[string]json.RawMessage
	if err := json.Unmarshal(rawDocument["repositories"], &rawRepositories); err != nil {
		return fmt.Errorf("decode raw repositories: %w", err)
	}
	if len(rawRepositories) != len(corpus.Repositories) {
		return fmt.Errorf("typed and raw repository counts differ: %d != %d", len(corpus.Repositories), len(rawRepositories))
	}

	projects := make(map[string]searchFixtureProject, len(corpus.Projects))
	for _, project := range corpus.Projects {
		projects[project.ProjectID] = project
	}
	itemCollections := make(map[string]string, len(corpus.Items))
	for _, item := range corpus.Items {
		itemCollections[item.ID] = item.Collection
	}

	for index, repository := range corpus.Repositories {
		label := fmt.Sprintf("repository %d", index)
		canonicalProject, ok := projects[repository.OwningProject.ProjectID]
		if !ok {
			return fmt.Errorf("%s %s owning_project references unknown project %s", label, repository.RepositoryID, repository.OwningProject.ProjectID)
		}
		if repository.OwningProject.Name != canonicalProject.Name ||
			repository.OwningProject.Lifecycle != canonicalProject.Lifecycle ||
			repository.OwningProject.NavigationRef != canonicalProject.NavigationRef {
			return fmt.Errorf("%s %s owning_project does not match canonical project %s", label, repository.RepositoryID, canonicalProject.ProjectID)
		}

		for _, shape := range shapes {
			payload, ok := rawRepositories[index][shape.container]
			if !ok {
				return fmt.Errorf("%s %s missing required nested container %s", label, repository.RepositoryID, shape.container)
			}
			switch shape.kind {
			case "object":
				if _, err := rawObjectWithRequiredFields(payload, shape.fields, label+" "+shape.container); err != nil {
					return err
				}
			case "array":
				items, err := rawArrayWithRequiredObjectFields(payload, shape.fields, label+" "+shape.container)
				if err != nil {
					return err
				}
				if len(items) != len(repository.AcceptedContext) {
					return fmt.Errorf("%s accepted_context typed/raw counts differ", label)
				}
			case "object_values":
				if err := rawObjectValuesWithRequiredFields(payload, shape.fields, label+" "+shape.container); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported nested shape %s for %s", shape.kind, shape.container)
			}
		}

		for contextIndex, context := range repository.AcceptedContext {
			if strings.TrimSpace(context.RecordID) == "" || strings.TrimSpace(context.Summary) == "" || strings.TrimSpace(context.Qualification) == "" {
				return fmt.Errorf("%s accepted_context[%d] is incomplete", label, contextIndex)
			}
			if itemCollections[context.RecordID] != "accepted_records" {
				return fmt.Errorf("%s accepted_context[%d] record_id %s does not resolve to accepted_records", label, contextIndex, context.RecordID)
			}
		}
	}
	return nil
}

func rawObjectWithRequiredFields(payload json.RawMessage, fields []string, label string) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return nil, fmt.Errorf("%s must be an object, got null", label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object: %v", label, err)
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("%s missing required nested field %s", label, field)
		}
	}
	return object, nil
}

func rawArrayWithRequiredObjectFields(payload json.RawMessage, fields []string, label string) ([]map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return nil, fmt.Errorf("%s must be an array, got null", label)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(payload, &items); err != nil {
		return nil, fmt.Errorf("%s must be an array of objects: %w", label, err)
	}
	for index, item := range items {
		if item == nil {
			return nil, fmt.Errorf("%s[%d] must be an object", label, index)
		}
		for _, field := range fields {
			if _, ok := item[field]; !ok {
				return nil, fmt.Errorf("%s[%d] missing required nested field %s", label, index, field)
			}
		}
	}
	return items, nil
}

func rawObjectValuesWithRequiredFields(payload json.RawMessage, fields []string, label string) error {
	object, err := rawObjectWithRequiredFields(payload, nil, label)
	if err != nil {
		return err
	}
	if len(object) == 0 {
		return fmt.Errorf("%s must contain at least one field source", label)
	}
	for key, value := range object {
		if _, err := rawObjectWithRequiredFields(value, fields, label+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet := stringSet(left)
	if len(leftSet) != len(left) {
		return false
	}
	for _, value := range right {
		if !leftSet[value] {
			return false
		}
	}
	return true
}

func mutateSearchContractPayload(t *testing.T, payload []byte, mutate func(*testing.T, map[string]any)) []byte {
	t.Helper()
	if mutate == nil {
		return payload
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode mutation source: %v", err)
	}
	mutate(t, document)
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode mutation: %v", err)
	}
	return mutated
}

func mustJSONObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not a JSON object: %T", label, value)
	}
	return object
}

func mustJSONArray(t *testing.T, value any, label string) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not a JSON array: %T", label, value)
	}
	return items
}

func fixtureIdentityIndex(t *testing.T, corpus searchFixtureCorpus) (map[string]bool, map[string]string) {
	t.Helper()
	ids := map[string]bool{}
	collections := map[string]string{}
	add := func(id, collection string) {
		if id == "" || ids[id] {
			t.Fatalf("fixture identity is empty or duplicated: %q", id)
		}
		ids[id] = true
		collections[id] = collection
	}
	projectIDs := map[string]searchFixtureProject{}
	for _, project := range corpus.Projects {
		if !validTypedID(project.ProjectID, "project_") || project.Name == "" || project.Slug == "" || project.Lifecycle == "" || project.NavigationRef == "" {
			t.Fatalf("project fixture is incomplete: %#v", project)
		}
		projectIDs[project.ProjectID] = project
		add(project.ProjectID, "technical_project_registry")
	}
	repositoryIDs := map[string]bool{}
	for _, repository := range corpus.Repositories {
		if _, ok := projectIDs[repository.OwningProject.ProjectID]; !ok {
			t.Fatalf("repository %s references an unknown owning project", repository.RepositoryID)
		}
		repositoryIDs[repository.RepositoryID] = true
		add(repository.RepositoryID, "repo_state")
	}
	for _, item := range corpus.Items {
		if item.ProjectID != nil {
			if _, ok := projectIDs[*item.ProjectID]; !ok {
				t.Fatalf("item %s references an unknown project", item.ID)
			}
		}
		if item.RepositoryID != nil && !repositoryIDs[*item.RepositoryID] {
			t.Fatalf("item %s references an unknown repository", item.ID)
		}
		add(item.ID, item.Collection)
	}
	return ids, collections
}

func itemWithTag(t *testing.T, items []searchFixtureItem, tag string) searchFixtureItem {
	t.Helper()
	for _, item := range items {
		if containsString(item.ScenarioTags, tag) {
			return item
		}
	}
	t.Fatalf("item with scenario tag %s was not found", tag)
	return searchFixtureItem{}
}

func assertTaggedItemContract(t *testing.T, items []searchFixtureItem, tag, collection, lifecycle, posture string, technicalOnly bool) {
	t.Helper()
	for _, item := range items {
		if !containsString(item.ScenarioTags, tag) || item.Collection != collection {
			continue
		}
		if item.Lifecycle != lifecycle || item.AssertionPosture != posture || item.TechnicalOnly != technicalOnly {
			t.Fatalf("scenario %s is not frozen to the expected lifecycle posture: %#v", tag, item)
		}
		return
	}
	t.Fatalf("scenario %s has no %s fixture", tag, collection)
}

func isSemanticCollection(collection string) bool {
	return collection == "accepted_records" || collection == "pending_candidates" || collection == "unresolved_cases"
}

func engineForFixtureCollection(collection string) string {
	switch collection {
	case "accepted_records", "pending_candidates", "unresolved_cases", "repo_state":
		return "provenance"
	case "notes":
		return "notes"
	case "objects":
		return "objects"
	case "project_context":
		return "project_registry"
	default:
		return "unrecognized"
	}
}

func exactGetPrefix(collection string) string {
	switch collection {
	case "accepted_records":
		return "loom provenance record get "
	case "pending_candidates":
		return "loom provenance candidate get "
	case "unresolved_cases":
		return "loom provenance case get "
	case "repo_state":
		return "loom provenance repo get "
	case "notes":
		return "loom notes get "
	case "objects":
		return "loom objects get "
	case "technical_project_registry":
		return "loom project inspect "
	default:
		return "unrecognized collection: "
	}
}

func collectionContains(collections []searchFixtureCollection, collection, id string) bool {
	for _, candidate := range collections {
		if candidate.Collection == collection && containsString(candidate.IDs, id) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func validTypedID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(value, prefix)
	if len(suffix) != 26 {
		return false
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, character := range suffix {
		if !strings.ContainsRune(alphabet, character) {
			return false
		}
	}
	return true
}

func loadRawSearchObject(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	payload, err := searchContractFS.ReadFile("testdata/search/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode raw %s: %v", name, err)
	}
	return value
}
