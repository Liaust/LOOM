package projectapply

import (
	"encoding/json"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

func TestApplicationBrowsePlanDoesNotReinstallMatchingRuntime(t *testing.T) {
	call, app, facts := applicationFixture(t)
	var original applicationPayload
	json.Unmarshal(call.Payload, &original)
	original.Request.Manifest.Listener = &sr.ApplicationListener{Address: "127.0.0.1", Port: 9380}
	yes, no := true, false
	facts.Installation = sr.ApplicationPrerequisiteInstallation{Present: true, Applied: &yes, Committed: &yes, Fenced: &no, Retired: &no, Revision: "installed"}
	facts.RuntimeDesired = original.Request.RuntimeDesiredDigest()
	data := app.Data["files"]
	data.Browse = &pc.ApplicationDataBrowse{Label: "Exports", Subpath: "notebooks"}
	app.Data["files"] = data
	manifest, _ := json.Marshal(original.Request.Manifest)
	doc := pc.ProjectDeclaration{Project: pc.ProjectSpec{Name: "Fixture"}, Resources: map[pc.ResourceKey]pc.ResourceDeclaration{"server": {Kind: pc.DeclarationApplication, Application: &app}}}
	x := localDeclaration{Target: call.Target, Analysis: pc.Analysis{Loaded: &pc.LoadedProject{Declaration: &doc}, Plan: pc.ProjectPlan{Declaration: &pc.DeclarationCompilation{Sources: []pc.DeclarationSourceSnapshot{{DeclarationSource: pc.DeclarationSource{Ref: app.Manifest}, Raw: manifest}}}}}}
	x.Input.Project.OwnerNode = "main"
	x.Authority.CanRead = true
	x.Authority.CanWrite = true
	x.Authority.Level = 5
	r := LocalResolver{Applications: &preparationFixture{facts: facts}}
	basis := pc.DeclarationPlanBasis{Bindings: map[pc.ResourceKey]pc.DeclarationBinding{"repo": {RepositoryID: facts.RepositoryID}}, Actions: []pc.DeclarationAction{{ID: "register_project:project", Authorization: pc.DeclarationAuthorization{Level: 5}}}}
	payloads := map[string]json.RawMessage{}
	if e := r.appendApplications(t.Context(), x, &basis, payloads); e != nil {
		t.Fatal(e)
	}
	if len(basis.Actions) != 2 || basis.Actions[1].ID != "apply_application_browse:server" || payloads["apply_application:server"] != nil {
		t.Fatalf("unexpected process apply: %+v", basis.Actions)
	}
	if e := (ApplicationOwner{}).Validate(t.Context(), basis.Actions[1], payloads[basis.Actions[1].ID]); e != nil {
		t.Fatal(e)
	}
}

func TestApplicationBrowsePassesGeneralPlanValidation(t *testing.T) {
	p := testPrincipal()
	x := testResolution(p, 1)
	a := x.Plan.Basis.Actions[0]
	a.ID, a.Kind, a.Owner, a.Resource = "apply_application_browse:server", pc.DeclarationApplyApplication, pc.DeclarationOwnerApplication, "server"
	a.TargetRef, a.Authorization.Level = x.Plan.Basis.Target.ProjectID+"/server", 5
	q := &sr.ApplicationBrowseRequest{Owner: sr.ApplicationOwner{ProjectID: x.Plan.Basis.Target.ProjectID, NodeID: p.OriginNodeID, Resource: "server"}, Operation: "apply", Views: map[string]sr.ApplicationBrowseBinding{}}
	raw, err := canonicalValue(applicationPayload{Endpoint: "workspace/main@node-agent-system.project.application.browse", Browse: q})
	if err != nil {
		t.Fatal(err)
	}
	a.InputHash = hashBytes(raw)
	x.Plan.Basis.Bindings["server"] = pc.DeclarationBinding{Kind: pc.DeclarationApplication}
	x.Plan.Basis.Actions = []pc.DeclarationAction{a}
	x.Payloads = map[string]json.RawMessage{a.ID: raw}
	s := NewService(nil, &staticResolver{x}, allowAuth{}, map[pc.DeclarationOwner]Owner{pc.DeclarationOwnerApplication: ApplicationOwner{}})
	if _, err = s.Plan(t.Context(), p, testRequest(x)); err != nil {
		t.Fatal(err)
	}
	a.ID = "apply_application:server"
	if (ApplicationOwner{}).Validate(t.Context(), a, raw) == nil {
		t.Fatal("browse accepted under runtime action identity")
	}
	call, _, _ := applicationFixture(t)
	call.Action.ID = "apply_application_browse:" + string(call.Action.Resource)
	if (ApplicationOwner{}).Validate(t.Context(), call.Action, call.Payload) == nil {
		t.Fatal("runtime accepted under browse action identity")
	}
}
