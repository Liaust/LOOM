package projectapply

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"loom.local/loom/internal/capabilities"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	sr "loom.local/loom/internal/serviceregistry"
)

type applicationPreparation struct {
	Grant               sr.ApplicationProvisionPlan      `json:"grant"`
	Descriptor          sr.ApplicationArtifactDescriptor `json:"descriptor"`
	ExpectedPublication string                           `json:"expected_publication"`
}

type ApplicationProvisioner interface {
	PlanProvision(context.Context, sr.ApplicationProvisionRequest) (sr.ApplicationProvisionPlan, error)
	Provision(context.Context, sr.ApplicationProvisionPlan) (sr.ApplicationProvisionPlan, error)
	PublishPrepared(context.Context, sr.ApplicationPreparedPublication) error
}

type ApplicationPreparer interface {
	Prepare(context.Context, requestctx.Context, ActionCall, applicationPayload) error
}

func (r *LocalResolver) prepareApplication(ctx context.Context, target pc.DeclarationTarget, key pc.ResourceKey, d pc.ApplicationDeclaration, repo string, m sr.ApplicationManifest, f *sr.ApplicationPrerequisiteSnapshot, raw []byte) (sr.ApplicationRuntimeRequest, *applicationPreparation, error) {
	var q sr.ApplicationRuntimeRequest
	descriptor, err := sr.ParseApplicationDescriptor(raw)
	if err != nil {
		return q, nil, fail(pc.DeclarationInvalid, "application_descriptor_invalid")
	}
	owner := sr.ApplicationOwner{ProjectID: target.ProjectID, NodeID: target.OwnerNodeID, Resource: string(key)}
	if f == nil {
		return q, nil, fail(pc.DeclarationTargetUnavailable, "application_runtime_unavailable")
	}
	if f.SchemaVersion != sr.ApplicationPrerequisiteSchema || f.Owner != owner || repo == "" || descriptor.RepositoryID != repo || descriptor.Artifact != m.Artifact || descriptor.Configuration.Schema != m.Config.Schema || descriptor.Artifact.Platform != f.Platform || (!f.GrantMissing && (f.RepositoryID != repo || f.LocationRevision != target.LocationRevision)) {
		return q, nil, fail(pc.DeclarationIdentityConflict, "application_project_binding_changed")
	}
	if !managedApplicationEndpoint(d) {
		return q, nil, fail(pc.DeclarationUnsupported, "application_loopback_adapter_only")
	}
	if f.Installation.Present && (f.Installation.Fenced == nil || *f.Installation.Fenced || f.Installation.Retired == nil || *f.Installation.Retired) {
		return q, nil, fail(pc.DeclarationTargetUnavailable, "application_installation_fenced")
	}
	if err := sr.ValidatePreparedApplication(descriptor, m); err != nil {
		return q, nil, applicationFailure(err)
	}
	provision := sr.ApplicationProvisionRequest{SchemaVersion: sr.ApplicationProvisioningSchema, Owner: owner, RepositoryID: repo, LocationRevision: target.LocationRevision, Data: map[string]string{}}
	if d.Endpoint != nil && d.Endpoint.Exposure == pc.ApplicationPublicHTTPS {
		if m.Listener == nil || m.Listener.Address != "127.0.0.1" || m.Listener.Port < 1024 || m.Health.Kind != sr.HealthKindHTTP {
			return q, nil, fail(pc.DeclarationInvalid, "application_public_listener_invalid")
		}
		provision.Endpoint = &sr.ApplicationEndpointRequest{Hostname: d.Endpoint.Hostname, BackendPort: m.Listener.Port}
	}
	data := map[string]sr.ApplicationPrerequisiteData{}
	for key, value := range d.Data {
		provision.CloudBackup = provision.CloudBackup || value.Backup == "cloud_history"
		provision.Data[string(key)] = value.BindingRef
		if current, exists := f.Data[string(key)]; exists {
			data[string(key)] = current
		} else {
			// This is desired allocation input, never an observed availability claim.
			data[string(key)] = sr.ApplicationPrerequisiteData{BindingRef: value.BindingRef, Availability: "planned", Custody: "planned"}
		}
	}
	q = sr.ApplicationRuntimeRequest{SchemaVersion: sr.ApplicationRuntimeSchema, Operation: "apply", Owner: owner, ExpectedInstallationRevision: f.Installation.Revision, LocationRevision: target.LocationRevision, DescriptorDigest: sr.ApplicationDescriptorDigest(descriptor), Manifest: &m, Data: map[string]sr.ApplicationDataRequest{}, CredentialRevisions: map[string]string{}}
	q.Endpoint = provision.Endpoint
	provision.CredentialSources = maps.Clone(d.CredentialSources)
	credentials := map[string]sr.ApplicationPrerequisiteCredential{}
	for ref, source := range d.CredentialSources {
		if _, _, ok := pc.ApplicationCredentialSourceShare(source); !ok {
			return q, nil, fail(pc.DeclarationInvalid, "application_credential_source_invalid")
		}
		credentials[ref] = sr.ApplicationPrerequisiteCredential{Revision: sr.ApplicationProtonCredentialRevision(source), Availability: "planned"}
	}
	q, err = applicationRequirements(q, d, m, data, credentials)
	if err != nil {
		return q, nil, err
	}
	helper, ok := r.Applications.(ApplicationProvisioner)
	if !ok {
		return q, nil, fail(pc.DeclarationTargetUnavailable, "application_preparer_not_configured")
	}
	plan, err := helper.PlanProvision(ctx, provision)
	if err != nil {
		return q, nil, applicationFailure(err)
	}
	q.PolicyRevision, q.ExpectedRevision = plan.PolicyRevision, sr.ApplicationPreparedRevision(plan, descriptor)
	preparation := &applicationPreparation{Grant: plan, Descriptor: descriptor, ExpectedPublication: f.Publication.Revision}
	if err := validateApplicationPreparation(applicationPayload{Request: q, Preparation: preparation}); err != nil {
		return q, nil, err
	}
	return q, preparation, nil
}

