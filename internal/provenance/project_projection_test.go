package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/repostate"
)

const (
	projectionTestProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	projectionTestRepoOne   = "repo_01ARZ3NDEKTSV4RRFFQ69G5FB0"
	projectionTestRepoTwo   = "repo_01ARZ3NDEKTSV4RRFFQ69G5FB1"
)

func TestRepositoryProjectionQueryTermsAreBoundedDeterministicAndFailClosed(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		wantTerms    []string
		wantRequired int
	}{
		{name: "empty list query", query: "", wantTerms: []string{}, wantRequired: 0},
		{name: "one term requires the term", query: "Atlas", wantTerms: []string{"atla"}, wantRequired: 1},
		{name: "natural finder terms require half", query: "Which repository is working on semantic search fixtures now?", wantTerms: []string{"semantic", "search", "fixture"}, wantRequired: 2},
		{name: "odd terms round up", query: "alpha beta gamma", wantTerms: []string{"alpha", "beta", "gamma"}, wantRequired: 2},
		{name: "duplicates stay deterministic", query: "Atlas atlas ATLAS", wantTerms: []string{"atla"}, wantRequired: 1},
		{name: "non-empty stop words fail closed", query: "the and where", wantTerms: []string{}, wantRequired: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for iteration := 0; iteration < 5; iteration++ {
				terms, required := repositoryProjectionQueryTerms(test.query)
				if !reflect.DeepEqual(terms, test.wantTerms) || required != test.wantRequired {
					t.Fatalf("query terms iteration %d = %#v required=%d, want %#v required=%d", iteration, terms, required, test.wantTerms, test.wantRequired)
				}
				if len(terms) > MaximumSearchTerms {
					t.Fatalf("query terms exceeded bound: %d > %d", len(terms), MaximumSearchTerms)
				}
			}
		})
	}
	terms, required := repositoryProjectionQueryTerms("term01 term02 term03 term04 term05 term06 term07 term08 term09 term10 term11 term12 term13 term14 term15 term16 term17 term18 term19 term20")
	if len(terms) != MaximumSearchTerms || required != MaximumSearchTerms/2 || terms[0] != "term01" || terms[len(terms)-1] != "term16" {
		t.Fatalf("bounded finder terms = %#v required=%d", terms, required)
	}
}

func TestRepositoryProjectionFinderFrozenRepoListQueriesPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "repository_projection_finder_frozen")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	queries := loadSearchContract[searchQueryContract](t, "queries.json")
	seedSliceTwoSearchCorpus(t, store, corpus)
	seedSearchRepositoryProjectionCorpus(t, store, corpus)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	ctx := context.Background()
	verified := 0
	for _, fixture := range queries.Queries {
		if fixture.Request.Surface != "repo_list" {
			continue
		}
		fixture := fixture
		t.Run(fixture.QueryID, func(t *testing.T) {
			request := RepositoryProjectionListRequest{
				Query: fixture.Query, Project: fixture.Request.Filters["project"],
				Topic: fixture.Request.Filters["topic"], Role: fixture.Request.Filters["role"],
				TrackingStatus: RepositoryTrackingStatus(fixture.Request.Filters["tracking_status"]),
			}
			var first RepositoryProjectionList
			for iteration := 0; iteration < 3; iteration++ {
				list, err := api.ListRepositoryProjections(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				if iteration == 0 {
					first = list
				} else if !reflect.DeepEqual(list, first) {
					t.Fatalf("repository finder was not deterministic on iteration %d: first=%#v current=%#v", iteration, first, list)
				}
			}
			want := fixtureRepositoryCollectionIDs(t, fixture)
			got := repositoryProjectionCardIDs(first.Items)
			if !reflect.DeepEqual(got, want) || first.Ordering != "repository_id_asc" || first.Returned != len(first.Items) || first.Truncated {
				t.Fatalf("frozen repository finder result = ids=%#v metadata=%#v, want ids=%#v", got, first, want)
			}
			for _, contamination := range fixture.Expected.ProhibitedContamination {
				if containsProjectionString(got, contamination.ID) {
					t.Fatalf("frozen repository finder returned prohibited contamination %s: %#v", contamination.ID, got)
				}
			}
			card, err := api.GetRepositoryProjection(ctx, fixture.Expected.ExactGet.ID)
			if err != nil || card.RepositoryID != fixture.Expected.ExactGet.ID || len(first.Items) != 1 || first.Items[0].RepositoryID != card.RepositoryID {
				t.Fatalf("repository exact get diverged from finder: card=%#v list=%#v err=%v", card, first.Items, err)
			}
			if fixture.QueryID == "repo-current-focus-over-stale-project" {
				if !repositoryContextContains(first.Items[0].AcceptedContext, SemanticID("11111111-1111-4111-8111-111111111009")) || !reflect.DeepEqual(first.Items[0].AcceptedContext, card.AcceptedContext) {
					t.Fatalf("repository finder changed the separate accepted-context join: list=%#v exact=%#v", first.Items[0].AcceptedContext, card.AcceptedContext)
				}
			}
		})
		verified++
	}
	if verified != 4 {
		t.Fatalf("verified %d frozen repo_list scenarios, want 4", verified)
	}
}

func TestRepositoryProjectionFinderPreservesFiltersOrderingLimitAndMembershipPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "repository_projection_finder_boundaries")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	seedSearchRepositoryProjectionCorpus(t, store, corpus)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	ctx := context.Background()

	allAtlas, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{Query: "Atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := repositoryProjectionCardIDs(allAtlas.Items), []string{"repo_01K41SEARCH000000000000002", "repo_01K41SEARCH000000000000003"}; !reflect.DeepEqual(got, want) || allAtlas.Ordering != "repository_id_asc" {
		t.Fatalf("stable Atlas ordering = %#v metadata=%#v, want %#v", got, allAtlas, want)
	}
	limited, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{Query: "Atlas", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := repositoryProjectionCardIDs(limited.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000002"}) || !limited.Truncated || limited.Returned != 1 {
		t.Fatalf("repository finder limit/truncation changed: %#v", limited)
	}
	personal, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{
		Query: "Atlas literature", Project: "personal-research", Topic: "literature", Role: "primary", TrackingStatus: RepositoryTrackingNotEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := repositoryProjectionCardIDs(personal.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000002"}) {
		t.Fatalf("combined project/topic/role/tracking filters changed: %#v", personal)
	}
	client, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{
		Query: "Atlas archival workflow", Project: "project_01K41SEARCH000000000000003", Role: "component", TrackingStatus: RepositoryTrackingMalformed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := repositoryProjectionCardIDs(client.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000003"}) {
		t.Fatalf("typed client filters changed: %#v", client)
	}
	stopWordsOnly, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{Query: "the and where", Project: "project_01K41SEARCH000000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stopWordsOnly.Items) != 0 || stopWordsOnly.Truncated {
		t.Fatalf("non-searchable query broadened repository results: %#v", stopWordsOnly)
	}

	personalProject := corpus.Projects[1]
	insertSearchProjectProjection(t, store, personalProject, 2, mustSearchFixtureTime(t, corpus.FrozenAt).Add(time.Minute))
	current, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{Query: "Atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if got := repositoryProjectionCardIDs(current.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000003"}) {
		t.Fatalf("repository finder retained removed latest membership: %#v", current)
	}
	if _, err := api.GetRepositoryProjection(ctx, "repo_01K41SEARCH000000000000002"); !errors.Is(err, ErrRepositoryProjectionNotFound) {
		t.Fatalf("exact get retained removed latest membership: %v", err)
	}
}

func fixtureRepositoryCollectionIDs(t *testing.T, fixture searchQueryExpectation) []string {
	t.Helper()
	for _, collection := range fixture.Expected.Collections {
		if collection.Collection == "repo_state" {
			return append([]string(nil), collection.IDs...)
		}
	}
	t.Fatalf("frozen repo_list query %s has no repo_state collection", fixture.QueryID)
	return nil
}

func repositoryProjectionCardIDs(cards []RepositoryCard) []string {
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.RepositoryID)
	}
	return ids
}

