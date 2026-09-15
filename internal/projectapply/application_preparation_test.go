package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	sr "loom.local/loom/internal/serviceregistry"
)

type preparationFixture struct {
	facts           sr.ApplicationPrerequisiteSnapshot
	project         projects.ProjectDetail
	provider        *capabilities.Provider
	sequence        []string
	failPublication bool
	plans, creates  int
}

func (f *preparationFixture) QueryPrerequisites(context.Context, sr.ApplicationPrerequisiteQuery) (sr.ApplicationPrerequisiteSnapshot, error) {
	return f.facts, nil
}
func (f *preparationFixture) PlanProvision(_ context.Context, q sr.ApplicationProvisionRequest) (sr.ApplicationProvisionPlan, error) {
	f.plans++
	p := sr.ApplicationProvisionPlan{SchemaVersion: sr.ApplicationProvisioningSchema, Request: q, PolicyRevision: "sha256:" + strings.Repeat("a", 64), GrantRevision: "sha256:" + strings.Repeat("b", 64)}
	raw, _ := json.Marshal(p)
	p.PlanID = hashBytes(raw)
	return p, nil
}
func (f *preparationFixture) Provision(_ context.Context, p sr.ApplicationProvisionPlan) (sr.ApplicationProvisionPlan, error) {
	f.sequence = append(f.sequence, "grant")
	return p, nil
}
func (f *preparationFixture) PublishPrepared(_ context.Context, q sr.ApplicationPreparedPublication) error {
	f.sequence = append(f.sequence, "publication")
	if q.Publication.ArchiveTarget == nil || f.provider == nil || q.Publication.ArchiveTarget.ProviderID != f.provider.ProviderID {
		return errors.New("publication missing registered identity")
	}
	if f.failPublication {
		return errors.New("publication interrupted")
	}
	return nil
}
func (f *preparationFixture) GetProject(context.Context, string) (projects.ProjectDetail, error) {
	return f.project, nil
}
func (f *preparationFixture) InspectProvider(context.Context, string) (capabilities.ProviderInspection, error) {
	if f.provider == nil {
		return capabilities.ProviderInspection{}, sql.ErrNoRows
	}
	return capabilities.ProviderInspection{Provider: *f.provider}, nil
}
func (f *preparationFixture) RegisterProvider(_ context.Context, _ requestctx.Context, in capabilities.RegisterProviderInput) (capabilities.Provider, error) {
	f.sequence = append(f.sequence, "service")
	f.creates++
	p := capabilities.Provider{ProviderID: "provider-fixture", ProviderKey: in.ProviderKey, CompactAddress: in.CompactAddress, ProviderType: in.ProviderType, NodeID: in.NodeRef, ScopeID: in.ScopeRef, Status: in.Status, RuntimeProfileJSON: in.RuntimeProfileJSON, Metadata: in.Metadata}
	f.provider = &p
	return p, nil
}

func managedPreparationFixture(t *testing.T) (ActionCall, pc.ApplicationDeclaration, sr.ApplicationArtifactDescriptor, *preparationFixture) {
	t.Helper()
	call, d, facts := applicationFixture(t)
	d.Credentials = nil
	d.ArtifactDescriptor = "deploy/artifact.json"
	var p applicationPayload
	_ = json.Unmarshal(call.Payload, &p)
	dataKey := pc.ResourceKey("files")
	p.Request.Manifest.Config.Values["files"] = sr.ApplicationParameter{Type: sr.ApplicationDataReference, DataRef: &dataKey}
	call.Payload, _ = canonicalValue(p)
	facts.GrantMissing = true
	facts.Publication = sr.ApplicationPrerequisitePublication{}
	facts.Credentials = map[string]sr.ApplicationPrerequisiteCredential{}
	facts.Data = map[string]sr.ApplicationPrerequisiteData{}
	root := "/nix/store/" + strings.Repeat("0", 32) + "-fixture"
	descriptor := sr.ApplicationArtifactDescriptor{SchemaVersion: "application.artifact.v1", RepositoryID: facts.RepositoryID, SourceRevision: strings.Repeat("a", 40), SourceDigest: "sha256:" + strings.Repeat("b", 64), Artifact: p.Request.Manifest.Artifact, StoreRoot: root, Launcher: root + "/bin/loom-application-launcher", Executable: root + "/bin/server", Closure: []sr.ApplicationClosurePath{{Path: root, NARHash: "sha256-" + strings.Repeat("A", 43) + "="}}, Configuration: sr.ApplicationConfigurationSchema{Schema: p.Request.Manifest.Config.Schema, Parameters: map[string]sr.ApplicationParameterSpec{"files": {Type: sr.ApplicationDataReference, Required: true}}}, DataFormat: "v1"}
	raw, _ := json.Marshal(descriptor.Closure)
	descriptor.ClosureDigest = hashBytes(raw)
	raw, _ = json.Marshal(descriptor.Configuration)
	descriptor.ConfigurationDigest = hashBytes(raw)
	f := &preparationFixture{facts: facts, project: projects.ProjectDetail{Project: projects.Project{ProjectID: call.Target.ProjectID, ProjectScopeID: "scope-project"}}}
	return call, d, descriptor, f
}

