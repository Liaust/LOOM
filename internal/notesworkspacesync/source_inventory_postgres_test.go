package notesworkspacesync

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
)

func TestSourceInventoryPostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local PostgreSQL administrator; creates only a disposable database")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, socket := u.Hostname(), u.Query().Get("host")
	localSocket := strings.HasPrefix(socket, "/tmp/") || socket == "/run/postgresql" || socket == "/var/run/postgresql"
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "" && localSocket) {
		t.Fatal("requires local disposable database endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "notes_inventory_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	}()
	u.Path = "/" + name
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	i, scope, now := inventoryFixture(t)
	scope.Collection = "notes_source_root_inventory_test"
	if _, err := db.ExecContext(t.Context(), `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path) VALUES($1,'box_notes','inventory-test','/test')`, scope.Collection); err != nil {
		t.Fatal(err)
	}
	i.Store = SQLStore{DB: db}
	t.Run("absent-directory-remains-actionable", func(t *testing.T) {
		dirScope := scope
		dirScope.Generation = "directory-generation"
		if err := i.Store.WithInventory(t.Context(), dirScope, func(r InventoryRecords) error {
			return r.Put(t.Context(), InventoryEntry{Path: "removed/", Kind: "directory", Sequence: 1, WaitingSince: now, RetryAt: now})
		}); err != nil {
			t.Fatal(err)
		}
		page, err := i.DeliveryPage(t.Context(), dirScope, "text", now, 32)
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Present {
			t.Fatal("absent directory was lost from delivery", page, err)
		}
		if err := i.Finish(t.Context(), dirScope, page.Entries[0], "", now); err != nil {
			t.Fatal(err)
		}
		page, err = i.DeliveryPage(t.Context(), dirScope, "text", now, 32)
		if err != nil || len(page.Entries) != 0 {
			t.Fatal("acknowledged directory stayed runnable", page, err)
		}
	})
	inventoryWrite(t, scope, "a.md", "A")
	inventoryWrite(t, scope, "asset.png", "asset")
	if err := i.Begin(t.Context(), scope, "", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), scope, now, 1); err != nil {
		t.Fatal(err)
	}
	if got := inventoryState(t, i, scope); got.Scans[0].After != "a.md" || got.Complete {
		t.Fatal("partial SQL cursor", got)
	}
	// Fresh owner retains continuation. Cross-process notification does not reset it.
	i.Store = SQLStore{DB: db}
	if err := i.Advance(t.Context(), scope, now, 1); err != nil {
		t.Fatal(err)
	}
	page, err := i.DeliveryPage(t.Context(), scope, "text", now, 32)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	old := page.Entries[0]
	inventoryWrite(t, scope, "a.md", "B")
	if err := i.Hint(t.Context(), scope, "a.md", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Finish(t.Context(), scope, old, "", now); err != nil {
		t.Fatal(err)
	}
	changed := inventoryRow(t, i, scope, "a.md")
	if changed.Sequence <= old.Sequence || changed.Delivered == changed.Sequence {
		t.Fatal("SQL compare-ack lost change", changed)
	}
	if err := i.Finish(t.Context(), scope, changed, "publication_pending", now); err != nil {
		t.Fatal(err)
	}
	page, err = i.DeliveryPage(t.Context(), scope, "text", now, 32)
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("SQL retry deadline", page, err)
	}
	page, err = i.DeliveryPage(t.Context(), scope, "asset", now, 32)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal("asset blocked by text retry", page, err)
	}
	progress, err := i.Progress(t.Context(), scope, now)
	if err != nil || !progress.CountersKnown || progress.Counts.TextPending != 1 || progress.Counts.AssetPending != 1 || progress.Counts.TextPresent != 1 || progress.Counts.AssetPresent != 1 {
		t.Fatal("SQL maintained counters", progress, err)
	}
	// Progress is a read-only exact-generation observation, including retry-not-due.
	readURL := *u
	values := readURL.Query()
	values.Set("default_transaction_read_only", "on")
	readURL.RawQuery = values.Encode()
	reader, err := sql.Open("pgx", readURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var beforeRecord, afterRecord string
	var beforeTime, afterTime time.Time
	if err = db.QueryRow(`SELECT record::text,updated_at FROM notes_workspace.source_inventory_generations WHERE collection_id=$1 AND generation=$2`, scope.Collection, scope.Generation).Scan(&beforeRecord, &beforeTime); err != nil {
		t.Fatal(err)
	}
	observed, err := ReadInventoryProgress(t.Context(), reader, scope.Collection, scope.Generation, now.Add(time.Second))
	if err != nil || observed.Counts != progress.Counts || observed.TextReason != "publication_pending" || observed.TextRetryAt == nil || !observed.TextRetryAt.After(now) {
		t.Fatalf("read-only generation progress: %+v %v", observed, err)
	}
	if err = db.QueryRow(`SELECT record::text,updated_at FROM notes_workspace.source_inventory_generations WHERE collection_id=$1 AND generation=$2`, scope.Collection, scope.Generation).Scan(&afterRecord, &afterTime); err != nil {
		t.Fatal(err)
	}
	if beforeRecord != afterRecord || !beforeTime.Equal(afterTime) {
		t.Fatal("status mutated inventory state")
	}
	if _, err = ReadInventoryProgress(t.Context(), reader, scope.Collection, "missing-generation", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing generation was fabricated: %v", err)
	}
	sentinel := errors.New("rollback")
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error { r.State().Sequence = 999999; return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if inventoryState(t, i, scope).Sequence == 999999 {
		t.Fatal("SQL state committed failed transaction")
	}
	beforeSeen := inventoryRow(t, i, scope, changed.Path)
	beforeCounts := inventoryState(t, i, scope).Counts
	scan := inventoryState(t, i, scope).Sequence
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
		if err := r.MarkSeen(t.Context(), changed.Path, scan); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if got := inventoryRow(t, i, scope, changed.Path); got != beforeSeen {
		t.Fatal("failed seen-only write committed", got, beforeSeen)
	}
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
		if err := r.MarkSeen(t.Context(), changed.Path, scan); err != nil {
			return err
		}
		return r.MarkSeen(t.Context(), changed.Path, scan-1)
	}); err != nil {
		t.Fatal(err)
	}
	wantSeen := beforeSeen
	wantSeen.SeenScan = scan
	if got := inventoryRow(t, i, scope, changed.Path); got != wantSeen || inventoryState(t, i, scope).Counts != beforeCounts {
		t.Fatal("seen-only write changed publication metadata/counters or moved backwards", got, wantSeen)
	}
	asset := inventoryRow(t, i, scope, "asset.png")
	if err := i.Finish(t.Context(), scope, asset, "source_excluded", now); err != nil {
		t.Fatal(err)
	}
	page, err = i.DeliveryPage(t.Context(), scope, "asset", now.Add(time.Hour), 32)
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("SQL excluded delivery", page, err)
	}
	progress, err = i.Progress(t.Context(), scope, now)
	if err != nil || progress.Counts.Excluded != 1 || progress.Counts.AssetPending != 0 || progress.AssetRetryAt != nil || progress.AssetReason != "" {
		t.Fatal("SQL excluded counts/deadlines", progress, err)
	}
	if row := inventoryRow(t, i, scope, asset.Path); row.Delivered != asset.Delivered {
		t.Fatal("SQL exclusion incorrectly acknowledged publication", row)
	}
	// Old code did not persist lane exclusions. Read-only status must not seed
	// them; the next generation-local writer upgrades them transactionally.
	if _, err = db.ExecContext(t.Context(), `UPDATE notes_workspace.source_inventory_generations
 SET record=(record-'lane_exclusions_ready') || jsonb_build_object('counts',(record->'counts')-'text_excluded'-'asset_excluded')
 WHERE collection_id=$1 AND generation=$2`, scope.Collection, scope.Generation); err != nil {
		t.Fatal(err)
	}
	observed, err = ReadInventoryProgress(t.Context(), reader, scope.Collection, scope.Generation, now)
	if err != nil || observed.LaneExclusionsKnown || inventoryPublication(observed).AssetPublished != nil {
		t.Fatal("legacy read fabricated lane counters", observed, err)
	}
	if err = i.Store.WithInventory(t.Context(), scope, func(InventoryRecords) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	observed, err = ReadInventoryProgress(t.Context(), reader, scope.Collection, scope.Generation, now)
	if err != nil || observed.LaneExclusionsKnown {
		t.Fatal("failed writer committed counter upgrade", observed, err)
	}
	if err = i.Store.WithInventory(t.Context(), scope, func(InventoryRecords) error { return nil }); err != nil {
		t.Fatal(err)
	}
	observed, err = ReadInventoryProgress(t.Context(), reader, scope.Collection, scope.Generation, now)
	publication := inventoryPublication(observed)
	if err != nil || !observed.LaneExclusionsKnown || observed.Counts.AssetExcluded != 1 || observed.Counts.TextExcluded != 0 || publication.AssetPublished == nil || *publication.AssetPublished != 0 || publication.TextPublished == nil || *publication.TextPublished != 0 {
		t.Fatal("writer upgrade did not preserve pending/excluded truth", observed, err)
	}
	if err = db.QueryRow(`SELECT record::text,updated_at FROM notes_workspace.source_inventory_generations WHERE collection_id=$1 AND generation=$2`, scope.Collection, scope.Generation).Scan(&beforeRecord, &beforeTime); err != nil {
		t.Fatal(err)
	}
	if err = i.Store.WithInventory(t.Context(), scope, func(InventoryRecords) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT record::text,updated_at FROM notes_workspace.source_inventory_generations WHERE collection_id=$1 AND generation=$2`, scope.Collection, scope.Generation).Scan(&afterRecord, &afterTime); err != nil {
		t.Fatal(err)
	}
	if beforeRecord != afterRecord || !beforeTime.Equal(afterTime) {
		t.Fatal("replayed upgrade rewrote unchanged counters")
	}
	for _, scans := range [][]inventoryScan{
		nil,
		{{Prefix: "", After: "a.md", Again: true}, {Prefix: "waiting-not-for-publication", After: "private-waiting-cursor"}},
		{{Prefix: "Selected", After: "Selected/gone.md", Withdrawing: true}},
	} {
		if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
			r.State().Scans, r.State().Complete = scans, len(scans) == 0
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		expected, err := i.Progress(t.Context(), scope, now)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := ReadInventoryProgress(t.Context(), reader, scope.Collection, scope.Generation, now)
		if err != nil || !reflect.DeepEqual(observed.ActiveScan, expected.ActiveScan) || observed.QueuedScans != expected.QueuedScans || observed.Complete != expected.Complete {
			t.Fatal("read-only projection lost scan head/count or exposed a different phase", expected, observed, err)
		}
	}
}
