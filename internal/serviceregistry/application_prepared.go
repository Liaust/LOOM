package serviceregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/requestctx"
)

const ApplicationRegistrationSource = "project.application"

type ApplicationRegistrationMetadata struct {
	Source    string           `json:"source"`
	ProjectID string           `json:"project_id"`
	Owner     ApplicationOwner `json:"owner"`
}

type ApplicationProviderRegistry interface {
	RegisterProvider(context.Context, requestctx.Context, capabilities.RegisterProviderInput) (capabilities.Provider, error)
	InspectProvider(context.Context, string) (capabilities.ProviderInspection, error)
}

type ApplicationPublicationReader interface {
	QueryPrerequisites(context.Context, ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error)
}

func ManagedApplicationArchiveTarget(ctx context.Context, reader ApplicationPublicationReader, p capabilities.Provider) (projectquiescence.Target, error) {
	var metadata ApplicationRegistrationMetadata
	var profile RuntimeProfile
	if reader == nil || applicationDecodeJSON(p.Metadata, &metadata) != nil || metadata.Source != ApplicationRegistrationSource || metadata.ProjectID != metadata.Owner.ProjectID || p.NodeID != metadata.Owner.NodeID || p.ProviderType != capabilities.ProviderTypeService || applicationDecodeJSON(p.RuntimeProfileJSON, &profile) != nil || applicationSHA(profile) != applicationSHA(ApplicationServiceProfile(metadata.Owner)) {
		return projectquiescence.Target{}, applicationError("service.archive_identity")
	}
	address, err := capabilities.ParseProviderAddress(p.CompactAddress)
	if err != nil {
		return projectquiescence.Target{}, applicationError("service.archive_identity")
	}
	target, err := ApplicationServiceTarget(metadata.Owner, address.ScopePath, p.ProviderID)
	if err != nil || target.ProviderKey != p.ProviderKey || target.ProviderAddress != p.CompactAddress {
		return target, applicationError("service.archive_identity")
	}
	facts, err := reader.QueryPrerequisites(ctx, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: metadata.Owner})
	if err != nil {
		return target, err
	}
	publication := facts.Publication
	if facts.SchemaVersion != ApplicationPrerequisiteSchema || facts.Owner != metadata.Owner || facts.GrantMissing || !publication.Present || publication.GrantMatches == nil || !*publication.GrantMatches || publication.ArchiveTarget == nil || *publication.ArchiveTarget != target {
		return target, applicationError("service.archive_identity")
	}
	return target, nil
}

// The managed service is inspected through application status, and stopped by
// the existing archive helper. Do not advertise generic service-manager calls
// which would still require a separate host allowlist.
func ApplicationServiceProfile(owner ApplicationOwner) RuntimeProfile {
	p, _ := BuildRuntimeProfile(RuntimeProfileInput{Manager: ManagerSystemd, Unit: owner.Unit(), ServiceClass: ServiceClassProject, Operations: []Operation{}, Health: RuntimeHealth{Kind: HealthKindManager}})
	return p
}

func ApplicationServiceTarget(owner ApplicationOwner, nodeKey, providerID string) (projectquiescence.Target, error) {
	t := projectquiescence.Target{Facet: projectquiescence.FacetServices, Kind: projectquiescence.TargetKindService, OwnerNode: nodeKey, ProviderKey: owner.Instance(), ProviderAddress: nodeKey + "@" + owner.Instance(), ProviderID: providerID, RuntimeProfileDigest: applicationSHA(ApplicationServiceProfile(owner)), AllowlistKey: owner.AllowlistKey(), Manager: "systemd", Unit: owner.Unit()}
	address, err := capabilities.ParseProviderAddress(t.ProviderAddress)
	if !owner.valid() || err != nil || address.ScopePath != nodeKey || projectquiescence.ValidateTarget(t) != nil {
		return t, applicationError("service.identity_invalid")
	}
	return t, nil
}

