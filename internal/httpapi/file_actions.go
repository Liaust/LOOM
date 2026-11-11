package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/response"
)

type fileActionInput struct {
	BoxID       string `json:"box_id,omitempty"`
	Root        string `json:"root"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id"`
	FileID      string `json:"file_id"`
	BaseID      string `json:"base_id"`
	Generation  string `json:"generation"`
	Kind        string `json:"kind"`
	Target      string `json:"target,omitempty"`
}

type fileActionSnapshot struct {
	Root       string              `json:"root"`
	File       notesworkspace.File `json:"file"`
	Path       string              `json:"path"`
	BaseID     string              `json:"base_id"`
	Generation string              `json:"generation"`
	Revision   string              `json:"revision"`
}

type fileActionReceipt struct {
	BoxID       string               `json:"box_id,omitempty"`
	Root        string               `json:"root,omitempty"`
	OperationID string               `json:"operation_id"`
	State       string               `json:"state"`
	Reason      string               `json:"reason,omitempty"`
	Generation  string               `json:"generation"`
	File        *notesworkspace.File `json:"file,omitempty"`
	Path        string               `json:"path,omitempty"`
	BaseID      string               `json:"base_id,omitempty"`
}

func metadataActionReceipt(op notesworkspace.Operation) fileActionReceipt {
	result := fileActionReceipt{BoxID: op.Request.BoxID, OperationID: op.Request.OperationID, State: op.State, Reason: op.Reason, Generation: op.Request.Generation}
	if op.State == notesworkspace.Accepted && op.ResultFile != nil {
		result.File, result.Path, result.BaseID = op.ResultFile, op.ResultFile.RelativePath, op.ResultBaseID
	}
	return result
}

func metadataActionReceiptAtRoot(ref string, op notesworkspace.Operation) fileActionReceipt {
	result := metadataActionReceipt(op)
	result.Root = ref
	return result
}

func fileActionReceiptMatches(input fileActionInput, op notesworkspace.Operation, root, from, to string) bool {
	request := op.Request
	if input.BoxID != "" && request.BoxID != "" && input.BoxID != request.BoxID {
		return false
	}
	if !boxregistry.IsBoxRef(input.Root) || root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		op.Base.MetadataAction == nil || len(request.Content) != 0 || len(request.DirectoryTargets) != 0 ||
		from != filepath.Join(root, op.File.RelativePath) {
		return false
	}
	if request.Kind == notesworkspace.KindRename {
		if to != filepath.Join(root, request.DestinationPath) {
			return false
		}
	} else if request.Kind != notesworkspace.KindDelete || input.Target != "" || to != "" {
		return false
	}
	return input.OperationID == request.OperationID && input.Kind == request.Kind && input.FileID == request.FileID && input.BaseID == request.BaseID && input.Generation == request.Generation
}

func (s Server) handleFileActions(w http.ResponseWriter, r *http.Request) {
	cid, _ := requestMeta(r)
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "files", "actions", "File actions require the operator Unix socket.", nil)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		s.writeError(w, cid, 405, "method.not_allowed", "files", "actions", "Use GET or POST.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		root := r.URL.Query().Get("root")
		var rootErr error
		ctx, _, rootErr = s.desktopRequestRootContext(ctx, r, root)
		if rootErr != nil {
			s.writeDesktopNoteError(w, cid, rootErr)
			return
		}
		owner, collection, relative, err := s.desktopSource(ctx, root, r.URL.Query().Get("path"))
		if err != nil {
			s.writeDesktopNoteError(w, cid, err)
			return
		}
		read, err := owner.ReadMetadataAction(ctx, collection, relative)
		if err != nil {
			s.writeDesktopNoteError(w, cid, err)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, fileActionSnapshot{Root: root, File: read.File, Path: read.File.RelativePath, BaseID: read.Base.ID, Generation: read.Base.Generation, Revision: read.Base.MetadataAction.Revision()}))
		return
	}
	var input fileActionInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !boxregistry.IsBoxRef(input.Root) || input.Path == "" ||
		!strings.HasPrefix(input.OperationID, "desktop_action_") || len(input.OperationID) > 160 || input.FileID == "" || input.BaseID == "" || input.Generation == "" ||
		(input.Kind != notesworkspace.KindRename && input.Kind != notesworkspace.KindDelete) ||
		(input.Kind == notesworkspace.KindRename && input.Target == "") || (input.Kind == notesworkspace.KindDelete && input.Target != "") {
		s.writeError(w, cid, 400, "files.invalid_action", "files", "actions", "Supply an exact metadata-bound rename or recovery delete without content.", nil)
		return
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
	old, err := store.Operation(ctx, input.OperationID)
	requestBoxID := boundRoot.BoxID
	if err == nil {
		if old.Request.BoxID != "" && old.Request.BoxID != boundRoot.BoxID {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrIntentMismatch)
			return
		}
		requestBoxID = old.Request.BoxID
	}
	if err == nil && (old.State == notesworkspace.Accepted || old.State == notesworkspace.Conflict) {
		from, e := s.services.Files.RetainedTarget(input.Root, input.Path)
		var root, to string
		if e == nil {
			root, e = store.ScopePath(ctx, old.File.CollectionID)
		}
		if e == nil && input.Target != "" {
			to, e = s.services.Files.RetainedTarget(input.Root, input.Target)
		}
		if e != nil || !fileActionReceiptMatches(input, old, root, from, to) || !s.desktopReceiptSourceMatches(ctx, input.Root, root, old.File.CollectionID, old.Request.Generation) {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrIntentMismatch)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, metadataActionReceiptAtRoot(input.Root, old)))
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	owner, collection, relative, err := s.desktopSource(ctx, input.Root, input.Path)
	if err != nil {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	file, err := owner.Store.File(ctx, input.FileID)
	if err != nil || file.CollectionID != collection || file.RelativePath != relative || file.Deleted {
		s.writeDesktopNoteError(w, cid, notesworkspace.ErrStalePath)
		return
	}
	target := ""
	if input.Kind == notesworkspace.KindRename {
		_, targetCollection, resolved, e := s.desktopSource(ctx, input.Root, input.Target)
		if e != nil || targetCollection != collection {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrMembership)
			return
		}
		target = resolved
	}
	_, err = owner.ReceiveMetadataAction(ctx, notesworkspace.Request{BoxID: requestBoxID, OperationID: input.OperationID, FileID: input.FileID, BaseID: input.BaseID, Generation: input.Generation, Kind: input.Kind, DestinationPath: target})
	if err != nil {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	op, err := owner.ApplyMetadataAction(ctx, input.OperationID)
	if err != nil {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, metadataActionReceiptAtRoot(input.Root, op)))
}
