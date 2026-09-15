package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	pc "loom.local/loom/internal/projectcontracts"
)

// Every parent owns a uniquely named database; no inherited LOOM_DB_URL is used.
func operationDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires disposable LOOM_TEST_DB_URL")
	}
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && !(u.Hostname() == "" && strings.HasPrefix(u.Query().Get("host"), "/tmp/")) {
		t.Fatal("refuse nonlocal PostgreSQL acceptance")
	}
	admin, e := sql.Open("pgx", raw)
	if e != nil {
		t.Fatal(e)
	}
	name := "decl_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, e = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	u.Path = "/" + name
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		if _, e := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	result, e := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations"))
	if e != nil || result.CurrentVersion != 71 {
		t.Fatalf("migrate through 71: %+v %v", result, e)
	}
	_, e = db.Exec(`CREATE SCHEMA declaration_fixture;
 CREATE TABLE declaration_fixture.inputs(project_id text PRIMARY KEY,resolution jsonb NOT NULL,current_state jsonb NOT NULL);
 CREATE TABLE declaration_fixture.effects(token text PRIMARY KEY,project_id text NOT NULL,receipt jsonb NOT NULL);
 CREATE TABLE declaration_fixture.calls(id bigserial PRIMARY KEY,token text NOT NULL,action_id text NOT NULL);
 CREATE TABLE declaration_fixture.events(id bigserial PRIMARY KEY);`)
	if e != nil {
		t.Fatal(e)
	}
	return db, u.String()
}

type pgResolver struct{ db *sql.DB }

func (r *pgResolver) Resolve(ctx context.Context, p Principal, req pc.DeclarationPlanRequest) (Resolution, error) {
	var raw, current []byte
	e := r.db.QueryRowContext(ctx, `SELECT resolution,current_state FROM declaration_fixture.inputs WHERE project_id=$1`, req.ProjectRef).Scan(&raw, &current)
	if e != nil {
		return Resolution{}, e
	}
	var x Resolution
	var c Prerequisites
	if json.Unmarshal(raw, &x) != nil || json.Unmarshal(current, &c) != nil {
		return x, errors.New("fixture decode")
	}
	x.Plan.Basis.Target = c.Target
	x.Plan.Basis.Sources = c.Sources
	x.Plan.Basis.Bindings = c.Bindings
	x.Plan.Basis.Revisions = c.Revisions
	x.Plan.PlanID = ""
	for i := range x.Plan.Basis.Actions {
		x.Plan.Basis.Actions[i].Authorization.ActorID = p.ActorID
	}
	return x, nil
}
func (r *pgResolver) Current(ctx context.Context, _ Principal, x Resolution) (Prerequisites, error) {
	var raw []byte
	var p Prerequisites
	e := r.db.QueryRowContext(ctx, `SELECT current_state FROM declaration_fixture.inputs WHERE project_id=$1`, x.Plan.Basis.Target.ProjectID).Scan(&raw)
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(raw, &p)
	return p, e
}

type pgAuth struct {
	readDenied     atomic.Bool
	executeDenied  atomic.Bool
	approvalDenied atomic.Bool
	before         func(pc.DeclarationAction)
}

func (a *pgAuth) Read(context.Context, Principal, pc.DeclarationTarget) error {
	if a.readDenied.Load() {
		return errors.New("sensitive raw auth text must not escape")
	}
	return nil
}
func (a *pgAuth) Execute(_ context.Context, p Principal, b pc.DeclarationPlanBasis, act pc.DeclarationAction, refs []string) error {
	if a.before != nil {
		a.before(act)
	}
	if a.executeDenied.Load() || act.Authorization.ActorID != p.ActorID {
		return fail(pc.DeclarationUnauthorized, "access_revoked")
	}
	if act.Authorization.ApprovalRequired && (a.approvalDenied.Load() || len(refs) != 1 || refs[0] != "approval:exact") {
		return fail(pc.DeclarationApprovalRequired, "approval_invalid")
	}
	return nil
}

type pgOwner struct {
	db           *sql.DB
	failAction   string
	state        ObservationState
	beforeCommit func(ActionCall)
	afterCommit  func(ActionCall)
	ignoreFence  bool
}

