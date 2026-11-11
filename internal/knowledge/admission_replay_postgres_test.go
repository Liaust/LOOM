package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"loom.local/loom/internal/migrations"
	lexical "loom.local/loom/internal/search"
	"loom.local/loom/internal/storagecatalog"
)

func TestAdmissionReplayLexicalMaterializationPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	_, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "metadata fixture", time.Now().UTC())
	var id string
	if err := db.QueryRow(`SELECT search_document_id FROM search.search_documents WHERE source_kind=$1 AND source_id=$2 AND index_version=$3`,
		KnowledgeMetadataSearchSourceKind, object.KnowledgeObjectID, KnowledgeSearchIndexKey).Scan(&id); err != nil {
		t.Fatal(err)
	}
	input := knowledgeMetadataLexicalDocumentInput(id, object)
	ensure := func(wantChanged bool) {
		t.Helper()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, changed, err := lexical.EnsureLexicalDocumentTx(t.Context(), tx, input)
		if err != nil || changed != wantChanged {
			t.Fatalf("ensure changed=%t want=%t: %v", changed, wantChanged, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := func() string {
		t.Helper()
		var value string
		err := db.QueryRow(`SELECT concat_ws('/',sd.xmin::text,sd.indexed_at::text,ld.xmin::text,ld.updated_at::text,
			(SELECT string_agg(lt.xmin::text,',' ORDER BY lt.field_key COLLATE "C",lt.term COLLATE "C") FROM search.lexical_terms lt WHERE lt.search_document_id=sd.search_document_id))
			FROM search.search_documents sd JOIN search.lexical_documents ld USING(search_document_id) WHERE sd.search_document_id=$1`, id).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := fingerprint()
	ensure(false)
	if fingerprint() != before {
		t.Fatal("unchanged ensure rewrote materialization")
	}
	for _, mutation := range []string{
		`DELETE FROM search.lexical_terms WHERE search_document_id=$1 AND (field_key,term)=(SELECT field_key,term FROM search.lexical_terms WHERE search_document_id=$1 LIMIT 1)`,
		`UPDATE search.lexical_terms SET term_frequency=term_frequency+1 WHERE search_document_id=$1`,
		`INSERT INTO search.lexical_terms(search_document_id,field_key,term,term_frequency) VALUES($1,'body','surplus',1)`,
		`DELETE FROM search.lexical_documents WHERE search_document_id=$1`,
	} {
		if _, err := db.Exec(mutation, id); err != nil {
			t.Fatal(err)
		}
		ensure(true)
		before = fingerprint()
		ensure(false)
		if fingerprint() != before {
			t.Fatal("repaired materialization did not become a no-op")
		}
	}
	input.Metadata = json.RawMessage(`{"large":9007199254740992,"other":true}`)
	ensure(true)
	before = fingerprint()
	input.Metadata = json.RawMessage(`{"other":true,"large":9007199254740992}`)
	ensure(false)
	if fingerprint() != before {
		t.Fatal("equivalent JSON rewrote the index")
	}
	input.Metadata = json.RawMessage(`{"large":9007199254740993,"other":true}`)
	ensure(true)
	input.Fields = nil
	ensure(true)
	before = fingerprint()
	ensure(false)
	if fingerprint() != before {
		t.Fatal("valid empty materialization rewrote on replay")
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	input.Fields = []lexical.LexicalFieldInput{{Key: "title", Text: "rollback change"}}
	if _, changed, err := lexical.EnsureLexicalDocumentTx(t.Context(), tx, input); err != nil || !changed {
		tx.Rollback()
		t.Fatalf("rollback fixture: %t %v", changed, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if fingerprint() != before {
		t.Fatal("rollback changed materialization")
	}
	input.Fields = nil

	owner, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Rollback()
	if _, _, err = lexical.EnsureLexicalDocumentTx(t.Context(), owner, input); err != nil {
		t.Fatal(err)
	}
	contender, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Rollback()
	if _, err = contender.Exec(`SET LOCAL lock_timeout='50ms'`); err != nil {
		t.Fatal(err)
	}
	_, _, err = lexical.EnsureLexicalDocumentTx(t.Context(), contender, input)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
		t.Fatalf("concurrent ensure bypassed owner lock: %v", err)
	}
	contender.Rollback()
	if err = owner.Commit(); err != nil {
		t.Fatal(err)
	}
	ensure(false)
}

func TestAdmissionReplayKnowledgeMetadataPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	service, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "metadata replay", time.Now().UTC())
	versions := func() string {
		t.Helper()
		value, err := admissionSearchVersions(t.Context(), db, object.KnowledgeObjectID)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	replay := func() {
		t.Helper()
		var err error
		object, err = service.store.UpsertKnowledgeObject(t.Context(), object)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := versions()
	replay()
	if versions() != before {
		t.Fatal("unchanged object admission rewrote search materialization")
	}
	object.Title = "Updated metadata title"
	replay()
	if versions() == before {
		t.Fatal("title change did not refresh search materialization")
	}
	before = versions()
	replay()
	if versions() != before {
		t.Fatal("title change did not converge to no-op")
	}
	var bodyHash string
	if err := db.QueryRow(`SELECT body_hash FROM search.search_documents WHERE source_kind=$1 AND source_id=$2`, KnowledgeMetadataSearchSourceKind, object.KnowledgeObjectID).Scan(&bodyHash); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	updated := object
	updated.SourceModifiedAt, updated.RecencyAt, updated.RecencyBasis = &mtime, mtime, AbsoluteTimeBasisSourceFilesystemMtime
	// Exercise the writer directly: catalog replay intentionally rejects an
	// unauthenticated timestamp observation for an unchanged source revision.
	writeTimestamp := func() {
		t.Helper()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = upsertKnowledgeMetadataSearchDocumentTx(t.Context(), tx, updated); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	writeTimestamp()
	if versions() == before {
		t.Fatal("timestamp-only change was skipped")
	}
	var sameBody bool
	if err := db.QueryRow(`SELECT body_hash=$3 AND source_modified_at=$4 AND recency_at=$4 FROM search.search_documents WHERE source_kind=$1 AND source_id=$2`, KnowledgeMetadataSearchSourceKind, object.KnowledgeObjectID, bodyHash, mtime).Scan(&sameBody); err != nil || !sameBody {
		t.Fatalf("timestamp-only update lost its binding: %t %v", sameBody, err)
	}
	before = versions()
	writeTimestamp()
	if versions() != before {
		t.Fatal("timestamp-only change did not converge to no-op")
	}
	replay()
	for _, mutation := range []string{
		`UPDATE search.search_documents SET tsv=to_tsvector('simple','broken') WHERE source_id=$1 AND source_kind='knowledge_object_metadata'`,
		`UPDATE search.search_documents SET metadata='{"stale":true}'::jsonb WHERE source_id=$1 AND source_kind='knowledge_object_metadata'`,
		`UPDATE search.search_documents SET language_config='english',freshness_state='stale' WHERE source_id=$1 AND source_kind='knowledge_object_metadata'`,
		`DELETE FROM search.lexical_terms WHERE search_document_id=(SELECT search_document_id FROM search.search_documents WHERE source_id=$1 AND source_kind='knowledge_object_metadata')`,
		`DELETE FROM search.lexical_documents WHERE search_document_id=(SELECT search_document_id FROM search.search_documents WHERE source_id=$1 AND source_kind='knowledge_object_metadata')`,
	} {
		result, err := db.Exec(mutation, object.KnowledgeObjectID)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			t.Fatalf("fixture did not change materialization: %d %v", n, err)
		}
		replay()
		before = versions()
		replay()
		if versions() != before {
			t.Fatal("repaired metadata materialization did not converge")
		}
	}
	var correct bool
	err := db.QueryRow(`SELECT title=$3 AND language_config='simple' AND freshness_state='fresh'
		AND metadata=$4::jsonb AND tsv @@ plainto_tsquery('simple',$3)
		FROM search.search_documents WHERE source_kind=$1 AND source_id=$2`, KnowledgeMetadataSearchSourceKind,
		object.KnowledgeObjectID, object.Title, knowledgeMetadataSearchDocumentMetadata(object)).Scan(&correct)
	if err != nil || !correct {
		t.Fatalf("repaired search projection differs: %t %v", correct, err)
	}
}

func TestAdmissionPublicationWithdrawalNoopPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	_, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "unchanged publication", time.Now().UTC())
	plan, err := CompilePipelinePlan(object, DefaultPipelinePolicy())
	if err != nil {
		t.Fatal(err)
	}
	version := func() string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT xmin::text FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := version()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := withdrawIncompatiblePublicationsTx(t.Context(), tx, object.KnowledgeObjectID, plan); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if version() != before {
		t.Fatal("unchanged publication pointers were rewritten")
	}
}

func admissionSearchVersions(ctx context.Context, db *sql.DB, objectID string) (string, error) {
	var version string
	err := db.QueryRowContext(ctx, `SELECT concat_ws('/',sd.search_document_id,sd.xmin::text,sd.indexed_at::text,
		ld.xmin::text,ld.updated_at::text,(SELECT string_agg(lt.xmin::text,',' ORDER BY lt.field_key COLLATE "C",lt.term COLLATE "C")
		FROM search.lexical_terms lt WHERE lt.search_document_id=sd.search_document_id))
		FROM search.search_documents sd JOIN search.lexical_documents ld USING(search_document_id)
		WHERE sd.source_kind=$1 AND sd.source_id=$2 AND sd.index_version=$3`, KnowledgeMetadataSearchSourceKind, objectID, KnowledgeSearchIndexKey).Scan(&version)
	return version, err
}
