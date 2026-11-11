package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

func verifyCommittedDependencyHints(t *testing.T, db *sql.DB, fileID string) {
	t.Helper()
	ctx := t.Context()
	listener, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := listener.ExecContext(ctx, "LISTEN loom_notes_dependency"); err != nil {
		t.Fatal(err)
	}
	wait := func(wantKind, wantID string) {
		t.Helper()
		err := listener.Raw(func(driver any) error {
			waitCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			defer cancel()
			n, err := driver.(*stdlib.Conn).Conn().WaitForNotification(waitCtx)
			if wantKind == "" && errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if err != nil {
				return err
			}
			var payload struct{ Kind, ID string }
			if err := json.Unmarshal([]byte(n.Payload), &payload); err != nil {
				return err
			}
			if wantKind == "" || payload.Kind != wantKind || payload.ID != wantID {
				t.Fatalf("unexpected dependency notification: %+v", payload)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE notes_workspace.files SET record=record WHERE file_id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	wait("", "")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes_workspace.files SET record=record || '{"path_version":99}' WHERE file_id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	wait("", "")
	if _, err := db.ExecContext(ctx, `UPDATE notes_workspace.files SET record=record || '{"path_version":99}' WHERE file_id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	wait("source", fileID)
	if _, err := db.ExecContext(ctx, `INSERT INTO notes_workspace.sync_records(replica_id,kind,record_id,record) VALUES('replica','cursor','dependency-test','{}')`); err != nil {
		t.Fatal(err)
	}
	wait("", "")
	if _, err := db.ExecContext(ctx, `INSERT INTO notes_workspace.sync_records(replica_id,kind,record_id,record) VALUES('replica','binding','missing-receipt','{}')`); err != nil {
		t.Fatal(err)
	}
	wait("binding", "missing-receipt")
}
