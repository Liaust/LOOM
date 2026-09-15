package serviceregistry

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	pc "loom.local/loom/internal/projectcontracts"
)

const ApplicationProvisioningSchema = "application.provisioning.v1"

// Configured once per node. Planned capacity is admission, not a filesystem
// quota or a reservation. Application data is allocated by the existing installer.
type ApplicationProvisioningPolicy struct {
	PublicEndpoints *ApplicationPublicEndpointPolicy `json:"public_endpoints,omitempty"`
	CloudBackup     bool                             `json:"cloud_backup,omitempty"`
	Proton          *ApplicationProtonPolicy         `json:"proton,omitempty"`
	DataRoot        string                           `json:"data_root"`
	MaxMemoryBytes  uint64                           `json:"max_memory_bytes"`
	MaxPlannedBytes uint64                           `json:"max_planned_bytes"`
}

type ApplicationPublicEndpointPolicy struct {
	Suffix      string `json:"suffix"`
	IngressIPv4 string `json:"ingress_ipv4"`
}

type ApplicationEndpointRequest struct {
	Hostname    string `json:"hostname"`
	BackendPort uint16 `json:"backend_port"`
}

type ApplicationProtonPolicy struct {
	AllowGeneration bool     `json:"allow_generation,omitempty"`
	ShareIDs        []string `json:"share_ids"`
	CredentialRoot  string   `json:"credential_root"`
}

// Supplied by the project coordinator after its normal project/actor checks.
// This is a helper-only daemon operation, not a remotely callable capability.
type ApplicationProvisionRequest struct {
	Endpoint          *ApplicationEndpointRequest `json:"endpoint,omitempty"`
	CloudBackup       bool                        `json:"cloud_backup,omitempty"`
	CredentialSources map[string]string           `json:"credential_sources,omitempty"`
	SchemaVersion     string                      `json:"schema_version"`
	Owner             ApplicationOwner            `json:"owner"`
	RepositoryID      string                      `json:"repository_id"`
	LocationRevision  string                      `json:"location_revision"`
	Data              map[string]string           `json:"data"`
}

// No physical allocation paths or credential values cross the helper boundary.
type ApplicationProvisionPlan struct {
	SchemaVersion    string                      `json:"schema_version"`
	Request          ApplicationProvisionRequest `json:"request"`
	PolicyRevision   string                      `json:"policy_revision"`
	ExpectedRevision string                      `json:"expected_revision"`
	GrantRevision    string                      `json:"grant_revision"`
	PlanID           string                      `json:"plan_id"`
}

type applicationManagedGrant struct {
	Request  ApplicationProvisionRequest `json:"request"`
	Grant    ApplicationGrant            `json:"grant"`
	Revision string                      `json:"revision"`
}

func (p ApplicationHostPolicy) provisioningRevision() (string, error) {
	v := p.Provisioning
	if v == nil {
		return "", applicationError("provisioning.disabled")
	}
	if p.SchemaVersion != "application.policy.v1" || !applicationNodePattern.MatchString(p.NodeID) || p.InstallerUID == 0 || p.InstallerUID == p.PublisherUID || len(p.Grants) > 128 || (p.Platform != "x86_64-linux" && p.Platform != "aarch64-linux") || !applicationAbsolutePath(v.DataRoot) || v.MaxMemoryBytes == 0 || v.MaxPlannedBytes == 0 {
		return "", applicationError("provisioning.policy_invalid")
	}
	if v.Proton != nil {
		if !applicationAbsolutePath(v.Proton.CredentialRoot) || strings.HasPrefix(v.Proton.CredentialRoot, "/nix/store/") || len(v.Proton.ShareIDs) == 0 || len(v.Proton.ShareIDs) > 16 {
			return "", applicationError("credential.policy_invalid")
		}
		for _, share := range v.Proton.ShareIDs {
			if _, _, _, ok := pc.ParseApplicationProtonReference("pass://" + share + "/item/password"); !ok {
				return "", applicationError("credential.policy_invalid")
			}
		}
	}
	if v.PublicEndpoints != nil && !v.PublicEndpoints.valid() {
		return "", applicationError("edge.policy_invalid")
	}
	// Unrelated explicit grants do not revise the node-wide provisioning policy.
	return applicationSHA(struct {
		NodeID, Platform           string
		InstallerUID, PublisherUID uint32
		Policy                     ApplicationProvisioningPolicy
	}{p.NodeID, p.Platform, p.InstallerUID, p.PublisherUID, *v}), nil
}

