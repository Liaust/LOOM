package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func sortEffects(x []pc.DeclarationEffect) { sort.Slice(x, func(i, j int) bool { return x[i] < x[j] }) }
func sortStrings(x []string)               { sort.Strings(x) }

func (s *Service) Apply(ctx context.Context, p Principal, r pc.DeclarationApplyRequest) (pc.DeclarationResult, error) {
	if e := s.config(true); e != nil {
		return pc.DeclarationResult{}, e
	}
	if e := validPrincipal(p); e != nil {
		return pc.DeclarationResult{}, e
	}
	if e := applyRequest(r); e != nil {
		return pc.DeclarationResult{}, e
	}
	normalized, hash, e := requestIdentity(r)
	if e != nil {
		return pc.DeclarationResult{}, e
	}
	var x Resolution
	var project string
	id := r.OperationID
	if id == "" {
		id, e = findExactRequestOperation(ctx, s.db, p, normalized, hash)
		if e != nil {
			return pc.DeclarationResult{}, e
		}
	}
	if id != "" {
		o, e := loadOperation(ctx, s.db, id)
		if e != nil {
			return pc.DeclarationResult{}, e
		}
		if e = s.read(ctx, p, o.Resolution.Plan.Basis.Target); e != nil {
			return pc.DeclarationResult{}, e
		}
		if o.Principal != p {
			return pc.DeclarationResult{}, fail(pc.DeclarationUnauthorized, "operation_principal_mismatch")
		}
		project = o.Resolution.Plan.Basis.Target.ProjectID
	} else {
		x, e = s.resolve(ctx, p, pc.DeclarationPlanRequest{SchemaVersion: r.SchemaVersion, ProjectRef: r.ProjectRef, NodeRef: r.NodeRef, Effects: r.Effects})
		if e != nil {
			return pc.DeclarationResult{}, e
		}
		project = x.Plan.Basis.Target.ProjectID
	}
	l, e := s.lock(ctx, project)
	if e != nil {
		return pc.DeclarationResult{}, e
	}
	defer l.close()
	if id == "" {
		id, e = findOperation(ctx, l.conn, p, project, r.IdempotencyKey)
		if e != nil {
			return pc.DeclarationResult{}, e
		}
	}
	var o *operation
	if id != "" {
		o, e = loadOperation(ctx, l.conn, id)
		if e != nil {
			return pc.DeclarationResult{}, e
		}
		if e = s.read(ctx, p, o.Resolution.Plan.Basis.Target); e != nil {
			return pc.DeclarationResult{}, e
		}
		if o.Principal != p {
			return pc.DeclarationResult{}, fail(pc.DeclarationUnauthorized, "operation_principal_mismatch")
		}
		if o.RequestHash != hash || !same(o.Request, normalized) {
			return pc.DeclarationResult{}, fail(pc.DeclarationOperationConflict, "idempotency_input_mismatch")
		}
		if o.Resolution.Plan.Basis.Target.ProjectID != project {
			return pc.DeclarationResult{}, fail(pc.DeclarationIdentityConflict, "target_changed")
		}
	} else {
		// Resolve once more under the project lock: an earlier concurrent executor
		// may have changed bindings while this caller waited.
		x, e = s.resolve(ctx, p, pc.DeclarationPlanRequest{SchemaVersion: r.SchemaVersion, ProjectRef: r.ProjectRef, NodeRef: r.NodeRef, Effects: r.Effects})
		if e != nil {
			return pc.DeclarationResult{}, e
		}
		if x.Plan.Basis.Target.ProjectID != project {
			return pc.DeclarationResult{}, fail(pc.DeclarationIdentityConflict, "target_changed")
		}
		if x.Plan.PlanID != r.PlanID {
			return pc.DeclarationResult{}, fail(pc.DeclarationPlanStale, "reviewed_plan_changed")
		}
		for _, a := range x.Plan.Basis.Actions {
			if e = s.executeAuth(ctx, p, x.Plan.Basis, a, r.ApprovalRefs); e != nil {
				return pc.DeclarationResult{}, e
			}
		}
		if e = s.checkCurrent(ctx, p, x, nil); e != nil {
			return pc.DeclarationResult{}, e
		}
		if e = l.Check(ctx); e != nil {
			return pc.DeclarationResult{}, e
		}
		o, e = createOperation(ctx, l, p, normalized, hash, x)
		if e != nil {
			if o != nil {
				return result(o), e
			}
			return pc.DeclarationResult{}, e
		}
	}
	return s.run(ctx, l, p, o)
}

