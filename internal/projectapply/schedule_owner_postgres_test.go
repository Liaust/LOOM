package projectapply

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	loomerrors "loom.local/loom/internal/errors"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

func TestDeclarationScheduleLifecyclePostgres(t *testing.T) {
	service, resolver, p, root, db := realOwnerFixture(t, 0)
	ctx := t.Context()
	req, err := requestctx.ResolveBootstrap(ctx, db, "schedule-declaration-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capabilities.SeedMainNodeRegistry(ctx, req, capabilities.NewService(db)); err != nil {
		t.Fatal(err)
	}
	service.owners[pc.DeclarationOwnerAutomation] = ScheduleOwner{Resolver: resolver}
	path := filepath.Join(root, pc.CanonicalRootContractPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc pc.ProjectDeclaration
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	spec := &pc.ScheduleDeclaration{Target: "main@system.health.read", Every: "5m", Status: "active", InputJSON: `{"exact":9007199254740993,"fraction":0.125}`}
	doc.Resources["check"] = pc.ResourceDeclaration{Kind: pc.DeclarationSchedule, Schedule: spec}
	once := &pc.ScheduleDeclaration{Target: spec.Target, At: time.Now().UTC().Add(time.Hour).Format(time.RFC3339), Status: "active"}
	doc.Resources["once"] = pc.ResourceDeclaration{Kind: pc.DeclarationSchedule, Schedule: once}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(key string) (pc.DeclarationApplyRequest, string) {
		t.Helper()
		plan, err := service.Plan(ctx, p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
		if err != nil {
			t.Fatal(err)
		}
		request := realApplyRequest(plan, root)
		request.IdempotencyKey = key
		result, err := service.Apply(ctx, p, request)
		if err != nil || result.State != pc.DeclarationOperationSucceeded {
			t.Fatalf("apply %s: %+v %v", key, result, err)
		}
		return request, result.OperationID
	}
	write()
	request, operationID := apply("create-schedules")
	native := automation.NewService(db, routing.NewService(db))
	get := func(key string) automation.Schedule {
		t.Helper()
		x, err := native.GetSchedule(ctx, pc.ProjectScheduleKey(doc.Project.Slug, key))
		if err != nil {
			t.Fatal(err)
		}
		return x.Schedule
	}
	first := get("check")
	if first.ProjectID == nil || *first.ProjectID != doc.Project.ID || first.ScopeID == nil || first.NextFireAt == nil {
		t.Fatal(first)
	}
	var number string
	if err := db.QueryRow(`SELECT input_json->>'exact' FROM automation.schedules WHERE schedule_id=$1`, first.ScheduleID).Scan(&number); err != nil || number != "9007199254740993" {
		t.Fatalf("numeric precision %s: %v", number, err)
	}
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM events.events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	request.OperationID = operationID
	if result, err := service.Apply(ctx, p, request); err != nil || result.OperationID != operationID {
		t.Fatalf("replay: %+v %v", result, err)
	}
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM events.events`).Scan(&after); err != nil || after != events {
		t.Fatalf("replay changed events %d/%d: %v", after, events, err)
	}
	// Simulate the ordinary completed one-shot state, then reapply its unchanged source.
	if _, err := db.Exec(`UPDATE automation.schedules SET status='completed',next_fire_at=NULL WHERE schedule_id=$1`, get("once").ScheduleID); err != nil {
		t.Fatal(err)
	}
	apply("unchanged")
	unchanged := get("check")
	if unchanged.ScheduleID != first.ScheduleID || !unchanged.NextFireAt.Equal(*first.NextFireAt) || get("once").Status != "completed" {
		t.Fatal("reapply reset occurrence or identity")
	}
	if _, err := native.PauseSchedule(ctx, req, first.ScheduleID, automation.UpdateScheduleStatusInput{Reason: "operator pause"}); err != nil {
		t.Fatal(err)
	}
	spec.TimeoutSeconds = 60
	write()
	apply("edit-paused")
	if get("check").Status != "paused" {
		t.Fatal("source edit undid operator pause")
	}
	// Explicit source status transition permits activation again.
	spec.Status = "disabled"
	write()
	apply("explicit-disable")
	if _, err := native.FireScheduleNow(ctx, req, first.ScheduleID, automation.FireScheduleInput{}); err == nil {
		t.Fatal("disabled schedule fired")
	} else {
		var typed *loomerrors.Error
		if !errors.As(err, &typed) || typed.Code != "schedule.disabled" {
			t.Fatal(err)
		}
	}
	spec.Status = "active"
	write()
	apply("explicit-enable")
	fire, err := native.FireScheduleNow(ctx, req, first.ScheduleID, automation.FireScheduleInput{})
	if err != nil {
		t.Fatal(err)
	}
	delete(doc.Resources, "check")
	delete(doc.Resources, "once")
	write()
	apply("withdraw")
	if get("check").Status != "disabled" || get("once").Status != "disabled" {
		t.Fatal("withdrawal left schedules active")
	}
	if _, err := native.GetScheduleFire(ctx, fire.Fire.ScheduleFireID); err != nil {
		t.Fatal("withdrawal lost history", err)
	}
	states, err := readDeclarationSchedules(ctx, db, doc.Project.ID)
	if err != nil || !states["check"].Marker.Retired {
		t.Fatalf("retirement marker: %+v %v", states, err)
	}
	plan, err := service.Plan(ctx, p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Basis.Actions {
		if a.Owner == pc.DeclarationOwnerAutomation {
			t.Fatal("retired resource keeps producing actions")
		}
	}
}
