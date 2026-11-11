package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type nativeSession struct {
	client *JSONLClient
	lock   *os.File
	stop   func()
	done   chan struct{}
}

func startNativeSession(ctx context.Context, cfg RuntimeConfig, hint func()) (*nativeSession, error) {
	info, err := os.Stat(cfg.ReplicaDir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("Notes replica directory unavailable")
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.ReplicaDir, "--settings", cfg.Settings)
	cmd.WaitDelay = 2 * time.Second
	cmd.ExtraFiles = []*os.File{write}
	cmd.Env = append(os.Environ(), "LOOM_NOTES_WAKE_FD=3")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		read.Close()
		write.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		read.Close()
		write.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		read.Close()
		write.Close()
		return nil, errors.New("Notes native process unavailable")
	}
	write.Close()
	s := &nativeSession{client: &JSONLClient{Input: stdin, Output: stdout}, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(s.done); hint() }()
	go func() {
		defer read.Close()
		var buffer [128]byte
		for {
			n, err := read.Read(buffer[:])
			if n > 0 {
				hint()
			}
			if err != nil {
				return
			}
		}
	}()
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			stdin.Close()
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-s.done
			}
			stdout.Close()
			read.Close()
		})
	}
	return s, nil
}

func (r *Runtime) nativeHint() {
	r.nativeRevision.Add(1)
	if r.Wake != nil {
		r.Wake()
	}
}

func (r *Runtime) closeNative() {
	if r.session != nil {
		r.session.stop()
		if r.session.lock != nil {
			r.session.lock.Close()
		}
		r.session = nil
	}
}

// Retained inode inspection is a recovery sweep, not source delivery. Incoming
// operations still reconcile through Step; source hints must not rescan history.
func (r *Runtime) retainedReconciliationDue(persistent bool, after string, now time.Time) bool {
	return !persistent || after != "" || !now.Before(r.nextRetained)
}

func recoveryRequested(replica string) bool {
	_, err := os.Lstat(filepath.Join(filepath.Dir(replica), "recovery.request"))
	return !errors.Is(err, os.ErrNotExist)
}

// Recovery requests quiesce the child before releasing the existing capture
// lock. Removing a request wakes durable catch-up; no replica is copied live.
func (r *Runtime) WatchRecovery(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err = w.Add(filepath.Dir(r.Config.ReplicaDir)); err != nil {
		w.Close()
		return err
	}
	go func() {
		defer w.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				r.nativeHint()
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != "recovery.request" {
					continue
				}
				r.mu.Lock()
				if recoveryRequested(r.Config.ReplicaDir) {
					r.hintMu.Lock()
					r.closeNative()
					r.hintMu.Unlock()
				} else {
					for _, inventory := range r.inventories {
						_ = r.SourceHint(ctx, inventory.Scope, "")
					}
				}
				r.nextSafety = time.Time{}
				r.nextRetained = time.Time{}
				r.mu.Unlock()
				r.nativeHint()
			}
		}
	}()
	return nil
}

func (r *Runtime) Readiness(ctx context.Context, now time.Time) (bool, *time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ready := func(reason string) (bool, *time.Time, error) {
		r.wakeReason = reason
		return true, nil, nil
	}
	if r.DB == nil {
		return false, nil, errors.New("Notes runtime unavailable")
	}
	if recoveryRequested(r.Config.ReplicaDir) {
		r.closeNative()
		next := now.Add(time.Minute)
		return false, &next, nil
	}
	if r.Lifetime == nil || r.session == nil {
		return ready("native_startup")
	}
	select {
	case <-r.session.done:
		r.closeNative()
		return ready("native_exit")
	default:
	}
	if r.drainPending {
		return ready("durable_drain")
	}
	if r.sourceRevision.Load() != r.seenSourceRevision {
		return ready("source_hint")
	}
	if r.nativeRevision.Load() != r.seenNativeRevision {
		return ready("native_hint")
	}
	if r.dependencyRevision.Load() != r.seenDependencyRevision {
		return ready("dependency_hint")
	}
	if !now.Before(r.nextSafety) {
		return ready("safety_recheck")
	}
	next := r.nextSafety
	for _, retry := range []struct {
		at     time.Time
		reason string
	}{
		{r.nextLayout, "layout_retry"}, {r.nextArchive, "archive_retry"},
		{r.nextEnrollment, "enrollment_repair"},
	} {
		if retry.at.IsZero() {
			continue
		}
		if !now.Before(retry.at) {
			return ready(retry.reason)
		}
		if retry.at.Before(next) {
			next = retry.at
		}
	}
	if !r.nextSourceMove.IsZero() {
		if !now.Before(r.nextSourceMove) {
			return ready("source_move_retry")
		}
		if r.nextSourceMove.Before(next) {
			next = r.nextSourceMove
		}
	}
	if !r.nextRetained.IsZero() {
		if !now.Before(r.nextRetained) {
			return ready("retained_repair")
		}
		if r.nextRetained.Before(next) {
			next = r.nextRetained
		}
	}
	for _, inventory := range r.inventories {
		if inventory.Watch != nil && inventory.Watch.Healthy() != inventory.Healthy {
			return ready("watch_health")
		}
		if r.OpenSourceWatch != nil && (inventory.Watch == nil || !inventory.Watch.Healthy() && !sourceWatchObserving(inventory.Watch)) {
			if inventory.Watch != nil || !inventory.RetryAt.After(now) {
				return ready("watch_retry")
			}
			if inventory.RetryAt.Before(next) {
				next = inventory.RetryAt
			}
		}
		var raw []byte
		err := r.DB.QueryRowContext(ctx, `SELECT record FROM notes_workspace.source_inventory_generations WHERE collection_id=$1 AND generation=$2`, inventory.Scope.Collection, inventory.Scope.Generation).Scan(&raw)
		if err != nil {
			return false, nil, err
		}
		var state InventoryState
		if err = json.Unmarshal(raw, &state); err != nil {
			return false, nil, err
		}
		at := state.NextRepair
		if state.Degraded == "" && (len(state.Scans) > 0 || !state.Complete) {
			return ready("inventory_catchup:" + inventory.Scope.Collection)
		}
		if at.IsZero() || !at.After(now) {
			return ready("inventory_repair:" + inventory.Scope.Collection)
		}
		if at.Before(next) {
			next = at
		}
		var retry sql.NullTime
		err = r.DB.QueryRowContext(ctx, `SELECT min(retry_at) FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND (present OR entry_kind='directory') AND reason<>'source_excluded' AND sequence>delivered_sequence`, inventory.Scope.Collection, inventory.Scope.Generation).Scan(&retry)
		if err != nil {
			return false, nil, err
		}
		if retry.Valid && !retry.Time.After(now) {
			return ready("inventory_retry:" + inventory.Scope.Collection)
		}
		if retry.Valid && retry.Time.Before(next) {
			next = retry.Time
		}
	}
	var pending sql.NullInt64
	err := r.DB.QueryRowContext(ctx, `SELECT min(CASE WHEN (`+predecessorReadySQL+`) THEN 0 ELSE COALESCE((record->>'RetryAfterMS')::bigint,0) END) FROM notes_workspace.sync_records o WHERE replica_id=$1 AND kind='operation' AND COALESCE(record->>'AckPublished','false')<>'true'`, r.Config.Replica).Scan(&pending)
	if err != nil {
		return false, nil, err
	}
	if pending.Valid {
		at := time.UnixMilli(pending.Int64)
		if !at.After(now) {
			return ready("operation_retry")
		}
		if at.Before(next) {
			next = at
		}
	}
	return false, &next, nil
}