func containsProjectionString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func repositoryContextContains(items []RepositoryAcceptedContext, expected SemanticID) bool {
	for _, item := range items {
		if item.RecordID == expected {
			return true
		}
	}
	return false
}

func TestPrepareProjectProjectionIsDeterministicBoundedAndPathFree(t *testing.T) {
	input := projectionTestSyncInput()
	createdAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	project, repositories, err := prepareProjectProjectionSync(input, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	replayedProject, replayedRepositories, err := prepareProjectProjectionSync(input, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if project.ID != replayedProject.ID || project.SnapshotDigest != replayedProject.SnapshotDigest || len(repositories) != 2 || len(replayedRepositories) != 2 {
		t.Fatalf("projection was not deterministic: %#v %#v", project, replayedProject)
	}
	for index := range repositories {
		if repositories[index].ID != replayedRepositories[index].ID || repositories[index].SnapshotDigest != replayedRepositories[index].SnapshotDigest {
			t.Fatalf("repository projection %d was not deterministic", index)
		}
	}

	var card map[string]any
	if err := json.Unmarshal(repositories[0].CardJSON, &card); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"repository_id", "name", "aliases", "owning_project", "role", "purpose", "topics",
		"current_state", "active_focus", "recent_outcomes", "next_priorities", "blockers",
		"accepted_context", "freshness", "portable_navigation_ref", "tracking_status", "diagnostics", "field_sources",
	} {
		if _, ok := card[field]; !ok {
			t.Fatalf("repository card omitted frozen field %q: %s", field, repositories[0].CardJSON)
		}
	}
	payload := string(repositories[0].CardJSON)
	for _, forbidden := range []string{"/private/loom/project", "project_root", "repository_path", "facet_path", "registry_recorded_at", "membership_source_digest", "git_command", "raw_observation"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("repository projection leaked forbidden technical material %q: %s", forbidden, payload)
		}
	}
	if got := card["accepted_context"]; got == nil {
		t.Fatalf("accepted context was omitted: %s", payload)
	}
	freshness, ok := card["freshness"].(map[string]any)
	if !ok || freshness["observed_commit"] == nil || freshness["source_digest"] == nil || freshness["frontier_posture"] != "observed_not_accepted_frontier" {
		t.Fatalf("repository freshness lost qualified observation: %#v", freshness)
	}
}

