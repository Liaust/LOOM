package automation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/requestctx"
)

func boxContext(t *testing.T, ids ...string) context.Context {
	t.Helper()
	return boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: ids})
}

type boxTestConnector struct{ overviewTestConnector }

func (c boxTestConnector) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c boxTestConnector) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return overviewTestTx{}, nil
}

func boxTestDB(t *testing.T, query func(string, []driver.NamedValue) (driver.Rows, error)) *sql.DB {
	t.Helper()
	db := sql.OpenDB(boxTestConnector{overviewTestConnector{t: t, query: query}})
	t.Cleanup(func() { db.Close() })
	return db
}

func TestScheduleBoxInventoryAndCursorBinding(t *testing.T) {
	ctx := boxContext(t, "box_company")
	queries := 0
	db := overviewTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		for _, want := range []string{"schedules.project_id IS NOT NULL", "box_project.project_id = schedules.project_id", "ELSE schedules.metadata_json->>'home_box_id'"} {
			if !strings.Contains(query, want) {
				t.Fatalf("counts/page predicate missing %q: %s", want, query)
			}
		}
		if args[0].Value != `["box_company"]` {
			t.Fatalf("wrong selection: %v", args)
		}
		if strings.HasPrefix(query, "SELECT") {
			return &overviewTestRows{values: [][]driver.Value{{int64(0), int64(2), int64(0), int64(2)}}}, nil
		}
		if strings.Index(query, "home_box_id") > strings.Index(query, " LIMIT ") {
			t.Fatal("page filtered after limit")
		}
		now := time.Now().UTC()
		return &overviewTestRows{values: [][]driver.Value{overviewTestValues("schedule_2", now), overviewTestValues("schedule_1", now)}}, nil
	})
	s := Service{DB: db}
	page, err := s.Overview(ctx, OverviewFilter{Limit: 1})
	if err != nil || page.Counts.Total != 2 || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("scoped overview: %+v %v", page, err)
	}
	for _, other := range []context.Context{
		boxContext(t, "box_personal"), boxContext(t, "box_company", "box_personal"),
		boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true, BoxIDs: []string{"box_company"}}), t.Context(),
	} {
		if _, err := s.Overview(other, OverviewFilter{Limit: 1, Cursor: page.NextCursor}); err == nil || !strings.Contains(err.Error(), "Box selection") {
			t.Fatalf("cross-selection replay accepted: %v", err)
		}
	}
	if queries != 2 {
		t.Fatalf("replay accessed storage: %d queries", queries)
	}
	// Legacy cursors remain usable only in their original unbound inventory.
	raw, _ := json.Marshal(overviewCursor{CreatedAt: time.Now().UTC(), ScheduleID: "schedule_1"})
	if _, err := s.Overview(ctx, OverviewFilter{Cursor: base64.RawURLEncoding.EncodeToString(raw)}); err == nil {
		t.Fatal("unbound legacy cursor admitted into scoped inventory")
	}
}

func TestScheduleBoxListsFilterBeforeLimit(t *testing.T) {
	ctx := boxContext(t, "box_company")
	for _, read := range []func(Service) error{
		func(s Service) error { _, e := s.ListSchedules(ctx, ScheduleFilter{Limit: 1}); return e },
		func(s Service) error { _, e := s.ListAutomations(ctx, AutomationFilter{Limit: 1}); return e },
		func(s Service) error { _, e := s.ListScheduleFires(ctx, ScheduleFireFilter{Limit: 1}); return e },
		func(s Service) error { _, e := s.ListInvocations(ctx, InvocationFilter{Limit: 1}); return e },
		func(s Service) error { _, e := s.ListInvocationFailures(ctx, InvocationFilter{Limit: 1}); return e },
	} {
		db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "home_box_id") || strings.Index(query, "home_box_id") > strings.Index(query, " LIMIT ") || args[len(args)-2].Value != `["box_company"]` {
				t.Fatalf("unscoped/late list predicate: %s %v", query, args)
			}
			return &overviewTestRows{}, nil
		})
		if err := read(Service{DB: db}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScheduleScopedReferencesRefuseBeforePayloadAndMutation(t *testing.T) {
	ctx := boxContext(t, "box_personal")
	for _, read := range []func(Service) error{
		func(s Service) error { _, e := s.GetSchedule(ctx, "other"); return e },
		func(s Service) error { _, e := s.GetAutomation(ctx, "other"); return e },
		func(s Service) error { _, e := s.GetScheduleFire(ctx, "other"); return e },
		func(s Service) error { _, e := s.GetInvocation(ctx, "other"); return e },
		func(s Service) error {
			_, e := s.PauseSchedule(ctx, requestctx.Context{}, "other", UpdateScheduleStatusInput{})
			return e
		},
		func(s Service) error {
			_, e := s.ResumeSchedule(ctx, requestctx.Context{}, "other", UpdateScheduleStatusInput{})
			return e
		},
		func(s Service) error {
			_, e := s.DisableSchedule(ctx, requestctx.Context{}, "other", UpdateScheduleStatusInput{})
			return e
		},
		func(s Service) error {
			_, e := s.FireScheduleNow(ctx, requestctx.Context{}, "other", FireScheduleInput{})
			return e
		},
		func(s Service) error {
			_, e := s.DispatchInvocationNow(ctx, requestctx.Context{}, "other", DispatcherRunInput{})
			return e
		},
		func(s Service) error {
			_, e := s.UpdateScheduleTiming(ctx, requestctx.Context{}, "other", ScheduleTimingInput{Cron: "0 * * * *", Timezone: "UTC", MisfirePolicy: MisfireMarkMissed})
			return e
		},
	} {
		queries := 0
		db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
			queries++
			if !strings.Contains(query, "home_box_id") || args[1].Value != `["box_personal"]` {
				t.Fatalf("unguarded reference: %s %v", query, args)
			}
			return &overviewTestRows{}, nil
		})
		if err := read(Service{DB: db}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cross-Box reference: %v", err)
		}
		if queries != 1 {
			t.Fatalf("read payload after refusal: %d", queries)
		}
	}
}

