package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/requestctx"
)

func selectedBox(t *testing.T, ids ...string) context.Context {
	t.Helper()
	return boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: ids})
}

func TestBoxPredicateAndActualSelectionDigest(t *testing.T) {
	for _, ctx := range []context.Context{t.Context(), boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true})} {
		args := []any{}
		if got := jobBoxPredicate(ctx, &args); got != "true" || len(args) != 0 {
			t.Fatalf("Main-wide inventory lost unassigned work: %s %v", got, args)
		}
	}
	args := []any{"existing"}
	query := jobBoxPredicate(selectedBox(t, "box_personal"), &args)
	for _, want := range []string{"project_scope_id = j.scope_id", "THEN (SELECT box_project.metadata->>'home_box_id'", "ELSE j.metadata->>'home_box_id'", "$2::jsonb", "NULLIF("} {
		if !strings.Contains(query, want) {
			t.Fatalf("membership predicate missing %q: %s", want, query)
		}
	}
	if args[1] != `["box_personal"]` {
		t.Fatalf("unsafe selection argument: %v", args)
	}
	args = nil
	if got := jobBoxPredicate(selectedBox(t), &args); got != "false" || len(args) != 0 {
		t.Fatalf("empty selection adopted unassigned jobs: %s", got)
	}
	ctx := selectedBox(t, "box_personal", "box_company")
	reversed := selectedBox(t, "box_company", "box_personal")
	all := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true, BoxIDs: []string{"box_personal", "box_company"}})
	if BoxSelectionDigest(ctx) != BoxSelectionDigest(reversed) || BoxSelectionDigest(ctx) == BoxSelectionDigest(all) || BoxSelectionDigest(all) == BoxSelectionDigest(t.Context()) {
		t.Fatal("selection digest trusts a membership digest or loses selection mode")
	}
}

func TestBindBoxMetadataNoProjectAndRetainedExecution(t *testing.T) {
	ctx := selectedBox(t, "box_company")
	got, err := BindBoxMetadata(ctx, nil, "", "", json.RawMessage(`{"custom":{"kept":true}}`))
	if err != nil || !strings.Contains(string(got), `"home_box_id":"box_company"`) || !strings.Contains(string(got), `"custom":{"kept":true}`) {
		t.Fatalf("no-project save: %s %v", got, err)
	}
	for _, raw := range []string{`{"home_box_id":"box_personal"}`, `{"home_box_id":null}`, `{"home_box_id":42}`, `[]`} {
		if _, err := BindBoxMetadata(ctx, nil, "", "", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted mismatched/invalid ownership: %s", raw)
		}
	}
	for _, ctx := range []context.Context{selectedBox(t), selectedBox(t, "box_personal", "box_company"), boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true})} {
		if _, err := BindBoxMetadata(ctx, nil, "", "", nil); err == nil {
			t.Fatal("ambiguous no-project creation accepted")
		}
	}
	got, err = BindBoxMetadata(t.Context(), nil, "", "", nil)
	if err != nil || string(got) != `{}` {
		t.Fatalf("internal system creation relabeled: %s %v", got, err)
	}
	got, err = BindBoxMetadata(WithBoxOwner(t.Context(), "box_company"), nil, "", "", nil)
	if err != nil || string(got) != `{"home_box_id":"box_company"}` {
		t.Fatalf("persisted execution owner lost: %s %v", got, err)
	}
	if _, err := BindBoxMetadata(WithBoxOwner(ctx, "box_personal"), nil, "", "", nil); err == nil {
		t.Fatal("UI selection overwrote retained execution owner")
	}
}

func TestBindBoxMetadataVerifiedProjects(t *testing.T) {
	for _, tc := range []struct {
		name, project, scope, owner, raw string
		ctx                              context.Context
		missing, reject                  bool
	}{
		{name: "unbound project snapshots", project: "project_company", owner: "box_company", ctx: t.Context()},
		{name: "unbound scope snapshots", scope: "scope_company", owner: "box_company", ctx: t.Context()},
		{name: "selected project", project: "project_company", owner: "box_company", ctx: selectedBox(t, "box_company")},
		{name: "cross Box", project: "project_personal", owner: "box_personal", ctx: selectedBox(t, "box_company"), reject: true},
		{name: "unassigned project stays unassigned", scope: "scope_legacy", ctx: t.Context()},
		{name: "Personal cannot adopt legacy", scope: "scope_legacy", ctx: selectedBox(t, "box_personal"), reject: true},
		{name: "metadata cannot reparent", project: "project_company", owner: "box_company", raw: `{"home_box_id":"box_personal"}`, ctx: t.Context(), reject: true},
		{name: "unknown scope selected", scope: "scope_system", owner: "box_company", ctx: selectedBox(t, "box_company"), missing: true},
		{name: "unknown project", project: "project_missing", ctx: t.Context(), missing: true, reject: true},
		{name: "snapshot contradicts project", scope: "scope_personal", owner: "box_personal", ctx: WithBoxOwner(t.Context(), "box_company"), reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, service := newOverviewTestService(t)
			rows := [][]driver.Value{{tc.owner}}
			if tc.missing {
				rows = nil
			}
			store.queries = []overviewTestQuery{{columns: 1, rows: rows, check: func(query string, args []driver.NamedValue) {
				if !strings.Contains(query, "projects.projects") || args[0].Value != tc.project || args[1].Value != tc.scope {
					t.Fatalf("not a verified project lookup: %s %v", query, args)
				}
			}}}
			got, err := BindBoxMetadata(tc.ctx, service.DB, tc.project, tc.scope, json.RawMessage(tc.raw))
			if (err != nil) != tc.reject {
				t.Fatalf("metadata=%s err=%v reject=%v", got, err, tc.reject)
			}
			if err == nil && tc.owner != "" && !strings.Contains(string(got), tc.owner) {
				t.Fatalf("project owner not saved: %s", got)
			}
		})
	}
}

