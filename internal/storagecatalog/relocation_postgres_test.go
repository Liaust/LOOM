package storagecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/migrations"
)

func TestRetainedRelocationPostgres(t *testing.T) {
	dsn := os.Getenv("LOOM_RELOCATION_TEST_DB_URL")
	if dsn == "" {
		t.Skip("explicit disposable PostgreSQL database required")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "loom_acceptance_") {
		t.Fatal("refusing non-acceptance database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS storage`); err != nil {
		t.Fatal(err)
	}
	schema := os.Getenv("LOOM_RELOCATION_SCHEMA_FILE")
	if schema == "" {
		schema = filepath.Join("..", "..", "migrations", "00108_retained_content_locations.sql")
	}
	payload, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "00108_retained_content_locations.sql"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	cancellation := os.Getenv("LOOM_RELOCATION_CANCELLATION_SCHEMA_FILE")
	if cancellation == "" {
		cancellation = filepath.Join("..", "..", "migrations", "00109_retained_relocation_cancellation.sql")
	}
	payload, err = os.ReadFile(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "00109_retained_relocation_cancellation.sql"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = migrations.Up(ctx, dsn, dir); err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	suffix := fmt.Sprint(time.Now().UnixNano())
	r := RetainedRelocation{ID: "relocation_" + suffix, CustodyID: "workspace:test_" + suffix, Original: "/storage/imports/paper", Source: "/storage/imports/paper", Destination: "/storage/Projects/paper-" + suffix, Digest: "sha256:" + strings.Repeat("a", 64), Plan: json.RawMessage(`{"preserved":"original proof"}`)}
	if err = service.SaveRetainedRelocation(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = service.SaveRetainedRelocation(ctx, r); err != nil {
		t.Fatalf("same plan replay: %v", err)
	}
	drift := r
	drift.Plan = json.RawMessage(`{"changed":true}`)
	if service.SaveRetainedRelocation(ctx, drift) == nil {
		t.Fatal("changed plan admitted")
	}
	releaseReader, err := AcquireWorkspaceCustodyReader(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyRetainedRelocation(ctx, r.ID, r.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
		t.Fatal("move overlapped an active backup reader")
		return RebindPathsInput{}, nil, nil
	})
	releaseReader()
	if err == nil {
		t.Fatal("active reader admitted relocation")
	}
	before, err := service.RetainedRelocation(ctx, r.ID)
	if err != nil || before.State != "planned" {
		t.Fatalf("busy reader changed durable state: %+v %v", before, err)
	}
	if _, err = service.ApplyRetainedRelocation(ctx, r.ID, r.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
		return RebindPathsInput{}, nil, errors.New("interrupted after rename")
	}); err == nil {
		t.Fatal("interruption lost")
	}
	saved, err := service.RetainedRelocation(ctx, r.ID)
	if err != nil || saved.State != "prepared" {
		t.Fatalf("durable intent: %+v %v", saved, err)
	}
	if _, err = service.ResolveCustodyPath(ctx, r.CustodyID, r.Original+"/note.md"); err == nil {
		t.Fatal("uncertain source was readable")
	}
	if release, e := AcquireWorkspaceCustodyReader(ctx, db); e == nil {
		release()
		t.Fatal("backup reader entered an externally prepared relocation")
	}
	other := r
	other.ID += "_other"
	other.Destination += "_other"
	if err = service.SaveRetainedRelocation(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApplyRetainedRelocation(ctx, other.ID, other.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
		t.Fatal("a new request bypassed an unfinished move for the same custody")
		return RebindPathsInput{}, nil, nil
	}); err == nil {
		t.Fatal("second unfinished move admitted")
	}
	completed, err := service.ApplyRetainedRelocation(ctx, r.ID, r.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
		return RebindPathsInput{}, json.RawMessage(`{"inventory_digest":"exact"}`), nil
	})
	if err != nil || completed.State != "complete" {
		t.Fatalf("completion: %+v %v", completed, err)
	}
	current, err := service.ResolveCustodyPath(ctx, r.CustodyID, r.Original+"/note.md")
	if err != nil || current != r.Destination+"/note.md" {
		t.Fatalf("current location: %q %v", current, err)
	}
	current, err = service.ResolveCustodyPath(ctx, "workspace:another-cycle", r.Original+"/note.md")
	if err != nil || current != r.Original+"/note.md" {
		t.Fatal("new archive cycle inherited another custody's location")
	}
	if _, err = service.ApplyRetainedRelocation(ctx, r.ID, r.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
		t.Fatal("complete replay moved bytes again")
		return RebindPathsInput{}, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE storage.retained_relocations SET plan_json='{}' WHERE relocation_id=$1`, r.ID); err == nil {
		t.Fatal("journal plan was mutable")
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM storage.retained_relocation_events WHERE relocation_id=$1`, r.ID); err == nil {
		t.Fatal("events could be deleted")
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM storage.retained_relocation_events WHERE relocation_id=$1`, r.ID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("append-only transitions: %d %v", count, err)
	}
	if _, err = service.CancelRetainedRelocation(ctx, r.ID, r.Digest, func(RetainedRelocation) (json.RawMessage, error) {
		t.Fatal("completed move was offered cancellation")
		return nil, nil
	}); err == nil {
		t.Fatal("completed move cancelled")
	}
	for _, prepared := range []bool{false, true} {
		candidate := other
		candidate.ID += fmt.Sprint(prepared)
		if err = service.SaveRetainedRelocation(ctx, candidate); err != nil {
			t.Fatal(err)
		}
		if prepared {
			_, _ = service.ApplyRetainedRelocation(ctx, candidate.ID, candidate.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
				return RebindPathsInput{}, nil, errors.New("no filesystem effect")
			})
		}
		if _, err = service.CancelRetainedRelocation(ctx, candidate.ID, candidate.Digest, func(RetainedRelocation) (json.RawMessage, error) {
			return nil, errors.New("source not proven")
		}); err == nil {
			t.Fatal("failed proof released hold")
		}
		cancelled, err := service.CancelRetainedRelocation(ctx, candidate.ID, candidate.Digest, func(RetainedRelocation) (json.RawMessage, error) {
			return json.RawMessage(`{"effect":"none"}`), nil
		})
		if err != nil || cancelled.State != "cancelled" {
			t.Fatalf("cancel: %+v %v", cancelled, err)
		}
		if _, err = service.CancelRetainedRelocation(ctx, candidate.ID, candidate.Digest, func(RetainedRelocation) (json.RawMessage, error) {
			t.Fatal("cancel replay reran proof")
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err = service.ApplyRetainedRelocation(ctx, candidate.ID, candidate.Digest, func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error) {
			t.Fatal("cancelled plan executed")
			return RebindPathsInput{}, nil, nil
		}); err == nil {
			t.Fatal("cancelled apply admitted")
		}
		if release, err := AcquireWorkspaceCustodyReader(ctx, db); err != nil {
			t.Fatalf("cancelled hold still blocks backup: %v", err)
		} else {
			release()
		}
		if _, err = db.ExecContext(ctx, `UPDATE storage.retained_relocations SET state='prepared' WHERE relocation_id=$1`, candidate.ID); err == nil {
			t.Fatal("terminal cancellation mutable")
		}
	}
}
