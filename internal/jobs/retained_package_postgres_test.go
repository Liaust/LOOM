package jobs_test

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/workflows"
)

type retainedFixture struct {
	db                                                                   *sql.DB
	req                                                                  requestctx.Context
	service                                                              jobs.Service
	objects                                                              objects.Service
	base, box, projectRoot, sourceRoot, projectID, scopeID, repositoryID string
	local                                                                projects.RetainedSourceLocal
	source                                                               projects.RetainedSourceSelector
	producers                                                            map[string]jobs.RetainedProducerSelector
	roots                                                                map[string]string
}

func retainedWrite(t *testing.T, root, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, name)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), mode); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(p, mode); e != nil {
		t.Fatal(e)
	}
}
func retainedRegisterProject(t *testing.T, db *sql.DB, req requestctx.Context, box, repoID, role string) (projects.ProjectRepositoryReadModel, string) {
	return retainedRegisterVersionedProject(t, db, req, box, repoID, role, projects.ProjectRepositorySourceVersions{ProjectContract: projects.ProjectRepositoryProjectSchemaV04, ReposContract: projects.ProjectRepositoryReposSchemaV04}, "app")
}
func retainedRegisterVersionedProject(t *testing.T, db *sql.DB, req requestctx.Context, box, repoID, role string, pair projects.ProjectRepositorySourceVersions, memberPath string) (projects.ProjectRepositoryReadModel, string) {
	t.Helper()
	projectID := ids.NewProjectID()
	slug := "rp-" + strings.ToLower(strings.TrimPrefix(projectID, "project_"))
	root := filepath.Join(box, slug)
	identity := "  id: " + projectID + "\n"
	if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV03 {
		identity = ""
	}
	raw := "kind: loom.project\nschema_version: " + pair.ProjectContract + "\nproject:\n" + identity + "  slug: " + slug + "\n  name: Retained package fixture\n  owner_node: main\n  status: active\n"
	navigation := filepath.Join("repos", memberPath)
	if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
		raw += "resources:\n  application:\n    kind: repository\n    repository:\n      path: " + memberPath + "\n      role: " + role + "\n"
		navigation = memberPath
	} else {
		raw += "facets:\n  repos: true\n"
		retainedWrite(t, root, ".loom/contracts/repos.yaml", "kind: loom.repos\nschema_version: "+pair.ReposContract+"\nrepos:\n  status: active\n  watch_roots: []\n  members:\n    - id: "+repoID+"\n      key: application\n      path: "+memberPath+"\n      role: "+role+"\n", 0600)
	}
	retainedWrite(t, root, projectcontracts.CanonicalRootContractPath, raw, 0600)
	if e := os.MkdirAll(filepath.Join(root, navigation), 0700); e != nil {
		t.Fatal(e)
	}
	analysis := projectcontracts.Analyze(root)
	if !analysis.Report.OK || !analysis.Plan.Registerable {
		t.Fatalf("actual analysis: %+v", analysis.Report.Diagnostics)
	}
	input, e := projectregistration.BuildInput(analysis, "retained-package-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := projects.NewService(db).RegisterProjectContract(t.Context(), req, input); e != nil {
		t.Fatal(e)
	}
	model, e := projects.NewService(db).ReadProjectRepositoryState(t.Context(), slug)
	if e != nil {
		t.Fatal(e)
	}
	return model, root
}