func TestProjectProjectionSyncReplayFiltersAndAppendOnlyStoragePostgres(t *testing.T) {
	pool, store, _ := migratedStore(t, "project_projection")
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service, err := NewService(store, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	input := projectionTestSyncInput()

	first, err := api.SyncProjectProjection(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.ProjectReplayed || len(first.Repositories) != 2 {
		t.Fatalf("first projection receipt = %#v", first)
	}
	if got := receiptRepositorySourceVersion(first, projectionTestRepoOne); got != 1 {
		t.Fatalf("first repository projection revision = %d, want 1", got)
	}
	replayed, err := api.SyncProjectProjection(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !replayed.ProjectReplayed || len(replayed.Repositories) != 2 || !replayed.Repositories[0].Replayed || !replayed.Repositories[1].Replayed {
		t.Fatalf("same-version replay was not idempotent: %#v", replayed)
	}
	observedAgain := projectionTestSyncInput()
	observedAgain.Project.ObservedAt = observedAgain.Project.ObservedAt.Add(time.Hour)
	for index := range observedAgain.Repositories {
		observedAgain.Repositories[index].Extraction.Freshness.ObservedAt = observedAgain.Repositories[index].Extraction.Freshness.ObservedAt.Add(time.Hour)
	}
	if receipt, err := api.SyncProjectProjection(ctx, observedAgain); err != nil || !receipt.Replayed {
		t.Fatalf("same source replay changed only by observation time = %#v, err=%v", receipt, err)
	}

	changedProjectDigest := projectionTestSyncInput()
	changedProjectDigest.Project.Source.SemanticDigest = "sha256:" + strings.Repeat("e", 64)
	if _, err := api.SyncProjectProjection(ctx, changedProjectDigest); !errors.Is(err, ErrProjectionReplayConflict) {
		t.Fatalf("same project source version with changed source digest error = %v, want %v", err, ErrProjectionReplayConflict)
	}
	changedRepositoryState := projectionTestSyncInput()
	changedRepositoryState.Repositories[0].Extraction.DiscoveryFields[10].Value = "sha256:" + strings.Repeat("f", 64)
	stateReceipt, err := api.SyncProjectProjection(ctx, changedRepositoryState)
	if err != nil || stateReceipt.Replayed || receiptRepositorySourceVersion(stateReceipt, projectionTestRepoOne) != 2 {
		t.Fatalf("changed repository source did not append the next projection revision: %#v, err=%v", stateReceipt, err)
	}
	changedGit := projectionTestSyncInput()
	changedGit.Repositories[0].Extraction.Freshness.SourceCommit = strings.Repeat("e", 40)
	gitReceipt, err := api.SyncProjectProjection(ctx, changedGit)
	if err != nil || gitReceipt.Replayed || receiptRepositorySourceVersion(gitReceipt, projectionTestRepoOne) != 3 {
		t.Fatalf("changed repository Git observation did not append the next projection revision: %#v, err=%v", gitReceipt, err)
	}
	changedProjectContext := projectionTestSyncInput()
	changedProjectContext.Project.Project.Name = "Atlas Next"
	changedProjectContext.Project.Project.Lifecycle = "review"
	changedProjectContext.Project.Source.SourceRevision = 13
	changedProjectContext.Project.Source.SemanticDigest = "sha256:" + strings.Repeat("d", 64)
	contextReceipt, err := api.SyncProjectProjection(ctx, changedProjectContext)
	if err != nil || contextReceipt.Replayed || receiptRepositorySourceVersion(contextReceipt, projectionTestRepoOne) != 4 {
		t.Fatalf("changed project context did not append the next repository projection revision: %#v, err=%v", contextReceipt, err)
	}
	if got, getErr := api.GetRepositoryProjection(ctx, projectionTestRepoOne); getErr != nil || got.OwningProject.Name != "Atlas Next" || got.OwningProject.Lifecycle != "review" {
		t.Fatalf("latest projection omitted the changed project context: %#v, err=%v", got.OwningProject, getErr)
	}
	projectSnapshot, contextRepositories, err := prepareProjectProjectionSync(changedProjectContext, now)
	if err != nil {
		t.Fatal(err)
	}
	var contextProjectionRevision int64
	if err := pool.QueryRow(ctx, `SELECT projection_revision FROM provenance.project_projection_snapshots WHERE id=$1::uuid`, string(contextReceipt.ProjectSnapshotID)).Scan(&contextProjectionRevision); err != nil {
		t.Fatal(err)
	}
	forgedParent := contextRepositories[0]
	forgedParent.SourceIdentityDigest = repositoryIdentityForProject(t, projectSnapshot, contextReceipt.ProjectSnapshotID, contextProjectionRevision, forgedParent)
	forgedParent.ProjectSnapshotID = first.ProjectSnapshotID
	if err := store.Transact(ctx, func(transaction *Store) error {
		_, _, _, appendErr := transaction.appendRepositoryProjection(ctx, forgedParent)
		return appendErr
	}); !errors.Is(err, ErrProjectionReplayConflict) {
		t.Fatalf("reused repository identity with a forged parent error = %v, want %v", err, ErrProjectionReplayConflict)
	}

	forged := contextRepositories[0]
	forged.ProjectSnapshotID = contextReceipt.ProjectSnapshotID
	forged.SourceIdentityDigest = repositoryIdentityForProject(t, projectSnapshot, contextReceipt.ProjectSnapshotID, contextProjectionRevision, forged)
	forged = projectionSnapshotWithChangedPurpose(t, forged, "Changed without changing the accepted source identity")
	if err := store.Transact(ctx, func(transaction *Store) error {
		_, _, _, appendErr := transaction.appendRepositoryProjection(ctx, forged)
		return appendErr
	}); !errors.Is(err, ErrProjectionReplayConflict) {
		t.Fatalf("same repository identity with changed deterministic payload error = %v, want %v", err, ErrProjectionReplayConflict)
	}

	if err := store.AppendRecord(ctx, Record{
		ID:               SemanticID("11111111-1111-4111-8111-111111111111"),
		SchemaVersion:    SchemaVersion,
		Claim:            "Accepted project decision",
		RecordKind:       "decision",
		RecordContext:    "projection acceptance fixture",
		Domain:           "loom",
		Visibility:       "private",
		AssertionPosture: "accepted",
		Temporal:         TemporalInterpretation{Interpretation: "current"},
		Anchors:          &StructuralAnchors{Entities: []string{projectionTestRepoOne}, Projects: []string{projectionTestProjectID}},
		CreatedAt:        now,
		Payload:          json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCandidate(ctx, Candidate{
		ID:               SemanticID("22222222-2222-4222-8222-222222222222"),
		SchemaVersion:    SchemaVersion,
		State:            "pending",
		Domain:           "loom",
		Visibility:       "private",
		RecordKind:       "decision",
		Claim:            "Pending proposal must stay separate",
		RecordContext:    "projection acceptance fixture",
		AssertionPosture: "proposed",
		ProducerID:       "fixture",
		RegisteredAt:     now,
		Submitted:        json.RawMessage(`{}`),
		Payload:          json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	list, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{Project: "atlas", Topic: "provenance", Role: "component", TrackingStatus: RepositoryTrackingValid})
	if err != nil {
		t.Fatal(err)
	}
	if list.Ordering != "repository_id_asc" || len(list.Items) != 1 || list.Items[0].RepositoryID != projectionTestRepoOne {
		t.Fatalf("stable filtered repository list = %#v", list)
	}
	if len(list.Items[0].AcceptedContext) != 1 || list.Items[0].AcceptedContext[0].Summary != "Accepted project decision" {
		t.Fatalf("accepted context was not joined separately: %#v", list.Items[0].AcceptedContext)
	}
	notEnabled, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{TrackingStatus: RepositoryTrackingNotEnabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(notEnabled.Items) != 1 || notEnabled.Items[0].RepositoryID != projectionTestRepoTwo {
		t.Fatalf("not_enabled projection was not visible: %#v", notEnabled)
	}
	got, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne)
	if err != nil {
		t.Fatal(err)
	}
	if got.RepositoryID != projectionTestRepoOne || len(got.AcceptedContext) != 1 {
		t.Fatalf("exact repository projection = %#v", got)
	}

	var storedContext []byte
	if err := pool.QueryRow(ctx, `SELECT card_json->'accepted_context' FROM provenance.repository_projection_snapshots WHERE repository_id=$1`, projectionTestRepoOne).Scan(&storedContext); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(storedContext)) != "[]" {
		t.Fatalf("accepted context was persisted inside a deterministic snapshot: %s", storedContext)
	}
	if _, err := pool.Exec(ctx, `UPDATE provenance.repository_projection_snapshots SET searchable_summary='forbidden' WHERE repository_id=$1`, projectionTestRepoOne); err == nil {
		t.Fatal("repository projection update bypassed append-only trigger")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM provenance.project_projection_snapshots WHERE project_id=$1`, projectionTestProjectID); err == nil {
		t.Fatal("project projection delete bypassed append-only trigger")
	}
}

func TestRepositoryProjectionRevisionAllocationIsConcurrentAndTransactionalPostgres(t *testing.T) {
	pool, store, _ := migratedStore(t, "project_projection_revision")
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service, err := NewService(store, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	initial, err := api.SyncProjectProjection(ctx, projectionTestSyncInput())
	if err != nil {
		t.Fatal(err)
	}

	inputs := []ProjectProjectionSyncInput{projectionTestSyncInput(), projectionTestSyncInput()}
	inputs[0].Repositories[0].Extraction.DiscoveryFields[10].Value = "sha256:" + strings.Repeat("e", 64)
	inputs[1].Repositories[0].Extraction.Freshness.SourceCommit = strings.Repeat("f", 40)
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(inputs))
	receipts := make(chan ProjectProjectionSyncReceipt, len(inputs))
	wait.Add(len(inputs))
	for _, input := range inputs {
		go func(input ProjectProjectionSyncInput) {
			defer wait.Done()
			receipt, syncErr := api.SyncProjectProjection(ctx, input)
			if syncErr == nil {
				receipts <- receipt
			}
			errorsFound <- syncErr
		}(input)
	}
	wait.Wait()
	close(errorsFound)
	close(receipts)
	for syncErr := range errorsFound {
		if syncErr != nil {
			t.Fatal(syncErr)
		}
	}
	versions := []int64{}
	for receipt := range receipts {
		versions = append(versions, receiptRepositorySourceVersion(receipt, projectionTestRepoOne))
	}
	sort.Slice(versions, func(left, right int) bool { return versions[left] < versions[right] })
	if !reflect.DeepEqual(versions, []int64{2, 3}) {
		t.Fatalf("concurrent source identities allocated revisions %v, want [2 3]", versions)
	}

	_, preparedRepositories, err := prepareProjectProjectionSync(projectionTestSyncInput(), now)
	if err != nil {
		t.Fatal(err)
	}
	appendable := preparedRepositories[0]
	appendable.ProjectSnapshotID = initial.ProjectSnapshotID
	appendable.SourceIdentityDigest = projectionTestDigest("1")
	forged := preparedRepositories[0]
	forged.SourceIdentityDigest = appendable.SourceIdentityDigest
	forged = projectionSnapshotWithChangedPurpose(t, forged, "Changed without changing the accepted source identity")
	forged.ProjectSnapshotID = appendable.ProjectSnapshotID
	if err := store.Transact(ctx, func(transaction *Store) error {
		if _, _, _, appendErr := transaction.appendRepositoryProjection(ctx, appendable); appendErr != nil {
			return appendErr
		}
		_, _, _, appendErr := transaction.appendRepositoryProjection(ctx, forged)
		return appendErr
	}); !errors.Is(err, ErrProjectionReplayConflict) {
		t.Fatalf("transactional conflict = %v, want %v", err, ErrProjectionReplayConflict)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.repository_projection_snapshots WHERE repository_id=$1`, projectionTestRepoOne).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("failed projection transaction retained a partial repository append: count=%d, want 3", count)
	}
}

func TestRepositoryProjectionRevisionAdvancesForRealRepositoryStateAdapterPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "project_projection_real_adapter")
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	service, err := NewService(store, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	root := t.TempDir()
	writeProjectionAdapterState(t, root, "First declared state")
	observer := &projectionAdapterGitObserver{root: root, sourceCommit: strings.Repeat("a", 40), observedAt: now}
	source := projectstate.ProvenanceRepositorySource{
		Projection: projectstate.RepositoryProjection{
			RepositoryID: projectionTestRepoOne, RepositoryOwnerProjectID: projectionTestProjectID,
			Key: "atlas-api", Role: projects.ProjectRepositoryRoleComponent,
			RepositoryLifecycle: projects.RepositoryLifecycleActive,
			SourceBindingDigest: "sha256:" + strings.Repeat("d", 64),
			ObservationPosture:  projects.ProjectRepositoryObservationObserved,
			Git:                 &projectstate.GitProjection{CurrentBranch: "main"},
		},
		NavigationPath: "repos/atlas-api", MembershipSourcePath: ".loom/contracts/repos.yaml",
		ProjectSlug: "atlas", SourceVersion: 4, RepositoryRoot: root, Available: true, Owned: true,
	}
	adapter := repostate.ProvenanceAdapter{Extractor: repostate.Extractor{Git: observer}}
	sync := func(project projectstate.ProjectProjection) (ProjectProjectionSyncReceipt, error) {
		projected := adapter.ProjectForProvenance(ctx, source)
		for _, field := range projected.Extraction.DiscoveryFields {
			if field.Name == "navigation_path" && (field.Value != source.NavigationPath || field.Source.Path != source.MembershipSourcePath || field.Source.Digest != source.Projection.SourceBindingDigest) {
				t.Fatalf("real adapter source attribution changed: %#v", field)
			}
		}
		return api.SyncProjectProjection(ctx, ProjectProjectionSyncInput{Project: project, Repositories: []repostate.ProvenanceProjection{projected}})
	}
	project := projectionTestSyncInput().Project
	first, err := sync(project)
	if err != nil || first.Replayed || receiptRepositorySourceVersion(first, projectionTestRepoOne) != 1 {
		t.Fatalf("first real-adapter sync = %#v, err=%v", first, err)
	}
	replayed, err := sync(project)
	if err != nil || !replayed.Replayed || receiptRepositorySourceVersion(replayed, projectionTestRepoOne) != 1 {
		t.Fatalf("same real-adapter source was not an exact replay: %#v, err=%v", replayed, err)
	}

	writeProjectionAdapterState(t, root, "Second declared state")
	declaredChanged, err := sync(project)
	if err != nil || declaredChanged.Replayed || receiptRepositorySourceVersion(declaredChanged, projectionTestRepoOne) != 2 {
		t.Fatalf("real-adapter declared state change = %#v, err=%v", declaredChanged, err)
	}
	observer.sourceCommit = strings.Repeat("b", 40)
	gitChanged, err := sync(project)
	if err != nil || gitChanged.Replayed || receiptRepositorySourceVersion(gitChanged, projectionTestRepoOne) != 3 {
		t.Fatalf("real-adapter Git change = %#v, err=%v", gitChanged, err)
	}
	projectRevisionChanged := project
	projectRevisionChanged.Source.SourceRevision = 13
	projectRevisionChanged.Source.SemanticDigest = "sha256:" + strings.Repeat("e", 64)
	projectRevisionReceipt, err := sync(projectRevisionChanged)
	if err != nil || projectRevisionReceipt.Replayed || projectRevisionReceipt.ProjectReplayed || receiptRepositorySourceVersion(projectRevisionReceipt, projectionTestRepoOne) != 4 {
		t.Fatalf("real-adapter project revision change with unchanged card = %#v, err=%v", projectRevisionReceipt, err)
	}
	contextChanged := projectRevisionChanged
	contextChanged.Project.Name = "Atlas Context Changed"
	contextChanged.Project.Lifecycle = "review"
	contextChanged.Source.SourceRevision = 14
	contextChanged.Source.SemanticDigest = "sha256:" + strings.Repeat("f", 64)
	projectChanged, err := sync(contextChanged)
	if err != nil || projectChanged.Replayed || projectChanged.ProjectReplayed || receiptRepositorySourceVersion(projectChanged, projectionTestRepoOne) != 5 {
		t.Fatalf("real-adapter owning-project context change = %#v, err=%v", projectChanged, err)
	}
}

func TestCurrentRepositoryProjectionMembershipFollowsLatestProjectSnapshotPostgres(t *testing.T) {
	t.Run("archived context refreshes then complete removal hides current cards", func(t *testing.T) {
		pool, store, _ := migratedStore(t, "project_projection_membership_removal")
		ctx := context.Background()
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		service, err := NewService(store, WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		api := &FoundationAPI{store: store, service: service}
		active := projectionTestSyncInput()
		if _, err := api.SyncProjectProjection(ctx, active); err != nil {
			t.Fatal(err)
		}
		archived := projectionTestSyncInput()
		archived.Project.Project.Lifecycle = "archived"
		for index := range archived.Repositories {
			archived.Repositories[index].Source.Projection.RepositoryLifecycle = projects.RepositoryLifecycleArchived
		}
		archiveReceipt, err := api.SyncProjectProjection(ctx, archived)
		if err != nil || archiveReceipt.Replayed || archiveReceipt.ProjectReplayed || receiptRepositorySourceVersion(archiveReceipt, projectionTestRepoOne) != 2 {
			t.Fatalf("archived context projection = %#v, err=%v", archiveReceipt, err)
		}
		archiveReplay, err := api.SyncProjectProjection(ctx, archived)
		if err != nil || !archiveReplay.Replayed || !archiveReplay.ProjectReplayed {
			t.Fatalf("unchanged archived context replay = %#v, err=%v", archiveReplay, err)
		}
		listed, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{})
		if err != nil || len(listed.Items) != 2 || listed.Items[0].OwningProject.Lifecycle != "archived" {
			t.Fatalf("archived current repository cards = %#v, err=%v", listed, err)
		}

		removed := archived
		removed.Project.Source.SourceRevision = 13
		removed.Project.Source.SemanticDigest = "sha256:" + strings.Repeat("e", 64)
		removed.Project.Members = []projectstate.RepositoryProjection{}
		removed.Repositories = []repostate.ProvenanceProjection{}
		removedReceipt, err := api.SyncProjectProjection(ctx, removed)
		if err != nil || removedReceipt.Replayed || removedReceipt.ProjectReplayed || len(removedReceipt.Repositories) != 0 {
			t.Fatalf("complete member removal projection = %#v, err=%v", removedReceipt, err)
		}
		assertNoCurrentRepositoryProjection(t, ctx, api, projectionTestRepoOne)
		var historicalRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.repository_projection_snapshots WHERE project_id=$1`, projectionTestProjectID).Scan(&historicalRows); err != nil {
			t.Fatal(err)
		}
		if historicalRows != 4 {
			t.Fatalf("member removal rewrote or omitted repository history: rows=%d, want 4", historicalRows)
		}
	})

	t.Run("explicit unregistered transition hides current cards without cleanup", func(t *testing.T) {
		pool, store, _ := migratedStore(t, "project_projection_unregistered")
		ctx := context.Background()
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		service, err := NewService(store, WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		api := &FoundationAPI{store: store, service: service}
		if _, err := api.SyncProjectProjection(ctx, projectionTestSyncInput()); err != nil {
			t.Fatal(err)
		}
		unregistered := projectionTestSyncInput()
		unregistered.Project.Source = projectstate.SourceProjection{Posture: projectstate.SourcePostureNotRegistered}
		unregistered.Project.Members = []projectstate.RepositoryProjection{}
		unregistered.Repositories = []repostate.ProvenanceProjection{}
		receipt, err := api.SyncProjectProjection(ctx, unregistered)
		if err != nil || receipt.Replayed || receipt.ProjectReplayed {
			t.Fatalf("unregistered transition projection = %#v, err=%v", receipt, err)
		}
		assertNoCurrentRepositoryProjection(t, ctx, api, projectionTestRepoOne)
		var sourceRevision, projectionRevision int64
		if err := pool.QueryRow(ctx, `
			SELECT source_revision, projection_revision
			FROM provenance.project_projection_snapshots
			WHERE id=$1::uuid
		`, string(receipt.ProjectSnapshotID)).Scan(&sourceRevision, &projectionRevision); err != nil {
			t.Fatal(err)
		}
		if sourceRevision != 0 || projectionRevision != 2 {
			t.Fatalf("unregistered current project snapshot = source=%d projection=%d, want source=0 projection=2", sourceRevision, projectionRevision)
		}
		var historicalRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.repository_projection_snapshots WHERE project_id=$1`, projectionTestProjectID).Scan(&historicalRows); err != nil {
			t.Fatal(err)
		}
		if historicalRows != 2 {
			t.Fatalf("unregistered transition rewrote repository history: rows=%d, want 2", historicalRows)
		}
	})
}

func TestRepositoryProjectionReversionAndOwnershipTransferUseGlobalLatestRevisionPostgres(t *testing.T) {
	t.Run("A B A appends a new current revision", func(t *testing.T) {
		pool, store, _ := migratedStore(t, "project_projection_reversion")
		ctx := context.Background()
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		service, err := NewService(store, WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		api := &FoundationAPI{store: store, service: service}
		base := projectionTestSyncInput()
		base.Repositories = base.Repositories[:1]
		first, err := api.SyncProjectProjection(ctx, base)
		if err != nil || receiptRepositorySourceVersion(first, projectionTestRepoOne) != 1 {
			t.Fatalf("first A projection = %#v, err=%v", first, err)
		}
		changed := projectionTestSyncInput()
		changed.Repositories = changed.Repositories[:1]
		changed.Repositories[0].Extraction.DiscoveryFields[10].Value = "sha256:" + strings.Repeat("e", 64)
		second, err := api.SyncProjectProjection(ctx, changed)
		if err != nil || receiptRepositorySourceVersion(second, projectionTestRepoOne) != 2 {
			t.Fatalf("B projection = %#v, err=%v", second, err)
		}
		final, err := api.SyncProjectProjection(ctx, base)
		if err != nil || final.Replayed || receiptRepositorySourceVersion(final, projectionTestRepoOne) != 3 {
			t.Fatalf("final A projection = %#v, err=%v", final, err)
		}
		card, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne)
		if err != nil || card.Freshness.SourceDigest == nil || *card.Freshness.SourceDigest != "sha256:"+strings.Repeat("c", 64) {
			t.Fatalf("current repository did not return final A: %#v, err=%v", card.Freshness, err)
		}
		var history int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.repository_projection_snapshots WHERE repository_id=$1`, projectionTestRepoOne).Scan(&history); err != nil {
			t.Fatal(err)
		}
		if history != 3 {
			t.Fatalf("A-B-A history rows=%d, want 3", history)
		}
	})

	t.Run("ownership transfer orders globally stable repository IDs", func(t *testing.T) {
		pool, store, _ := migratedStore(t, "project_projection_transfer")
		ctx := context.Background()
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		service, err := NewService(store, WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		api := &FoundationAPI{store: store, service: service}
		ownerA := projectionTestSyncInput()
		ownerA.Repositories = ownerA.Repositories[:1]
		first, err := api.SyncProjectProjection(ctx, ownerA)
		if err != nil || receiptRepositorySourceVersion(first, projectionTestRepoOne) != 1 {
			t.Fatalf("owner A projection = %#v, err=%v", first, err)
		}
		ownerB := projectionTestSyncInput()
		ownerB.Project.Project.ProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FB2"
		ownerB.Project.Project.Name = "Beta"
		ownerB.Project.Project.Slug = "beta"
		ownerB.Project.Source.SourceRevision = 1
		ownerB.Project.Source.SemanticDigest = "sha256:" + strings.Repeat("e", 64)
		ownerB.Repositories = ownerB.Repositories[:1]
		ownerB.Repositories[0].Source.Projection.RepositoryOwnerProjectID = ownerB.Project.Project.ProjectID
		ownerB.Repositories[0].Source.ProjectSlug = "beta"
		second, err := api.SyncProjectProjection(ctx, ownerB)
		if err != nil || receiptRepositorySourceVersion(second, projectionTestRepoOne) != 2 {
			t.Fatalf("owner B projection = %#v, err=%v", second, err)
		}
		listed, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{})
		if err != nil || len(listed.Items) != 1 || listed.Items[0].OwningProject.ProjectID != ownerB.Project.Project.ProjectID || listed.Items[0].OwningProject.Slug != "beta" {
			t.Fatalf("global current ownership list = %#v, err=%v", listed, err)
		}
		got, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne)
		if err != nil || got.OwningProject.ProjectID != ownerB.Project.Project.ProjectID {
			t.Fatalf("global current ownership get = %#v, err=%v", got.OwningProject, err)
		}
		var history int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.repository_projection_snapshots WHERE repository_id=$1`, projectionTestRepoOne).Scan(&history); err != nil {
			t.Fatal(err)
		}
		if history != 2 {
			t.Fatalf("ownership transfer rewrote owner A history: rows=%d, want 2", history)
		}
	})
}

func TestProjectProjectionSourcePosturesFailClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		input ProjectProjectionSyncInput
		want  bool
	}{
		{
			name: "registered semantic digest missing",
			input: func() ProjectProjectionSyncInput {
				input := projectionTestSyncInput()
				input.Project.Source.SemanticDigest = ""
				return input
			}(),
		},
		{
			name: "registered semantic digest malformed",
			input: func() ProjectProjectionSyncInput {
				input := projectionTestSyncInput()
				input.Project.Source.SemanticDigest = "sha256:not-a-digest"
				return input
			}(),
		},
		{
			name: "unregistered explicit empty project",
			input: func() ProjectProjectionSyncInput {
				input := projectionTestSyncInput()
				input.Project.Source = projectstate.SourceProjection{Posture: projectstate.SourcePostureNotRegistered}
				input.Project.Members = []projectstate.RepositoryProjection{}
				input.Repositories = []repostate.ProvenanceProjection{}
				return input
			}(),
			want: true,
		},
		{
			name: "unregistered revision is inconsistent",
			input: func() ProjectProjectionSyncInput {
				input := projectionTestSyncInput()
				input.Project.Source = projectstate.SourceProjection{Posture: projectstate.SourcePostureNotRegistered, SourceRevision: 1}
				input.Project.Members = []projectstate.RepositoryProjection{}
				input.Repositories = []repostate.ProvenanceProjection{}
				return input
			}(),
		},
		{
			name: "unregistered members are inconsistent",
			input: func() ProjectProjectionSyncInput {
				input := projectionTestSyncInput()
				input.Project.Source = projectstate.SourceProjection{Posture: projectstate.SourcePostureNotRegistered}
				input.Project.Members = []projectstate.RepositoryProjection{{RepositoryID: projectionTestRepoOne}}
				input.Repositories = []repostate.ProvenanceProjection{}
				return input
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, _, err := prepareProjectProjectionSync(test.input, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))
			if test.want {
				if err != nil {
					t.Fatal(err)
				}
				if project.SourceSchemaVersion != unregisteredProjectProjectionSourceSchemaVersion || !validProjectionDigest(project.SourceDigest) {
					t.Fatalf("unregistered source was not explicitly projected: %#v", project)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid project source posture was accepted")
			}
		})
	}
}