func validateApplicationPreparation(p applicationPayload) error {
	x, q := p.Preparation, p.Request
	if x == nil || q.Manifest == nil || sr.ValidatePreparedApplication(x.Descriptor, *q.Manifest) != nil || x.Grant.Validate() != nil || x.Grant.Request.Owner != q.Owner || x.Grant.Request.RepositoryID != x.Descriptor.RepositoryID || x.Grant.Request.LocationRevision != q.LocationRevision || x.Grant.PolicyRevision != q.PolicyRevision || q.DescriptorDigest != sr.ApplicationDescriptorDigest(x.Descriptor) || q.ExpectedRevision != sr.ApplicationPreparedRevision(x.Grant, x.Descriptor) || len(q.CredentialRevisions) != len(x.Grant.Request.CredentialSources) || len(q.Data) != len(x.Grant.Request.Data) {
		return fail(pc.DeclarationInvalid, "application_preparation_invalid")
	}
	for key, data := range q.Data {
		if x.Grant.Request.Data[key] != data.BindingRef || (data.Backup != "" && (data.Backup != "cloud_history" || !x.Grant.Request.CloudBackup)) {
			return fail(pc.DeclarationInvalid, "application_preparation_invalid")
		}
	}
	if !sameEndpoint(q.Endpoint, x.Grant.Request.Endpoint) || (q.Endpoint != nil && (q.Manifest.Listener == nil || q.Manifest.Listener.Address != "127.0.0.1" || q.Manifest.Listener.Port != q.Endpoint.BackendPort)) {
		return fail(pc.DeclarationInvalid, "application_preparation_invalid")
	}
	for ref, source := range x.Grant.Request.CredentialSources {
		if q.CredentialRevisions[ref] != sr.ApplicationProtonCredentialRevision(source) {
			return fail(pc.DeclarationInvalid, "application_preparation_invalid")
		}
	}
	return nil
}

func sameEndpoint(a, b *sr.ApplicationEndpointRequest) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func managedApplicationEndpoint(d pc.ApplicationDeclaration) bool {
	return d.Endpoint == nil || (d.Endpoint.Exposure == pc.ApplicationLoopback && d.Endpoint.EndpointRef == "" && d.Endpoint.Hostname == "") || (d.ArtifactDescriptor != "" && d.Endpoint.Exposure == pc.ApplicationPublicHTTPS && d.Endpoint.EndpointRef == "" && pc.ValidApplicationHostname(d.Endpoint.Hostname))
}

type ApplicationProjectReader interface {
	GetProject(context.Context, string) (projects.ProjectDetail, error)
}

// Uses the existing project journal for ordering/retry and the existing owners
// for durable effects. No separate setup operation or agent command is needed.
type ManagedApplicationPreparer struct {
	Helper   ApplicationProvisioner
	Registry sr.ApplicationProviderRegistry
	Projects ApplicationProjectReader
}

func (m ManagedApplicationPreparer) Prepare(ctx context.Context, req requestctx.Context, call ActionCall, p applicationPayload) error {
	if nilInterface(m.Helper) || nilInterface(m.Registry) || nilInterface(m.Projects) || call.Fence == nil {
		return fail(pc.DeclarationTargetUnavailable, "application_preparer_not_configured")
	}
	if err := validateApplicationPreparation(p); err != nil {
		return err
	}
	project, err := m.Projects.GetProject(ctx, p.Request.Owner.ProjectID)
	if err != nil {
		return err
	}
	if project.Project.ProjectID != p.Request.Owner.ProjectID || project.Project.ProjectScopeID == "" {
		return fail(pc.DeclarationIdentityConflict, "application_project_binding_changed")
	}
	address, err := capabilities.ParseAddress(p.Endpoint)
	if err != nil {
		return err
	}
	nodeKey := strings.TrimPrefix(address.ScopePath, "workspace/")
	if err = call.Fence.Check(ctx); err != nil {
		return err
	}
	granted, err := m.Helper.Provision(ctx, p.Preparation.Grant)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(granted)
	b, _ := json.Marshal(p.Preparation.Grant)
	if !sameJSON(a, b) {
		return fail(pc.DeclarationOwnerFailed, "application_grant_receipt_invalid")
	}
	if err = call.Fence.Check(ctx); err != nil {
		return err
	}
	target, err := sr.RegisterApplicationService(ctx, req, m.Registry, p.Request.Owner, nodeKey, project.Project.ProjectScopeID)
	if err != nil {
		return err
	}
	if err = call.Fence.Check(ctx); err != nil {
		return err
	}
	return m.Helper.PublishPrepared(ctx, sr.ApplicationPreparedPublication{GrantRevision: granted.GrantRevision, Publication: sr.ApplicationPublication{Owner: p.Request.Owner, ExpectedRevision: p.Preparation.ExpectedPublication, Revision: p.Request.ExpectedRevision, LocationRevision: p.Request.LocationRevision, PolicyRevision: p.Request.PolicyRevision, Descriptor: p.Preparation.Descriptor, ArchiveTarget: &target}})
}
