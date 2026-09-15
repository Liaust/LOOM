package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	sr "loom.local/loom/internal/serviceregistry"
)

func TestApplicationCredentialFailureRetainsPublicCause(t *testing.T) {
	for _, cause := range []string{"application.credential.session_unavailable", "application.credential.proton_failed", "private-fixture-value"} {
		t.Run(cause, func(t *testing.T) {
			err := fmt.Errorf("private wrapping detail: %w", applicationFailure(errors.New(cause)))
			got := publicError(safeError(err, pc.DeclarationOwnerFailed, "owner_apply_failed"), nil, "apply_application:webdav")
			want := cause
			if cause == "private-fixture-value" {
				want = "application_runtime_unavailable"
			}
			if got.CauseCode != want || !got.Retryable || got.ActionID != "apply_application:webdav" {
				t.Fatalf("lost application failure: %+v", got)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "private") {
				t.Fatal("private resolver detail escaped")
			}
		})
	}
}

func applicationFixture(t *testing.T) (ActionCall, pc.ApplicationDeclaration, sr.ApplicationPrerequisiteSnapshot) {
	t.Helper()
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	target := pc.DeclarationTarget{ProjectID: "project_" + id, OwnerNodeID: "node_" + id, ProjectRoot: "/box/project", LocationRevision: "location-a"}
	owner := sr.ApplicationOwner{ProjectID: target.ProjectID, NodeID: target.OwnerNodeID, Resource: "server"}
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact := sr.ApplicationArtifact{Ref: "fixture.server", Digest: digest, Platform: "x86_64-linux"}
	m := sr.ApplicationManifest{Kind: "loom.application", SchemaVersion: sr.ApplicationContractSchema, Artifact: artifact, Process: sr.ApplicationProcess{Manager: sr.ManagerSystemd, User: "fixture", Restart: "on-failure", MemoryMaxBytes: 64 << 20}, Config: sr.ApplicationConfiguration{Schema: "fixture.config", Values: map[string]sr.ApplicationParameter{}}, Health: sr.ApplicationHealth{Kind: sr.HealthKindManager}, Data: map[pc.ResourceKey]sr.ApplicationDataRequirements{"files": {}}}
	d := pc.ApplicationDeclaration{Repository: "repo", Manifest: "deploy/app.json", Data: map[pc.ResourceKey]pc.ApplicationData{"files": {BindingRef: "fixture.files"}}, Credentials: []string{"fixture.auth"}}
	yes := true
	f := sr.ApplicationPrerequisiteSnapshot{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: owner, RepositoryID: "repo_" + id, LocationRevision: target.LocationRevision, PolicyRevision: "policy-a", Platform: artifact.Platform, Publication: sr.ApplicationPrerequisitePublication{Present: true, Revision: "published-a", PolicyRevision: "policy-a", LocationRevision: target.LocationRevision, GrantMatches: &yes, Artifact: &sr.ApplicationPrerequisiteArtifact{RepositoryID: "repo_" + id, Artifact: artifact, DescriptorDigest: digest, ConfigurationSchema: m.Config.Schema}}, Data: map[string]sr.ApplicationPrerequisiteData{"files": {BindingRef: "fixture.files", Availability: "missing", Custody: "unknown"}}, Credentials: map[string]sr.ApplicationPrerequisiteCredential{"fixture.auth": {Revision: "credential-a", Availability: "available"}}}
	f.Publication.ArchiveTarget = &projectquiescence.Target{Facet: projectquiescence.FacetServices, Kind: projectquiescence.TargetKindService, OwnerNode: "main", ProviderID: "provider_fixture", ProviderKey: "fixture", ProviderAddress: "workspace/main@fixture", RuntimeProfileDigest: digest, AllowlistKey: owner.AllowlistKey(), Manager: "systemd", Unit: owner.Unit()}
	q, err := applicationRequest(target, "server", d, f.RepositoryID, m, f)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := canonicalValue(applicationPayload{Endpoint: "workspace/main@node-agent-system.project.application.apply", Request: q})
	a := pc.DeclarationAction{ID: "apply_application:server", Kind: pc.DeclarationApplyApplication, Owner: pc.DeclarationOwnerApplication, Resource: "server", TargetRef: target.ProjectID + "/server", InputHash: hashBytes(payload)}
	return ActionCall{Principal: Principal{ActorID: "actor_" + id, OriginNodeID: target.OwnerNodeID}, OperationID: "job_" + id, Target: target, Action: a, Payload: payload, Token: "declaration-token", Fence: applicationTestFence{}}, d, f
}