func TestManagedApplicationProtonCredentialPreparation(t *testing.T) {
	call, d, descriptor, f := managedPreparationFixture(t)
	var old applicationPayload
	_ = json.Unmarshal(call.Payload, &old)
	ref := "fixture.auth"
	d.Credentials = []string{ref}
	d.CredentialSources = map[string]string{ref: "pass://share-id/item-id/password"}
	old.Request.Manifest.Config.Values["auth"] = sr.ApplicationParameter{Type: sr.ApplicationCredentialReference, CredentialRef: &ref}
	descriptor.Configuration.Parameters["auth"] = sr.ApplicationParameterSpec{Type: sr.ApplicationCredentialReference, Required: true}
	raw, _ := json.Marshal(descriptor.Configuration)
	descriptor.ConfigurationDigest = hashBytes(raw)
	raw, _ = json.Marshal(descriptor)
	r := LocalResolver{Applications: f}
	if issues := managedApplicationPreflight(call.Target, call.Action.Resource, d, descriptor.RepositoryID, *old.Request.Manifest, &f.facts); len(issues) != 0 {
		t.Fatal(issues)
	}
	q, prep, err := r.prepareApplication(t.Context(), call.Target, call.Action.Resource, d, descriptor.RepositoryID, *old.Request.Manifest, &f.facts, raw)
	if err != nil {
		t.Fatal(err)
	}
	if q.CredentialRevisions[ref] != sr.ApplicationProtonCredentialRevision(d.CredentialSources[ref]) || prep.Grant.Request.CredentialSources[ref] != d.CredentialSources[ref] || len(f.sequence) != 0 {
		t.Fatal("credential plan lost intent or applied effects")
	}
	p := applicationPayload{Endpoint: old.Endpoint, Request: q, Preparation: prep}
	if err := validateApplicationPreparation(p); err != nil {
		t.Fatal(err)
	}
	p.Request.CredentialRevisions[ref] = "forged"
	if err := validateApplicationPreparation(p); err == nil {
		t.Fatal("credential revision substitution accepted")
	}
}

func TestManagedApplicationPublicEndpointPreparation(t *testing.T) {
	call, d, descriptor, f := managedPreparationFixture(t)
	var old applicationPayload
	_ = json.Unmarshal(call.Payload, &old)
	d.Endpoint = &pc.ApplicationEndpoint{Exposure: pc.ApplicationPublicHTTPS, Hostname: "webdav.apps.example.com"}
	old.Request.Manifest.Listener = &sr.ApplicationListener{Address: "127.0.0.1", Port: 8088}
	old.Request.Manifest.Health = sr.ApplicationHealth{Kind: sr.HealthKindHTTP, Method: "GET", Path: "/", ExpectedStatus: 401, TimeoutSeconds: 1}
	raw, _ := json.Marshal(descriptor)
	r := LocalResolver{Applications: f}
	if issues := managedApplicationPreflight(call.Target, call.Action.Resource, d, descriptor.RepositoryID, *old.Request.Manifest, &f.facts); len(issues) != 0 {
		t.Fatal(issues)
	}
	q, prep, err := r.prepareApplication(t.Context(), call.Target, call.Action.Resource, d, descriptor.RepositoryID, *old.Request.Manifest, &f.facts, raw)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Grant.Request.Endpoint == nil || q.Endpoint == nil || q.Endpoint.Hostname != d.Endpoint.Hostname || q.Endpoint.BackendPort != 8088 {
		t.Fatal("endpoint not bound to plan", q)
	}
	q.Endpoint = &sr.ApplicationEndpointRequest{Hostname: "other.apps.example.com", BackendPort: 8088}
	if validateApplicationPreparation(applicationPayload{Request: q, Preparation: prep}) == nil {
		t.Fatal("endpoint substitution accepted")
	}
	old.Request.Manifest.Health.Kind = sr.HealthKindManager
	if _, _, err := r.prepareApplication(t.Context(), call.Target, call.Action.Resource, d, descriptor.RepositoryID, *old.Request.Manifest, &f.facts, raw); err == nil {
		t.Fatal("public route accepted without owned HTTP health")
	}
}

