package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/response"
)

type fileTreeMoveInput struct {
	BoxID       string `json:"box_id,omitempty"`
	Root        string `json:"root,omitempty"`
	OperationID string `json:"operation_id"`
	From        string `json:"from"`
	To          string `json:"to"`
	PlanHash    string `json:"plan_hash"`
	Confirm     bool   `json:"confirm"`
}

func fileTreeReceiptMatches(input fileTreeMoveInput, old notesworkspace.TreeMove, from, to string) bool {
	return (input.BoxID == "" || old.BoxID == "" || input.BoxID == old.BoxID) && input.PlanHash != "" && old.Hash == input.PlanHash && old.ID == input.OperationID && old.SourcePath != "" &&
		from == filepath.Join(old.SourcePath, old.From) && to == filepath.Join(old.SourcePath, old.To)
}

func fileTreeSummary(root string, move notesworkspace.TreeMove) map[string]any {
	result := move.Summary()
	result["root"] = root
	if move.BoxID != "" {
		result["box_id"] = move.BoxID
	}
	return result
}

func (s Server) handleFileTreeMove(w http.ResponseWriter, r *http.Request) {
	cid, _ := requestMeta(r)
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "files", "moves", "File operations require the operator Unix socket.", nil)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.writeError(w, cid, 405, "method.not_allowed", "files", "moves", "Use POST.", nil)
		return
	}
	var input fileTreeMoveInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || !strings.HasPrefix(input.OperationID, "desktop_tree_") || len(input.OperationID) > 160 {
		s.writeError(w, cid, 400, "files.invalid_tree_move", "files", "moves", "Supply an exact Main-side move request.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if input.Root == "" {
		input.Root = "box" // Historical tree requests are permanently Personal.
	}
	ctx, boundRoot, rootErr := s.desktopRequestRootContext(ctx, r, input.Root)
	if rootErr == nil && input.BoxID != "" && input.BoxID != boundRoot.BoxID {
		rootErr = notesworkspace.ErrIntentMismatch
	}
	if rootErr != nil {
		s.writeDesktopNoteError(w, cid, rootErr)
		return
	}
	if s.services.Files == nil || s.services.DB == nil {
		s.writeDesktopNoteError(w, cid, notesworkspace.ErrMembership)
		return
	}
	store := notesworkspace.SQLStore{DB: s.services.DB, ExclusiveSource: true}
	if input.Confirm {
		old, e := store.TreeMove(ctx, input.OperationID)
		if e != nil {
			s.writeDesktopNoteError(w, cid, e)
			return
		}
		from, e := s.services.Files.RetainedTarget(input.Root, input.From)
		if e != nil {
			s.writeDesktopNoteError(w, cid, e)
			return
		}
		to, e := s.services.Files.RetainedTarget(input.Root, input.To)
		if e != nil {
			s.writeDesktopNoteError(w, cid, e)
			return
		}
		if old.BoxID != "" && old.BoxID != boundRoot.BoxID || !fileTreeReceiptMatches(input, old, from, to) || !s.desktopReceiptSourceMatches(ctx, input.Root, old.SourcePath, old.ScopeID, old.Generation) {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrIntentMismatch)
			return
		}
		if old.State == notesworkspace.Accepted || old.State == notesworkspace.Conflict {
			if old.State == notesworkspace.Accepted && s.services.DB != nil {
				_ = s.workspaceLinks().Move(ctx, input.Root, input.From, input.To)
			}
			response.WriteJSON(w, 200, response.Success(cid, fileTreeSummary(input.Root, old)))
			return
		}
	}
	owner, collection, from, e := s.desktopSource(ctx, input.Root, input.From)
	if e != nil {
		s.writeDesktopNoteError(w, cid, e)
		return
	}
	_, targetCollection, to, e := s.desktopSource(ctx, input.Root, input.To)
	if e != nil || targetCollection != collection {
		s.writeDesktopNoteError(w, cid, notesworkspace.ErrMembership)
		return
	}
	var move notesworkspace.TreeMove
	if input.Confirm {
		move, e = owner.ApplyTree(ctx, input.OperationID, input.PlanHash)
	} else {
		if input.PlanHash != "" {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrIntentMismatch)
			return
		}
		move, e = owner.PreviewTree(ctx, collection, input.OperationID, from, to)
	}
	if e != nil {
		if errors.Is(e, fs.ErrNotExist) {
			s.writeError(w, cid, 404, "files.move_missing", "files", "moves", "The source or exact move receipt is missing.", nil)
		} else {
			s.writeError(w, cid, 409, "files.move_unavailable", "files", "moves", e.Error(), e)
		}
		return
	}
	if move.State == notesworkspace.Accepted && s.services.DB != nil {
		_ = s.workspaceLinks().Move(ctx, input.Root, input.From, input.To)
	}
	response.WriteJSON(w, 200, response.Success(cid, fileTreeSummary(input.Root, move)))
}