func retainedSelector(model projects.ProjectRepositoryReadModel) projects.RetainedSourceSelector {
	return projects.RetainedSourceSelector{ProjectID: model.Project.ProjectID, RepositoryID: model.Members[0].RepositoryID, SourceBindingDigest: model.Members[0].SourceBindingDigest, LocationDigest: model.Source.LocationDigest, SourceRevision: model.Source.SourceRevision, Selection: []string{"."}}
}
func newRetainedFixture(t *testing.T) retainedFixture {
	return newRetainedVersionedFixture(t, projects.ProjectRepositorySourceVersions{ProjectContract: projects.ProjectRepositoryProjectSchemaV04, ReposContract: projects.ProjectRepositoryReposSchemaV04}, "app")
}
func newRetainedVersionedFixture(t *testing.T, pair projects.ProjectRepositorySourceVersions, memberPath string) retainedFixture {
	t.Helper()
	db, req := sourceVersionDatabase(t)
	base := t.TempDir()
	box := filepath.Join(base, "box")
	if e := os.Mkdir(box, 0700); e != nil {
		t.Fatal(e)
	}
	repoID := strings.Replace(ids.NewProjectID(), "project_", "repo_", 1)
	model, projectRoot := retainedRegisterVersionedProject(t, db, req, box, repoID, "primary", pair, memberPath)
	navigation := filepath.Join("repos", memberPath)
	if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
		navigation = memberPath
	}
	obj := objects.NewService(db, objectstore.New(filepath.Join(base, "objects")))
	service := jobs.NewService(db, filepath.Join(base, "runtime"), scripts.NewService(db), obj, artifacts.Service{DB: db, Objects: obj})
	f := retainedFixture{db: db, req: req, base: base, box: box, projectRoot: projectRoot, sourceRoot: filepath.Join(projectRoot, navigation), projectID: model.Project.ProjectID, scopeID: model.Project.ProjectScopeID, repositoryID: model.Members[0].RepositoryID, objects: obj, service: service, source: retainedSelector(model), local: projects.RetainedSourceLocal{BoxRoot: box, NodeID: req.OriginNodeID}, producers: map[string]jobs.RetainedProducerSelector{}, roots: map[string]string{}}
	retainedWrite(t, f.sourceRoot, "go.mod", "module example.org/fixture\n\ngo 1.25\n\nrequire example.org/message v1.0.0\n", 0600)
	retainedWrite(t, f.sourceRoot, "cmd/app/main.go", "package main\nimport (\"fmt\"; \"example.org/message\")\nfunc main(){fmt.Println(message.Text)}\n", 0600)
	retainedWrite(t, f.sourceRoot, "vendor/example.org/message/message.go", "package message\nconst Text = \"retained source\"\n", 0600)
	retainedWrite(t, f.sourceRoot, "vendor/modules.txt", "# example.org/message v1.0.0\n## explicit; go 1.25\nexample.org/message\n", 0600)
	retainedWrite(t, f.sourceRoot, "assets/banner.txt", "retained asset", 0600)
	// A project-root decoy proves v0.4 member paths resolve under the repos facet.
	if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
		retainedWrite(t, projectRoot, filepath.Join("repos", memberPath, "decoy.txt"), "WRONG PREFIX", 0600)
	} else {
		retainedWrite(t, projectRoot, filepath.Join(memberPath, "decoy.txt"), "WRONG ROOT", 0600)
	}
	for _, kind := range []string{"script", "workflow"} {
		root := filepath.Join(projectRoot, "producer-"+kind)
		f.roots[kind] = root
		retainedWrite(t, root, "producer.sh", "#!/bin/sh\nprintf executed > '"+filepath.Join(base, "EXECUTED")+"'\n", 0755)
		manifest := "kind: loom.script\nid: retained_" + kind + "\nname: Retained builder\nversion: 1.0.0\nentrypoint:\n  command: [\"/bin/sh\", \"producer.sh\"]\nexecution:\n  timeout_seconds: 10\n  network: false\n"
		if kind == "workflow" {
			manifest = "kind: loom.workflow\nschema_version: workflow.contract.v0.3.1\nworkflow:\n  id: retained_workflow\n  name: Retained workflow\n  version: 1.0.0\nimplementation:\n  kind: workflow\nentrypoint:\n  command: [\"/bin/sh\", \"producer.sh\"]\nexecution:\n  timeout_seconds: 10\n  network: false\n"
		}
		retainedWrite(t, root, "arbitrary-builder.contract.yaml", manifest, 0600)
		for _, name := range []string{"node_modules/ignored", ".git/config", ".direnv/ignored", ".cache/ignored", "tmp/ignored", "result/ignored", ".DS_Store"} {
			retainedWrite(t, root, name, "not retained", 0600)
		}
		if kind == "script" {
			v, e := service.Scripts.RegisterScript(t.Context(), req, scripts.RegisterInput{ManifestPath: filepath.Join(root, "arbitrary-builder.contract.yaml"), ProjectRef: f.projectID, Activate: true})
			if e != nil {
				t.Fatal(e)
			}
			f.producers[kind] = jobs.RetainedProducerSelector{Kind: kind, ProducerID: v.Script.ScriptID, VersionID: v.Version.ScriptVersionID}
		} else {
			v, e := service.Workflows.RegisterWorkflow(t.Context(), req, workflows.RegisterInput{ManifestPath: filepath.Join(root, "arbitrary-builder.contract.yaml"), ProjectRef: f.projectID, Activate: true})
			if e != nil {
				t.Fatal(e)
			}
			f.producers[kind] = jobs.RetainedProducerSelector{Kind: kind, ProducerID: v.Workflow.WorkflowID, VersionID: v.Version.WorkflowVersionID}
		}
	}
	return f
}
func (f retainedFixture) input(kind string) jobs.RetainedInputsRequest {
	return jobs.RetainedInputsRequest{Source: f.source, Producer: f.producers[kind]}
}
func (f retainedFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if e := f.db.QueryRowContext(t.Context(), query, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func (f retainedFixture) counts(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"objects.objects", "objects.object_versions", "files.blobs", "events.events", "jobs.jobs", "jobs.job_attempts", "jobs.artifacts"} {
		out[table] = f.count(t, "SELECT count(*) FROM "+table)
	}
	return out
}
func (f retainedFixture) noExecution(t *testing.T) {
	t.Helper()
	for _, table := range []string{"jobs.jobs", "jobs.job_attempts", "jobs.artifacts"} {
		if f.count(t, "SELECT count(*) FROM "+table) != 0 {
			t.Fatal("unexpected effect in " + table)
		}
	}
	if _, e := os.Stat(filepath.Join(f.base, "EXECUTED")); !os.IsNotExist(e) {
		t.Fatal("producer executed")
	}
}
func (f retainedFixture) capture(t *testing.T, kind string) jobs.RetainedInputs {
	t.Helper()
	out, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input(kind))
	if e != nil {
		t.Fatal(e)
	}
	f.noExecution(t)
	return out
}
func (f retainedFixture) members(t *testing.T, ref objects.PackageRef) map[string]string {
	t.Helper()
	source, e := f.objects.ReadObjectVersionSource(t.Context(), ref.ObjectID, ref.ObjectVersionID)
	if e != nil {
		t.Fatal(e)
	}
	name := filepath.Join(t.TempDir(), "package.tar")
	if e := f.objects.Store.MaterializeVerified(t.Context(), source.Blob, name); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(name)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	tr := tar.NewReader(file)
	out := map[string]string{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		out[h.Name] = string(b)
	}
	return out
}

