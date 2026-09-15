package projectapply

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

func TestApplicationPreflightReportsIndependentRequirements(t *testing.T) {
	call, d, f := applicationFixture(t)
	var payload applicationPayload
	_ = json.Unmarshal(call.Payload, &payload)
	m := *payload.Request.Manifest
	if issues := applicationPreflight(call.Target, "server", d, f.RepositoryID, m, &f); len(issues) != 0 {
		t.Fatalf("valid existing deployment changed: %+v", issues)
	}
	repo := f.RepositoryID
	f.GrantMissing, f.RepositoryID, f.LocationRevision, f.PolicyRevision = true, "", "", ""
	f.Publication = sr.ApplicationPrerequisitePublication{}
	f.Data, f.Credentials = nil, nil
	d.Endpoint = &pc.ApplicationEndpoint{Exposure: pc.ApplicationPublicHTTPS}
	data := d.Data["files"]
	data.Protection = "backup"
	d.Data["files"] = data
	m.Data["files"] = sr.ApplicationDataRequirements{QuotaBytes: 1024}
	issues := applicationPreflight(call.Target, "server", d, repo, m, &f)
	want := []string{"application_loopback_adapter_only", "application_data_protection_adapter_required", "application_quota_not_supported", "application_grant_required", "application_publication_required", "application_service_registration_required", "application_data_binding_required", "application_credential_binding_required"}
	got := []string{}
	for _, issue := range issues {
		got = append(got, issue.Code)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements lost or reordered: %v", got)
	}
	if !reflect.DeepEqual(issues, applicationPreflight(call.Target, "server", d, repo, m, &f)) {
		t.Fatal("unstable preflight")
	}
	if _, err := applicationRequest(call.Target, "server", d, repo, m, f); failureCause(err) != "application_grant_required" {
		t.Fatalf("missing grant became execution authority: %v", err)
	}
	issues = applicationPreflight(call.Target, "server", d, repo, m, nil)
	for _, issue := range issues {
		if issue.State == "missing" {
			t.Fatalf("unavailable evidence became absence: %+v", issue)
		}
	}
	if issues[len(issues)-1].Code != "application_runtime_unavailable" {
		t.Fatal(issues)
	}
}

type preflightReader struct {
	calls    []string
	snapshot sr.ApplicationPrerequisiteSnapshot
}

func (r *preflightReader) QueryPrerequisites(_ context.Context, q sr.ApplicationPrerequisiteQuery) (sr.ApplicationPrerequisiteSnapshot, error) {
	r.calls = append(r.calls, q.Owner.Resource)
	f := r.snapshot
	f.Owner = q.Owner
	return f, nil
}

func TestApplicationPreflightAllResourcesAndAuthority(t *testing.T) {
	call, d, f := applicationFixture(t)
	var p applicationPayload
	_ = json.Unmarshal(call.Payload, &p)
	p.Request.Manifest.Listener = &sr.ApplicationListener{Address: "127.0.0.1", Port: 9380}
	raw, _ := json.Marshal(p.Request.Manifest)
	document := pc.ProjectDeclaration{Resources: map[pc.ResourceKey]pc.ResourceDeclaration{}}
	for _, key := range []pc.ResourceKey{"second", "first"} {
		decl := d
		document.Resources[key] = pc.ResourceDeclaration{Kind: pc.DeclarationApplication, Application: &decl}
	}
	x := localDeclaration{Target: call.Target, Analysis: pc.Analysis{Loaded: &pc.LoadedProject{Declaration: &document}, Plan: pc.ProjectPlan{Declaration: &pc.DeclarationCompilation{Sources: []pc.DeclarationSourceSnapshot{{DeclarationSource: pc.DeclarationSource{Ref: d.Manifest}, Raw: raw}}}}}}
	x.Authority.CanRead, x.Authority.CanWrite, x.Authority.Level = true, true, 3
	basis := pc.DeclarationPlanBasis{Bindings: map[pc.ResourceKey]pc.DeclarationBinding{"repo": {RepositoryID: f.RepositoryID}}}
	f.GrantMissing, f.RepositoryID, f.LocationRevision, f.PolicyRevision = true, "", "", ""
	f.Publication = sr.ApplicationPrerequisitePublication{}
	f.Data, f.Credentials = nil, nil
	reader := &preflightReader{snapshot: f}
	r := LocalResolver{Applications: reader}
	err := r.appendApplications(t.Context(), x, &basis, map[string]json.RawMessage{})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Cause != "application_preflight_blocked" {
		t.Fatalf("%v", err)
	}
	if !slices.Equal(reader.calls, []string{"first", "second"}) {
		t.Fatalf("stopped at first application: %v", reader.calls)
	}
	for _, key := range []pc.ResourceKey{"first", "second"} {
		found := false
		for _, issue := range failure.Preflight {
			found = found || issue.Resource == key && issue.Code == "application_execution_authority_required"
		}
		if !found {
			t.Fatalf("execution authority was hidden by grant absence: %s", key)
		}
	}
	if len(basis.Actions) != 0 {
		t.Fatal("blocked applications produced execution actions")
	}
	var safe *Failure
	if !errors.As(safeError(err, pc.DeclarationTargetUnavailable, "fallback"), &safe) || !reflect.DeepEqual(safe.Preflight, failure.Preflight) {
		t.Fatal("service error boundary lost report")
	}
}

func TestApplicationPreflightPublicMessagesAreReconstructed(t *testing.T) {
	f := &Failure{Code: pc.DeclarationTargetUnavailable, Cause: "application_preflight_blocked", Preflight: []pc.DeclarationPreflightIssue{applicationPreflightIssue("application_grant_required", "server", "grant")}}
	f.Preflight[0].Message, f.Preflight[0].State = "SECRET /private/host/path", "automatically_provisionable"
	raw, _ := json.Marshal(f.PublicPreflight())
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "automatically_provisionable") {
		t.Fatal(string(raw))
	}
	f.Preflight[0].Field = "/private/host/path"
	if f.PublicPreflight() != nil {
		t.Fatal("unsafe field published")
	}
}
