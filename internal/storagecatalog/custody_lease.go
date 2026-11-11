package storagecatalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"time"
)

func AcquireWorkspaceCustodyReader(ctx context.Context, db *sql.DB) (func(), error) {
	conn, release, err := acquireWorkspaceCustodySession(ctx, db, true)
	if err != nil {
		return nil, err
	}
	var prepared bool
	err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM storage.retained_relocations WHERE state='prepared')`).Scan(&prepared)
	if err != nil || prepared {
		release()
		if err == nil {
			err = fmt.Errorf("retained relocation requires exact recovery before a custody read or capture")
		}
		return nil, err
	}
	return release, err
}
func acquireWorkspaceCustodySession(ctx context.Context, db *sql.DB, shared bool) (*sql.Conn, func(), error) {
	if db == nil {
		return nil, nil, fmt.Errorf("custody database unavailable")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	unlock := `SELECT pg_advisory_unlock(hashtextextended('storage.workspace_custody',0))`
	if shared {
		unlock = `SELECT pg_advisory_unlock_shared(hashtextextended('storage.workspace_custody',0))`
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			defer conn.Close()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, e := conn.ExecContext(cleanup, unlock); e != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		})
	}
	if shared {
		_, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock_shared(hashtextextended('storage.workspace_custody',0))`)
	} else {
		var available bool
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended('storage.workspace_custody',0))`).Scan(&available)
		if err == nil && !available {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("custody readers or another move are active; no relocation was started")
		}
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return conn, release, nil
}