func TestRetainedInputsCapturePostgres(t *testing.T) {
	for _, layout := range []struct {
		name   string
		pair   projects.ProjectRepositorySourceVersions
		member string
	}{
		{"v05_repos", projects.ProjectRepositorySourceVersions{ProjectContract: projects.ProjectRepositoryProjectSchemaV05, ReposContract: projects.ProjectRepositoryProjectSchemaV05}, "repos/app"},
		{"v05_code", projects.ProjectRepositorySourceVersions{ProjectContract: projects.ProjectRepositoryProjectSchemaV05, ReposContract: projects.ProjectRepositoryProjectSchemaV05}, "code/app"},
		{"v03_v04_control", projects.ProjectRepositorySourceVersions{ProjectContract: projects.ProjectRepositoryProjectSchemaV03, ReposContract: projects.ProjectRepositoryReposSchemaV04}, "app"},
	} {
		t.Run(layout.name, func(t *testing.T) {
			f := newRetainedVersionedFixture(t, layout.pair, layout.member)
			for _, kind := range []string{"script", "workflow"} {
				t.Run(kind, func(t *testing.T) {
					receipt := f.capture(t, kind)
					members := f.members(t, receipt.Source.Ref)
					if members["go.mod"] == "" || members["cmd/app/main.go"] == "" || members["decoy.txt"] != "" {
						t.Fatalf("schema-bound source captured wrong-prefix decoy: %v", members)
					}
					want := layout.member
					if layout.pair.ProjectContract != projects.ProjectRepositoryProjectSchemaV05 {
						want = "repos/" + layout.member
					}
					if receipt.Source.Binding.Source.MemberPath != want {
						t.Fatalf("member path=%q want=%q", receipt.Source.Binding.Source.MemberPath, want)
					}
					fresh := objects.NewService(f.db, objectstore.New(f.objects.Store.Root))
					if _, e := fresh.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, receipt.Source.Ref); e != nil {
						t.Fatal(e)
					}
				})
			}
			// Keep the wrong-prefix decoy present while the schema-selected tree
			// disappears. Existence must never select a different interpretation.
			if e := os.Rename(f.sourceRoot, f.sourceRoot+"-removed"); e != nil {
				t.Fatal(e)
			}
			before := f.counts(t)
			if _, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input("script")); e == nil {
				t.Fatal("missing schema-selected root fell back to decoy")
			}
			if !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("missing selected root wrote state")
			}
		})
	}
	t.Run("v04_retention", func(t *testing.T) {
		f := newRetainedFixture(t)
		for _, kind := range []string{"script", "workflow"} {
			t.Run(kind, func(t *testing.T) {
				before := f.counts(t)
				receipt := f.capture(t, kind)
				after := f.counts(t)
				if after["objects.objects"] != before["objects.objects"]+2 || after["objects.object_versions"] != before["objects.object_versions"]+2 || after["events.events"] <= before["events.events"] {
					t.Fatalf("before=%v after=%v", before, after)
				}
				src := f.members(t, receipt.Source.Ref)
				if src["assets/banner.txt"] != "retained asset" || src["vendor/example.org/message/message.go"] == "" || src["decoy.txt"] != "" {
					t.Fatal("wrong source tree", src)
				}
				prod := f.members(t, receipt.Producer.Ref)
				if prod["producer.sh"] == "" || prod["arbitrary-builder.contract.yaml"] == "" || len(prod) != 2 {
					t.Fatal("producer capture/exclusions", prod)
				}
				if receipt.Source.Binding.Source.MemberPath != "repos/app" {
					t.Fatal("wrong member custody path")
				}
				// Modes are additional identity; the established content hash stays exact.
				oldHash, e := scripts.HashPackage(f.roots[kind])
				if e != nil {
					t.Fatal(e)
				}
				os.Chmod(filepath.Join(f.roots[kind], "producer.sh"), 0600)
				changed := f.capture(t, kind)
				newHash, e := scripts.HashPackage(f.roots[kind])
				if e != nil || oldHash != newHash || changed.Producer.Ref.SHA256 == receipt.Producer.Ref.SHA256 {
					t.Fatal("mode/content hash semantics changed")
				}
				later := filepath.Join(f.base, "later-"+kind)
				retainedWrite(t, f.base, "later-"+kind, "later version", 0600)
				if _, e := f.objects.IngestFileVersion(t.Context(), f.req, receipt.Source.Ref.ObjectID, objects.IngestFileInput{Path: later, ScopeRef: f.scopeID}); e != nil {
					t.Fatal(e)
				}
				if _, e := f.objects.IngestFileVersion(t.Context(), f.req, receipt.Producer.Ref.ObjectID, objects.IngestFileInput{Path: later, ScopeRef: f.scopeID}); e != nil {
					t.Fatal(e)
				}
				if e := os.RemoveAll(f.roots[kind]); e != nil {
					t.Fatal(e)
				}
				// JSONB reload cannot reorder the normalized manifest string or use mutable
				// object metadata from the later appended version.
				fresh := objects.NewService(f.db, objectstore.New(f.objects.Store.Root))
				for _, want := range []objects.PackageSnapshot{receipt.Source, receipt.Producer} {
					got, e := fresh.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, want.Ref)
					if e != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("fresh exact read=%+v err=%v want=%+v", got, e, want)
					}
				}
				t.Logf("durable_counts_before=%v after=%v", before, after)
			})
		}
		// Source live tree deletion does not invalidate already retained bytes.
		input := f.source
		input.Selection = []string{"cmd", "go.mod", "vendor", "assets"}
		owner, e := projects.NewService(f.db).ResolveRetainedSource(t.Context(), f.req, f.local, input)
		if e != nil {
			t.Fatal(e)
		}
		owner.Close()
		var object, version, blob string
		var raw []byte
		if e := f.db.QueryRow(`SELECT o.object_id,v.object_version_id,v.blob_id,v.metadata->'retained_package' FROM objects.objects o JOIN objects.object_versions v USING(object_id) WHERE v.metadata->'retained_package'->'binding'->>'role'='source' LIMIT 1`).Scan(&object, &version, &blob, &raw); e != nil {
			t.Fatal(e)
		}
		var info struct {
			Package objectstore.PackageInfo `json:"package"`
		}
		if e := json.Unmarshal(raw, &info); e != nil {
			t.Fatal(e)
		}
		os.RemoveAll(f.sourceRoot)
		if _, e := f.objects.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, objects.PackageRef{PackageInfo: info.Package, ObjectID: object, ObjectVersionID: version, BlobID: blob}); e != nil {
			t.Fatal(e)
		}
		f.noExecution(t)
	})
}

