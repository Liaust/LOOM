package projectapply

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func testPrincipal() Principal {
	return Principal{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID()}
}
func testResolution(p Principal, n int) Resolution {
	project := ids.NewProjectID()
	b := pc.DeclarationPlanBasis{SchemaVersion: pc.DeclarationPlanSchemaV05, Target: pc.DeclarationTarget{ProjectID: project, OwnerNodeID: p.OriginNodeID, ProjectRoot: "/fixture/" + project, LocationRevision: "1"}, Sources: []pc.DeclarationSource{{Ref: pc.CanonicalRootContractPath, SchemaVersion: pc.ProjectSchemaV05, Hash: hashBytes([]byte("root")), Revision: "1"}}, Bindings: map[pc.ResourceKey]pc.DeclarationBinding{}, Revisions: map[string]string{"projects:registry": "absent", "projects:lifecycle": "draft", "authorization:" + p.ActorID + ":" + p.OriginNodeID: "1", "knowledge:source": "absent"}, Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, Actions: []pc.DeclarationAction{}}
	x := Resolution{Plan: pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, Basis: b, Errors: []pc.DeclarationError{}}, Payloads: map[string]json.RawMessage{}}
	for i := 0; i < n; i++ {
		a := pc.DeclarationAction{ID: "register_project:project", Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, TargetRef: project, DependsOn: []string{}, Authorization: pc.DeclarationAuthorization{ActorID: p.ActorID, NodeID: p.OriginNodeID, Level: 3, PolicyRevision: "1"}}
		raw := json.RawMessage(`{"step":"A","large":9007199254740993}`)
		if i == 1 {
			a.ID = "enroll_knowledge:reading"
			a.Kind = pc.DeclarationEnrollKnowledge
			a.Owner = pc.DeclarationOwnerKnowledge
			a.Resource = "reading"
			a.TargetRef = project + "/reading"
			a.DependsOn = []string{"register_project:project"}
			x.Plan.Basis.Bindings["reading"] = pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}
			raw = json.RawMessage(`{"step":"B","large":9007199254740993}`)
		}
		a.InputHash, _ = PayloadHash(raw)
		x.Plan.Basis.Actions = append(x.Plan.Basis.Actions, a)
		x.Payloads[a.ID] = raw
	}
	return x
}

type staticResolver struct{ x Resolution }

func (r *staticResolver) Resolve(context.Context, Principal, pc.DeclarationPlanRequest) (Resolution, error) {
	return cloneResolution(r.x), nil
}
func (r *staticResolver) Current(context.Context, Principal, Resolution) (Prerequisites, error) {
	return prerequisites(cloneResolution(r.x).Plan.Basis), nil
}

type allowAuth struct{}

func (allowAuth) Read(context.Context, Principal, pc.DeclarationTarget) error { return nil }
func (allowAuth) Execute(context.Context, Principal, pc.DeclarationPlanBasis, pc.DeclarationAction, []string) error {
	return nil
}

type inertOwner struct{}