func TestRepositoryDeclaredStateListsPreserveDeclarationOrder(t *testing.T) {
	makeCard := func() RepositoryCard {
		return RepositoryCard{FieldSources: repositoryDefaultFieldSources()}
	}
	apply := func(values []string) RepositoryCard {
		card := makeCard()
		applyRepositoryDiscovery(&card, repostate.Extraction{DiscoveryFields: []repostate.ExtractedField{
			{Name: "aliases", Value: []string{"zulu", "alpha", "zulu"}},
			{Name: "topics", Value: []string{"zebra", "alpha", "zebra"}},
			{Name: "active_focus", Value: values},
			{Name: "recent_outcomes", Value: values},
			{Name: "next_priorities", Value: values},
			{Name: "blockers", Value: values},
		}})
		return card
	}
	first := apply([]string{"second", "first", "second"})
	second := apply([]string{"first", "second", "first"})
	for name, got := range map[string][]string{
		"active_focus": first.ActiveFocus, "recent_outcomes": first.RecentOutcomes,
		"next_priorities": first.NextPriorities, "blockers": first.Blockers,
	} {
		if !reflect.DeepEqual(got, []string{"second", "first"}) {
			t.Fatalf("%s declaration order = %v, want [second first]", name, got)
		}
	}
	for name, got := range map[string][]string{
		"active_focus": second.ActiveFocus, "recent_outcomes": second.RecentOutcomes,
		"next_priorities": second.NextPriorities, "blockers": second.Blockers,
	} {
		if !reflect.DeepEqual(got, []string{"first", "second"}) {
			t.Fatalf("%s opposing declaration order = %v, want [first second]", name, got)
		}
	}
	if !reflect.DeepEqual(first.Aliases, []string{"alpha", "zulu"}) || !reflect.DeepEqual(first.Topics, []string{"alpha", "zebra"}) {
		t.Fatalf("canonical alias/topic sorting changed: aliases=%v topics=%v", first.Aliases, first.Topics)
	}
}

