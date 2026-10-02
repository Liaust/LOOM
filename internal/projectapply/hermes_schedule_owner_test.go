package projectapply

import (
	"context"
	"encoding/json"
	hs "loom.local/loom/internal/hermesschedules"
	pc "loom.local/loom/internal/projectcontracts"
	"testing"
)

func TestHermesReceiptRetainsCommittedRevisionAfterManualPause(t *testing.T) {
	call := ActionCall{Token: "token", Action: pc.DeclarationAction{ID: "reconcile_hermes_schedule:review", Resource: "review", InputHash: "hash"}, Expected: Prerequisites{Revisions: map[string]string{"hermesschedules:review": "absent"}, Bindings: map[pc.ResourceKey]pc.DeclarationBinding{"review": {Kind: pc.DeclarationHermesSchedule}}}}
	j := hs.ProjectJob{ID: "aabbccddeeff", Token: "token", InputHash: "hash", BeforeRevision: "absent", CommittedRevision: "committed", Revision: "later_manual_pause"}
	out, err := hermesReceipt(call, j)
	if err != nil || out.State != Committed || out.Receipt.Revisions["hermesschedules:review"].After != "committed" || out.Receipt.Bindings["review"].After.OwnerRef != j.ID {
		t.Fatalf("%+v %v", out, err)
	}
	j.InputHash = "changed"
	if _, err := hermesReceipt(call, j); err == nil {
		t.Fatal("accepted changed token payload")
	}
}
func TestHermesRetiredPrerequisiteClosure(t *testing.T) {
	x := localDeclaration{HermesStates: map[pc.ResourceKey]hs.ProjectJob{"review": {ID: "native", Revision: "paused", Retired: true}}}
	out := Prerequisites{Bindings: map[pc.ResourceKey]pc.DeclarationBinding{}, Revisions: map[string]string{}}
	addHermesPrerequisites(&out, x, nil)
	if len(out.Bindings) != 0 {
		t.Fatal("withdrawn resource re-enrolled")
	}
	addHermesPrerequisites(&out, x, map[pc.ResourceKey]pc.DeclarationBinding{"review": {Kind: pc.DeclarationHermesSchedule}})
	if out.Bindings["review"].OwnerRef != "native" || out.Revisions["hermesschedules:review"] != "paused" {
		t.Fatal("lost replay closure")
	}
}

func TestHermesReceiptReplayAfterCoordinatorCommit(t *testing.T) {
	for _, beforeID := range []string{"", "native"} {
		t.Run("before_id="+beforeID, func(t *testing.T) {
			call := ActionCall{Token: "token", Action: pc.DeclarationAction{ID: "reconcile_hermes_schedule:review", Resource: "review", InputHash: "hash"}, Expected: Prerequisites{Revisions: map[string]string{"hermesschedules:review": "before"}, Bindings: map[pc.ResourceKey]pc.DeclarationBinding{"review": {Kind: pc.DeclarationHermesSchedule, OwnerRef: beforeID}}}}
			job := hs.ProjectJob{ID: "native", Token: "token", InputHash: "hash", BeforeID: beforeID, BeforeRevision: "before", CommittedRevision: "after"}
			first, err := hermesReceipt(call, job)
			if err != nil {
				t.Fatal(err)
			}
			call.Expected.Revisions["hermesschedules:review"] = "after"
			call.Expected.Bindings["review"] = pc.DeclarationBinding{Kind: pc.DeclarationHermesSchedule, OwnerRef: "native"}
			replayed, err := hermesReceipt(call, job)
			if err != nil || !same(first, replayed) {
				t.Fatalf("receipt changed on replay: %+v %+v %v", first, replayed, err)
			}
			if beforeID == "" {
				call.Expected.Bindings["review"] = pc.DeclarationBinding{Kind: pc.DeclarationHermesSchedule}
				if _, err := hermesReceipt(call, job); err == nil {
					t.Fatal("accepted mixed before binding and after revision")
				}
			}
			call.Expected.Bindings["review"] = pc.DeclarationBinding{Kind: pc.DeclarationHermesSchedule, OwnerRef: "other"}
			if _, err := hermesReceipt(call, job); err == nil {
				t.Fatal("accepted another native identity")
			}
			call.Expected.Revisions["hermesschedules:review"] = "unrelated"
			call.Expected.Bindings["review"] = pc.DeclarationBinding{Kind: pc.DeclarationHermesSchedule, OwnerRef: "native"}
			if _, err := hermesReceipt(call, job); err == nil {
				t.Fatal("accepted unrelated revision")
			}
		})
	}
}

func TestHermesPlanNeverAddsLOOMTimer(t *testing.T) {
	spec := &pc.HermesScheduleDeclaration{Profile: "mina", Prompt: "Read README.md", EveryMinutes: 60}
	x := localDeclaration{Analysis: pc.Analysis{Loaded: &pc.LoadedProject{Declaration: &pc.ProjectDeclaration{Resources: map[pc.ResourceKey]pc.ResourceDeclaration{"review": {Kind: pc.DeclarationHermesSchedule, HermesSchedule: spec}}}}}, Target: pc.DeclarationTarget{ProjectID: "project_test", ProjectRoot: "/owned/project"}}
	x.Input.Project.OwnerNode = "main"
	r := LocalResolver{HermesSource: hs.Source{Host: "main", Profile: "mina", Revision: hs.NativeRevision}}
	basis := pc.DeclarationPlanBasis{Bindings: map[pc.ResourceKey]pc.DeclarationBinding{"review": {Kind: pc.DeclarationHermesSchedule}}, Actions: []pc.DeclarationAction{{ID: "register_project:project"}}}
	payloads := map[string]json.RawMessage{}
	if err := r.appendHermesSchedules(x, &basis, payloads); err != nil {
		t.Fatal(err)
	}
	if err := r.appendSchedules(context.Background(), Principal{}, x, &basis, payloads); err != nil {
		t.Fatal(err)
	}
	if len(basis.Actions) != 2 || basis.Actions[1].Owner != pc.DeclarationOwnerHermes {
		t.Fatalf("wrong timer owner: %+v", basis.Actions)
	}
	var payload hermesOwnerPayload
	if err := json.Unmarshal(payloads[basis.Actions[1].ID], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Spec.Workdir != "/owned/project" || payload.Spec.Status != "paused" {
		t.Fatalf("lost bound context/default pause: %+v", payload)
	}
}
