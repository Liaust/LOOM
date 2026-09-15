package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	pc "loom.local/loom/internal/projectcontracts"
)

type ApplicationRuntimeRequest struct {
	Endpoint                     *ApplicationEndpointRequest       `json:"endpoint,omitempty"`
	ExpectedInstallationRevision string                            `json:"expected_installation_revision"`
	SchemaVersion                string                            `json:"schema_version"`
	Operation                    string                            `json:"operation"`
	Owner                        ApplicationOwner                  `json:"owner"`
	OperationToken               string                            `json:"operation_token"`
	ExpectedRevision             string                            `json:"expected_revision"`
	PolicyRevision               string                            `json:"policy_revision"`
	LocationRevision             string                            `json:"location_revision"`
	CredentialRevisions          map[string]string                 `json:"credential_revisions"`
	DescriptorDigest             string                            `json:"descriptor_digest"`
	Manifest                     *ApplicationManifest              `json:"manifest,omitempty"`
	Data                         map[string]ApplicationDataRequest `json:"data"`
	Rollback                     bool                              `json:"rollback"`
}

// Digest is the installer journal identity, including the exact operation token.
// Other owners must not substitute their own JSON canonicalization algorithm.
func (q ApplicationRuntimeRequest) Digest() string { return applicationSHA(q) }

type ApplicationCurrentObservation struct {
	Endpoint          *pc.ApplicationEndpointStatus `json:"endpoint,omitempty"`
	State             string                        `json:"state"`
	ErrorCode         string                        `json:"error_code,omitempty"`
	ObservedAt        time.Time                     `json:"observed_at"`
	Process           ApplicationProcessObservation `json:"process"`
	Readiness         ApplicationReadiness          `json:"readiness"`
	ManagementPresent bool                          `json:"management_present"`
}
type ApplicationRuntimeReceipt struct {
	Endpoint       *pc.ApplicationEndpointStatus `json:"endpoint,omitempty"`
	AppliedAt      *time.Time                    `json:"applied_at,omitempty"`
	DesiredMatches *bool                         `json:"desired_matches,omitempty"`
	// Original effect facts remain immutable. A newly authorized call may attach
	// a fresh observation; dispatch/outbox replay still returns its frozen bytes.
	Current              *ApplicationCurrentObservation           `json:"current,omitempty"`
	Credentials          map[string]ApplicationCredentialIdentity `json:"credentials"`
	InstallationRevision string                                   `json:"installation_revision"`
	SchemaVersion        string                                   `json:"schema_version"`
	Owner                ApplicationOwner                         `json:"owner"`
	OperationToken       string                                   `json:"operation_token"`
	InputDigest          string                                   `json:"input_digest"`
	Revision             string                                   `json:"revision"`
	Generation           string                                   `json:"generation"`
	EvidenceRef          string                                   `json:"evidence_ref"`
	State                string                                   `json:"state"`
	ErrorCode            string                                   `json:"error_code,omitempty"`
	Applied              bool                                     `json:"applied"`
	Retired              bool                                     `json:"retired"`
	Readiness            ApplicationReadiness                     `json:"readiness"`
	Process              ApplicationProcessObservation            `json:"process"`
	Effects              []ApplicationEffect                      `json:"effects"`
	Data                 map[string]ApplicationDataIdentity       `json:"data"`
	Capacity             []ApplicationCapacityObservation         `json:"capacity"`
	ObservedAt           time.Time                                `json:"observed_at"`
}

// ApplicationSystem is the fixed host effect adapter. Production uses only the
// startup-selected tool paths; callers cannot supply commands or unit names.
// Tests replace the host adapter, never the publication/revision/intent owner.
type ApplicationSystem interface {
	VerifyArtifact(context.Context, ApplicationArtifactDescriptor) error
	EnsureAccount(context.Context, ApplicationOwner, uint32, bool) error
	EnsureData(context.Context, ApplicationDataPolicy, uint32, *ApplicationDataIdentity) (ApplicationDataIdentity, error)
	ConfigurationPath(ApplicationGeneration) string
	PublishGeneration(context.Context, ApplicationGeneration) error
	GenerationPublished(ApplicationGeneration) bool
	Observe(context.Context, ApplicationGeneration) (ApplicationProcessObservation, error)
	Restart(context.Context, ApplicationGeneration) error
	Start(context.Context, ApplicationGeneration) error
	Stop(context.Context, ApplicationGeneration) error
	RemoveManagement(context.Context, ApplicationGeneration) error
	PublishEdge(context.Context, ApplicationGeneration, []byte) error
}
type ApplicationGeneration struct {
	Listener   *ApplicationListener
	Owner      ApplicationOwner
	ID         string
	UID        uint32
	Descriptor ApplicationArtifactDescriptor
	Previous   *ApplicationArtifactDescriptor
	Config     []byte
	DropIn     string
}
type ApplicationRuntime struct {
	Credentials ApplicationCredentialResolver
	Store       ApplicationStateStore
	PolicyPath  string
	System      ApplicationSystem
	// FailureHook is a fixture-only boundary observer, not authorization. CAS and
	// token checks belong to this owner under flock, independent of callbacks.
	FailureHook func(string) error
}