func TestProjectionMigrationBindsRepositoryParentsAndRejectsInvalidTypedIDsPostgres(t *testing.T) {
	pool, _, _ := migratedStore(t, "project_projection_constraints")
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	projectOne := projectionTestProjectID
	projectTwo := "project_01ARZ3NDEKTSV4RRFFQ69G5FB2"
	insertProjectProjectionFixture(t, ctx, pool, "33333333-3333-4333-8333-333333333333", projectOne, now)
	insertProjectProjectionFixture(t, ctx, pool, "44444444-4444-4444-8444-444444444444", projectTwo, now)
	insertRepositoryProjectionFixture(t, ctx, pool, "55555555-5555-4555-8555-555555555555", "33333333-3333-4333-8333-333333333333", projectOne, projectionTestRepoOne, now)
	if _, err := pool.Exec(ctx, `
		INSERT INTO provenance.repository_projection_snapshots(
			id, project_snapshot_id, project_id, repository_id, source_revision,
			source_identity_digest, snapshot_digest, tracking_status, searchable_summary, card_json, created_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, 1, $5, $6, 'valid', '', '{}'::jsonb, $7)
	`, "66666666-6666-4666-8666-666666666666", "33333333-3333-4333-8333-333333333333", projectTwo,
		projectionTestRepoTwo, projectionTestDigest("e"), projectionTestDigest("f"), now); err == nil {
		t.Fatal("repository snapshot accepted another project's parent snapshot")
	}

	projectULID := strings.TrimPrefix(projectOne, "project_")
	for _, invalid := range []string{
		"project_" + projectULID[:25] + "I",
		"project_8" + projectULID[1:],
		"project_" + strings.ToLower(projectULID),
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO provenance.project_projection_snapshots(
				id, project_id, projection_revision, source_revision, source_identity_digest, source_digest, source_schema_version,
				snapshot_digest, observed_at, searchable_summary, projection_json, created_at
			) VALUES ($1::uuid, $2, 7, 7, $3, $4, 'fixture', $5, $6, '', '{}'::jsonb, $6)
		`, projectionFixtureID(invalid), invalid, projectionTestDigest("a"), projectionTestDigest("b"), projectionTestDigest("c"), now); err == nil {
			t.Fatalf("invalid project id %q passed the durable boundary", invalid)
		}
	}
	repositoryULID := strings.TrimPrefix(projectionTestRepoTwo, "repo_")
	for _, invalid := range []string{
		"repo_" + repositoryULID[:25] + "O",
		"repo_8" + repositoryULID[1:],
		"repo_" + strings.ToLower(repositoryULID),
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO provenance.repository_projection_snapshots(
				id, project_snapshot_id, project_id, repository_id, source_revision,
				source_identity_digest, snapshot_digest, tracking_status, searchable_summary, card_json, created_at
			) VALUES ($1::uuid, $2::uuid, $3, $4, 9, $5, $6, 'valid', '', '{}'::jsonb, $7)
		`, projectionFixtureID(invalid), "33333333-3333-4333-8333-333333333333", projectOne, invalid,
			projectionTestDigest("c"), projectionTestDigest("d"), now); err == nil {
			t.Fatalf("invalid repository id %q passed the durable boundary", invalid)
		}
	}
}

