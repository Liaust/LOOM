package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (s Store) rootHasQueuedDelivery(ctx context.Context, root string) (bool, error) {
	pending := false
	err := s.withQueueSummary(ctx, func(tx *bolt.Tx) error {
		if _, current := readQueueReport(tx); !current {
			return errors.New("queue record summary revision unavailable")
		}
		var sync LocalSyncCounts
		var backup LocalWatchedRootBackupCounts
		for _, row := range []struct {
			bucket, key string
			target      any
		}{
			{"counts", "sync/outbox\x00" + root, &sync},
			{"backup-summary", root, &backup},
		} {
			if raw := tx.Bucket([]byte(row.bucket)).Get([]byte(row.key)); raw != nil {
				if err := json.Unmarshal(raw, row.target); err != nil {
					return fmt.Errorf("queue delivery summary: %w", err)
				}
			}
		}
		pending = sync.Pending > 0 || backup.Pending > 0 || backup.Retryable > 0
		return nil
	})
	return pending, err
}

func recordDeliveryDeadline(store Store, root string, next, lastStarted time.Time, interval time.Duration) (time.Time, error) {
	wait, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	pending, err := store.rootHasQueuedDelivery(wait, root)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, bolt.ErrTimeout) {
			return minDeadline(next, time.Now().Add(time.Second)), nil
		}
		// Corrupt/incomplete summaries remain visible, not a silent healthy zero
		// or a permanent one-second retry loop. The writer owns their recovery.
		return next, err
	}
	if pending {
		return minDeadline(next, lastStarted.Add(min(interval, time.Minute))), nil
	}
	return next, nil
}