func RegisterApplicationService(ctx context.Context, req requestctx.Context, registry ApplicationProviderRegistry, owner ApplicationOwner, nodeKey, scopeID string) (projectquiescence.Target, error) {
	target, err := ApplicationServiceTarget(owner, nodeKey, "pending-provider")
	if err != nil || scopeID == "" {
		return projectquiescence.Target{}, applicationError("service.identity_invalid")
	}
	metadata := ApplicationRegistrationMetadata{Source: ApplicationRegistrationSource, ProjectID: owner.ProjectID, Owner: owner}
	profile := ApplicationServiceProfile(owner)
	inspection, err := registry.InspectProvider(ctx, target.ProviderAddress)
	if errors.Is(err, sql.ErrNoRows) {
		provider, createErr := registry.RegisterProvider(ctx, req, capabilities.RegisterProviderInput{ProviderKey: target.ProviderKey, CompactAddress: target.ProviderAddress, DisplayName: owner.Resource, ProviderType: capabilities.ProviderTypeService, NodeRef: owner.NodeID, ScopeRef: scopeID, Version: "0.1.0", Status: capabilities.ProviderStatusActive, RuntimeProfileJSON: mustJSON(profile), DocumentationRefsJSON: json.RawMessage(`[]`), Metadata: mustJSON(metadata)})
		if createErr == nil {
			inspection.Provider, err = provider, nil
		} else {
			// A lost reply or concurrent identical registration must not create another
			// provider. Inspect and compare; never overwrite a provider at this address.
			inspection, err = registry.InspectProvider(ctx, target.ProviderAddress)
			if err != nil {
				return target, createErr
			}
		}
	}
	if err != nil {
		return target, err
	}
	p := inspection.Provider
	var stored ApplicationRegistrationMetadata
	var storedProfile RuntimeProfile
	if applicationDecodeJSON(p.Metadata, &stored) != nil || stored != metadata || applicationDecodeJSON(p.RuntimeProfileJSON, &storedProfile) != nil || applicationSHA(storedProfile) != applicationSHA(profile) || p.ProviderKey != target.ProviderKey || p.CompactAddress != target.ProviderAddress || p.NodeID != owner.NodeID || p.ScopeID != scopeID || p.ProviderType != capabilities.ProviderTypeService || p.Status != capabilities.ProviderStatusActive {
		return target, applicationError("service.identity_conflict")
	}
	return ApplicationServiceTarget(owner, nodeKey, p.ProviderID)
}

func ParseApplicationDescriptor(raw []byte) (ApplicationArtifactDescriptor, error) {
	var d ApplicationArtifactDescriptor
	if applicationDecodeJSON(raw, &d) != nil {
		return d, applicationError("artifact.descriptor_invalid")
	}
	return d, d.Validate()
}

func ValidatePreparedApplication(d ApplicationArtifactDescriptor, m ApplicationManifest) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Artifact != m.Artifact {
		return applicationError("artifact.manifest_mismatch")
	}
	data, _, code := applicationConfigurationBindings(m.Config, d.Configuration)
	if code != "" {
		return applicationError(code)
	}
	if len(data) != len(m.Data) {
		return applicationError("configuration.bindings_mismatch")
	}
	for key := range data {
		if _, ok := m.Data[key]; !ok {
			return applicationError("configuration.bindings_mismatch")
		}
	}
	return nil
}

// A prepared descriptor is not trusted merely because a project submitted it.
// The root helper verifies its exact realized closure before publication.
type ApplicationPreparedPublication struct {
	GrantRevision string                 `json:"grant_revision"`
	Publication   ApplicationPublication `json:"publication"`
}

func ApplicationPreparedRevision(plan ApplicationProvisionPlan, d ApplicationArtifactDescriptor) string {
	return applicationSHA(struct{ Grant, Descriptor string }{plan.GrantRevision, ApplicationDescriptorDigest(d)})
}

func (r ApplicationRuntime) PublishPrepared(ctx context.Context, peer uint32, q ApplicationPreparedPublication) error {
	p, err := r.policy()
	if err != nil {
		return err
	}
	if peer != p.InstallerUID || p.Provisioning == nil {
		return applicationError("publication.prepared_peer_denied")
	}
	release, err := r.Store.lock(ctx, q.Publication.Owner.Instance())
	if err != nil {
		return err
	}
	defer release()
	p, err = r.policy()
	if err != nil {
		return err
	}
	if peer != p.InstallerUID || p.Provisioning == nil {
		return applicationError("publication.prepared_peer_denied")
	}
	managed, err := r.readManagedGrant(q.Publication.Owner)
	if err != nil || managed.Revision != q.GrantRevision {
		return applicationError("publication.managed_grant_required")
	}
	grant, err := r.resolveGrant(p, q.Publication.Owner)
	if err != nil || applicationSHA(grant) != applicationSHA(managed.Grant) {
		return applicationError("publication.managed_grant_required")
	}
	publication := q.Publication
	if publication.ArchiveTarget == nil {
		return applicationError("publication.archive_identity")
	}
	expected, err := ApplicationServiceTarget(publication.Owner, publication.ArchiveTarget.OwnerNode, publication.ArchiveTarget.ProviderID)
	if err != nil || expected != *publication.ArchiveTarget || publication.Revision != ApplicationPreparedRevision(ApplicationProvisionPlan{GrantRevision: q.GrantRevision}, publication.Descriptor) {
		return applicationError("publication.archive_identity")
	}
	// A fresh plan of already-published content can name the current revision as
	// its CAS predecessor. The desired content, not that observation, owns replay.
	publication.GrantDigest = applicationSHA(grant)
	var current ApplicationPublication
	if r.Store.read("publication-"+publication.Owner.Instance(), &current) == nil {
		same := publication
		same.ExpectedRevision = current.ExpectedRevision
		if applicationSHA(same) == applicationSHA(current) {
			return nil
		}
	}
	return r.publishLocked(ctx, p, grant, publication)
}