func receiptRepositorySourceVersion(receipt ProjectProjectionSyncReceipt, repositoryID string) int64 {
	for _, item := range receipt.Repositories {
		if item.RepositoryID == repositoryID {
			return item.SourceVersion
		}
	}
	return 0
}

func assertNoCurrentRepositoryProjection(t *testing.T, ctx context.Context, api *FoundationAPI, repositoryID string) {
	t.Helper()
	listed, err := api.ListRepositoryProjections(ctx, RepositoryProjectionListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("removed repository remained in current list: %#v", listed.Items)
	}
	if _, err := api.GetRepositoryProjection(ctx, repositoryID); !errors.Is(err, ErrRepositoryProjectionNotFound) {
		t.Fatalf("removed repository exact get error = %v, want %v", err, ErrRepositoryProjectionNotFound)
	}
}

func projectionTestDigest(character string) string {
	return "sha256:" + character + strings.Repeat("0", 63)
}

func projectionFixtureID(seed string) string {
	return string(stableProjectionID("fixture", seed))
}

func projectionSnapshotWithChangedPurpose(t *testing.T, snapshot repositorySnapshot, purpose string) repositorySnapshot {
	t.Helper()
	var card RepositoryCard
	if err := json.Unmarshal(snapshot.CardJSON, &card); err != nil {
		t.Fatal(err)
	}
	card.Purpose = purpose
	payload, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := deterministicRepositoryCardDigest(card)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.CardJSON = payload
	snapshot.SnapshotDigest = digest
	return snapshot
}

