package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
)

type ApplicationActionRequest struct {
	Owner                        ApplicationOwner `json:"owner"`
	Name                         string           `json:"name"`
	Token                        string           `json:"token"`
	ExpectedInstallationRevision string           `json:"expected_installation_revision"`
	ReleaseFailed                bool             `json:"release_failed,omitempty"`
}

type ApplicationActionPhase string

const (
	ApplicationActionIntent     ApplicationActionPhase = "intent"
	ApplicationActionQuiescing  ApplicationActionPhase = "quiescing"
	ApplicationActionSubmitting ApplicationActionPhase = "submitting"
	ApplicationActionPending    ApplicationActionPhase = "pending"
	ApplicationActionResuming   ApplicationActionPhase = "resuming"
	ApplicationActionSucceeded  ApplicationActionPhase = "succeeded"
	ApplicationActionFailed     ApplicationActionPhase = "failed"
	ApplicationActionUncertain  ApplicationActionPhase = "uncertain"
)

type ApplicationActionReceipt struct {
	SchemaVersion        string                 `json:"schema_version"`
	Owner                ApplicationOwner       `json:"owner"`
	Name                 string                 `json:"name"`
	Token                string                 `json:"token"`
	InstallationRevision string                 `json:"installation_revision"`
	Unit                 string                 `json:"unit"`
	Phase                ApplicationActionPhase `json:"phase"`
	ExitCode             *int                   `json:"exit_code,omitempty"`
	ErrorCode            string                 `json:"error_code,omitempty"`
	ReadOnly             bool                   `json:"read_only"`
	Finalized            bool                   `json:"finalized"`
	ServicePaused        bool                   `json:"service_paused"`
	ServiceResumed       bool                   `json:"service_resumed"`
}

type applicationActionExecution struct {
	Request        ApplicationActionRequest
	Generation     ApplicationGeneration
	Grant          ApplicationGrant
	Unit           string
	Config         []byte
	MemoryMaxBytes uint64
}

type applicationActionObservation struct {
	Phase     ApplicationActionPhase
	ExitCode  *int
	ErrorCode string
}

type applicationActionSystem interface {
	startApplicationAction(context.Context, applicationActionExecution) error
	observeApplicationAction(context.Context, applicationActionExecution) (applicationActionObservation, error)
}

type applicationActionJournal struct {
	Request         ApplicationActionRequest `json:"request"`
	RequestDigest   string                   `json:"request_digest"`
	PolicyDigest    string                   `json:"policy_digest"`
	GrantDigest     string                   `json:"grant_digest"`
	PublicationHash string                   `json:"publication_hash"`
	CredentialsHash string                   `json:"credentials_hash"`
	WasActive       bool                     `json:"was_active"`
	ResumeIntent    bool                     `json:"resume_intent"`
	Receipt         ApplicationActionReceipt `json:"receipt"`
}

type applicationActionLease struct {
	Owner         ApplicationOwner `json:"owner"`
	Token         string           `json:"token"`
	RequestDigest string           `json:"request_digest"`
	Active        bool             `json:"active"`
}

func (q ApplicationActionRequest) valid() bool {
	return q.Owner.valid() && applicationKeyPattern.MatchString(q.Name) && applicationRevisionPattern.MatchString(q.Token) && applicationDigestPattern.MatchString(q.ExpectedInstallationRevision)
}

func applicationActionUnit(q ApplicationActionRequest) string {
	return "loom-application-action-" + q.Owner.Instance() + "-" + applicationSHA(q.Token)[7:] + ".service"
}

func applicationActionKey(q ApplicationActionRequest) string {
	return "action-" + q.Owner.Instance() + "-" + applicationSHA(q.Token)[7:]
}

func (r ApplicationRuntime) readAction(q ApplicationActionRequest) (applicationActionJournal, error) {
	var j applicationActionJournal
	if err := r.Store.read(applicationActionKey(q), &j); err != nil {
		return j, err
	}
	if j.Request != q || j.RequestDigest != applicationSHA(q) || j.Receipt.Owner != q.Owner || j.Receipt.Token != q.Token || j.Receipt.Name != q.Name || j.Receipt.InstallationRevision != q.ExpectedInstallationRevision || j.Receipt.Unit != applicationActionUnit(q) || j.Receipt.SchemaVersion != "application.action.v1" || !applicationActionPhaseValid(j.Receipt.Phase) {
		return j, applicationError("action.token_conflict")
	}
	return j, nil
}

