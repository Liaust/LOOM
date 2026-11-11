package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"loom.local/loom/internal/workspacefiles"
	"net/http"
)

func (s Server) handleFileWatch(w http.ResponseWriter, r *http.Request) {
	files, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "watch", "Use GET or POST.", nil)
		return
	}
	var paths []string
	root := r.URL.Query().Get("root")
	valid := false
	if r.Method == http.MethodPost {
		var input struct {
			Root  string   `json:"root"`
			Paths []string `json:"paths"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) == nil && decoder.Decode(&struct{}{}) == io.EOF {
			root, paths, valid = input.Root, input.Paths, true
		}
	} else {
		valid = len(r.URL.Query().Get("paths")) <= 65536 && json.Unmarshal([]byte(r.URL.Query().Get("paths")), &paths) == nil && len(paths) <= 64
	}
	if !valid {
		s.writeFileError(w, cid, workspacefiles.ErrInvalidPath)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, cid, 503, "files.watch_unavailable", "files", "watch", "Streaming is unavailable.", nil)
		return
	}
	started := false
	err := files.Watch(r.Context(), root, paths, func(changed []string, initial bool) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			started = true
		}
		kind := "changed"
		if initial {
			kind = "ready"
		}
		raw, _ := json.Marshal(map[string]any{"type": kind, "paths": changed})
		_, e := fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
		return e
	})
	if err != nil && !started {
		s.writeFileError(w, cid, err)
	}
}
