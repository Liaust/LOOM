package workers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const workerWakeChannel = "loom_worker_wake"

// PostgreSQL delivers this hint only after the owner's transaction commits.
// Durable queues, policy and existing claims remain the execution authority.
func NotifyQueueTx(ctx context.Context, tx *sql.Tx, kind string) error {
	if !workerKindPattern.MatchString(kind) {
		return fmt.Errorf("%w: invalid queue wake kind", ErrInvalid)
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_notify('loom_worker_wake',$1)`, kind)
	return err
}

type QueueWakes struct {
	NodeDeliveryQueued      func(string)
	NotesWorkspaceQueued    func()
	NotesDependencyChanged  func([]byte)
	jobs, notes, background chan struct{}
	lanes                   map[string]chan struct{}
	mu                      sync.Mutex
	pending                 map[string]map[string]bool
}

func NewQueueWakes() *QueueWakes {
	w := &QueueWakes{lanes: map[string]chan struct{}{}, pending: map[string]map[string]bool{}}
	for _, lane := range supervisorLanes {
		w.lanes[lane] = make(chan struct{}, 1)
	}
	w.jobs, w.notes, w.background = w.lanes["jobs"], w.lanes["notes"], w.lanes["background"]
	return w
}
func (w *QueueWakes) channel(lane string) <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.lanes[lane]
}
func (w *QueueWakes) signal(kind string) {
	if w == nil {
		return
	}
	w.signalLane(workerLane(kind), kind)
}

func (w *QueueWakes) signalLane(lane, kind string) {
	ch := w.lanes[lane]
	w.mu.Lock()
	if w.pending[lane] == nil {
		w.pending[lane] = map[string]bool{}
	}
	w.pending[lane][kind] = true
	w.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (w *QueueWakes) take(lane string) map[string]bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	kinds := w.pending[lane]
	delete(w.pending, lane)
	return kinds
}

// SignalLocal coalesces a same-process committed/file-change hint. Durable
// readiness and worker policy remain the authority to run.
func (w *QueueWakes) SignalLocal(kind string) {
	if workerKindPattern.MatchString(kind) {
		w.signal(kind)
	}
}
func (w *QueueWakes) catchUp() {
	if w.NotesDependencyChanged != nil {
		w.NotesDependencyChanged(nil)
	}
	for _, lane := range supervisorLanes {
		w.signalLane(lane, "*")
	}
}

// One dedicated connection, never a held transaction or a pool slot. Listener
// registration precedes catch-up; a disconnect falls back to deadline/safety reads.
func (w *QueueWakes) Listen(ctx context.Context, dbURL string, logger *slog.Logger) {
	backoff := time.Second
	for ctx.Err() == nil {
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := pgx.Connect(connectCtx, dbURL)
		if err == nil {
			_, err = conn.Exec(connectCtx, "SET application_name = 'loom-worker-wake'; LISTEN "+workerWakeChannel+"; LISTEN loom_node_delivery; LISTEN loom_notes_dependency")
		}
		cancel()
		if err == nil {
			backoff = time.Second
			w.catchUp()
			if logger != nil {
				logger.Info("worker queue listener connected", "component", "workers", "fallback_seconds", 60)
			}
			for ctx.Err() == nil {
				notification, waitErr := conn.WaitForNotification(ctx)
				if waitErr != nil {
					break
				}
				if notification.Channel == workerWakeChannel && workerKindPattern.MatchString(notification.Payload) {
					if notification.Payload == "notes_workspace_sync" && w.NotesWorkspaceQueued != nil {
						w.NotesWorkspaceQueued()
					}
					w.signal(notification.Payload)
				}
				if notification.Channel == "loom_node_delivery" && w.NodeDeliveryQueued != nil {
					w.NodeDeliveryQueued(notification.Payload)
				}
				if notification.Channel == "loom_notes_dependency" && w.NotesDependencyChanged != nil {
					w.NotesDependencyChanged([]byte(notification.Payload))
				}
			}
		}
		if conn != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Close(closeCtx)
			cancel()
		}
		if ctx.Err() != nil {
			return
		}
		// Connection errors may contain credentials; expose posture, not raw URLs.
		if logger != nil {
			logger.Warn("worker queue listener unavailable; deadline fallback active", "component", "workers", "retry_seconds", backoff.Seconds())
		}
		w.catchUp()
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(60*time.Second, backoff*2)
	}
}
