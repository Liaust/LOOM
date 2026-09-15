package jobs_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/workflows"
)

func sourceVersionDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
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
	name := "e3c_jobs_" + strings.ToLower(strings.TrimPrefix(ids.NewJobID(), "job_"))
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
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "e3c-jobs-fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned_database=%s PostgreSQL=%d socket_only=true migrations=71", name, version)
	return db, req
}

type sourceVersionFixture struct {
	db            *sql.DB
	req           requestctx.Context
	service       jobs.Service
	objects       objects.Service
	root          string
	first, second projects.Project
	runner        jobs.Runner
}

func newSourceVersionFixture(t *testing.T) sourceVersionFixture {
	t.Helper()
	db, req := sourceVersionDatabase(t)
	root := t.TempDir()
	store := objectstore.New(filepath.Join(root, "objects"))
	obj := objects.NewService(db, store)
	service := jobs.NewService(db, filepath.Join(root, "runtime"), scripts.NewService(db), obj, artifacts.Service{DB: db, Objects: obj, Search: search.NewService(db, store)})
	f := sourceVersionFixture{db: db, req: req, service: service, objects: obj, root: root}
	for i, name := range []string{"source-a", "source-b"} {
		created, err := projects.NewService(db).CreateProject(t.Context(), req, projects.CreateInput{Name: name, Slug: name, HomeNodeRef: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.first = created.Project.Project
		} else {
			f.second = created.Project.Project
		}
	}
	for _, kind := range []string{"script", "workflow", "retry"} {
		dir := filepath.Join(root, "producer-"+kind)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `#!/bin/sh
set -eu
printf launched > "$LOOM_OUTPUT_DIR/launched"
if [ "${LOOM_INPUT_OBJECT_PATH+present}" = present ]; then
 /bin/cat "$LOOM_INPUT_OBJECT_PATH" > "$LOOM_OUTPUT_DIR/observed.txt"
else
 printf 'objectless\n' > "$LOOM_OUTPUT_DIR/observed.txt"
fi
/bin/cat "$LOOM_OUTPUT_DIR/observed.txt"
`
		if kind == "retry" {
			body += `if [ "$LOOM_ATTEMPT" = 1 ]; then exit 7; fi
`
		}
		body += `/bin/cp "$LOOM_OUTPUT_DIR/observed.txt" "$LOOM_ARTIFACT_DIR/observed.txt"
printf '{"status":"ok","outputs":{"marker":"source_version"},"artifacts":[{"key":"observed","path":"observed.txt","type":"file"}]}\n' > "$LOOM_RESULT_FILE"
`
		if err := os.WriteFile(filepath.Join(dir, "probe.sh"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		manifest := `kind: loom.script
id: source_version_` + kind + `
name: Source Version Fixture
version: 1.0.0
entrypoint:
  command: ["/bin/sh", "probe.sh"]
execution:
  timeout_seconds: 10
  network: false
`
		if kind == "workflow" {
			manifest = `kind: loom.workflow
schema_version: workflow.contract.v0.3.1
workflow:
  id: source_version_workflow
  name: Source Version Workflow Fixture
  version: 1.0.0
implementation:
  kind: workflow
entrypoint:
  command: ["/bin/sh", "probe.sh"]
execution:
  timeout_seconds: 10
  network: false
`
		}
		path := filepath.Join(dir, kind+".yaml")
		if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		if kind == "workflow" {
			if _, err := service.Workflows.RegisterWorkflow(t.Context(), req, workflows.RegisterInput{ManifestPath: path, Activate: true}); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := service.Scripts.RegisterScript(t.Context(), req, scripts.RegisterInput{ManifestPath: path, Activate: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var err error
	f.runner, err = service.EnsureLocalRunner(t.Context(), req, "source-version-fixture")
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f sourceVersionFixture) ingest(t *testing.T, scope, content string) objects.ObjectDetail {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := f.objects.IngestFile(t.Context(), f.req, objects.IngestFileInput{Path: p, ScopeRef: scope})
	if err != nil {
		t.Fatal(err)
	}
	return out.Object
}
func (f sourceVersionFixture) update(t *testing.T, object objects.ObjectDetail, content string) objects.ObjectDetail {
	t.Helper()
	p := filepath.Join(t.TempDir(), "updated.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := f.objects.IngestFileVersion(t.Context(), f.req, object.Object.ObjectID, objects.IngestFileInput{Path: p, ScopeRef: *object.Object.HomeScopeID})
	if err != nil {
		t.Fatal(err)
	}
	return out.Object
}
func (f sourceVersionFixture) enqueue(t *testing.T, kind, object, scope string) jobs.Job {
	t.Helper()
	var job jobs.Job
	var err error
	if kind == "workflow" {
		job, err = f.service.CreateWorkflowRun(t.Context(), f.req, jobs.CreateWorkflowRunInput{WorkflowRef: "source_version_workflow", ObjectRef: object, ScopeRef: scope})
	} else {
		job, err = f.service.CreateScriptRun(t.Context(), f.req, jobs.CreateScriptRunInput{ScriptRef: "source_version_" + kind, ObjectRef: object, ScopeRef: scope})
	}
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func (f sourceVersionFixture) claim(t *testing.T, job jobs.Job) jobs.JobClaim {
	t.Helper()
	claim, err := f.service.ClaimNext(t.Context(), f.req, f.runner.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Job.JobID != job.JobID || !strings.HasPrefix(claim.Attempt.WorkdirPath, f.root+"/") {
		t.Fatal("wrong job or unowned work directory claimed")
	}
	return claim
}
func (f sourceVersionFixture) assertPin(t *testing.T, job jobs.Job, want string) {
	t.Helper()
	var stored *string
	var input []byte
	if err := f.db.QueryRowContext(t.Context(), `SELECT source_object_version_id,input_json FROM jobs.jobs WHERE job_id=$1`, job.JobID).Scan(&stored, &input); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != want || job.SourceObjectVersionID == nil || *job.SourceObjectVersionID != want {
		t.Fatal("job row pin changed")
	}
	var payload struct {
		Object struct {
			Version string `json:"object_version_id"`
		} `json:"object"`
	}
	if err := json.Unmarshal(input, &payload); err != nil || payload.Object.Version != want {
		t.Fatal("input envelope pin changed")
	}
}
func (f sourceVersionFixture) assertSuccess(t *testing.T, claim jobs.JobClaim, want string) jobs.RunResult {
	t.Helper()
	result, err := f.service.RunClaim(t.Context(), f.req, claim)
	if err != nil {
		t.Fatalf("RunClaim failed: %v", err)
	}
	if result.Job.Job.Status != jobs.StatusCompleted || len(result.Job.Attempts) == 0 {
		t.Fatal("job not completed")
	}
	var status string
	var exit *int
	if err := f.db.QueryRowContext(t.Context(), `SELECT status,exit_code FROM jobs.job_attempts WHERE job_attempt_id=$1`, claim.Attempt.JobAttemptID).Scan(&status, &exit); err != nil || status != jobs.StatusCompleted || exit == nil || *exit != 0 {
		t.Fatalf("attempt status=%s exit=%v error=%v", status, exit, err)
	}
	for _, path := range []string{filepath.Join(claim.Attempt.OutputDir, "observed.txt"), filepath.Join(claim.Attempt.ArtifactDir, "observed.txt"), filepath.Join(claim.Attempt.WorkdirPath, "logs", "stdout.log")} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("executed bytes=%q want=%q error=%v", got, want, err)
		}
	}
	if len(result.Job.Artifacts) != 1 || len(result.Job.Outputs) != 2 {
		t.Fatalf("artifacts=%d outputs=%d", len(result.Job.Artifacts), len(result.Job.Outputs))
	}
	artifact := result.Job.Artifacts[0]
	if artifact.JobID != claim.Job.JobID || artifact.ScopeID == nil || *artifact.ScopeID != *claim.Job.ScopeID {
		t.Fatal("artifact scope/job mismatch")
	}
	detail, err := f.objects.GetObject(t.Context(), artifact.ObjectID)
	if err != nil || detail.Blob == nil {
		t.Fatalf("artifact lookup: %v", err)
	}
	artifactPath := detail.Blob.StoragePath
	if !filepath.IsAbs(artifactPath) {
		artifactPath = filepath.Join(f.objects.Store.Root, artifactPath)
	}
	payload, err := os.ReadFile(artifactPath)
	if err != nil || string(payload) != want {
		t.Fatal("retained artifact bytes mismatch")
	}
	var completed int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM events.events WHERE payload->>'job_id'=$1 AND scope_id=$2 AND event_type IN ('job.completed','script.run.completed','workflow.run.completed')`, claim.Job.JobID, *claim.Job.ScopeID).Scan(&completed); err != nil || completed != 2 {
		t.Fatalf("completed events=%d error=%v", completed, err)
	}
	return result
}

func TestSourceVersionQueuedScriptWorkflowPostgres(t *testing.T) {
	f := newSourceVersionFixture(t)
	for _, kind := range []string{"script", "workflow"} {
		t.Run(kind, func(t *testing.T) {
			first := f.ingest(t, f.first.ProjectScopeID, "queued-v1-"+kind+"\n")
			job := f.enqueue(t, kind, first.Object.ObjectID, f.first.ProjectScopeID)
			v1 := first.LatestVersion.ObjectVersionID
			f.assertPin(t, job, v1)
			second := f.update(t, first, "later-v2-"+kind+"\n")
			claim := f.claim(t, job)
			f.assertPin(t, claim.Job, v1)
			result := f.assertSuccess(t, claim, "queued-v1-"+kind+"\n")
			f.assertPin(t, result.Job.Job, v1)
			latest, err := f.objects.GetObject(t.Context(), first.Object.ObjectID)
			if err != nil || latest.LatestVersion.ObjectVersionID != second.LatestVersion.ObjectVersionID {
				t.Fatal("GetObject stopped returning latest")
			}
			next := f.enqueue(t, kind, first.Object.ObjectID, f.first.ProjectScopeID)
			f.assertPin(t, next, second.LatestVersion.ObjectVersionID)
			f.assertSuccess(t, f.claim(t, next), "later-v2-"+kind+"\n")
			t.Log("queued=v1 updated=v2 executed=v1 later_job=v2; each success: 1 attempt, 1 artifact, 2 output rows, 2 scoped completion events")
		})
	}
	t.Run("objectless", func(t *testing.T) {
		for _, kind := range []string{"script", "workflow"} {
			job := f.enqueue(t, kind, "", f.second.ProjectScopeID)
			if job.SourceObjectID != nil || job.SourceObjectVersionID != nil {
				t.Fatal("objectless pin added")
			}
			claim := f.claim(t, job)
			f.assertSuccess(t, claim, "objectless\n")
			if _, err := os.Lstat(filepath.Join(claim.Attempt.InputDir, "object")); !os.IsNotExist(err) {
				t.Fatal("objectless job got input object")
			}
		}
	})
	t.Run("empty_source", func(t *testing.T) {
		first := f.ingest(t, f.second.ProjectScopeID, "")
		job := f.enqueue(t, "script", first.Object.ObjectID, f.second.ProjectScopeID)
		f.update(t, first, "nonempty-later")
		f.assertSuccess(t, f.claim(t, job), "")
	})
	t.Run("root_relative_retained_blob", func(t *testing.T) {
		first := f.ingest(t, f.second.ProjectScopeID, "relative-retained-v1\n")
		job := f.enqueue(t, "script", first.Object.ObjectID, f.second.ProjectScopeID)
		f.update(t, first, "relative-later-v2\n")
		relative, err := filepath.Rel(f.objects.Store.Root, first.Blob.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.ExecContext(t.Context(), `UPDATE files.blobs SET storage_path=$2 WHERE blob_id=$1`, first.Blob.BlobID, relative); err != nil {
			t.Fatal(err)
		}
		f.assertSuccess(t, f.claim(t, job), "relative-retained-v1\n")
	})
}

func TestSourceVersionRetryPostgres(t *testing.T) {
	f := newSourceVersionFixture(t)
	first := f.ingest(t, f.first.ProjectScopeID, "retry-v1\n")
	job := f.enqueue(t, "retry", first.Object.ObjectID, f.first.ProjectScopeID)
	v1 := first.LatestVersion.ObjectVersionID
	f.update(t, first, "retry-v2\n")
	claim := f.claim(t, job)
	result, err := f.service.RunClaim(t.Context(), f.req, claim)
	if err == nil || result.Job.Job.Status != jobs.StatusFailed || len(result.Job.Attempts) != 1 || result.Job.Attempts[0].ExitCode == nil || *result.Job.Attempts[0].ExitCode != 7 {
		t.Fatalf("retry first attempt status=%s error=%v", result.Job.Job.Status, err)
	}
	observed, err := os.ReadFile(filepath.Join(claim.Attempt.OutputDir, "observed.txt"))
	if err != nil || string(observed) != "retry-v1\n" {
		t.Fatalf("first attempt bytes=%q error=%v", observed, err)
	}
	f.update(t, first, "retry-v3\n")
	retried, err := f.service.RetryJob(t.Context(), f.req, jobs.RetryJobInput{JobRef: job.JobID, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	f.assertPin(t, retried, v1)
	retryClaim := f.claim(t, retried)
	if retryClaim.Attempt.AttemptNumber != 2 {
		t.Fatal("retry attempt changed")
	}
	final := f.assertSuccess(t, retryClaim, "retry-v1\n")
	f.assertPin(t, final.Job.Job, v1)
	if len(final.Job.Attempts) != 2 {
		t.Fatal("retry lost original attempt")
	}
	observed, _ = os.ReadFile(filepath.Join(claim.Attempt.OutputDir, "observed.txt"))
	if string(observed) != "retry-v1\n" {
		t.Fatal("retry overwrote old attempt output")
	}
	t.Log("retry preserved v1 after v2/v3, failed attempt exit7 retained, next attempt completed with v1")
}

func TestSourceVersionMaterializationFailuresPostgres(t *testing.T) {
	f := newSourceVersionFixture(t)
	other := f.ingest(t, f.second.ProjectScopeID, "independent-project-source\n")
	cases := []string{"object_only", "version_only", "blank_object", "blank_version", "malformed_object", "malformed_version", "wrong_object_version", "missing_lookup", "missing_blob", "missing_hash", "missing_size", "object_archived", "version_missing", "blob_missing", "same_length_corruption", "truncate", "grow", "missing_file", "unreadable", "nonregular", "outside_root", "symlink_leaf", "source_path_is_not_fallback", "job_archived_after_claim", "source_home_archived_after_claim", "destination_file", "destination_symlink", "cancel_before_copy", "referenced_version_fk"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("mode-denial fixture requires nonroot")
			}
			content := "retained-v1-" + name + "\n"
			first := f.ingest(t, f.first.ProjectScopeID, content)
			job := f.enqueue(t, "script", first.Object.ObjectID, f.first.ProjectScopeID)
			f.update(t, first, "available-v2-"+name+"\n")
			claim := f.claim(t, job)
			execSQL := func(q string, args ...any) {
				t.Helper()
				if _, err := f.db.ExecContext(t.Context(), q, args...); err != nil {
					t.Fatal(err)
				}
			}
			oid, vid, bid := first.Object.ObjectID, first.LatestVersion.ObjectVersionID, first.Blob.BlobID
			restoreProject := func(id string) {
				t.Cleanup(func() {
					if _, err := f.db.ExecContext(context.Background(), `UPDATE projects.projects SET status='active' WHERE project_id=$1`, id); err != nil {
						t.Error(err)
					}
				})
			}
			var existing []byte
			var existingInfo os.FileInfo
			ctx := t.Context()
			var cancel context.CancelFunc
			switch name {
			case "object_only":
				execSQL(`UPDATE jobs.jobs SET source_object_version_id=NULL WHERE job_id=$1`, job.JobID)
				claim.Job.SourceObjectVersionID = nil
			case "version_only":
				execSQL(`UPDATE jobs.jobs SET source_object_id=NULL WHERE job_id=$1`, job.JobID)
				claim.Job.SourceObjectID = nil
			case "blank_object":
				v := ""
				claim.Job.SourceObjectID = &v
			case "blank_version":
				v := ""
				claim.Job.SourceObjectVersionID = &v
			case "malformed_object":
				v := "object_bad"
				claim.Job.SourceObjectID = &v
			case "malformed_version":
				v := "version_bad"
				claim.Job.SourceObjectVersionID = &v
			case "wrong_object_version":
				v := other.LatestVersion.ObjectVersionID
				execSQL(`UPDATE jobs.jobs SET source_object_version_id=$2 WHERE job_id=$1`, job.JobID, v)
				claim.Job.SourceObjectVersionID = &v
			case "missing_lookup":
				v := ids.NewObjectVersionID()
				claim.Job.SourceObjectVersionID = &v
			case "missing_blob":
				execSQL(`UPDATE objects.object_versions SET blob_id=NULL WHERE object_version_id=$1`, vid)
			case "missing_hash":
				execSQL(`UPDATE objects.object_versions SET content_hash=NULL WHERE object_version_id=$1`, vid)
			case "missing_size":
				execSQL(`UPDATE objects.object_versions SET size_bytes=NULL WHERE object_version_id=$1`, vid)
			case "object_archived":
				execSQL(`UPDATE objects.objects SET status='archived' WHERE object_id=$1`, oid)
			case "version_missing":
				execSQL(`UPDATE objects.object_versions SET status='missing' WHERE object_version_id=$1`, vid)
			case "blob_missing":
				execSQL(`UPDATE files.blobs SET status='missing' WHERE blob_id=$1`, bid)
			case "same_length_corruption":
				if err := os.WriteFile(first.Blob.StoragePath, []byte(strings.Replace(content, "v1", "v9", 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "truncate":
				if err := os.Truncate(first.Blob.StoragePath, 1); err != nil {
					t.Fatal(err)
				}
			case "grow":
				if err := os.WriteFile(first.Blob.StoragePath, []byte(content+"extra"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing_file":
				if err := os.Remove(first.Blob.StoragePath); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Chmod(first.Blob.StoragePath, 0); err != nil {
					t.Fatal(err)
				}
			case "nonregular":
				execSQL(`UPDATE files.blobs SET storage_path=$2 WHERE blob_id=$1`, bid, t.TempDir())
			case "outside_root":
				p := filepath.Join(t.TempDir(), "secret-password-sentinel")
				if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				execSQL(`UPDATE files.blobs SET storage_path=$2 WHERE blob_id=$1`, bid, p)
			case "symlink_leaf":
				p := filepath.Join(f.objects.Store.Root, "leaf-"+vid)
				if err := os.Symlink(first.Blob.StoragePath, p); err != nil {
					t.Fatal(err)
				}
				execSQL(`UPDATE files.blobs SET storage_path=$2 WHERE blob_id=$1`, bid, p)
			case "source_path_is_not_fallback":
				execSQL(`UPDATE files.blobs SET storage_path=$2 WHERE blob_id=$1`, bid, filepath.Join(f.objects.Store.Root, "missing-private-credential"))
				p := filepath.Join(t.TempDir(), "source-valid")
				if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				execSQL(`UPDATE objects.object_versions SET source_path=$2 WHERE object_version_id=$1`, vid, p)
			case "job_archived_after_claim":
				execSQL(`UPDATE projects.projects SET status='archived' WHERE project_id=$1`, f.first.ProjectID)
				restoreProject(f.first.ProjectID)
			case "source_home_archived_after_claim":
				execSQL(`UPDATE objects.objects SET home_scope_id=$2 WHERE object_id=$1`, oid, f.second.ProjectScopeID)
				execSQL(`UPDATE projects.projects SET status='archived' WHERE project_id=$1`, f.second.ProjectID)
				restoreProject(f.second.ProjectID)
			case "destination_file", "destination_symlink":
				if err := os.MkdirAll(claim.Attempt.InputDir, 0o700); err != nil {
					t.Fatal(err)
				}
				destination := filepath.Join(claim.Attempt.InputDir, "object")
				existing = []byte("pre-existing-owned-input")
				if name == "destination_file" {
					if err := os.WriteFile(destination, existing, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					p := filepath.Join(t.TempDir(), "untouched")
					if err := os.WriteFile(p, existing, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(p, destination); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				existingInfo, err = os.Lstat(destination)
				if err != nil {
					t.Fatal(err)
				}
			case "cancel_before_copy":
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			case "referenced_version_fk":
				_, err := f.db.ExecContext(t.Context(), `DELETE FROM objects.object_versions WHERE object_version_id=$1`, vid)
				var pgerr *pgconn.PgError
				if !errors.As(err, &pgerr) || pgerr.Code != "23503" {
					t.Fatalf("referenced version deletion was not blocked by FK: %v", err)
				}
				f.assertSuccess(t, claim, content)
				return
			}
			result, err := f.service.RunClaim(ctx, f.req, claim)
			if err == nil || !regexp.MustCompile(`^[a-z_]+$`).MatchString(err.Error()) {
				t.Fatalf("unsafe or missing categorical error: %v", err)
			}
			if result.Job.Job.Status != jobs.StatusFailed || result.Job.Job.FailureCode == nil || *result.Job.Job.FailureCode != "input_materialize_failed" || result.Job.Job.FailureMessage == nil || *result.Job.Job.FailureMessage != err.Error() {
				t.Fatalf("failure receipt mismatch: status=%s error=%v", result.Job.Job.Status, err)
			}
			if len(result.Job.Attempts) != 1 || result.Job.Attempts[0].Status != jobs.StatusFailed || result.Job.Attempts[0].ExitCode != nil || len(result.Job.Artifacts) != 0 || len(result.Job.Outputs) != 0 {
				t.Fatal("input failure recorded command/output success")
			}
			for _, path := range []string{filepath.Join(claim.Attempt.OutputDir, "launched"), filepath.Join(claim.Attempt.OutputDir, "observed.txt"), filepath.Join(claim.Attempt.ArtifactDir, "observed.txt"), filepath.Join(claim.Attempt.WorkdirPath, "result.json"), filepath.Join(claim.Attempt.WorkdirPath, "logs", "stdout.log")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("process/output unexpectedly exists: %s", filepath.Base(path))
				}
			}
			var completed int
			if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM events.events WHERE payload->>'job_id'=$1 AND event_type IN ('job.completed','script.run.completed')`, job.JobID).Scan(&completed); err != nil || completed != 0 {
				t.Fatalf("completed=%d error=%v", completed, err)
			}
			var eventsJSON string
			if err := f.db.QueryRowContext(t.Context(), `SELECT coalesce(jsonb_agg(payload)::text,'[]') FROM events.events WHERE payload->>'job_id'=$1 AND event_type IN ('job.failed','script.run.failed')`, job.JobID).Scan(&eventsJSON); err != nil {
				t.Fatal(err)
			}
			runnerLog, readErr := os.ReadFile(filepath.Join(claim.Attempt.WorkdirPath, "logs", "runner.log"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, private := range []string{first.Blob.StoragePath, "secret-password-sentinel", "missing-private-credential", "SELECT ", "openat ", "source-valid"} {
				if strings.Contains(eventsJSON, private) || strings.Contains(string(runnerLog), private) || strings.Contains(err.Error(), private) {
					t.Fatal("private failure detail leaked")
				}
			}
			entries, err := os.ReadDir(claim.Attempt.InputDir)
			if err != nil {
				t.Fatal(err)
			}
			if existing == nil {
				if len(entries) != 0 {
					t.Fatal("failed materialization left input/temp bytes")
				}
			} else {
				if len(entries) != 1 || entries[0].Name() != "object" {
					t.Fatal("no-clobber left temporary entries")
				}
				destination := filepath.Join(claim.Attempt.InputDir, "object")
				after, err := os.Lstat(destination)
				if err != nil || !os.SameFile(existingInfo, after) || existingInfo.Mode() != after.Mode() {
					t.Fatal("existing input entry changed")
				}
				got, _ := os.ReadFile(destination)
				if string(got) != string(existing) {
					t.Fatal("existing input bytes changed")
				}
			}
			if _, err := f.objects.ReadObjectVersionSource(t.Context(), other.Object.ObjectID, other.LatestVersion.ObjectVersionID); err != nil {
				t.Fatalf("independent object affected: %v", err)
			}
		})
	}
	t.Run("independent_project_executes", func(t *testing.T) {
		job := f.enqueue(t, "script", other.Object.ObjectID, f.second.ProjectScopeID)
		f.assertSuccess(t, f.claim(t, job), "independent-project-source\n")
	})
}