func applicationActionPhaseValid(p ApplicationActionPhase) bool {
	switch p {
	case ApplicationActionIntent, ApplicationActionQuiescing, ApplicationActionSubmitting, ApplicationActionPending, ApplicationActionResuming, ApplicationActionSucceeded, ApplicationActionFailed, ApplicationActionUncertain:
		return true
	}
	return false
}

// Caller holds Store.lock(owner.Instance()). Failed/uncertain work stays held;
// a stale or unreadable record is never treated as an absent admission fence.
func (r ApplicationRuntime) applicationActionActive(owner ApplicationOwner) (bool, error) {
	var lease applicationActionLease
	err := r.Store.read("active-action-"+owner.Instance(), &lease)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !owner.valid() || lease.Owner != owner || !applicationRevisionPattern.MatchString(lease.Token) || !applicationDigestPattern.MatchString(lease.RequestDigest) {
		return true, applicationError("action.lease_uncertain")
	}
	if lease.Active {
		return true, nil
	}
	var j applicationActionJournal
	if r.Store.read("action-"+owner.Instance()+"-"+applicationSHA(lease.Token)[7:], &j) != nil || j.RequestDigest != lease.RequestDigest || j.Request.Owner != owner || j.Request.Token != lease.Token || j.Receipt.Phase != ApplicationActionSucceeded && !(j.Receipt.Phase == ApplicationActionFailed && j.Receipt.Finalized) {
		return true, applicationError("action.lease_uncertain")
	}
	if _, err = r.readAction(j.Request); err != nil {
		return true, applicationError("action.lease_uncertain")
	}
	return false, nil
}

func (r ApplicationRuntime) actionContext(ctx context.Context, peer uint32, q ApplicationActionRequest, effects bool) (applicationActionExecution, applicationActionJournal, error) {
	var e applicationActionExecution
	var pin applicationActionJournal
	policy, err := r.policy()
	if err != nil {
		return e, pin, err
	}
	if peer != policy.InstallerUID {
		return e, pin, applicationError("peer.denied")
	}
	grant, err := r.resolveGrant(policy, q.Owner)
	if err != nil {
		return e, pin, err
	}
	var i ApplicationInstallation
	if r.Store.read("installation-"+q.Owner.Instance(), &i) != nil || i.Owner != q.Owner || i.UID != applicationAccountUID(q.Owner) || i.Revision != q.ExpectedInstallationRevision || !i.Applied || !i.Committed || i.Fenced || i.Retired || i.Request == nil || i.Request.Operation != "apply" {
		return e, pin, applicationError("action.installation_not_current")
	}
	var p ApplicationPublication
	request := *i.Request
	if r.Store.read("publication-"+q.Owner.Instance(), &p) != nil || p.Owner != q.Owner || p.GrantDigest != applicationSHA(grant) || p.Revision != request.ExpectedRevision || p.PolicyRevision != request.PolicyRevision || p.PolicyRevision != grant.PolicyRevision || p.LocationRevision != request.LocationRevision || p.LocationRevision != grant.LocationRevision || p.Descriptor.ProducerIdentity() != grant.ProducerIdentity() || p.Descriptor.Artifact.Platform != policy.Platform || applicationSHA(p.Descriptor) != applicationSHA(i.Descriptor) || applicationSHA(p.Descriptor) != request.DescriptorDigest {
		return e, pin, applicationError("action.revision_conflict")
	}
	definition, ok := i.Descriptor.Actions[q.Name]
	if !ok {
		return e, pin, applicationError("action.not_declared")
	}
	var prior ApplicationJournal
	if r.Store.read("operation-"+q.Owner.Instance()+"-"+applicationSHA(request.OperationToken)[7:], &prior) != nil || prior.Owner != q.Owner || prior.InputDigest != applicationSHA(request) || prior.InputDigest != i.Revision || prior.Receipt == nil || prior.Receipt.State != "succeeded" || prior.Receipt.InstallationRevision != i.Revision {
		return e, pin, applicationError("action.installation_not_committed")
	}
	credentials, err := applicationCredentialIdentities(grant, r.Store.OwnerUID)
	if err != nil || applicationSHA(credentials) != applicationSHA(prior.Receipt.Credentials) || len(request.CredentialRevisions) != len(grant.Credentials) || len(i.Data) != len(grant.Data) {
		return e, pin, applicationError("action.bindings_changed")
	}
	for ref, credential := range grant.Credentials {
		if request.CredentialRevisions[ref] != credential.Revision {
			return e, pin, applicationError("action.bindings_changed")
		}
	}
	g, err := r.installedGeneration(i, grant)
	if err != nil {
		return e, pin, err
	}
	if effects {
		if err = r.System.VerifyArtifact(ctx, i.Descriptor); err != nil {
			return e, pin, err
		}
		for key, identity := range i.Data {
			binding, ok := grant.Data[key]
			if !ok {
				return e, pin, applicationError("action.bindings_changed")
			}
			if _, err = r.System.EnsureData(ctx, binding, i.UID, &identity); err != nil {
				return e, pin, err
			}
		}
	}
	unit := applicationActionUnit(q)
	config, err := applicationActionConfiguration(*request.Manifest, i.Descriptor, grant, unit)
	if err != nil {
		return e, pin, err
	}
	e = applicationActionExecution{Request: q, Generation: g, Grant: grant, Unit: unit, Config: config, MemoryMaxBytes: request.Manifest.Process.MemoryMaxBytes}
	pin = applicationActionJournal{PolicyDigest: applicationSHA(policy), GrantDigest: applicationSHA(grant), PublicationHash: applicationSHA(p), CredentialsHash: applicationSHA(credentials)}
	pin.Receipt.ReadOnly = definition.ReadOnly
	return e, pin, nil
}

