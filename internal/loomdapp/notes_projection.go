package loomdapp

import (
	"context"
	"database/sql"
	"encoding/json"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
	workerruntimes "loom.local/loom/internal/workers/runtimes"
)

func registerNotesProjection(db *sql.DB, cfg config.Config, sources *knowledge.Service, reader notesprojection.SourceProvider, registry *workers.Registry) error {
	projection := notesprojection.NewService(reader, cfg.NotesProjectionRoot())
	runtime := workerruntimes.NewNotesProjectionRuntime(sources, &projection)
	runtime.LoadCheckpoint = func(ctx context.Context, instanceID string) (json.RawMessage, error) {
		var raw json.RawMessage
		err := db.QueryRowContext(ctx, `SELECT checkpoint_json FROM workers.worker_checkpoints WHERE worker_instance_id=$1 AND checkpoint_key='default'`, instanceID).Scan(&raw)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return raw, err
	}
	return registry.Register(runtime)
}