func TestManagedApplicationPlanApplySetupReplay(t *testing.T) {
	call, d, descriptor, f := managedPreparationFixture(t)
	var old applicationPayload
	_ = json.Unmarshal(call.Payload, &old)
	raw, _ := json.Marshal(descriptor)
	r := LocalResolver{Applications: f}
	q, preparation, err := r.prepareApplication(t.Context(), call.Target, call.Action.Resource, d, descriptor.RepositoryID, *old.Request.Manifest, &f.facts, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sequence) != 0 || f.plans != 1 || q.ExpectedRevision == "" || preparation.Grant.Request.Data["files"] != "fixture.files" {
		t.Fatal("plan effects or missing intent")
	}
	p := applicationPayload{Endpoint: old.Endpoint, Request: q, Preparation: preparation}
	call.Payload, _ = canonicalValue(p)
	call.Action.InputHash = hashBytes(call.Payload)
	calls := &applicationTestCalls{}
	owner := ApplicationOwner{Calls: calls, Preparer: ManagedApplicationPreparer{Helper: f, Registry: f, Projects: f}}
	if err := owner.Validate(t.Context(), call.Action, call.Payload); err != nil {
		t.Fatal(err)
	}
	f.failPublication = true
	if _, err := owner.Apply(t.Context(), call); err == nil || calls.dispatches != 0 || strings.Join(f.sequence, ",") != "grant,service,publication" {
		t.Fatal("invalid setup ordering", err, f.sequence)
	}
	f.failPublication = false
	observed, err := owner.Apply(t.Context(), call)
	if err != nil || observed.State != Committed || calls.dispatches != 1 || f.creates != 1 || strings.Join(f.sequence, ",") != "grant,service,publication,grant,publication" {
		t.Fatalf("resume: %+v %v sequence=%v", observed, err, f.sequence)
	}
	count := len(f.sequence)
	if _, err := owner.Apply(t.Context(), call); err != nil || len(f.sequence) != count || calls.dispatches != 1 {
		t.Fatal("completed replay repeated setup")
	}
	p.Preparation.Descriptor.SourceRevision = strings.Repeat("b", 40)
	changed, _ := canonicalValue(p)
	if err := owner.Validate(t.Context(), call.Action, changed); err == nil {
		t.Fatal("descriptor substitution accepted")
	}
}