func (s *Service) run(ctx context.Context, l *projectLock, p Principal, o *operation) (pc.DeclarationResult, error) {
	if e := safeResolution(s, ctx, p, o); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	if o.State == pc.DeclarationOperationSuperseded {
		// Current read access is still required; no superseded action is dispatched.
		if e := s.read(ctx, p, o.Resolution.Plan.Basis.Target); e != nil {
			return pc.DeclarationResult{}, e
		}
		return result(o), nil
	}
	b := o.Resolution.Plan.Basis
	// Observe every original token before checking current prerequisites. This
	// recovers the owner's commit when the coordinator lost its local receipt.
	for _, a := range b.Actions {
		if e := s.executeAuth(ctx, p, b, a, o.Request.ApprovalRefs); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if e := l.Check(ctx); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		call, e := actionCall(o, p, a, l)
		if e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		obs, e := s.owners[a.Owner].Observe(ctx, call)
		if e != nil {
			return s.stop(ctx, l, p, o, fail(pc.DeclarationOwnerFailed, "observation_uncertain"))
		}
		if e = l.Check(ctx); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if obs.State == Committed {
			if e = acceptReceipt(ctx, l, o, a, obs.Receipt); e != nil {
				return s.stop(ctx, l, p, o, e)
			}
		} else {
			if o.Actions[a.ID].Receipt != nil {
				return s.stop(ctx, l, p, o, fail(pc.DeclarationPlanStale, "committed_receipt_unavailable"))
			}
			if obs.State != Absent && obs.State != Resumable {
				return s.pending(ctx, l, p, o, a, obs)
			}
		}
	}
	if e := s.checkCurrent(ctx, p, o.Resolution, allReceipts(o)); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	if o.State == pc.DeclarationOperationSucceeded {
		if e := s.read(ctx, p, b.Target); e != nil {
			return pc.DeclarationResult{}, e
		}
		return result(o), nil
	}
	if e := saveState(ctx, l, o, pc.DeclarationOperationRunning, []pc.DeclarationError{}); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	for _, a := range b.Actions {
		if o.Actions[a.ID].Receipt != nil {
			continue
		}
		if e := s.executeAuth(ctx, p, b, a, o.Request.ApprovalRefs); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if e := s.checkCurrent(ctx, p, o.Resolution, allReceipts(o)); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if e := l.Check(ctx); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		call, e := actionCall(o, p, a, l)
		if e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		for _, d := range a.DependsOn {
			if _, ok := call.Dependencies[d]; !ok {
				return s.stop(ctx, l, p, o, fail(pc.DeclarationOperationConflict, "dependency_not_committed"))
			}
		}
		obs, e := s.owners[a.Owner].Observe(ctx, call)
		if e != nil {
			return s.pending(ctx, l, p, o, a, Observation{State: Uncertain})
		}
		if e = l.Check(ctx); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if obs.State == Committed {
			if e = acceptReceipt(ctx, l, o, a, obs.Receipt); e != nil {
				return s.stop(ctx, l, p, o, e)
			}
			continue
		}
		if obs.State != Absent && obs.State != Resumable {
			return s.pending(ctx, l, p, o, a, obs)
		}
		// Recheck after read-only callbacks; revocation between A and B cannot hide
		// behind an earlier preflight, nor can the owner observation grant authority.
		if e = s.executeAuth(ctx, p, b, a, o.Request.ApprovalRefs); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if e = s.checkCurrent(ctx, p, o.Resolution, allReceipts(o)); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		ar := *o.Actions[a.ID]
		ar.State = pc.DeclarationOperationRunning
		ar.Error = nil
		if e = saveAction(ctx, l, o, a, &ar); e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		obs, applyErr := s.owners[a.Owner].Apply(ctx, call)
		// The caller may cancel after the owner commits. A bounded journal-only
		// recovery context preserves returned durable evidence; it never applies.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		fenceErr := l.Check(recovery)
		if obs.State == Committed {
			e = acceptReceipt(recovery, l, o, a, obs.Receipt)
		} else {
			e = fenceErr
		}
		cancel()
		if e != nil {
			return s.stop(ctx, l, p, o, e)
		}
		if ctx.Err() != nil {
			return s.stop(ctx, l, p, o, fail(pc.DeclarationOwnerFailed, "execution_cancelled"))
		}
		if fenceErr != nil {
			return s.stop(ctx, l, p, o, fenceErr)
		}
		if obs.State == Committed {
			continue
		}
		if applyErr != nil {
			// Errors do not prove non-delivery. Observe; only an explicit absent result
			// allows a later same-token attempt. Never automatically repeat here.
			if e = l.Check(ctx); e != nil {
				return s.stop(ctx, l, p, o, e)
			}
			obs, e = s.owners[a.Owner].Observe(ctx, call)
			if e != nil {
				obs = Observation{State: Uncertain}
			}
			if obs.State == Committed {
				if e = acceptReceipt(ctx, l, o, a, obs.Receipt); e != nil {
					return s.stop(ctx, l, p, o, e)
				}
				continue
			}
			if obs.State == Absent {
				return s.actionFailed(ctx, l, p, o, a, safeError(applyErr, pc.DeclarationOwnerFailed, "owner_apply_failed"))
			}
		}
		if obs.State == Absent {
			return s.actionFailed(ctx, l, p, o, a, fail(pc.DeclarationOwnerFailed, "owner_not_committed"))
		}
		return s.pending(ctx, l, p, o, a, obs)
	}
	if e := s.read(ctx, p, b.Target); e != nil {
		return pc.DeclarationResult{}, e
	}
	if e := s.checkCurrent(ctx, p, o.Resolution, allReceipts(o)); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	if e := saveState(ctx, l, o, pc.DeclarationOperationSucceeded, []pc.DeclarationError{}); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	if e := s.read(ctx, p, b.Target); e != nil {
		return pc.DeclarationResult{}, e
	}
	return result(o), nil
}