func DecodeApplicationRuntimeRequest(raw []byte) (ApplicationRuntimeRequest, error) {
	var q ApplicationRuntimeRequest
	err := applicationDecodeJSON(raw, &q)
	if err != nil {
		return q, err
	}
	if q.SchemaVersion != ApplicationRuntimeSchema || !q.Owner.valid() || (q.Operation != "inspect" && q.Operation != "apply" && q.Operation != "retire") || !applicationRevisionPattern.MatchString(q.OperationToken) || !applicationRevisionPattern.MatchString(q.ExpectedRevision) {
		return q, applicationError("request.invalid")
	}
	if q.Endpoint != nil && !q.Endpoint.valid() {
		return q, applicationError("edge.request_invalid")
	}
	return q, nil
}
func (r ApplicationRuntime) policy() (ApplicationHostPolicy, error) {
	var p ApplicationHostPolicy
	resolved, e := filepath.EvalSymlinks(r.PolicyPath)
	if e != nil {
		return p, applicationError("policy.unavailable")
	}
	if r.Store.OwnerUID == 0 && resolved != r.PolicyPath && !strings.HasPrefix(resolved, "/nix/store/") {
		return p, applicationError("policy.custody")
	}
	if err := applicationImmutableParents(resolved, r.Store.OwnerUID); err != nil {
		return p, applicationError("policy.custody")
	}
	if err := applicationCustody(resolved, r.Store.OwnerUID, false); err != nil {
		return p, applicationError("policy.custody")
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return p, applicationError("policy.unavailable")
	}
	err = applicationDecodeJSON(raw, &p)
	return p, err
}
func applicationAccountUID(owner ApplicationOwner) uint32 { // Dedicated high range, collision checked, never recycled.
	var n uint32
	for _, b := range []byte(owner.Instance()) {
		n = n*33 + uint32(b)
	}
	return 200000 + n%800000
}
func (r ApplicationRuntime) Execute(ctx context.Context, peerUID uint32, q ApplicationRuntimeRequest) (receipt ApplicationRuntimeReceipt, err error) {
	raw, _ := json.Marshal(q)
	if _, err = DecodeApplicationRuntimeRequest(raw); err != nil {
		return receipt, err
	}
	policy, err := r.policy()
	if err != nil {
		return receipt, err
	}
	if peerUID != policy.InstallerUID {
		return receipt, applicationError("peer.denied")
	}
	if e := r.fail("policy_read"); e != nil {
		return receipt, e
	}
	release, err := r.Store.lock(ctx, q.Owner.Instance())
	if err != nil {
		return receipt, err
	}
	defer release()
	policy, err = r.policy()
	if err != nil {
		return receipt, err
	}
	if peerUID != policy.InstallerUID {
		return receipt, applicationError("peer.denied")
	}
	grant, err := r.resolveGrant(policy, q.Owner)
	if err != nil {
		return receipt, err
	}
	if (q.Endpoint != nil && (grant.Edge == nil || grant.Edge.Hostname != q.Endpoint.Hostname || grant.Edge.BackendPort != q.Endpoint.BackendPort)) || (grant.Edge != nil && grant.Edge.IngressIPv4 != "" && q.Endpoint == nil) {
		return receipt, applicationError("edge.request_mismatch")
	}
	if q.Endpoint != nil && (q.Manifest == nil || q.Manifest.Health.Kind != HealthKindHTTP) {
		return receipt, applicationError("edge.http_health_required")
	}
	if q.Endpoint != nil {
		if _, e := applicationEdgeFragment(grant, *q.Manifest); e != nil {
			return receipt, e
		}
	}
	var publication ApplicationPublication
	if err = r.Store.read("publication-"+q.Owner.Instance(), &publication); err != nil {
		return receipt, applicationError("artifact.publication_prerequisite")
	}
	if publication.Owner != q.Owner || publication.GrantDigest != applicationSHA(grant) || publication.Revision != q.ExpectedRevision || publication.PolicyRevision != grant.PolicyRevision || publication.LocationRevision != grant.LocationRevision || q.PolicyRevision != grant.PolicyRevision || q.LocationRevision != grant.LocationRevision || publication.Descriptor.RepositoryID != grant.RepositoryID || policy.Platform != publication.Descriptor.Artifact.Platform {
		return receipt, applicationError("revision.conflict")
	}
	if len(q.CredentialRevisions) != len(grant.Credentials) {
		return receipt, applicationError("credential.revision_conflict")
	}
	for key, data := range q.Data {
		if data.Backup != "" && (data.Backup != "cloud_history" || !grant.Data[key].CloudBackup) {
			return receipt, applicationError("backup.not_configured")
		}
	}
	for k, p := range grant.Credentials {
		if q.CredentialRevisions[k] != p.Revision {
			return receipt, applicationError("credential.revision_conflict")
		}
	}
	var installed ApplicationInstallation
	exists := true
	if err = r.Store.read("installation-"+q.Owner.Instance(), &installed); errors.Is(err, os.ErrNotExist) {
		exists = false
		installed = ApplicationInstallation{Owner: q.Owner, UID: applicationAccountUID(q.Owner), Data: map[string]ApplicationDataIdentity{}}
	} else if err != nil {
		return receipt, err
	}
	if installed.Owner != q.Owner || installed.UID != applicationAccountUID(q.Owner) {
		return receipt, applicationError("installation.owner_conflict")
	}
	receipt = ApplicationRuntimeReceipt{SchemaVersion: ApplicationRuntimeSchema, Owner: q.Owner, OperationToken: q.OperationToken, InputDigest: applicationSHA(q), Revision: q.ExpectedRevision, Generation: installed.Generation, InstallationRevision: installed.Revision, State: "partial", Readiness: applicationUnknownReadiness(), Data: installed.Data, Effects: []ApplicationEffect{}, Capacity: []ApplicationCapacityObservation{}, ObservedAt: time.Now().UTC()}
	if q.Operation == "inspect" {
		receipt.AppliedAt = installed.AppliedAt
		if q.Manifest != nil {
			matches := exists && installed.Request != nil && installed.Applied && !installed.Retired && !installed.Fenced && installed.Request.ExpectedRevision == q.ExpectedRevision && installed.Request.PolicyRevision == q.PolicyRevision && installed.Request.LocationRevision == q.LocationRevision && installed.Request.DescriptorDigest == q.DescriptorDigest && applicationSHA(installed.Request.Manifest) == applicationSHA(q.Manifest) && applicationSHA(installed.Request.Data) == applicationSHA(q.Data) && applicationSHA(installed.Request.CredentialRevisions) == applicationSHA(q.CredentialRevisions)
			receipt.DesiredMatches = &matches
		}
		receipt.Retired = installed.Retired
		receipt.Applied = installed.Applied && !installed.Retired
		receipt.State = "observed"
		if exists {
			current, _ := r.observeInstallation(ctx, installed, grant)
			receipt.Current = &current
			receipt.Process, receipt.Readiness = current.Process, current.Readiness
			return receipt, nil
		}
		return receipt, nil
	}
	if q.Operation == "apply" && (installed.Fenced || installed.Retired) {
		return receipt, applicationError("installation.fenced")
	}
	if q.Operation == "apply" {
		if q.DescriptorDigest != applicationSHA(publication.Descriptor) {
			return receipt, applicationError("artifact.selection_conflict")
		}
		if err = r.System.VerifyArtifact(ctx, publication.Descriptor); err != nil {
			return receipt, err
		}
		receipt.Credentials, err = applicationCredentialIdentities(grant, r.Store.OwnerUID)
		if err != nil {
			return receipt, err
		}
	}
	journalKey := "operation-" + q.Owner.Instance() + "-" + applicationSHA(q.OperationToken)[7:]
	receipt.EvidenceRef = "application-receipt:" + q.Owner.Instance() + ":" + applicationSHA(q.OperationToken)[7:]
	journal := ApplicationJournal{Owner: q.Owner, Token: q.OperationToken, InputDigest: receipt.InputDigest, Revision: q.ExpectedRevision, Effects: []ApplicationEffect{}}
	var prior ApplicationJournal
	if readErr := r.Store.read(journalKey, &prior); readErr == nil {
		if prior.Owner != q.Owner || prior.Token != q.OperationToken || prior.InputDigest != receipt.InputDigest || prior.Revision != q.ExpectedRevision {
			return receipt, applicationError("operation.token_conflict")
		}
		journal = prior
		if prior.Receipt != nil && prior.Receipt.State == "succeeded" {
			if installed.Revision != prior.Receipt.InstallationRevision {
				return receipt, applicationError("installation.revision_conflict")
			}
			if q.Operation == "apply" {
				if applicationSHA(prior.Receipt.Credentials) != applicationSHA(receipt.Credentials) {
					return receipt, applicationError("credential.revision_conflict")
				}
				for key, identity := range installed.Data {
					p, ok := grant.Data[key]
					if !ok {
						return receipt, applicationError("data.binding_changed")
					}
					if _, e := r.System.EnsureData(ctx, p, installed.UID, &identity); e != nil {
						return receipt, e
					}
				}
			}
			// Copy the historical receipt without rewriting its process facts/time or
			// durable journal. Only this new call's observation is fresh.
			observed := *prior.Receipt
			current, currentErr := r.observeInstallation(ctx, installed, grant)
			observed.Current = &current
			return observed, currentErr
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return receipt, readErr
	}
	if installed.Revision != q.ExpectedInstallationRevision {
		// Only this already-journaled operation may reconcile its own completed
		// installation effect. A different token/input cannot reuse that revision.
		ownCommit := prior.InputDigest == receipt.InputDigest && installed.Revision == receipt.InputDigest
		if !ownCommit {
			return receipt, applicationError("installation.revision_conflict")
		}
	}
	reconciling := prior.InputDigest == receipt.InputDigest && installed.Revision == receipt.InputDigest
	if q.Operation == "apply" && exists && installed.Generation != "" && !installed.Applied && !reconciling {
		observed, e := r.System.Observe(ctx, ApplicationGeneration{Owner: q.Owner, ID: installed.Generation, UID: installed.UID, Descriptor: installed.Descriptor})
		// A fresh authorized transition may recover known stopped/failed or exact
		// active prepared code. Activating/unknown delivery must first settle.
		settled := observed.State == "inactive" || observed.State == "failed" || applicationProcessMatches(observed, q.Owner, installed.Generation, installed.Descriptor.Executable, installed.UID)
		if e != nil || !settled {
			return receipt, applicationError("effect.uncertain")
		}
	}
	defer func() {
		receipt.ObservedAt = time.Now().UTC()
		receipt.Effects = append([]ApplicationEffect{}, journal.Effects...)
		if err != nil {
			receipt.ErrorCode = applicationSafeError(err)
			receipt.State = "partial"
		}
		journal.Receipt = &receipt
		if writeErr := r.Store.write(journalKey, journal); writeErr != nil {
			err = writeErr
		}
		if hookErr := r.fail("receipt"); hookErr != nil {
			err = hookErr
		}
	}()
	policyHash := applicationSHA(policy)
	fence := func() error {
		if e := ctx.Err(); e != nil {
			return applicationError("operation.cancelled")
		}
		current, e := r.policy()
		if e != nil || applicationSHA(current) != policyHash {
			return applicationError("revision.policy_changed")
		}
		var p ApplicationPublication
		if r.Store.read("publication-"+q.Owner.Instance(), &p) != nil || applicationSHA(p) != applicationSHA(publication) {
			return applicationError("revision.conflict")
		}
		return nil
	}
	effect := func(kind string, probe func() bool, apply func() error, replaySafe bool) error {
		if e := fence(); e != nil {
			return e
		}
		index := -1
		for i := range journal.Effects {
			if journal.Effects[i].Kind == kind {
				index = i
				break
			}
		}
		if index >= 0 {
			if journal.Effects[index].State == "done" {
				return nil
			}
			if probe != nil && probe() {
				journal.Effects[index].State = "done"
				return r.Store.write(journalKey, journal)
			}
			if !replaySafe {
				return applicationError("effect.uncertain")
			}
		}
		if index < 0 {
			index = len(journal.Effects)
			journal.Effects = append(journal.Effects, ApplicationEffect{Kind: kind, State: "intent"})
			if e := r.Store.write(journalKey, journal); e != nil {
				return e
			}
		}
		if e := r.fail("intent:" + kind); e != nil {
			return e
		}
		if e := fence(); e != nil {
			return e
		}
		if e := apply(); e != nil {
			return e
		}
		if e := r.fail("effect:" + kind); e != nil {
			return e
		}
		journal.Effects[index].State = "done"
		if e := r.Store.write(journalKey, journal); e != nil {
			return e
		}
		return r.fail("recorded:" + kind)
	}
	generation := ApplicationGeneration{Owner: q.Owner, ID: installed.Generation, UID: installed.UID, Descriptor: installed.Descriptor}
	if q.Operation == "retire" {
		if !exists {
			return receipt, applicationError("installation.missing")
		}
		// Durable root fence precedes stop: a helper surviving node-agent death
		// cannot subsequently restart this instance, even with a new request token.
		installed.Fenced = true
		if err = effect("fence", nil, func() error { return r.Store.write("installation-"+q.Owner.Instance(), installed) }, true); err != nil {
			return receipt, err
		}
		stopped := func() bool { p, e := r.System.Observe(ctx, generation); return e == nil && p.State == "inactive" }
		if err = effect("stop", stopped, func() error { return r.System.Stop(ctx, generation) }, true); err != nil {
			return receipt, err
		}
		// Missing drop-in bytes do not prove edge removal or daemon-reload
		// completed. Repeat this idempotent composite effect after interruption.
		if err = effect("remove_management", nil, func() error { return r.System.RemoveManagement(ctx, generation) }, true); err != nil {
			return receipt, err
		}
		installed.Retired = true
		installed.Revision = receipt.InputDigest
		receipt.InstallationRevision = installed.Revision
		if err = effect("retired", nil, func() error { return r.Store.write("installation-"+q.Owner.Instance(), installed) }, true); err != nil {
			return receipt, err
		}
		receipt.Retired = true
		receipt.State = "succeeded"
		receipt.Readiness.Process = "stopped"
		return receipt, nil
	}
	descriptor := publication.Descriptor
	if q.DescriptorDigest != applicationSHA(descriptor) || descriptor.Validate() != nil || q.Manifest == nil {
		return receipt, applicationError("artifact.selection_conflict")
	}
	manifestRaw, _ := json.Marshal(q.Manifest)
	if q.Manifest.Listener == nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(manifestRaw, &fields)
		delete(fields, "listener")
		manifestRaw, _ = json.Marshal(fields)
	}
	m, parseErr := ParseApplicationManifest(manifestRaw)
	if parseErr != nil {
		return receipt, applicationError("manifest.invalid")
	}
	if m.Artifact != descriptor.Artifact || m.Process.MemoryMaxBytes > grant.MaxMemoryBytes {
		return receipt, applicationError("manifest.policy_conflict")
	}
	if exists && installed.Generation != "" && installed.Descriptor.DataFormat != descriptor.DataFormat {
		compatible := false
		for _, f := range descriptor.CompatibleDataFormats {
			compatible = compatible || f == installed.Descriptor.DataFormat
		}
		if !compatible {
			return receipt, applicationError("data.format_migration_required")
		}
	}
	if q.Rollback && !reconciling && (installed.PreviousDescriptor == nil || applicationSHA(*installed.PreviousDescriptor) != applicationSHA(descriptor)) {
		return receipt, applicationError("rollback.not_previous")
	}
	config, configErr := applicationConfiguration(m, descriptor, grant)
	if configErr != nil {
		return receipt, configErr
	}
	for k, v := range m.Data {
		if v.QuotaBytes != 0 || q.Data[string(k)].QuotaBytes != 0 {
			return receipt, applicationError("data.quota_unsupported")
		}
	}
	if err = applicationValidateCredentials(grant, r.Store.OwnerUID); err != nil {
		return receipt, err
	}
	observations, unlockPools, dataErr := r.admitData(ctx, grant, q.Data)
	if dataErr != nil {
		return receipt, dataErr
	}
	defer unlockPools()
	receipt.Capacity = observations
	installed.Committed = false
	if err = effect("preparing", nil, func() error { return r.Store.write("installation-"+q.Owner.Instance(), installed) }, true); err != nil {
		return receipt, err
	}
	if err = effect("account", nil, func() error { return r.System.EnsureAccount(ctx, q.Owner, installed.UID, exists) }, true); err != nil {
		return receipt, err
	}
	for _, key := range applicationSortedData(grant) {
		var priorData *ApplicationDataIdentity
		if old, ok := installed.Data[key]; ok {
			priorData = &old
		}
		var identity ApplicationDataIdentity
		if err = effect("data:"+key, nil, func() error {
			var e error
			identity, e = r.System.EnsureData(ctx, grant.Data[key], installed.UID, priorData)
			return e
		}, true); err != nil {
			return receipt, err
		}
		// A completed step still revalidates the retained inode/custody.
		identity, err = r.System.EnsureData(ctx, grant.Data[key], installed.UID, priorData)
		if err != nil {
			return receipt, err
		}
		installed.Data[key] = identity
	}
	generation = ApplicationGeneration{Owner: q.Owner, ID: receipt.InputDigest[7:], Listener: m.Listener, UID: installed.UID, Descriptor: descriptor, Config: config}
	if reconciling {
		generation.Previous = installed.PreviousDescriptor
	} else if exists && installed.Generation != "" {
		previous := installed.Descriptor
		generation.Previous = &previous
	}
	generation.DropIn = applicationDropIn(grant, m, descriptor, r.System.ConfigurationPath(generation), installed.UID)
	if err = effect("generation", func() bool { return r.System.GenerationPublished(generation) }, func() error { return r.System.PublishGeneration(ctx, generation) }, true); err != nil {
		return receipt, err
	}
	processMatches := func() bool {
		p, e := r.System.Observe(ctx, generation)
		return e == nil && applicationProcessMatches(p, q.Owner, generation.ID, descriptor.Executable, installed.UID)
	}

	// Persist prepared custody and the new owner revision BEFORE sending restart.
	// This is not applied truth. It makes uncertain startup inspectable/retirable
	// and prevents a new token with an old revision from replacing partial work.
	if !reconciling {
		installed.Previous = installed.Generation
		if installed.Request != nil && installed.PreviousEndpoint == nil {
			installed.PreviousEndpoint = installed.Request.Endpoint
		}
		installed.PreviousDescriptor = generation.Previous
		now := time.Now().UTC()
		installed.AppliedAt = &now
	}
	installed.Request = &q
	installed.Generation = generation.ID
	installed.Descriptor = descriptor
	installed.Revision = receipt.InputDigest
	installed.Applied = false
	receipt.InstallationRevision = installed.Revision
	receipt.Generation = generation.ID
	if err = effect("installation", nil, func() error { return r.Store.write("installation-"+q.Owner.Instance(), installed) }, true); err != nil {
		return receipt, err
	}
	if err = effect("restart", processMatches, func() error { return r.System.Restart(ctx, generation) }, false); err != nil {
		return receipt, err
	}
	receipt.Applied = true
	installed.Applied = true
	if err = effect("applied", nil, func() error { return r.Store.write("installation-"+q.Owner.Instance(), installed) }, true); err != nil {
		return receipt, err
	}

	receipt.Data = installed.Data
	receipt.AppliedAt = installed.AppliedAt
	receipt.Process, receipt.Readiness, err = waitApplicationHealth(ctx, 30*time.Second, m, q.Owner, generation.ID, descriptor.Executable, installed.UID, func(c context.Context) (ApplicationProcessObservation, error) { return r.System.Observe(c, generation) })
	if err != nil {
		return receipt, err
	}
	if grant.Edge != nil || installed.PreviousEndpoint != nil {
		receipt.Readiness.Ingress = "pending"
		fragment, edgeErr := applicationEdgeFragment(grant, m)
		if edgeErr != nil {
			return receipt, edgeErr
		}
		combined := append([]byte(nil), fragment...)
		changingHostname := grant.Edge != nil && installed.PreviousEndpoint != nil && installed.PreviousEndpoint.Hostname != grant.Edge.Hostname
		if changingHostname {
			previous := *grant.Edge
			previous.Hostname = installed.PreviousEndpoint.Hostname
			priorGrant := grant
			priorGrant.Edge = &previous
			prior, e := applicationEdgeFragment(priorGrant, m)
			if e != nil {
				return receipt, e
			}
			combined = append(combined, prior...)
		}
		if err = effect("edge", nil, func() error { return r.System.PublishEdge(ctx, generation, combined) }, true); err != nil {
			return receipt, err
		}
		receipt.Readiness.Ingress = "configured"
		receipt.Endpoint = probeApplicationEdge(ctx, grant, m)
		if changingHostname {
			if receipt.Endpoint == nil || receipt.Endpoint.Routing != "satisfied" {
				return receipt, applicationError("edge.replacement_pending")
			}
			if err = effect("edge_retire_previous", nil, func() error { return r.System.PublishEdge(ctx, generation, fragment) }, true); err != nil {
				return receipt, err
			}
		}
	}
	installed.PreviousEndpoint = nil
	installed.Committed = true
	if err = effect("committed", nil, func() error { return r.Store.write("installation-"+q.Owner.Instance(), installed) }, true); err != nil {
		return receipt, err
	}
	receipt.State = "succeeded"
	return receipt, nil
}
func applicationSortedData(g ApplicationGrant) []string {
	keys := make([]string, 0, len(g.Data))
	for k := range g.Data {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
func (r ApplicationRuntime) fail(stage string) error {
	if r.FailureHook != nil {
		return r.FailureHook(stage)
	}
	return nil
}
func applicationSafeError(e error) string {
	v := e.Error()
	if strings.HasPrefix(v, "application.") && len(v) < 128 && !strings.ContainsAny(v, " /\n") {
		return v
	}
	return "application.effect.failed"
}
func receiptError(r ApplicationRuntimeReceipt) error {
	if r.ErrorCode != "" {
		return errors.New(r.ErrorCode)
	}
	return nil
}

// ApplicationHelperClient verifies the root socket activator independently of
// the root executor. SocketPath is local configuration, never an RPC field.
type ApplicationHelperClient struct{ SocketPath string }
type ApplicationHelperEnvelope struct {
	Prepared      *ApplicationPreparedPublication `json:"prepared,omitempty"`
	ProvisionPlan *ApplicationProvisionRequest    `json:"provision_plan,omitempty"`
	Provision     *ApplicationProvisionPlan       `json:"provision,omitempty"`
	Prerequisites *ApplicationPrerequisiteQuery   `json:"prerequisites,omitempty"`
	Archive       *ApplicationArchiveControl      `json:"archive,omitempty"`
	Request       *ApplicationRuntimeRequest      `json:"request,omitempty"`
	Publication   *ApplicationPublication         `json:"publication,omitempty"`
}
type ApplicationHelperResponse struct {
	Provision     *ApplicationProvisionPlan        `json:"provision,omitempty"`
	Prerequisites *ApplicationPrerequisiteSnapshot `json:"prerequisites,omitempty"`
	Manager       *ManagerResult                   `json:"manager,omitempty"`
	Receipt       *ApplicationRuntimeReceipt       `json:"receipt,omitempty"`
	ErrorCode     string                           `json:"error_code,omitempty"`
}
type ApplicationHelperConfig struct {
	// Host-managed exact ancestors only; these bindings never come from projects.
	TrustedParentOwners map[string]uint32 `json:"trusted_parent_owners,omitempty"`
	PassCLI             string            `json:"pass_cli,omitempty"`
	PassSessionEnsure   string            `json:"pass_session_ensure,omitempty"`
	FixtureFaultPath    string            `json:"fixture_fault_path,omitempty"`
	ConfigRoot          string            `json:"config_root"`
	PolicyPath          string            `json:"policy_path"`
	StateRoot           string            `json:"state_root"`
	UnitRoot            string            `json:"unit_root"`
	GCRoot              string            `json:"gc_root"`
	Systemctl           string            `json:"systemctl"`
	Sysusers            string            `json:"sysusers"`
	Nix                 string            `json:"nix"`
	EdgeRoot            string            `json:"edge_root,omitempty"`
	Caddy               string            `json:"caddy,omitempty"`
	CaddyConfig         string            `json:"caddy_config,omitempty"`
}

func applicationLoadedGeneration(raw []byte, g ApplicationGeneration, configPath string) bool {
	var reload, environment, command string
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "NeedDaemonReload":
			reload = value
		case "Environment":
			environment = value
		case "ExecStart":
			command = value
		}
	}
	return reload == "no" && strings.Contains(environment, "LOOM_APPLICATION_GENERATION="+g.ID) && strings.Contains(command, g.Descriptor.Launcher+" "+configPath)
}

