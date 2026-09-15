package objects_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/requestctx"
)

func objectVersionDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("LOOM_TEST_DB_URL is unset; requires owned socket-only PostgreSQL 17 fixture")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgresql" || u.Host != "" || u.Path != "/postgres" || u.User == nil || u.User.Username() != "e3c_fixture" || u.Fragment != "" || len(u.Query()) != 2 || u.Query().Get("sslmode") != "disable" || !regexp.MustCompile(`^/tmp/le3c\.[A-Za-z0-9]{6}/socket$`).MatchString(u.Query().Get("host")) {
		t.Fatal("refusing non-fixture database URL")
	}
	if _, password := u.User.Password(); password {
		t.Fatal("fixture cannot contain a password")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	var directory, user, database, listen string
	var socket bool
	var version int
	if err := admin.QueryRowContext(t.Context(), `SELECT current_setting('data_directory'),current_user,current_database(),inet_server_addr() IS NULL,current_setting('listen_addresses'),current_setting('server_version_num')::int`).Scan(&directory, &user, &database, &socket, &listen, &version); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(u.Query().Get("host")), "data"))
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	actual, err := filepath.EvalSymlinks(directory)
	if err != nil || want != actual || user != "e3c_fixture" || database != "postgres" || !socket || listen != "" || version < 170000 || version >= 180000 {
		admin.Close()
		t.Fatal("server is not the owned socket-only PostgreSQL 17 fixture")
	}
	name := "e3c_objects_" + strings.ToLower(strings.TrimPrefix(ids.NewJobID(), "job_"))
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *sql.DB
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`"`); err != nil {
			t.Errorf("owned database cleanup: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + name
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("migration path unavailable")
	}
	migrated, err := migrations.Up(t.Context(), u.String(), filepath.Join(filepath.Dir(file), "..", "..", "migrations"))
	if err != nil || migrated.CurrentVersion != 71 || migrated.Pending != 0 {
		t.Fatalf("migration=%+v error=%v", migrated, err)
	}
	db, err = sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "e3c-objects-fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned_database=%s PostgreSQL=%d socket_only=true migrations=71", name, version)
	return db, req
}