func applicationActionSamePins(a, b applicationActionJournal) bool {
	return a.PolicyDigest == b.PolicyDigest && a.GrantDigest == b.GrantDigest && a.PublicationHash == b.PublicationHash && a.CredentialsHash == b.CredentialsHash
}

func applicationActionConfiguration(m ApplicationManifest, d ApplicationArtifactDescriptor, grant ApplicationGrant, unit string) ([]byte, error) {
	raw, err := applicationConfiguration(m, d, grant)
	if err != nil {
		return nil, err
	}
	var config struct {
		Schema string                     `json:"schema"`
		Values map[string]json.RawMessage `json:"values"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return nil, applicationError("action.configuration_invalid")
	}
	for key, parameter := range m.Config.Values {
		if parameter.Type == ApplicationCredentialReference {
			config.Values[key], err = json.Marshal(map[string]string{"credential_file": "/run/credentials/" + unit + "/" + applicationCredentialName(*parameter.CredentialRef)})
			if err != nil {
				return nil, applicationError("action.configuration_invalid")
			}
		}
	}
	return json.Marshal(config)
}

func (r ApplicationRuntime) actionAdmission(ctx context.Context, peer uint32, q ApplicationActionRequest) (func(), error) {
	if !q.valid() || r.System == nil {
		return nil, applicationError("action.request_invalid")
	}
	p, err := r.policy()
	if err != nil {
		return nil, err
	}
	if peer != p.InstallerUID {
		return nil, applicationError("peer.denied")
	}
	release, err := r.Store.lock(ctx, q.Owner.Instance())
	if err != nil {
		return nil, err
	}
	p, err = r.policy()
	if err == nil && peer == p.InstallerUID {
		_, err = r.resolveGrant(p, q.Owner)
	} else if err == nil {
		err = applicationError("peer.denied")
	}
	if err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (r ApplicationRuntime) saveAction(j applicationActionJournal) error {
	return r.Store.write(applicationActionKey(j.Request), j)
}

func (r ApplicationRuntime) holdAction(j applicationActionJournal, active bool) error {
	return r.Store.write("active-action-"+j.Request.Owner.Instance(), applicationActionLease{Owner: j.Request.Owner, Token: j.Request.Token, RequestDigest: j.RequestDigest, Active: active})
}

func (r ApplicationRuntime) actionError(j *applicationActionJournal, err error) (ApplicationActionReceipt, error) {
	j.Receipt.Phase, j.Receipt.ErrorCode = ApplicationActionUncertain, applicationSafeError(err)
	if e := r.saveAction(*j); e != nil {
		return j.Receipt, e
	}
	return j.Receipt, err
}

// RunAction holds the existing owner lock only for admission/submission or one
// observation. The durable active-action record fences other work while the
// unit runs asynchronously. No existing token ever submits an action again.
func (r ApplicationRuntime) RunAction(ctx context.Context, peer uint32, q ApplicationActionRequest) (ApplicationActionReceipt, error) {
	var empty ApplicationActionReceipt
	releaseFailed := q.ReleaseFailed
	q.ReleaseFailed = false
	release, err := r.actionAdmission(ctx, peer, q)
	if err != nil {
		return empty, err
	}
	defer release()
	system, ok := r.System.(applicationActionSystem)
	if !ok {
		return empty, applicationError("action.backend_unavailable")
	}
	j, readErr := r.readAction(q)
	if releaseFailed {
		if readErr != nil {
			return empty, applicationError("action.failed_receipt_required")
		}
		return r.releaseFailedAction(ctx, peer, system, &j)
	}
	if readErr == nil && j.Receipt.Phase == ApplicationActionFailed {
		return j.Receipt, nil
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return empty, readErr
	}
	e, pins, err := r.actionContext(ctx, peer, q, false)
	if err != nil {
		return j.Receipt, err
	}
	if readErr == nil {
		if !applicationActionSamePins(j, pins) {
			return j.Receipt, applicationError("action.revision_conflict")
		}
		return r.observeAction(ctx, peer, system, e, &j)
	}
	if active, err := r.applicationActionActive(q.Owner); err != nil || active {
		if err == nil {
			err = applicationError("action.active")
		}
		return empty, err
	}
	process, err := r.System.Observe(ctx, e.Generation)
	if err != nil || process.State != "inactive" && process.State != "failed" && !applicationProcessMatches(process, q.Owner, e.Generation.ID, e.Generation.Descriptor.Executable, e.Generation.UID) {
		return empty, applicationError("action.service_uncertain")
	}
	j = pins
	j.Request, j.RequestDigest, j.WasActive = q, applicationSHA(q), process.State == "active"
	j.Receipt = ApplicationActionReceipt{SchemaVersion: "application.action.v1", Owner: q.Owner, Name: q.Name, Token: q.Token, InstallationRevision: q.ExpectedInstallationRevision, Unit: e.Unit, Phase: ApplicationActionIntent, ReadOnly: pins.Receipt.ReadOnly}
	if err = r.saveAction(j); err != nil {
		return j.Receipt, err
	}
	if err = r.holdAction(j, true); err != nil {
		return j.Receipt, err
	}
	if err = r.fail("action:intent"); err != nil {
		return j.Receipt, err
	}
	j.Receipt.Phase = ApplicationActionQuiescing
	if err = r.saveAction(j); err != nil {
		return j.Receipt, err
	}
	// Read-only actions also serialize and stop: no competing same-UID writer.
	if err = r.System.Stop(ctx, e.Generation); err != nil {
		return r.actionError(&j, applicationError("action.stop_uncertain"))
	}
	process, err = r.System.Observe(ctx, e.Generation)
	if err != nil || process.PID != 0 || process.State != "inactive" && process.State != "failed" {
		return r.actionError(&j, applicationError("action.stop_uncertain"))
	}
	j.Receipt.ServicePaused = true
	_, fresh, err := r.actionContext(ctx, peer, q, true)
	if err != nil || !applicationActionSamePins(j, fresh) {
		return r.actionError(&j, applicationError("action.revision_conflict"))
	}
	if network, ok := r.System.(applicationNetworkSystem); ok {
		err = network.ensureApplicationNetwork(ctx, e.Generation, e.Grant)
	} else if e.Grant.Network == ApplicationNetworkOutboundHTTPS {
		err = applicationError("network.backend_unavailable")
	}
	if err != nil {
		return r.actionError(&j, err)
	}
	j.Receipt.Phase = ApplicationActionSubmitting
	if err = r.saveAction(j); err != nil {
		return j.Receipt, err
	}
	if err = r.fail("action:submit_intent"); err != nil {
		return j.Receipt, err
	}
	_, fresh, err = r.actionContext(ctx, peer, q, false)
	if err != nil || !applicationActionSamePins(j, fresh) {
		return r.actionError(&j, applicationError("action.revision_conflict"))
	}
	if err = system.startApplicationAction(ctx, e); err != nil {
		code := applicationSafeError(err)
		if code == "application.effect.failed" {
			code = "application.action.submit_uncertain"
		}
		return r.actionError(&j, errors.New(code))
	}
	if err = r.fail("action:submitted"); err != nil {
		return j.Receipt, err
	}
	j.Receipt.Phase = ApplicationActionPending
	if err = r.saveAction(j); err != nil {
		return j.Receipt, err
	}
	return r.observeAction(ctx, peer, system, e, &j)
}

// Explicit acknowledgement releases only a proved failed, quiescent action.
// It neither erases its receipt, repeats its package nor resumes its consumer.
func (r ApplicationRuntime) releaseFailedAction(ctx context.Context, peer uint32, system applicationActionSystem, j *applicationActionJournal) (ApplicationActionReceipt, error) {
	if j.Receipt.Phase == ApplicationActionSucceeded {
		return j.Receipt, applicationError("action.failed_receipt_required")
	}
	var lease applicationActionLease
	if err := r.Store.read("active-action-"+j.Request.Owner.Instance(), &lease); err != nil || lease.Owner != j.Request.Owner || lease.Token != j.Request.Token || lease.RequestDigest != j.RequestDigest {
		return j.Receipt, applicationError("action.lease_uncertain")
	}
	if !lease.Active && j.Receipt.Finalized {
		return j.Receipt, nil
	}
	e, pins, err := r.actionContext(ctx, peer, j.Request, false)
	if err != nil || !applicationActionSamePins(*j, pins) {
		return j.Receipt, applicationError("action.revision_conflict")
	}
	observation, err := system.observeApplicationAction(ctx, e)
	if err != nil || observation.Phase != ApplicationActionFailed || observation.ExitCode == nil || j.Receipt.ExitCode != nil && *observation.ExitCode != *j.Receipt.ExitCode {
		return j.Receipt, applicationError("action.observation_uncertain")
	}
	process, err := r.System.Observe(ctx, e.Generation)
	if err != nil || process.PID != 0 || process.State != "inactive" && process.State != "failed" {
		return j.Receipt, applicationError("action.service_not_quiescent")
	}
	j.Receipt.Phase, j.Receipt.ExitCode, j.Receipt.ErrorCode = ApplicationActionFailed, observation.ExitCode, "application.action.exit_failed"
	j.Receipt.Finalized = true
	if err = r.saveAction(*j); err == nil {
		err = r.holdAction(*j, false)
	}
	return j.Receipt, err
}

func (r ApplicationRuntime) observeAction(ctx context.Context, peer uint32, system applicationActionSystem, e applicationActionExecution, j *applicationActionJournal) (ApplicationActionReceipt, error) {
	if j.Receipt.Phase == ApplicationActionSucceeded {
		// A crash after the success journal but before releasing this exact lease
		// must not release a different, later action's fence.
		var lease applicationActionLease
		if err := r.Store.read("active-action-"+j.Request.Owner.Instance(), &lease); err != nil {
			return j.Receipt, err
		}
		if lease.Active && lease.Token == j.Request.Token && lease.RequestDigest == j.RequestDigest {
			if err := r.holdAction(*j, false); err != nil {
				return j.Receipt, err
			}
		}
		j.Receipt.Finalized = true
		return j.Receipt, r.saveAction(*j)
	}
	observation, err := system.observeApplicationAction(ctx, e)
	if err != nil || observation.Phase == ApplicationActionUncertain {
		return r.actionError(j, applicationError("action.observation_uncertain"))
	}
	if observation.Phase != ApplicationActionPending && observation.Phase != ApplicationActionSucceeded && observation.Phase != ApplicationActionFailed {
		return r.actionError(j, applicationError("action.observation_invalid"))
	}
	j.Receipt.ExitCode = observation.ExitCode
	if observation.Phase != ApplicationActionSucceeded {
		j.Receipt.Phase = observation.Phase
		if observation.Phase == ApplicationActionFailed {
			j.Receipt.ErrorCode = "application.action.exit_failed"
		}
		return j.Receipt, r.saveAction(*j)
	}
	if observation.ExitCode == nil || *observation.ExitCode != 0 {
		return r.actionError(j, applicationError("action.observation_invalid"))
	}
	_, fresh, err := r.actionContext(ctx, peer, j.Request, true)
	if err != nil || !applicationActionSamePins(*j, fresh) {
		return r.actionError(j, applicationError("action.revision_conflict"))
	}
	if j.WasActive {
		if !r.System.GenerationPublished(e.Generation) {
			return r.actionError(j, applicationError("action.generation_changed"))
		}
		if network, ok := r.System.(applicationNetworkSystem); ok {
			if err = network.ensureApplicationNetwork(ctx, e.Generation, e.Grant); err != nil {
				return r.actionError(j, err)
			}
		} else if e.Grant.Network == ApplicationNetworkOutboundHTTPS {
			return r.actionError(j, applicationError("network.backend_unavailable"))
		}
		if !j.ResumeIntent {
			j.ResumeIntent, j.Receipt.Phase = true, ApplicationActionResuming
			if err = r.saveAction(*j); err != nil {
				return j.Receipt, err
			}
			if err = r.System.Start(ctx, e.Generation); err != nil {
				return r.actionError(j, applicationError("action.resume_uncertain"))
			}
		}
		process, err := r.System.Observe(ctx, e.Generation)
		if err != nil || !applicationProcessMatches(process, e.Generation.Owner, e.Generation.ID, e.Generation.Descriptor.Executable, e.Generation.UID) {
			return r.actionError(j, applicationError("action.resume_uncertain"))
		}
		j.Receipt.ServiceResumed = true
	}
	j.Receipt.Phase, j.Receipt.ErrorCode = ApplicationActionSucceeded, ""
	if err = r.saveAction(*j); err != nil {
		return j.Receipt, err
	}
	if err = r.holdAction(*j, false); err != nil {
		return j.Receipt, err
	}
	j.Receipt.Finalized = true
	return j.Receipt, r.saveAction(*j)
}

// InspectAction never writes journals, changes the lease, stops/resumes a
// service or submits a unit. A successful observed action can remain held until
// RunAction reconciles the durable result and any permitted service resume.
func (r ApplicationRuntime) InspectAction(ctx context.Context, peer uint32, q ApplicationActionRequest) (ApplicationActionReceipt, error) {
	var empty ApplicationActionReceipt
	if q.ReleaseFailed {
		return empty, applicationError("action.request_invalid")
	}
	release, err := r.actionAdmission(ctx, peer, q)
	if err != nil {
		return empty, err
	}
	defer release()
	j, err := r.readAction(q)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, applicationError("action.missing")
		}
		return empty, err
	}
	if j.Receipt.Phase == ApplicationActionFailed || j.Receipt.Phase == ApplicationActionSucceeded {
		return j.Receipt, nil
	}
	e, pins, err := r.actionContext(ctx, peer, q, false)
	if err != nil || !applicationActionSamePins(j, pins) {
		j.Receipt.Phase, j.Receipt.ErrorCode = ApplicationActionUncertain, "application.action.revision_conflict"
		return j.Receipt, nil
	}
	system, ok := r.System.(applicationActionSystem)
	if !ok {
		return j.Receipt, applicationError("action.backend_unavailable")
	}
	observation, err := system.observeApplicationAction(ctx, e)
	validObservation := observation.Phase == ApplicationActionPending || observation.Phase == ApplicationActionFailed || observation.Phase == ApplicationActionUncertain || observation.Phase == ApplicationActionSucceeded && observation.ExitCode != nil && *observation.ExitCode == 0
	if err != nil || !validObservation {
		j.Receipt.Phase, j.Receipt.ErrorCode = ApplicationActionUncertain, "application.action.observation_uncertain"
	} else {
		j.Receipt.Phase, j.Receipt.ExitCode = observation.Phase, observation.ExitCode
		j.Receipt.Finalized = false
		if observation.Phase == ApplicationActionSucceeded {
			j.Receipt.ServicePaused, j.Receipt.ServiceResumed = true, false
		}
	}
	return j.Receipt, nil
}

func applicationActionCommandArgs(e applicationActionExecution, configPath string) ([]string, error) {
	if !e.Request.valid() || e.Unit != applicationActionUnit(e.Request) || e.Generation.Owner != e.Request.Owner || e.Generation.UID != applicationAccountUID(e.Request.Owner) || e.Grant.Owner != e.Request.Owner || e.Generation.Descriptor.Validate() != nil || !applicationAbsolutePath(configPath) || e.MemoryMaxBytes == 0 || e.MemoryMaxBytes > e.Grant.MaxMemoryBytes {
		return nil, applicationError("action.execution_invalid")
	}
	if _, declared := e.Generation.Descriptor.Actions[e.Request.Name]; !declared {
		return nil, applicationError("action.not_declared")
	}
	uid := strconv.FormatUint(uint64(e.Generation.UID), 10)
	environment := "LOOM_APPLICATION_GENERATION=" + e.Generation.ID
	if e.Grant.SourceIdentity != "" {
		environment += " SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt"
	}
	args := []string{"--quiet", "--no-block", "--unit=" + e.Unit, "--service-type=exec", "--description=LOOM action " + applicationSHA(e.Request)}
	properties := []string{
		"User=" + uid, "Group=" + uid, "DynamicUser=no", "PrivateUsers=no", "PrivateNetwork=no",
		"RemainAfterExit=yes", "CollectMode=inactive", "Restart=no", "RuntimeMaxSec=300", "TimeoutStartSec=30", "TimeoutStopSec=20", "KillMode=control-group",
		"StandardInput=null", "StandardOutput=null", "StandardError=null", "UMask=0077",
		"NoNewPrivileges=yes", "RestrictSUIDSGID=yes", "PrivateTmp=yes", "PrivateDevices=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectControlGroups=yes", "RestrictNamespaces=yes", "LockPersonality=yes",
		"CapabilityBoundingSet=", "AmbientCapabilities=", "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6", "IPAccounting=yes",
		"MemoryMax=" + strconv.FormatUint(e.MemoryMaxBytes, 10), "ReadOnlyPaths=" + configPath, "TasksAccounting=yes", "Slice=system.slice",
		"Environment=" + environment,
	}
	if e.Grant.Network != ApplicationNetworkOutboundHTTPS {
		properties = append(properties, "IPAddressDeny=any", "IPAddressAllow=localhost")
	}
	var paths []string
	for _, key := range applicationSortedData(e.Grant) {
		path := e.Grant.Data[key].Path
		if !applicationAbsolutePath(path) {
			return nil, applicationError("action.data_path_invalid")
		}
		paths = append(paths, path)
	}
	if e.Generation.Descriptor.Actions[e.Request.Name].ReadOnly {
		properties = append(properties, "ReadWritePaths=", "ReadOnlyPaths="+strings.Join(paths, " "))
	} else {
		properties = append(properties, "ReadWritePaths="+strings.Join(paths, " "))
	}
	for _, ref := range applicationNetworkSortedKeys(e.Grant.Credentials) {
		path := e.Grant.Credentials[ref].Path
		if !applicationAbsolutePath(path) {
			return nil, applicationError("action.credential_path_invalid")
		}
		properties = append(properties, "LoadCredential="+applicationCredentialName(ref)+":"+path)
	}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	return append(args, "--", e.Generation.Descriptor.Launcher, "--action", e.Request.Name, configPath), nil
}

const applicationActionUnitProperties = "LoadState,Transient,Type,Description,User,Group,ExecStart,ActiveState,SubState,MainPID,ControlPID,ExecMainCode,ExecMainStatus,Result,TasksCurrent,ControlGroup,Job"

func applicationActionJournalFailure(raw []byte, e applicationActionExecution) applicationActionObservation {
	unknown := applicationActionObservation{Phase: ApplicationActionUncertain, ErrorCode: "application.action.unit_uncertain"}
	if len(raw) == 0 || len(raw) > 65536 {
		return unknown
	}
	var invocation string
	var exit *int
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			return unknown
		}
		get := func(key string) string { var s string; _ = json.Unmarshal(v[key], &s); return s }
		if get("_UID") != "0" || get("_PID") != "1" || get("SYSLOG_IDENTIFIER") != "systemd" || get("UNIT") != e.Unit {
			continue
		}
		id := get("INVOCATION_ID")
		if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
			return unknown
		}
		if get("MESSAGE_ID") == "7d4958e842da4a758f6c1cdc7b36dcc5" {
			if invocation != "" || get("MESSAGE") != "Starting LOOM action "+applicationSHA(e.Request)+"..." {
				return unknown
			}
			invocation = id
		} else if invocation != "" && id != invocation {
			return unknown
		}
		if get("MESSAGE_ID") == "98e322203f7a4ed290d09fe03c09fe15" {
			status, err := strconv.Atoi(get("EXIT_STATUS"))
			if invocation == "" || id != invocation || exit != nil || get("EXIT_CODE") != "exited" || err != nil || status < 1 || status > 255 {
				return unknown
			}
			exit = &status
		}
	}
	if exit == nil {
		return unknown
	}
	return applicationActionObservation{Phase: ApplicationActionFailed, ExitCode: exit}
}

func applicationActionUnitObservation(raw []byte, e applicationActionExecution, configPath string) applicationActionObservation {
	unknown := applicationActionObservation{Phase: ApplicationActionUncertain, ErrorCode: "application.action.unit_uncertain"}
	if len(raw) == 0 || len(raw) > 32768 {
		return unknown
	}
	v := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return unknown
		}
		if _, exists := v[key]; exists {
			return unknown
		}
		v[key] = value
	}
	for _, key := range strings.Split(applicationActionUnitProperties, ",") {
		if _, ok := v[key]; !ok {
			return unknown
		}
	}
	uid := strconv.FormatUint(uint64(e.Generation.UID), 10)
	exec := v["ExecStart"]
	argv := e.Generation.Descriptor.Launcher + " --action " + e.Request.Name + " " + configPath
	if v["LoadState"] != "loaded" || v["Transient"] != "yes" || v["Type"] != "exec" || v["Description"] != "LOOM action "+applicationSHA(e.Request) || v["User"] != uid || v["Group"] != uid || strings.Count(exec, "path=") != 1 || strings.Count(exec, "argv[]=") != 1 || !strings.Contains(exec, "path="+e.Generation.Descriptor.Launcher+" ;") || !strings.Contains(exec, "argv[]="+argv+" ;") {
		return unknown
	}
	pid, err := strconv.ParseUint(v["MainPID"], 10, 32)
	if err != nil {
		return unknown
	}
	if v["ActiveState"] == "activating" || v["ActiveState"] == "inactive" && v["Job"] != "" || v["ActiveState"] == "active" && v["SubState"] == "running" && pid > 0 {
		return applicationActionObservation{Phase: ApplicationActionPending}
	}
	// systemd may release the empty cgroup while retaining a successful
	// RemainAfterExit unit. An unavailable count alone is not proof of quiescence.
	emptyTasks := v["TasksCurrent"] == "0" || v["TasksCurrent"] == "[not set]" && v["ControlGroup"] == ""
	if pid != 0 || v["ControlPID"] != "0" || !emptyTasks {
		return unknown
	}
	status, err := strconv.Atoi(v["ExecMainStatus"])
	if err != nil || status < 0 || status > 255 {
		return unknown
	}
	if v["ExecMainCode"] == "1" && status == 0 && v["Result"] == "success" && v["ActiveState"] == "active" && v["SubState"] == "exited" {
		return applicationActionObservation{Phase: ApplicationActionSucceeded, ExitCode: &status}
	}
	if v["ActiveState"] == "failed" && v["SubState"] == "failed" && (v["ExecMainCode"] == "1" || v["ExecMainCode"] == "2" || v["ExecMainCode"] == "3") {
		result := applicationActionObservation{Phase: ApplicationActionFailed}
		if v["ExecMainCode"] == "1" {
			result.ExitCode = &status
		}
		return result
	}
	return unknown
}
