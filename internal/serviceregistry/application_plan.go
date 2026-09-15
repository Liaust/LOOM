package serviceregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	wr "loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
)

const ApplicationPlanSchema = "application.plan.v1"

// ApplicationSource is a caller-supplied snapshot, never a path to open. Hash
// must match Bytes; Revision fences the authorized source resolver's selection.
type ApplicationSource struct {
	Source pc.DeclarationSource `json:"source"`
	Bytes  []byte               `json:"bytes"`
}

// ApplicationPlanInput is an internal trusted-adapter input, NOT an HTTP request
// accepting authorization claims. Future E2/E3 adapters must obtain these facts
// through existing authenticated owners and recheck them at every execution.
type ApplicationPlanInput struct {
	Target   pc.DeclarationTarget         `json:"target"`
	Resource pc.ResourceKey               `json:"resource"`
	Effects  []pc.DeclarationEffect       `json:"effects"`
	Sources  map[string]ApplicationSource `json:"sources"`
	Facts    ApplicationTargetFacts       `json:"facts"`
}
type ApplicationScope struct {
	ProjectID string         `json:"project_id"`
	NodeID    string         `json:"node_id"`
	Resource  pc.ResourceKey `json:"resource"`
	Revision  string         `json:"revision"`
}
type ApplicationTargetFacts struct {
	Scope                ApplicationScope                       `json:"scope"`
	Available            bool                                   `json:"available"`
	Platform             string                                 `json:"platform"`
	Systemd              bool                                   `json:"systemd"`
	Active               bool                                   `json:"active"`
	LifecycleRevision    string                                 `json:"lifecycle_revision"`
	Repository           ApplicationRepositoryFact              `json:"repository"`
	Artifact             ApplicationArtifactFact                `json:"artifact"`
	User                 string                                 `json:"user"`
	AvailableMemoryBytes uint64                                 `json:"available_memory_bytes"`
	Port                 uint16                                 `json:"port"`
	ListenerAddress      string                                 `json:"listener_address"`
	PortAvailable        bool                                   `json:"port_available"`
	Data                 map[pc.ResourceKey]ApplicationDataFact `json:"data"`
	Endpoint             *ApplicationEndpointFact               `json:"endpoint,omitempty"`
	Credentials          map[string]ApplicationCredentialFact   `json:"credentials"`
	Authorization        ApplicationAuthority                   `json:"authorization"`
}
type ApplicationRepositoryFact struct {
	ProjectID string         `json:"project_id"`
	Key       pc.ResourceKey `json:"key"`
	ID        string         `json:"id"`
	Path      string         `json:"path"`
	Revision  string         `json:"revision"`
	Owned     bool           `json:"owned"`
}
type ApplicationArtifactFact struct {
	Ref                            string                         `json:"ref"`
	Digest                         string                         `json:"digest"`
	Platform                       string                         `json:"platform"`
	Revision                       string                         `json:"revision"`
	Reviewed                       bool                           `json:"reviewed"`
	Configuration                  ApplicationConfigurationSchema `json:"configuration"`
	ConfigurationDeliverySupported bool                           `json:"configuration_delivery_supported"`
}
type ApplicationDataFact struct {
	Scope            ApplicationScope `json:"scope"`
	BindingRef       string           `json:"binding_ref,omitempty"`
	Path             string           `json:"path"`
	ExistingPath     string           `json:"existing_path,omitempty"`
	Persistent       bool             `json:"persistent"`
	CustodyChecked   bool             `json:"custody_checked"`
	Allocated        bool             `json:"allocated"`
	AvailableBytes   uint64           `json:"available_bytes"`
	PoolRevision     string           `json:"pool_revision"`
	CapacityPool     string           `json:"capacity_pool"`
	MonitorSupported bool             `json:"monitor_supported"`
	QuotaSupported   bool             `json:"quota_supported"`
	// ProtectionPath is the exact policy-root selection for an approved external
	// allocation. No portable source is rewritten to an absolute host path.
	ProtectionPath string `json:"protection_path,omitempty"`
}
type ApplicationEndpointFact struct {
	Scope                   ApplicationScope       `json:"scope"`
	Ref                     string                 `json:"ref"`
	Exposure                pc.ApplicationExposure `json:"exposure"`
	URL                     string                 `json:"url"`
	Purpose                 string                 `json:"purpose"`
	Backend                 string                 `json:"backend"`
	Available               bool                   `json:"available"`
	Private                 bool                   `json:"private"`
	ApprovedApplicationEdge bool                   `json:"approved_application_edge"`
	ApprovalRef             string                 `json:"approval_ref,omitempty"`
}
type ApplicationCredentialFact struct {
	Scope              ApplicationScope `json:"scope"`
	Ref                string           `json:"ref"`
	Provider           string           `json:"provider"`
	ProviderSupported  bool             `json:"provider_supported"`
	ReferenceAvailable bool             `json:"reference_available"`
	DeliverySupported  bool             `json:"delivery_supported"`
}
type ApplicationAuthority struct {
	ActorID          string `json:"actor_id"`
	NodeID           string `json:"node_id"`
	PolicyRevision   string `json:"policy_revision"`
	Allowed          bool   `json:"allowed"`
	Level            int    `json:"level"`
	RequiredLevel    int    `json:"required_level"`
	ApprovalRequired bool   `json:"approval_required"`
	ApprovalRef      string `json:"approval_ref,omitempty"`
}
type ApplicationStageKind string

