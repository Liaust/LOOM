package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workspaceactions"
	"loom.local/loom/internal/workspacefiles"
)

func (s Server) handleFileSnapshot(w http.ResponseWriter, r *http.Request) {
	files, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "snapshot", "Use POST.", nil)
		return
	}
	var input struct {
		Root     string `json:"root"`
		Path     string `json:"path"`
		Revision string `json:"revision"`
		Action   string `json:"action"`
		PlanHash string `json:"plan_hash"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || input.Revision == "" || (input.Action != "plan" && input.Action != "apply") || workspaceactions.Managed(input.Root, input.Path) {
		s.writeError(w, cid, 400, "files.snapshot_invalid", "files", "snapshot", "Choose an exact named content folder and its current revision.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	if s.services.StorageCatalog.DB != nil {
		release, err := storagecatalog.AcquireWorkspaceCustodyReader(ctx, s.services.StorageCatalog.DB)
		if err != nil {
			s.writeFileError(w, cid, err)
			return
		}
		defer release()
	}
	temporary, err := os.CreateTemp("", "loom-directory-snapshot-*.tar")
	if err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	defer func() { temporary.Close(); os.Remove(temporary.Name()) }()
	var summary projectexport.Summary
	var planHash string
	err = files.WithSnapshotDirectory(ctx, input.Root, input.Path, func(directory string, info workspacefiles.Entry) error {
		if info.Revision != input.Revision {
			return fmt.Errorf("source revision changed; review the copy again")
		}
		plan, err := projectexport.PlanDirectory(ctx, directory)
		if err != nil {
			return err
		}
		defer plan.Close()
		planHash = plan.DirectoryDigest(info.Revision)
		if input.Action == "plan" {
			summary = plan.Summary
			return plan.CheckDirectory(ctx)
		}
		if input.PlanHash != planHash {
			return fmt.Errorf("source inventory changed; review the copy again")
		}
		summary, err = projectexport.WriteArchive(ctx, temporary, plan)
		if err != nil {
			return err
		}
		return plan.CheckDirectory(ctx)
	})
	if err != nil {
		s.writeError(w, cid, 409, "files.snapshot_held", "files", input.Path, "Snapshot could not be verified. Original files are unchanged.", err)
		return
	}
	summary.SourceRoot, summary.SourcePath, summary.SourceRevision = input.Root, input.Path, input.Revision
	if input.Action == "plan" {
		response.WriteJSON(w, 200, response.Success(cid, map[string]any{"plan_hash": planHash, "summary": summary, "qualification": "Policy-filtered standalone copy, not a backup, sync selection or registered project. Links and special files are refused. Up to 10,000 entries and 512 MiB."}))
		return
	}
	if err = temporary.Sync(); err == nil {
		_, err = temporary.Seek(0, io.SeekStart)
	}
	if err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", fmt.Sprint(summary.ArchiveBytes))
	w.Header().Set(projectexport.SummaryHeader, base64.RawURLEncoding.EncodeToString(raw))
	w.WriteHeader(200)
	_, _ = io.Copy(w, temporary)
}