func TestEnsureScheduleKeyCannotModifyAnotherBox(t *testing.T) {
	ctx := boxContext(t, "box_company")
	queries := 0
	db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		if queries == 1 {
			if !strings.Contains(query, "WHERE schedule_key = $1") || !strings.Contains(query, "FOR UPDATE") {
				t.Fatalf("lost global key lock: %s", query)
			}
			return &overviewTestRows{values: [][]driver.Value{boxScheduleValues("box_personal")}}, nil
		}
		if queries != 2 || !strings.Contains(query, "COALESCE(NULLIF(") || args[1].Value != `["box_company"]` {
			t.Fatalf("mutation before key conflict guard: %s %v", query, args)
		}
		return &overviewTestRows{values: [][]driver.Value{{false}}}, nil
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := ensureScheduleTx(ctx, tx, requestctx.Context{}, normalizedCreateSchedule{ScheduleKey: "shared", Metadata: json.RawMessage(`{"home_box_id":"box_company"}`)}); err == nil {
		t.Fatal("global key conflict modified another Box")
	}
	if queries != 2 {
		t.Fatalf("guard query count: %d", queries)
	}
}

func TestScheduleMetadataCannotReassignOwnerAndEnsurePreservesMetadata(t *testing.T) {
	db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "SELECT COALESCE(true") {
			return &overviewTestRows{values: [][]driver.Value{{true}}}, nil
		}
		if !strings.Contains(query, "box_project.project_id = schedules.project_id") {
			t.Fatalf("unverified owner: %s", query)
		}
		return &overviewTestRows{values: [][]driver.Value{{"box_company"}}}, nil
	})
	project := "project_company"
	schedule := Schedule{ScheduleID: "schedule", ProjectID: &project, MetadataJSON: json.RawMessage(`{"custom":{"kept":true}}`)}
	for _, raw := range []string{`{"home_box_id":"box_personal"}`, `{"home_box_id":null}`} {
		if err := validateScheduleMetadata(t.Context(), db, schedule, json.RawMessage(raw)); err == nil {
			t.Fatalf("metadata changed ownership: %s", raw)
		}
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	normalized := normalizedCreateSchedule{ProjectID: project, Metadata: json.RawMessage(`{"home_box_id":"box_company","new":1}`)}
	if err := guardScheduleEnsure(t.Context(), tx, schedule, &normalized); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(normalized.Metadata), `"custom":{"kept":true}`) || !strings.Contains(string(normalized.Metadata), `"home_box_id":"box_company"`) {
		t.Fatalf("lost metadata: %s", normalized.Metadata)
	}
	normalized.ProjectID = "project_personal"
	if err := guardScheduleEnsure(t.Context(), tx, schedule, &normalized); err == nil {
		t.Fatal("ensure reassigned project")
	}
}