func repositoryIdentityForProject(t *testing.T, project projectSnapshot, projectID SemanticID, projectionRevision int64, snapshot repositorySnapshot) string {
	t.Helper()
	project.ID = projectID
	project.ProjectionRevision = projectionRevision
	identity, err := repositoryProjectionSourceIdentity(project, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func insertProjectProjectionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, projectID string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO provenance.project_projection_snapshots(
			id, project_id, projection_revision, source_revision, source_identity_digest, source_digest, source_schema_version,
			snapshot_digest, observed_at, searchable_summary, projection_json, created_at
		) VALUES ($1::uuid, $2, 1, 1, $3, $4, 'fixture', $5, $6, '', '{}'::jsonb, $6)
	`, id, projectID, projectionTestDigest("a"), projectionTestDigest("b"), projectionTestDigest("c"), now); err != nil {
		t.Fatal(err)
	}
}

func insertRepositoryProjectionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, projectSnapshotID, projectID, repositoryID string, now time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO provenance.repository_projection_snapshots(
			id, project_snapshot_id, project_id, repository_id, source_revision,
			source_identity_digest, snapshot_digest, tracking_status, searchable_summary, card_json, created_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, 1, $5, $6, 'valid', '', '{}'::jsonb, $7)
	`, id, projectSnapshotID, projectID, repositoryID, projectionTestDigest("c"), projectionTestDigest("d"), now); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryTrackingStatusesRemainVisible(t *testing.T) {
	statuses := []struct {
		name string
		item repostate.ProvenanceProjection
		want RepositoryTrackingStatus
	}{
		{name: "not enabled", item: projectionTestRepository(projectionTestRepoOne, "one", 3, repostate.TrackingNotEnabled), want: RepositoryTrackingNotEnabled},
		{name: "stale", item: projectionTestRepository(projectionTestRepoOne, "one", 3, repostate.TrackingStaleVersion), want: RepositoryTrackingStale},
		{name: "malformed", item: projectionTestRepository(projectionTestRepoOne, "one", 3, repostate.TrackingMalformed), want: RepositoryTrackingMalformed},
		{name: "owner mismatch", item: projectionTestRepository(projectionTestRepoOne, "one", 3, repostate.TrackingMismatchedOwner), want: RepositoryTrackingMismatchedOwner},
		{name: "not observed", item: repostate.ProvenanceProjection{Source: projectionTestRepository(projectionTestRepoOne, "one", 3, repostate.TrackingValid).Source, Available: false, ReasonCode: "member_path_missing"}, want: RepositoryTrackingNotObserved},
		{name: "remote unavailable", item: repostate.ProvenanceProjection{Source: projectstate.ProvenanceRepositorySource{Projection: projectstate.RepositoryProjection{RepositoryID: projectionTestRepoOne, ObservationPosture: projects.ProjectRepositoryObservationRemoteUnavailable}, Owned: true}, Available: false, ReasonCode: "remote_unavailable"}, want: RepositoryTrackingRemoteUnavailable},
	}
	for _, test := range statuses {
		t.Run(test.name, func(t *testing.T) {
			if got := repositoryTrackingStatus(test.item); got != test.want {
				t.Fatalf("tracking status = %q, want %q", got, test.want)
			}
		})
	}
}

func projectionTestSyncInput() ProjectProjectionSyncInput {
	now := time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC)
	return ProjectProjectionSyncInput{
		Project: projectstate.ProjectProjection{
			SchemaVersion: projectstate.SchemaVersion,
			Project: projectstate.ProjectIdentityProjection{
				ProjectID: projectionTestProjectID, Name: "Atlas", Slug: "atlas", Lifecycle: "active",
			},
			Source: projectstate.SourceProjection{
				Posture: projectstate.SourcePostureRegistered, SourceRevision: 12,
				SemanticDigest: "sha256:" + strings.Repeat("a", 64), OwnerNode: "main",
			},
			ObservedAt: now,
		},
		Repositories: []repostate.ProvenanceProjection{
			projectionTestRepository(projectionTestRepoOne, "atlas-search", 4, repostate.TrackingValid),
			projectionTestRepository(projectionTestRepoTwo, "atlas-runtime", 5, repostate.TrackingNotEnabled),
		},
	}
}