const (
	ApplicationArtifactStage   ApplicationStageKind = "artifact"
	ApplicationDataStage       ApplicationStageKind = "data"
	ApplicationCredentialStage ApplicationStageKind = "credential"
	ApplicationProcessStage    ApplicationStageKind = "process"
	ApplicationHealthStage     ApplicationStageKind = "health"
	ApplicationEndpointStage   ApplicationStageKind = "endpoint"
	ApplicationProtectionStage ApplicationStageKind = "protection"
)

type ApplicationStage struct {
	ID            string                      `json:"id"`
	Kind          ApplicationStageKind        `json:"kind"`
	TargetRef     string                      `json:"target_ref"`
	DependsOn     []string                    `json:"depends_on"`
	Adapter       string                      `json:"adapter"`
	InputHash     string                      `json:"input_hash"`
	Authorization pc.DeclarationAuthorization `json:"authorization"`
	Acceptance    string                      `json:"acceptance"`
	Rollback      string                      `json:"rollback"`
}
type ApplicationDataPlan struct {
	Path                  string `json:"path"`
	BindingRef            string `json:"binding_ref,omitempty"`
	PlannedBytes          uint64 `json:"planned_bytes"`
	MonitorThresholdBytes uint64 `json:"monitor_threshold_bytes"`
	QuotaBytes            uint64 `json:"quota_bytes"`
	// PlannedBytes is neither reserved space nor an enforced quota. QuotaBytes
	// is a requested future effect, not proof that a quota has been installed.
	CapacityReserved bool   `json:"capacity_reserved"`
	QuotaEnforced    bool   `json:"quota_enforced"`
	Removal          string `json:"removal"`
}
type ApplicationProtectionPlan struct {
	Source   pc.DeclarationSource    `json:"source"`
	Enabled  bool                    `json:"enabled"`
	Defaults pc.BackupRootPolicySpec `json:"defaults"`
	Root     pc.BackupRootPolicySpec `json:"root"`
}
type ApplicationPlanBasis struct {
	SchemaVersion string                                       `json:"schema_version"`
	Input         ApplicationPlanInput                         `json:"input"`
	Manifest      ApplicationManifest                          `json:"manifest"`
	Data          map[pc.ResourceKey]ApplicationDataPlan       `json:"data"`
	Protection    map[pc.ResourceKey]ApplicationProtectionPlan `json:"protection"`
	Stages        []ApplicationStage                           `json:"stages"`
}
type ApplicationPlan struct {
	SchemaVersion          string                                       `json:"schema_version"`
	PlanID                 string                                       `json:"plan_id,omitempty"`
	Ready                  bool                                         `json:"ready"`
	Executable             bool                                         `json:"executable"`
	Basis                  *ApplicationPlanBasis                        `json:"basis,omitempty"`
	Stages                 []ApplicationStage                           `json:"stages"`
	Data                   map[pc.ResourceKey]ApplicationDataPlan       `json:"data"`
	Protection             map[pc.ResourceKey]ApplicationProtectionPlan `json:"protection"`
	Readiness              pc.DeclarationReadiness                      `json:"readiness"`
	Errors                 []pc.DeclarationError                        `json:"errors"`
	ExecutionPrerequisites []string                                     `json:"execution_prerequisites"`
}

