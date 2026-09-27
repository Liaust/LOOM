package projectapply

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"loom.local/loom/internal/automation"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/routing"
)

type scheduleDeclarationMarker struct {
	Resource    pc.ResourceKey `json:"resource"`
	DesiredHash string         `json:"desired_hash"`
	Status      string         `json:"status"`
	Retired     bool           `json:"retired"`
}
type declarationScheduleState struct {
	Schedule automation.Schedule
	Marker   scheduleDeclarationMarker
	Revision string
}
type scheduleOwnerPayload struct {
	Project  pc.ProjectSpec          `json:"project"`
	Resource pc.ResourceKey          `json:"resource"`
	Schedule *pc.ScheduleDeclaration `json:"schedule,omitempty"`
	Key      string                  `json:"key"`
	Retire   bool                    `json:"retire"`
}

func scheduleState(s automation.Schedule) (declarationScheduleState, error) {
	var metadata struct {
		Declaration scheduleDeclarationMarker `json:"project_declaration"`
	}
	if err := json.Unmarshal(s.MetadataJSON, &metadata); err != nil || !keyPattern.MatchString(string(metadata.Declaration.Resource)) || !digestPattern.MatchString(metadata.Declaration.DesiredHash) {
		return declarationScheduleState{}, fail(pc.DeclarationIdentityConflict, "schedule_owner_marker_invalid")
	}
	// Only configuration affects reviewed identity. Normal fires change next/last
	// times and complete one-shots without invalidating their source declaration.
	status := s.Status
	if status == automation.ScheduleStatusCompleted {
		status = automation.ScheduleStatusActive
	}
	raw, err := canonicalValue(map[string]any{
		"id": s.ScheduleID, "key": s.ScheduleKey, "project": s.ProjectID, "scope": s.ScopeID,
		"actor": s.RunAsActorID, "kind": s.ScheduleKind, "expr": s.ScheduleExpr, "timezone": s.Timezone,
		"status": status, "input": string(s.InputJSON), "target": string(s.TargetProfileJSON),
		"misfire": string(s.MisfireProfileJSON), "concurrency": string(s.ConcurrencyProfileJSON),
		"timeout": string(s.TimeoutProfileJSON), "retry": string(s.RetryProfileJSON), "approval": string(s.ApprovalProfileJSON),
		"marker": metadata.Declaration,
	})
	if err != nil {
		return declarationScheduleState{}, err
	}
	return declarationScheduleState{Schedule: s, Marker: metadata.Declaration, Revision: hashBytes(raw)}, nil
}