func TestRetainedInputsAuthorityPostgres(t *testing.T) {
	f := newRetainedFixture(t)
	t.Run("existing_wrong_version_parent", func(t *testing.T) {
		other, e := f.service.Scripts.RegisterScript(t.Context(), f.req, scripts.RegisterInput{ManifestPath: filepath.Join(f.roots["script"], "arbitrary-builder.contract.yaml"), ProjectRef: f.projectID, SlugOverride: "other-retained-producer", Activate: true})
		if e != nil {
			t.Fatal(e)
		}
		in := f.input("script")
		in.Producer.VersionID = other.Version.ScriptVersionID
		before := f.counts(t)
		if _, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, in); e == nil || !strings.Contains(e.Error(), "parent mismatch") {
			t.Fatalf("wrong existing version parent: %v", e)
		}
		if !reflect.DeepEqual(before, f.counts(t)) {
			t.Fatal("rejected parent wrote state")
		}
	})
	for _, kind := range []string{"project", "repository", "node", "node_key", "origin", "location", "binding", "revision", "selection", "escape", "symlink", "producer_parent", "producer_version", "foreign_reference"} {
		t.Run(kind, func(t *testing.T) {
			in := f.input("script")
			local := f.local
			req := f.req
			switch kind {
			case "project":
				in.Source.ProjectID = ids.NewProjectID()
			case "repository":
				in.Source.RepositoryID = strings.Replace(ids.NewProjectID(), "project_", "repo_", 1)
			case "node":
				local.NodeID = ids.NewNodeID()
			case "node_key":
				local.NodeID = "main"
			case "origin":
				req.OriginNodeID = ids.NewNodeID()
			case "location":
				in.Source.LocationDigest = "sha256:" + strings.Repeat("0", 64)
			case "binding":
				in.Source.SourceBindingDigest = "sha256:" + strings.Repeat("0", 64)
			case "revision":
				in.Source.SourceRevision++
			case "selection":
				in.Source.Selection = nil
			case "escape":
				in.Source.Selection = []string{"../producer-script"}
			case "symlink":
				os.Symlink(t.TempDir(), filepath.Join(f.sourceRoot, "link"))
				defer os.Remove(filepath.Join(f.sourceRoot, "link"))
			case "producer_parent":
				in.Producer.ProducerID = ids.NewScriptID()
			case "producer_version":
				in.Producer.VersionID = f.producers["workflow"].VersionID
			case "foreign_reference":
				model, _ := retainedRegisterProject(t, f.db, f.req, f.box, f.repositoryID, "reference")
				in.Source = retainedSelector(model)
			}
			before := f.counts(t)
			out, e := f.service.CaptureRetainedInputs(t.Context(), req, local, in)
			if e == nil || out.Source.Ref.ObjectID != "" {
				t.Fatalf("unauthorized capture=%+v err=%v", out, e)
			}
			after := f.counts(t)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("denied capture wrote state: %v -> %v", before, after)
			}
			f.noExecution(t)
		})
	}
	for _, kind := range []string{"project_archived", "scope_archived", "revoked", "producer_inactive", "version_inactive", "scope_absent", "scope_foreign", "manifest_hash", "content_hash", "source_location_drift"} {
		t.Run(kind, func(t *testing.T) {
			var query, restore string
			var args []any
			switch kind {
			case "project_archived":
				query = `UPDATE projects.projects SET status='archived' WHERE project_id=$1`
				restore = `UPDATE projects.projects SET status='active' WHERE project_id=$1`
				args = []any{f.projectID}
			case "scope_archived":
				query = `UPDATE scopes.scopes SET status='archived' WHERE scope_id=$1`
				restore = `UPDATE scopes.scopes SET status='active' WHERE scope_id=$1`
				args = []any{f.scopeID}
			case "revoked":
				query = `UPDATE identity.actor_node_authorizations SET status='revoked' WHERE actor_id=$1`
				restore = `UPDATE identity.actor_node_authorizations SET status='active' WHERE actor_id=$1`
				args = []any{f.req.ActorID}
			case "producer_inactive":
				query = `UPDATE packages.scripts SET status='registered' WHERE script_id=$1`
				restore = `UPDATE packages.scripts SET status='active' WHERE script_id=$1`
				args = []any{f.producers["script"].ProducerID}
			case "version_inactive":
				query = `UPDATE packages.script_versions SET status='pending_review' WHERE script_version_id=$1`
				restore = `UPDATE packages.script_versions SET status='active' WHERE script_version_id=$1`
				args = []any{f.producers["script"].VersionID}
			case "scope_absent":
				query = `UPDATE packages.scripts SET owner_scope_id=NULL WHERE script_id=$1`
				restore = `UPDATE packages.scripts SET owner_scope_id=$2 WHERE script_id=$1`
				args = []any{f.producers["script"].ProducerID, f.scopeID}
			case "scope_foreign":
				other, e := projects.NewService(f.db).CreateProject(t.Context(), f.req, projects.CreateInput{Name: "Foreign", Slug: "foreign-producer", HomeNodeRef: "main"})
				if e != nil {
					t.Fatal(e)
				}
				query = `UPDATE packages.scripts SET owner_scope_id=$3 WHERE script_id=$1 AND $2::text IS NOT NULL`
				restore = `UPDATE packages.scripts SET owner_scope_id=$2 WHERE script_id=$1`
				args = []any{f.producers["script"].ProducerID, f.scopeID, other.Project.Project.ProjectScopeID}
			case "manifest_hash", "content_hash":
				column := kind
				var old string
				if e := f.db.QueryRow(`SELECT `+column+` FROM packages.script_versions WHERE script_version_id=$1`, f.producers["script"].VersionID).Scan(&old); e != nil {
					t.Fatal(e)
				}
				query = `UPDATE packages.script_versions SET ` + column + `='sha256:` + strings.Repeat("0", 64) + `' WHERE script_version_id=$1`
				restore = `UPDATE packages.script_versions SET ` + column + `=$2 WHERE script_version_id=$1`
				args = []any{f.producers["script"].VersionID, old}
			case "source_location_drift":
				query = `UPDATE projects.project_contract_registrations SET project_root=project_root||'/moved' WHERE project_id=$1`
				restore = `UPDATE projects.project_contract_registrations SET project_root=$2 WHERE project_id=$1`
				args = []any{f.projectID, f.projectRoot}
			}
			execute := func(q string) {
				t.Helper()
				used := 1
				if strings.Contains(q, "$3") {
					used = 3
				} else if strings.Contains(q, "$2") {
					used = 2
				}
				if _, e := f.db.Exec(q, args[:used]...); e != nil {
					t.Fatal(e)
				}
			}
			execute(query)
			defer execute(restore)
			out, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input("script"))
			if e == nil || out.Source.Ref.ObjectID != "" {
				t.Fatal("invalid current authority/version accepted")
			}
			f.noExecution(t)
		})
	}
	t.Run("new_activation_never_substitutes", func(t *testing.T) {
		p := filepath.Join(f.roots["script"], "arbitrary-builder.contract.yaml")
		raw, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		os.WriteFile(p, []byte(strings.Replace(string(raw), "1.0.0", "2.0.0", 1)), 0600)
		v, e := f.service.Scripts.RegisterScript(t.Context(), f.req, scripts.RegisterInput{ManifestPath: p, ProjectRef: f.projectID, Activate: true})
		if e != nil {
			t.Fatal(e)
		}
		if _, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input("script")); e == nil {
			t.Fatal("old version silently replaced")
		}
		in := f.input("script")
		in.Producer.VersionID = v.Version.ScriptVersionID
		if _, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, in); e != nil {
			t.Fatal(e)
		}
	})
}