func TestScheduleNormalizationSavesVerifiedOwnership(t *testing.T) {
	for _, project := range []bool{false, true} {
		ctx := boxContext(t, "box_company")
		if project {
			ctx = t.Context()
		}
		db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
			value := "actor"
			switch {
			case strings.Contains(query, "capability_endpoint_id"):
				value = "endpoint"
			case strings.Contains(query, "SELECT scope_id"):
				value = "scope"
			case strings.Contains(query, "SELECT project_id FROM projects.projects"):
				if !project {
					return &overviewTestRows{}, nil
				}
				value = "project_company"
			case strings.Contains(query, "COALESCE(metadata->>'home_box_id'"):
				value = "box_company"
			}
			return &overviewTestRows{values: [][]driver.Value{{value}}}, nil
		})
		normalized, err := (Service{DB: db}).normalizeCreateSchedule(ctx, requestctx.Context{ScopeID: "scope", ActorID: "actor"}, CreateScheduleInput{ScheduleKey: "shared", TargetCapability: "main@test", ScheduleKind: "interval", ScheduleExpr: "1h", Metadata: json.RawMessage(`{"custom":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(normalized.Metadata), `"home_box_id":"box_company"`) || !strings.Contains(string(normalized.Metadata), `"custom":1`) {
			t.Fatalf("owner/metadata lost: %s", normalized.Metadata)
		}
		if project && normalized.ProjectID != "project_company" {
			t.Fatal("known project scope did not bind project")
		}
	}
}

func TestScheduleMainWidePredicateIncludesUnassigned(t *testing.T) {
	for _, ctx := range []context.Context{t.Context(), boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true})} {
		args := []any{}
		for _, predicate := range []string{scheduleBoxPredicate(ctx, &args), fireBoxPredicate(ctx, &args), invocationBoxPredicate(ctx, &args)} {
			if predicate != "true" || len(args) != 0 {
				t.Fatalf("Main-wide predicate excludes unassigned: %s %v", predicate, args)
			}
		}
	}
	// The cursor must bind selection mode even if catalog membership is equal.
	all := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true, BoxIDs: []string{"box_company"}, Digest: "same"})
	if jobs.BoxSelectionDigest(all) == jobs.BoxSelectionDigest(boxContext(t, "box_company")) {
		t.Fatal("AllBoxes digest aliases a scoped selection")
	}
}

func TestScheduleExecutionOwnerUsesPersistedStateNotSelection(t *testing.T) {
	ctx := boxContext(t, "box_personal")
	owner, err := scheduleInvocationOwner(ctx, nil, Schedule{}, Invocation{MetadataJSON: json.RawMessage(`{"home_box_id":"box_company"}`)})
	if err != nil || owner != "box_company" {
		t.Fatalf("saved invocation owner follows UI: %q %v", owner, err)
	}
	db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "jsonb_array_elements_text") || len(args) != 1 || args[0].Value != "schedule" {
			t.Fatalf("legacy schedule owner uses UI selection: %s %v", query, args)
		}
		return &overviewTestRows{values: [][]driver.Value{{"box_company"}}}, nil
	})
	owner, err = scheduleInvocationOwner(ctx, db, Schedule{ScheduleID: "schedule"}, Invocation{MetadataJSON: json.RawMessage(`{}`)})
	if err != nil || owner != "box_company" {
		t.Fatalf("legacy persisted owner: %q %v", owner, err)
	}
	metadata, err := jobs.BindBoxMetadata(jobs.WithBoxOwner(t.Context(), owner), nil, "", "", json.RawMessage(`{"routing":{"kept":true}}`))
	if err != nil || !strings.Contains(string(metadata), `"home_box_id":"box_company"`) || !strings.Contains(string(metadata), `"routing":{"kept":true}`) {
		t.Fatalf("queued job owner lost: %s %v", metadata, err)
	}
}

func TestScheduleStatusScopesEveryAggregate(t *testing.T) {
	for _, ctx := range []context.Context{boxContext(t, "box_personal"), t.Context(), boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{AllBoxes: true})} {
		selection, bound := boxregistry.FromContext(ctx)
		scoped := bound && !selection.AllBoxes
		queries := 0
		db := boxTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
			queries++
			if strings.Contains(query, "home_box_id") != scoped {
				t.Fatalf("status scope mismatch: %s", query)
			}
			if scoped && args[len(args)-1].Value != `["box_personal"]` {
				t.Fatalf("status uses wrong selection: %v", args)
			}
			switch queries {
			case 1:
				return &overviewTestRows{values: [][]driver.Value{{int64(1), int64(2), int64(3), int64(4), int64(1), nil}}}, nil
			case 2:
				if scoped && !strings.Contains(query, "schedules.schedule_id = schedule_fires.schedule_id") {
					t.Fatal("missed fires do not inherit schedule membership")
				}
				return &overviewTestRows{values: [][]driver.Value{{int64(5)}}}, nil
			case 3:
				if scoped && !strings.Contains(query, "invocations.project_id IS NOT NULL") {
					t.Fatal("invocations do not inherit verified membership")
				}
				return &overviewTestRows{values: [][]driver.Value{{int64(6), int64(7), nil}}}, nil
			default:
				t.Fatalf("unexpected status query: %s", query)
				return nil, nil
			}
		})
		status, err := (Service{DB: db}).ScheduleStatus(ctx)
		if err != nil || queries != 3 || status.ActiveScheduleCount != 1 || status.MissedFireCount != 5 || status.PendingInvocationCount != 6 || status.FailedInvocationCount != 7 {
			t.Fatalf("status aggregates: %+v %v", status, err)
		}
	}
}

func boxScheduleValues(owner string) []driver.Value {
	now := time.Now().UTC()
	return []driver.Value{"schedule", "automation", "shared", "Name", "", "paused", "interval", "1h", "UTC", nil, nil, nil, nil,
		[]byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), "actor", "actor", nil, nil, now, now, []byte(`{"home_box_id":"` + owner + `"}`)}
}
