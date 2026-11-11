package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/response"
)

func (s Server) desktopUploadContext(ctx context.Context, r *http.Request, intent notesworkspace.UploadIntent) (context.Context, error) {
	if err := notesworkspace.ValidateUploadIntent(intent); err != nil {
		return ctx, err
	}
	ctx, root, err := s.desktopRequestRootContext(ctx, r, intent.Root)
	if err == nil && intent.BoxID != "" && intent.BoxID != root.BoxID {
		err = notesworkspace.ErrIntentMismatch
	}
	return ctx, err
}

func (s Server) handleFileUploads(w http.ResponseWriter, r *http.Request) {
	_, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if s.services.DB == nil {
		s.writeError(w, cid, 503, "files.uploads_unavailable", "files", "uploads", "Main upload storage is unavailable.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	owner := notesworkspace.Uploads{DB: s.services.DB, Directory: filepath.Join(s.services.RuntimeConfig.DataDir, "workspace-imports"), Blobs: s.services.Objects.Store}
	var result notesworkspace.Upload
	var err error
	action, id := r.URL.Query().Get("action"), r.URL.Query().Get("id")
	var saved notesworkspace.Upload
	if action == "status" || action == "chunk" || action == "complete" || action == "cancel" {
		saved, err = owner.Get(ctx, id)
		if err == nil {
			ctx, err = s.desktopUploadContext(ctx, r, saved.Intent)
		}
		if err != nil {
			s.writeFileUploadError(w, cid, err)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && action == "status":
		result = saved
	case r.Method == http.MethodPost && action == "begin":
		var input notesworkspace.UploadIntent
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || notesworkspace.ValidateUploadIntent(input) != nil {
			s.writeError(w, cid, 400, "files.invalid_upload", "files", "uploads", "Supply an exact bounded upload intent.", nil)
			return
		}
		ctx, err = s.desktopUploadContext(ctx, r, input)
		if err != nil {
			break
		}
		// An immutable old receipt does not depend on today's parent location.
		if old, e := owner.Get(ctx, input.ID); e == nil {
			if old.Intent != input {
				err = notesworkspace.ErrIntentMismatch
			} else {
				result = old
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			err = e
		} else {
			var collection string
			var source notesworkspace.Service
			source, collection, _, err = s.desktopSource(ctx, input.Root, input.Path)
			if err == nil {
				result, err = owner.Begin(ctx, source, collection, input)
			}
		}
	case r.Method == http.MethodPut && action == "chunk":
		var offset int64
		offset, err = strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err == nil {
			var payload []byte
			payload, err = io.ReadAll(http.MaxBytesReader(w, r.Body, notesworkspace.UploadChunkBytes))
			if err == nil {
				result, err = owner.Chunk(ctx, id, offset, payload, r.Header.Get("X-LOOM-SHA256"))
			}
		}
	case r.Method == http.MethodPost && action == "complete":
		var done bool
		result, done, err = owner.Replay(ctx, id)
		if err != nil || done {
			break
		}
		if saved.State == "accepted" || saved.State == "skipped" {
			result = saved
		} else {
			var source notesworkspace.Service
			source, _, _, err = s.desktopSource(ctx, saved.Intent.Root, saved.Path)
			if err == nil {
				result, err = owner.Complete(ctx, id, source)
			}
		}
		if err == nil && result.State == "accepted" {
			s.observeWorkspaceLinks(ctx, result.Intent.Root, result.Path)
		}
	case r.Method == http.MethodPost && action == "cancel":
		result, err = owner.Cancel(ctx, id)
	default:
		s.writeError(w, cid, 400, "files.invalid_upload_action", "files", "uploads", "Use begin, status, chunk, complete or cancel.", nil)
		return
	}
	if err != nil {
		s.writeFileUploadError(w, cid, err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, result))
}

func (s Server) writeFileUploadError(w http.ResponseWriter, cid string, err error) {
	status := 409
	if errors.Is(err, sql.ErrNoRows) {
		status = 404
	}
	summary := err.Error()
	if len(summary) > 240 {
		summary = summary[:240]
	}
	s.writeError(w, cid, status, "files.upload_not_accepted", "files", "uploads", "Upload paused: "+summary+". Source original and acknowledged progress are preserved.", err)
}
