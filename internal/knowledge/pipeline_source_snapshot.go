package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/config"
)

// A snapshot is private execution state. The object retains its original citation
// path; only readers receive the selected, verified input, never the latest file.
type pipelineSourceSnapshot struct {
	Object      KnowledgeObject     `json:"object"`
	Input       syncedSourceBinding `json:"input"`
	Temporary   bool                `json:"temporary,omitempty"`
	StagingRoot string              `json:"staging_root,omitempty"`
	RunID       string              `json:"-"`
}

func capturedPipelineObject(run PipelineRun, fallback KnowledgeObject) (KnowledgeObject, error) {
	if len(run.SourceSnapshot) == 0 {
		return fallback, nil
	}
	var selected pipelineSourceSnapshot
	if err := json.Unmarshal(run.SourceSnapshot, &selected); err != nil {
		return KnowledgeObject{}, err
	}
	if selected.Object.KnowledgeObjectID != run.KnowledgeObjectID || selected.Object.SourceHash != run.SourceHash || selected.Object.SourceRevision != run.SourceRevision {
		return KnowledgeObject{}, fmt.Errorf("%w: invalid selected source identity", ErrConflict)
	}
	selected.RunID = run.KnowledgePipelineRunID
	object := selected.Object
	object.pipelineSource = &selected
	return object, nil
}

func pipelineAdmissionMatches(selected, current KnowledgeObject) bool {
	if selected.NotesSourceRootID != current.NotesSourceRootID || selected.SourcePath != current.SourcePath ||
		selected.RelativePath != current.RelativePath || selected.SourceNodeKey != current.SourceNodeKey ||
		!reflect.DeepEqual(selected.ProjectID, current.ProjectID) || !reflect.DeepEqual(selected.SourceNodeID, current.SourceNodeID) {
		return false
	}
	a, ea := knowledgeSourcePolicy(selected)
	b, eb := knowledgeSourcePolicy(current)
	if ea != nil || eb != nil {
		return false
	}
	if a != nil {
		a.Refresh = nil
		if a.Processing == nil {
			a = nil
		}
	}
	if b != nil {
		b.Refresh = nil
		if b.Processing == nil {
			b = nil
		}
	}
	return reflect.DeepEqual(a, b)
}

