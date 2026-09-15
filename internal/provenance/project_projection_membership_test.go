package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"loom.local/loom/internal/projectstate"
)

func projectMembershipTestInput() ProjectProjectionSyncInput {
	input := projectDevelopmentTestInput("Lexical retrieval")
	input.Repositories = projectionTestSyncInput().Repositories[:1]
	input.Repositories[0].Source.Projection.RelativeSource = "repos/atlas-search"
	input.Repositories[0].Source.Projection.StateRoot = ".repo"
	input.Project.Members = []projectstate.RepositoryProjection{input.Repositories[0].Source.Projection}
	return input
}

func TestProjectContextOnlyRequiresExactMembershipEvidence(t *testing.T) {
	input := projectMembershipTestInput()
	bindings, err := projectRepositoryBindings(input.Project)
	if err != nil {
		t.Fatal(err)
	}
	previous := ProjectContextProjection{ProjectID: input.Project.Project.ProjectID, Memberships: bindings}
	if len(unchangedProjectBindings(previous, previous)) != 1 {
		t.Fatal("unchanged owned binding was not eligible")
	}
	for _, test := range []struct {
		name   string
		change func(*ProjectRepositoryBinding)
	}{
		{"repository", func(b *ProjectRepositoryBinding) { b.RepositoryID = projectionTestRepoTwo }},
		{"owner", func(b *ProjectRepositoryBinding) { b.OwnerProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FB0" }},
		{"key", func(b *ProjectRepositoryBinding) { b.Key = "renamed" }},
		{"role", func(b *ProjectRepositoryBinding) { b.Role = "primary" }},
		{"path", func(b *ProjectRepositoryBinding) { b.Path = "repos/other" }},
		{"state root", func(b *ProjectRepositoryBinding) { b.StateRoot = ".repo-other" }},
		{"digest", func(b *ProjectRepositoryBinding) { b.BindingDigest = digestProjection([]byte("new binding")) }},
		{"membership lifecycle", func(b *ProjectRepositoryBinding) { b.MembershipLifecycle = "archived" }},
		{"repository lifecycle", func(b *ProjectRepositoryBinding) { b.RepositoryLifecycle = "archived" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := bindings[0]
			test.change(&changed)
			next := ProjectContextProjection{ProjectID: previous.ProjectID, Memberships: []ProjectRepositoryBinding{changed}}
			if len(unchangedProjectBindings(previous, next)) != 0 {
				t.Fatal("changed binding inherited old capture")
			}
		})
	}
	legacy := ProjectContextProjection{ProjectID: previous.ProjectID}
	if len(unchangedProjectBindings(legacy, previous)) != 0 || len(unchangedProjectBindings(previous, legacy)) != 0 {
		t.Fatal("new, removed, or unproven membership inherited a capture")
	}
	legacyInput := projectionTestSyncInput()
	legacySnapshot, _, err := prepareProjectProjectionSync(legacyInput, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	legacyInput.ContextOnly, legacyInput.Repositories = true, nil
	emptySnapshot, _, err := prepareProjectProjectionSync(legacyInput, time.Now())
	if err != nil || emptySnapshot.SnapshotDigest == legacySnapshot.SnapshotDigest {
		t.Fatalf("authoritative empty membership replayed unknown legacy membership: %v", err)
	}
	input.ContextOnly = true
	if _, _, err := prepareProjectProjectionSync(input, time.Now()); err == nil {
		t.Fatal("context-only sync accepted new repository observations")
	}
	input.ContextOnly = false
	input.Repositories[0].Source.Projection.RelativeSource = "repos/different-capture"
	if _, _, err := prepareProjectProjectionSync(input, time.Now()); err == nil {
		t.Fatal("manual capture was not bound to the same project membership")
	}
}

func TestProjectContextOnlyRebindPreservesRepositoryCapture(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	input := projectMembershipTestInput()
	parent, repositories, err := prepareProjectProjectionSync(input, now)
	if err != nil {
		t.Fatal(err)
	}
	var original RepositoryCard
	if err := json.Unmarshal(repositories[0].CardJSON, &original); err != nil {
		t.Fatal(err)
	}
	current, err := decodeProjectContext(parent.ProjectionJSON, parent.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	parent.ID = stableProjectionID("next parent")
	current.Lifecycle = "archived"
	current.Name = "Atlas renamed"
	rebound, err := reboundRepositorySnapshot(original, current.Memberships[0], parent, current)
	if err != nil {
		t.Fatal(err)
	}
	var captured RepositoryCard
	if err := json.Unmarshal(rebound.CardJSON, &captured); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(captured.Freshness, original.Freshness) || captured.OwningProject.Lifecycle != "archived" || captured.OwningProject.Name != "Atlas renamed" {
		t.Fatalf("rebind altered Git/freshness or omitted parent context: %#v", captured)
	}
	captured.OwningProject = original.OwningProject
	if !reflect.DeepEqual(original, captured) {
		t.Fatal("rebind changed repository capture fields")
	}
	if rebound.ProjectSnapshotID != parent.ID || !reflect.DeepEqual(rebound.ObservedAt, original.Freshness.ObservedAt) || !reflect.DeepEqual(rebound.ObservedCommit, original.Freshness.ObservedCommit) {
		t.Fatal("rebind fabricated a new repository observation")
	}
}

func TestProjectContextOnlyRejectsOlderObservation(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	previous := ProjectProjectionSnapshot{SnapshotID: stableProjectionID("current"), SourceRevision: 12,
		SourceDigest: digestProjection([]byte("binding")), SnapshotDigest: digestProjection([]byte("current state")),
		SourceSchemaVersion: projectstate.SchemaVersion, ObservedAt: now}
	base := projectSnapshot{SourceRevision: previous.SourceRevision, SourceDigest: previous.SourceDigest,
		SnapshotDigest: previous.SnapshotDigest, SourceSchemaVersion: previous.SourceSchemaVersion, ObservedAt: now}
	for _, test := range []struct {
		name     string
		change   func(*projectSnapshot)
		conflict bool
	}{
		{"older registration", func(p *projectSnapshot) { p.SourceRevision--; p.ObservedAt = now.Add(time.Minute) }, true},
		{"older changed capture", func(p *projectSnapshot) {
			p.ObservedAt = now.Add(-time.Minute)
			p.SnapshotDigest = digestProjection([]byte("older state"))
		}, true},
		{"older identical replay", func(p *projectSnapshot) { p.ObservedAt = now.Add(-time.Minute) }, false},
		{"newer capture same registration", func(p *projectSnapshot) {
			p.ObservedAt = now.Add(time.Minute)
			p.SnapshotDigest = digestProjection([]byte("changed state"))
		}, false},
		{"newer registration", func(p *projectSnapshot) { p.SourceRevision++; p.ObservedAt = now.Add(-time.Minute) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := base
			test.change(&next)
			err := validateContextOnlyObservation(previous, next)
			var conflict *FoundationConflictError
			if errors.As(err, &conflict) != test.conflict {
				t.Fatalf("stale observation conflict=%t: %v", test.conflict, err)
			}
		})
	}
	if err := validateContextOnlyObservation(ProjectProjectionSnapshot{}, base); err != nil {
		t.Fatalf("initial observation rejected: %v", err)
	}
}

func TestProjectContextOnlySearchAndHistoryPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "project_context_capture")
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	service, err := NewService(store, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	input := projectMembershipTestInput()
	first, err := api.SyncProjectProjection(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne)
	if err != nil {
		t.Fatal(err)
	}
	input.ContextOnly, input.Repositories = true, nil
	unchanged, err := api.SyncProjectProjection(ctx, input)
	if err != nil || !unchanged.Replayed || len(unchanged.Repositories) != 1 {
		t.Fatalf("first automatic poll churned manual captures: %#v %v", unchanged, err)
	}
	input.Project.Development = projectDevelopmentTestInput("Changed lexical retrieval").Project.Development
	now = now.Add(time.Minute)
	changed, err := api.SyncProjectProjection(ctx, input)
	if err != nil || changed.ProjectSnapshotID == first.ProjectSnapshotID || len(changed.Repositories) != 1 || changed.ProjectSourceVersion != first.ProjectSourceVersion {
		t.Fatalf("context rebind: %#v %v", changed, err)
	}
	retained, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne)
	if err != nil || !reflect.DeepEqual(retained, original) {
		t.Fatalf("context refresh lost or changed prior repository capture: %#v %v", retained, err)
	}
	input.Project.ObservedAt = input.Project.ObservedAt.Add(time.Hour)
	replay, err := api.SyncProjectProjection(ctx, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("unchanged automatic poll churned: %#v %v", replay, err)
	}
	page, err := api.Search(ctx, SearchRequest{Query: "Atlas", Limit: 1})
	if err != nil || len(page.RepositoryState.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("repository/project collection boundary: %#v %v", page, err)
	}
	page, err = api.Search(ctx, SearchRequest{Query: "Atlas", Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(page.ProjectState.Items) != 1 || page.ProjectState.Items[0].SnapshotID != changed.ProjectSnapshotID {
		t.Fatalf("project collection continuation: %#v %v", page, err)
	}
	historical, err := api.GetProjectProjection(ctx, first.ProjectID, first.ProjectSnapshotID)
	if err != nil || historical.Context.Development.CurrentFocus != "Lexical retrieval" {
		t.Fatalf("historical exact capture changed: %#v %v", historical, err)
	}
	input.Project.Members[0].RelativeSource = "repos/moved"
	if _, err := api.SyncProjectProjection(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne); !errors.Is(err, ErrRepositoryProjectionNotFound) {
		t.Fatalf("changed membership inherited stale card: %v", err)
	}
	filtered, err := api.Search(ctx, SearchRequest{Query: "lexical", Repository: projectionTestRepoOne, Collections: []SearchCollection{SearchCollectionProjectState}})
	if err != nil || len(filtered.ProjectState.Items) != 1 {
		t.Fatalf("unobserved registered member lost project search: %#v %v", filtered, err)
	}
	input.Project.Members = nil
	if _, err := api.SyncProjectProjection(ctx, input); err != nil {
		t.Fatal(err)
	}
	filtered, err = api.Search(ctx, SearchRequest{Query: "lexical", Repository: projectionTestRepoOne, Collections: []SearchCollection{SearchCollectionProjectState}})
	if err != nil || len(filtered.ProjectState.Items) != 0 {
		t.Fatalf("removed membership still matched project filter: %#v %v", filtered, err)
	}
	zeroRepo, err := api.Search(ctx, SearchRequest{Query: "lexical", Project: first.ProjectID, Collections: []SearchCollection{SearchCollectionProjectState}})
	if err != nil || len(zeroRepo.ProjectState.Items) != 1 {
		t.Fatalf("zero-repository project is not searchable: %#v %v", zeroRepo, err)
	}
	input.Project.Development = projectDevelopmentTestInput("Lexical retrieval").Project.Development
	reverted, err := api.SyncProjectProjection(ctx, input)
	if err != nil || reverted.ProjectSnapshotID == first.ProjectSnapshotID {
		t.Fatalf("reversion reused historical snapshot: %#v %v", reverted, err)
	}
	// A manual sync remains the path to a fresh repository observation.
	manual := projectMembershipTestInput()
	if _, err := api.SyncProjectProjection(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetRepositoryProjection(ctx, projectionTestRepoOne); err != nil {
		t.Fatal(err)
	}
}