func TestManagedApplicationPlanPreservesRequirements(t *testing.T) {
	for _, name := range []string{"credentials", "protection", "quota", "public", "repository", "fenced", "data_conflict", "unknown_field"} {
		t.Run(name, func(t *testing.T) {
			call, d, descriptor, f := managedPreparationFixture(t)
			var p applicationPayload
			_ = json.Unmarshal(call.Payload, &p)
			m := *p.Request.Manifest
			switch name {
			case "credentials":
				d.Credentials = []string{"auth"}
			case "protection":
				v := d.Data["files"]
				v.Protection = "backup"
				d.Data["files"] = v
			case "quota":
				m.Data["files"] = sr.ApplicationDataRequirements{QuotaBytes: 1}
			case "public":
				d.Endpoint = &pc.ApplicationEndpoint{Exposure: pc.ApplicationPublicHTTPS, EndpointRef: "approved.example"}
			case "repository":
				descriptor.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "fenced":
				yes := true
				f.facts.Installation = sr.ApplicationPrerequisiteInstallation{Present: true, Fenced: &yes}
			case "data_conflict":
				f.facts.Data["files"] = sr.ApplicationPrerequisiteData{BindingRef: "fixture.files", Availability: "available", Custody: "conflict"}
			}
			raw, _ := json.Marshal(descriptor)
			if name == "unknown_field" {
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"trusted":true}`)
			}
			r := LocalResolver{Applications: f}
			if _, _, err := r.prepareApplication(t.Context(), call.Target, call.Action.Resource, d, f.facts.RepositoryID, m, &f.facts, raw); err == nil || f.plans != 0 {
				t.Fatal("requirement dropped or unsafe planning", err)
			}
		})
	}
}

func TestManagedApplicationResolverUsesCapturedDescriptorAndAuthority(t *testing.T) {
	call, d, descriptor, f := managedPreparationFixture(t)
	var p applicationPayload
	_ = json.Unmarshal(call.Payload, &p)
	p.Request.Manifest.Listener = &sr.ApplicationListener{Address: "127.0.0.1", Port: 9380}
	manifestRaw, _ := json.Marshal(p.Request.Manifest)
	descriptorRaw, _ := json.Marshal(descriptor)
	document := pc.ProjectDeclaration{Resources: map[pc.ResourceKey]pc.ResourceDeclaration{"server": {Kind: pc.DeclarationApplication, Application: &d}}}
	x := localDeclaration{Target: call.Target, Analysis: pc.Analysis{Loaded: &pc.LoadedProject{Declaration: &document}, Plan: pc.ProjectPlan{Declaration: &pc.DeclarationCompilation{Sources: []pc.DeclarationSourceSnapshot{{DeclarationSource: pc.DeclarationSource{Ref: d.Manifest}, Raw: manifestRaw}, {DeclarationSource: pc.DeclarationSource{Ref: d.ArtifactDescriptor}, Raw: descriptorRaw}}}}}}
	x.Input.Project.OwnerNode = "main"
	x.Authority.CanRead = true
	x.Authority.CanWrite = true
	x.Authority.Level = 5
	basis := pc.DeclarationPlanBasis{Bindings: map[pc.ResourceKey]pc.DeclarationBinding{"repo": {RepositoryID: descriptor.RepositoryID}}, Actions: []pc.DeclarationAction{{ID: "register_project:project", Authorization: pc.DeclarationAuthorization{ActorID: call.Principal.ActorID, NodeID: call.Target.OwnerNodeID, Level: 3}}}}
	payloads := map[string]json.RawMessage{}
	r := LocalResolver{Applications: f}
	if err := r.appendApplications(t.Context(), x, &basis, payloads); err != nil {
		t.Fatal(err)
	}
	if len(basis.Actions) != 2 || len(payloads) != 1 || len(f.sequence) != 0 {
		t.Fatal("application composition missing or plan mutated")
	}
	var prepared applicationPayload
	if json.Unmarshal(payloads["apply_application:server"], &prepared) != nil || prepared.Preparation == nil || prepared.Preparation.Descriptor.SourceRevision != descriptor.SourceRevision {
		t.Fatal("captured descriptor lost")
	}
	if err := (ApplicationOwner{}).Validate(t.Context(), basis.Actions[1], payloads["apply_application:server"]); err != nil {
		t.Fatal(err)
	}
	x.Authority.Level = 3
	plans := f.plans
	err := r.appendApplications(t.Context(), x, &basis, map[string]json.RawMessage{})
	var failure *Failure
	if !errors.As(err, &failure) || len(failure.PublicPreflight()) != 1 || failure.PublicPreflight()[0].Code != "application_execution_authority_required" || f.plans != plans {
		t.Fatal("helper planning substituted for actor authorization", err)
	}
	d.Credentials = []string{"auth"}
	v := d.Data["files"]
	v.Protection = "backup"
	d.Data["files"] = v
	issues := managedApplicationPreflight(call.Target, "server", d, descriptor.RepositoryID, *p.Request.Manifest, &f.facts)
	if len(issues) != 2 {
		t.Fatalf("independent unmet requirements lost: %+v", issues)
	}
}

func TestManagedApplicationCloudHistoryBinding(t *testing.T) {
	call, d, descriptor, f := managedPreparationFixture(t)
	data := d.Data["files"]
	data.Backup = "cloud_history"
	d.Data["files"] = data
	var p applicationPayload
	_ = json.Unmarshal(call.Payload, &p)
	raw, _ := json.Marshal(descriptor)
	r := LocalResolver{Applications: f}
	q, preparation, err := r.prepareApplication(t.Context(), call.Target, call.Action.Resource, d, descriptor.RepositoryID, *p.Request.Manifest, &f.facts, raw)
	if err != nil || !preparation.Grant.Request.CloudBackup || q.Data["files"].Backup != "cloud_history" {
		t.Fatalf("backup requirement lost: %+v %v", q, err)
	}
	preparation.Grant.Request.CloudBackup = false
	if validateApplicationPreparation(applicationPayload{Request: q, Preparation: preparation}) == nil {
		t.Fatal("backup requirement was unbound")
	}
}