func (r ApplicationRuntime) installedGeneration(i ApplicationInstallation, grant ApplicationGrant) (ApplicationGeneration, error) {
	g := ApplicationGeneration{Owner: i.Owner, ID: i.Generation, UID: i.UID, Descriptor: i.Descriptor, Previous: i.PreviousDescriptor}
	if i.Request == nil || i.Request.Manifest == nil || i.Request.Owner != i.Owner || i.Generation != applicationSHA(*i.Request)[7:] {
		return g, applicationError("installation.request_missing")
	}
	q := *i.Request
	raw, _ := json.Marshal(q)
	if _, err := DecodeApplicationRuntimeRequest(raw); err != nil {
		return g, err
	}
	m := q.Manifest
	// Saved private requests were validated before their effects; revalidate the
	// artifact-owned configuration and current resource policy when reconstructing.
	if m.Artifact != i.Descriptor.Artifact || m.Process.MemoryMaxBytes > grant.MaxMemoryBytes || i.Descriptor.Validate() != nil {
		return g, applicationError("installation.request_conflict")
	}
	config, err := applicationConfiguration(*m, i.Descriptor, grant)
	if err != nil {
		return g, err
	}
	g.Listener = m.Listener
	g.Config = config
	g.DropIn = applicationDropIn(grant, *m, i.Descriptor, r.System.ConfigurationPath(g), i.UID)
	return g, nil
}
func (r ApplicationRuntime) observeInstallation(ctx context.Context, i ApplicationInstallation, grant ApplicationGrant) (out ApplicationCurrentObservation, err error) {
	out = ApplicationCurrentObservation{ObservedAt: time.Now().UTC(), State: "observed", Readiness: applicationUnknownReadiness()}
	defer func() {
		out.ObservedAt = time.Now().UTC()
		if grant.Edge != nil && !i.Retired {
			out.Readiness.Ingress = "pending"
			if i.Request != nil && i.Request.Manifest != nil {
				out.Endpoint = probeApplicationEdge(ctx, grant, *i.Request.Manifest)
				if out.Endpoint.Routing == "satisfied" {
					out.Readiness.Ingress = "configured"
				}
			}
		}
		if err != nil {
			out.State = "pending"
			out.ErrorCode = applicationSafeError(err)
		}
	}()
	g, e := r.installedGeneration(i, grant)
	if e != nil {
		out.Process, _ = r.System.Observe(ctx, g)
		return out, e
	}
	out.ManagementPresent = r.System.GenerationPublished(g)
	if i.Retired || i.Fenced {
		out.Process, err = r.System.Observe(ctx, g)
		if err == nil && out.Process.State == "inactive" {
			out.Readiness.Process = "stopped"
			return out, nil
		}
		return out, applicationError("health.stopped_unproven")
	}
	out.Process, out.Readiness, err = ProbeApplicationHealth(ctx, *i.Request.Manifest, i.Owner, i.Generation, i.Descriptor.Executable, i.UID, func(c context.Context) (ApplicationProcessObservation, error) { return r.System.Observe(c, g) })
	if err == nil && !out.ManagementPresent {
		err = applicationError("installation.management_missing")
	}
	return out, err
}

