package notesworkspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/migrations"
)

func TestPDFDedupPreservesHistoricalCustodyPostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires existing local PostgreSQL administrator for one disposable database")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, socket := u.Hostname(), u.Query().Get("host")
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "" && (strings.HasPrefix(socket, "/tmp/") || socket == "/run/postgresql" || socket == "/var/run/postgresql")) {
		t.Fatal("local disposable database only")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id, _ := newFileID()
	name := "pdf_" + strings.TrimPrefix(id, "notes_file_")
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
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
	if _, err := db.Exec(`INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path) VALUES('notes_source_root_pdf','box_notes','pdf','/test')`); err != nil {
		t.Fatal(err)
	}
	s := SQLStore{DB: db}
	f, err := s.Bind(t.Context(), File{ID: id, CollectionID: "notes_source_root_pdf", RelativePath: "history.pdf", PathKey: "history.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	originals := map[string]Base{}
	for i, body := range []string{"%PDF-same", "%PDF-same", "%PDF-new-version"} {
		b := makeBase(f, fmt.Sprint("generation-", i), true, []byte(body))
		originals[b.ID] = b
		data, _ := json.Marshal(b)
		// Deliberate legacy representation, not the new shared SaveBase path.
		if _, err := db.Exec(`INSERT INTO notes_workspace.bases(base_id,file_id,record) VALUES($1,$2,$3::jsonb || '{"extra":9007199254740993}'::jsonb)`, b.ID, f.ID, data); err != nil {
			t.Fatal(err)
		}
	}
	streamed := Base{ID: "streamed", FileID: f.ID, Generation: "streamed", Hash: digest([]byte("elsewhere")), ReferenceSize: 9, Exists: true}
	if err := s.SaveBase(t.Context(), streamed); err != nil {
		t.Fatal(err)
	}
	tf, err := s.Bind(t.Context(), File{ID: "text-file", CollectionID: f.CollectionID, RelativePath: "note.md", PathKey: "note.md"})
	if err != nil {
		t.Fatal(err)
	}
	text := makeBase(tf, "text", true, []byte("keep text merge base"))
	if err := s.SaveBase(t.Context(), text); err != nil {
		t.Fatal(err)
	}
	p, err := s.PlanPDFDedup(t.Context())
	if err != nil || len(p.Entries) != 3 || p.UniquePayloads != 2 {
		t.Fatalf("plan: %+v %v", p, err)
	}
	if _, err := db.Exec(`UPDATE notes_workspace.bases SET record=record || '{"extra":9007199254740992}'::jsonb WHERE base_id=$1`, p.Entries[0].BaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPDFDedup(t.Context(), p, p.PlanHash, nil); err == nil {
		t.Fatal("changed reviewed metadata accepted")
	}
	if _, err := db.Exec(`UPDATE notes_workspace.bases SET record=record || '{"extra":9007199254740993}'::jsonb WHERE base_id=$1`, p.Entries[0].BaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes_workspace.sync_replicas(replica_id,native_epoch) VALUES('test','epoch')`); err != nil {
		t.Fatal(err)
	}
	binding := `{"source_base_id":"` + p.Entries[0].BaseID + `","receipt":"retain"}`
	if _, err := db.Exec(`INSERT INTO notes_workspace.sync_records(replica_id,kind,record_id,record) VALUES('test','binding','saved',$1)`, binding); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, err = s.ApplyPDFDedup(ctx, p, p.PlanHash, func(n int) {
		if n == 1 {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("cancelled conversion did not stop")
	}
	result, err := s.ApplyPDFDedup(t.Context(), p, p.PlanHash, nil)
	if err != nil || result.Replayed != 1 || result.Converted != 2 {
		t.Fatalf("resume: %+v %v", result, err)
	}
	result, err = s.VerifyPDFDedup(t.Context(), p, p.PlanHash)
	if err != nil || result.Verified != 3 {
		t.Fatalf("verify: %+v %v", result, err)
	}
	for id, want := range originals {
		got, err := s.Base(t.Context(), id)
		if err != nil || jsonHash(got) != jsonHash(want) {
			t.Fatalf("historical base changed: %s %v", id, err)
		}
		var extra string
		if err := db.QueryRow(`SELECT record->>'extra' FROM notes_workspace.bases WHERE base_id=$1`, id).Scan(&extra); err != nil || extra != "9007199254740993" {
			t.Fatal("unknown metadata changed", extra, err)
		}
	}
	var payloads, count int
	if err := db.QueryRow(`SELECT count(*) FROM notes_workspace.reference_payloads`).Scan(&payloads); err != nil || payloads != 2 {
		t.Fatal("duplicate stored payload", payloads, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM notes_workspace.bases`).Scan(&count); err != nil || count != 5 {
		t.Fatal("base history removed", count, err)
	}
	var retained string
	if err := db.QueryRow(`SELECT record->>'source_base_id' FROM notes_workspace.sync_records WHERE replica_id='test' AND kind='binding' AND record_id='saved'`).Scan(&retained); err != nil || retained != p.Entries[0].BaseID {
		t.Fatal("binding history changed", retained, err)
	}
	got, err := s.Base(t.Context(), text.ID)
	if err != nil || jsonHash(got) != jsonHash(text) {
		t.Fatal("text changed", err)
	}
	got, err = s.Base(t.Context(), streamed.ID)
	if err != nil || jsonHash(got) != jsonHash(streamed) {
		t.Fatal("streamed metadata changed", err)
	}
	// Direct future PDF reads also share, even across another generation.
	future := makeBase(f, "future", true, []byte("%PDF-same"))
	if err := s.SaveBase(t.Context(), future); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM notes_workspace.reference_payloads`).Scan(&payloads); err != nil || payloads != 2 {
		t.Fatal("future duplicate payload", err)
	}
	if err := s.CheckPDFDedupProtection(t.Context(), p, "missing-cloud-operation"); err == nil {
		t.Fatal("missing protection accepted")
	}
	// Rewriting physical storage must leave exact logical history intact.
	if _, err := db.Exec(`VACUUM (FULL, ANALYZE) notes_workspace.bases`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyPDFDedup(t.Context(), p, p.PlanHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE notes_workspace.reference_payloads SET content=convert_to('XXXXXXXXX','UTF8') WHERE payload_hash=$1`, digest([]byte("%PDF-same"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Base(t.Context(), future.ID); err == nil {
		t.Fatal("corrupt shared payload accepted")
	}
	if err := s.SaveBase(t.Context(), future); err == nil {
		t.Fatal("conflicting same-hash payload accepted")
	}
}