type applicationTestFence struct{ err error }

func TestPreparedApplicationRejectsCollidingMainProvider(t *testing.T) {
	call, _, _ := applicationFixture(t)
	if err := (ApplicationOwner{}).Validate(context.Background(), call.Action, call.Payload); err != nil {
		t.Fatal(err)
	}
	var payload applicationPayload
	_ = json.Unmarshal(call.Payload, &payload)
	payload.Endpoint = "workspace/main@system.project.application.apply"
	raw, _ := json.Marshal(payload)
	if err := (ApplicationOwner{}).Validate(context.Background(), call.Action, raw); err == nil {
		t.Fatal("accepted Main's colliding system provider")
	}
}

func (f applicationTestFence) Check(context.Context) error { return f.err }

type applicationTestCalls struct {
	stored     *routing.CapabilityCall
	err        error
	dispatches int
}

func (f *applicationTestCalls) FindCapabilityCallByToken(context.Context, string, string) (routing.CapabilityCall, error) {
	if f.err != nil {
		return routing.CapabilityCall{}, f.err
	}
	if f.stored == nil {
		return routing.CapabilityCall{}, sql.ErrNoRows
	}
	return *f.stored, nil
}
func (f *applicationTestCalls) Call(_ context.Context, req requestctx.Context, input routing.CapabilityCallInput, token string) (routing.CapabilityCallOutcome, error) {
	f.dispatches++
	var q sr.ApplicationRuntimeRequest
	_ = json.Unmarshal(input.Input, &q)
	receipt, _ := json.Marshal(sr.ApplicationRuntimeReceipt{SchemaVersion: sr.ApplicationRuntimeSchema, Owner: q.Owner, OperationToken: token, InputDigest: q.Digest(), InstallationRevision: q.Digest(), Revision: q.ExpectedRevision, State: "succeeded", Applied: true, EvidenceRef: "application-receipt:fixture"})
	f.stored = &routing.CapabilityCall{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID, TargetNodeID: q.Owner.NodeID, Operation: "capability:" + input.Target, InputJSON: input.Input, ResultJSON: receipt, Status: routing.CapabilityCallStatusCompleted}
	return routing.CapabilityCallOutcome{CapabilityCall: *f.stored}, nil
}

func TestPreparedApplicationPrerequisites(t *testing.T) {
	for name, edit := range map[string]func(*pc.ApplicationDeclaration, *sr.ApplicationPrerequisiteSnapshot){
		"unregistered_service": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			f.Publication.ArchiveTarget = nil
		},
		"unpublished": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			f.Publication.Present = false
		},
		"repository": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			f.RepositoryID = "repo_other"
		},
		"location": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			f.LocationRevision = "moved"
		},
		"credentials": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			f.Credentials["fixture.auth"] = sr.ApplicationPrerequisiteCredential{Availability: "missing"}
		},
		"data": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) { delete(f.Data, "files") },
		"public": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			d.Endpoint = &pc.ApplicationEndpoint{Exposure: pc.ApplicationPublicHTTPS}
		},
		"protection": func(d *pc.ApplicationDeclaration, f *sr.ApplicationPrerequisiteSnapshot) {
			v := d.Data["files"]
			v.Protection = "backup"
			d.Data["files"] = v
		},
	} {
		t.Run(name, func(t *testing.T) {
			call, d, f := applicationFixture(t)
			var payload applicationPayload
			_ = json.Unmarshal(call.Payload, &payload)
			repo := f.RepositoryID
			edit(&d, &f)
			if _, err := applicationRequest(call.Target, "server", d, repo, *payload.Request.Manifest, f); err == nil {
				t.Fatal("accepted invalid prerequisite")
			}
		})
	}
}

func TestPreparedApplicationDispatchReplayAndUncertainty(t *testing.T) {
	call, _, _ := applicationFixture(t)
	f := &applicationTestCalls{}
	o := ApplicationOwner{Calls: f}
	first, err := o.Apply(t.Context(), call)
	if err != nil || first.State != Committed || first.Receipt == nil || f.dispatches != 1 {
		t.Fatalf("apply=%+v err=%v calls=%d", first, err, f.dispatches)
	}
	again, err := o.Apply(t.Context(), call)
	if err != nil || again.State != Committed || f.dispatches != 1 {
		t.Fatalf("replay=%+v %v", again, err)
	}
	f.stored.Status = routing.CapabilityCallStatusDispatched
	obs, err := o.Apply(t.Context(), call)
	if err != nil || obs.State != Pending || f.dispatches != 1 {
		t.Fatal("pending dispatch repeated")
	}
	f.stored.Status = routing.CapabilityCallStatusFailed
	obs, err = o.Apply(t.Context(), call)
	if err != nil || obs.State != Uncertain || f.dispatches != 1 {
		t.Fatal("failed delivery treated as absent")
	}
	f.err = errors.New("unavailable")
	if _, err = o.Apply(t.Context(), call); err == nil || f.dispatches != 1 {
		t.Fatal("unavailable delivery repeated")
	}
}

