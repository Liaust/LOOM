package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

type retainedReview struct {
	ID            string `json:"id"`
	Hash          string `json:"plan_hash"`
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Entries       int    `json:"entries"`
	Bytes         int64  `json:"bytes"`
	Inventory     string `json:"inventory"`
	State         string `json:"state"`
	Qualification string `json:"qualification"`
}

func retainedMoveReview(record storagecatalog.RetainedRelocation) (retainedReview, error) {
	var plan storagearchive.RetainedRelocationPlan
	if json.Unmarshal(record.Plan, &plan) != nil || plan.ID != record.ID || plan.Digest != record.Digest {
		return retainedReview{}, fmt.Errorf("saved retained plan unavailable")
	}
	qualification := "Same-filesystem retained-content relocation. Original-source history is unchanged. Source selection is not proof of a new cloud upload."
	if record.State == "cancelled" {
		qualification = "Cancelled after verifying the original source root and absent destination. Cancellation moved, deleted and rebound nothing; saved history remains."
	} else if record.State == "prepared" {
		qualification = "Unfinished exact move: the filesystem effect is not yet confirmed. Inspect/recover this same request, or cancel only after Main proves nothing moved."
	}
	return retainedReview{ID: record.ID, Hash: record.Digest, Source: plan.Source.RelativePath, Destination: plan.Destination.RelativePath, Entries: len(plan.Inventory.Entries), Bytes: plan.Inventory.TotalBytes, Inventory: plan.Inventory.Digest, State: record.State, Qualification: qualification}, nil
}
func (s Server) handleRetainedMove(w http.ResponseWriter, r *http.Request) {
	files, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if s.services.RetainedRelocations == nil {
		s.writeError(w, cid, 503, "files.relocation_unavailable", "files", "retained-move", "Retained relocation is unavailable.", nil)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "retained-move", "Use GET for status or POST for reviewed actions.", nil)
		return
	}
	rootOperator, _ := r.Context().Value(localRootTransportKey{}).(bool)
	budget := 3 * time.Minute
	if rootOperator {
		budget = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	var in struct {
		Action  string `json:"action"`
		From    string `json:"from"`
		To      string `json:"to"`
		ID      string `json:"id"`
		Hash    string `json:"plan_hash"`
		Confirm bool   `json:"confirm"`
	}
	if r.Method == http.MethodGet {
		in.Action = "status"
		in.ID = r.URL.Query().Get("id")
	} else if !s.decodeWorkspaceArchiveRequest(w, r, cid, &in) {
		return
	}
	if (in.Action == "operator_plan" || in.Action == "operator_prepare") && !rootOperator {
		s.writeError(w, cid, 403, "files.operator_required", "files", "retained-move", "This action requires the local root operator over the Unix socket.", nil)
		return
	}
	capability := capabilities.WorkspaceArchivePlanCapability
	if in.Action == "apply" || in.Action == "operator_prepare" || in.Action == "cancel" {
		capability = capabilities.WorkspaceArchiveApplyCapability
	}
	actor, ok := s.authorizeWorkspaceArchive(w, ctx, cid, capability)
	if !ok {
		return
	}
	service := s.services.RetainedRelocations
	if in.Action == "plan" || in.Action == "operator_plan" {
		if _, _, err := files.SourcePath(ctx, "storage", in.From); err != nil {
			s.writeFileError(w, cid, err)
			return
		}
		planFn := service.Plan
		if in.Action == "operator_plan" {
			planFn = service.PlanOperator
		}
		plan, err := planFn(ctx, in.From, in.To, actor.ActorID, "Content-first organisation reviewed in Workspace.")
		if err != nil {
			s.writeError(w, cid, 409, "files.relocation_held", "files", "retained-move", err.Error(), nil)
			return
		}
		record, err := service.Catalog.RetainedRelocation(ctx, plan.ID)
		if err != nil {
			s.writeError(w, cid, 503, "files.relocation_unavailable", "files", "retained-move", "Saved relocation review is unavailable.", nil)
			return
		}
		review, err := retainedMoveReview(record)
		if err != nil {
			s.writeError(w, cid, 503, "files.relocation_unavailable", "files", "retained-move", err.Error(), nil)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, review))
		return
	}
	record, err := service.Catalog.RetainedRelocation(ctx, in.ID)
	if err != nil {
		s.writeError(w, cid, 404, "files.relocation_not_found", "files", "retained-move", "Saved relocation not found.", nil)
		return
	}
	var plan storagearchive.RetainedRelocationPlan
	if json.Unmarshal(record.Plan, &plan) != nil || plan.Actor != actor.ActorID {
		s.writeError(w, cid, 403, "files.relocation_forbidden", "files", "retained-move", "This actor does not own the saved review.", nil)
		return
	}
	if in.Action == "apply" || in.Action == "operator_prepare" || in.Action == "cancel" {
		if !in.Confirm || in.Hash == "" || in.Hash != record.Digest {
			s.writeError(w, cid, 409, "files.relocation_confirmation", "files", "retained-move", "Confirm the exact reviewed plan hash.", nil)
			return
		}
		applyFn := service.Apply
		if in.Action == "operator_prepare" {
			applyFn = service.PrepareOperator
		}
		if in.Action == "cancel" {
			applyFn = service.Cancel
		}
		record, err = applyFn(ctx, in.ID, in.Hash)
		if err != nil {
			review, _ := retainedMoveReview(record)
			response.WriteJSON(w, 409, struct {
				response.ErrorEnvelope
				Data retainedReview `json:"data"`
			}{response.ErrorEnvelope{OK: false, Meta: response.NewMeta(cid), Error: response.ErrorBody{Code: "files.relocation_held", Domain: "files", Summary: err.Error(), CorrelationID: cid}}, review})
			return
		}
		if in.Action == "operator_prepare" {
			response.WriteJSON(w, 200, response.Success(cid, struct {
				State string                                `json:"state"`
				Plan  storagearchive.RetainedRelocationPlan `json:"plan"`
			}{record.State, plan}))
			return
		}
	} else if in.Action != "status" {
		s.writeError(w, cid, 400, "files.relocation_action", "files", "retained-move", "Use plan, status, apply or cancel.", nil)
		return
	}
	review, err := retainedMoveReview(record)
	if err != nil {
		s.writeError(w, cid, 503, "files.relocation_unavailable", "files", "retained-move", err.Error(), nil)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, review))
}