func (inertOwner) Validate(_ context.Context, _ pc.DeclarationAction, raw json.RawMessage) error {
	var p struct {
		Step  string      `json:"step"`
		Large json.Number `json:"large"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Step == "" {
		return fail(pc.DeclarationInvalid, "invalid_test_payload")
	}
	return nil
}
func (inertOwner) Observe(context.Context, ActionCall) (Observation, error) {
	return Observation{State: Absent}, nil
}
func (inertOwner) Apply(context.Context, ActionCall) (Observation, error) {
	panic("plan must not apply")
}
func testService(p Principal, x Resolution) *Service {
	return NewService(nil, &staticResolver{x}, allowAuth{}, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerProjects: inertOwner{}, pc.DeclarationOwnerKnowledge: inertOwner{}})
}
func testRequest(x Resolution) pc.DeclarationPlanRequest {
	return pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: x.Plan.Basis.Target.ProjectID, Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}}
}
func requireCode(t *testing.T, err error, code pc.DeclarationErrorCode) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), string(code)+":") {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestDeclarationOperationCanonicalPlan(t *testing.T) {
	raw, e := os.ReadFile(filepath.Join("..", "projectcontracts", "testdata", "declarations_v05", "plan.json"))
	if e != nil {
		t.Fatal(e)
	}
	var fixture pc.DeclarationPlan
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture decode")
	}
	id, e := PlanID(fixture.Basis)
	if e != nil || id != fixture.PlanID {
		t.Fatalf("D0 digest %s %v want %s", id, e, fixture.PlanID)
	}
	p := testPrincipal()
	x := testResolution(p, 2)
	s := testService(p, x)
	a, e := s.Plan(t.Context(), p, testRequest(x))
	if e != nil {
		t.Fatal(e)
	}
	if a.Basis.Actions[0].ID != "register_project:project" {
		t.Fatal("dependency order")
	}
	x.Plan.Basis.Actions[0], x.Plan.Basis.Actions[1] = x.Plan.Basis.Actions[1], x.Plan.Basis.Actions[0]
	x.Plan.GeneratedAt = "different clock"
	b, e := testService(p, x).Plan(t.Context(), p, testRequest(x))
	if e != nil || a.PlanID != b.PlanID {
		t.Fatal("order or clock changed identity", e)
	}
	x.Plan.Basis.Sources = append(x.Plan.Basis.Sources, pc.DeclarationSource{Ref: "owner:artifact", SchemaVersion: "fixture.v1", Hash: hashBytes([]byte("transitive")), Revision: "1"})
	c, e := testService(p, x).Plan(t.Context(), p, testRequest(x))
	if e != nil || c.PlanID == a.PlanID {
		t.Fatal("transitive source ignored", e)
	}
	for _, bad := range []string{`{"x":1.2}`, `{"x":1e3}`, `{"x":1,"x":2}`, `{"x":1} {}`, `{"x": [1, 2.0]}`} {
		if _, e := CanonicalJSON([]byte(bad)); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	aHash, _ := PayloadHash([]byte(`{"z":9007199254740992,"a":1}`))
	bHash, _ := PayloadHash([]byte(`{"a":1,"z":9007199254740993}`))
	if aHash == bHash {
		t.Fatal("integer precision lost")
	}
	if got, _ := CanonicalJSON([]byte(`{"x":18446744073709551615}`)); string(got) != `{"x":18446744073709551615}` {
		t.Fatal("uint64 precision lost")
	}
}
func TestDeclarationOperationRejectBeforeEffects(t *testing.T) {
	p := testPrincipal()
	base := testResolution(p, 2)
	cases := map[string]func(*Resolution){
		"unknown_owner":   func(x *Resolution) { x.Plan.Basis.Actions[0].Owner = "shell" },
		"unknown_action":  func(x *Resolution) { x.Plan.Basis.Actions[0].Kind = "shell" },
		"unknown_effect":  func(x *Resolution) { x.Plan.Basis.Effects = []pc.DeclarationEffect{"shell"} },
		"missing_payload": func(x *Resolution) { delete(x.Payloads, x.Plan.Basis.Actions[0].ID) },
		"payload_hash":    func(x *Resolution) { x.Payloads[x.Plan.Basis.Actions[0].ID] = json.RawMessage(`{"step":"changed"}`) },
		"payload_not_object": func(x *Resolution) {
			a := &x.Plan.Basis.Actions[0]
			x.Payloads[a.ID] = json.RawMessage(`[]`)
			a.InputHash, _ = PayloadHash(x.Payloads[a.ID])
		},
		"duplicate_action":    func(x *Resolution) { x.Plan.Basis.Actions[1] = x.Plan.Basis.Actions[0] },
		"cycle":               func(x *Resolution) { x.Plan.Basis.Actions[0].DependsOn = []string{x.Plan.Basis.Actions[1].ID} },
		"missing_dep":         func(x *Resolution) { x.Plan.Basis.Actions[0].DependsOn = []string{"missing"} },
		"duplicate_dep":       func(x *Resolution) { a := &x.Plan.Basis.Actions[1]; a.DependsOn = append(a.DependsOn, a.DependsOn[0]) },
		"missing_source":      func(x *Resolution) { x.Plan.Basis.Sources = nil },
		"missing_binding":     func(x *Resolution) { x.Plan.Basis.Bindings = map[pc.ResourceKey]pc.DeclarationBinding{} },
		"missing_lifecycle":   func(x *Resolution) { delete(x.Plan.Basis.Revisions, "projects:lifecycle") },
		"missing_auth":        func(x *Resolution) { delete(x.Plan.Basis.Revisions, "authorization:"+p.ActorID+":"+p.OriginNodeID) },
		"wrong_target":        func(x *Resolution) { x.Plan.Basis.Target.ProjectID = ids.NewProjectID() },
		"wrong_action_target": func(x *Resolution) { x.Plan.Basis.Actions[0].TargetRef = ids.NewProjectID() },
		"actor_claim":         func(x *Resolution) { x.Plan.Basis.Actions[0].Authorization.ActorID = ids.NewActorID() },
		"repo_binding_kind": func(x *Resolution) {
			x.Plan.Basis.Bindings["reading"] = pc.DeclarationBinding{Kind: pc.DeclarationRepository}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			x := cloneResolution(base)
			mutate(&x)
			if _, e := testService(p, x).Plan(t.Context(), p, testRequest(base)); e == nil {
				t.Fatal("invalid executable plan accepted")
			}
		})
	}
	s := NewService(nil, nil, nil, nil)
	if _, e := s.Plan(t.Context(), p, testRequest(base)); e == nil {
		t.Fatal("missing core accepted")
	}
	s = testService(p, base)
	delete(s.owners, pc.DeclarationOwnerKnowledge)
	if _, e := s.Plan(t.Context(), p, testRequest(base)); e == nil {
		t.Fatal("missing owner accepted")
	}
	for _, n := range []int{0, 1, 2} {
		x := testResolution(p, n)
		if _, e := testService(p, x).Plan(t.Context(), p, testRequest(x)); e != nil {
			t.Fatalf("%d actions: %v", n, e)
		}
	}
}
func TestDeclarationOperationReceiptFence(t *testing.T) {
	p := testPrincipal()
	x := testResolution(p, 1)
	x.Plan.PlanID, _ = PlanID(x.Plan.Basis)
	a := x.Plan.Basis.Actions[0]
	r := Receipt{Token: stableToken(x, a), ActionID: a.ID, Owner: a.Owner, InputHash: a.InputHash, EffectRef: "fixture:receipt", Revisions: map[string]RevisionChange{"projects:registry": {Before: "absent", After: "1"}}, Bindings: map[pc.ResourceKey]BindingChange{}}
	expected, e := expectedState(x, []Receipt{r})
	if e != nil || expected.Revisions["projects:registry"] != "1" {
		t.Fatal(e)
	}
	r.Revisions["authorization:"+p.ActorID+":"+p.OriginNodeID] = RevisionChange{Before: "1", After: "2"}
	if _, e = expectedState(x, []Receipt{r}); e == nil {
		t.Fatal("owner may not rebase auth")
	}
}

func TestDeclarationOperationSupersessionDesiredScope(t *testing.T) {
	p := testPrincipal()
	x := testResolution(p, 2)
	x.Plan.PlanID, _ = PlanID(x.Plan.Basis)
	old := &operation{Resolution: x, Actions: map[string]*actionRecord{}}
	for _, a := range x.Plan.Basis.Actions {
		old.Actions[a.ID] = &actionRecord{State: pc.DeclarationOperationPartial, Token: stableToken(x, a)}
	}
	for _, scenario := range []string{"disjoint_owner", "actor_only", "owner_fact_only", "desired_input", "changed_source", "mixed_equivalent", "binding_without_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			next := cloneResolution(x)
			wantOverlap, wantChange := true, false
			wantError := false
			switch scenario {
			case "disjoint_owner":
				next.Plan.Basis.Actions = []pc.DeclarationAction{{ID: "refresh_projection:reading:notesprojection", Kind: pc.DeclarationRefreshProjection, Resource: "reading", Owner: pc.DeclarationOwnerNotes, TargetRef: x.Plan.Basis.Target.ProjectID + "/reading", InputHash: x.Plan.Basis.Actions[1].InputHash, DependsOn: []string{}}}
				wantOverlap = false
			case "actor_only":
				next.Plan.Basis.Actions[0].Authorization.ActorID = ids.NewActorID()
			case "owner_fact_only":
				next.Plan.Basis.Revisions["notesprojection:cache"] = "other"
			case "desired_input":
				next.Plan.Basis.Actions = []pc.DeclarationAction{next.Plan.Basis.Actions[1]}
				next.Plan.Basis.Actions[0].InputHash = hashBytes([]byte("new desired payload"))
				wantChange = true
			case "changed_source":
				next.Plan.Basis.Sources[0].Hash = hashBytes([]byte("new source"))
				wantChange = true
			case "mixed_equivalent":
				next.Plan.Basis.Actions[1].InputHash = hashBytes([]byte("new B; same unresolved A"))
			case "binding_without_receipt":
				b := next.Plan.Basis.Bindings["reading"]
				b.OwnerRef = "unproven"
				next.Plan.Basis.Bindings["reading"] = b
				wantError = true
			}
			overlap, changed, e := desiredSupersession(old, next)
			if wantError {
				requireCode(t, e, pc.DeclarationOperationConflict)
				return
			}
			if e != nil || overlap != wantOverlap || changed != wantChange {
				t.Fatalf("overlap=%v changed=%v err=%v, want %v/%v", overlap, changed, e, wantOverlap, wantChange)
			}
		})
	}
	// Completed A cannot make unrelated remaining B a conflicting operation.
	a := x.Plan.Basis.Actions[0]
	old.Actions[a.ID].Receipt = &Receipt{Token: stableToken(x, a), ActionID: a.ID, Owner: a.Owner, InputHash: a.InputHash, EffectRef: "fixture:A", Revisions: map[string]RevisionChange{"projects:registry": {Before: "absent", After: "registered"}}, Bindings: map[pc.ResourceKey]BindingChange{}}
	next := cloneResolution(x)
	next.Plan.Basis.Actions = next.Plan.Basis.Actions[:1]
	next.Plan.Basis.Revisions["projects:registry"] = "registered"
	next.Plan.Basis.Actions[0].InputHash = hashBytes([]byte("new A only"))
	overlap, changed, e := desiredSupersession(old, next)
	if e != nil || overlap || changed {
		t.Fatalf("completed action caused supersession: %v %v %v", overlap, changed, e)
	}
}

func TestDeclarationAtomicRegistrationBindingBoundary(t *testing.T) {
	x := testResolution(testPrincipal(), 1)
	x.Plan.Basis.Bindings["code"] = pc.DeclarationBinding{Kind: pc.DeclarationRepository}
	x.Plan.Basis.Bindings["reading"] = pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}
	payload := projects.DeclarationRegistrationPayload{Project: projects.ProjectContractProjectInput{ID: x.Plan.Basis.Target.ProjectID}, Repositories: []projects.ProjectRepositoryValidatedMember{{Key: "code", Path: "code", Role: projects.ProjectRepositoryRoleComponent}}}
	raw, err := canonicalValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	action := x.Plan.Basis.Actions[0]
	action.InputHash = hashBytes(raw)
	x.Plan.Basis.Actions[0] = action
	x.Payloads[action.ID] = raw
	receipt := Receipt{ActionID: action.ID, Owner: action.Owner, InputHash: action.InputHash, Token: stableToken(x, action), EffectRef: "registration", Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{"code": {Before: x.Plan.Basis.Bindings["code"], After: pc.DeclarationBinding{Kind: pc.DeclarationRepository, RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}}}
	if _, err = expectedState(x, []Receipt{receipt}); err != nil {
		t.Fatalf("exact grouped allocation: %v", err)
	}
	for _, name := range []string{"absent", "other_kind", "payload_missing", "foreign_project", "established_id"} {
		t.Run(name, func(t *testing.T) {
			y := cloneResolution(x)
			r := receipt
			r.Bindings = map[pc.ResourceKey]BindingChange{}
			for k, v := range receipt.Bindings {
				r.Bindings[k] = v
			}
			switch name {
			case "absent":
				r.Bindings["missing"] = r.Bindings["code"]
			case "other_kind":
				r.Bindings["reading"] = BindingChange{Before: y.Plan.Basis.Bindings["reading"], After: pc.DeclarationBinding{Kind: pc.DeclarationKnowledge, OwnerRef: "invented"}}
			case "payload_missing":
				y.Payloads[action.ID] = []byte(`{"project":{"id":"` + y.Plan.Basis.Target.ProjectID + `"},"repositories":[]}`)
			case "foreign_project":
				y.Payloads[action.ID] = []byte(`{"project":{"id":"foreign"},"repositories":[{"key":"code"}]}`)
			case "established_id":
				b := y.Plan.Basis.Bindings["code"]
				b.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAW"
				y.Plan.Basis.Bindings["code"] = b
				c := r.Bindings["code"]
				c.Before = b
				r.Bindings["code"] = c
			}
			if _, err := expectedState(y, []Receipt{r}); err == nil {
				t.Fatal("malicious grouped binding accepted")
			}
		})
	}
}
