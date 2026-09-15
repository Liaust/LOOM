package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	sr "loom.local/loom/internal/serviceregistry"
)

type ApplicationPrerequisiteReader interface {
	QueryPrerequisites(context.Context, sr.ApplicationPrerequisiteQuery) (sr.ApplicationPrerequisiteSnapshot, error)
}

type ApplicationCalls interface {
	Call(context.Context, requestctx.Context, routing.CapabilityCallInput, string) (routing.CapabilityCallOutcome, error)
	FindCapabilityCallByToken(context.Context, string, string) (routing.CapabilityCall, error)
}

type applicationPayload struct {
	Endpoint    string                       `json:"endpoint"`
	Request     sr.ApplicationRuntimeRequest `json:"request"`
	Preparation *applicationPreparation      `json:"preparation,omitempty"`
}

func applicationFailure(err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "application.") && causePattern.MatchString(err.Error()) {
		return fail(pc.DeclarationOwnerFailed, err.Error())
	}
	return fail(pc.DeclarationTargetUnavailable, "application_runtime_unavailable")
}

func (r *LocalResolver) appendApplications(ctx context.Context, x localDeclaration, basis *pc.DeclarationPlanBasis, payloads map[string]json.RawMessage) error {
	d := x.Analysis.Loaded.Declaration
	keys := make([]pc.ResourceKey, 0, len(d.Resources))
	for key := range d.Resources {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	issues := []pc.DeclarationPreflightIssue{}
	for _, key := range keys {
		res := d.Resources[key]
		if res.Kind != pc.DeclarationApplication {
			continue
		}
		var raw []byte
		for _, source := range x.Analysis.Plan.Declaration.Sources {
			if source.Ref == res.Application.Manifest {
				raw = source.Raw
			}
		}
		manifest, err := sr.ParseApplicationManifest(raw)
		if err != nil {
			return fail(pc.DeclarationInvalid, "application_manifest_invalid")
		}
		repo := basis.Bindings[res.Application.Repository].RepositoryID
		owner := sr.ApplicationOwner{ProjectID: x.Target.ProjectID, NodeID: x.Target.OwnerNodeID, Resource: string(key)}
		var facts sr.ApplicationPrerequisiteSnapshot
		var observed *sr.ApplicationPrerequisiteSnapshot
		if !nilInterface(r.Applications) {
			facts, err = r.Applications.QueryPrerequisites(ctx, sr.ApplicationPrerequisiteQuery{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: owner})
			if err == nil {
				observed = &facts
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		currentIssues := applicationPreflight(x.Target, key, *res.Application, repo, manifest, observed)
		var prepared *applicationPreparation
		var q sr.ApplicationRuntimeRequest
		if res.Application.ArtifactDescriptor != "" {
			currentIssues = managedApplicationPreflight(x.Target, key, *res.Application, repo, manifest, observed)
			var descriptor []byte
			for _, source := range x.Analysis.Plan.Declaration.Sources {
				if source.Ref == res.Application.ArtifactDescriptor {
					descriptor = source.Raw
				}
			}
			if len(currentIssues) == 0 && x.Authority.CanWrite && x.Authority.Level >= 5 {
				q, prepared, err = r.prepareApplication(ctx, x.Target, key, *res.Application, repo, manifest, observed, descriptor)
				if err != nil {
					var failure *Failure
					if errors.As(err, &failure) && failure.Cause == "application.provisioning.disabled" {
						currentIssues = append(currentIssues, applicationPreflightIssue("application_node_policy_required", key, "grant"))
					} else {
						return err
					}
				}
			}
		}
		if !x.Authority.CanWrite || x.Authority.Level < 5 {
			currentIssues = append(currentIssues, applicationPreflightIssue("application_execution_authority_required", key, "authority"))
		}
		issues = append(issues, currentIssues...)
		if len(currentIssues) > 0 {
			continue
		}
		if prepared == nil {
			q, err = applicationRequest(x.Target, key, *res.Application, repo, manifest, facts)
			if err != nil {
				return err
			}
		}
		payload, err := canonicalValue(applicationPayload{Endpoint: capabilities.NodeSystemProviderAddress(x.Input.Project.OwnerNode) + ".project.application.apply", Request: q, Preparation: prepared})
		if err != nil {
			return err
		}
		auth := basis.Actions[0].Authorization
		auth.Level = 5
		a := pc.DeclarationAction{ID: "apply_application:" + string(key), Kind: pc.DeclarationApplyApplication, Owner: pc.DeclarationOwnerApplication, Resource: key, TargetRef: x.Target.ProjectID + "/" + string(key), InputHash: hashBytes(payload), DependsOn: []string{basis.Actions[len(basis.Actions)-1].ID}, Authorization: auth}
		if q.Endpoint != nil {
			a.PublicURL = "https://" + q.Endpoint.Hostname
		}
		basis.Actions = append(basis.Actions, a)
		payloads[a.ID] = payload
	}
	if len(issues) > 0 {
		return &Failure{Code: pc.DeclarationTargetUnavailable, Cause: "application_preflight_blocked", Preflight: issues}
	}
	return nil
}

func applicationRequest(target pc.DeclarationTarget, key pc.ResourceKey, d pc.ApplicationDeclaration, repo string, m sr.ApplicationManifest, f sr.ApplicationPrerequisiteSnapshot) (sr.ApplicationRuntimeRequest, error) {
	var q sr.ApplicationRuntimeRequest
	if f.GrantMissing {
		return q, fail(pc.DeclarationTargetUnavailable, "application_grant_required")
	}
	owner := sr.ApplicationOwner{ProjectID: target.ProjectID, NodeID: target.OwnerNodeID, Resource: string(key)}
	if f.SchemaVersion != sr.ApplicationPrerequisiteSchema || f.Owner != owner || f.RepositoryID != repo || repo == "" || f.LocationRevision != target.LocationRevision {
		return q, fail(pc.DeclarationIdentityConflict, "application_project_binding_changed")
	}
	p := f.Publication
	if !p.Present || p.Artifact == nil || p.GrantMatches == nil || !*p.GrantMatches {
		return q, fail(pc.DeclarationTargetUnavailable, "application_publication_required")
	}
	if p.ArchiveTarget == nil || projectquiescence.ValidateTarget(*p.ArchiveTarget) != nil || p.ArchiveTarget.Kind != projectquiescence.TargetKindService || p.ArchiveTarget.Unit != owner.Unit() || p.ArchiveTarget.AllowlistKey != owner.AllowlistKey() || p.ArchiveTarget.Manager != "systemd" {
		return q, fail(pc.DeclarationTargetUnavailable, "application_service_registration_required")
	}
	if p.Artifact.RepositoryID != repo || p.Artifact.Artifact != m.Artifact || f.Platform != m.Artifact.Platform || p.Artifact.ConfigurationSchema != m.Config.Schema || p.PolicyRevision != f.PolicyRevision || p.LocationRevision != f.LocationRevision {
		return q, fail(pc.DeclarationPlanStale, "application_publication_mismatch")
	}
	if d.Endpoint != nil && (d.Endpoint.Exposure != pc.ApplicationLoopback || d.Endpoint.EndpointRef != "") {
		return q, fail(pc.DeclarationUnsupported, "application_loopback_adapter_only")
	}
	if f.Installation.Present && (f.Installation.Fenced == nil || *f.Installation.Fenced || f.Installation.Retired == nil || *f.Installation.Retired) {
		return q, fail(pc.DeclarationTargetUnavailable, "application_installation_fenced")
	}
	q = sr.ApplicationRuntimeRequest{SchemaVersion: sr.ApplicationRuntimeSchema, Operation: "apply", Owner: owner, ExpectedRevision: p.Revision, ExpectedInstallationRevision: f.Installation.Revision, PolicyRevision: f.PolicyRevision, LocationRevision: f.LocationRevision, DescriptorDigest: p.Artifact.DescriptorDigest, Manifest: &m, Data: map[string]sr.ApplicationDataRequest{}, CredentialRevisions: map[string]string{}}
	return applicationRequirements(q, d, m, f.Data, f.Credentials)
}

func applicationRequirements(q sr.ApplicationRuntimeRequest, d pc.ApplicationDeclaration, m sr.ApplicationManifest, dataFacts map[string]sr.ApplicationPrerequisiteData, credentials map[string]sr.ApplicationPrerequisiteCredential) (sr.ApplicationRuntimeRequest, error) {
	if len(d.Data) != len(m.Data) || len(d.Data) != len(dataFacts) {
		return q, fail(pc.DeclarationInvalid, "application_data_set_mismatch")
	}
	for key, data := range d.Data {
		fact, ok := dataFacts[string(key)]
		requirement, declared := m.Data[key]
		if !ok || !declared || data.Path != "" || data.BindingRef == "" || data.BindingRef != fact.BindingRef {
			return q, fail(pc.DeclarationTargetUnavailable, "application_data_binding_required")
		}
		if data.Protection != "" {
			return q, fail(pc.DeclarationUnsupported, "application_data_protection_adapter_required")
		}
		if requirement.QuotaBytes != 0 {
			return q, fail(pc.DeclarationUnsupported, "application_quota_not_supported")
		}
		if fact.Availability == "unavailable" || fact.Custody == "conflict" {
			return q, fail(pc.DeclarationTargetUnavailable, "application_data_unavailable")
		}
		if data.Backup != "" && (data.Backup != "cloud_history" || d.ArtifactDescriptor == "") {
			return q, fail(pc.DeclarationInvalid, "application_backup_invalid")
		}
		request := sr.ApplicationDataRequest{BindingRef: data.BindingRef, Backup: data.Backup}
		if data.Capacity != nil {
			request.PlannedBytes = data.Capacity.PlannedBytes
		}
		q.Data[string(key)] = request
	}
	if len(d.Credentials) != len(credentials) {
		return q, fail(pc.DeclarationTargetUnavailable, "application_credential_binding_required")
	}
	for _, ref := range d.Credentials {
		credential, ok := credentials[ref]
		source := d.CredentialSources[ref]
		_, _, validSource := pc.ApplicationCredentialSourceShare(source)
		planned := validSource && credential.Availability == "planned" && credential.Revision == sr.ApplicationProtonCredentialRevision(source)
		if !ok || (!planned && credential.Availability != "available") || credential.Revision == "" {
			return q, fail(pc.DeclarationTargetUnavailable, "application_credential_unavailable")
		}
		q.CredentialRevisions[ref] = credential.Revision
	}
	for _, parameter := range m.Config.Values {
		if parameter.CredentialRef != nil && q.CredentialRevisions[*parameter.CredentialRef] == "" {
			return q, fail(pc.DeclarationInvalid, "application_credential_not_declared")
		}
		if parameter.DataRef != nil {
			if _, ok := q.Data[string(*parameter.DataRef)]; !ok {
				return q, fail(pc.DeclarationInvalid, "application_data_not_declared")
			}
		}
	}
	return q, nil
}

// ApplicationOwner dispatches through the existing authenticated node route.
// Pending/ambiguous delivery is never repeated. An exact partial helper receipt
// can continue its journal through a new delivery, retaining the helper token.
type ApplicationInspector interface {
	Execute(context.Context, sr.ApplicationRuntimeRequest) (sr.ApplicationRuntimeReceipt, error)
}

type ApplicationOwner struct {
	Backup    ApplicationBackupStatus
	Calls     ApplicationCalls
	Inspector ApplicationInspector
	Preparer  ApplicationPreparer
}

func (o ApplicationOwner) ResourceStatus(ctx context.Context, a pc.DeclarationAction, raw json.RawMessage) (pc.DeclarationReadiness, error) {
	out := readiness(a.InputHash)
	if err := o.Validate(ctx, a, raw); err != nil {
		return out, err
	}
	if nilInterface(o.Inspector) {
		return out, fail(pc.DeclarationTargetUnavailable, "application_runtime_not_configured")
	}
	var p applicationPayload
	_ = json.Unmarshal(raw, &p)
	q := p.Request
	if q.Endpoint != nil {
		out.Endpoint = &pc.ApplicationEndpointStatus{URL: "https://" + q.Endpoint.Hostname, DNS: "unknown", TLS: "unknown", Routing: "unknown", ObservedAt: time.Now().UTC()}
	}
	q.Operation, q.OperationToken = "inspect", "project-status"
	result, err := o.Inspector.Execute(ctx, q)
	if err != nil {
		return out, applicationFailure(err)
	}
	if result.DesiredMatches == nil || !*result.DesiredMatches {
		out.Applied.State = pc.DeclarationPending
		return out, nil
	}
	out.Applied = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: result.InstallationRevision, EvidenceRef: "application-installation:" + q.Owner.Instance()}
	if result.Current != nil {
		out.Endpoint = result.Current.Endpoint
	}
	if result.Current != nil && result.Current.State == "observed" && result.Current.ErrorCode == "" && result.Current.Readiness.Process == "satisfied" && (result.Current.Readiness.Protocol == "satisfied" || result.Current.Readiness.Protocol == "not_applicable") {
		out.Healthy = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: result.Generation, EvidenceRef: out.Applied.EvidenceRef}
	} else {
		out.Healthy.State = pc.DeclarationFailed
	}
	if q.Endpoint != nil && (out.Endpoint == nil || out.Endpoint.Routing != "satisfied") {
		out.Healthy.State = pc.DeclarationPending
	}
	// Running is not evidence of backup coverage or authenticated client behavior.
	for _, data := range q.Data {
		if data.Backup == "cloud_history" {
			out.Protected.State = pc.DeclarationPending
			if o.Backup != nil {
				out.Protected, err = o.Backup.ApplicationProtection(ctx, q, result)
				if err != nil {
					return out, err
				}
			}
			break
		}
	}
	return out, nil
}

func (o ApplicationOwner) Validate(_ context.Context, a pc.DeclarationAction, raw json.RawMessage) error {
	var p applicationPayload
	if strictOwnerPayload(raw, &p) != nil || a.Kind != pc.DeclarationApplyApplication || a.Owner != pc.DeclarationOwnerApplication || p.Request.Operation != "apply" || p.Request.OperationToken != "" || p.Request.Owner.ProjectID+"/"+p.Request.Owner.Resource != a.TargetRef || p.Request.Owner.Resource != string(a.Resource) || p.Request.Manifest == nil || p.Request.Rollback || !strings.HasPrefix(p.Endpoint, "workspace/") || !strings.HasSuffix(p.Endpoint, ".project.application.apply") {
		return fail(pc.DeclarationInvalid, "application_payload_invalid")
	}
	address, err := capabilities.ParseAddress(p.Endpoint)
	if err != nil || p.Endpoint != capabilities.NodeSystemProviderAddress(strings.TrimPrefix(address.ScopePath, "workspace/"))+".project.application.apply" {
		return fail(pc.DeclarationInvalid, "application_payload_invalid")
	}
	q := p.Request
	expectedURL := ""
	if q.Endpoint != nil {
		expectedURL = "https://" + q.Endpoint.Hostname
	}
	if a.PublicURL != expectedURL {
		return fail(pc.DeclarationInvalid, "application_endpoint_review_mismatch")
	}
	if p.Preparation != nil && validateApplicationPreparation(p) != nil {
		return fail(pc.DeclarationInvalid, "application_preparation_invalid")
	}
	q.OperationToken = "validate"
	encoded, _ := json.Marshal(q)
	if _, err := sr.DecodeApplicationRuntimeRequest(encoded); err != nil {
		return fail(pc.DeclarationInvalid, "application_request_invalid")
	}
	return nil
}

func applicationCallInput(call ActionCall) (applicationPayload, []byte, error) {
	var p applicationPayload
	if err := strictOwnerPayload(call.Payload, &p); err != nil {
		return p, nil, err
	}
	if p.Request.Owner.ProjectID != call.Target.ProjectID || p.Request.Owner.NodeID != call.Target.OwnerNodeID || p.Request.LocationRevision != call.Target.LocationRevision {
		return p, nil, fail(pc.DeclarationIdentityConflict, "application_target_mismatch")
	}
	p.Request.OperationToken = call.Token
	raw, err := json.Marshal(p.Request)
	return p, raw, err
}

func (o ApplicationOwner) Observe(ctx context.Context, call ActionCall) (Observation, error) {
	observed, _, err := o.observeDelivery(ctx, call)
	return observed, err
}

// Delivery receipts are immutable. Follow deterministic continuation tokens to
// the latest delivery while retaining the original helper token and input.
func (o ApplicationOwner) observeDelivery(ctx context.Context, call ActionCall) (Observation, string, error) {
	if nilInterface(o.Calls) {
		return Observation{}, "", fail(pc.DeclarationTargetUnavailable, "application_routing_not_configured")
	}
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, "", err
	}
	p, raw, err := applicationCallInput(call)
	if err != nil {
		return Observation{}, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token := call.Token
	previous := Observation{State: Absent}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return Observation{}, "", err
		}
		if seen[token] {
			return Observation{}, "", fail(pc.DeclarationOperationConflict, "application_recovery_cycle")
		}
		seen[token] = true
		found, err := o.Calls.FindCapabilityCallByToken(ctx, call.Principal.ActorID, token)
		if errors.Is(err, sql.ErrNoRows) {
			return previous, token, nil
		}
		if err != nil {
			return Observation{}, "", err
		}
		if found.ActorID != call.Principal.ActorID || found.OriginNodeID != call.Principal.OriginNodeID || found.TargetNodeID != call.Target.OwnerNodeID || found.Operation != "capability:"+p.Endpoint || !sameJSON(found.InputJSON, raw) {
			return Observation{}, "", fail(pc.DeclarationOperationConflict, "application_call_identity_conflict")
		}
		var receipt sr.ApplicationRuntimeReceipt
		bound := json.Unmarshal(found.ResultJSON, &receipt) == nil && receipt.SchemaVersion == sr.ApplicationRuntimeSchema && receipt.Owner == p.Request.Owner && receipt.OperationToken == call.Token && receipt.InputDigest == p.Request.Digest() && receipt.InstallationRevision == receipt.InputDigest && receipt.Applied && !receipt.Retired && receipt.Revision == p.Request.ExpectedRevision && receipt.EvidenceRef != ""
		if found.Status == routing.CapabilityCallStatusFailed {
			cause := "application_call_failed"
			if found.ErrorCode != nil && causePattern.MatchString(*found.ErrorCode) {
				cause = *found.ErrorCode
			}
			if !bound || receipt.State != "partial" || receipt.ErrorCode != cause || !strings.HasPrefix(cause, "application.") || found.CapabilityCallID == "" {
				return Observation{State: Uncertain, CauseCode: cause}, "", nil
			}
			previous = Observation{State: Resumable, CauseCode: cause}
			token = hashBytes([]byte("application-continuation:" + call.Token + ":" + found.CapabilityCallID))
			continue
		}
		if found.Status != routing.CapabilityCallStatusCompleted {
			return Observation{State: Pending, CauseCode: "application_node_execution_pending"}, "", nil
		}
		if !bound || receipt.State != "succeeded" {
			return Observation{}, "", fail(pc.DeclarationOwnerFailed, "application_receipt_invalid")
		}
		return Observation{State: Committed, Receipt: &Receipt{Token: call.Token, ActionID: call.Action.ID, Owner: call.Action.Owner, InputHash: call.Action.InputHash, EffectRef: receipt.EvidenceRef, Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{}}}, "", nil
	}
}

