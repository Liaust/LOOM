package httpapi

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

// Only ServeUnix creates this context value. Headers and TCP bootstrap context
// are not authentication for arbitrary original-file reads.
type localFileTransportKey struct{}
type localRootTransportKey struct{}

func unixFileTransportContext(ctx context.Context, conn net.Conn) context.Context {
	if _, ok := conn.(*net.UnixConn); ok {
		ctx = context.WithValue(ctx, localFileTransportKey{}, true)
		return context.WithValue(ctx, localRootTransportKey{}, unixRootPeer(conn))
	}
	return ctx
}

func (s Server) filesRequest(w http.ResponseWriter, r *http.Request) (*workspacefiles.Service, string, bool) {
	correlationID, _ := requestMeta(r)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "files", r.URL.Path, "This file interface is read-only.", nil)
		return nil, correlationID, false
	}
	return s.filesTransport(w, r)
}

func (s Server) filesTransport(w http.ResponseWriter, r *http.Request) (*workspacefiles.Service, string, bool) {
	correlationID, _ := requestMeta(r)
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, correlationID, http.StatusForbidden, "files.local_transport_required", "files", "transport", "Original-file access requires the existing local Unix socket.", nil)
		return nil, correlationID, false
	}
	if s.services.Files == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "files.unavailable", "files", "roots", "Original-file access is not configured.", nil)
		return nil, correlationID, false
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	return s.services.Files, correlationID, true
}

func (s Server) handleFileRoots(w http.ResponseWriter, r *http.Request) {
	service, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	result, err := service.ListRoots(r.Context())
	if err != nil {
		s.writeFileError(w, correlationID, err)
		return
	}
	if r.URL.Query().Get("box_context") != "v1" {
		legacy := result[:0]
		for _, root := range result {
			if root.SourceKind != "box" || root.Ref == "box" {
				legacy = append(legacy, root)
			}
		}
		result = legacy
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleFileEntries(w http.ResponseWriter, r *http.Request) {
	service, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	input := workspacefiles.ListInput{Root: query.Get("root"), Path: query.Get("path"), Cursor: query.Get("cursor")}
	var err error
	if query.Has("limit") {
		input.Limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil {
			s.writeFileError(w, correlationID, workspacefiles.ErrInvalidPath)
			return
		}
	}
	if query.Has("show_ignored") {
		input.ShowIgnored, err = strconv.ParseBool(query.Get("show_ignored"))
		if err != nil {
			s.writeFileError(w, correlationID, workspacefiles.ErrInvalidPath)
			return
		}
	}
	result, err := service.List(workspacefiles.WithApplicationView(r.Context(), query.Get("view_revision")), input)
	if err != nil {
		s.writeFileError(w, correlationID, err)
		return
	}
	if s.services.DB != nil {
		_ = s.workspaceLinks().Register(r.Context(), input.Root, result.Entries)
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleFileStat(w http.ResponseWriter, r *http.Request) {
	service, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	result, err := service.Stat(workspacefiles.WithApplicationView(r.Context(), r.URL.Query().Get("view_revision")), r.URL.Query().Get("root"), r.URL.Query().Get("path"))
	if err != nil {
		s.writeFileError(w, correlationID, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleFileSearch(w http.ResponseWriter, r *http.Request) {
	service, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	in := workspacefiles.SearchInput{Root: q.Get("root"), Path: q.Get("path"), Query: q.Get("query"), Cursor: q.Get("cursor")}
	if q.Has("limit") {
		var err error
		in.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			s.writeFileError(w, correlationID, workspacefiles.ErrInvalidPath)
			return
		}
	}
	page, err := service.Search(r.Context(), in)
	if err != nil {
		s.writeFileError(w, correlationID, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, page))
}

func (s Server) handleFileExplain(w http.ResponseWriter, r *http.Request) {
	service, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	result, err := service.Explain(r.Context(), r.URL.Query().Get("root"), r.URL.Query().Get("path"))
	if err != nil {
		s.writeFileError(w, correlationID, err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	service, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	file, metadata, err := service.Open(workspacefiles.WithApplicationView(r.Context(), r.URL.Query().Get("view_revision")), r.URL.Query().Get("root"), r.URL.Query().Get("path"))
	if err != nil {
		s.writeFileError(w, correlationID, err)
		return
	}
	defer file.Close()
	w.Header().Set("ETag", metadata.Revision)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, metadata.Name, metadata.ModifiedAt, contextFile{ctx: r.Context(), file: file})
}

type contextFile struct {
	ctx  context.Context
	file *os.File
}

func (f contextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.Read(p)
}
func (f contextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.Seek(offset, whence)
}

var _ io.ReadSeeker = contextFile{}

func (s Server) writeFileError(w http.ResponseWriter, correlationID string, err error) {
	status, code, summary := http.StatusInternalServerError, "files.read_failed", "Could not read the requested original."
	switch {
	case strings.HasPrefix(err.Error(), "application.browse.") || strings.HasPrefix(err.Error(), "application.socket."):
		status, code, summary = http.StatusConflict, "files.application_unavailable", "The approved application view is unavailable or changed. Refresh this source; no files were moved or enrolled."
		if err.Error() == "application.browse.private" {
			status, code, summary = http.StatusForbidden, "files.application_private", "This allocation has no approved content view."
		}
		if strings.HasSuffix(err.Error(), "path_invalid") || strings.HasSuffix(err.Error(), "page_invalid") || strings.HasSuffix(err.Error(), "ref_invalid") {
			status, code, summary = http.StatusBadRequest, "files.invalid_path", "Use an approved application view and a valid relative path."
		}
	case errors.Is(err, workspacefiles.ErrInvalidPath):
		status, code, summary = http.StatusBadRequest, "files.invalid_path", "Use a configured root and a valid relative path and page limit."
	case errors.Is(err, workspacefiles.ErrUnknownRoot):
		status, code, summary = http.StatusNotFound, "files.root_not_found", "The requested file root is not configured."
	case errors.Is(err, workspacefiles.ErrInvalidCursor):
		status, code, summary = http.StatusConflict, "files.cursor_invalid", "The directory changed or its cursor expired; restart this directory listing."
	case errors.Is(err, workspacefiles.ErrRootChanged):
		status, code, summary = http.StatusConflict, "files.root_changed", "The configured root changed identity; its owner must re-establish the root binding."
	case errors.Is(err, fs.ErrPermission):
		status, code, summary = http.StatusForbidden, "files.access_denied", "The requested path is restricted or traverses a symbolic link."
	case errors.Is(err, fs.ErrExist):
		status, code, summary = http.StatusConflict, "files.already_exists", "The destination already exists; no existing file was replaced."
	case errors.Is(err, fs.ErrNotExist):
		status, code, summary = http.StatusNotFound, "files.not_found", "The requested original is missing; no retained copy was substituted."
	case errors.Is(err, workspacefiles.ErrNotRegular):
		status, code, summary = http.StatusBadRequest, "files.not_regular", "Only regular original files can be streamed."
	case errors.Is(err, workspacefiles.ErrNotDirectory):
		status, code, summary = http.StatusBadRequest, "files.not_directory", "Only a directory can be listed."
	case errors.Is(err, context.Canceled):
		status, code, summary = http.StatusRequestTimeout, "files.cancelled", "The file request was cancelled."
	}
	s.writeError(w, correlationID, status, code, "files", "original", summary, err)
}