// ApplicationSourceHash binds exact bytes, including comments.
func ApplicationSourceHash(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// applicationHash uses recursive byte-key ordering, exact JSON integers and Go
// escaping, matching D0 canonical JSON rather than float64 map round-tripping.
func applicationHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("application identity contains non-JSON value")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if dec.Decode(&tree) != nil {
		panic("application identity could not be decoded")
	}
	b, err = json.Marshal(tree)
	if err != nil {
		panic("application identity could not be encoded")
	}
	return ApplicationSourceHash(b)
}

// CompileApplicationPlan is pure: no clock, network, filesystem, credential
// lookup, registry write, provisioning, job, event or process call. Invalid
// inputs never return raw sources, partially usable stages, or an identity.
func CompileApplicationPlan(input ApplicationPlanInput) ApplicationPlan {
	p := ApplicationPlan{SchemaVersion: ApplicationPlanSchema, Stages: []ApplicationStage{}, Data: map[pc.ResourceKey]ApplicationDataPlan{}, Protection: map[pc.ResourceKey]ApplicationProtectionPlan{}, Errors: []pc.DeclarationError{}, ExecutionPrerequisites: []string{"E2: reviewed artifact/systemd execution adapter", "E3: project apply authorization/reconstruction/idempotency adapter"}}
	unknown := pc.DeclarationFact{State: pc.DeclarationUnknown}
	p.Readiness = pc.DeclarationReadiness{Desired: unknown, Queued: unknown, Applied: unknown, Processing: unknown, Healthy: unknown, Protected: unknown, Verified: unknown}
	fail := func(code pc.DeclarationErrorCode, field, cause string) {
		p.Errors = append(p.Errors, pc.DeclarationError{Code: code, Field: field, Message: "application prerequisite unavailable or invalid", CauseCode: cause, CompletedEffects: []string{}})
	}
	// Detach all caller-owned maps/slices/pointers, including snapshots. No alias
	// in the returned basis can be mutated by later changes to the caller's input.
	raw, err := json.Marshal(input)
	if err != nil {
		fail(pc.DeclarationInvalid, "input", "input.shape")
		return p
	}
	var in ApplicationPlanInput
	if json.Unmarshal(raw, &in) != nil {
		fail(pc.DeclarationInvalid, "input", "input.shape")
		return p
	}
	if in.Effects == nil {
		in.Effects = []pc.DeclarationEffect{pc.DeclarationReconcile}
	}
	if len(in.Effects) != 1 || in.Effects[0] != pc.DeclarationReconcile {
		fail(pc.DeclarationUnsupported, "effects", "effect.unsupported")
	}
	target := in.Target
	facts := in.Facts
	scopeOK := func(s ApplicationScope) bool {
		return s.ProjectID == target.ProjectID && s.NodeID == target.OwnerNodeID && s.Resource == in.Resource && applicationText(s.Revision)
	}
	if !applicationProjectPattern.MatchString(target.ProjectID) || !applicationKeyPattern.MatchString(string(in.Resource)) || !applicationText(target.OwnerNodeID) || !applicationAbsolute(target.ProjectRoot) || !applicationText(target.LocationRevision) || !scopeOK(facts.Scope) || !facts.Available || !facts.Active || !applicationText(facts.LifecycleRevision) {
		fail(pc.DeclarationTargetUnavailable, "target", "target.location_lifecycle")
	}
	authority := facts.Authorization
	if !authority.Allowed || !applicationText(authority.ActorID) || authority.NodeID != target.OwnerNodeID || !applicationText(authority.PolicyRevision) || authority.RequiredLevel < 1 || authority.RequiredLevel > 5 || authority.Level < authority.RequiredLevel || authority.Level > 5 {
		fail(pc.DeclarationUnauthorized, "authority", "authority.actor_node")
	}
	if authority.ApprovalRequired && !applicationText(authority.ApprovalRef) {
		fail(pc.DeclarationApprovalRequired, "authority", "authority.approval")
	}
	source := func(ref, schema string) (ApplicationSource, bool) {
		s, ok := in.Sources[ref]
		if !ok || !applicationRelative(ref, true) || s.Source.Ref != ref || s.Source.SchemaVersion != schema || !applicationText(s.Source.Revision) || s.Source.Hash != ApplicationSourceHash(s.Bytes) {
			fail(pc.DeclarationReferenceMissing, "sources", "source.snapshot")
			return ApplicationSource{}, false
		}
		return s, true
	}
	root, ok := source(".loom/project.yaml", pc.ProjectSchemaV05)
	if !ok {
		return p
	}
	var project pc.ProjectDeclaration
	if applicationDecode(root.Bytes, &project) != nil || project.Kind != "loom.project" || project.SchemaVersion != pc.ProjectSchemaV05 || project.Project.ID != target.ProjectID || project.Project.OwnerNode != target.OwnerNodeID || project.Resources == nil {
		fail(pc.DeclarationInvalid, "project", "project.source_scope")
		return p
	}
	if !applicationText(project.Project.Name) || !applicationKeyPattern.MatchString(project.Project.Slug) {
		fail(pc.DeclarationInvalid, "project", "project.identity")
	}
	switch project.Project.Status {
	case "", "draft", "active", "paused", "archived":
	default:
		fail(pc.DeclarationInvalid, "project", "project.status")
	}
	if project.Project.Status == "archived" || project.Project.Status == "paused" {
		fail(pc.DeclarationTargetUnavailable, "project", "project.inactive")
	}
	selected, ok := project.Resources[in.Resource]
	if !ok || selected.Kind != pc.DeclarationApplication || selected.Application == nil {
		fail(pc.DeclarationReferenceMissing, "application", "application.declaration")
		return p
	}
	app := selected.Application
	// Validate closed unions and portable paths across this source before checking
	// application data overlap. D1 remains owner of general declaration compilation.
	for _, key := range applicationKeys(project.Resources) {
		r := project.Resources[key]
		count := 0
		for _, b := range []bool{r.Repository != nil, r.Knowledge != nil, r.Protection != nil, r.Application != nil} {
			if b {
				count++
			}
		}
		match := (r.Kind == pc.DeclarationRepository && r.Repository != nil) || (r.Kind == pc.DeclarationKnowledge && r.Knowledge != nil) || (r.Kind == pc.DeclarationProtection && r.Protection != nil) || (r.Kind == pc.DeclarationApplication && r.Application != nil)
		if !applicationKeyPattern.MatchString(string(key)) || count != 1 || !match {
			fail(pc.DeclarationInvalid, "resources", "resource.union")
			continue
		}
		if r.Repository != nil && !applicationRelative(r.Repository.Path, false) || r.Knowledge != nil && !applicationRelative(r.Knowledge.Path, false) {
			fail(pc.DeclarationInvalid, "resources", "resource.path")
		}
	}
	repo, ok := project.Resources[app.Repository]
	rf := facts.Repository
	if !ok || repo.Kind != pc.DeclarationRepository || repo.Repository == nil {
		fail(pc.DeclarationReferenceMissing, "repository", "repository.reference")
	} else if rf.Key != app.Repository || !rf.Owned || rf.ProjectID != target.ProjectID || !applicationRepoPattern.MatchString(rf.ID) || !applicationText(rf.Revision) || rf.Path != repo.Repository.Path || (repo.Repository.ID != "" && repo.Repository.ID != rf.ID) || (repo.Repository.Role != "primary" && repo.Repository.Role != "component") {
		fail(pc.DeclarationIdentityConflict, "repository", "repository.ownership")
	}
	manifestSource, ok := source(app.Manifest, ApplicationContractSchema)
	if !ok {
		return p
	}
	manifest, err := ParseApplicationManifest(manifestSource.Bytes)
	if err != nil {
		fail(pc.DeclarationInvalid, "manifest", "manifest.contract")
		return p
	}
	af := facts.Artifact
	if af.Ref != manifest.Artifact.Ref || af.Digest != manifest.Artifact.Digest || af.Platform != manifest.Artifact.Platform || facts.Platform != af.Platform || !af.Reviewed || !applicationText(af.Revision) {
		fail(pc.DeclarationUnsupported, "artifact", "artifact.review_platform")
	}
	dataRefs, credentialRefs, configCause := applicationConfigurationBindings(manifest.Config, af.Configuration)
	if configCause != "" {
		fail(pc.DeclarationInvalid, "config", configCause)
		return p
	}
	if !af.ConfigurationDeliverySupported {
		fail(pc.DeclarationUnsupported, "config", "E2.artifact_configuration_delivery")
	}
	p.ExecutionPrerequisites = append(p.ExecutionPrerequisites, "E2: artifact-owned typed configuration delivery adapter", "E2: manager/HTTP health receipt adapter")
	if !facts.Systemd || facts.User != manifest.Process.User || facts.AvailableMemoryBytes < manifest.Process.MemoryMaxBytes {
		fail(pc.DeclarationUnsupported, "process", "process.manager_user_capacity")
	}
	if manifest.Listener != nil {
		if !facts.PortAvailable || facts.Port != manifest.Listener.Port || facts.ListenerAddress != manifest.Listener.Address {
			fail(pc.DeclarationTargetUnavailable, "listener", "endpoint.port_occupied")
		}
	} else if facts.Port != 0 || facts.ListenerAddress != "" || facts.PortAvailable {
		fail(pc.DeclarationInvalid, "listener", "endpoint.unrequested_listener")
	}
	if len(app.Data) != len(dataRefs) || len(manifest.Data) != len(dataRefs) || len(facts.Data) != len(dataRefs) || len(app.Data) > 16 {
		fail(pc.DeclarationInvalid, "data", "data.binding_set")
	}
	for key := range dataRefs {
		if _, ok := app.Data[key]; !ok {
			fail(pc.DeclarationReferenceMissing, "data", "data.binding_set")
		}
	}
	if len(dataRefs) > 0 {
		p.ExecutionPrerequisites = append(p.ExecutionPrerequisites, "E2: checked persistent allocation/capacity/monitor/quota adapter")
	}
	type capacityPool struct {
		available, demand uint64
		revision          string
	}
	pools := map[string]capacityPool{}
	usedSources := map[string]bool{".loom/project.yaml": true, app.Manifest: true}
	for _, key := range applicationKeys(app.Data) {
		d := app.Data[key]
		req, exists := manifest.Data[key]
		df, known := facts.Data[key]
		if !applicationKeyPattern.MatchString(string(key)) || !exists || !known || !dataRefs[key] || !scopeOK(df.Scope) || !df.Persistent || !df.CustodyChecked || !applicationAbsolute(df.Path) || !applicationText(df.CapacityPool) || !applicationText(df.PoolRevision) {
			fail(pc.DeclarationReferenceMissing, "data", "data.custody_capacity")
			continue
		}
		if (d.Path == "") == (d.BindingRef == "") {
			fail(pc.DeclarationInvalid, "data", "data.path_or_allocation")
			continue
		}
		policyPath := d.Path
		if d.Path != "" {
			if !applicationRelative(d.Path, false) || df.BindingRef != "" || df.Path != path.Join(target.ProjectRoot, d.Path) {
				fail(pc.DeclarationInvalid, "data", "data.path_scope")
			}
		} else {
			if !applicationRef(d.BindingRef) || df.BindingRef != d.BindingRef || !df.Allocated || !applicationRelative(df.ProtectionPath, false) {
				fail(pc.DeclarationUnsupported, "data", "E2.allocation_binding")
			}
			policyPath = df.ProtectionPath
		}
		if df.ExistingPath != "" && df.ExistingPath != df.Path {
			fail(pc.DeclarationIdentityConflict, "data", "data.migration_required")
		}
		if applicationTransient(df.Path) || applicationTransient(target.ProjectRoot) {
			fail(pc.DeclarationInvalid, "data", "data.transient")
		}
		// Resolved allocation paths can overlap even when neither declaration has a
		// project-relative path. Do not trust optimistic custody flags over this
		// directly observable collision in the supplied snapshot.
		for _, otherKey := range applicationKeys(facts.Data) {
			other := facts.Data[otherKey]
			if otherKey != key && (applicationAbsolute(other.Path) && applicationOverlap(df.Path, other.Path) || df.BindingRef != "" && df.BindingRef == other.BindingRef) {
				fail(pc.DeclarationInvalid, "data", "data.overlap")
			}
		}
		// Both lexical declaration overlap and supplied physical custody must pass.
		for _, otherKey := range applicationKeys(project.Resources) {
			r := project.Resources[otherKey]
			roots := []string{}
			if r.Repository != nil {
				roots = append(roots, r.Repository.Path)
			}
			if r.Knowledge != nil {
				roots = append(roots, r.Knowledge.Path)
			}
			if r.Application != nil {
				for name, other := range r.Application.Data {
					if otherKey != in.Resource || name != key {
						if other.Path != "" {
							roots = append(roots, other.Path)
						}
					}
				}
			}
			for _, root := range roots {
				if applicationOverlap(df.Path, path.Join(target.ProjectRoot, root)) {
					fail(pc.DeclarationInvalid, "data", "data.overlap")
				}
			}
		}
		if d.Capacity == nil || d.Capacity.PlannedBytes == 0 || d.Capacity.PlannedBytes > df.AvailableBytes {
			fail(pc.DeclarationTargetUnavailable, "data", "data.insufficient_capacity")
			continue
		}
		pool, seen := pools[df.CapacityPool]
		if !seen {
			pool = capacityPool{available: df.AvailableBytes, revision: df.PoolRevision}
		}
		if pool.available != df.AvailableBytes || pool.revision != df.PoolRevision {
			fail(pc.DeclarationInvalid, "data", "data.pool_snapshot")
		}
		if d.Capacity.PlannedBytes > pool.available-pool.demand {
			fail(pc.DeclarationTargetUnavailable, "data", "data.pool_capacity")
		} else {
			pool.demand += d.Capacity.PlannedBytes
		}
		pools[df.CapacityPool] = pool
		if req.QuotaBytes > 0 && (!df.QuotaSupported || req.QuotaBytes < d.Capacity.PlannedBytes || req.QuotaBytes > df.AvailableBytes) {
			fail(pc.DeclarationUnsupported, "data", "data.quota")
		}
		if req.MonitorThresholdBytes > 0 && (!df.MonitorSupported || (req.QuotaBytes > 0 && req.MonitorThresholdBytes > req.QuotaBytes)) {
			fail(pc.DeclarationUnsupported, "data", "data.monitor")
		}
		p.Data[key] = ApplicationDataPlan{Path: df.Path, BindingRef: d.BindingRef, PlannedBytes: d.Capacity.PlannedBytes, MonitorThresholdBytes: req.MonitorThresholdBytes, QuotaBytes: req.QuotaBytes, Removal: "retire_management_retain_data"}
		if d.Protection == "" {
			fail(pc.DeclarationReferenceMissing, "protection", "protection.required")
			continue
		}
		protection, ok := project.Resources[d.Protection]
		if !ok || protection.Kind != pc.DeclarationProtection || protection.Protection == nil {
			fail(pc.DeclarationReferenceMissing, "protection", "protection.reference")
			continue
		}
		pr := protection.Protection
		if pr.Path != "" && pr.Path != policyPath {
			fail(pc.DeclarationInvalid, "protection", "protection.explicit_path")
		}
		ps, ok := source(pr.PolicyRef, "backup.policy.v0.3")
		usedSources[pr.PolicyRef] = true
		if !ok {
			continue
		}
		var policy pc.ProjectBackupPolicyContract
		if applicationDecode(ps.Bytes, &policy) != nil || policy.Kind != "loom.project_backup_policy" || policy.SchemaVersion != "backup.policy.v0.3" || len(policy.Metadata) > 0 {
			fail(pc.DeclarationInvalid, "protection", "protection.source_contract")
			continue
		}
		count := 0
		var selectedRoot pc.BackupRootPolicySpec
		for _, r := range policy.Backup.Roots {
			if r.Path == policyPath {
				selectedRoot = r
				count++
			}
		}
		if count != 1 || !applicationPolicyRoot(selectedRoot) || !applicationPolicyRoot(policy.Backup.Defaults) {
			fail(pc.DeclarationInvalid, "protection", "protection.exact_root")
			continue
		}
		enabled := policy.Backup.Enabled == nil || *policy.Backup.Enabled
		p.Protection[key] = ApplicationProtectionPlan{Source: ps.Source, Enabled: enabled, Defaults: policy.Backup.Defaults, Root: selectedRoot}
	}
	// Reject surplus snapshots rather than permitting unvalidated secret-bearing
	// payloads into the plan basis; the entire accepted closure is retained.
	for ref := range in.Sources {
		if !usedSources[ref] {
			fail(pc.DeclarationInvalid, "sources", "source.unreferenced")
		}
	}
	if len(app.Credentials) != len(credentialRefs) || len(facts.Credentials) != len(credentialRefs) || len(app.Credentials) > 32 {
		fail(pc.DeclarationInvalid, "credentials", "credential.reference_set")
	}
	seenCredentials := map[string]bool{}
	for _, ref := range app.Credentials {
		if !credentialRefs[ref] || seenCredentials[ref] {
			fail(pc.DeclarationInvalid, "credentials", "credential.reference_set")
		}
		seenCredentials[ref] = true
		c, ok := facts.Credentials[ref]
		if !ok || !applicationRef(ref) || c.Ref != ref || !scopeOK(c.Scope) || !applicationKeyPattern.MatchString(c.Provider) || !c.ProviderSupported || !c.ReferenceAvailable || !c.DeliverySupported {
			fail(pc.DeclarationUnsupported, "credentials", "E2.credential_delivery")
		}
	}
	for ref := range credentialRefs {
		if !seenCredentials[ref] {
			fail(pc.DeclarationReferenceMissing, "credentials", "credential.reference_set")
		}
	}
	if len(credentialRefs) > 0 {
		p.ExecutionPrerequisites = append(p.ExecutionPrerequisites, "E2: approved runtime credential delivery adapter")
	}
	// Credential declarations are a set; retain exact source bytes but normalize
	// parsed iteration for stable per-stage order independently of map traversal.
	sort.Strings(app.Credentials)
	exposure := pc.ApplicationLoopback
	if app.Endpoint != nil {
		exposure = app.Endpoint.Exposure
	}
	switch exposure {
	case pc.ApplicationLoopback:
		if (app.Endpoint != nil && app.Endpoint.EndpointRef != "") || facts.Endpoint != nil {
			fail(pc.DeclarationInvalid, "endpoint", "endpoint.loopback_no_edge")
		}
	case pc.ApplicationPrivate, pc.ApplicationPublicHTTPS:
		p.ExecutionPrerequisites = append(p.ExecutionPrerequisites, "E2: approved application edge adapter")
		e := facts.Endpoint
		if app.Endpoint == nil || !applicationRef(app.Endpoint.EndpointRef) || e == nil || manifest.Listener == nil {
			fail(pc.DeclarationReferenceMissing, "endpoint", "E2.endpoint_binding")
			break
		}
		if !scopeOK(e.Scope) || e.Ref != app.Endpoint.EndpointRef || e.Exposure != exposure || e.Purpose != "application" || !e.Available || e.Backend != net.JoinHostPort(manifest.Listener.Address, strconv.Itoa(int(manifest.Listener.Port))) {
			fail(pc.DeclarationInvalid, "endpoint", "endpoint.scope_backend")
		}
		u, err := url.Parse(e.URL)
		if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") {
			fail(pc.DeclarationInvalid, "endpoint", "endpoint.url")
		}
		if exposure == pc.ApplicationPrivate && !e.Private {
			fail(pc.DeclarationUnauthorized, "endpoint", "endpoint.private")
		}
		if exposure == pc.ApplicationPublicHTTPS && (!e.ApprovedApplicationEdge || !applicationText(e.ApprovalRef) || u == nil || u.Scheme != "https" || !applicationHostname(u.Hostname()) || net.ParseIP(u.Hostname()) != nil || (u.Port() != "" && u.Port() != "443")) {
			fail(pc.DeclarationApprovalRequired, "endpoint", "endpoint.public_application_approval")
		}
	default:
		fail(pc.DeclarationUnsupported, "endpoint", "endpoint.exposure")
	}
	if len(p.Errors) > 0 {
		p.Data = map[pc.ResourceKey]ApplicationDataPlan{}
		p.Protection = map[pc.ResourceKey]ApplicationProtectionPlan{}
		return p
	}
	auth := pc.DeclarationAuthorization{ActorID: authority.ActorID, NodeID: authority.NodeID, Level: authority.RequiredLevel, PolicyRevision: authority.PolicyRevision, ApprovalRequired: authority.ApprovalRequired || exposure == pc.ApplicationPublicHTTPS}
	payloadHash := applicationHash(struct {
		Input      ApplicationPlanInput
		Manifest   ApplicationManifest
		Data       map[pc.ResourceKey]ApplicationDataPlan
		Protection map[pc.ResourceKey]ApplicationProtectionPlan
	}{in, manifest, p.Data, p.Protection})
	add := func(kind ApplicationStageKind, key, adapter, acceptance, rollback string) {
		deps := []string{}
		if len(p.Stages) > 0 {
			deps = append(deps, p.Stages[len(p.Stages)-1].ID)
		}
		p.Stages = append(p.Stages, ApplicationStage{ID: string(kind) + ":" + key, Kind: kind, TargetRef: target.ProjectID + "/" + string(in.Resource) + "/" + key, DependsOn: deps, Adapter: adapter, InputHash: payloadHash, Authorization: auth, Acceptance: acceptance, Rollback: rollback})
	}
	add(ApplicationArtifactStage, string(in.Resource), "E2.artifact_update", "reviewed_digest_platform_configuration_schema_receipt", "revert_executable_keep_data")
	for _, k := range applicationKeys(p.Data) {
		add(ApplicationDataStage, string(k), "E2.storage_binding", "checked_persistent_binding_capacity_monitor_quota_receipt", "retain_data_and_binding")
	}
	for _, ref := range app.Credentials {
		add(ApplicationCredentialStage, ref, "E2.credential_delivery", "reference_delivery_receipt_no_value", "revoke_runtime_delivery_keep_data")
	}
	add(ApplicationProcessStage, string(in.Resource), "E2.systemd_application", "desired_process_revision_applied_receipt", "revert_executable_config_keep_data")
	healthAcceptance := "manager_state_receipt"
	if manifest.Health.Kind == HealthKindHTTP {
		healthAcceptance = "loopback_http_expectation_receipt"
	}
	add(ApplicationHealthStage, string(in.Resource), "E2.health_observation", healthAcceptance, "no_data_rollback")
	if exposure != pc.ApplicationLoopback {
		acceptance := "exact_private_edge_client_acceptance_receipt"
		if exposure == pc.ApplicationPublicHTTPS {
			acceptance = "exact_https_edge_tls_client_acceptance_receipt"
		}
		add(ApplicationEndpointStage, string(in.Resource), "E2.application_edge", acceptance, "withdraw_application_edge_keep_data")
	}
	if len(p.Protection) > 0 {
		p.ExecutionPrerequisites = append(p.ExecutionPrerequisites, "E2: exact protection selection and verification receipt adapter")
	}
	for _, k := range applicationKeys(p.Protection) {
		accept := "effective_coverage_and_separate_verification_receipts"
		v := p.Protection[k]
		if !v.Enabled || v.Root.Mode == "none" || (v.Root.Mode == "" && v.Defaults.Mode == "none") {
			accept = "disabled_policy_preserved_not_protected"
		}
		add(ApplicationProtectionStage, string(k), "E2.projectwatch_backupcontracts", accept, "retain_payload_and_recovery_evidence")
	}
	p.Basis = &ApplicationPlanBasis{SchemaVersion: ApplicationPlanSchema, Input: in, Manifest: manifest, Data: p.Data, Protection: p.Protection, Stages: p.Stages}
	p.PlanID = applicationHash(p.Basis)
	p.Ready = true
	p.Readiness.Desired = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: p.PlanID}
	return p
}