func TestRetainedInputsFailurePostgres(t *testing.T) {
	f := newRetainedFixture(t)
	receipt := f.capture(t, "script")
	other, err := projects.NewService(f.db).CreateProject(t.Context(), f.req, projects.CreateInput{Name: "Other readable scope", Slug: "other-readable-scope", HomeNodeRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	otherScope := other.Project.Project.ProjectScopeID
	for _, kind := range []string{"object", "version", "blob", "hash", "size", "count", "content", "scope"} {
		t.Run(kind, func(t *testing.T) {
			ref := receipt.Source.Ref
			scope := f.scopeID
			switch kind {
			case "object":
				ref.ObjectID = receipt.Producer.Ref.ObjectID
			case "version":
				ref.ObjectVersionID = receipt.Producer.Ref.ObjectVersionID
			case "blob":
				ref.BlobID = receipt.Producer.Ref.BlobID
			case "hash":
				ref.SHA256 = "sha256:" + strings.Repeat("0", 64)
			case "size":
				ref.SizeBytes++
			case "count":
				ref.EntryCount++
			case "content":
				ref.ContentBytes++
			case "scope":
				scope = otherScope
			}
			before := f.counts(t)
			if _, e := f.objects.ReadPackageSnapshot(t.Context(), f.req, scope, ref); e == nil {
				t.Fatal("wrong retained tuple accepted")
			}
			if !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("failed read wrote state")
			}
		})
	}
	for _, kind := range []string{"foreign_home_scope", "missing_home_scope", "archived_scope", "revoked_read"} {
		t.Run(kind, func(t *testing.T) {
			var query, restore string
			var args []any
			switch kind {
			case "foreign_home_scope":
				query = `UPDATE objects.objects SET home_scope_id='` + otherScope + `' WHERE object_id=$1`
				restore = `UPDATE objects.objects SET home_scope_id='` + f.scopeID + `' WHERE object_id=$1`
				args = []any{receipt.Source.Ref.ObjectID}
			case "missing_home_scope":
				query = `UPDATE objects.objects SET home_scope_id=NULL WHERE object_id=$1`
				restore = `UPDATE objects.objects SET home_scope_id='` + f.scopeID + `' WHERE object_id=$1`
				args = []any{receipt.Source.Ref.ObjectID}
			case "archived_scope":
				query = `UPDATE scopes.scopes SET status='archived' WHERE scope_id=$1`
				restore = `UPDATE scopes.scopes SET status='active' WHERE scope_id=$1`
				args = []any{f.scopeID}
			case "revoked_read":
				query = `UPDATE identity.actor_node_authorizations SET status='revoked' WHERE actor_id=$1`
				restore = `UPDATE identity.actor_node_authorizations SET status='active' WHERE actor_id=$1`
				args = []any{f.req.ActorID}
			}
			if _, e := f.db.Exec(query, args...); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := f.db.Exec(restore, args...); e != nil {
					t.Error(e)
				}
			}()
			before := f.counts(t)
			if _, e := f.objects.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, receipt.Source.Ref); e == nil {
				t.Fatal("byte identity incorrectly supplied scope/read authority")
			}
			if !reflect.DeepEqual(before, f.counts(t)) {
				t.Fatal("denied read mutated records")
			}
		})
	}
	var originalMetadata []byte
	if e := f.db.QueryRow(`SELECT metadata FROM objects.object_versions WHERE object_version_id=$1`, receipt.Source.Ref.ObjectVersionID).Scan(&originalMetadata); e != nil {
		t.Fatal(e)
	}
	t.Run("metadata_and_ref_cannot_forge_archive_count", func(t *testing.T) {
		ref := receipt.Source.Ref
		ref.EntryCount++
		if _, e := f.db.Exec(`UPDATE objects.object_versions SET metadata=jsonb_set(metadata,'{retained_package,package,entry_count}',to_jsonb($2::int)) WHERE object_version_id=$1`, ref.ObjectVersionID, ref.EntryCount); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := f.db.Exec(`UPDATE objects.object_versions SET metadata=$2 WHERE object_version_id=$1`, ref.ObjectVersionID, originalMetadata); e != nil {
				t.Error(e)
			}
		}()
		if _, e := f.objects.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, ref); e == nil || !strings.Contains(e.Error(), "archive mismatch") {
			t.Fatalf("forged archive count: %v", e)
		}
	})
	for _, kind := range []string{"missing_metadata", "wrong_metadata_scope", "wrong_metadata_owner", "unknown_metadata", "wrong_kind", "version_unavailable", "object_unavailable", "blob_unverified"} {
		t.Run(kind, func(t *testing.T) {
			ref := receipt.Source.Ref
			var q, restore string
			args := []any{ref.ObjectVersionID, originalMetadata}
			switch kind {
			case "missing_metadata":
				q = `UPDATE objects.object_versions SET metadata='{}' WHERE object_version_id=$1`
				restore = `UPDATE objects.object_versions SET metadata=$2 WHERE object_version_id=$1`
			case "wrong_metadata_scope":
				q = `UPDATE objects.object_versions SET metadata=jsonb_set(metadata,'{retained_package,binding,scope_id}',to_jsonb('` + ids.NewScopeID() + `'::text)) WHERE object_version_id=$1`
				restore = `UPDATE objects.object_versions SET metadata=$2 WHERE object_version_id=$1`
			case "wrong_metadata_owner":
				q = `UPDATE objects.object_versions SET metadata=jsonb_set(metadata,'{retained_package,binding,owner_node_id}',to_jsonb('` + ids.NewNodeID() + `'::text)) WHERE object_version_id=$1`
				restore = `UPDATE objects.object_versions SET metadata=$2 WHERE object_version_id=$1`
			case "unknown_metadata":
				q = `UPDATE objects.object_versions SET metadata=jsonb_set(metadata,'{retained_package,unknown}','true') WHERE object_version_id=$1`
				restore = `UPDATE objects.object_versions SET metadata=$2 WHERE object_version_id=$1`
			case "wrong_kind":
				q = `UPDATE objects.objects SET object_type='file' WHERE object_id=$1`
				restore = `UPDATE objects.objects SET object_type='package' WHERE object_id=$1`
				args = []any{ref.ObjectID}
			case "version_unavailable":
				q = `UPDATE objects.object_versions SET status='missing' WHERE object_version_id=$1`
				restore = `UPDATE objects.object_versions SET status='active' WHERE object_version_id=$1`
				args = []any{ref.ObjectVersionID}
			case "object_unavailable":
				q = `UPDATE objects.objects SET status='archived' WHERE object_id=$1`
				restore = `UPDATE objects.objects SET status='active' WHERE object_id=$1`
				args = []any{ref.ObjectID}
			case "blob_unverified":
				q = `UPDATE files.blobs SET status='missing' WHERE blob_id=$1`
				restore = `UPDATE files.blobs SET status='verified' WHERE blob_id=$1`
				args = []any{ref.BlobID}
			}
			if _, e := f.db.Exec(q, args[:1]...); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := f.db.Exec(restore, args...); e != nil {
					t.Error(e)
				}
			}()
			if _, e := f.objects.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, ref); e == nil {
				t.Fatal("invalid retained metadata/state accepted")
			}
		})
	}
	t.Run("existing_hash_corruption", func(t *testing.T) {
		source, e := f.objects.ReadObjectVersionSource(t.Context(), receipt.Source.Ref.ObjectID, receipt.Source.Ref.ObjectVersionID)
		if e != nil {
			t.Fatal(e)
		}
		p := source.Blob.StoragePath
		original, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		bad := append([]byte{}, original...)
		bad[0] ^= 1
		if e := os.WriteFile(p, bad, 0600); e != nil {
			t.Fatal(e)
		}
		defer os.WriteFile(p, original, 0600)
		if _, e := f.objects.ReadPackageSnapshot(t.Context(), f.req, f.scopeID, receipt.Source.Ref); e == nil {
			t.Fatal("corrupt bytes read")
		}
		out, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input("script"))
		if e == nil || out.Source.Ref.ObjectID != "" {
			t.Fatal("preexisting corrupt deduplicated blob admitted")
		}
		got, _ := os.ReadFile(p)
		if !reflect.DeepEqual(got, bad) {
			t.Fatal("capture repaired or replaced existing blob")
		}
		f.noExecution(t)
	})
	t.Run("second_capture_failure", func(t *testing.T) {
		before := f.counts(t)
		p := filepath.Join(f.roots["script"], "producer.sh")
		original, _ := os.ReadFile(p)
		defer os.WriteFile(p, original, 0755)
		retainedWrite(t, f.roots["script"], "producer.sh", "changed", 0755)
		out, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input("script"))
		if e == nil || out.Source.Ref.ObjectID != "" || out.Producer.Ref.ObjectID != "" {
			t.Fatal("partial pair returned")
		}
		after := f.counts(t)
		if after["objects.objects"] != before["objects.objects"]+1 || after["objects.object_versions"] != before["objects.object_versions"]+1 {
			t.Fatalf("partial count=%v -> %v", before, after)
		}
		f.noExecution(t)
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before := f.counts(t)
		if _, e := f.service.CaptureRetainedInputs(ctx, f.req, f.local, f.input("workflow")); e == nil {
			t.Fatal("cancel accepted")
		}
		if !reflect.DeepEqual(before, f.counts(t)) {
			t.Fatal("cancelled call wrote state")
		}
		f.noExecution(t)
	})
	t.Run("producer_link_not_silently_skipped", func(t *testing.T) {
		link := filepath.Join(f.roots["workflow"], "link")
		os.Symlink("producer.sh", link)
		defer os.Remove(link)
		if _, e := f.service.CaptureRetainedInputs(t.Context(), f.req, f.local, f.input("workflow")); e == nil {
			t.Fatal("producer link silently skipped")
		}
		f.noExecution(t)
	})
	t.Logf("durable final counts=%v", f.counts(t))
}

// These use the original guarded fixture and exact required top-level cases.
// Enabled explicitly for the repeated retained-package gate; never point them at
// a shared database or broaden the existing fixture URL/server guard.
func TestRetainedPackagePostgres(t *testing.T) {
	t.Run("capture", TestRetainedInputsCapturePostgres)
	t.Run("authority", TestRetainedInputsAuthorityPostgres)
	t.Run("failure", TestRetainedInputsFailurePostgres)
}