func (o *pgOwner) Validate(ctx context.Context, a pc.DeclarationAction, p json.RawMessage) error {
	return (inertOwner{}).Validate(ctx, a, p)
}
func (o *pgOwner) Observe(ctx context.Context, c ActionCall) (Observation, error) {
	var raw []byte
	e := o.db.QueryRowContext(ctx, `SELECT receipt FROM declaration_fixture.effects WHERE token=$1`, c.Token).Scan(&raw)
	if e == nil {
		var r Receipt
		if e = json.Unmarshal(raw, &r); e != nil {
			return Observation{}, e
		}
		return Observation{State: Committed, Receipt: &r}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Observation{}, e
	}
	if o.state != "" {
		return Observation{State: o.state}, nil
	}
	return Observation{State: Absent}, nil
}
func (o *pgOwner) Apply(ctx context.Context, c ActionCall) (Observation, error) {
	if _, e := o.db.ExecContext(ctx, `INSERT INTO declaration_fixture.calls(token,action_id) VALUES($1,$2)`, c.Token, c.Action.ID); e != nil {
		return Observation{}, e
	}
	if o.failAction == c.Action.ID {
		return Observation{}, errors.New("password=fixture-secret must never escape")
	}
	if o.beforeCommit != nil {
		o.beforeCommit(c)
	}
	// ignoreFence deliberately simulates delayed delivery whose old coordinator
	// lost its session: owner CAS still must refuse a superseding durable revision.
	if !o.ignoreFence {
		if e := c.Fence.Check(ctx); e != nil {
			return Observation{State: Uncertain}, e
		}
	}
	tx, e := o.db.BeginTx(ctx, nil)
	if e != nil {
		return Observation{}, e
	}
	defer tx.Rollback()
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT current_state FROM declaration_fixture.inputs WHERE project_id=$1 FOR UPDATE`, c.Target.ProjectID).Scan(&raw)
	if e != nil {
		return Observation{}, e
	}
	var current Prerequisites
	if e = json.Unmarshal(raw, &current); e != nil {
		return Observation{}, e
	}
	var existing []byte
	e = tx.QueryRowContext(ctx, `SELECT receipt FROM declaration_fixture.effects WHERE token=$1`, c.Token).Scan(&existing)
	if e == nil {
		var r Receipt
		json.Unmarshal(existing, &r)
		return Observation{State: Committed, Receipt: &r}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Observation{}, e
	}
	// Exact durable compare-and-swap at the effect boundary, not a callback check.
	if !same(current, c.Expected) {
		return Observation{State: Uncertain}, fail(pc.DeclarationOperationConflict, "owner_cas_conflict")
	}
	key := "projects:registry"
	after := "registered"
	if c.Action.Owner == pc.DeclarationOwnerKnowledge {
		key = "knowledge:source"
		after = "enrolled"
	}
	// A new independent declaration revision yields a distinct owner revision.
	after = after + ":" + c.Action.InputHash
	receipt := Receipt{Token: c.Token, ActionID: c.Action.ID, Owner: c.Action.Owner, InputHash: c.Action.InputHash, EffectRef: "fixture:" + c.Token, Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{}}
	if current.Revisions[key] != after {
		receipt.Revisions[key] = RevisionChange{Before: current.Revisions[key], After: after}
		current.Revisions[key] = after
	}
	raw, _ = canonicalValue(current)
	rr, _ := canonicalValue(receipt)
	if _, e = tx.ExecContext(ctx, `UPDATE declaration_fixture.inputs SET current_state=$2 WHERE project_id=$1`, c.Target.ProjectID, raw); e != nil {
		return Observation{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO declaration_fixture.effects(token,project_id,receipt) VALUES($1,$2,$3)`, c.Token, c.Target.ProjectID, rr); e != nil {
		return Observation{}, e
	}
	if e = tx.Commit(); e != nil {
		return Observation{}, e
	}
	if o.afterCommit != nil {
		o.afterCommit(c)
	}
	return Observation{State: Committed, Receipt: &receipt}, nil
}

type pgFixture struct {
	db       *sql.DB
	url      string
	p        Principal
	x        Resolution
	resolver *pgResolver
	auth     *pgAuth
	owner    *pgOwner
	s        *Service
	req      pc.DeclarationApplyRequest
}

type legacyReplayResolver struct{ Resolver }

func (r legacyReplayResolver) Resolve(context.Context, Principal, pc.DeclarationPlanRequest) (Resolution, error) {
	return Resolution{}, errors.New("fresh legacy adoption is no longer eligible")
}

func TestDeclarationLegacyOwnerSameRequestReplayPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 1)
	first, err := f.s.Apply(t.Context(), f.p, f.req)
	if err != nil || first.State != pc.DeclarationOperationSucceeded {
		t.Fatalf("initial operation: %+v %v", first, err)
	}
	// Original Current remains authoritative, but new adoption cannot resolve.
	f.s.resolver = legacyReplayResolver{f.resolver}
	before := f.count(t, "declaration_fixture.effects")
	again, err := f.s.Apply(t.Context(), f.p, f.req)
	if err != nil || again.OperationID != first.OperationID || again.State != pc.DeclarationOperationSucceeded || f.count(t, "declaration_fixture.effects") != before {
		t.Fatalf("same key without OperationID must recover original operation: %+v %v", again, err)
	}
	f.change(t, func(_ *Resolution, p *Prerequisites) { p.Sources[0].Hash = hashBytes([]byte("changed")) })
	if _, err = f.s.Apply(t.Context(), f.p, f.req); err == nil {
		t.Fatal("original operation hid current source drift")
	}
}