func TestPreparedApplicationReceiptAndFence(t *testing.T) {
	call, _, _ := applicationFixture(t)
	f := &applicationTestCalls{}
	o := ApplicationOwner{Calls: f}
	call.Fence = applicationTestFence{err: errors.New("lost")}
	if _, err := o.Apply(t.Context(), call); err == nil || f.dispatches != 0 {
		t.Fatal("lost fence dispatched")
	}
	call.Fence = applicationTestFence{}
	if _, err := o.Apply(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	var receipt sr.ApplicationRuntimeReceipt
	_ = json.Unmarshal(f.stored.ResultJSON, &receipt)
	receipt.InputDigest = "substituted"
	f.stored.ResultJSON, _ = json.Marshal(receipt)
	if _, err := o.Observe(t.Context(), call); err == nil {
		t.Fatal("substituted receipt accepted")
	}
	f.stored.OriginNodeID = "other"
	if _, err := o.Observe(t.Context(), call); err == nil {
		t.Fatal("foreign delivery accepted")
	}
}

type applicationContinuationCalls struct {
	stored     map[string]routing.CapabilityCall
	dispatches int
	failures   int
}

func (f *applicationContinuationCalls) FindCapabilityCallByToken(_ context.Context, _ string, token string) (routing.CapabilityCall, error) {
	if call, ok := f.stored[token]; ok {
		return call, nil
	}
	return routing.CapabilityCall{}, sql.ErrNoRows
}

func (f *applicationContinuationCalls) Call(_ context.Context, req requestctx.Context, input routing.CapabilityCallInput, token string) (routing.CapabilityCallOutcome, error) {
	f.dispatches++
	if _, exists := f.stored[token]; exists {
		return routing.CapabilityCallOutcome{}, errors.New("fixture refuses duplicate delivery")
	}
	var q sr.ApplicationRuntimeRequest
	_ = json.Unmarshal(input.Input, &q)
	r := sr.ApplicationRuntimeReceipt{SchemaVersion: sr.ApplicationRuntimeSchema, Owner: q.Owner, OperationToken: q.OperationToken, InputDigest: q.Digest(), InstallationRevision: q.Digest(), Revision: q.ExpectedRevision, State: "succeeded", Applied: true, EvidenceRef: "application-receipt:fixture"}
	call := routing.CapabilityCall{CapabilityCallID: ids.NewCapabilityCallID(), IdempotencyKey: &token, ActorID: req.ActorID, OriginNodeID: req.OriginNodeID, TargetNodeID: q.Owner.NodeID, Operation: "capability:" + input.Target, InputJSON: input.Input, Status: routing.CapabilityCallStatusCompleted}
	if f.failures > 0 {
		f.failures--
		r.State, r.ErrorCode = "partial", "application.health.process_unproven"
		call.Status, call.ErrorCode = routing.CapabilityCallStatusFailed, &r.ErrorCode
	}
	call.ResultJSON, _ = json.Marshal(r)
	f.stored[token] = call
	return routing.CapabilityCallOutcome{CapabilityCall: call}, nil
}

func TestApplicationPartialDeliveryContinuation(t *testing.T) {
	call, _, _ := applicationFixture(t)
	f := &applicationContinuationCalls{stored: map[string]routing.CapabilityCall{}, failures: 2}
	o := ApplicationOwner{Calls: f}
	first, err := o.Apply(t.Context(), call)
	if err != nil || first.State != Resumable || first.CauseCode != "application.health.process_unproven" || f.dispatches != 1 {
		t.Fatalf("first=%+v err=%v deliveries=%d", first, err, f.dispatches)
	}
	original := f.stored[call.Token]
	second, err := o.Apply(t.Context(), call)
	if err != nil || second.State != Resumable || f.dispatches != 2 {
		t.Fatalf("one continuation per apply: %+v err=%v deliveries=%d", second, err, f.dispatches)
	}
	last, err := o.Apply(t.Context(), call)
	if err != nil || last.State != Committed || last.Receipt.Token != call.Token || f.dispatches != 3 {
		t.Fatalf("completion=%+v err=%v deliveries=%d", last, err, f.dispatches)
	}
	for token, delivery := range f.stored {
		var receipt sr.ApplicationRuntimeReceipt
		_ = json.Unmarshal(delivery.ResultJSON, &receipt)
		if !sameJSON(original.InputJSON, delivery.InputJSON) || receipt.OperationToken != call.Token {
			t.Fatalf("continuation changed original helper input at %s", token)
		}
	}
	if !sameJSON(original.ResultJSON, f.stored[call.Token].ResultJSON) || f.stored[call.Token].Status != routing.CapabilityCallStatusFailed {
		t.Fatal("original failed delivery was rewritten")
	}
	if _, err = o.Apply(t.Context(), call); err != nil || f.dispatches != 3 {
		t.Fatal("completed continuation replay dispatched", err)
	}
}

func TestApplicationPartialContinuationRequiresExactReceiptAndFence(t *testing.T) {
	for _, scenario := range []string{"pending", "missing_receipt", "foreign_input", "foreign_helper_token", "not_applied", "lost_fence"} {
		t.Run(scenario, func(t *testing.T) {
			call, _, _ := applicationFixture(t)
			f := &applicationContinuationCalls{stored: map[string]routing.CapabilityCall{}, failures: 1}
			o := ApplicationOwner{Calls: f}
			if obs, err := o.Apply(t.Context(), call); err != nil || obs.State != Resumable {
				t.Fatalf("setup=%+v %v", obs, err)
			}
			delivery := f.stored[call.Token]
			var receipt sr.ApplicationRuntimeReceipt
			_ = json.Unmarshal(delivery.ResultJSON, &receipt)
			switch scenario {
			case "pending":
				delivery.Status = routing.CapabilityCallStatusDispatched
			case "missing_receipt":
				delivery.ResultJSON = nil
			case "foreign_input":
				delivery.InputJSON = json.RawMessage(`{}`)
			case "foreign_helper_token":
				receipt.OperationToken = "other"
				delivery.ResultJSON, _ = json.Marshal(receipt)
			case "not_applied":
				receipt.Applied = false
				delivery.ResultJSON, _ = json.Marshal(receipt)
			case "lost_fence":
				call.Fence = applicationTestFence{err: errors.New("lost")}
			}
			f.stored[call.Token] = delivery
			obs, err := o.Apply(t.Context(), call)
			if f.dispatches != 1 || (err == nil && obs.State != Pending && obs.State != Uncertain) {
				t.Fatalf("unsafe retry=%+v err=%v dispatches=%d", obs, err, f.dispatches)
			}
		})
	}
}

type applicationTestInspector struct {
	result sr.ApplicationRuntimeReceipt
	err    error
}

func (f applicationTestInspector) Execute(_ context.Context, q sr.ApplicationRuntimeRequest) (sr.ApplicationRuntimeReceipt, error) {
	if q.Operation != "inspect" {
		panic("status attempted a mutation")
	}
	return f.result, f.err
}

func TestPreparedApplicationStatusSeparatesReadiness(t *testing.T) {
	call, _, _ := applicationFixture(t)
	yes := true
	current := &sr.ApplicationCurrentObservation{State: "observed", Readiness: sr.ApplicationReadiness{Process: "satisfied", Protocol: "not_applicable"}}
	f := applicationTestInspector{result: sr.ApplicationRuntimeReceipt{DesiredMatches: &yes, InstallationRevision: "installed", Generation: "generation", Current: current}}
	o := ApplicationOwner{Inspector: f}
	r, err := o.ResourceStatus(t.Context(), call.Action, call.Payload)
	if err != nil || r.Applied.State != pc.DeclarationSatisfied || r.Healthy.State != pc.DeclarationSatisfied || r.Protected.State != pc.DeclarationUnknown || r.Verified.State != pc.DeclarationUnknown {
		t.Fatalf("status=%+v %v", r, err)
	}
	current.ErrorCode = "application.health.process_unproven"
	r, err = o.ResourceStatus(t.Context(), call.Action, call.Payload)
	if err != nil || r.Applied.State != pc.DeclarationSatisfied || r.Healthy.State != pc.DeclarationFailed {
		t.Fatalf("stale health=%+v %v", r, err)
	}
	yes = false
	r, err = o.ResourceStatus(t.Context(), call.Action, call.Payload)
	if err != nil || r.Applied.State != pc.DeclarationPending || r.Healthy.State != pc.DeclarationUnknown {
		t.Fatal("different configuration reported current")
	}
}
