package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

type fileAnnotation struct {
	Path       string                      `json:"path"`
	FileID     string                      `json:"file_id,omitempty"`
	ObjectID   string                      `json:"object_id,omitempty"`
	Index      string                      `json:"index"`
	Embeddings string                      `json:"embeddings"`
	OCR        string                      `json:"ocr"`
	Vision     string                      `json:"vision"`
	Freshness  string                      `json:"freshness"`
	Binding    *notesworkspacesync.Binding `json:"binding,omitempty"`
}

func (s Server) handleFileAnnotations(w http.ResponseWriter, r *http.Request) {
	files, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	var paths []string
	if len(r.URL.Query().Get("paths")) > 65536 || json.Unmarshal([]byte(r.URL.Query().Get("paths")), &paths) != nil || len(paths) == 0 || len(paths) > 100 {
		s.writeError(w, correlationID, http.StatusBadRequest, "files.invalid_annotation_batch", "files", "paths", "Supply 1 to 100 exact visible paths, at most 64 KiB.", nil)
		return
	}
	type source struct {
		Path string `json:"path"`
	}
	sources := []source{}
	entries := map[string]workspacefiles.Entry{}
	byAbsolute := map[string]int{}
	results := []fileAnnotation{}
	for _, path := range paths {
		absolute, metadata, err := files.SourcePath(r.Context(), r.URL.Query().Get("root"), path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				results = append(results, fileAnnotation{Path: path, Index: "unknown", Embeddings: "unknown", OCR: "unknown", Vision: "unknown", Freshness: "source_missing"})
				continue
			}
			s.writeFileError(w, correlationID, err)
			return
		}
		if _, exists := byAbsolute[absolute]; exists {
			continue
		}
		byAbsolute[absolute] = len(results)
		sources = append(sources, source{absolute})
		entries[absolute] = metadata
		results = append(results, fileAnnotation{Path: path, Index: "unknown", Embeddings: "unknown", OCR: "unknown", Vision: "unknown", Freshness: "not_observed"})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	state := "available"
	if s.services.DB == nil {
		state = "unavailable"
	} else {
		raw, _ := json.Marshal(sources)
		rows, err := s.services.DB.QueryContext(ctx, `SELECT p.path,COALESCE(r.notes_source_root_id,''),COALESCE(r.relative,''),
 COALESCE(o.knowledge_object_id,''),COALESCE(o.canonical_file_id,''),COALESCE(o.lexical_version_id,''),COALESCE(o.semantic_version_id,''),
 COALESCE(run.knowledge_object_version_id,''),COALESCE(run.source_hash,''),COALESCE(o.source_hash,''),o.source_modified_at,o.size_bytes,
 COALESCE(stages.states,'{}'::jsonb)
 FROM jsonb_to_recordset($1::jsonb) AS p(path text)
 LEFT JOIN LATERAL (SELECT root.notes_source_root_id,substring(p.path FROM length(root.source_path)+2) AS relative
 FROM knowledge.notes_source_roots root WHERE root.node_key=$2 AND root.status='active'
 AND root.source_path<>'' AND left(p.path,length(root.source_path)+1)=root.source_path||'/'
 ORDER BY length(root.source_path) DESC,root.notes_source_root_id LIMIT 1) r ON true
 LEFT JOIN knowledge.knowledge_objects o ON o.notes_source_root_id=r.notes_source_root_id AND o.relative_path=r.relative AND o.deleted_at IS NULL
 LEFT JOIN LATERAL (SELECT pr.knowledge_pipeline_run_id,pr.knowledge_object_version_id,pr.source_hash
 FROM knowledge.pipeline_runs pr WHERE pr.knowledge_object_id=o.knowledge_object_id ORDER BY pr.generation DESC LIMIT 1) run ON true
 LEFT JOIN LATERAL (SELECT jsonb_object_agg(sr.stage_key,sr.status) AS states FROM knowledge.pipeline_stage_runs sr
 WHERE sr.knowledge_pipeline_run_id=run.knowledge_pipeline_run_id AND sr.stage_key IN ('pdf_ocr','image_ocr','image_description','lexical_index','embedding')) stages ON true`, raw, s.services.RuntimeConfig.NodeID)
		lookups := []notesworkspacesync.WorkspaceLookup{}
		lookupTargets := map[string]int{}
		if err != nil {
			state = "unavailable"
		} else {
			for rows.Next() {
				var absolute, collection, relative, object, file, lexical, semantic, version, runHash, sourceHash string
				var modified sql.NullTime
				var size sql.NullInt64
				var stagesRaw []byte
				if err = rows.Scan(&absolute, &collection, &relative, &object, &file, &lexical, &semantic, &version, &runHash, &sourceHash, &modified, &size, &stagesRaw); err != nil {
					break
				}
				index := byAbsolute[absolute]
				a := &results[index]
				a.ObjectID, a.FileID = object, file
				stages := map[string]string{}
				_ = json.Unmarshal(stagesRaw, &stages)
				if object != "" {
					a.Freshness = "metadata_not_content_verified"
					metadata := entries[absolute]
					stale := modified.Valid && !modified.Time.Truncate(time.Microsecond).Equal(metadata.ModifiedAt.Truncate(time.Microsecond)) || size.Valid && size.Int64 != metadata.Size
					current := sourceHash != "" && runHash == sourceHash && !stale
					a.Index = annotationStage(stages["lexical_index"])
					a.Embeddings = annotationStage(stages["embedding"])
					a.OCR = annotationStage(stages["pdf_ocr"])
					if stages["image_ocr"] != "" {
						a.OCR = annotationStage(stages["image_ocr"])
					}
					a.Vision = annotationStage(stages["image_description"])
					if lexical != "" {
						a.Index = "stale"
						if current && lexical == version {
							a.Index = "completed"
						}
					}
					if semantic != "" {
						a.Embeddings = "stale"
						if current && semantic == version {
							a.Embeddings = "completed"
						}
					}
					if stale || runHash != sourceHash {
						a.Freshness = "source_changed"
						if a.OCR == "completed" {
							a.OCR = "stale"
						}
						if a.Vision == "completed" {
							a.Vision = "stale"
						}
					}
				}
				if collection != "" {
					lookups = append(lookups, notesworkspacesync.WorkspaceLookup{Collection: collection, Relative: relative})
					lookupTargets[notesworkspacesync.LookupKey(collection, relative)] = index
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				state = "unavailable"
			}
		}
		if state == "available" && s.services.RuntimeConfig.NotesWorkspaceConfig != "" {
			config, err := notesworkspacesync.LoadRuntimeConfig(s.services.RuntimeConfig.NotesWorkspaceConfig)
			if err == nil {
				runtime := notesworkspacesync.Runtime{DB: s.services.DB, Config: config, NodeKey: s.services.RuntimeConfig.NodeID}
				bindings, lookupErr := runtime.LookupWorkspaceFiles(ctx, lookups)
				if lookupErr == nil {
					for k, binding := range bindings {
						b := binding
						results[lookupTargets[k]].Binding = &b
					}
				}
			}
		}
	}
	if state != "available" {
		for i := range results {
			results[i].Index, results[i].Embeddings, results[i].OCR, results[i].Vision = "unknown", "unknown", "unknown", "unknown"
			results[i].Binding = nil
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, struct {
		State      string           `json:"state"`
		ObservedAt time.Time        `json:"observed_at"`
		Entries    []fileAnnotation `json:"entries"`
	}{state, time.Now().UTC(), results}))
}

func annotationStage(status string) string {
	switch status {
	case "complete", "completed", "succeeded":
		return "completed"
	case "failed", "blocked", "running", "queued", "pending", "waiting_retry":
		return status
	case "skipped":
		return "not_requested"
	case "":
		return "unknown"
	default:
		return status
	}
}