func TestJobBoxFilteringBeforeLimitAndAcrossOverview(t *testing.T) {
	ctx := selectedBox(t, "box_company")
	for _, read := range []func(Service) error{
		func(s Service) error { _, e := s.ListJobs(ctx, ListFilter{Limit: 1}); return e },
		func(s Service) error { _, e := s.ListQueuedJobs(ctx, ListFilter{Limit: 1}); return e },
		func(s Service) error { _, e := s.ListFailedJobs(ctx, ListFilter{Limit: 1}); return e },
	} {
		store, s := newOverviewTestService(t)
		store.queries = []overviewTestQuery{{check: func(query string, args []driver.NamedValue) {
			if strings.Index(query, "home_box_id") > strings.Index(query, " LIMIT ") || args[len(args)-2].Value != `["box_company"]` {
				t.Fatalf("filter after limit or wrong selection: %s %v", query, args)
			}
		}}}
		if err := read(s); err != nil {
			t.Fatal(err)
		}
	}
	store, s := newOverviewTestService(t)
	for _, columns := range []int{3, 4, 15} {
		q := overviewTestQuery{columns: columns, check: func(query string, args []driver.NamedValue) {
			if !strings.Contains(query, "project_scope_id = j.scope_id") || !strings.Contains(query, "ELSE j.metadata->>'home_box_id'") {
				t.Fatalf("overview ownership mismatch: %s", query)
			}
		}}
		if columns == 3 {
			q.rows = [][]driver.Value{{int64(0), int64(0), int64(0)}}
		}
		store.queries = append(store.queries, q)
	}
	if _, err := s.Overview(ctx, OverviewFilter{Hours: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestJobScopedReferencesStopBeforePayloadAndMutations(t *testing.T) {
	ctx := selectedBox(t, "box_personal")
	for _, read := range []func(Service) error{
		func(s Service) error { _, e := s.GetJob(ctx, "other"); return e },
		func(s Service) error { _, e := s.ListJobLogs(ctx, "other"); return e },
		func(s Service) error { _, e := s.ListJobOutputs(ctx, "other"); return e },
		func(s Service) error {
			_, e := s.RetryJob(ctx, requestctx.Context{}, RetryJobInput{JobRef: "other"})
			return e
		},
		func(s Service) error {
			_, e := s.CancelJob(ctx, requestctx.Context{}, CancelJobInput{JobRef: "other"})
			return e
		},
		func(s Service) error {
			_, e := s.AcknowledgeJobAttention(ctx, requestctx.Context{}, JobAttentionInput{JobRef: "other"})
			return e
		},
		func(s Service) error {
			_, e := s.ArchiveJobAttention(ctx, requestctx.Context{}, JobAttentionInput{JobRef: "other"})
			return e
		},
	} {
		store, s := newOverviewTestService(t)
		store.queries = []overviewTestQuery{{check: func(query string, args []driver.NamedValue) {
			if !strings.Contains(query, "project_scope_id = j.scope_id") || args[1].Value != `["box_personal"]` {
				t.Fatalf("unguarded reference: %s %v", query, args)
			}
		}}}
		if err := read(s); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cross-Box ref: %v", err)
		}
	}
}

func TestJobBoxOwnerSurvivesDetachedRouting(t *testing.T) {
	for _, ctx := range []context.Context{t.Context(), selectedBox(t, "box_company"), selectedBox(t, "box_personal")} {
		store, s := newOverviewTestService(t)
		store.queries = []overviewTestQuery{{columns: 1, rows: [][]driver.Value{{"box_company"}}, check: func(query string, args []driver.NamedValue) {
			if !strings.Contains(query, "routing.capability_calls") || args[0].Value != "call" {
				t.Fatalf("not retained routing metadata: %s %v", query, args)
			}
		}}}
		metadata, err := bindJobBoxMetadata(ctx, s.DB, "", "call", json.RawMessage(`{"routing":{"capability_call_id":"call"}}`))
		selection, bound := boxregistry.FromContext(ctx)
		if bound && selection.Contains("box_personal") {
			if err == nil {
				t.Fatal("current UI selection overrode retained routing owner")
			}
			continue
		}
		if err != nil || !strings.Contains(string(metadata), `"home_box_id":"box_company"`) || !strings.Contains(string(metadata), `"capability_call_id":"call"`) {
			t.Fatalf("detached job lost ownership/metadata: %s %v", metadata, err)
		}
	}
}
