package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

type desktopNote struct {
	Root       string              `json:"root"`
	Path       string              `json:"path"`
	File       notesworkspace.File `json:"file"`
	BaseID     string              `json:"base_id"`
	Generation string              `json:"generation"`
	Hash       string              `json:"hash"`
	Exists     bool                `json:"exists"`
	Text       string              `json:"text"`
}

type desktopMutation struct {
	BoxID       string `json:"box_id,omitempty"`
	Root        string `json:"root"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id"`
	FileID      string `json:"file_id"`
	BaseID      string `json:"base_id"`
	Generation  string `json:"generation"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	Target      string `json:"target,omitempty"`
	Directory   bool   `json:"directory,omitempty"`
}

// A request's immutable file root overrides a discovery default, but not an
// explicit Box selection. Validate before observing either live files or history.
func (s Server) desktopRootContext(ctx context.Context, ref string) (context.Context, workspacefiles.Root, error) {
	if !boxregistry.IsBoxRef(ref) {
		return ctx, workspacefiles.Root{}, notesworkspace.ErrUnsafePath
	}
	if s.services.Files == nil {
		return ctx, workspacefiles.Root{}, notesworkspace.ErrMembership
	}
	var root workspacefiles.Root
	for _, candidate := range s.services.Files.Roots {
		if candidate.Ref == ref {
			root = candidate
			break
		}
	}
	if root.SourcePath == "" {
		return ctx, root, workspacefiles.ErrUnknownRoot
	}
	if !root.IsWritableBox() {
		return ctx, root, notesworkspace.ErrUnsafePath
	}
	if s.services.Boxes != nil {
		binding, err := s.services.Boxes.Get(ref)
		if err != nil {
			return ctx, root, err
		}
		if binding.RootRef != ref || binding.ID != root.BoxID || binding.RootPath != root.SourcePath {
			return ctx, root, notesworkspace.ErrMembership
		}
		if selected, scoped := boxregistry.FromContext(ctx); scoped && selected.Origin != "default" && !selected.Contains(binding.ID) {
			return ctx, root, notesworkspace.ErrUnsafePath
		}
		selected, err := s.services.Boxes.Select(boxregistry.Selection{BoxIDs: []string{binding.ID}}, "")
		if err != nil {
			return ctx, root, err
		}
		ctx = boxregistry.WithSelection(ctx, selected)
	} else if ref != "box" {
		return ctx, root, boxregistry.ErrUnavailable
	}
	if _, err := s.services.Files.Stat(ctx, ref, "."); err != nil {
		return ctx, root, fmt.Errorf("%w: configured Box root cannot be opened", boxregistry.ErrUnavailable)
	}
	return ctx, root, nil
}

func (s Server) desktopRequestRootContext(ctx context.Context, r *http.Request, ref string) (context.Context, workspacefiles.Root, error) {
	if queryRoot := r.URL.Query().Get("root"); queryRoot != "" && queryRoot != ref {
		return ctx, workspacefiles.Root{}, notesworkspace.ErrUnsafePath
	}
	return s.desktopRootContext(ctx, ref)
}

func (s Server) desktopFilesystemSource(ctx context.Context, ref, path string) (context.Context, notesworkspace.Source, string, error) {
	ctx, root, err := s.desktopRootContext(ctx, ref)
	if err != nil {
		return ctx, notesworkspace.Source{}, "", err
	}
	absolute, err := s.services.Files.WritableTarget(ctx, ref, path)
	if err != nil {
		return ctx, notesworkspace.Source{}, "", err
	}
	physical, err := s.services.Files.MutationSource(ctx, root.SourcePath, "")
	if err != nil {
		return ctx, notesworkspace.Source{}, "", err
	}
	relative, err := filepath.Rel(physical.Path, absolute)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ctx, notesworkspace.Source{}, "", notesworkspace.ErrUnsafePath
	}
	return ctx, physical, filepath.ToSlash(relative), nil
}

// Company scopes are new filesystem-bound scopes. Their generation includes the
// immutable root ref; replay needs the root, never a former file parent.
func (s Server) desktopReceiptSourceMatches(ctx context.Context, ref, source, collection, generation string) bool {
	if ref == "box" {
		return true // Retain historical Personal Notes generations.
	}
	physical, err := s.services.Files.MutationSource(ctx, source, collection)
	return err == nil && physical.Generation == generation
}

func (s Server) desktopSource(ctx context.Context, root, path string) (notesworkspace.Service, string, string, error) {
	var empty notesworkspace.Service
	ctx, physical, relative, err := s.desktopFilesystemSource(ctx, root, path)
	if err != nil {
		return empty, "", "", err
	}
	if s.services.DB == nil {
		return empty, "", "", notesworkspace.ErrMembership
	}
	store := notesworkspace.SQLStore{DB: s.services.DB, ExclusiveSource: true}
	if strings.HasPrefix(physical.CollectionID, "filesystem_box_") {
		if err = store.EnsureBoxScope(ctx, notesworkspace.SourceScope{ID: physical.CollectionID, Path: physical.Path, NodeKey: s.services.RuntimeConfig.NodeID, RootIdentity: physical.Generation}); err != nil {
			return empty, "", "", err
		}
	}
	return notesworkspace.Service{Store: store, Sources: workspacefiles.MutationResolver{Files: s.services.Files, Source: physical}}, physical.CollectionID,
		relative, nil
}

// Old saved requests retain their original selection-bound generation. Never
// silently rebase them onto the new filesystem admission contract.
func (s Server) legacyDesktopSource(ctx context.Context, root, path string) (notesworkspace.Service, string, string, error) {
	var empty notesworkspace.Service
	if root != "box" || s.services.Files == nil || s.services.DB == nil {
		return empty, "", "", notesworkspace.ErrMembership
	}
	absolute, err := s.services.Files.WritableTarget(ctx, root, path)
	if err != nil {
		return empty, "", "", err
	}
	var collection, source string
	err = s.services.DB.QueryRowContext(ctx, `SELECT notes_source_root_id,source_path FROM knowledge.notes_source_roots
 WHERE node_key=$1 AND status='active' AND source_path<>'' AND left($2,length(source_path)+1)=source_path||'/'
 ORDER BY length(source_path) DESC,notes_source_root_id LIMIT 1`, s.services.RuntimeConfig.NodeID, absolute).Scan(&collection, &source)
	if err != nil {
		return empty, "", "", err
	}
	config, err := notesworkspacesync.LoadRuntimeConfig(s.services.RuntimeConfig.NotesWorkspaceConfig)
	if err != nil {
		return empty, "", "", fmt.Errorf("desktop source runtime configuration: %w", err)
	}
	runtime := notesworkspacesync.Runtime{DB: s.services.DB, Config: config, NodeKey: s.services.RuntimeConfig.NodeID, Knowledge: knowledge.NewService(s.services.DB)}
	owner, err := runtime.DesktopSource(ctx)
	return owner, collection, filepath.ToSlash(strings.TrimPrefix(absolute, source+string(filepath.Separator))), err
}

func noteSnapshot(root, path string, read notesworkspace.ReadResult) desktopNote {
	return desktopNote{Root: root, Path: path, File: read.File, BaseID: read.Base.ID, Generation: read.Base.Generation,
		Hash: read.Base.Hash, Exists: read.Base.Exists, Text: string(read.Base.Content)}
}

func (s Server) handleFileNote(w http.ResponseWriter, r *http.Request) {
	cid, _ := requestMeta(r)
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "files", "notes", "Notes operations require the existing operator Unix socket.", nil)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		root, path := r.URL.Query().Get("root"), r.URL.Query().Get("path")
		var rootErr error
		ctx, _, rootErr = s.desktopRequestRootContext(ctx, r, root)
		if rootErr != nil {
			s.writeDesktopNoteError(w, cid, rootErr)
			return
		}
		owner, collection, relative, err := s.desktopSource(ctx, root, path)
		var read notesworkspace.ReadResult
		if err == nil {
			if r.URL.Query().Get("directory") == "true" {
				read, err = owner.ReadDirectory(ctx, collection, relative)
			} else {
				read, err = owner.ReadText(ctx, collection, relative)
			}
		}
		if err != nil {
			s.writeDesktopNoteError(w, cid, err)
			return
		}
		if read.Base.Exists && r.URL.Query().Get("directory") != "true" {
			s.observeWorkspaceLinks(ctx, root, path)
		}
		response.WriteJSON(w, 200, response.Success(cid, noteSnapshot(root, path, read)))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		s.writeError(w, cid, 405, "method.not_allowed", "files", "notes", "Use GET or POST.", nil)
		return
	}
	var input desktopMutation
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || !strings.HasPrefix(input.OperationID, "desktop_") || len(input.OperationID) > 160 || (input.Kind != "edit" && input.Kind != "rename" && input.Kind != "delete") {
		s.writeError(w, cid, 400, "files.invalid_note_operation", "files", "notes", "Supply a bounded source-bound note operation with a stable desktop identity.", nil)
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
	requestBoxID := boundRoot.BoxID
	// A receipt is history, not a new mutation. Match the complete original
	// request before looking up a live parent that may no longer exist.
	if s.services.DB != nil && s.services.Files != nil {
		store := notesworkspace.SQLStore{DB: s.services.DB}
		old, lookupErr := store.Operation(ctx, input.OperationID)
		if lookupErr == nil {
			if old.Request.BoxID != "" && old.Request.BoxID != boundRoot.BoxID {
				s.writeDesktopNoteError(w, cid, notesworkspace.ErrIntentMismatch)
				return
			}
			requestBoxID = old.Request.BoxID // Do not re-stamp old pending Personal requests.
		}
		if lookupErr == nil && (old.State == notesworkspace.Accepted || old.State == notesworkspace.Conflict) {
			absolute, matchErr := s.services.Files.RetainedTarget(input.Root, input.Path)
			var source string
			if matchErr == nil {
				source, matchErr = store.ScopePath(ctx, old.File.CollectionID)
			}
			var destination string
			if matchErr == nil && input.Target != "" {
				destination, matchErr = s.services.Files.RetainedTarget(input.Root, input.Target)
			}
			if matchErr != nil || !desktopReceiptMatches(input, old, source, absolute, destination) || !s.desktopReceiptSourceMatches(ctx, input.Root, source, old.File.CollectionID, old.Request.Generation) {
				s.writeDesktopNoteError(w, cid, notesworkspace.ErrIntentMismatch)
				return
			}
			s.writeDesktopReceipt(ctx, w, cid, input, old, store)
			return
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			s.writeDesktopNoteError(w, cid, lookupErr)
			return
		}
	}
	owner, collection, relative, err := s.desktopSource(ctx, input.Root, input.Path)
	if err != nil {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	if !strings.HasPrefix(input.Generation, "filesystem:") {
		owner, collection, relative, err = s.legacyDesktopSource(ctx, input.Root, input.Path)
		if err != nil {
			s.writeDesktopNoteError(w, cid, err)
			return
		}
	}
	file, err := owner.Store.File(ctx, input.FileID)
	if err != nil || file.CollectionID != collection {
		s.writeDesktopNoteError(w, cid, notesworkspace.ErrMembership)
		return
	}
	// Only an exact saved request may replay after a completed rename/delete.
	if file.RelativePath != relative || file.Deleted {
		old, e := owner.Store.Operation(ctx, input.OperationID)
		if e != nil || old.File.RelativePath != relative || old.File.CollectionID != collection {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrStalePath)
			return
		}
	}
	destination := ""
	if input.Kind == "rename" {
		resolve := s.desktopSource
		if !strings.HasPrefix(input.Generation, "filesystem:") {
			resolve = s.legacyDesktopSource
		}
		_, targetCollection, targetRelative, e := resolve(ctx, input.Root, input.Target)
		if e != nil || targetCollection != collection {
			s.writeDesktopNoteError(w, cid, notesworkspace.ErrMembership)
			return
		}
		destination = targetRelative
	} else if input.Target != "" {
		s.writeDesktopNoteError(w, cid, notesworkspace.ErrUnsafePath)
		return
	}
	request := notesworkspace.Request{BoxID: requestBoxID, OperationID: input.OperationID, FileID: input.FileID, BaseID: input.BaseID,
		Generation: input.Generation, Kind: input.Kind, Content: []byte(input.Text), DestinationPath: destination}
	if input.Directory {
		_, err = owner.ReceiveDirectory(ctx, request)
	} else {
		_, err = owner.Receive(ctx, request)
	}
	if err != nil {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	op, err := owner.Apply(ctx, input.OperationID)
	if err != nil {
		s.writeDesktopNoteError(w, cid, err)
		return
	}
	s.writeDesktopReceipt(ctx, w, cid, input, op, owner.Store)
}

func desktopReceiptMatches(input desktopMutation, op notesworkspace.Operation, source, absolute, destination string) bool {
	request := op.Request
	if input.BoxID != "" && request.BoxID != "" && input.BoxID != request.BoxID {
		return false
	}
	if source == "" || !filepath.IsAbs(source) || filepath.Clean(source) != source || !boxregistry.IsBoxRef(input.Root) ||
		absolute != filepath.Join(source, op.File.RelativePath) ||
		input.Directory != (op.File.EntryKind == "directory") || len(request.DirectoryTargets) != 0 {
		return false
	}
	if request.Kind == "rename" {
		if destination != filepath.Join(source, request.DestinationPath) {
			return false
		}
	} else if input.Target != "" || destination != "" {
		return false
	}
	return input.OperationID == request.OperationID && input.FileID == request.FileID && input.BaseID == request.BaseID &&
		input.Generation == request.Generation && input.Kind == request.Kind && bytes.Equal([]byte(input.Text), request.Content)
}

func (s Server) writeDesktopReceipt(ctx context.Context, w http.ResponseWriter, cid string, input desktopMutation, op notesworkspace.Operation, store notesworkspace.Store) {
	result := map[string]any{"root": input.Root, "operation_id": input.OperationID, "state": op.State, "reason": op.Reason}
	if op.Request.BoxID != "" {
		result["box_id"] = op.Request.BoxID
	}
	if op.State == notesworkspace.Accepted && s.services.DB != nil && s.services.Files != nil {
		graph := s.workspaceLinks()
		switch input.Kind {
		case "delete":
			if graph.Retire(ctx, input.Root, input.Path) != nil {
				result["links_state"] = "deferred"
			}
		case "rename":
			if input.Directory {
				if graph.Move(ctx, input.Root, input.Path, input.Target) != nil {
					result["links_state"] = "deferred"
				}
			} else {
				_ = graph.Retire(ctx, input.Root, input.Path)
				result["links_state"] = s.observeWorkspaceLinks(ctx, input.Root, input.Target)
			}
		default:
			if !input.Directory {
				result["links_state"] = s.observeWorkspaceLinks(ctx, input.Root, input.Path)
			}
		}
	}
	if op.ResultBaseID != "" {
		base, e := store.Base(ctx, op.ResultBaseID)
		if e != nil {
			s.writeDesktopNoteError(w, cid, e)
			return
		}
		resultFile := op.File
		if op.ResultFile != nil {
			resultFile = *op.ResultFile
		}
		resultPath := input.Path
		if input.Kind == "rename" {
			resultPath = input.Target
		}
		result["note"] = noteSnapshot(input.Root, resultPath, notesworkspace.ReadResult{File: resultFile, Base: base})
	}
	response.WriteJSON(w, 200, response.Success(cid, result))
}

func (s Server) writeDesktopNoteError(w http.ResponseWriter, cid string, err error) {
	var admission *knowledge.NotesAdmissionError
	if errors.As(err, &admission) {
		summary := "Source admission is paused at " + admission.Phase + ". Local drafts are preserved."
		if admission.Reason != "" {
			summary += " Reason: " + admission.Reason + "."
		}
		s.writeError(w, cid, 409, "files.note_source_unavailable", "files", "notes", summary, fmt.Errorf("source admission phase %s reason %s", admission.Phase, admission.Reason))
		return
	}
	switch {
	case errors.Is(err, boxregistry.ErrUnknown), errors.Is(err, workspacefiles.ErrUnknownRoot):
		s.writeError(w, cid, 404, "files.box_not_found", "files", "notes", "The requested Box root is not registered.", nil)
	case errors.Is(err, boxregistry.ErrUnavailable):
		s.writeError(w, cid, 503, "files.box_unavailable", "files", "notes", "The requested Box root is unavailable. Saved work and receipts are preserved.", nil)
	case errors.Is(err, notesworkspace.ErrMembership), errors.Is(err, knowledge.ErrNotesCustodyPaused), errors.Is(err, knowledge.ErrNotesPathExcluded), errors.Is(err, notesworkspace.ErrReferenceOnly):
		s.writeError(w, cid, 409, "files.note_source_unavailable", "files", "notes", "This path is not available to the current source owner or editable file type. Original browsing and local drafts are preserved.", nil)
	case errors.Is(err, notesworkspace.ErrStalePath), errors.Is(err, notesworkspace.ErrPathBusy), errors.Is(err, notesworkspace.ErrIntentMismatch):
		s.writeError(w, cid, 409, "files.note_conflict", "files", "notes", "The source or operation changed. Preserve the local edit and inspect the current source before retrying.", nil)
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, fs.ErrNotExist):
		s.writeError(w, cid, 404, "files.note_not_found", "files", "notes", "The source scope, file or exact base was not found.", nil)
	case errors.Is(err, notesworkspace.ErrUnsafePath), errors.Is(err, fs.ErrPermission):
		s.writeError(w, cid, 403, "files.note_access_denied", "files", "notes", "This Notes path is restricted.", nil)
	default:
		s.writeError(w, cid, 503, "files.note_unavailable", "files", "notes", "The source owner could not complete this operation. Retain the same operation identity and local edit.", err)
	}
}