func TestObjectVersionExactSourcePostgres(t *testing.T) {
	db, req := objectVersionDatabase(t)
	store := objectstore.New(t.TempDir())
	service := objects.NewService(db, store)
	ingest := func(content string) objects.ObjectDetail {
		p := filepath.Join(t.TempDir(), "source-private.txt")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := service.IngestFile(t.Context(), req, objects.IngestFileInput{Path: p, ScopeRef: req.ScopeID})
		if err != nil {
			t.Fatal(err)
		}
		return got.Object
	}
	first := ingest("retained-v1")
	other := ingest("different-object")
	empty := ingest("")
	p := filepath.Join(t.TempDir(), "v2")
	if err := os.WriteFile(p, []byte("current-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, err := service.IngestFileVersion(t.Context(), req, first.Object.ObjectID, objects.IngestFileInput{Path: p, ScopeRef: req.ScopeID})
	if err != nil {
		t.Fatal(err)
	}
	oid, vid, bid := first.Object.ObjectID, first.LatestVersion.ObjectVersionID, first.Blob.BlobID
	fingerprint := func() string {
		out := ""
		for _, table := range []string{"objects.objects", "objects.object_versions", "files.blobs", "events.events"} {
			var raw string
			if err := db.QueryRowContext(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text,'[]') FROM `+table+` t`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			out += raw
		}
		return out
	}
	for _, tc := range []struct{ name, object, version, status, content string }{{"superseded", oid, vid, "superseded", "retained-v1"}, {"active", oid, updated.Object.LatestVersion.ObjectVersionID, "active", "current-v2"}, {"empty", empty.Object.ObjectID, empty.LatestVersion.ObjectVersionID, "active", ""}, {"independent_object", other.Object.ObjectID, other.LatestVersion.ObjectVersionID, "active", "different-object"}} {
		t.Run(tc.name, func(t *testing.T) {
			before := fingerprint()
			source, err := service.ReadObjectVersionSource(t.Context(), tc.object, tc.version)
			if err != nil || source.ObjectID != tc.object || source.ObjectVersionID != tc.version || source.VersionStatus != tc.status {
				t.Fatalf("source=%+v error=%v", source, err)
			}
			if fingerprint() != before {
				t.Fatal("read mutated object/blob/event state")
			}
			dest := filepath.Join(t.TempDir(), "object")
			if err := store.MaterializeVerified(t.Context(), source.Blob, dest); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(dest)
			if err != nil || string(content) != tc.content {
				t.Fatalf("content=%q error=%v", content, err)
			}
		})
	}
	t.Run("public_get_still_latest", func(t *testing.T) {
		got, err := service.GetObject(t.Context(), oid)
		if err != nil || got.LatestVersion.ObjectVersionID != updated.Object.LatestVersion.ObjectVersionID || got.Blob.BlobID != updated.Object.Blob.BlobID {
			t.Fatalf("latest=%+v error=%v", got, err)
		}
	})
	for _, tc := range []struct{ name, object, version string }{{"wrong_object", other.Object.ObjectID, vid}, {"missing_object", ids.NewObjectID(), vid}, {"missing_version", oid, ids.NewObjectVersionID()}, {"blank_object", "", vid}, {"blank_version", oid, ""}, {"malformed_object", "object_bad", vid}, {"malformed_version", oid, "version_bad"}, {"object_alias", oid + " ", vid}} {
		t.Run(tc.name, func(t *testing.T) {
			before := fingerprint()
			got, err := service.ReadObjectVersionSource(t.Context(), tc.object, tc.version)
			if err == nil || !reflect.DeepEqual(got, objects.ObjectVersionSource{}) {
				t.Fatalf("invalid source accepted: %+v %v", got, err)
			}
			if fingerprint() != before {
				t.Fatal("invalid read changed rows")
			}
		})
	}
	type mutation struct {
		name, query, restore string
		args, restoreArgs    []any
	}
	cases := []mutation{}
	for _, status := range []string{"archived", "failed", "deleted_later"} {
		cases = append(cases, mutation{"object_" + status, `UPDATE objects.objects SET status=$2 WHERE object_id=$1`, `UPDATE objects.objects SET status='active' WHERE object_id=$1`, []any{oid, status}, []any{oid}})
	}
	for _, status := range []string{"failed", "missing"} {
		cases = append(cases, mutation{"version_" + status, `UPDATE objects.object_versions SET status=$2 WHERE object_version_id=$1`, `UPDATE objects.object_versions SET status='superseded' WHERE object_version_id=$1`, []any{vid, status}, []any{vid}})
	}
	for _, status := range []string{"pending", "missing", "corrupt"} {
		cases = append(cases, mutation{"blob_" + status, `UPDATE files.blobs SET status=$2 WHERE blob_id=$1`, `UPDATE files.blobs SET status='verified' WHERE blob_id=$1`, []any{bid, status}, []any{bid}})
	}
	cases = append(cases,
		mutation{"missing_blob", `UPDATE objects.object_versions SET blob_id=NULL WHERE object_version_id=$1`, `UPDATE objects.object_versions SET blob_id=$2 WHERE object_version_id=$1`, []any{vid}, []any{vid, bid}},
		mutation{"missing_hash", `UPDATE objects.object_versions SET content_hash=NULL WHERE object_version_id=$1`, `UPDATE objects.object_versions SET content_hash=$2 WHERE object_version_id=$1`, []any{vid}, []any{vid, first.Blob.HashURI}},
		mutation{"missing_size", `UPDATE objects.object_versions SET size_bytes=NULL WHERE object_version_id=$1`, `UPDATE objects.object_versions SET size_bytes=$2 WHERE object_version_id=$1`, []any{vid}, []any{vid, first.Blob.SizeBytes}},
		mutation{"version_hash_mismatch", `UPDATE objects.object_versions SET content_hash=$2 WHERE object_version_id=$1`, `UPDATE objects.object_versions SET content_hash=$2 WHERE object_version_id=$1`, []any{vid, other.Blob.HashURI}, []any{vid, first.Blob.HashURI}},
		mutation{"version_blob_mismatch", `UPDATE objects.object_versions SET blob_id=$2 WHERE object_version_id=$1`, `UPDATE objects.object_versions SET blob_id=$2 WHERE object_version_id=$1`, []any{vid, other.Blob.BlobID}, []any{vid, bid}},
		mutation{"size_mismatch", `UPDATE objects.object_versions SET size_bytes=size_bytes+1 WHERE object_version_id=$1`, `UPDATE objects.object_versions SET size_bytes=$2 WHERE object_version_id=$1`, []any{vid}, []any{vid, first.Blob.SizeBytes}},
		mutation{"hash_uri_mismatch", `UPDATE files.blobs SET hash_uri=$2 WHERE blob_id=$1`, `UPDATE files.blobs SET hash_uri=$2 WHERE blob_id=$1`, []any{bid, "sha256:" + strings.Repeat("a", 64)}, []any{bid, first.Blob.HashURI}},
		mutation{"hash_hex_mismatch", `UPDATE files.blobs SET hash_hex=$2 WHERE blob_id=$1`, `UPDATE files.blobs SET hash_hex=$2 WHERE blob_id=$1`, []any{bid, strings.Repeat("b", 64)}, []any{bid, first.Blob.HashHex}},
		mutation{"missing_managed_path", `UPDATE files.blobs SET storage_path='' WHERE blob_id=$1`, `UPDATE files.blobs SET storage_path=$2 WHERE blob_id=$1`, []any{bid}, []any{bid, first.Blob.StoragePath}},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), tc.query, tc.args...); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.ExecContext(t.Context(), tc.restore, tc.restoreArgs...); err != nil {
					t.Fatal(err)
				}
			}()
			before := fingerprint()
			got, err := service.ReadObjectVersionSource(t.Context(), oid, vid)
			if err == nil || !reflect.DeepEqual(got, objects.ObjectVersionSource{}) {
				t.Fatalf("invalid metadata accepted: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), "/") || strings.Contains(err.Error(), "SELECT") {
				t.Fatalf("unsafe error: %v", err)
			}
			if before != fingerprint() {
				t.Fatal("failed read changed rows")
			}
			if _, err := service.ReadObjectVersionSource(t.Context(), other.Object.ObjectID, other.LatestVersion.ObjectVersionID); err != nil {
				t.Fatalf("other object affected: %v", err)
			}
		})
	}
	t.Run("constraints_stay_intact", func(t *testing.T) {
		for _, q := range []string{`UPDATE files.blobs SET hash_algorithm='sha1' WHERE blob_id=$1`, `UPDATE files.blobs SET hash_hex='bad' WHERE blob_id=$1`, `UPDATE files.blobs SET hash_uri='bad' WHERE blob_id=$1`, `UPDATE files.blobs SET size_bytes=-1 WHERE blob_id=$1`} {
			if _, err := db.ExecContext(t.Context(), q, bid); err == nil {
				t.Fatal("invalid blob metadata bypassed database constraint")
			}
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := service.ReadObjectVersionSource(ctx, oid, vid); !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}
