package projectapply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	ab "loom.local/loom/internal/applicationbuild"
	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

type sourceApplicationBuilderFixture struct {
	inner                       *ab.Service
	snapshots, builds, commands int
	last                        ab.Input
	buildError                  error
}

func (b *sourceApplicationBuilderFixture) Snapshot(ctx context.Context, in ab.Input) (ab.Snapshot, error) {
	b.snapshots++
	b.last = in
	return b.inner.Snapshot(ctx, in)
}

func (b *sourceApplicationBuilderFixture) Build(ctx context.Context, in ab.Input) (ab.Output, error) {
	b.builds++
	b.last = in
	if b.buildError != nil {
		return ab.Output{}, b.buildError
	}
	return b.inner.Build(ctx, in)
}

type sourceApplicationPlannerFixture struct {
	facts          sr.ApplicationPrerequisiteSnapshot
	queries, plans int
	request        sr.ApplicationProvisionRequest
	change         func(*sr.ApplicationProvisionPlan)
	err            error
}

func (h *sourceApplicationPlannerFixture) QueryPrerequisites(_ context.Context, q sr.ApplicationPrerequisiteQuery) (sr.ApplicationPrerequisiteSnapshot, error) {
	h.queries++
	if q.Owner != h.facts.Owner {
		return sr.ApplicationPrerequisiteSnapshot{}, errors.New("unexpected owner")
	}
	return h.facts, h.err
}

func (h *sourceApplicationPlannerFixture) PlanProvision(_ context.Context, q sr.ApplicationProvisionRequest) (sr.ApplicationProvisionPlan, error) {
	h.plans++
	h.request = q
	if h.err != nil {
		return sr.ApplicationProvisionPlan{}, h.err
	}
	q.StateIdentities = map[string]sr.ApplicationProjectStatePolicy{}
	for key, relative := range q.ProjectState {
		q.StateIdentities[key] = sr.ApplicationProjectStatePolicy{ProjectRoot: q.ProjectRoot, RelativePath: relative, Owner: q.Owner,
			ProjectUID: uint32(os.Geteuid()), ProjectGID: uint32(os.Getegid()),
			RootIdentity: sr.ApplicationStatePathIdentity{Inode: 10, Directory: true}, DirectoryIdentity: sr.ApplicationStatePathIdentity{Inode: 11, Directory: true}}
	}
	p := sr.ApplicationProvisionPlan{SchemaVersion: sr.ApplicationProvisioningSchema, Request: q,
		PolicyRevision: "sha256:" + strings.Repeat("a", 64), GrantRevision: "sha256:" + strings.Repeat("b", 64)}
	if h.change != nil {
		h.change(&p)
	}
	sourceApplicationPlanID(&p)
	return p, nil
}

func sourceApplicationPlanID(p *sr.ApplicationProvisionPlan) {
	p.PlanID = ""
	raw, _ := json.Marshal(p)
	p.PlanID = hashBytes(raw)
}