func readDeclarationSchedules(ctx context.Context, db *sql.DB, projectID string) (map[pc.ResourceKey]declarationScheduleState, error) {
	rows, err := db.QueryContext(ctx, `SELECT to_jsonb(s) FROM automation.schedules s WHERE project_id=$1 AND metadata_json ? 'project_declaration' ORDER BY schedule_key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[pc.ResourceKey]declarationScheduleState{}
	for rows.Next() {
		var raw []byte
		var s automation.Schedule
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		state, err := scheduleState(s)
		if err != nil {
			return nil, err
		}
		if _, exists := out[state.Marker.Resource]; exists {
			return nil, fail(pc.DeclarationIdentityConflict, "schedule_owner_duplicate")
		}
		out[state.Marker.Resource] = state
	}
	return out, rows.Err()
}

func addSchedulePrerequisites(out *Prerequisites, x localDeclaration, original map[pc.ResourceKey]pc.DeclarationBinding) {
	for key, binding := range out.Bindings {
		if binding.Kind == pc.DeclarationSchedule {
			out.Revisions["automation:"+string(key)] = "absent"
		}
	}
	for key, state := range x.ScheduleStates {
		binding, declared := out.Bindings[key]
		_, retained := original[key]
		if !declared && state.Marker.Retired && !retained {
			continue
		}
		if declared && binding.Kind != pc.DeclarationSchedule {
			continue
		}
		out.Bindings[key] = pc.DeclarationBinding{Kind: pc.DeclarationSchedule, OwnerRef: state.Schedule.ScheduleID}
		out.Revisions["automation:"+string(key)] = state.Revision
	}
}

func (r *LocalResolver) appendSchedules(ctx context.Context, p Principal, x localDeclaration, basis *pc.DeclarationPlanBasis, payloads map[string]json.RawMessage) error {
	keys := []pc.ResourceKey{}
	for key, binding := range basis.Bindings {
		if binding.Kind == pc.DeclarationSchedule {
			keys = append(keys, key)
		}
	}
	for key := range x.ScheduleStates {
		if resource, exists := x.Analysis.Loaded.Declaration.Resources[key]; exists && resource.Kind != pc.DeclarationSchedule {
			return fail(pc.DeclarationIdentityConflict, "schedule_resource_kind_changed")
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	service := automation.NewService(r.DB, routing.NewService(r.DB))
	for _, key := range keys {
		spec := x.Analysis.Loaded.Declaration.Resources[key].Schedule
		payload := scheduleOwnerPayload{Project: x.Analysis.Loaded.Declaration.Project, Resource: key, Schedule: spec, Key: pc.ProjectScheduleKey(x.Input.Project.Slug, string(key)), Retire: spec == nil}
		kind := pc.DeclarationReconcileSchedule
		if spec == nil {
			kind = pc.DeclarationRetireResource
			payload.Key = x.ScheduleStates[key].Schedule.ScheduleKey
		} else {
			input, err := pc.ScheduleDeclarationInput(payload.Project, key, *spec)
			if err != nil {
				return fail(pc.DeclarationInvalid, "schedule_input_invalid")
			}
			// New projects acquire their scope in the preceding registration action.
			// Preview target and run-as without inventing a future project/scope row.
			input.DryRun, input.ProjectRef, input.ScopeRef = true, "", "system"
			if _, err := service.CreateSchedule(ctx, declarationRequest(p), input); err != nil {
				return fail(pc.DeclarationTargetUnavailable, "schedule_target_or_actor_unavailable")
			}
		}
		raw, err := canonicalValue(payload)
		if err != nil {
			return err
		}
		a := pc.DeclarationAction{ID: string(kind) + ":" + string(key), Kind: kind, Resource: key, Owner: pc.DeclarationOwnerAutomation, TargetRef: x.Target.ProjectID + "/" + string(key), InputHash: hashBytes(raw), DependsOn: []string{"register_project:project"}, Authorization: basis.Actions[0].Authorization}
		if payload.Retire {
			a.ID += ":automation"
		}
		basis.Actions = append(basis.Actions, a)
		payloads[a.ID] = raw
	}
	return nil
}

type ScheduleOwner struct{ Resolver *LocalResolver }

func (o ScheduleOwner) Validate(_ context.Context, a pc.DeclarationAction, raw json.RawMessage) error {
	var p scheduleOwnerPayload
	if err := strictOwnerPayload(raw, &p); err != nil {
		return err
	}
	if a.Owner != pc.DeclarationOwnerAutomation || p.Resource != a.Resource || p.Project.ID+"/"+string(p.Resource) != a.TargetRef || p.Key != pc.ProjectScheduleKey(p.Project.Slug, string(p.Resource)) || p.Retire != (p.Schedule == nil) || (p.Retire && a.Kind != pc.DeclarationRetireResource) || (!p.Retire && a.Kind != pc.DeclarationReconcileSchedule) {
		return fail(pc.DeclarationInvalid, "schedule_owner_input_invalid")
	}
	if p.Schedule != nil {
		if _, err := pc.ScheduleDeclarationInput(p.Project, p.Resource, *p.Schedule); err != nil {
			return fail(pc.DeclarationInvalid, "schedule_input_invalid")
		}
	}
	return nil
}

func (o ScheduleOwner) Observe(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	var raw []byte
	err := o.Resolver.DB.QueryRowContext(ctx, `SELECT result FROM projects.declaration_owner_receipts
		WHERE project_id=$1 AND owner='automation' AND token=$2 AND input_hash=$3 AND operation_id=$4 AND action_id=$5 AND actor_id=$6 AND origin_node_id=$7 AND target_node_id=$8`,
		call.Target.ProjectID, call.Token, call.Action.InputHash, call.OperationID, call.Action.ID, call.Principal.ActorID, call.Principal.OriginNodeID, call.Target.OwnerNodeID).Scan(&raw)
	if err == sql.ErrNoRows {
		return Observation{State: Absent}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return Observation{}, err
	}
	return Observation{State: Committed, Receipt: &receipt}, nil
}

func (o ScheduleOwner) Apply(ctx context.Context, call ActionCall) (Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return Observation{}, err
	}
	if prior, err := o.Observe(ctx, call); err != nil || prior.State == Committed {
		return prior, err
	}
	var payload scheduleOwnerPayload
	if err := strictOwnerPayload(call.Payload, &payload); err != nil {
		return Observation{}, err
	}
	tx, err := o.Resolver.DB.BeginTx(ctx, nil)
	if err != nil {
		return Observation{}, err
	}
	defer tx.Rollback()
	// Fence project archive and exact operation ownership before changing a row.
	var lifecycle string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM projects.projects WHERE project_id=$1 FOR SHARE`, call.Target.ProjectID).Scan(&lifecycle); err != nil {
		return Observation{}, err
	}
	if lifecycle == "archived" {
		return Observation{}, fail(pc.DeclarationTargetUnavailable, "project_archived")
	}
	var existing automation.Schedule
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT to_jsonb(s) FROM automation.schedules s WHERE schedule_key=$1 FOR UPDATE`, payload.Key).Scan(&raw)
	before := "absent"
	var old declarationScheduleState
	if err == nil {
		if err = json.Unmarshal(raw, &existing); err != nil {
			return Observation{}, err
		}
		old, err = scheduleState(existing)
		if err != nil {
			return Observation{}, err
		}
		if existing.ProjectID == nil || *existing.ProjectID != call.Target.ProjectID || old.Marker.Resource != payload.Resource {
			return Observation{}, fail(pc.DeclarationIdentityConflict, "schedule_owned_elsewhere")
		}
		before = old.Revision
	} else if err != sql.ErrNoRows {
		return Observation{}, err
	}
	key := "automation:" + string(payload.Resource)
	if before != call.Expected.Revisions[key] || call.Expected.Bindings[payload.Resource].OwnerRef != existing.ScheduleID {
		return Observation{}, fail(pc.DeclarationPlanStale, "schedule_changed_since_plan")
	}
	service := automation.NewService(o.Resolver.DB, routing.NewService(o.Resolver.DB))
	updated := existing
	if payload.Retire {
		if existing.ScheduleID == "" {
			return Observation{}, fail(pc.DeclarationPlanStale, "retired_schedule_missing")
		}
		// Retain all timing, history, input and identities; only stop future runs.
		marker := old.Marker
		marker.Retired = true
		metadata, _ := json.Marshal(map[string]any{"project_declaration": marker})
		updated, err = service.DisableScheduleTx(ctx, tx, declarationRequest(call.Principal), existing.ScheduleID, metadata)
		if err != nil {
			return Observation{}, err
		}
	} else {
		input, err := pc.ScheduleDeclarationInput(payload.Project, payload.Resource, *payload.Schedule)
		if err != nil {
			return Observation{}, err
		}
		// Hash the portable declaration, not volatile next-run timestamps.
		desired, _ := canonicalValue(payload.Schedule)
		desiredHash := hashBytes(desired)
		marker := scheduleDeclarationMarker{Resource: payload.Resource, DesiredHash: desiredHash, Status: input.Status}
		// An unrelated source edit is not permission to undo an operator pause.
		if !old.Marker.Retired && old.Marker.Status == input.Status && (existing.Status == "paused" || existing.Status == "disabled") {
			input.Status = existing.Status
		}
		input.Metadata, _ = json.Marshal(map[string]any{"project_declaration": marker})
		detail, _, err := service.EnsureScheduleTx(ctx, tx, declarationRequest(call.Principal), input)
		if err != nil {
			return Observation{}, err
		}
		updated = detail.Schedule
	}
	// Read the stored JSONB encoding so all future fingerprints use identical
	// serialization, rather than mixing Go JSON with PostgreSQL spacing/order.
	if err = tx.QueryRowContext(ctx, `SELECT to_jsonb(s) FROM automation.schedules s WHERE schedule_id=$1`, updated.ScheduleID).Scan(&raw); err != nil {
		return Observation{}, err
	}
	if err = json.Unmarshal(raw, &updated); err != nil {
		return Observation{}, err
	}
	after, err := scheduleState(updated)
	if err != nil {
		return Observation{}, err
	}
	receipt := &Receipt{Token: call.Token, ActionID: call.Action.ID, Owner: call.Action.Owner, InputHash: call.Action.InputHash, EffectRef: updated.ScheduleID, Revisions: map[string]RevisionChange{}, Bindings: map[pc.ResourceKey]BindingChange{}}
	if before != after.Revision {
		receipt.Revisions[key] = RevisionChange{Before: before, After: after.Revision}
	}
	if existing.ScheduleID == "" {
		receipt.Bindings[payload.Resource] = BindingChange{Before: call.Expected.Bindings[payload.Resource], After: pc.DeclarationBinding{Kind: pc.DeclarationSchedule, OwnerRef: updated.ScheduleID}}
	}
	if err := (declarationSourceFence{Call: call, Resolver: o.Resolver}).Check(ctx); err != nil {
		return Observation{}, err
	}
	raw, err = json.Marshal(receipt)
	if err != nil {
		return Observation{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projects.declaration_owner_receipts(project_id,owner,token,input_hash,operation_id,action_id,actor_id,origin_node_id,target_node_id,result)
		VALUES($1,'automation',$2,$3,$4,$5,$6,$7,$8,$9)`, call.Target.ProjectID, call.Token, call.Action.InputHash, call.OperationID, call.Action.ID, call.Principal.ActorID, call.Principal.OriginNodeID, call.Target.OwnerNodeID, raw)
	if err != nil {
		return Observation{}, err
	}
	if err = tx.Commit(); err != nil {
		return Observation{}, err
	}
	return Observation{State: Committed, Receipt: receipt}, nil
}
