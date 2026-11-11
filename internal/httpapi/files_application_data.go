package httpapi

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

func (s Server) handleApplicationFiles(w http.ResponseWriter, r *http.Request) {
	files, id, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	cursor, limit := 0, 100
	var e error
	if q.Has("cursor") {
		cursor, e = strconv.Atoi(q.Get("cursor"))
		if e != nil {
			s.writeFileError(w, id, workspacefiles.ErrInvalidCursor)
			return
		}
	}
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil {
			s.writeFileError(w, id, workspacefiles.ErrInvalidPath)
			return
		}
	}
	x, e := files.ApplicationRoots(r.Context(), q.Get("project_id"), cursor, limit)
	if e != nil {
		s.writeFileError(w, id, e)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(id, x))
}

func (s Server) handleArchiveMembers(w http.ResponseWriter, r *http.Request) {
	files, id, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if !workspacefiles.IsApplicationRoot(q.Get("root")) {
		s.writeFileError(w, id, workspacefiles.ErrUnknownRoot)
		return
	}
	file, metadata, e := files.Open(workspacefiles.WithApplicationView(r.Context(), q.Get("view_revision")), q.Get("root"), q.Get("path"))
	if e != nil {
		s.writeFileError(w, id, e)
		return
	}
	defer file.Close()
	members, e := workspacefiles.ListArchiveMembers(r.Context(), file)
	if e != nil {
		s.writeError(w, id, http.StatusUnprocessableEntity, "files.archive_unsupported", "files", "archive-members", "This archive is invalid or exceeds safe preview limits. The original was not changed.", nil)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(id, struct {
		Archive workspacefiles.Entry           `json:"archive"`
		Members []workspacefiles.ArchiveMember `json:"members"`
	}{metadata, members}))
}

var applicationArchiveReaders = make(chan struct{}, 2)

func (s Server) handleArchiveMemberContent(w http.ResponseWriter, r *http.Request) {
	files, id, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	select {
	case applicationArchiveReaders <- struct{}{}:
		defer func() { <-applicationArchiveReaders }()
	case <-r.Context().Done():
		return
	}
	q := r.URL.Query()
	if !workspacefiles.IsApplicationRoot(q.Get("root")) {
		s.writeFileError(w, id, workspacefiles.ErrUnknownRoot)
		return
	}
	file, metadata, e := files.Open(workspacefiles.WithApplicationView(r.Context(), q.Get("view_revision")), q.Get("root"), q.Get("path"))
	if e != nil {
		s.writeFileError(w, id, e)
		return
	}
	defer file.Close()
	if q.Get("revision") == "" || metadata.Revision != q.Get("revision") {
		s.writeFileError(w, id, workspacefiles.ErrRootChanged)
		return
	}
	member, info, e := workspacefiles.OpenArchiveMember(r.Context(), file, q.Get("member"))
	if e != nil {
		s.writeError(w, id, http.StatusUnprocessableEntity, "files.archive_unsupported", "files", "archive-content", "This member is unavailable or exceeds safe preview limits.", nil)
		return
	}
	defer member.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("X-LOOM-CRC32", strconv.FormatUint(uint64(info.CRC32), 10))
	w.Header().Set("ETag", strings.TrimSuffix(metadata.Revision, `"`)+"-"+info.ID+`"`)
	if _, e = io.Copy(w, member); e != nil {
		panic(http.ErrAbortHandler)
	}
}