// RestoreCommitted is the bounded boot oneshot, not an operation/authorization
// producer. Only owners in the current root policy are considered. Removing a
// grant removes that owner from boot admission; retained state is untouched.
func (r ApplicationRuntime) RestoreCommitted(ctx context.Context) ([]ApplicationStartupObservation, error) {
	policy, err := r.policy()
	if err != nil {
		return nil, err
	}
	owners, err := r.grantOwners(policy)
	if err != nil {
		return nil, err
	}
	if len(owners) > 128 {
		return nil, applicationError("boot.policy_limit")
	}
	results := make([]ApplicationStartupObservation, 0, len(owners))
	var resultErr error
	for _, owner := range owners {
		if err = ctx.Err(); err != nil {
			return results, err
		}
		observation := r.restoreCommittedOwner(ctx, owner)
		results = append(results, observation)
		if observation.State == "failed" {
			resultErr = applicationError("boot.incomplete")
		}
	}
	return results, resultErr
}

type ApplicationStartupObservation struct {
	Owner      ApplicationOwner               `json:"owner"`
	State      string                         `json:"state"`
	ErrorCode  string                         `json:"error_code,omitempty"`
	Current    *ApplicationCurrentObservation `json:"current,omitempty"`
	ObservedAt time.Time                      `json:"observed_at"`
}