func applicationKeys[V any](m map[pc.ResourceKey]V) []pc.ResourceKey {
	ks := make([]pc.ResourceKey, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}
func applicationTransient(v string) bool {
	for _, part := range strings.Split(v, "/") {
		switch part {
		case ".git", ".repo", ".loom", "tmp", "var-tmp", ".codex", "worktrees", ".worktrees", ".loom-acceptance":
			return true
		}
	}
	return false
}
func applicationPolicyRoot(r pc.BackupRootPolicySpec) bool {
	if wr.ValidatePatterns(r.Include) != nil || wr.ValidatePatterns(r.Exclude) != nil {
		return false
	}
	if r.Key != "" && !applicationKeyPattern.MatchString(r.Key) {
		return false
	}
	if r.SafeRoot != "" && !applicationKeyPattern.MatchString(r.SafeRoot) {
		return false
	}
	if r.Path != "" && !applicationRelative(r.Path, false) {
		return false
	}
	if r.MaxFileBytes < 0 || r.MaxBatchBytes < 0 || r.MaxPendingItems < 0 || r.MaxPendingBytes < 0 {
		return false
	}
	switch r.Mode {
	case "", wr.BackupModeNone, wr.BackupModeMetadataOnly, wr.BackupModePrivateRaw, wr.BackupModeRawSnapshot, wr.BackupModeIncrementalRaw:
	default:
		return false
	}
	switch r.OnLimit {
	case "", wr.BackupOnLimitDegradeAndRequireManualAction:
	default:
		return false
	}
	return true
}

// String excludes sources and fact content when used in diagnostics.
func (p ApplicationPlan) String() string {
	return fmt.Sprintf("application plan ready=%t executable=%t stages=%d errors=%d", p.Ready, p.Executable, len(p.Stages), len(p.Errors))
}

// Public ingress binds one concrete DNS hostname, never a wildcard or authority
// pattern. DNS resolution/TLS approval remain supplied owner facts, not probes.
func applicationHostname(v string) bool {
	if len(v) > 253 || !strings.Contains(v, ".") {
		return false
	}
	for _, label := range strings.Split(v, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
