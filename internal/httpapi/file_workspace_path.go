package httpapi

import (
	"context"
	"net/http"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/response"
)

type workspacePathRequest struct {
	Action   string `json:"action"`
	Kind     string `json:"kind"`
	Project  string `json:"project"`
	From     string `json:"from"`
	To       string `json:"to"`
	FromRoot string `json:"from_root,omitempty"`
	ToRoot   string `json:"to_root,omitempty"`
	ID       string `json:"id"`
	Hash     string `json:"plan_hash"`
	Confirm  bool   `json:"confirm"`
}

func (in workspacePathRequest) matches(summary map[string]any) bool {
	for _, field := range []struct{ value, name string }{{in.Kind, "kind"}, {in.Project, "project"}, {in.From, "source"}, {in.To, "destination"}, {in.FromRoot, "from_root"}, {in.ToRoot, "to_root"}} {
		if field.value != "" && summary[field.name] != field.value {
			return false
		}
	}
	return true
}

func (s Server) handleWorkspacePath(w http.ResponseWriter, r *http.Request) {
	_, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "workspace-path", "Use POST.", nil)
		return
	}
	var in workspacePathRequest
	if !s.decodeWorkspaceArchiveRequest(w, r, cid, &in) {
		return
	}
	if in.Action != "plan" && in.Action != "status" && in.Action != "apply" && in.Action != "cancel" {
		s.writeError(w, cid, 400, "files.location_action", "files", "workspace-path", "Use plan, status, apply or cancel.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	ctx, err := s.lifecycleRequestContext(ctx, r)
	if err != nil {
		s.writeLifecycleBoxError(w, cid, err)
		return
	}
	owner, err := s.lifecycleWorkspacePaths(ctx)
	if err != nil {
		s.writeLifecycleBoxError(w, cid, err)
		return
	}
	box, _ := s.lifecycleBox(ctx)
	if in.FromRoot != "" && in.FromRoot != box.RootRef || !box.Primary && (in.FromRoot == "" || in.ToRoot == "") {
		s.writeError(w, cid, 400, "files.location_roots", "files", "workspace-path", "Name the exact selected Box source and destination roots. Cross-Box transfer is not supported.", nil)
		return
	}
	capability := capabilities.WorkspaceArchivePlanCapability
	if in.Action == "apply" || in.Action == "cancel" {
		capability = capabilities.WorkspaceArchiveApplyCapability
	}
	actor, ok := s.authorizeWorkspaceArchive(w, ctx, cid, capability)
	if !ok {
		return
	}
	if in.Action == "plan" {
		toRoot, to := box.RootRef, in.To
		if in.Kind == "retain_child" {
			toRoot = "storage"
		}
		if in.ToRoot != "" && in.ToRoot != toRoot {
			s.writeError(w, cid, 400, "files.location_roots", "files", "workspace-path", "The destination root does not match this bound operation. Cross-Box transfer is not supported.", nil)
			return
		}
		if toRoot == "storage" {
			prefix, err := s.lifecycleStoragePrefix(ctx)
			if err == nil {
				to, err = boundStoragePath(prefix, to)
			}
			if err != nil {
				s.writeError(w, cid, 400, "files.location_namespace", "files", "workspace-path", err.Error(), nil)
				return
			}
		}
		plan, err := owner.Plan(ctx, in.Kind, in.Project, in.From, to, actor.ActorID, "Reviewed filesystem operation in Loom.")
		if err != nil {
			s.writeError(w, cid, 409, "files.location_held", "files", "workspace-path", err.Error(), nil)
			return
		}
		summary, err := s.lifecyclePathSummary(ctx, plan, "planned")
		if err != nil || !in.matches(summary) {
			s.writeError(w, cid, 409, "files.location_binding", "files", "workspace-path", "The reviewed paths do not match the selected Box and Storage namespace.", err)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, summary))
		return
	}
	load := owner.Load
	if in.Action == "cancel" {
		load = owner.LoadCancellation
	}
	plan, record, err := load(ctx, in.ID, in.Hash)
	if err != nil {
		s.writeError(w, cid, 404, "files.location_not_found", "files", "workspace-path", err.Error(), nil)
		return
	}
	if plan.Move.Actor != actor.ActorID {
		s.writeError(w, cid, 403, "files.location_forbidden", "files", "workspace-path", "This actor does not own the saved operation.", nil)
		return
	}
	summary, err := s.lifecyclePathSummary(ctx, plan, record.State)
	if err != nil || !in.matches(summary) {
		s.writeError(w, cid, 409, "files.location_binding", "files", "workspace-path", "Saved operation does not match the selected Box and original global paths.", err)
		return
	}
	if in.Action != "status" {
		if !in.Confirm || in.Hash == "" || in.Hash != plan.Digest {
			s.writeError(w, cid, 409, "files.location_confirmation", "files", "workspace-path", "Confirm the exact saved review.", nil)
			return
		}
		if in.Action == "apply" {
			plan, record, err = owner.Apply(ctx, in.ID, in.Hash)
		} else {
			plan, record, err = owner.Cancel(ctx, in.ID, in.Hash)
		}
		if err != nil {
			s.writeError(w, cid, 409, "files.location_held", "files", "workspace-path", err.Error(), nil)
			return
		}
	}
	summary, err = s.lifecyclePathSummary(ctx, plan, record.State)
	if err != nil {
		s.writeError(w, cid, 409, "files.location_binding", "files", "workspace-path", "Source owner receipt does not match the selected Box.", err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, summary))
}