func (s *Service) capturePipelineSource(ctx context.Context, item PipelineWorkItem) (PipelineWorkItem, error) {
	if item.Object.pipelineSource != nil {
		return item, nil
	}
	selected := pipelineSourceSnapshot{Object: item.Object}
	cleanup := func() {}
	keep := false
	defer func() {
		if !keep {
			cleanup()
		}
	}()
	if pipelineFileFamily(item.Object) != "metadata_only" {
		if isSyncedKnowledgeObject(item.Object) {
			binding, err := s.resolveSyncedSource(ctx, item.Object)
			if err != nil {
				return item, err
			}
			selected.Input = binding
		} else {
			prepared, release, err := s.preparePathBackedExtractionObject(ctx, item.Object, DefaultPDFMaxSourceBytes)
			if err != nil {
				return item, err
			}
			defer release()
			root := s.sourceStagingRoot
			if root == "" {
				cfg, e := config.Load(config.Overrides{})
				if e != nil {
					return item, e
				}
				root = filepath.Join(cfg.DataDir, "notes-inputs")
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return item, err
			}
			if err = os.MkdirAll(root, 0700); err != nil {
				return item, err
			}
			info, e := os.Lstat(root)
			if e != nil {
				return item, e
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return item, fmt.Errorf("%w: invalid Notes input staging directory", ErrConflict)
			}
			staged, e := os.CreateTemp(root, "loom-knowledge-source-*")
			if e != nil {
				return item, e
			}
			path := staged.Name()
			remove := func() { _ = os.Remove(path) }
			err = staged.Close()
			if err != nil {
				remove()
				return item, err
			}
			cleanup = remove
			file, err := os.OpenFile(path, os.O_WRONLY|unix.O_NOFOLLOW, 0600)
			if err != nil {
				return item, err
			}
			if item.Object.SizeBytes == nil {
				_ = file.Close()
				return item, fmt.Errorf("%w: selected source size is unknown", ErrConflict)
			}
			binding := syncedSourceBinding{Path: prepared.SourcePath, Hash: item.Object.SourceHash, Size: *item.Object.SizeBytes}
			err = copyVerifiedSyncedBlob(ctx, binding, DefaultPDFMaxSourceBytes, file)
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err != nil {
				return item, err
			}
			if closeErr != nil {
				return item, closeErr
			}
			directory, err := os.Open(root)
			if err != nil {
				return item, err
			}
			err = directory.Sync()
			_ = directory.Close()
			if err != nil {
				return item, err
			}
			binding.Path = path
			selected.Input, selected.Temporary = binding, true
			selected.StagingRoot = root
		}
	}
	payload, err := json.Marshal(selected)
	if err != nil {
		return item, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return item, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET source_snapshot=$2
		WHERE knowledge_pipeline_run_id=$1 AND source_snapshot IS NULL`, item.Run.KnowledgePipelineRunID, payload)
	if err != nil {
		return item, err
	}
	if err = requireOneRow(result, "source was already selected"); err != nil {
		return item, err
	}
	if err = tx.Commit(); err != nil {
		return item, err
	}
	keep = true
	item.Run.SourceSnapshot = payload
	item.Object, err = capturedPipelineObject(item.Run, item.Object)
	return item, err
}

func (s *Service) selectedPipelineBinding(ctx context.Context, object KnowledgeObject) (syncedSourceBinding, error) {
	selected := object.pipelineSource
	if selected == nil {
		return syncedSourceBinding{}, fmt.Errorf("%w: no selected source", ErrConflict)
	}
	run, err := s.store.GetPipelineRun(ctx, selected.RunID)
	if err != nil {
		return syncedSourceBinding{}, err
	}
	if run.Status != FilePipelineStatusProcessing {
		return syncedSourceBinding{}, fmt.Errorf("%w: selected run is not processing", ErrConflict)
	}
	bound, err := capturedPipelineObject(run, KnowledgeObject{})
	if err != nil {
		return syncedSourceBinding{}, err
	}
	if bound.pipelineSource == nil || bound.pipelineSource.Input != selected.Input {
		return syncedSourceBinding{}, fmt.Errorf("%w: selected source binding changed", ErrConflict)
	}
	current, err := s.store.GetKnowledgeObject(ctx, object.KnowledgeObjectID)
	if err != nil {
		return syncedSourceBinding{}, err
	}
	var admitted bool
	err = s.store.db.QueryRowContext(ctx, `SELECT `+visibleNotesKnowledgeObjectSQL("o")+` AND `+notesCustodyWriteAllowedSQL("o")+`
		FROM knowledge.knowledge_objects o WHERE o.knowledge_object_id=$1 AND o.deleted_at IS NULL`, object.KnowledgeObjectID).Scan(&admitted)
	if err != nil {
		return syncedSourceBinding{}, err
	}
	if !admitted || !pipelineAdmissionMatches(bound, current) {
		return syncedSourceBinding{}, fmt.Errorf("%w: selected source admission changed", ErrConflict)
	}
	return selected.Input, nil
}

func (s *Service) readSelectedPipelineSource(ctx context.Context, object KnowledgeObject, maxBytes int64, dst io.Writer) error {
	binding, err := s.selectedPipelineBinding(ctx, object)
	if err != nil {
		return err
	}
	if err = copyVerifiedSyncedBlob(ctx, binding, maxBytes, dst); err != nil {
		return err
	}
	_, err = s.selectedPipelineBinding(ctx, object)
	return err
}

func (s *Service) prepareSelectedPipelineSource(ctx context.Context, object KnowledgeObject, maxBytes int64) (KnowledgeObject, func(), error) {
	path, cleanup, err := writeExtractionTempFile(object, nil)
	if err != nil {
		return object, func() {}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if err == nil {
		err = s.readSelectedPipelineSource(ctx, object, maxBytes, file)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		cleanup()
		return object, func() {}, err
	}
	object.SourcePath = path
	return object, cleanup, nil
}

func cleanupSelectedPipelineInput(run PipelineRun) {
	var selected pipelineSourceSnapshot
	if json.Unmarshal(run.SourceSnapshot, &selected) != nil || !selected.Temporary {
		return
	}
	path := selected.Input.Path
	if selected.StagingRoot == "" || filepath.Dir(path) != selected.StagingRoot || !strings.HasPrefix(filepath.Base(path), "loom-knowledge-source-") {
		return
	}
	// Delete only a still-verifiable run-owned temporary input, never an Objects blob.
	if copyVerifiedSyncedBlob(context.Background(), selected.Input, DefaultPDFMaxSourceBytes, io.Discard) == nil {
		_ = os.Remove(path)
	}
}

func (s *Service) cleanupSupersededPipelineInputs(ctx context.Context, objectID string) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT source_snapshot FROM knowledge.pipeline_runs
	 WHERE knowledge_object_id=$1 AND status IN ('stale','cancelled') AND source_snapshot->>'temporary'='true'
	 ORDER BY generation DESC LIMIT 100`, objectID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var run PipelineRun
		if rows.Scan(&run.SourceSnapshot) == nil {
			cleanupSelectedPipelineInput(run)
		}
	}
}

func selectedPipelineRunTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, snapshot json.RawMessage) (PipelineRun, bool, error) {
	run, err := scanPipelineRun(tx.QueryRowContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs
		WHERE knowledge_object_id=$1 AND started_at IS NOT NULL AND status IN
		('processing','queued','waiting_coordinator','waiting_heavy','waiting_quiet_window')
		ORDER BY generation DESC LIMIT 1`, object.KnowledgeObjectID))
	if err == sql.ErrNoRows {
		return PipelineRun{}, false, nil
	}
	if err != nil {
		return PipelineRun{}, false, err
	}
	if !pipelineExecutionPlansEqual(run.PlanSnapshot, snapshot) {
		return run, false, nil
	}
	if len(run.SourceSnapshot) == 0 {
		return run, run.SourceHash == object.SourceHash && run.SourceRevision == object.SourceRevision, nil
	}
	selected, err := capturedPipelineObject(run, KnowledgeObject{})
	return run, err == nil && pipelineAdmissionMatches(selected, object), err
}

func pipelineExecutionPlansEqual(a, b json.RawMessage) bool {
	var x, y CompiledPipelinePlan
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	for _, p := range []*CompiledPipelinePlan{&x, &y} {
		p.HeavyQuietWindowSeconds = 0
		p.SourcePolicy = nil
		p.EffectivePolicy = nil
		for i := range p.Stages {
			p.Stages[i].QuietWindow = false
			p.Stages[i].SkipReason = ""
		}
	}
	return reflect.DeepEqual(x, y)
}