func applicationProvisionValid(q ApplicationProvisionRequest) bool {
	if q.Endpoint != nil && !q.Endpoint.valid() {
		return false
	}
	if len(q.CredentialSources) > 16 {
		return false
	}
	for ref, source := range q.CredentialSources {
		if _, _, ok := pc.ApplicationCredentialSourceShare(source); !ok || !applicationPrerequisiteBindingRef(ref) || ref == "" {
			return false
		}
	}
	if q.SchemaVersion != ApplicationProvisioningSchema || !q.Owner.valid() || !applicationRepoPattern.MatchString(q.RepositoryID) || !applicationRevisionPattern.MatchString(q.LocationRevision) || q.Data == nil || len(q.Data) > 16 {
		return false
	}
	seen := map[string]bool{}
	for key, ref := range q.Data {
		if !applicationKeyPattern.MatchString(key) || ref == "" || !applicationPrerequisiteBindingRef(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}

func (p ApplicationHostPolicy) deriveGrant(q ApplicationProvisionRequest) (applicationManagedGrant, error) {
	var out applicationManagedGrant
	if !applicationProvisionValid(q) || q.Owner.NodeID != p.NodeID {
		return out, applicationError("provisioning.request_invalid")
	}
	q.Data = maps.Clone(q.Data)
	q.CredentialSources = maps.Clone(q.CredentialSources)
	if q.Endpoint != nil {
		endpoint := *q.Endpoint
		q.Endpoint = &endpoint
	}
	revision, err := p.provisioningRevision()
	if err != nil {
		return out, err
	}
	if q.CloudBackup && !p.Provisioning.CloudBackup {
		return out, applicationError("backup.not_configured")
	}
	for _, g := range p.Grants {
		if g.Owner == q.Owner {
			return out, applicationError("provisioning.explicit_grant")
		}
	}
	g := ApplicationGrant{Owner: q.Owner, RepositoryID: q.RepositoryID, LocationRevision: q.LocationRevision, PolicyRevision: revision, MaxMemoryBytes: p.Provisioning.MaxMemoryBytes, MaxPlannedBytes: p.Provisioning.MaxPlannedBytes, Data: map[string]ApplicationDataPolicy{}, Credentials: map[string]ApplicationCredentialPolicy{}}
	if q.Endpoint != nil {
		edge := p.Provisioning.PublicEndpoints
		if edge == nil || !strings.HasSuffix(q.Endpoint.Hostname, "."+edge.Suffix) || strings.Contains(strings.TrimSuffix(q.Endpoint.Hostname, "."+edge.Suffix), ".") {
			return out, applicationError("edge.hostname_not_permitted")
		}
		g.Edge = &ApplicationEdgePolicy{Owner: q.Owner, Hostname: q.Endpoint.Hostname, BackendPort: q.Endpoint.BackendPort, TLS: "managed", Revision: revision, IngressIPv4: edge.IngressIPv4}
	}
	for key, ref := range q.Data {
		g.Data[key] = ApplicationDataPolicy{Pool: p.Provisioning.DataRoot, Path: filepath.Join(p.Provisioning.DataRoot, q.Owner.Instance()+"-"+key), BindingRef: ref, CloudBackup: p.Provisioning.CloudBackup}
	}
	for ref, source := range q.CredentialSources {
		share, generate, _ := pc.ApplicationCredentialSourceShare(source)
		if p.Provisioning.Proton == nil || !slices.Contains(p.Provisioning.Proton.ShareIDs, share) {
			return out, applicationError("credential.share_not_permitted")
		}
		if generate && !p.Provisioning.Proton.AllowGeneration {
			return out, applicationError("credential.generation_not_permitted")
		}
		g.Credentials[ref] = ApplicationCredentialPolicy{Path: filepath.Join(p.Provisioning.Proton.CredentialRoot, q.Owner.Instance()+"-"+applicationCredentialName(ref)), Revision: ApplicationProtonCredentialRevision(source)}
	}
	// Reuse the installer's grant validator, including path restrictions.
	check := p
	check.Grants = []ApplicationGrant{g}
	if _, err = check.grant(q.Owner); err != nil {
		return out, err
	}
	out = applicationManagedGrant{Request: q, Grant: g}
	out.Revision = out.digest()
	return out, nil
}

func (g applicationManagedGrant) digest() string {
	g.Revision = ""
	return applicationSHA(g)
}

func (r ApplicationRuntime) readManagedGrant(owner ApplicationOwner) (applicationManagedGrant, error) {
	var g applicationManagedGrant
	// Use the existing no-follow, bounded prerequisite reader; it also validates
	// the state directory and single-link file custody without creating anything.
	_, err := applicationPrerequisiteRead(filepath.Join(r.Store.Root, "grant-"+owner.Instance()+".json"), r.Store.OwnerUID, false, &g)
	if err != nil {
		return g, err
	}
	if !applicationProvisionValid(g.Request) || g.Request.Owner != owner || g.Grant.Owner != owner || g.Revision != g.digest() {
		return g, applicationError("provisioning.state_invalid")
	}
	return g, nil
}

func (r ApplicationRuntime) resolveGrant(p ApplicationHostPolicy, owner ApplicationOwner) (ApplicationGrant, error) {
	for _, g := range p.Grants {
		if g.Owner == owner {
			return p.grant(owner)
		}
	}
	// Validate the header even when neither kind of grant exists.
	if _, err := p.grant(owner); err != nil && err.Error() != "application.policy.scope_denied" {
		return ApplicationGrant{}, err
	}
	stored, err := r.readManagedGrant(owner)
	if errors.Is(err, os.ErrNotExist) {
		return ApplicationGrant{}, applicationError("policy.scope_denied")
	}
	if err != nil {
		return ApplicationGrant{}, applicationError("provisioning.state_invalid")
	}
	current, err := p.deriveGrant(stored.Request)
	if err != nil {
		return ApplicationGrant{}, err
	}
	if current.Revision != stored.Revision {
		return ApplicationGrant{}, applicationError("provisioning.policy_changed")
	}
	return current.Grant, nil
}

func applicationProvisionPlanID(p ApplicationProvisionPlan) string {
	p.PlanID = ""
	return applicationSHA(p)
}

func (p ApplicationProvisionPlan) Validate() error {
	if p.SchemaVersion != ApplicationProvisioningSchema || !applicationProvisionValid(p.Request) || p.PlanID != applicationProvisionPlanID(p) || !applicationDigestPattern.MatchString(p.GrantRevision) || !applicationDigestPattern.MatchString(p.PolicyRevision) || (p.ExpectedRevision != "" && !applicationDigestPattern.MatchString(p.ExpectedRevision)) {
		return applicationError("provisioning.plan_invalid")
	}
	return nil
}

func (r ApplicationRuntime) provisionPlan(p ApplicationHostPolicy, peer uint32, q ApplicationProvisionRequest) (ApplicationProvisionPlan, applicationManagedGrant, error) {
	var out ApplicationProvisionPlan
	var desired applicationManagedGrant
	if peer != p.InstallerUID {
		return out, desired, applicationError("provisioning.peer_denied")
	}
	desired, err := p.deriveGrant(q)
	if err != nil {
		return out, desired, err
	}
	pool := p.Provisioning.DataRoot
	if applicationCheckTrustedParents(pool, r.Store.OwnerUID, r.Store.TrustedParentOwners) != nil || applicationCustody(pool, r.Store.OwnerUID, true) != nil {
		return out, desired, applicationError("provisioning.pool_unavailable")
	}
	if len(q.CredentialSources) > 0 && (applicationCheckParents(p.Provisioning.Proton.CredentialRoot, r.Store.OwnerUID) != nil || applicationCustody(p.Provisioning.Proton.CredentialRoot, r.Store.OwnerUID, true) != nil) {
		return out, desired, applicationError("credential.materialization_unavailable")
	}
	if len(q.CredentialSources) > 0 && r.Credentials == nil {
		return out, desired, applicationError("credential.resolver_unavailable")
	}
	if err := r.checkEndpointOwnership(p, q); err != nil {
		return out, desired, err
	}
	stored, err := r.readManagedGrant(q.Owner)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, desired, applicationError("provisioning.state_invalid")
	}
	if err == nil {
		var installation ApplicationInstallation
		if e := r.Store.read("installation-"+q.Owner.Instance(), &installation); e != nil && !errors.Is(e, os.ErrNotExist) {
			return out, desired, e
		}
		if installation.PreviousEndpoint != nil && q.Endpoint != nil && (installation.Request == nil || installation.Request.Endpoint == nil || installation.Request.Endpoint.Hostname != q.Endpoint.Hostname) {
			return out, desired, applicationError("edge.replacement_pending")
		}
		// Grant revision updates never relocate or detach retained application data.
		if stored.Grant.RepositoryID != desired.Grant.RepositoryID || stored.Grant.LocationRevision != desired.Grant.LocationRevision {
			return out, desired, applicationError("provisioning.identity_changed")
		}
		for key, old := range stored.Grant.Data {
			current := desired.Grant.Data[key]
			if current.Path != old.Path || current.Pool != old.Pool || current.BindingRef != old.BindingRef || (old.CloudBackup && !current.CloudBackup) {
				return out, desired, applicationError("provisioning.data_migration_required")
			}
		}
		for ref, source := range stored.Request.CredentialSources {
			if desired.Request.CredentialSources[ref] != source || desired.Grant.Credentials[ref] != stored.Grant.Credentials[ref] {
				return out, desired, applicationError("credential.rotation_required")
			}
		}
	}
	out = ApplicationProvisionPlan{SchemaVersion: ApplicationProvisioningSchema, Request: desired.Request, PolicyRevision: desired.Grant.PolicyRevision, ExpectedRevision: stored.Revision, GrantRevision: desired.Revision}
	out.PlanID = applicationProvisionPlanID(out)
	return out, desired, nil
}

func (r ApplicationRuntime) PlanProvision(ctx context.Context, peer uint32, q ApplicationProvisionRequest) (ApplicationProvisionPlan, error) {
	if err := ctx.Err(); err != nil {
		return ApplicationProvisionPlan{}, err
	}
	p, err := r.policy()
	if err != nil {
		return ApplicationProvisionPlan{}, err
	}
	plan, _, err := r.provisionPlan(p, peer, q)
	return plan, err
}

func (r ApplicationRuntime) Provision(ctx context.Context, peer uint32, plan ApplicationProvisionPlan) (ApplicationProvisionPlan, error) {
	var empty ApplicationProvisionPlan
	if err := plan.Validate(); err != nil {
		return empty, err
	}
	p, err := r.policy()
	if err != nil {
		return empty, err
	}
	// Admission before locking prevents denied peers from even creating a lock.
	if _, _, err = r.provisionPlan(p, peer, plan.Request); err != nil {
		return empty, err
	}
	release, err := r.Store.lock(ctx, plan.Request.Owner.Instance())
	if err != nil {
		return empty, err
	}
	defer release()
	// Acquire inventory after the instance lock: a same-app retry must not hold
	// the global inventory while another request is resolving its credentials.
	releaseInventory, err := r.Store.lock(ctx, "provisioning")
	if err != nil {
		return empty, err
	}
	defer func() {
		if releaseInventory != nil {
			releaseInventory()
		}
	}()
	p, err = r.policy()
	if err != nil {
		return empty, err
	}
	current, desired, err := r.provisionPlan(p, peer, plan.Request)
	if err != nil {
		return empty, err
	}
	if current.PolicyRevision != plan.PolicyRevision || current.GrantRevision != plan.GrantRevision || (current.ExpectedRevision != plan.ExpectedRevision && current.ExpectedRevision != plan.GrantRevision) {
		return empty, applicationError("provisioning.plan_stale")
	}
	if current.ExpectedRevision == plan.GrantRevision {
		releaseInventory()
		releaseInventory = nil
		return plan, r.materializeApplicationCredentials(ctx, desired)
	}
	owners, err := r.grantOwners(p)
	if err != nil {
		return empty, err
	}
	if current.ExpectedRevision == "" && len(owners) >= 128 {
		return empty, applicationError("provisioning.owner_limit")
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if err = r.Store.write("grant-"+plan.Request.Owner.Instance(), desired); err != nil {
		return empty, err
	}
	// Proton I/O holds only this application's lock, not the node inventory lock.
	releaseInventory()
	releaseInventory = nil
	if err = r.materializeApplicationCredentials(ctx, desired); err != nil {
		return empty, err
	}
	return plan, r.fail("provisioning:committed")
}

// Boot and archive control must see managed owners too. This reads only grant
// names from the existing helper store, never scans application payloads.
func (r ApplicationRuntime) grantOwners(p ApplicationHostPolicy) ([]ApplicationOwner, error) {
	owners := make([]ApplicationOwner, 0, len(p.Grants))
	seen := map[ApplicationOwner]bool{}
	for _, g := range p.Grants {
		owners = append(owners, g.Owner)
		seen[g.Owner] = true
	}
	if p.Provisioning == nil {
		return owners, nil
	}
	if _, err := p.provisioningRevision(); err != nil {
		return nil, err
	}
	if err := applicationCustody(r.Store.Root, r.Store.OwnerUID, true); errors.Is(err, os.ErrNotExist) {
		return owners, nil
	} else if err != nil {
		return nil, applicationError("provisioning.state_invalid")
	}
	entries, err := os.ReadDir(r.Store.Root)
	if err != nil {
		return nil, applicationError("provisioning.state_invalid")
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "grant-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var g applicationManagedGrant
		_, err := applicationPrerequisiteRead(filepath.Join(r.Store.Root, entry.Name()), r.Store.OwnerUID, false, &g)
		if err != nil || !g.Request.Owner.valid() || entry.Name() != "grant-"+g.Request.Owner.Instance()+".json" || g.Revision != g.digest() {
			return nil, applicationError("provisioning.state_invalid")
		}
		if !seen[g.Request.Owner] {
			owners = append(owners, g.Request.Owner)
			seen[g.Request.Owner] = true
		}
	}
	return owners, nil
}

func applicationProvisionResponse(response ApplicationHelperResponse, q ApplicationProvisionRequest, err error) (ApplicationProvisionPlan, error) {
	var empty ApplicationProvisionPlan
	if err != nil {
		return empty, err
	}
	p := response.Provision
	if p == nil || response.Receipt != nil || response.Manager != nil || response.Prerequisites != nil || p.SchemaVersion != ApplicationProvisioningSchema || applicationSHA(p.Request) != applicationSHA(q) || !applicationProvisionValid(p.Request) || p.PlanID != applicationProvisionPlanID(*p) || !applicationDigestPattern.MatchString(p.PolicyRevision) || !applicationDigestPattern.MatchString(p.GrantRevision) || (p.ExpectedRevision != "" && !applicationDigestPattern.MatchString(p.ExpectedRevision)) {
		return empty, applicationError("provisioning.response_invalid")
	}
	return *p, nil
}