func sameJSON(a, b []byte) bool {
	x, ex := CanonicalJSON(a)
	y, ey := CanonicalJSON(b)
	return ex == nil && ey == nil && string(x) == string(y)
}

func (o ApplicationOwner) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	observed, deliveryToken, err := o.observeDelivery(ctx, call)
	if err != nil || (observed.State != Absent && observed.State != Resumable) {
		return observed, err
	}
	p, raw, err := applicationCallInput(call)
	if err != nil {
		return Observation{}, err
	}
	if call.Fence == nil {
		return Observation{}, fail(pc.DeclarationUnauthorized, "application_fence_required")
	}
	if err = call.Fence.Check(ctx); err != nil {
		return Observation{}, err
	}
	req := declarationRequest(call.Principal)
	req.CorrelationID = call.OperationID
	if p.Preparation != nil && observed.State == Absent {
		if nilInterface(o.Preparer) {
			return Observation{}, fail(pc.DeclarationTargetUnavailable, "application_preparer_not_configured")
		}
		if err = o.Preparer.Prepare(ctx, req, call, p); err != nil {
			return Observation{}, applicationFailure(err)
		}
		if err = call.Fence.Check(ctx); err != nil {
			return Observation{}, err
		}
	}
	_, err = o.Calls.Call(ctx, req, routing.CapabilityCallInput{Target: p.Endpoint, Input: raw}, deliveryToken)
	if err != nil {
		return Observation{}, applicationFailure(err)
	}
	// Normal local-node dispatch is asynchronous. Briefly await its existing
	// receipt; longer operations remain pending and resume via project apply.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		observed, err = o.Observe(ctx, call)
		if err != nil || observed.State != Pending {
			return observed, err
		}
		select {
		case <-ctx.Done():
			return observed, ctx.Err()
		case <-deadline.C:
			return observed, nil
		case <-tick.C:
		}
	}
}