func sourceApplicationFixture(t *testing.T) (*SourceApplicationAdapter, pc.DeclarationTarget, pc.ApplicationDeclaration, *sourceApplicationBuilderFixture, *sourceApplicationPlannerFixture) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceApplicationWrite(t, filepath.Join(root, "app", "__init__.py"), "")
	sourceApplicationWrite(t, filepath.Join(root, "app", "main.py"), "print('service')\n")
	sourceApplicationWrite(t, filepath.Join(root, ".data", "private.db"), "PRIVATE_SENTINEL")
	b := &sourceApplicationBuilderFixture{}
	b.inner, err = ab.NewService(ab.Options{WorkDir: filepath.Join(backend, "build"), NixpkgsPath: "/nix/store/cccccccccccccccccccccccccccccccc-nixpkgs", Platform: "x86_64-linux",
		Runner: ab.CommandRunnerFunc(func(_ context.Context, command ab.Command) ([]byte, error) {
			b.commands++
			root := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-application"
			if slices.Contains(command.Args, "build") {
				return []byte(`[{"outputs":{"out":"` + root + `"}}]`), nil
			}
			if slices.Contains(command.Args, "eval") {
				return []byte(root + "/bin/python3.13"), nil
			}
			return []byte(`{"` + root + `":{"narHash":"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}`), nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	target := pc.DeclarationTarget{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", OwnerNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", ProjectRoot: root, LocationRevision: "location-1"}
	d := pc.ApplicationDeclaration{Source: &pc.ApplicationSource{Path: ".", PythonModule: "app.main"},
		Config:            &pc.ApplicationSourceConfig{Values: map[string]any{"large": json.Number("9007199254740993"), "enabled": false}},
		Data:              map[pc.ResourceKey]pc.ApplicationData{"database": {Path: ".data", Backup: "cloud_history", Capacity: &pc.ApplicationCapacity{PlannedBytes: 1024}}},
		CredentialSources: map[string]string{"oauth": "pass://share-id/item-id/password"}}
	h := &sourceApplicationPlannerFixture{facts: sr.ApplicationPrerequisiteSnapshot{SchemaVersion: sr.ApplicationPrerequisiteSchema,
		Owner: sr.ApplicationOwner{ProjectID: target.ProjectID, NodeID: target.OwnerNodeID, Resource: "intake"}, GrantMissing: true,
		Platform: "x86_64-linux", Data: map[string]sr.ApplicationPrerequisiteData{}, Credentials: map[string]sr.ApplicationPrerequisiteCredential{}}}
	a := &SourceApplicationAdapter{Builder: b, Helper: h, OutputRoot: filepath.Join(backend, "retained"), MemoryMaxBytes: 128 * 1024 * 1024}
	return a, target, d, b, h
}

func sourceApplicationWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func sourceApplicationSnapshot(t *testing.T, a *SourceApplicationAdapter, target pc.DeclarationTarget, d pc.ApplicationDeclaration) applicationSourcePayload {
	t.Helper()
	p, err := a.Snapshot(t.Context(), target, "intake", d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sourceApplicationFailureCause(t *testing.T, err error, code pc.DeclarationErrorCode, cause string) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) || f.Code != code || f.Cause != cause {
		t.Fatalf("failure = %v, expected %s/%s", err, code, cause)
	}
}

func TestSourceApplicationSnapshotIsPureAndNormalizesBindings(t *testing.T) {
	a, target, d, b, h := sourceApplicationFixture(t)
	p := sourceApplicationSnapshot(t, a, target, d)
	if p.ExpectedSourceDigest != p.SourceSnapshot.SourceDigest || b.snapshots != 1 || b.builds != 0 || b.commands != 0 || h.plans != 1 || h.queries != 1 {
		t.Fatal("plan built or lost snapshot")
	}
	q := p.ProvisionPlan.Request
	if q.RepositoryID != "" || q.SourceIdentity != target.ProjectID+"/intake" || q.Network != sr.ApplicationNetworkLoopback || q.ProjectRoot != target.ProjectRoot || q.ProjectState["database"] != ".data" || q.StateIdentities["database"].DirectoryIdentity.Inode != 11 || q.Data["database"] != "application.intake.database" || q.CredentialSources["application.intake.oauth"] != d.CredentialSources["oauth"] || !q.CloudBackup {
		t.Fatalf("normalized provision request: %#v", q)
	}
	if _, err := os.Stat(a.OutputRoot); !os.IsNotExist(err) {
		t.Fatalf("plan wrote cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(a.OutputRoot), "build")); !os.IsNotExist(err) {
		t.Fatalf("plan wrote producer staging: %v", err)
	}
	d.Config.Values["large"] = int64(1)
	if p.Declaration.Config.Values["large"].(json.Number).String() != "9007199254740993" {
		t.Fatal("payload aliases declaration")
	}
	raw, _ := json.Marshal(p)
	var decoded applicationSourcePayload
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Declaration.Config.Values["large"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("payload lost exact integer: %v", err)
	}
}

func TestSourceApplicationSnapshotExcludesObservationChurn(t *testing.T) {
	a, target, d, _, helper := sourceApplicationFixture(t)
	helper.facts.CollectedAt = time.Now().UTC()
	helper.facts.Observation = &sr.ApplicationPrerequisiteObservation{ObservedAt: time.Now().UTC(), State: "starting"}
	first := sourceApplicationSnapshot(t, a, target, d)
	helper.facts.CollectedAt = helper.facts.CollectedAt.Add(time.Second)
	helper.facts.Observation = &sr.ApplicationPrerequisiteObservation{ObservedAt: time.Now().UTC(), State: "observed"}
	second := sourceApplicationSnapshot(t, a, target, d)
	aRaw, _ := canonicalValue(first)
	bRaw, _ := canonicalValue(second)
	if !bytes.Equal(aRaw, bRaw) {
		t.Fatal("unchanged deployment became stale because of observation timing")
	}
	helper.facts.IdentityRevision = "changed-prerequisite"
	third := sourceApplicationSnapshot(t, a, target, d)
	cRaw, _ := canonicalValue(third)
	if bytes.Equal(aRaw, cRaw) {
		t.Fatal("actual prerequisite change disappeared")
	}
}

func TestSourceApplicationOwnerRechecksFullReviewedClosure(t *testing.T) {
	a, target, _, builder, _ := sourceApplicationFixture(t)
	sourceApplicationWrite(t, filepath.Join(target.ProjectRoot, "notes", "agent.md"), "Project instructions")
	sourceApplicationWrite(t, filepath.Join(target.ProjectRoot, ".loom", "project.yaml"), `kind: loom.project
schema_version: project.contract.v0.6
project: {id: `+target.ProjectID+`, slug: source-worker, name: Source worker, owner_node: main}
resources:
  intake:
    kind: application
    application: {source: {path: ., python_module: app.main}}
  helper:
    kind: agent
    agent: {instructions_ref: notes/agent.md, model: local, provider: ollama}
`)
	x := localDeclaration{Target: target, Analysis: pc.Analyze(target.ProjectRoot)}
	if !x.Analysis.Report.OK {
		t.Fatal(x.Analysis.Report.Diagnostics)
	}
	p := Principal{ActorID: "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV", OriginNodeID: target.OwnerNodeID}
	reviewed := localPrerequisites(p, x)
	snapshot, err := builder.Snapshot(t.Context(), ab.Input{SourceRoot: target.ProjectRoot, PythonModule: "app.main", Source: ab.SourceIdentity{ProjectID: target.ProjectID, Resource: "intake"}})
	if err != nil {
		t.Fatal(err)
	}
	reviewed.Sources = append(reviewed.Sources, sourceApplicationEvidence("intake", snapshot.SourceDigest))
	reviewed.Sources = append(reviewed.Sources, sourceApplicationProducerEvidence("intake", snapshot.ProducerDigest))
	slices.SortFunc(reviewed.Sources, func(a, b pc.DeclarationSource) int { return strings.Compare(a.Ref, b.Ref) })
	r := &LocalResolver{SourceApplications: a}
	current, err := r.currentPrerequisitesFor(t.Context(), p, x, reviewed)
	if err != nil || !same(current, reviewed) {
		t.Fatalf("owner lost reviewed package source or ordering: %v", err)
	}
	sourceApplicationWrite(t, filepath.Join(target.ProjectRoot, "app", "main.py"), "print('changed')\n")
	current, err = r.currentPrerequisitesFor(t.Context(), p, x, reviewed)
	if err != nil || same(current, reviewed) {
		t.Fatalf("changed source was not fenced: %v", err)
	}
	if builder.builds != 0 {
		t.Fatal("owner fence built the application")
	}
}

func TestSourceApplicationResolveBuildAndRestart(t *testing.T) {
	a, target, d, b, h := sourceApplicationFixture(t)
	p := sourceApplicationSnapshot(t, a, target, d)
	if _, err := a.Resolve(t.Context(), p, false); !errors.Is(err, ErrSourceApplicationOutputMissing) {
		t.Fatalf("missing output: %v", err)
	}
	if b.snapshots != 1 || b.builds != 0 {
		t.Fatal("observe invoked producer")
	}
	first, err := a.Resolve(t.Context(), p, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest, descriptor := first.Request.Manifest, first.Preparation.Descriptor
	if descriptor.SchemaVersion != "application.artifact.v2" || descriptor.RepositoryID != "" || descriptor.SourceIdentity != target.ProjectID+"/intake" || descriptor.Artifact != manifest.Artifact || descriptor.SourceDigest != p.ExpectedSourceDigest || descriptor.Validate() != nil || validateApplicationPreparation(first) != nil {
		t.Fatal("invalid project-backed materialization")
	}
	if manifest.Process.User != "app" || manifest.Process.Manager != sr.ManagerSystemd || manifest.Process.Restart != "on-failure" || manifest.Process.MemoryMaxBytes != a.MemoryMaxBytes || manifest.Health.Kind != sr.HealthKindManager || manifest.Listener != nil {
		t.Fatalf("wrong worker defaults: %#v", manifest)
	}
	if *manifest.Config.Values["large"].Integer != 9007199254740993 || *manifest.Config.Values["database"].DataRef != "database" || *manifest.Config.Values["oauth"].CredentialRef != "application.intake.oauth" {
		t.Fatal("inferred typed config mismatch")
	}
	if first.Request.Data["database"].BindingRef != "application.intake.database" || first.Request.Data["database"].Backup != "cloud_history" || first.Request.Data["database"].PlannedBytes != 1024 || first.Request.CredentialRevisions["application.intake.oauth"] != sr.ApplicationProtonCredentialRevision(d.CredentialSources["oauth"]) {
		t.Fatal("data/credential requirements lost")
	}
	if b.builds != 1 || b.last.ExpectedSourceDigest != p.ExpectedSourceDigest || h.plans != 1 {
		t.Fatal("apply replanned or did not fence build")
	}
	entries, err := os.ReadDir(a.OutputRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache artifacts: %v %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(a.OutputRoot, entries[0].Name()))
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("cache output not immutable: %v", err)
	}
	// A restarted read does not require a producer, helper or existing live code.
	restarted := &SourceApplicationAdapter{OutputRoot: a.OutputRoot}
	if err := os.Rename(target.ProjectRoot, target.ProjectRoot+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(target.ProjectRoot+"-moved", target.ProjectRoot) })
	second, err := restarted.Resolve(t.Context(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := canonicalValue(first)
	right, _ := canonicalValue(second)
	if !bytes.Equal(left, right) || b.builds != 1 || h.plans != 1 {
		t.Fatal("restart changed materialized request")
	}
}

func TestSourceApplicationCacheKeysExactConfigAndSource(t *testing.T) {
	a, target, d, b, _ := sourceApplicationFixture(t)
	p := sourceApplicationSnapshot(t, a, target, d)
	if _, err := a.Resolve(t.Context(), p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resolve(t.Context(), p, true); err != nil || b.builds != 1 {
		t.Fatalf("unchanged output rebuilt: %v", err)
	}
	p.Declaration.Config.Values["large"] = json.Number("9007199254740994")
	if _, err := a.Resolve(t.Context(), p, false); !errors.Is(err, ErrSourceApplicationOutputMissing) {
		t.Fatalf("changed config reused: %v", err)
	}
	changed, err := a.Resolve(t.Context(), p, true)
	if err != nil || *changed.Request.Manifest.Config.Values["large"].Integer != 9007199254740994 || b.builds != 2 {
		t.Fatalf("changed config lost: %v", err)
	}
	sourceApplicationWrite(t, filepath.Join(target.ProjectRoot, "app", "main.py"), "print('new')\n")
	_, err = a.Resolve(t.Context(), p, true)
	sourceApplicationFailureCause(t, err, pc.DeclarationPlanStale, "application_source_changed")
	if b.builds != 2 {
		t.Fatal("changed source reached build")
	}
	if _, err := a.Resolve(t.Context(), p, false); err != nil {
		t.Fatalf("retained observation depended on live source: %v", err)
	}
}

func TestSourceApplicationPlanChecksNetworkAndStateIdentity(t *testing.T) {
	for _, change := range []func(*sr.ApplicationProvisionPlan){
		func(p *sr.ApplicationProvisionPlan) { p.Request.Network = sr.ApplicationNetworkOutboundHTTPS },
		func(p *sr.ApplicationProvisionPlan) { p.Request.ProjectState["database"] = ".other" },
		func(p *sr.ApplicationProvisionPlan) {
			p.Request.StateIdentities = nil
			p.Request.ProjectState = nil
			p.Request.ProjectRoot = ""
		},
		func(p *sr.ApplicationProvisionPlan) { p.Request.Owner.Resource = "foreign" },
		func(p *sr.ApplicationProvisionPlan) {
			p.Request.SourceIdentity = p.Request.Owner.ProjectID + "/foreign"
		},
		func(p *sr.ApplicationProvisionPlan) {
			p.Request.CredentialSources = map[string]string{"application.foreign.oauth": "pass://share-id/item-id/password"}
		},
		func(p *sr.ApplicationProvisionPlan) { p.Request.CloudBackup = false },
	} {
		a, target, d, b, h := sourceApplicationFixture(t)
		h.change = change
		if _, err := a.Snapshot(t.Context(), target, "intake", d); err == nil || b.builds != 0 {
			t.Fatal("substituted plan accepted")
		}
	}
	a, target, d, _, _ := sourceApplicationFixture(t)
	d.Network = "outbound_https"
	p := sourceApplicationSnapshot(t, a, target, d)
	if p.ProvisionPlan.Request.Network != sr.ApplicationNetworkOutboundHTTPS {
		t.Fatal("outbound request became loopback")
	}
	if _, err := a.Resolve(t.Context(), p, true); err != nil {
		t.Fatal(err)
	}
}

func TestSourceApplicationRetainedOutputCustody(t *testing.T) {
	for _, kind := range []string{"root-link", "ancestor-link", "public-root", "entry-link", "hardlink", "writable-entry", "corrupt-entry", "inside-project"} {
		t.Run(kind, func(t *testing.T) {
			a, target, d, b, _ := sourceApplicationFixture(t)
			p := sourceApplicationSnapshot(t, a, target, d)
			if kind == "inside-project" {
				a.OutputRoot = filepath.Join(target.ProjectRoot, "retained")
			} else if kind == "ancestor-link" {
				parent := filepath.Dir(a.OutputRoot)
				alias := filepath.Join(parent, "alias")
				if err := os.Symlink(parent, alias); err != nil {
					t.Fatal(err)
				}
				a.OutputRoot = filepath.Join(alias, "retained")
			} else if kind == "root-link" {
				elsewhere := t.TempDir()
				if err := os.Symlink(elsewhere, a.OutputRoot); err != nil {
					t.Fatal(err)
				}
			} else if kind == "public-root" {
				if err := os.Mkdir(a.OutputRoot, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := a.Resolve(t.Context(), p, true); err != nil {
					t.Fatal(err)
				}
				entries, _ := os.ReadDir(a.OutputRoot)
				path := filepath.Join(a.OutputRoot, entries[0].Name())
				switch kind {
				case "entry-link":
					if err := os.Rename(path, path+"-original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+"-original", path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, path+"-linked"); err != nil {
						t.Fatal(err)
					}
				case "writable-entry":
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
				case "corrupt-entry":
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(`{"schema_version":"forged"}`), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0400); err != nil {
						t.Fatal(err)
					}
				}
			}
			builds := b.builds
			if _, err := a.Resolve(t.Context(), p, true); err == nil || b.builds != builds {
				t.Fatal("unsafe cache caused success/rebuild")
			}
		})
	}
}

func TestSourceApplicationCancellationAndPendingOutput(t *testing.T) {
	a, target, d, b, _ := sourceApplicationFixture(t)
	p := sourceApplicationSnapshot(t, a, target, d)
	b.buildError = context.Canceled
	if _, err := a.Resolve(t.Context(), p, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost build cancellation: %v", err)
	}
	if _, err := os.Stat(a.OutputRoot); !os.IsNotExist(err) {
		t.Fatalf("cancelled output retained: %v", err)
	}
	root, err := openSourceOutputRoot(a.OutputRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := unix.Flock(int(root.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(root.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.Resolve(ctx, p, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cache lock ignored cancellation: %v", err)
	}
}

func TestSourceApplicationMemoryCeilingAndAllocatedData(t *testing.T) {
	a, target, d, _, _ := sourceApplicationFixture(t)
	a.MemoryMaxBytes = 1024 * 1024 * 1024
	data := d.Data["database"]
	data.Path, data.BindingRef = "", "existing.database"
	d.Data["database"] = data
	p := sourceApplicationSnapshot(t, a, target, d)
	if len(p.ProvisionPlan.Request.ProjectState) != 0 || p.ProvisionPlan.Request.ProjectRoot != "" || p.ProvisionPlan.Request.Data["database"] != "existing.database" {
		t.Fatal("allocated data silently adopted")
	}
	result, err := a.Resolve(t.Context(), p, true)
	if err != nil || result.Request.Manifest.Process.MemoryMaxBytes != sourceDefaultMemoryBytes {
		t.Fatalf("default memory ceiling: %v", err)
	}
}

func TestSourceApplicationActionsMaterializeAndChangeCacheKey(t *testing.T) {
	a, target, d, b, _ := sourceApplicationFixture(t)
	sourceApplicationWrite(t, filepath.Join(target.ProjectRoot, "app", "bootstrap.py"), "print('bootstrap')\n")
	d.Actions = map[string]pc.ApplicationAction{
		"bootstrap": {PythonModule: "app.bootstrap", Args: []pc.ApplicationArgument{pc.LiteralApplicationArgument("--history"), pc.LiteralApplicationArgument("approved")}},
		"inspect":   {PythonModule: "app.main", ReadOnly: true},
	}
	p := sourceApplicationSnapshot(t, a, target, d)
	first, err := a.Resolve(t.Context(), p, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Preparation.Descriptor.Actions) != 2 || first.Preparation.Descriptor.Actions["bootstrap"].ReadOnly || !first.Preparation.Descriptor.Actions["inspect"].ReadOnly || !slices.Equal(b.last.Actions["bootstrap"].Args, []string{"--history", "approved"}) || first.Preparation.Descriptor.Validate() != nil {
		t.Fatal("signed action policy lost")
	}
	if _, err := a.Resolve(t.Context(), p, false); err != nil {
		t.Fatal(err)
	}
	action := p.Declaration.Actions["bootstrap"]
	action.Args = []pc.ApplicationArgument{pc.LiteralApplicationArgument("--history"), pc.LiteralApplicationArgument("different")}
	p.Declaration.Actions["bootstrap"] = action
	if _, err := a.Resolve(t.Context(), p, false); !errors.Is(err, ErrSourceApplicationOutputMissing) {
		t.Fatalf("changed action arguments reused output: %v", err)
	}
	second, err := a.Resolve(t.Context(), p, true)
	if err != nil || second.Request.DescriptorDigest == first.Request.DescriptorDigest || b.builds != 2 {
		t.Fatalf("changed action not rebuilt: %v", err)
	}
}

func TestSourceApplicationPreservesNativeAndExplicitRunContracts(t *testing.T) {
	_, target, d, _, _ := sourceApplicationFixture(t)
	for _, change := range []func(*pc.ApplicationDeclaration){
		func(d *pc.ApplicationDeclaration) {
			d.Source.PythonModule = ""
			d.Source.Build = &pc.ApplicationBuild{Kind: "cargo"}
			d.Run = &pc.ApplicationRun{Binary: "server"}
		},
		func(d *pc.ApplicationDeclaration) {
			d.Source.PythonModule = ""
			d.Source.NixAttribute = "server"
		},
		func(d *pc.ApplicationDeclaration) {
			d.Run = &pc.ApplicationRun{Args: []pc.ApplicationArgument{pc.LiteralApplicationArgument("serve")}}
		},
		func(d *pc.ApplicationDeclaration) {
			d.Actions = map[string]pc.ApplicationAction{"status": {
				PythonModule: d.Source.PythonModule, Args: []pc.ApplicationArgument{{ConfigFile: true}}, ReadOnly: true,
			}}
		},
	} {
		next := d
		source := *d.Source
		next.Source = &source
		change(&next)
		if code := pc.ValidateApplicationSource(next); code != "" {
			t.Fatalf("test contract invalid: %s", code)
		}
		in, err := sourceApplicationInput(target, "intake", next)
		if next.Run == nil && next.Source.PythonModule != "" {
			if err == nil {
				t.Fatal("legacy ABI cannot silently interpret config-file tokens")
			}
			continue
		}
		if err != nil || !same(in.Build, next.Source.Build) || !same(in.Run, next.Run) || in.NixAttribute != next.Source.NixAttribute {
			t.Fatalf("producer selectors lost: %v", err)
		}
	}
}