func projectionTestRepository(repositoryID, key string, sourceVersion int64, status repostate.TrackingStatus) repostate.ProvenanceProjection {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	commit := strings.Repeat("b", 40)
	sourceDigest := "sha256:" + strings.Repeat("c", 64)
	extraction := repostate.Extraction{
		TrackingStatus: status,
		Manifest: &repostate.RepositoryManifest{
			SchemaVersion: repostate.RepositorySchemaVersion,
		},
		ManifestSource: repostate.SourceMetadata{Digest: sourceDigest},
		Freshness:      repostate.Freshness{ObservedAt: now, SourceCommit: commit},
		DiscoveryFields: []repostate.ExtractedField{
			{Name: "name", Value: key, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "aliases", Value: []string{"search", key}, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "role", Value: "component", Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "purpose", Value: "Bounded deterministic provenance tests", Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "topics", Value: []string{"provenance", "search"}, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "current_state", Value: "Validated", Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "active_focus", Value: []string{"projection"}, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "recent_outcomes", Value: []string{"schema"}, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "next_priorities", Value: []string{"review"}, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "blockers", Value: []string{}, Source: repostate.SourceMetadata{Posture: repostate.PostureDeclared}},
			{Name: "source_digest", Value: sourceDigest, Source: repostate.SourceMetadata{Posture: repostate.PostureDerived}},
		},
	}
	if status == repostate.TrackingNotEnabled {
		extraction.Manifest = nil
		extraction.Freshness = repostate.Freshness{}
	}
	return repostate.ProvenanceProjection{
		Source: projectstate.ProvenanceRepositorySource{
			Projection: projectstate.RepositoryProjection{
				RepositoryID: repositoryID, RepositoryOwnerProjectID: projectionTestProjectID,
				Key: key, Role: projects.ProjectRepositoryRoleComponent,
				RepositoryLifecycle: projects.RepositoryLifecycleActive,
				SourceBindingDigest: "sha256:" + strings.Repeat("d", 64),
				Git:                 &projectstate.GitProjection{CurrentBranch: "main"},
			},
			ProjectSlug: "atlas", SourceVersion: sourceVersion, RepositoryRoot: "/private/loom/project/repos/" + key,
			Available: true, Owned: true,
		},
		Extraction: extraction, Available: true,
	}
}

type projectionAdapterGitObserver struct {
	root         string
	sourceCommit string
	observedAt   time.Time
}

func (observer *projectionAdapterGitObserver) Observe(context.Context, string) (repostate.GitObservation, error) {
	paths := []string{
		".repo/repo.yaml", ".repo/README.md", ".repo/REPOSITORY.md", ".repo/STATE.md", ".repo/ROADMAP.md",
	}
	entries := make([]repostate.GitEntry, 0, len(paths))
	for _, relative := range paths {
		payload, err := os.ReadFile(filepath.Join(observer.root, relative))
		if err != nil {
			return repostate.GitObservation{}, err
		}
		entries = append(entries, repostate.GitEntry{Path: relative, Mode: "100644", Content: payload})
	}
	return repostate.GitObservation{
		ObservedAt: observer.observedAt, HeadCommit: observer.sourceCommit, SourceCommit: observer.sourceCommit,
		LatestRepoCommitAt: observer.observedAt.Add(-time.Hour), TrackedState: repostate.GitTrackedClean,
		Entries: entries, UntrackedPaths: []string{},
	}, nil
}

func writeProjectionAdapterState(t *testing.T, root, currentState string) {
	t.Helper()
	files := map[string]string{
		".repo/repo.yaml": `kind: loom.repository_state
schema_version: repo.state.v1
repository:
  id: repo_01ARZ3NDEKTSV4RRFFQ69G5FB0
  name: Atlas API
  aliases: [atlas-api, atlas]
  role: component
  purpose: Serve the Atlas product API.
  topics: [api, atlas]
owner_project:
  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV
  slug: atlas
branches:
  stable: main
  default: main
source:
  tracking: git
  state_root: .repo
`,
		".repo/README.md":     "# Repository State\n",
		".repo/REPOSITORY.md": "# Atlas API\n",
		".repo/STATE.md":      "# State\n\n## Current State\n\n" + currentState + "\n\n## Active Focus\n\n- First focus\n- Second focus\n\n## Recent Outcomes\n\n- First outcome\n\n## Blockers\n\n- None\n",
		".repo/ROADMAP.md":    "# Roadmap\n\n## Next Priorities\n\n1. First priority\n2. Second priority\n",
	}
	for relative, payload := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