func (r ApplicationRuntime) restoreCommittedOwner(ctx context.Context, owner ApplicationOwner) (out ApplicationStartupObservation) {
	out = ApplicationStartupObservation{Owner: owner, State: "skipped", ObservedAt: time.Now().UTC()}
	if !owner.valid() {
		out.ErrorCode = "application.boot.owner_invalid"
		return
	}
	release, err := r.Store.lock(ctx, owner.Instance())
	if err != nil {
		out.State = "failed"
		out.ErrorCode = applicationSafeError(err)
		return
	}
	defer release()
	defer func() {
		out.ObservedAt = time.Now().UTC()
		if e := r.Store.write("startup-"+owner.Instance(), out); e != nil {
			out.State = "failed"
			out.ErrorCode = applicationSafeError(e)
		}
	}()
	policy, err := r.policy()
	if err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	grant, err := r.resolveGrant(policy, owner)
	if err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	var i ApplicationInstallation
	if r.Store.read("installation-"+owner.Instance(), &i) != nil || i.Owner != owner || i.UID != applicationAccountUID(owner) || !i.Applied || !i.Committed || i.Fenced || i.Retired || i.Request == nil {
		out.ErrorCode = "application.boot.not_committed_or_fenced"
		return
	}
	q := *i.Request
	if q.Operation != "apply" {
		out.ErrorCode = "application.boot.request_invalid"
		return
	}
	var publication ApplicationPublication
	if r.Store.read("publication-"+owner.Instance(), &publication) != nil || publication.Owner != owner || publication.GrantDigest != applicationSHA(grant) || publication.Revision != q.ExpectedRevision || publication.PolicyRevision != q.PolicyRevision || publication.PolicyRevision != grant.PolicyRevision || publication.LocationRevision != q.LocationRevision || publication.LocationRevision != grant.LocationRevision || publication.Descriptor.RepositoryID != grant.RepositoryID || publication.Descriptor.Artifact.Platform != policy.Platform || applicationSHA(publication.Descriptor) != q.DescriptorDigest || applicationSHA(publication.Descriptor) != applicationSHA(i.Descriptor) {
		out.ErrorCode = "application.boot.revision_conflict"
		return
	}
	var journal ApplicationJournal
	key := "operation-" + owner.Instance() + "-" + applicationSHA(q.OperationToken)[7:]
	if r.Store.read(key, &journal) != nil || journal.Owner != owner || journal.Token != q.OperationToken || journal.InputDigest != i.Revision || journal.InputDigest != applicationSHA(q) || journal.Revision != q.ExpectedRevision || journal.Receipt == nil || journal.Receipt.State != "succeeded" || journal.Receipt.InstallationRevision != i.Revision || journal.Receipt.InputDigest != i.Revision {
		out.ErrorCode = "application.boot.operation_incomplete"
		return
	}
	credentials, err := applicationCredentialIdentities(grant, r.Store.OwnerUID)
	if err != nil || applicationSHA(credentials) != applicationSHA(journal.Receipt.Credentials) || len(q.CredentialRevisions) != len(grant.Credentials) {
		out.ErrorCode = "application.boot.credential_conflict"
		return
	}
	for ref, value := range grant.Credentials {
		if q.CredentialRevisions[ref] != value.Revision {
			out.ErrorCode = "application.boot.credential_conflict"
			return
		}
	}
	g, err := r.installedGeneration(i, grant)
	if err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	if err = r.System.VerifyArtifact(ctx, i.Descriptor); err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	if len(grant.Data) != len(i.Data) {
		out.ErrorCode = "application.boot.data_conflict"
		return
	}
	for key, prior := range i.Data {
		p, ok := grant.Data[key]
		if !ok {
			out.ErrorCode = "application.boot.data_conflict"
			return
		}
		if _, err = r.System.EnsureData(ctx, p, i.UID, &prior); err != nil {
			out.ErrorCode = applicationSafeError(err)
			return
		}
	}
	fence := func() error {
		fresh, e := r.policy()
		var current ApplicationPublication
		if e != nil || applicationSHA(fresh) != applicationSHA(policy) || r.Store.read("publication-"+owner.Instance(), &current) != nil || applicationSHA(current) != applicationSHA(publication) {
			return applicationError("boot.revision_changed")
		}
		return ctx.Err()
	}
	out.State = "failed"
	if err = fence(); err == nil {
		err = r.System.EnsureAccount(ctx, owner, i.UID, true)
	}
	if err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	if !r.System.GenerationPublished(g) {
		if err = fence(); err == nil {
			err = r.System.PublishGeneration(ctx, g)
		}
		if err == nil {
			err = r.fail("boot:generation")
		}
		if err != nil {
			out.ErrorCode = applicationSafeError(err)
			return
		}
	}
	process, err := r.System.Observe(ctx, g)
	if err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	if !applicationProcessMatches(process, owner, g.ID, g.Descriptor.Executable, g.UID) {
		if process.State != "inactive" && process.State != "failed" {
			out.ErrorCode = "application.boot.process_uncertain"
			return
		}
		if err = fence(); err == nil {
			err = r.System.Start(ctx, g)
		}
		if err == nil {
			err = r.fail("boot:start")
		}
		if err != nil {
			out.ErrorCode = applicationSafeError(err)
			return
		}
	}
	if grant.Edge != nil {
		fragment, e := applicationEdgeFragment(grant, *q.Manifest)
		if e == nil {
			e = fence()
		}
		if e == nil {
			e = r.System.PublishEdge(ctx, g, fragment)
		}
		if e != nil {
			out.ErrorCode = applicationSafeError(e)
			return
		}
	}
	current, err := r.observeInstallation(ctx, i, grant)
	out.Current = &current
	if err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	if err = r.fail("boot:observed"); err != nil {
		out.ErrorCode = applicationSafeError(err)
		return
	}
	out.State = "observed"
	return
}
