package serviceregistry

import (
	"context"
	"errors"
	"loom.local/loom/internal/projectquiescence"
	"os"
)

type ApplicationPublication struct {
	GrantDigest      string                        `json:"grant_digest"`
	ArchiveTarget    *projectquiescence.Target     `json:"archive_target,omitempty"`
	Owner            ApplicationOwner              `json:"owner"`
	ExpectedRevision string                        `json:"expected_revision"`
	Revision         string                        `json:"revision"`
	LocationRevision string                        `json:"location_revision"`
	PolicyRevision   string                        `json:"policy_revision"`
	Descriptor       ApplicationArtifactDescriptor `json:"descriptor"`
}

func (r ApplicationRuntime) Publish(ctx context.Context, peerUID uint32, p ApplicationPublication) error {
	policy, err := r.policy()
	if err != nil {
		return err
	}
	if peerUID != policy.PublisherUID || peerUID == policy.InstallerUID {
		return applicationError("publication.peer_denied")
	}
	release, err := r.Store.lock(ctx, p.Owner.Instance())
	if err != nil {
		return err
	}
	defer release()
	// Policy reload and CAS occur while owning the same fence as every effect.
	policy, err = r.policy()
	if err != nil {
		return err
	}
	grant, err := r.resolveGrant(policy, p.Owner)
	if err != nil {
		return err
	}
	if peerUID != policy.PublisherUID {
		return applicationError("publication.peer_denied")
	}
	return r.publishLocked(ctx, policy, grant, p)
}

func (r ApplicationRuntime) publishLocked(ctx context.Context, policy ApplicationHostPolicy, grant ApplicationGrant, p ApplicationPublication) error {
	if grant.RepositoryID != p.Descriptor.RepositoryID || grant.PolicyRevision != p.PolicyRevision || grant.LocationRevision != p.LocationRevision || policy.Platform != p.Descriptor.Artifact.Platform || !applicationRevisionPattern.MatchString(p.Revision) {
		return applicationError("publication.scope_denied")
	}
	p.GrantDigest = applicationSHA(grant)
	if p.ArchiveTarget != nil {
		t := p.ArchiveTarget
		if t.Kind != projectquiescence.TargetKindService || t.AllowlistKey != p.Owner.AllowlistKey() || t.Unit != p.Owner.Unit() || t.Manager != "systemd" || t.ProviderID == "" || !applicationDigestPattern.MatchString(t.RuntimeProfileDigest) {
			return applicationError("publication.archive_identity")
		}
	}
	if err := p.Descriptor.Validate(); err != nil {
		return err
	}
	var current ApplicationPublication
	err := r.Store.read("publication-"+p.Owner.Instance(), &current)
	if err == nil && applicationSHA(current) == applicationSHA(p) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if (err == nil && current.Revision != p.ExpectedRevision) || (errors.Is(err, os.ErrNotExist) && p.ExpectedRevision != "") {
		return applicationError("publication.revision_conflict")
	}
	if current.Revision == p.Revision {
		return applicationError("publication.token_conflict")
	}
	if err = r.System.VerifyArtifact(ctx, p.Descriptor); err != nil {
		return err
	}
	// Descriptor objects are immutable. Installation selects a published digest,
	// never supplies its own closure, schema, launcher or trust assertion.
	descriptorKey := "descriptor-" + applicationSHA(p.Descriptor)[7:]
	var old ApplicationArtifactDescriptor
	if err = r.Store.read(descriptorKey, &old); err == nil && applicationSHA(old) != applicationSHA(p.Descriptor) {
		return applicationError("publication.descriptor_conflict")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = r.Store.write(descriptorKey, p.Descriptor); err != nil {
		return err
	}
	if err = r.Store.write("publication-"+p.Owner.Instance(), p); err != nil {
		return err
	}
	return r.fail("publication:committed")
}

// Archive control is the existing service stop/status vocabulary with an exact
// publisher-bound target. It cannot start, restart, publish or choose a unit.
type ApplicationArchiveControl struct {
	Target    projectquiescence.Target `json:"target"`
	Operation Operation                `json:"operation"`
}

func (r ApplicationRuntime) ArchiveControl(ctx context.Context, peerUID uint32, q ApplicationArchiveControl) (ManagerResult, error) {
	out := ManagerResult{Operation: q.Operation, ProcessState: ProcessStateUnknown}
	if q.Operation != OperationStop && q.Operation != OperationStatus {
		return out, applicationError("archive.operation_denied")
	}
	policy, e := r.policy()
	if e != nil || peerUID != policy.InstallerUID {
		return out, applicationError("archive.peer_denied")
	}
	var owner *ApplicationOwner
	owners, e := r.grantOwners(policy)
	if e != nil {
		return out, e
	}
	for _, candidate := range owners {
		if q.Target.Unit == candidate.Unit() && q.Target.AllowlistKey == candidate.AllowlistKey() {
			if owner != nil {
				return out, applicationError("archive.owner_ambiguous")
			}
			v := candidate
			owner = &v
		}
	}
	if owner == nil {
		return out, applicationError("archive.owner_missing")
	}
	if e = r.fail("policy_read"); e != nil {
		return out, e
	}
	release, e := r.Store.lock(ctx, owner.Instance())
	if e != nil {
		return out, e
	}
	defer release()
	policy, e = r.policy()
	if e != nil {
		return out, e
	}
	if peerUID != policy.InstallerUID {
		return out, applicationError("archive.peer_denied")
	}
	if _, e = r.resolveGrant(policy, *owner); e != nil {
		return out, e
	}
	var publication ApplicationPublication
	if r.Store.read("publication-"+owner.Instance(), &publication) != nil || publication.ArchiveTarget == nil || *publication.ArchiveTarget != q.Target {
		return out, applicationError("archive.identity_conflict")
	}
	var installed ApplicationInstallation
	if r.Store.read("installation-"+owner.Instance(), &installed) != nil || installed.Owner != *owner {
		return out, applicationError("archive.installation_missing")
	}
	g := ApplicationGeneration{Owner: *owner, ID: installed.Generation, UID: installed.UID, Descriptor: installed.Descriptor}
	if q.Operation == OperationStop {
		installed.Fenced = true
		installed.Revision = applicationSHA(q)
		// Fsync root fence before systemctl. A surviving older helper owns this
		// lock first; once archive returns, no older or newer apply can restart it.
		if e = r.Store.write("installation-"+owner.Instance(), installed); e != nil {
			return out, e
		}
		if e = r.System.Stop(ctx, g); e != nil {
			return out, e
		}
	}
	observed, e := r.System.Observe(ctx, g)
	if e != nil {
		return out, e
	}
	if observed.State == "inactive" {
		out.ProcessState = ProcessStateStopped
	} else if observed.State == "active" {
		out.ProcessState = ProcessStateRunning
	}
	out.Success = true
	out.Message = "exact owned application manager observation"
	return out, nil
}