func seedPrincipal(t *testing.T, db *sql.DB, p Principal) {
	t.Helper()
	_, e := db.Exec(`INSERT INTO identity.actors(actor_id,actor_key,display_name,actor_kind,status) VALUES($1,$1,'fixture','agent','active') ON CONFLICT DO NOTHING`, p.ActorID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO nodes.nodes(node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES($1,$1,'fixture','workspace','workspace','test','active') ON CONFLICT DO NOTHING`, p.OriginNodeID)
	if e != nil {
		t.Fatal(e)
	}
}
func addFixture(t *testing.T, db *sql.DB, url string, n int) *pgFixture {
	t.Helper()
	p := testPrincipal()
	x := testResolution(p, n)
	seedPrincipal(t, db, p)
	raw, _ := canonicalValue(x)
	state, _ := canonicalValue(prerequisites(x.Plan.Basis))
	if _, e := db.Exec(`INSERT INTO declaration_fixture.inputs VALUES($1,$2,$3)`, x.Plan.Basis.Target.ProjectID, raw, state); e != nil {
		t.Fatal(e)
	}
	f := &pgFixture{db: db, url: url, p: p, x: x, resolver: &pgResolver{db}, auth: &pgAuth{}, owner: &pgOwner{db: db}}
	f.service()
	plan, e := f.s.Plan(t.Context(), p, testRequest(x))
	if e != nil {
		t.Fatal(e)
	}
	f.req = pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: x.Plan.Basis.Target.ProjectID, Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, PlanID: plan.PlanID, IdempotencyKey: "request"}
	return f
}
func (f *pgFixture) service() {
	f.s = NewService(f.db, f.resolver, f.auth, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: f.owner, pc.DeclarationOwnerKnowledge: f.owner})
}
func (f *pgFixture) count(t *testing.T, table string) int {
	t.Helper()
	allowed := map[string]bool{"projects.declaration_operations": true, "projects.declaration_action_receipts": true, "declaration_fixture.calls": true, "declaration_fixture.effects": true, "declaration_fixture.events": true}
	if !allowed[table] {
		t.Fatal("invalid fixture table")
	}
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func (f *pgFixture) change(t *testing.T, mutate func(*Resolution, *Prerequisites)) {
	t.Helper()
	var raw, state []byte
	if e := f.db.QueryRow(`SELECT resolution,current_state FROM declaration_fixture.inputs WHERE project_id=$1`, f.req.ProjectRef).Scan(&raw, &state); e != nil {
		t.Fatal(e)
	}
	var x Resolution
	var current Prerequisites
	json.Unmarshal(raw, &x)
	json.Unmarshal(state, &current)
	mutate(&x, &current)
	raw, _ = canonicalValue(x)
	state, _ = canonicalValue(current)
	if _, e := f.db.Exec(`UPDATE declaration_fixture.inputs SET resolution=$2,current_state=$3 WHERE project_id=$1`, f.req.ProjectRef, raw, state); e != nil {
		t.Fatal(e)
	}
}
func requireSucceeded(t *testing.T, r pc.DeclarationResult, e error) {
	t.Helper()
	if e != nil || r.State != pc.DeclarationOperationSucceeded {
		t.Fatalf("state=%s op=%s actions=%+v error=%v", r.State, r.OperationID, r.Actions, e)
	}
	if r.Readiness.Healthy.State != pc.DeclarationUnknown || r.Readiness.Verified.State != pc.DeclarationUnknown || r.Readiness.Processing.State != pc.DeclarationUnknown {
		t.Fatal("operation invented readiness")
	}
}

func TestDeclarationOperationJournalPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 2)
	for i := 0; i < 2; i++ {
		if _, e := f.s.Plan(t.Context(), f.p, testRequest(f.x)); e != nil {
			t.Fatal(e)
		}
		if _, e := f.s.Status(t.Context(), f.p, testRequest(f.x)); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range []string{"projects.declaration_operations", "projects.declaration_action_receipts", "declaration_fixture.calls", "declaration_fixture.events"} {
		if f.count(t, table) != 0 {
			t.Fatalf("read-only operation wrote %s", table)
		}
	}
	r, e := f.s.Apply(t.Context(), f.p, f.req)
	requireSucceeded(t, r, e)
	again, e := f.s.Apply(t.Context(), f.p, f.req)
	requireSucceeded(t, again, e)
	if r.OperationID != again.OperationID || f.count(t, "declaration_fixture.calls") != 2 || f.count(t, "projects.declaration_operations") != 1 {
		t.Fatal("replay duplicated effects/operation")
	}
	var raw string
	if e = db.QueryRow(`SELECT resolution #>> '{payloads,register_project:project,large}' FROM projects.declaration_operations WHERE operation_id=$1`, r.OperationID).Scan(&raw); e != nil || raw != "9007199254740993" {
		t.Fatalf("jsonb lost precision %s %v", raw, e)
	}
	for _, mutate := range []func(*pc.DeclarationApplyRequest){func(r *pc.DeclarationApplyRequest) { r.PlanID = hashBytes([]byte("other")) }, func(r *pc.DeclarationApplyRequest) { r.ApprovalRefs = []string{"another"} }, func(r *pc.DeclarationApplyRequest) { r.Effects = append(r.Effects, pc.DeclarationProjections) }} {
		q := f.req
		mutate(&q)
		q.OperationID = r.OperationID
		_, e = f.s.Apply(t.Context(), f.p, q)
		requireCode(t, e, pc.DeclarationOperationConflict)
	}
	f.req.OperationID = r.OperationID
	p2 := testPrincipal()
	seedPrincipal(t, db, p2)
	_, e = f.s.Apply(t.Context(), p2, f.req)
	requireCode(t, e, pc.DeclarationUnauthorized)
	sameActorOtherNode := Principal{ActorID: f.p.ActorID, OriginNodeID: p2.OriginNodeID}
	_, e = f.s.Apply(t.Context(), sameActorOtherNode, f.req)
	requireCode(t, e, pc.DeclarationUnauthorized)
	before := f.count(t, "declaration_fixture.calls")
	if _, e = f.s.Operation(t.Context(), f.p, r.OperationID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Status(t.Context(), f.p, testRequest(f.x)); e != nil {
		t.Fatal(e)
	}
	if before != f.count(t, "declaration_fixture.calls") {
		t.Fatal("status applied")
	}
	// Fresh/replay migration and real constraints, including retention guards.
	againMigration, e := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations"))
	if e != nil || againMigration.CurrentVersion != 71 {
		t.Fatal(e)
	}
	for name, statement := range map[string]string{
		"bad_state":        `UPDATE projects.declaration_operations SET state='healthy'`,
		"identity_rewrite": `UPDATE projects.declaration_operations SET project_id='project_01ARZ3NDEKTSV4RRFFQ69G5FAV'`,
		"receipt_rewrite":  `UPDATE projects.declaration_action_receipts SET token='sha256:` + strings.Repeat("a", 64) + `'`,
		"delete_operation": `DELETE FROM projects.declaration_operations`,
		"delete_receipt":   `DELETE FROM projects.declaration_action_receipts`,
		"invalid_fk":       `INSERT INTO projects.declaration_action_receipts(operation_id,action_id,owner,input_hash,token,state) VALUES('job_01ARZ3NDEKTSV4RRFFQ69G5FAV','register_project:project','projects','sha256:` + strings.Repeat("a", 64) + `','sha256:` + strings.Repeat("b", 64) + `','queued')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := db.Exec(statement); e == nil {
				t.Fatal("invalid journal mutation succeeded")
			}
		})
	}
	if f.count(t, "declaration_fixture.events") != 0 {
		t.Fatal("core emitted domain event")
	}
}

func TestDeclarationResumableOwnerPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 1)
	f.owner.state = Pending
	partial, err := f.s.Apply(t.Context(), f.p, f.req)
	if err == nil || partial.OperationID == "" || partial.State != pc.DeclarationOperationPartial || f.count(t, "declaration_fixture.calls") != 0 {
		t.Fatalf("pending=%+v err=%v", partial, err)
	}
	f.owner.state = Resumable
	f.req.OperationID = partial.OperationID
	complete, err := f.s.Apply(t.Context(), f.p, f.req)
	requireSucceeded(t, complete, err)
	wantCalls := len(f.x.Plan.Basis.Actions)
	if complete.OperationID != partial.OperationID || f.count(t, "projects.declaration_operations") != 1 || f.count(t, "declaration_fixture.calls") != wantCalls {
		t.Fatalf("resume replaced operation or duplicated effects: calls=%d want=%d", f.count(t, "declaration_fixture.calls"), wantCalls)
	}
	if _, err = f.s.Apply(t.Context(), f.p, f.req); err != nil || f.count(t, "declaration_fixture.calls") != wantCalls {
		t.Fatal("completed operation replay repeated effects", err)
	}
}

func TestDeclarationOperationPartialResumePostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 2)
	f.owner.failAction = "enroll_knowledge:reading"
	r, e := f.s.Apply(t.Context(), f.p, f.req)
	if e == nil || r.State != pc.DeclarationOperationPartial || r.OperationID == "" || len(r.Actions) != 2 || r.Actions[0].State != pc.DeclarationOperationSucceeded || r.Actions[1].State != pc.DeclarationOperationFailed {
		t.Fatalf("partial truth %+v %v", r, e)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "fixture-secret") {
		t.Fatal("raw owner error escaped")
	}
	f.owner = &pgOwner{db: db}
	f.service()
	f.req.OperationID = r.OperationID
	r, e = f.s.Apply(t.Context(), f.p, f.req)
	requireSucceeded(t, r, e)
	var aCount int
	if e = db.QueryRow(`SELECT count(*) FROM declaration_fixture.calls WHERE action_id='register_project:project'`).Scan(&aCount); e != nil || aCount != 1 {
		t.Fatalf("A repeated %d %v", aCount, e)
	}
	// Both stored and owner dependency identities are fixed across recreation.
	o, e := loadOperation(t.Context(), db, r.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	call, e := actionCall(o, f.p, o.Resolution.Plan.Basis.Actions[1], nil)
	if e != nil || call.Dependencies["register_project:project"].Token != o.Actions["register_project:project"].Token {
		t.Fatal("dependency identity changed", e)
	}
}

func TestDeclarationOperationUncertainAndDriftPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	for _, state := range []ObservationState{Pending, Uncertain, "unrecognized"} {
		t.Run(string(state), func(t *testing.T) {
			f := addFixture(t, db, url, 1)
			f.owner.state = state
			r, e := f.s.Apply(t.Context(), f.p, f.req)
			if e == nil || r.OperationID == "" || r.Readiness.Healthy.State != pc.DeclarationUnknown {
				t.Fatal("pending truth", r, e)
			}
			f.req.OperationID = r.OperationID
			_, _ = f.s.Apply(t.Context(), f.p, f.req)
			if f.count(t, "declaration_fixture.calls") != 0 {
				t.Fatal("uncertain outcome redispatched")
			}
		})
	}
	for _, dimension := range []string{"source", "owner", "target", "lifecycle", "binding"} {
		t.Run(dimension, func(t *testing.T) {
			f := addFixture(t, db, url, 2)
			f.owner.failAction = "enroll_knowledge:reading"
			r, e := f.s.Apply(t.Context(), f.p, f.req)
			if e == nil {
				t.Fatal("expected partial")
			}
			before := f.count(t, "declaration_fixture.calls")
			f.change(t, func(x *Resolution, c *Prerequisites) {
				switch dimension {
				case "source":
					c.Sources[0].Hash = hashBytes([]byte("changed"))
				case "owner":
					c.Revisions["knowledge:source"] = "independent"
				case "target":
					c.Target.LocationRevision = "moved"
				case "lifecycle":
					c.Revisions["projects:lifecycle"] = "archived"
				case "binding":
					b := c.Bindings["reading"]
					b.OwnerRef = "independent"
					c.Bindings["reading"] = b
				}
			})
			f.owner.failAction = ""
			f.req.OperationID = r.OperationID
			r, e = f.s.Apply(t.Context(), f.p, f.req)
			requireCode(t, e, pc.DeclarationPlanStale)
			if f.count(t, "declaration_fixture.calls") != before || r.Actions[0].State != pc.DeclarationOperationSucceeded {
				t.Fatal("drift lost committed effect or dispatched")
			}
		})
	}
}

func TestDeclarationOperationAuthorizationPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	t.Run("before_creation", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		f.auth.executeDenied.Store(true)
		_, e := f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationUnauthorized)
		if f.count(t, "projects.declaration_operations") != 0 {
			t.Fatal("unauthorized intent created")
		}
	})
	t.Run("between_actions_and_resume", func(t *testing.T) {
		f := addFixture(t, db, url, 2)
		f.owner.afterCommit = func(c ActionCall) {
			if c.Action.Kind == pc.DeclarationRegisterProject {
				f.auth.executeDenied.Store(true)
			}
		}
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationUnauthorized)
		if r.OperationID == "" || r.Actions[0].State != pc.DeclarationOperationSucceeded {
			t.Fatal("lost A")
		}
		before := f.count(t, "declaration_fixture.calls")
		f.req.OperationID = r.OperationID
		_, e = f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationUnauthorized)
		if before != f.count(t, "declaration_fixture.calls") {
			t.Fatal("revoked resume applied")
		}
		f.auth.readDenied.Store(true)
		r, e = f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationUnauthorized)
		if r.OperationID != "" {
			t.Fatal("unauthorized result read")
		}
	})
	t.Run("replay", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		requireSucceeded(t, r, e)
		f.auth.executeDenied.Store(true)
		f.req.OperationID = r.OperationID
		_, e = f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationUnauthorized)
		f.auth.readDenied.Store(true)
		r, e = f.s.Operation(t.Context(), f.p, r.OperationID)
		requireCode(t, e, pc.DeclarationUnauthorized)
		if r.OperationID != "" {
			t.Fatal("read leak")
		}
	})
	t.Run("approval", func(t *testing.T) {
		f := addFixture(t, db, url, 2)
		f.change(t, func(x *Resolution, c *Prerequisites) { x.Plan.Basis.Actions[1].Authorization.ApprovalRequired = true })
		plan, e := f.s.Plan(t.Context(), f.p, testRequest(f.x))
		if e != nil {
			t.Fatal(e)
		}
		f.req.PlanID = plan.PlanID
		f.req.ApprovalRefs = []string{"approval:exact"}
		f.owner.failAction = "enroll_knowledge:reading"
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		if e == nil {
			t.Fatal("expected partial")
		}
		f.owner.failAction = ""
		f.auth.approvalDenied.Store(true)
		before := f.count(t, "declaration_fixture.calls")
		f.req.OperationID = r.OperationID
		_, e = f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationApprovalRequired)
		if before != f.count(t, "declaration_fixture.calls") {
			t.Fatal("invalidated approval dispatched")
		}
	})
}

func TestDeclarationOperationConcurrencyPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 1)
	var wg sync.WaitGroup
	out := make(chan pc.DeclarationResult, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := f.s.Apply(t.Context(), f.p, f.req); out <- r; errs <- e }()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for r := range out {
		if id != "" && id != r.OperationID {
			t.Fatal("concurrent duplicate operations")
		}
		id = r.OperationID
	}
	if f.count(t, "declaration_fixture.calls") != 1 || f.count(t, "projects.declaration_operations") != 1 {
		t.Fatal("concurrent duplicate effects")
	}
	t.Run("independent_projects", func(t *testing.T) {
		a := addFixture(t, db, url, 1)
		b := addFixture(t, db, url, 1)
		started := make(chan struct{})
		release := make(chan struct{})
		a.owner.beforeCommit = func(ActionCall) { close(started); <-release }
		done := make(chan error, 1)
		go func() { _, e := a.s.Apply(t.Context(), a.p, a.req); done <- e }()
		<-started
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		r, e := b.s.Apply(ctx, b.p, b.req)
		close(release)
		requireSucceeded(t, r, e)
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("new_revision_supersedes", func(t *testing.T) {
		g := addFixture(t, db, url, 2)
		g.owner.failAction = "enroll_knowledge:reading"
		old, e := g.s.Apply(t.Context(), g.p, g.req)
		if e == nil {
			t.Fatal("expected partial")
		}
		g.change(t, func(x *Resolution, c *Prerequisites) {
			c.Sources[0].Hash = hashBytes([]byte("new source"))
			raw := json.RawMessage(`{"step":"B","large":9007199254740994}`)
			x.Payloads["enroll_knowledge:reading"] = raw
			x.Plan.Basis.Actions[1].InputHash, _ = PayloadHash(raw)
		})
		g.owner.failAction = ""
		plan, e := g.s.Plan(t.Context(), g.p, testRequest(g.x))
		if e != nil {
			t.Fatal(e)
		}
		oldReq := g.req
		g.req.PlanID = plan.PlanID
		g.req.IdempotencyKey = "new-revision"
		newResult, e := g.s.Apply(t.Context(), g.p, g.req)
		requireSucceeded(t, newResult, e)
		before := g.count(t, "declaration_fixture.calls")
		oldReq.OperationID = old.OperationID
		old, e = g.s.Apply(t.Context(), g.p, oldReq)
		if e != nil || old.State != pc.DeclarationOperationSuperseded || old.SupersededBy != newResult.OperationID || before != g.count(t, "declaration_fixture.calls") || old.Actions[0].State != pc.DeclarationOperationSucceeded {
			t.Fatalf("supersession lost truth %+v %v", old, e)
		}
	})
}

func TestDeclarationOperationCancellationAndFencePostgres(t *testing.T) {
	db, url := operationDatabase(t)
	t.Run("before_effect", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, e := f.s.Apply(ctx, f.p, f.req)
		if e == nil || f.count(t, "declaration_fixture.calls") != 0 {
			t.Fatal("cancel before effect")
		}
	})
	t.Run("after_commit", func(t *testing.T) {
		f := addFixture(t, db, url, 2)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.owner.afterCommit = func(ActionCall) { cancel() }
		r, e := f.s.Apply(ctx, f.p, f.req)
		if e == nil || r.OperationID == "" || r.Actions[0].State != pc.DeclarationOperationSucceeded || r.Actions[1].State == pc.DeclarationOperationSucceeded {
			t.Fatalf("cancel lost durable truth %+v %v", r, e)
		}
		var n int
		if e = db.QueryRow(`SELECT count(*) FROM projects.declaration_action_receipts WHERE operation_id=$1 AND state='succeeded'`, r.OperationID).Scan(&n); e != nil || n != 1 {
			t.Fatal("committed evidence not durable", n, e)
		}
		f.owner.afterCommit = nil
		f.req.OperationID = r.OperationID
		r, e = f.s.Apply(t.Context(), f.p, f.req)
		requireSucceeded(t, r, e)
	})
	t.Run("lock_loss_no_next_action", func(t *testing.T) {
		f := addFixture(t, db, url, 2)
		f.owner.afterCommit = func(c ActionCall) {
			l := c.Fence.(*projectLock)
			if _, e := db.Exec(`SELECT pg_terminate_backend($1)`, l.pid); e != nil {
				t.Error(e)
			}
		}
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		if e == nil || r.OperationID == "" {
			t.Fatal("lock loss concealed")
		}
		var n int
		if e = db.QueryRow(`SELECT count(*) FROM declaration_fixture.calls WHERE token IN (SELECT token FROM projects.declaration_action_receipts WHERE operation_id=$1)`, r.OperationID).Scan(&n); e != nil || n != 1 {
			t.Fatal("stale executor dispatched B", n, e)
		}
		f.owner.afterCommit = nil
		f.req.OperationID = r.OperationID
		r, e = f.s.Apply(t.Context(), f.p, f.req)
		requireSucceeded(t, r, e)
	})
	t.Run("stale_completion_owner_cas", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		started := make(chan ActionCall, 1)
		release := make(chan struct{})
		f.owner.ignoreFence = true
		f.owner.beforeCommit = func(c ActionCall) { started <- c; <-release }
		oldDone := make(chan error, 1)
		go func() { _, e := f.s.Apply(t.Context(), f.p, f.req); oldDone <- e }()
		oldCall := <-started
		if _, e := db.Exec(`SELECT pg_terminate_backend($1)`, oldCall.Fence.(*projectLock).pid); e != nil {
			t.Fatal(e)
		}
		f.change(t, func(x *Resolution, c *Prerequisites) {
			c.Sources[0].Hash = hashBytes([]byte("newer source"))
			raw := json.RawMessage(`{"step":"A","large":9007199254740994}`)
			x.Payloads["register_project:project"] = raw
			x.Plan.Basis.Actions[0].InputHash, _ = PayloadHash(raw)
		})
		newer := NewService(db, f.resolver, &pgAuth{}, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: &pgOwner{db: db}})
		plan, e := newer.Plan(t.Context(), f.p, testRequest(f.x))
		if e != nil {
			t.Fatal(e)
		}
		req := f.req
		req.PlanID = plan.PlanID
		req.IdempotencyKey = "newer"
		r, e := newer.Apply(t.Context(), f.p, req)
		close(release)
		requireSucceeded(t, r, e)
		if e = <-oldDone; e == nil {
			t.Fatal("stale old request succeeded")
		}
		var n int
		if e = db.QueryRow(`SELECT count(*) FROM declaration_fixture.effects WHERE token=$1`, oldCall.Token).Scan(&n); e != nil || n != 0 {
			t.Fatal("stale completion bypassed durable owner CAS", n, e)
		}
	})
}

func TestDeclarationOperationProcessRecoveryPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 1)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"crash", "resume"} {
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestDeclarationOperationProcessChild$", "-test.v")
		cmd.Env = append(os.Environ(), "LOOM_DECL_PROCESS="+mode, "LOOM_DECL_FIXTURE_URL="+url, "LOOM_DECL_PROJECT="+f.req.ProjectRef, "LOOM_DECL_ACTOR="+f.p.ActorID, "LOOM_DECL_NODE="+f.p.OriginNodeID, "LOOM_DECL_PLAN="+f.req.PlanID)
		output, err := cmd.CombinedOutput()
		if mode == "crash" {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("expected intentional process loss, got %v %s", err, output)
			}
		} else if err != nil {
			t.Fatalf("fresh process resume: %v %s", err, output)
		}
		t.Logf("%s child: %s", mode, strings.TrimSpace(string(output)))
	}
	if f.count(t, "declaration_fixture.calls") != 1 || f.count(t, "declaration_fixture.effects") != 1 || f.count(t, "projects.declaration_operations") != 1 {
		t.Fatal("fresh process repeated owner effect")
	}
	var state string
	if e = db.QueryRow(`SELECT state FROM projects.declaration_operations`).Scan(&state); e != nil || state != "succeeded" {
		t.Fatal("receipt not recovered", state, e)
	}
}
func TestDeclarationOperationProcessChild(t *testing.T) {
	mode := os.Getenv("LOOM_DECL_PROCESS")
	if mode == "" {
		t.Skip("executed only by the disposable PostgreSQL process-recovery parent")
	}
	raw := os.Getenv("LOOM_DECL_FIXTURE_URL")
	u, e := url.Parse(raw)
	if e != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "decl_") {
		t.Fatal("child fixture database guard")
	}
	db, e := sql.Open("pgx", raw)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	p := Principal{ActorID: os.Getenv("LOOM_DECL_ACTOR"), OriginNodeID: os.Getenv("LOOM_DECL_NODE")}
	owner := &pgOwner{db: db}
	if mode == "crash" {
		owner.afterCommit = func(ActionCall) { fmt.Println("durable owner commit; exiting before coordinator receipt"); os.Exit(23) }
	}
	s := NewService(db, &pgResolver{db}, &pgAuth{}, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: owner})
	req := pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: os.Getenv("LOOM_DECL_PROJECT"), Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, PlanID: os.Getenv("LOOM_DECL_PLAN"), IdempotencyKey: "request"}
	if mode == "resume" {
		req.OperationID, e = findOperation(t.Context(), db, p, req.ProjectRef, req.IdempotencyKey)
		if e != nil || req.OperationID == "" {
			t.Fatal("missing crash intent", e)
		}
	}
	r, e := s.Apply(t.Context(), p, req)
	requireSucceeded(t, r, e)
}

func TestDeclarationOperationReviewEdgesPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	t.Run("missing_receipt_reference_constraint", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		f.owner.state = Pending
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		if e == nil {
			t.Fatal("expected pending")
		}
		_, e = db.Exec(`UPDATE projects.declaration_action_receipts SET state='succeeded',receipt=jsonb_build_object('token',token,'action_id',action_id,'owner',owner,'input_hash',input_hash,'revisions','{}'::jsonb,'bindings','{}'::jsonb) WHERE operation_id=$1`, r.OperationID)
		if e == nil {
			t.Fatal("missing effect_ref passed receipt CHECK via SQL NULL")
		}
	})
	t.Run("returned_commit_survives_lost_journal_connection", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		f.owner.afterCommit = func(c ActionCall) {
			if _, e := db.Exec(`SELECT pg_terminate_backend($1)`, c.Fence.(*projectLock).pid); e != nil {
				t.Error(e)
			}
		}
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		if e == nil || r.OperationID == "" || r.State != pc.DeclarationOperationPartial || r.Actions[0].State != pc.DeclarationOperationSucceeded || r.Actions[0].EffectRef == "" {
			t.Fatalf("known owner commit vanished from failure result: %+v %v", r, e)
		}
	})
	t.Run("pending_source_drift", func(t *testing.T) {
		f := addFixture(t, db, url, 1)
		f.owner.state = Pending
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		if e == nil {
			t.Fatal("expected pending")
		}
		f.change(t, func(_ *Resolution, c *Prerequisites) { c.Sources[0].Hash = hashBytes([]byte("changed pending source")) })
		f.req.OperationID = r.OperationID
		_, e = f.s.Apply(t.Context(), f.p, f.req)
		requireCode(t, e, pc.DeclarationPlanStale)
	})
}

func TestDeclarationOperationInitialGuardsPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	t.Run("empty_operation", func(t *testing.T) {
		f := addFixture(t, db, url, 0)
		r, e := f.s.Apply(t.Context(), f.p, f.req)
		requireSucceeded(t, r, e)
		if len(r.Actions) != 0 || f.count(t, "declaration_fixture.calls") != 0 {
			t.Fatal("empty declaration had effects")
		}
	})
	for _, name := range []string{"selector", "node", "effect", "action", "owner", "payload", "binding", "approval"} {
		t.Run(name, func(t *testing.T) {
			f := addFixture(t, db, url, 2)
			before := f.count(t, "projects.declaration_operations")
			calls := f.count(t, "declaration_fixture.calls")
			switch name {
			case "selector":
				f.req.ProjectRef = ids.NewProjectID()
			case "node":
				f.req.NodeRef = ids.NewNodeID()
			case "effect":
				f.req.Effects = []pc.DeclarationEffect{"shell"}
			case "approval":
				f.change(t, func(x *Resolution, _ *Prerequisites) { x.Plan.Basis.Actions[0].Authorization.ApprovalRequired = true })
				plan, e := f.s.Plan(t.Context(), f.p, testRequest(f.x))
				if e != nil {
					t.Fatal(e)
				}
				f.req.PlanID = plan.PlanID
			default:
				f.change(t, func(x *Resolution, _ *Prerequisites) {
					switch name {
					case "action":
						x.Plan.Basis.Actions[0].Kind = "shell"
					case "owner":
						x.Plan.Basis.Actions[0].Owner = "shell"
					case "payload":
						x.Payloads["register_project:project"] = json.RawMessage(`{"step":"changed","large":9007199254740992}`)
					case "binding":
						x.Plan.Basis.Actions[1].Resource = "absent"
					}
				})
			}
			if _, e := f.s.Apply(t.Context(), f.p, f.req); e == nil {
				t.Fatal("invalid apply accepted")
			}
			if f.count(t, "projects.declaration_operations") != before || f.count(t, "declaration_fixture.calls") != calls {
				t.Fatal("invalid apply created work")
			}
		})
	}
	t.Run("same_key_scoped_by_actor_and_origin", func(t *testing.T) {
		f := addFixture(t, db, url, 0)
		first, e := f.s.Apply(t.Context(), f.p, f.req)
		requireSucceeded(t, first, e)
		p2 := testPrincipal()
		seedPrincipal(t, db, p2)
		otherOrigin := Principal{ActorID: f.p.ActorID, OriginNodeID: p2.OriginNodeID}
		second, e := f.s.Apply(t.Context(), otherOrigin, f.req)
		requireSucceeded(t, second, e)
		if first.OperationID == second.OperationID {
			t.Fatal("origin omitted from durable key")
		}
		f.change(t, func(_ *Resolution, c *Prerequisites) {
			c.Revisions["authorization:"+p2.ActorID+":"+f.p.OriginNodeID] = "1"
		})
		plan, e := f.s.Plan(t.Context(), p2, testRequest(f.x))
		if e != nil {
			t.Fatal(e)
		}
		req := f.req
		req.PlanID = plan.PlanID
		third, e := f.s.Apply(t.Context(), p2, req)
		requireSucceeded(t, third, e)
		if third.OperationID == first.OperationID || third.OperationID == second.OperationID {
			t.Fatal("actor omitted from durable key")
		}
	})
}

// Additional resolver facts model metadata relevant only to a newly requested
// scope. The old resolver keeps reporting its original prerequisite closure.
type supersessionResolver struct {
	base     *pgResolver
	empty    bool
	metadata map[string]string
}

func (r *supersessionResolver) Resolve(ctx context.Context, p Principal, req pc.DeclarationPlanRequest) (Resolution, error) {
	x, e := r.base.Resolve(ctx, p, req)
	if e != nil {
		return x, e
	}
	x.Plan.Basis.Effects = append([]pc.DeclarationEffect{}, req.Effects...)
	for k, v := range r.metadata {
		x.Plan.Basis.Revisions[k] = v
	}
	if r.empty {
		x.Plan.Basis.Actions = []pc.DeclarationAction{}
		x.Payloads = map[string]json.RawMessage{}
	}
	return x, nil
}
func (r *supersessionResolver) Current(ctx context.Context, p Principal, x Resolution) (Prerequisites, error) {
	c, e := r.base.Current(ctx, p, x)
	if e != nil {
		return c, e
	}
	for k, v := range r.metadata {
		c.Revisions[k] = v
	}
	return c, nil
}
func TestDeclarationOperationSupersessionScopePostgres(t *testing.T) {
	db, url := operationDatabase(t)
	for _, scenario := range []string{"projection", "other_actor_projection", "owner_fact_projection"} {
		t.Run(scenario, func(t *testing.T) {
			f := addFixture(t, db, url, 2)
			f.owner.failAction = "enroll_knowledge:reading"
			old, e := f.s.Apply(t.Context(), f.p, f.req)
			if e == nil {
				t.Fatal("expected partial")
			}
			calls := f.count(t, "declaration_fixture.calls")
			principal := f.p
			metadata := map[string]string{}
			if scenario == "other_actor_projection" {
				principal = testPrincipal()
				seedPrincipal(t, db, principal)
				metadata["authorization:"+principal.ActorID+":"+f.p.OriginNodeID] = "1"
			}
			if scenario == "owner_fact_projection" {
				metadata["notesprojection:cache"] = "new-projection-fact"
			}
			r := &supersessionResolver{base: f.resolver, empty: true, metadata: metadata}
			s := NewService(db, r, f.auth, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: f.owner, pc.DeclarationOwnerKnowledge: f.owner})
			req := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.req.ProjectRef, Effects: []pc.DeclarationEffect{pc.DeclarationProjections}}
			plan, e := s.Plan(t.Context(), principal, req)
			if e != nil {
				t.Fatal(e)
			}
			apply := pc.DeclarationApplyRequest{SchemaVersion: req.SchemaVersion, ProjectRef: req.ProjectRef, Effects: req.Effects, PlanID: plan.PlanID, IdempotencyKey: "projection"}
			fresh, e := s.Apply(t.Context(), principal, apply)
			requireSucceeded(t, fresh, e)
			original, e := f.s.Operation(t.Context(), f.p, old.OperationID)
			if e != nil || original.State == pc.DeclarationOperationSuperseded || original.SupersededBy != "" {
				t.Fatalf("disjoint scope superseded pending reconciliation: %+v %v", original, e)
			}
			if calls != f.count(t, "declaration_fixture.calls") {
				t.Fatal("disjoint projection repeated owner effects")
			}
			if scenario == "projection" {
				apply.IdempotencyKey = f.req.IdempotencyKey
				_, e = s.Apply(t.Context(), principal, apply)
				requireCode(t, e, pc.DeclarationOperationConflict)
			}
			f.owner.failAction = ""
			f.req.OperationID = old.OperationID
			resumed, e := f.s.Apply(t.Context(), f.p, f.req)
			requireSucceeded(t, resumed, e)
			if resumed.OperationID != old.OperationID {
				t.Fatal("did not resume original operation")
			}
		})
	}
	for _, scenario := range []string{"own_completed_revision", "unknown_other_actor", "unknown_owner_fact", "effect_selection"} {
		t.Run(scenario, func(t *testing.T) {
			f := addFixture(t, db, url, 2)
			if strings.HasPrefix(scenario, "unknown") {
				f.owner.state = Uncertain
			} else {
				f.owner.failAction = "enroll_knowledge:reading"
			}
			old, e := f.s.Apply(t.Context(), f.p, f.req)
			if e == nil {
				t.Fatal("expected unfinished original")
			}
			calls := f.count(t, "declaration_fixture.calls")
			ops := f.count(t, "projects.declaration_operations")
			principal := f.p
			metadata := map[string]string{}
			selected := []pc.DeclarationEffect{pc.DeclarationReconcile}
			if scenario == "unknown_other_actor" {
				principal = testPrincipal()
				seedPrincipal(t, db, principal)
				metadata["authorization:"+principal.ActorID+":"+f.p.OriginNodeID] = "1"
			}
			if scenario == "unknown_owner_fact" {
				metadata["notesprojection:cache"] = "independent"
			}
			if scenario == "effect_selection" {
				selected = append(selected, pc.DeclarationProjections)
			}
			r := &supersessionResolver{base: f.resolver, metadata: metadata}
			s := NewService(db, r, f.auth, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: f.owner, pc.DeclarationOwnerKnowledge: f.owner})
			plan, e := s.Plan(t.Context(), principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: f.req.ProjectRef, Effects: selected})
			if e != nil {
				t.Fatal(e)
			}
			req := f.req
			req.PlanID = plan.PlanID
			req.IdempotencyKey = "overlapping-equivalent"
			req.Effects = selected
			_, e = s.Apply(t.Context(), principal, req)
			requireCode(t, e, pc.DeclarationOperationConflict)
			original, e := f.s.Operation(t.Context(), f.p, old.OperationID)
			if e != nil || original.State == pc.DeclarationOperationSuperseded {
				t.Fatal("non-desired plan difference superseded original", e)
			}
			if calls != f.count(t, "declaration_fixture.calls") || ops != f.count(t, "projects.declaration_operations") {
				t.Fatal("equivalent unfinished work created another dispatch token")
			}
			f.owner.failAction = ""
			f.owner.state = ""
			f.req.OperationID = old.OperationID
			resumed, e := f.s.Apply(t.Context(), f.p, f.req)
			requireSucceeded(t, resumed, e)
		})
	}
}

func TestDeclarationLegacyExactReplayIsolationPostgres(t *testing.T) {
	db, url := operationDatabase(t)
	f := addFixture(t, db, url, 1)
	result, err := f.s.Apply(t.Context(), f.p, f.req)
	requireSucceeded(t, result, err)
	f.s.resolver = legacyReplayResolver{f.resolver}
	foreign := testPrincipal()
	seedPrincipal(t, db, foreign)
	for _, scenario := range []string{"foreign_principal", "different_key", "different_request"} {
		t.Run(scenario, func(t *testing.T) {
			principal, request := f.p, f.req
			switch scenario {
			case "foreign_principal":
				principal = foreign
			case "different_key":
				request.IdempotencyKey += "-other"
			case "different_request":
				request.PlanID = hashBytes([]byte("other-plan"))
			}
			got, err := f.s.Apply(t.Context(), principal, request)
			if err == nil || got.OperationID != "" {
				t.Fatalf("unrelated request discovered operation: %+v %v", got, err)
			}
		})
	}
	// A bounded ambiguity check must not choose the newest matching operation.
	// Insert a second immutable fixture row under another project-scoped key.
	if _, err = db.Exec(`INSERT INTO projects.declaration_operations SELECT (jsonb_populate_record(NULL::projects.declaration_operations,to_jsonb(o)||jsonb_build_object('operation_id',$2::text,'project_id',$3::text,'resolution',jsonb_set(o.resolution,'{plan,basis,target,project_id}',to_jsonb($3::text))))).* FROM projects.declaration_operations o WHERE operation_id=$1`, result.OperationID, ids.NewJobID(), ids.NewProjectID()); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Apply(t.Context(), f.p, f.req)
	if err == nil || got.OperationID != "" || failureCause(err) != "exact_request_identity_conflict" {
		t.Fatalf("ambiguous replay: %+v %v", got, err)
	}
	if f.count(t, "declaration_fixture.effects") != 1 {
		t.Fatal("replay isolation changed effects")
	}
}
