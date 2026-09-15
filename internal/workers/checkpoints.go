package workers

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"loom.local/loom/internal/ids"
)

var checkpointKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,127}$`)

func applyCheckpointUpdates(ctx context.Context, tx *sql.Tx, run WorkerRun, lease WorkerLease, updates []CheckpointUpdate) ([]WorkerCheckpoint, error) {
	if len(updates) == 0 {
		return nil, nil
	}
	if err := ensureLeaseFence(ctx, tx, lease); err != nil {
		return nil, err
	}

	applied := make([]WorkerCheckpoint, 0, len(updates))
	for _, update := range updates {
		update.Key = strings.TrimSpace(update.Key)
		if update.Key == "" {
			return nil, fmt.Errorf("%w: checkpoint key is required", ErrInvalid)
		}
		if !checkpointKeyPattern.MatchString(update.Key) {
			return nil, fmt.Errorf("%w: checkpoint key %q is invalid", ErrInvalid, update.Key)
		}
		value, err := normalizeJSONObject(update.Value, "checkpoint_json")
		if err != nil {
			return nil, err
		}
		metadata, err := normalizeJSONObject(update.Metadata, "metadata")
		if err != nil {
			return nil, err
		}

		checkpoint, err := scanWorkerCheckpoint(tx.QueryRowContext(ctx, `
			INSERT INTO workers.worker_checkpoints (
				worker_checkpoint_id, worker_instance_id, checkpoint_key,
				checkpoint_json, schema_version, updated_by_run_id, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (worker_instance_id, checkpoint_key)
			DO UPDATE SET checkpoint_json = EXCLUDED.checkpoint_json,
			              schema_version = EXCLUDED.schema_version,
			              updated_by_run_id = EXCLUDED.updated_by_run_id,
			              updated_at = now(),
			              metadata = EXCLUDED.metadata
			RETURNING worker_checkpoint_id, worker_instance_id, checkpoint_key,
			          checkpoint_json, schema_version, updated_by_run_id, updated_at, metadata
		`,
			ids.NewWorkerCheckpointID(),
			run.WorkerInstanceID,
			update.Key,
			[]byte(value),
			strings.TrimSpace(update.SchemaVersion),
			run.WorkerRunID,
			[]byte(metadata),
		))
		if err != nil {
			return nil, err
		}
		applied = append(applied, checkpoint)
	}
	return applied, nil
}