func safeResolution(s *Service, ctx context.Context, p Principal, o *operation) error {
	if e := s.read(ctx, p, o.Resolution.Plan.Basis.Target); e != nil {
		return e
	}
	// Validate a detached copy so a resolver/owner cannot rewrite stored identity.
	x := cloneResolution(o.Resolution)
	if e := s.validate(ctx, p, &x, o.Request.Effects); e != nil {
		return e
	}
	if x.Plan.PlanID != o.Request.PlanID {
		return fail(pc.DeclarationInvalid, "journal_plan_mismatch")
	}
	for _, a := range x.Plan.Basis.Actions {
		ar := o.Actions[a.ID]
		if ar == nil || ar.Token != stableToken(x, a) {
			return fail(pc.DeclarationInvalid, "journal_action_mismatch")
		}
	}
	return nil
}
func cloneResolution(x Resolution) Resolution {
	raw, _ := canonicalValue(x)
	var y Resolution
	_ = jsonDecode(raw, &y)
	return y
}
func jsonDecode(raw []byte, v any) error { return json.Unmarshal(raw, v) }

func actionCall(o *operation, p Principal, a pc.DeclarationAction, l Fence) (ActionCall, error) {
	expected, e := expectedState(o.Resolution, allReceipts(o))
	if e != nil {
		return ActionCall{}, e
	}
	dependencies := map[string]Receipt{}
	for _, d := range a.DependsOn {
		if ar := o.Actions[d]; ar != nil && ar.Receipt != nil {
			dependencies[d] = *ar.Receipt
		}
	}
	return ActionCall{Principal: p, OperationID: o.ID, PlanID: o.Resolution.Plan.PlanID, Target: o.Resolution.Plan.Basis.Target, Action: a, Payload: append([]byte{}, o.Resolution.Payloads[a.ID]...), Token: o.Actions[a.ID].Token, Expected: expected, Dependencies: dependencies, Fence: l}, nil
}
func allReceipts(o *operation) []Receipt {
	out := []Receipt{}
	for _, a := range o.Resolution.Plan.Basis.Actions {
		if ar := o.Actions[a.ID]; ar != nil && ar.Receipt != nil {
			out = append(out, *ar.Receipt)
		}
	}
	return out
}
func expectedState(x Resolution, receipts []Receipt) (Prerequisites, error) {
	// Copy through exact JSON, retaining raw payload numbers elsewhere.
	raw, _ := canonicalValue(prerequisites(x.Plan.Basis))
	var p Prerequisites
	_ = jsonDecode(raw, &p)
	actions := map[string]pc.DeclarationAction{}
	for _, a := range x.Plan.Basis.Actions {
		actions[a.ID] = a
	}
	done := map[string]bool{}
	for _, r := range receipts {
		a, ok := actions[r.ActionID]
		if !ok || done[a.ID] || !validReceiptIdentity(x, a, r) {
			return p, fail(pc.DeclarationPlanStale, "invalid_owner_receipt")
		}
		for _, dep := range a.DependsOn {
			if !done[dep] {
				return p, fail(pc.DeclarationPlanStale, "receipt_dependency_missing")
			}
		}
		for k, c := range r.Revisions {
			if !strings.HasPrefix(k, string(a.Owner)+":") || !nonempty(c.After) || c.Before == c.After || p.Revisions[k] == "" || p.Revisions[k] != c.Before {
				return p, fail(pc.DeclarationPlanStale, "receipt_revision_mismatch")
			}
			p.Revisions[k] = c.After
		}
		for k, c := range r.Bindings {
			before, ok := p.Bindings[k]
			if !ok || !receiptBindingAllowed(x, a, k, before) || !same(before, c.Before) || !validBinding(c.After) || c.Before.Kind != c.After.Kind {
				return p, fail(pc.DeclarationPlanStale, "receipt_binding_mismatch")
			}
			// Retained IDs never change; retirement retains the binding as evidence.
			if c.Before.RepositoryID != "" && c.After.RepositoryID != c.Before.RepositoryID || c.Before.OwnerRef != "" && c.After.OwnerRef != c.Before.OwnerRef {
				return p, fail(pc.DeclarationIdentityConflict, "receipt_rebind_refused")
			}
			if a.Kind == pc.DeclarationRetireResource && !same(c.Before, c.After) {
				return p, fail(pc.DeclarationIdentityConflict, "retirement_binding_changed")
			}
			p.Bindings[k] = c.After
		}
		done[a.ID] = true
	}
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].Ref < p.Sources[j].Ref })
	return p, nil
}
func validReceiptIdentity(x Resolution, a pc.DeclarationAction, r Receipt) bool {
	return r.ActionID == a.ID && r.Owner == a.Owner && r.InputHash == a.InputHash && r.Token == stableToken(x, a) && nonempty(r.EffectRef) && len(r.EffectRef) <= 512 && r.Revisions != nil && r.Bindings != nil
}
func acceptReceipt(ctx context.Context, l *projectLock, o *operation, a pc.DeclarationAction, r *Receipt) error {
	if r == nil || !validReceiptIdentity(o.Resolution, a, *r) {
		return fail(pc.DeclarationPlanStale, "invalid_owner_receipt")
	}
	old := o.Actions[a.ID]
	if old.Receipt != nil {
		if !same(old.Receipt, r) {
			return fail(pc.DeclarationPlanStale, "owner_receipt_changed")
		}
		return nil
	}
	receipts := allReceipts(o)
	receipts = append(receipts, *r)
	// Reorder by original dependency order, then validate complete before/after.
	byID := map[string]Receipt{}
	for _, r := range receipts {
		byID[r.ActionID] = r
	}
	ordered := []Receipt{}
	for _, a := range o.Resolution.Plan.Basis.Actions {
		if r, ok := byID[a.ID]; ok {
			ordered = append(ordered, r)
		}
	}
	if _, e := expectedState(o.Resolution, ordered); e != nil {
		return e
	}
	ar := &actionRecord{State: pc.DeclarationOperationSucceeded, Token: old.Token, Receipt: r}
	err := saveAction(ctx, l, o, a, ar)
	if err != nil {
		// A validated owner receipt remains known durable owner truth even when the
		// coordinator connection is lost. Return it without pretending the local
		// journal saved it. Resume will observe and persist this same token.
		o.Actions[a.ID] = ar
	}
	return err
}
func (s *Service) checkCurrent(ctx context.Context, p Principal, x Resolution, receipts []Receipt) error {
	expected, e := expectedState(x, receipts)
	if e != nil {
		return e
	}
	current, e := s.resolver.Current(ctx, p, cloneResolution(x))
	if e != nil {
		return safeError(e, pc.DeclarationTargetUnavailable, "prerequisites_unavailable")
	}
	if validPrerequisites(current) != nil {
		return fail(pc.DeclarationPlanStale, "prerequisites_invalid")
	}
	current.Sources = append([]pc.DeclarationSource{}, current.Sources...)
	sort.Slice(current.Sources, func(i, j int) bool { return current.Sources[i].Ref < current.Sources[j].Ref })
	if !same(current, expected) {
		return fail(pc.DeclarationPlanStale, "prerequisites_changed")
	}
	return nil
}
func publicError(err error, o *operation, action string) pc.DeclarationError {
	e := safeError(err, pc.DeclarationOwnerFailed, "execution_failed").(*Failure)
	done := []string{}
	if o != nil {
		for _, a := range o.Resolution.Plan.Basis.Actions {
			if r := o.Actions[a.ID]; r != nil && r.Receipt != nil {
				done = append(done, r.Receipt.EffectRef)
			}
		}
	}
	return pc.DeclarationError{Code: e.Code, ActionID: action, Message: "Declaration operation stopped; inspect the typed cause and retained effects.", CauseCode: e.Cause, Retryable: e.Code == pc.DeclarationOwnerFailed || e.Code == pc.DeclarationTargetUnavailable, CompletedEffects: done}
}
func result(o *operation) pc.DeclarationResult {
	p := o.Resolution.Plan
	r := pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: o.ID, PlanID: p.PlanID, Target: p.Basis.Target, Revision: p.PlanID, State: o.State, Actions: []pc.DeclarationActionResult{}, Readiness: readiness(p.PlanID), Errors: append([]pc.DeclarationError{}, o.Errors...), SupersededBy: o.SupersededBy}
	r.Readiness.Queued = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: p.PlanID, EvidenceRef: o.ID}
	for _, a := range p.Basis.Actions {
		ar := o.Actions[a.ID]
		if ar == nil {
			continue
		}
		x := pc.DeclarationActionResult{ActionID: a.ID, State: ar.State, Error: ar.Error}
		if ar.Receipt != nil {
			x.EffectRef = ar.Receipt.EffectRef
		}
		r.Actions = append(r.Actions, x)
	}
	if o.State == pc.DeclarationOperationSucceeded {
		r.Readiness.Applied = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: p.PlanID, EvidenceRef: o.ID}
	} else {
		r.Readiness.Applied.State = pc.DeclarationPending
	}
	return r
}
func (s *Service) stop(ctx context.Context, l *projectLock, p Principal, o *operation, err error) (pc.DeclarationResult, error) {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	// Never return historical result data to a principal that no longer has read.
	if e := s.read(recovery, p, o.Resolution.Plan.Basis.Target); e != nil {
		return pc.DeclarationResult{}, e
	}
	issue := publicError(err, o, "")
	state := pc.DeclarationOperationPartial
	if o.State != pc.DeclarationOperationSucceeded && o.State != pc.DeclarationOperationSuperseded {
		_ = saveState(recovery, l, o, state, []pc.DeclarationError{issue})
	}
	// A newer executor may have superseded us after connection loss. Read its
	// durable terminal state without trying to repair or overwrite the journal.
	if current, e := loadOperation(recovery, s.db, o.ID); e == nil && current.State == pc.DeclarationOperationSuperseded {
		o.State = current.State
		o.SupersededBy = current.SupersededBy
	}
	r := result(o)
	if r.State != pc.DeclarationOperationSucceeded && r.State != pc.DeclarationOperationSuperseded {
		r.State = pc.DeclarationOperationPartial
	}
	r.Errors = []pc.DeclarationError{issue}
	r.Readiness.Applied.State = pc.DeclarationUnknown
	return r, err
}
func (s *Service) actionFailed(ctx context.Context, l *projectLock, p Principal, o *operation, a pc.DeclarationAction, err error) (pc.DeclarationResult, error) {
	ar := *o.Actions[a.ID]
	ar.State = pc.DeclarationOperationFailed
	issue := publicError(err, o, a.ID)
	ar.Error = &issue
	if e := saveAction(ctx, l, o, a, &ar); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	return s.stop(ctx, l, p, o, err)
}
func (s *Service) pending(ctx context.Context, l *projectLock, p Principal, o *operation, a pc.DeclarationAction, obs Observation) (pc.DeclarationResult, error) {
	if e := s.checkCurrent(ctx, p, o.Resolution, allReceipts(o)); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	cause := "owner_outcome_uncertain"
	if obs.State == Pending {
		cause = "owner_pending"
	}
	// CauseCode is an explicitly supplied stable owner code, not an error string.
	// Only an owner-qualified lexical code can augment the core outcome cause.
	if causePattern.MatchString(obs.CauseCode) && (strings.HasPrefix(obs.CauseCode, string(a.Owner)+".") || (a.Owner == pc.DeclarationOwnerApplication && strings.HasPrefix(obs.CauseCode, "application."))) {
		cause = obs.CauseCode
	}
	ar := *o.Actions[a.ID]
	ar.State = pc.DeclarationOperationPartial
	err := fail(pc.DeclarationOwnerFailed, cause)
	issue := publicError(err, o, a.ID)
	ar.Error = &issue
	if e := saveAction(ctx, l, o, a, &ar); e != nil {
		return s.stop(ctx, l, p, o, e)
	}
	return s.stop(ctx, l, p, o, err)
}

