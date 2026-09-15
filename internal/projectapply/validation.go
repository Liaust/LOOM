package projectapply

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var causePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,79}$`)

func fail(code pc.DeclarationErrorCode, cause string) error {
	return &Failure{Code: code, Cause: cause}
}
func safeError(err error, code pc.DeclarationErrorCode, cause string) error {
	var f *Failure
	if errors.As(err, &f) && knownCode(f.Code) && causePattern.MatchString(f.Cause) {
		return &Failure{Code: f.Code, Cause: f.Cause, Preflight: f.PublicPreflight()}
	}
	return fail(code, cause)
}
func knownCode(c pc.DeclarationErrorCode) bool {
	switch c {
	case pc.DeclarationInvalid, pc.DeclarationUnsupported, pc.DeclarationIdentityConflict, pc.DeclarationReferenceMissing, pc.DeclarationTargetUnavailable, pc.DeclarationPlanStale, pc.DeclarationUnauthorized, pc.DeclarationApprovalRequired, pc.DeclarationOperationConflict, pc.DeclarationOwnerFailed:
		return true
	}
	return false
}
func nonempty(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}
func nilInterface(v any) bool {
	if v == nil {
		return true
	}
	x := reflect.ValueOf(v)
	switch x.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Func, reflect.Interface, reflect.Slice:
		return x.IsNil()
	}
	return false
}
func (s *Service) config(db bool) error {
	if s == nil || nilInterface(s.resolver) || nilInterface(s.auth) || (db && s.db == nil) {
		return fail(pc.DeclarationUnsupported, "core_not_configured")
	}
	return nil
}
func validPrincipal(p Principal) error {
	if ids.Validate(ids.ActorPrefix, p.ActorID) != nil || ids.Validate(ids.NodePrefix, p.OriginNodeID) != nil {
		return fail(pc.DeclarationUnauthorized, "invalid_principal")
	}
	return nil
}
func effects(xs []pc.DeclarationEffect) error {
	if len(xs) == 0 {
		return fail(pc.DeclarationInvalid, "effects_required")
	}
	seen := map[pc.DeclarationEffect]bool{}
	for _, x := range xs {
		if (x != pc.DeclarationReconcile && x != pc.DeclarationProjections) || seen[x] {
			return fail(pc.DeclarationInvalid, "invalid_effects")
		}
		seen[x] = true
	}
	return nil
}
func planRequest(r pc.DeclarationPlanRequest) (pc.DeclarationPlanRequest, error) {
	if r.SchemaVersion != pc.DeclarationRequestSchemaV05 || !nonempty(r.ProjectRef) || (r.NodeRef != "" && !nonempty(r.NodeRef)) {
		return r, fail(pc.DeclarationInvalid, "invalid_selector")
	}
	if r.Effects == nil {
		r.Effects = []pc.DeclarationEffect{pc.DeclarationReconcile}
	}
	return r, effects(r.Effects)
}
func applyRequest(r pc.DeclarationApplyRequest) error {
	_, e := planRequest(pc.DeclarationPlanRequest{SchemaVersion: r.SchemaVersion, ProjectRef: r.ProjectRef, NodeRef: r.NodeRef, Effects: r.Effects})
	if e != nil {
		return e
	}
	if e = effects(r.Effects); e != nil {
		return e
	}
	if !digestPattern.MatchString(r.PlanID) || !nonempty(r.IdempotencyKey) || len(r.IdempotencyKey) > 256 || (r.OperationID != "" && ids.Validate(ids.JobPrefix, r.OperationID) != nil) {
		return fail(pc.DeclarationInvalid, "invalid_operation_request")
	}
	seen := map[string]bool{}
	for _, a := range r.ApprovalRefs {
		if !nonempty(a) || seen[a] {
			return fail(pc.DeclarationInvalid, "invalid_approval")
		}
		seen[a] = true
	}
	return nil
}
func validTarget(t pc.DeclarationTarget) error {
	if ids.Validate(ids.ProjectPrefix, t.ProjectID) != nil || ids.Validate(ids.NodePrefix, t.OwnerNodeID) != nil || !nonempty(t.LocationRevision) || !nonempty(t.ProjectRoot) || !path.IsAbs(t.ProjectRoot) || path.Clean(t.ProjectRoot) != t.ProjectRoot || t.ProjectRoot == "/" || strings.Contains(t.ProjectRoot, "\\") {
		return fail(pc.DeclarationInvalid, "invalid_target")
	}
	return nil
}
func selectorTarget(r pc.DeclarationPlanRequest, t pc.DeclarationTarget) error {
	// Friendly selectors are resolved by Resolver. Explicit identities/paths must
	// additionally agree with its output; never fall back to another target.
	if (strings.HasPrefix(r.ProjectRef, "project_") && r.ProjectRef != t.ProjectID) || (path.IsAbs(r.ProjectRef) && r.ProjectRef != t.ProjectRoot) || (strings.HasPrefix(r.NodeRef, "node_") && r.NodeRef != t.OwnerNodeID) {
		return fail(pc.DeclarationIdentityConflict, "selector_target_mismatch")
	}
	return validTarget(t)
}
func validBinding(b pc.DeclarationBinding) bool {
	switch b.Kind {
	case pc.DeclarationRepository:
		if b.RepositoryID != "" && ids.Validate("repo", b.RepositoryID) != nil {
			return false
		}
	case pc.DeclarationKnowledge, pc.DeclarationProtection, pc.DeclarationApplication:
		if b.RepositoryID != "" {
			return false
		}
	default:
		return false
	}
	return b.OwnerRef == "" || nonempty(b.OwnerRef)
}
func validPrerequisites(p Prerequisites) error {
	if e := validTarget(p.Target); e != nil {
		return e
	}
	if p.Bindings == nil || len(p.Sources) == 0 || len(p.Revisions) == 0 {
		return fail(pc.DeclarationInvalid, "incomplete_prerequisites")
	}
	seen := map[string]bool{}
	root := false
	for _, s := range p.Sources {
		if !nonempty(s.Ref) || !nonempty(s.Revision) || !nonempty(s.SchemaVersion) || !digestPattern.MatchString(s.Hash) || seen[s.Ref] {
			return fail(pc.DeclarationInvalid, "invalid_source")
		}
		seen[s.Ref] = true
		root = root || (s.Ref == pc.CanonicalRootContractPath && s.SchemaVersion == pc.ProjectSchemaV05)
	}
	if !root {
		return fail(pc.DeclarationInvalid, "root_source_missing")
	}
	repoIDs := map[string]bool{}
	for k, b := range p.Bindings {
		if !keyPattern.MatchString(string(k)) || !validBinding(b) || b.RepositoryID != "" && repoIDs[b.RepositoryID] {
			return fail(pc.DeclarationInvalid, "invalid_binding")
		}
		if b.RepositoryID != "" {
			repoIDs[b.RepositoryID] = true
		}
	}
	for k, v := range p.Revisions {
		if !nonempty(k) || !strings.Contains(k, ":") || !nonempty(v) {
			return fail(pc.DeclarationInvalid, "invalid_revision")
		}
	}
	// These fences apply even to an empty declaration and must encode explicit
	// absence when no project registration exists yet.
	for _, k := range []string{"projects:registry", "projects:lifecycle"} {
		if p.Revisions[k] == "" {
			return fail(pc.DeclarationInvalid, "missing_project_fence")
		}
	}
	return nil
}
func actionOwner(a pc.DeclarationAction) bool {
	switch a.Kind {
	case pc.DeclarationRegisterProject, pc.DeclarationRegisterRepository:
		return a.Owner == pc.DeclarationOwnerProjects
	case pc.DeclarationEnrollKnowledge:
		return a.Owner == pc.DeclarationOwnerKnowledge
	case pc.DeclarationReconcileProtection:
		return a.Owner == pc.DeclarationOwnerProtection
	case pc.DeclarationApplyApplication:
		return a.Owner == pc.DeclarationOwnerApplication
	case pc.DeclarationRefreshProjection:
		return a.Owner == pc.DeclarationOwnerNotes || a.Owner == pc.DeclarationOwnerProvenance
	case pc.DeclarationRetireResource:
		return a.Owner == pc.DeclarationOwnerProjects || a.Owner == pc.DeclarationOwnerKnowledge || a.Owner == pc.DeclarationOwnerProtection || a.Owner == pc.DeclarationOwnerApplication
	}
	return false
}
func (s *Service) validate(ctx context.Context, p Principal, x *Resolution, requested []pc.DeclarationEffect) error {
	b := x.Plan.Basis
	if x.Plan.SchemaVersion != pc.DeclarationPlanSchemaV05 || b.SchemaVersion != pc.DeclarationPlanSchemaV05 || b.Actions == nil || x.Payloads == nil || len(x.Plan.Errors) > 0 {
		return fail(pc.DeclarationInvalid, "invalid_plan")
	}
	if e := validPrerequisites(prerequisites(b)); e != nil {
		return e
	}
	if e := effects(b.Effects); e != nil {
		return e
	}
	got := append([]pc.DeclarationEffect{}, b.Effects...)
	want := append([]pc.DeclarationEffect{}, requested...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !same(got, want) {
		return fail(pc.DeclarationInvalid, "effects_mismatch")
	}
	authKey := "authorization:" + p.ActorID + ":" + b.Target.OwnerNodeID
	if b.Revisions[authKey] == "" {
		return fail(pc.DeclarationInvalid, "missing_authorization_fence")
	}
	if len(x.Payloads) != len(b.Actions) {
		return fail(pc.DeclarationInvalid, "payload_count")
	}
	payloads := make(map[string]json.RawMessage, len(x.Payloads))
	for _, a := range b.Actions {
		if !actionOwner(a) {
			return fail(pc.DeclarationUnsupported, "unknown_action_owner")
		}
		owner := s.owners[a.Owner]
		if nilInterface(owner) {
			return fail(pc.DeclarationUnsupported, "owner_not_configured")
		}
		id := string(a.Kind) + ":" + string(a.Resource)
		target := b.Target.ProjectID + "/" + string(a.Resource)
		if a.Kind == pc.DeclarationRegisterProject {
			if a.Resource != "" {
				return fail(pc.DeclarationInvalid, "project_resource")
			}
			id = "register_project:project"
			target = b.Target.ProjectID
		} else {
			binding, ok := b.Bindings[a.Resource]
			if !ok {
				return fail(pc.DeclarationInvalid, "action_binding_missing")
			}
			expected := map[pc.DeclarationActionKind]pc.DeclarationResourceKind{pc.DeclarationRegisterRepository: pc.DeclarationRepository, pc.DeclarationEnrollKnowledge: pc.DeclarationKnowledge, pc.DeclarationReconcileProtection: pc.DeclarationProtection, pc.DeclarationApplyApplication: pc.DeclarationApplication}
			if kind, ok := expected[a.Kind]; ok && kind != binding.Kind {
				return fail(pc.DeclarationInvalid, "action_binding_kind")
			}
			if a.Kind == pc.DeclarationRetireResource {
				owners := map[pc.DeclarationResourceKind]pc.DeclarationOwner{pc.DeclarationRepository: pc.DeclarationOwnerProjects, pc.DeclarationKnowledge: pc.DeclarationOwnerKnowledge, pc.DeclarationProtection: pc.DeclarationOwnerProtection, pc.DeclarationApplication: pc.DeclarationOwnerApplication}
				if owners[binding.Kind] != a.Owner {
					return fail(pc.DeclarationInvalid, "retirement_owner")
				}
			}
		}
		effect := pc.DeclarationReconcile
		if a.Kind == pc.DeclarationRefreshProjection {
			effect = pc.DeclarationProjections
		}
		found := false
		for _, e := range b.Effects {
			found = found || e == effect
		}
		if !found {
			return fail(pc.DeclarationInvalid, "action_effect_mismatch")
		}
		if a.Kind == pc.DeclarationRefreshProjection || a.Kind == pc.DeclarationRetireResource {
			id += ":" + string(a.Owner)
		}
		if a.ID != id || a.TargetRef != target || a.DependsOn == nil || !digestPattern.MatchString(a.InputHash) {
			return fail(pc.DeclarationInvalid, "invalid_action")
		}
		if a.Authorization.ActorID != p.ActorID || a.Authorization.NodeID != b.Target.OwnerNodeID || a.Authorization.Level < 1 || a.Authorization.Level > 5 || a.Authorization.PolicyRevision != b.Revisions[authKey] {
			return fail(pc.DeclarationUnauthorized, "authorization_snapshot_mismatch")
		}
		raw, e := CanonicalJSON(x.Payloads[a.ID])
		if e != nil {
			return e
		}
		if len(raw) < 2 || raw[0] != '{' || hashBytes(raw) != a.InputHash {
			return fail(pc.DeclarationInvalid, "payload_hash_mismatch")
		}
		if e = owner.Validate(ctx, a, raw); e != nil {
			return safeError(e, pc.DeclarationInvalid, "owner_input_invalid")
		}
		payloads[a.ID] = raw
	}
	var e error
	b, e = orderedBasis(b)
	if e != nil {
		return e
	}
	id, e := PlanID(b)
	if e != nil {
		return e
	}
	if x.Plan.PlanID != "" && x.Plan.PlanID != id {
		return fail(pc.DeclarationPlanStale, "resolver_hash_mismatch")
	}
	x.Plan.Basis = b
	x.Plan.PlanID = id
	x.Payloads = payloads
	return nil
}
func (s *Service) read(ctx context.Context, p Principal, t pc.DeclarationTarget) error {
	if e := validPrincipal(p); e != nil {
		return e
	}
	if e := validTarget(t); e != nil {
		return e
	}
	if e := s.auth.Read(ctx, p, t); e != nil {
		return safeError(e, pc.DeclarationUnauthorized, "read_denied")
	}
	return nil
}
func (s *Service) executeAuth(ctx context.Context, p Principal, b pc.DeclarationPlanBasis, a pc.DeclarationAction, refs []string) error {
	if e := s.read(ctx, p, b.Target); e != nil {
		return e
	}
	if a.Authorization.ActorID != p.ActorID || a.Authorization.NodeID != b.Target.OwnerNodeID {
		return fail(pc.DeclarationUnauthorized, "actor_changed")
	}
	if a.Authorization.ApprovalRequired && len(refs) == 0 {
		return fail(pc.DeclarationApprovalRequired, "approval_missing")
	}
	if e := s.auth.Execute(ctx, p, b, a, refs); e != nil {
		return safeError(e, pc.DeclarationUnauthorized, "execute_denied")
	}
	return nil
}