// Operation is a read-only current-access view of historical receipts. It never
// observes/applies owners or repairs the journal; Apply with OperationID resumes.
func (s *Service) Operation(ctx context.Context, p Principal, id string) (pc.DeclarationResult, error) {
	if e := s.config(true); e != nil {
		return pc.DeclarationResult{}, e
	}
	if e := validPrincipal(p); e != nil {
		return pc.DeclarationResult{}, e
	}
	if ids.Validate(ids.JobPrefix, id) != nil {
		return pc.DeclarationResult{}, fail(pc.DeclarationInvalid, "invalid_operation_id")
	}
	o, e := loadOperation(ctx, s.db, id)
	if e != nil {
		return pc.DeclarationResult{}, e
	}
	if e = s.read(ctx, p, o.Resolution.Plan.Basis.Target); e != nil {
		return pc.DeclarationResult{}, e
	}
	r := result(o)
	if e = s.checkCurrent(ctx, p, o.Resolution, allReceipts(o)); e != nil {
		r.Readiness.Applied.State = pc.DeclarationUnknown
		r.Errors = append(r.Errors, publicError(e, o, ""))
	}
	if e := s.read(ctx, p, o.Resolution.Plan.Basis.Target); e != nil {
		return pc.DeclarationResult{}, e
	}
	return r, nil
}
func (s *Service) Status(ctx context.Context, p Principal, req pc.DeclarationPlanRequest) (pc.DeclarationStatus, error) {
	x, e := s.resolve(ctx, p, req)
	if e != nil {
		return pc.DeclarationStatus{}, e
	}
	if s.db == nil {
		return pc.DeclarationStatus{}, fail(pc.DeclarationUnsupported, "core_not_configured")
	}
	status := pc.DeclarationStatus{SchemaVersion: pc.DeclarationStatusSchemaV05, Target: x.Plan.Basis.Target, Revision: x.Plan.PlanID, Resources: map[pc.ResourceKey]pc.DeclarationResourceStatus{}, Readiness: readiness(x.Plan.PlanID), Errors: []pc.DeclarationError{}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for key, b := range x.Plan.Basis.Bindings {
		status.Resources[key] = pc.DeclarationResourceStatus{Binding: b, Readiness: readiness(x.Plan.PlanID), Errors: []pc.DeclarationError{}}
	}
	for _, action := range x.Plan.Basis.Actions {
		reader, ok := s.owners[action.Owner].(interface {
			ResourceStatus(context.Context, pc.DeclarationAction, json.RawMessage) (pc.DeclarationReadiness, error)
		})
		if !ok {
			continue
		}
		resource := status.Resources[action.Resource]
		observed, err := reader.ResourceStatus(ctx, action, x.Payloads[action.ID])
		if err != nil {
			resource.Errors = append(resource.Errors, publicError(err, nil, action.ID))
		} else {
			resource.Readiness = observed
		}
		status.Resources[action.Resource] = resource
	}
	var id string
	e = s.db.QueryRowContext(ctx, `SELECT operation_id FROM projects.declaration_operations WHERE project_id=$1 ORDER BY created_at DESC,operation_id DESC LIMIT 1`, status.Target.ProjectID).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return status, nil
	}
	if e != nil {
		return pc.DeclarationStatus{}, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	op, e := s.Operation(ctx, p, id)
	if e != nil {
		return pc.DeclarationStatus{}, e
	}
	status.Operation = &op
	return status, nil
}

// desiredSupersession distinguishes reviewed desired work from execution
// metadata. Supersession replaces an unfinished owner/resource action, never
// just an actor, selected effect, display field or unrelated owner fact.
func desiredSupersession(old *operation, next Resolution) (overlap, changed bool, err error) {
	expected, err := expectedState(old.Resolution, allReceipts(old))
	if err != nil {
		return false, false, err
	}
	current := prerequisites(next.Plan.Basis)
	current.Sources = append([]pc.DeclarationSource{}, current.Sources...)
	sort.Slice(current.Sources, func(i, j int) bool { return current.Sources[i].Ref < current.Sources[j].Ref })
	// Sources are the complete reviewed closure: a changed source conservatively
	// invalidates the desired revision, but only for overlapping pending work.
	sourceChanged := !same(expected.Sources, current.Sources)
	targetChanged := !same(expected.Target, current.Target)
	equivalentOverlap := false
	for _, prior := range old.Resolution.Plan.Basis.Actions {
		if record := old.Actions[prior.ID]; record != nil && record.Receipt != nil {
			continue
		}
		for _, action := range next.Plan.Basis.Actions {
			if prior.Owner != action.Owner || prior.TargetRef != action.TargetRef {
				continue
			}
			overlap = true
			if sourceChanged || targetChanged || prior.Kind != action.Kind || prior.InputHash != action.InputHash || !same(prior.DependsOn, action.DependsOn) {
				changed = true
				continue
			}
			equivalentOverlap = true
			// Binding/revision drift with unchanged desired input is not by itself a
			// newer desired declaration. In particular, a missing local receipt could
			// hide the old action's already-committed binding. Do not use that absence
			// to grant another dispatch token; the original operation must recover it.
			if !same(expected.Bindings[prior.Resource], current.Bindings[action.Resource]) {
				return true, false, fail(pc.DeclarationOperationConflict, "unfinished_binding_unresolved")
			}
		}
	}
	// A changed action cannot grant a second token to a different overlapping
	// action whose desired work is unchanged and outcome is still unresolved.
	return overlap, changed && !equivalentOverlap, nil
}

// Grouped registration may allocate only repository-kind keys in both the frozen
// basis and its complete typed registration payload. Existing IDs still cannot
// change; other owners retain the original single-resource receipt rule.
func receiptBindingAllowed(x Resolution, a pc.DeclarationAction, key pc.ResourceKey, before pc.DeclarationBinding) bool {
	if key == a.Resource {
		return true
	}
	if a.Owner != pc.DeclarationOwnerProjects || a.Kind != pc.DeclarationRegisterProject || before.Kind != pc.DeclarationRepository {
		return false
	}
	var payload projects.DeclarationRegistrationPayload
	if err := strictOwnerPayload(x.Payloads[a.ID], &payload); err != nil || payload.Project.ID != x.Plan.Basis.Target.ProjectID {
		return false
	}
	for _, m := range payload.Repositories {
		if m.Key == string(key) {
			return m.RepositoryID == "" || m.RepositoryID == before.RepositoryID
		}
	}
	return false
}
