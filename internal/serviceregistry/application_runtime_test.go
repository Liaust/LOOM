package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
	pc "loom.local/loom/internal/projectcontracts"
)

type applicationFixtureHost struct {
	mu          sync.Mutex
	root        string
	restarts    int
	generation  ApplicationGeneration
	process     ApplicationProcessObservation
	failRestart bool
	failReload  bool
	loaded      string
	reloads     int
	removals    int
	failRemoval bool
}

func (f *applicationFixtureHost) VerifyArtifact(_ context.Context, d ApplicationArtifactDescriptor) error {
	return d.Validate()
}
func (f *applicationFixtureHost) EnsureAccount(_ context.Context, o ApplicationOwner, _ uint32, _ bool) error {
	return os.WriteFile(filepath.Join(f.root, "account-"+o.Instance()), []byte(o.Instance()), 0600)
}
func (f *applicationFixtureHost) EnsureData(_ context.Context, p ApplicationDataPolicy, uid uint32, prior *ApplicationDataIdentity) (ApplicationDataIdentity, error) {
	if prior == nil {
		if e := os.Mkdir(p.Path, 0700); e != nil && !os.IsExist(e) {
			return ApplicationDataIdentity{}, e
		}
	}
	var st unix.Stat_t
	if unix.Lstat(p.Path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ApplicationDataIdentity{}, applicationError("data.custody")
	}
	var poolStat unix.Stat_t
	if err := unix.Lstat(p.Pool, &poolStat); err != nil {
		return ApplicationDataIdentity{}, err
	}
	actual := ApplicationDataIdentity{Filesystem: "fixture-fs", PoolInode: poolStat.Ino, Path: p.Path, Device: uint64(st.Dev), Inode: st.Ino, UID: uid, GID: uid, Mount: p.Pool}
	if prior != nil && !applicationSameDataIdentity(*prior, actual) {
		return actual, applicationError("data.identity_migration_required")
	}
	return actual, nil
}
func (f *applicationFixtureHost) ConfigurationPath(g ApplicationGeneration) string {
	return filepath.Join(f.root, "configs", g.Owner.Instance(), g.ID, "config.json")
}
func (f *applicationFixtureHost) PublishGeneration(_ context.Context, g ApplicationGeneration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation = g
	f.reloads++
	if f.failReload {
		return applicationError("fixture.reload_failed")
	}
	f.loaded = g.ID
	return nil
}
func (f *applicationFixtureHost) GenerationPublished(g ApplicationGeneration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation.ID == g.ID && f.loaded == g.ID
}
func (f *applicationFixtureHost) Observe(_ context.Context, _ ApplicationGeneration) (ApplicationProcessObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.process, nil
}
func (f *applicationFixtureHost) Restart(_ context.Context, g ApplicationGeneration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	if f.failRestart {
		return applicationError("fixture.startup_failure")
	}
	f.process = ApplicationProcessObservation{ListenerOwned: g.Listener != nil, State: "active", PID: 42, UID: g.UID, InvocationID: strings.Repeat("a", 32), Executable: g.Descriptor.Executable, ControlGroup: "/system.slice/" + g.Owner.Unit(), Generation: g.ID}
	return nil
}
func (f *applicationFixtureHost) Start(ctx context.Context, g ApplicationGeneration) error {
	return f.Restart(ctx, g)
}
func (f *applicationFixtureHost) Stop(_ context.Context, _ ApplicationGeneration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.process.State = "inactive"
	return nil
}
func (f *applicationFixtureHost) RemoveManagement(_ context.Context, _ ApplicationGeneration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation = ApplicationGeneration{}
	f.removals++
	if f.failRemoval {
		return applicationError("fixture.removal_reload_failed")
	}
	return nil
}
func (f *applicationFixtureHost) PublishEdge(context.Context, ApplicationGeneration, []byte) error {
	return nil
}
func applicationTestDescriptor() ApplicationArtifactDescriptor {
	root := "/nix/store/00000000000000000000000000000000-fixture"
	d := ApplicationArtifactDescriptor{SchemaVersion: "application.artifact.v1", RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", SourceRevision: strings.Repeat("a", 40), SourceDigest: "sha256:" + strings.Repeat("1", 64), Artifact: ApplicationArtifact{Ref: "fixture.app", Digest: "sha256:" + strings.Repeat("2", 64), Platform: "x86_64-linux"}, StoreRoot: root, Launcher: root + "/bin/loom-application-launcher", Executable: root + "/bin/loom-application-launcher", Closure: []ApplicationClosurePath{{Path: root, NARHash: "sha256-" + strings.Repeat("A", 43) + "="}}, Configuration: ApplicationConfigurationSchema{Schema: "fixture.schema", Parameters: map[string]ApplicationParameterSpec{"large": {Type: ApplicationInteger, Required: true}}}, DataFormat: "fixture-v1", CompatibleDataFormats: []string{"fixture-v1"}}
	d.ClosureDigest = applicationSHA(d.Closure)
	d.ConfigurationDigest = applicationSHA(d.Configuration)
	return d
}
func applicationTestRuntime(t *testing.T) (ApplicationRuntime, *applicationFixtureHost, ApplicationRuntimeRequest, ApplicationPublication) {
	t.Helper()
	root := applicationFixtureDir(t)
	host := &applicationFixtureHost{root: root}
	owner := ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "fixture"}
	d := applicationTestDescriptor()
	grant := ApplicationGrant{Owner: owner, RepositoryID: d.RepositoryID, PolicyRevision: "policy-a", LocationRevision: "location-a", MaxMemoryBytes: 128 * 1024 * 1024, Data: map[string]ApplicationDataPolicy{}, Credentials: map[string]ApplicationCredentialPolicy{}}
	policy := ApplicationHostPolicy{SchemaVersion: "application.policy.v1", NodeID: owner.NodeID, Platform: d.Artifact.Platform, InstallerUID: 1234, PublisherUID: 0, Grants: []ApplicationGrant{grant}}
	path := filepath.Join(root, "policy.json")
	raw, _ := json.Marshal(policy)
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	runtime := ApplicationRuntime{Store: ApplicationStateStore{Root: filepath.Join(root, "state"), OwnerUID: uint32(os.Geteuid())}, PolicyPath: path, System: host}
	publication := ApplicationPublication{Owner: owner, Revision: "revision-a", LocationRevision: "location-a", PolicyRevision: "policy-a", Descriptor: d}
	if e := runtime.Publish(context.Background(), 0, publication); e != nil {
		t.Fatal(e)
	}
	integer := int64(9007199254740993)
	manifest := ApplicationManifest{Kind: "loom.application", SchemaVersion: ApplicationContractSchema, Artifact: d.Artifact, Process: ApplicationProcess{Manager: ManagerSystemd, User: "fixture", Restart: "on-failure", MemoryMaxBytes: 64 * 1024 * 1024}, Config: ApplicationConfiguration{Schema: d.Configuration.Schema, Values: map[string]ApplicationParameter{"large": {Type: ApplicationInteger, Integer: &integer}}}, Health: ApplicationHealth{Kind: HealthKindManager}, Data: map[pc.ResourceKey]ApplicationDataRequirements{}}
	request := ApplicationRuntimeRequest{SchemaVersion: ApplicationRuntimeSchema, Operation: "apply", Owner: owner, OperationToken: "operation-a", ExpectedRevision: publication.Revision, PolicyRevision: grant.PolicyRevision, LocationRevision: grant.LocationRevision, CredentialRevisions: map[string]string{}, DescriptorDigest: applicationSHA(d), Manifest: &manifest, Data: map[string]ApplicationDataRequest{}}
	return runtime, host, request, publication
}
func TestApplicationRuntimeApplyReplayRetire(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	ctx := context.Background()
	receipt, e := r.Execute(ctx, 1234, q)
	if e != nil || !receipt.Applied || receipt.State != "succeeded" || receipt.Readiness.Protocol != "not_applicable" || receipt.Readiness.Protection != "unknown" {
		t.Fatalf("apply=%+v err=%v", receipt, e)
	}
	if !strings.Contains(string(h.generation.Config), "9007199254740993") {
		t.Fatal("integer lost precision")
	}
	again, e := r.Execute(ctx, 1234, q)
	if e != nil || applicationSHA(applicationHistoricalReceipt(again)) != applicationSHA(receipt) || h.restarts != 1 {
		t.Fatalf("replay=%+v err=%v restarts=%d", again, e, h.restarts)
	}
	changed := q
	changed.Manifest = &ApplicationManifest{}
	if _, e = r.Execute(ctx, 1234, changed); e == nil || !strings.Contains(e.Error(), "token_conflict") {
		t.Fatalf("input conflict=%v", e)
	}
	q.ExpectedInstallationRevision = receipt.InstallationRevision
	q.Operation = "retire"
	q.OperationToken = "retire-a"
	q.Manifest = nil
	retired, e := r.Execute(ctx, 1234, q)
	if e != nil || !retired.Retired || h.process.State != "inactive" {
		t.Fatalf("retire=%+v err=%v", retired, e)
	}
	if _, e = os.Stat(filepath.Join(h.root, "account-"+q.Owner.Instance())); e != nil {
		t.Fatal("account was removed")
	}
	q.Operation = "apply"
	q.OperationToken = "apply-after-retire"
	if _, e = r.Execute(ctx, 1234, q); e == nil {
		t.Fatal("retired instance restarted")
	}
}

func TestApplicationRuntimeInterruptedManagementRemoval(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	ctx := context.Background()
	applied, err := r.Execute(ctx, 1234, q)
	if err != nil {
		t.Fatal(err)
	}
	q.Operation, q.OperationToken = "retire", "retire-partial-management"
	q.ExpectedInstallationRevision = applied.InstallationRevision
	q.Manifest = nil
	h.failRemoval = true
	partial, err := r.Execute(ctx, 1234, q)
	if err == nil || partial.Retired || h.generation.ID != "" {
		t.Fatalf("expected removed drop-in with unfinished reload: receipt=%+v err=%v", partial, err)
	}
	h.failRemoval = false
	retired, err := r.Execute(ctx, 1234, q)
	if err != nil || !retired.Retired || h.removals != 2 {
		t.Fatalf("retry must finish management removal: receipt=%+v err=%v removals=%d", retired, err, h.removals)
	}
}
func TestApplicationRuntimeFailureReplay(t *testing.T) {
	for _, stage := range []string{"intent:preparing", "effect:preparing", "recorded:preparing", "intent:committed", "effect:committed", "recorded:committed", "intent:account", "effect:account", "recorded:account", "intent:generation", "effect:generation", "recorded:generation", "intent:restart", "effect:restart", "recorded:restart", "intent:installation", "effect:installation", "recorded:installation", "intent:applied", "effect:applied", "recorded:applied", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			r, h, q, _ := applicationTestRuntime(t)
			once := true
			r.FailureHook = func(s string) error {
				if once && s == stage {
					once = false
					return errors.New("interruption")
				}
				return nil
			}
			first, e := r.Execute(context.Background(), 1234, q)
			if e == nil {
				t.Fatal("failure boundary did not execute")
			}
			if first.EvidenceRef == "" {
				t.Fatal("effect evidence lost")
			}
			r.FailureHook = nil
			recovered, e := r.Execute(context.Background(), 1234, q)
			if stage == "intent:restart" {
				if e == nil || !strings.Contains(e.Error(), "uncertain") || h.restarts != 0 {
					t.Fatalf("uncertain restart blindly repeated: %v %+v", e, recovered)
				}
				return
			}
			if e != nil || !recovered.Applied || h.restarts != 1 {
				t.Fatalf("recovery=%+v err=%v restarts=%d", recovered, e, h.restarts)
			}
		})
	}
}
func TestApplicationRuntimePublicationCASBlocksLateCommit(t *testing.T) {
	r, _, q, p := applicationTestRuntime(t)
	entered := make(chan struct{})
	continueApply := make(chan struct{})
	r.FailureHook = func(stage string) error {
		if stage == "intent:restart" {
			close(entered)
			<-continueApply
		}
		return nil
	}
	applyDone := make(chan error, 1)
	go func() { _, e := r.Execute(context.Background(), 1234, q); applyDone <- e }()
	<-entered
	p.ExpectedRevision = p.Revision
	p.Revision = "revision-b"
	publishDone := make(chan error, 1)
	go func() { publishDone <- r.Publish(context.Background(), 0, p) }()
	select {
	case e := <-publishDone:
		t.Fatalf("publication bypassed effect fence: %v", e)
	default:
	}
	close(continueApply)
	if e := <-applyDone; e != nil {
		t.Fatal(e)
	}
	if e := <-publishDone; e != nil {
		t.Fatal(e)
	}
	if _, e := r.Execute(context.Background(), 1234, q); e == nil || !strings.Contains(e.Error(), "revision.conflict") {
		t.Fatalf("old receipt hid revision change: %v", e)
	}
}

func TestApplicationRuntimeSameArtifactConfigurationCAS(t *testing.T) {
	r, h, first, _ := applicationTestRuntime(t)
	ctx := context.Background()
	a, e := r.Execute(ctx, 1234, first)
	if e != nil {
		t.Fatal(e)
	}
	second := first
	second.OperationToken = "config-b"
	second.ExpectedInstallationRevision = a.InstallationRevision
	manifest := *first.Manifest
	manifest.Config.Values = map[string]ApplicationParameter{}
	for k, v := range first.Manifest.Config.Values {
		manifest.Config.Values[k] = v
	}
	n := int64(9007199254740995)
	manifest.Config.Values["large"] = ApplicationParameter{Type: ApplicationInteger, Integer: &n}
	second.Manifest = &manifest
	b, e := r.Execute(ctx, 1234, second)
	if e != nil || b.InstallationRevision == a.InstallationRevision || h.restarts != 2 {
		t.Fatalf("second=%+v error=%v", b, e)
	}
	// Delayed work on the same artifact has a distinct token but an old exact
	// installation revision. It must not revert config or retire the newer app.
	for _, op := range []string{"apply", "retire"} {
		late := first
		late.Operation = op
		late.OperationToken = "late-" + op
		late.ExpectedInstallationRevision = a.InstallationRevision
		if _, e = r.Execute(ctx, 1234, late); e == nil || !strings.Contains(e.Error(), "installation.revision_conflict") {
			t.Fatalf("late %s error=%v", op, e)
		}
	}
	if _, e = r.Execute(ctx, 1234, first); e == nil {
		t.Fatal("prior receipt hid newer configuration")
	}
	again, e := r.Execute(ctx, 1234, second)
	if e != nil || applicationSHA(applicationHistoricalReceipt(again)) != applicationSHA(b) || h.restarts != 2 {
		t.Fatalf("legitimate replay=%+v err=%v restarts=%d", again, e, h.restarts)
	}
}

func TestApplicationRuntimeRevokedInstallerWhileWaiting(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	ctx := context.Background()
	release, e := r.Store.lock(ctx, q.Owner.Instance())
	if e != nil {
		t.Fatal(e)
	}
	read := make(chan struct{})
	r.FailureHook = func(stage string) error {
		if stage == "policy_read" {
			close(read)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, e := r.Execute(ctx, 1234, q); done <- e }()
	<-read
	policy, e := r.policy()
	if e != nil {
		t.Fatal(e)
	}
	policy.InstallerUID = 2345
	raw, _ := json.Marshal(policy)
	if e = applicationAtomic(r.PolicyPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	release()
	if e = <-done; e == nil || !strings.Contains(e.Error(), "peer.denied") || h.restarts != 0 {
		t.Fatalf("revoked installer executed: %v restarts=%d", e, h.restarts)
	}
}

func TestApplicationRuntimeTwoArtifactsUpdateRollbackRetainsData(t *testing.T) {
	r, h, q, p := applicationTestRuntime(t)
	ctx := context.Background()
	policy, e := r.policy()
	if e != nil {
		t.Fatal(e)
	}
	g := &policy.Grants[0]
	pool := applicationFixtureDir(t)
	// A second artifact has its own schema and two data/credential bindings.
	descriptor := applicationTestDescriptor()
	descriptor.Configuration.Schema = "fixture.indexer"
	descriptor.Configuration.Parameters = map[string]ApplicationParameterSpec{}
	manifest := *q.Manifest
	manifest.Config = ApplicationConfiguration{Schema: "fixture.indexer", Values: map[string]ApplicationParameter{}}
	manifest.Data = map[pc.ResourceKey]ApplicationDataRequirements{}
	for _, key := range []string{"files", "index"} {
		g.Data[key] = ApplicationDataPolicy{Path: filepath.Join(pool, key), Pool: pool}
		q.Data[key] = ApplicationDataRequest{PlannedBytes: 1}
		manifest.Data[pc.ResourceKey(key)] = ApplicationDataRequirements{}
		ref := pc.ResourceKey(key)
		manifest.Config.Values[key] = ApplicationParameter{Type: ApplicationDataReference, DataRef: &ref}
		descriptor.Configuration.Parameters[key] = ApplicationParameterSpec{Type: ApplicationDataReference, Required: true}
	}
	for _, key := range []string{"first", "second"} {
		ref := "fixture." + key
		path := filepath.Join(applicationFixtureDir(t), "bootstrap")
		if e = os.WriteFile(path, []byte("synthetic-private-value"), 0600); e != nil {
			t.Fatal(e)
		}
		g.Credentials[ref] = ApplicationCredentialPolicy{Path: path, Revision: "credential-a"}
		q.CredentialRevisions[ref] = "credential-a"
		refCopy := ref
		manifest.Config.Values[key] = ApplicationParameter{Type: ApplicationCredentialReference, CredentialRef: &refCopy}
		descriptor.Configuration.Parameters[key] = ApplicationParameterSpec{Type: ApplicationCredentialReference, Required: true}
	}
	descriptor.ConfigurationDigest = applicationSHA(descriptor.Configuration)
	descriptor.Artifact.Ref = "fixture.indexer"
	descriptor.Artifact.Digest = "sha256:" + strings.Repeat("4", 64)
	manifest.Artifact = descriptor.Artifact
	raw, _ := json.Marshal(policy)
	if e = applicationAtomic(r.PolicyPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	p.ExpectedRevision = p.Revision
	p.Revision = "indexer-a"
	p.Descriptor = descriptor
	if e = r.Publish(ctx, 0, p); e != nil {
		t.Fatal(e)
	}
	q.ExpectedRevision = p.Revision
	q.DescriptorDigest = applicationSHA(descriptor)
	q.Manifest = &manifest
	first, e := r.Execute(ctx, 1234, q)
	if e != nil {
		t.Fatal(e)
	}
	sentinel := filepath.Join(g.Data["files"].Path, "sentinel")
	if e = os.WriteFile(sentinel, []byte("retained"), 0600); e != nil {
		t.Fatal(e)
	}
	before := first.Data["files"]
	// Code revision, same data format. The original descriptor remains the
	// previous code/config candidate; no data rollback operation exists.
	previous := descriptor
	descriptor.SourceRevision = strings.Repeat("b", 40)
	descriptor.Artifact.Digest = "sha256:" + strings.Repeat("5", 64)
	manifest.Artifact = descriptor.Artifact
	p.ExpectedRevision = p.Revision
	p.Revision = "indexer-b"
	p.Descriptor = descriptor
	if e = r.Publish(ctx, 0, p); e != nil {
		t.Fatal(e)
	}
	q.OperationToken = "update-b"
	q.ExpectedRevision = p.Revision
	q.ExpectedInstallationRevision = first.InstallationRevision
	q.DescriptorDigest = applicationSHA(descriptor)
	updated, e := r.Execute(ctx, 1234, q)
	if e != nil || updated.Data["files"] != before {
		t.Fatalf("update=%+v error=%v", updated, e)
	}
	p.ExpectedRevision = p.Revision
	p.Revision = "rollback-a"
	p.Descriptor = previous
	if e = r.Publish(ctx, 0, p); e != nil {
		t.Fatal(e)
	}
	manifest.Artifact = previous.Artifact
	q.OperationToken = "rollback-a"
	q.ExpectedRevision = p.Revision
	q.ExpectedInstallationRevision = updated.InstallationRevision
	q.DescriptorDigest = applicationSHA(previous)
	q.Rollback = true
	rolled, e := r.Execute(ctx, 1234, q)
	if e != nil || rolled.Data["files"] != before {
		t.Fatalf("rollback=%+v error=%v", rolled, e)
	}
	q.Operation = "retire"
	q.OperationToken = "retire-indexer"
	q.ExpectedInstallationRevision = rolled.InstallationRevision
	q.Manifest = nil
	q.Rollback = false
	retired, e := r.Execute(ctx, 1234, q)
	if e != nil || !retired.Retired {
		t.Fatalf("retire=%+v %v", retired, e)
	}
	retained, e := os.ReadFile(sentinel)
	if e != nil || string(retained) != "retained" || h.restarts != 3 {
		t.Fatalf("data/operation changed: %s %v restarts=%d", retained, e, h.restarts)
	}
}

func applicationFixtureDir(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestApplicationRuntimeInterruptedManagerReload(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	h.failReload = true
	first, e := r.Execute(context.Background(), 1234, q)
	if e == nil || h.generation.ID == "" || h.loaded != "" || h.restarts != 0 {
		t.Fatalf("reload failure boundary not reproduced: %+v %v", first, e)
	}
	h.failReload = false
	next, e := r.Execute(context.Background(), 1234, q)
	if e != nil || !next.Applied || h.loaded != h.generation.ID || h.reloads != 2 || h.restarts != 1 {
		t.Fatalf("disk bytes bypassed manager reload: %+v err=%v reloads=%d", next, e, h.reloads)
	}
}
func TestApplicationLoadedGenerationRequiresManagerEvidence(t *testing.T) {
	d := applicationTestDescriptor()
	g := ApplicationGeneration{ID: strings.Repeat("a", 64), Descriptor: d}
	path := "/var/lib/loom-project-application-configs/app/generation/config.json"
	valid := "NeedDaemonReload=no\nEnvironment=LOOM_APPLICATION_GENERATION=" + g.ID + "\nExecStart={ path=" + d.Launcher + " ; argv[]=" + d.Launcher + " " + path + " ; }\n"
	if !applicationLoadedGeneration([]byte(valid), g, path) {
		t.Fatal("exact loaded manager evidence rejected")
	}
	for _, bad := range []string{strings.Replace(valid, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1), strings.ReplaceAll(valid, g.ID, "old-generation"), strings.ReplaceAll(valid, path, "/old/config.json")} {
		if applicationLoadedGeneration([]byte(bad), g, path) {
			t.Fatal("unreloaded unit accepted from disk state")
		}
	}
}

func TestApplicationRuntimeUncertainStartupHasRevisionAndCanRetire(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	h.failRestart = true
	first, e := r.Execute(context.Background(), 1234, q)
	if e == nil || first.Applied || first.InstallationRevision == "" {
		t.Fatalf("uncertain startup lost prepared revision: %+v %v", first, e)
	}
	stale := q
	stale.OperationToken = "different-token"
	if _, e = r.Execute(context.Background(), 1234, stale); e == nil || !strings.Contains(e.Error(), "revision_conflict") {
		t.Fatalf("stale token overwrote partial startup: %v", e)
	}
	inspect := q
	inspect.Operation = "inspect"
	observed, e := r.Execute(context.Background(), 1234, inspect)
	if e != nil || observed.Applied || observed.InstallationRevision != first.InstallationRevision {
		t.Fatalf("prepared code claimed applied: %+v %v", observed, e)
	}
	retire := q
	retire.Operation = "retire"
	retire.OperationToken = "retire-partial"
	retire.ExpectedInstallationRevision = first.InstallationRevision
	retire.Manifest = nil
	retired, e := r.Execute(context.Background(), 1234, retire)
	if e != nil || !retired.Retired {
		t.Fatalf("partial startup cannot retire safely: %+v %v", retired, e)
	}
}

func TestApplicationRuntimeReplayReportsMissingCurrentProcess(t *testing.T) {
	r, h, q, _ := applicationTestRuntime(t)
	first, err := r.Execute(context.Background(), 1234, q)
	if err != nil {
		t.Fatal(err)
	}
	key := "operation-" + q.Owner.Instance() + "-" + applicationSHA(q.OperationToken)[7:]
	before, err := os.ReadFile(filepath.Join(r.Store.Root, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.process = ApplicationProcessObservation{State: "inactive"}
	h.generation = ApplicationGeneration{}
	h.loaded = ""
	h.mu.Unlock()
	replay, err := r.Execute(context.Background(), 1234, q)
	if err == nil || replay.Current == nil || replay.Current.Readiness.Process == "satisfied" || replay.Current.Process.State == "active" {
		t.Fatalf("historical success hid missing current process: %+v %v", replay, err)
	}
	after, err := os.ReadFile(filepath.Join(r.Store.Root, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || h.restarts != 1 || replay.InputDigest != first.InputDigest {
		t.Fatal("observation rewrote history or retried an effect")
	}
}

func applicationHistoricalReceipt(r ApplicationRuntimeReceipt) ApplicationRuntimeReceipt {
	r.Current = nil
	return r
}

type applicationBootFixtureHost struct {
	*applicationFixtureHost
	hosts map[ApplicationOwner]*applicationFixtureHost
}

func (h applicationBootFixtureHost) PublishGeneration(c context.Context, g ApplicationGeneration) error {
	return h.hosts[g.Owner].PublishGeneration(c, g)
}
func (h applicationBootFixtureHost) GenerationPublished(g ApplicationGeneration) bool {
	return h.hosts[g.Owner].GenerationPublished(g)
}
func (h applicationBootFixtureHost) Observe(c context.Context, g ApplicationGeneration) (ApplicationProcessObservation, error) {
	return h.hosts[g.Owner].Observe(c, g)
}
func (h applicationBootFixtureHost) Restart(c context.Context, g ApplicationGeneration) error {
	return h.hosts[g.Owner].Restart(c, g)
}
func (h applicationBootFixtureHost) Start(c context.Context, g ApplicationGeneration) error {
	return h.hosts[g.Owner].Start(c, g)
}
func (h applicationBootFixtureHost) Stop(c context.Context, g ApplicationGeneration) error {
	return h.hosts[g.Owner].Stop(c, g)
}
func (h applicationBootFixtureHost) RemoveManagement(c context.Context, g ApplicationGeneration) error {
	return h.hosts[g.Owner].RemoveManagement(c, g)
}

func TestApplicationBootRestoresOnlyCommittedOwners(t *testing.T) {
	r, base, q, p := applicationTestRuntime(t)
	policy, err := r.policy()
	if err != nil {
		t.Fatal(err)
	}
	grant := policy.Grants[0]
	host := applicationBootFixtureHost{applicationFixtureHost: base, hosts: map[ApplicationOwner]*applicationFixtureHost{}}
	requests := []ApplicationRuntimeRequest{}
	for _, resource := range []string{"fixture", "beta", "retired"} {
		v := q
		v.Owner.Resource = resource
		requests = append(requests, v)
		host.hosts[v.Owner] = &applicationFixtureHost{root: base.root}
		if resource != "fixture" {
			g := grant
			g.Owner = v.Owner
			policy.Grants = append(policy.Grants, g)
		}
	}
	raw, _ := json.Marshal(policy)
	if err = os.WriteFile(r.PolicyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r.System = host
	historical := map[string]string{}
	for _, request := range requests {
		publication := p
		publication.Owner = request.Owner
		if err = r.Publish(context.Background(), 0, publication); err != nil {
			t.Fatal(err)
		}
		receipt, err := r.Execute(context.Background(), 1234, request)
		if err != nil {
			t.Fatal(err)
		}
		key := "operation-" + request.Owner.Instance() + "-" + applicationSHA(request.OperationToken)[7:] + ".json"
		b, err := os.ReadFile(filepath.Join(r.Store.Root, key))
		if err != nil {
			t.Fatal(err)
		}
		historical[key] = string(b)
		if request.Owner.Resource == "retired" {
			request.Operation = "retire"
			request.OperationToken = "retire-control"
			request.ExpectedInstallationRevision = receipt.InstallationRevision
			request.Manifest = nil
			if _, err = r.Execute(context.Background(), 1234, request); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Simulate losing only volatile per-instance manager state. Durable receipts,
	// publication, config and exact data/account identities remain authoritative.
	for _, h := range host.hosts {
		h.process = ApplicationProcessObservation{State: "inactive"}
		h.generation = ApplicationGeneration{}
		h.loaded = ""
	}
	for pass := 0; pass < 2; pass++ {
		results, err := r.RestoreCommitted(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 3 {
			t.Fatal("boot owner bound lost")
		}
		for _, result := range results {
			h := host.hosts[result.Owner]
			if result.Owner.Resource == "retired" {
				if result.State != "skipped" || h.process.State != "inactive" || h.restarts != 1 {
					t.Fatal("retired owner revived")
				}
			} else if result.State != "observed" || h.restarts != 2 || result.Current == nil || result.Current.Process.State != "active" {
				t.Fatalf("boot did not restore exactly once: %+v starts=%d", result, h.restarts)
			}
		}
	}
	for key, before := range historical {
		after, err := os.ReadFile(filepath.Join(r.Store.Root, key))
		if err != nil || string(after) != before {
			t.Fatal("boot rewrote operation history")
		}
	}
}

func TestApplicationBootInterruptionDoesNotDuplicateStart(t *testing.T) {
	for _, stage := range []string{"boot:generation", "boot:start", "boot:observed"} {
		t.Run(stage, func(t *testing.T) {
			r, h, q, _ := applicationTestRuntime(t)
			if _, err := r.Execute(context.Background(), 1234, q); err != nil {
				t.Fatal(err)
			}
			h.process = ApplicationProcessObservation{State: "inactive"}
			h.generation = ApplicationGeneration{}
			h.loaded = ""
			r.FailureHook = func(at string) error {
				if at == stage {
					return errors.New("boot interrupted")
				}
				return nil
			}
			if _, err := r.RestoreCommitted(context.Background()); err == nil {
				t.Fatal("boot interruption did not run")
			}
			r.FailureHook = nil
			for i := 0; i < 2; i++ {
				results, err := r.RestoreCommitted(context.Background())
				if err != nil || len(results) != 1 || results[0].State != "observed" || h.restarts != 2 {
					t.Fatalf("boot recovery duplicated/missed start: %+v %v starts=%d", results, err, h.restarts)
				}
			}
		})
	}
}

func TestApplicationBootRefusesStaleOrUnfinishedState(t *testing.T) {
	for _, kind := range []string{"not_committed", "not_applied", "fenced", "retired", "receipt_missing", "receipt_partial", "source_changed", "policy_changed", "location_changed", "grant_removed", "wrong_process"} {
		t.Run(kind, func(t *testing.T) {
			r, h, q, _ := applicationTestRuntime(t)
			if _, err := r.Execute(context.Background(), 1234, q); err != nil {
				t.Fatal(err)
			}
			h.process = ApplicationProcessObservation{State: "inactive"}
			h.generation = ApplicationGeneration{}
			h.loaded = ""
			key := "installation-" + q.Owner.Instance()
			var installed ApplicationInstallation
			if err := r.Store.read(key, &installed); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "not_committed":
				installed.Committed = false
			case "not_applied":
				installed.Applied = false
			case "fenced":
				installed.Fenced = true
			case "retired":
				installed.Retired = true
			case "receipt_missing", "receipt_partial":
				journalKey := "operation-" + q.Owner.Instance() + "-" + applicationSHA(q.OperationToken)[7:]
				if kind == "receipt_missing" {
					if err := os.Rename(filepath.Join(r.Store.Root, journalKey+".json"), filepath.Join(r.Store.Root, "retained-original.json")); err != nil {
						t.Fatal(err)
					}
				} else {
					var j ApplicationJournal
					if err := r.Store.read(journalKey, &j); err != nil {
						t.Fatal(err)
					}
					j.Receipt.State = "partial"
					if err := r.Store.write(journalKey, j); err != nil {
						t.Fatal(err)
					}
				}
			case "source_changed":
				var pub ApplicationPublication
				if err := r.Store.read("publication-"+q.Owner.Instance(), &pub); err != nil {
					t.Fatal(err)
				}
				pub.Revision = "later-publication"
				if err := r.Store.write("publication-"+q.Owner.Instance(), pub); err != nil {
					t.Fatal(err)
				}
			case "policy_changed", "location_changed", "grant_removed":
				policy, err := r.policy()
				if err != nil {
					t.Fatal(err)
				}
				if kind == "policy_changed" {
					policy.Grants[0].PolicyRevision = "later-policy"
				}
				if kind == "location_changed" {
					policy.Grants[0].LocationRevision = "later-location"
				}
				if kind == "grant_removed" {
					policy.Grants = nil
				}
				raw, _ := json.Marshal(policy)
				if err = os.WriteFile(r.PolicyPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong_process":
				h.process = ApplicationProcessObservation{State: "active", Executable: "/other", PID: 70}
			}
			if err := r.Store.write(key, installed); err != nil {
				t.Fatal(err)
			}
			results, _ := r.RestoreCommitted(context.Background())
			if h.restarts != 1 {
				t.Fatal("stale/unfinished owner restarted")
			}
			for _, v := range results {
				if v.State == "observed" || v.ErrorCode == "" {
					t.Fatalf("refusal was not actionable: %+v", v)
				}
			}
		})
	}
}

func TestApplicationBootRevalidatesCredentialAndDataCustody(t *testing.T) {
	for _, kind := range []string{"credential_inode", "data_inode", "pool_inode", "filesystem", "data_owner"} {
		t.Run(kind, func(t *testing.T) {
			r, h, q, p := applicationTestRuntime(t)
			policy, err := r.policy()
			if err != nil {
				t.Fatal(err)
			}
			pool := filepath.Join(h.root, "pool")
			if err = os.Mkdir(pool, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(pool, "files")
			grant := &policy.Grants[0]
			grant.Data["files"] = ApplicationDataPolicy{Path: path, Pool: pool}
			q.Data["files"] = ApplicationDataRequest{}
			q.Manifest.Data[pc.ResourceKey("files")] = ApplicationDataRequirements{}
			dataRef := pc.ResourceKey("files")
			q.Manifest.Config.Values["files"] = ApplicationParameter{Type: ApplicationDataReference, DataRef: &dataRef}
			p.Descriptor.Configuration.Parameters["files"] = ApplicationParameterSpec{Type: ApplicationDataReference, Required: true}
			secret := filepath.Join(h.root, "fixture-credential")
			if err = os.WriteFile(secret, []byte("synthetic-bootstrap"), 0600); err != nil {
				t.Fatal(err)
			}
			ref := "fixture.boot-secret"
			grant.Credentials[ref] = ApplicationCredentialPolicy{Path: secret, Revision: "credential-a"}
			q.CredentialRevisions[ref] = "credential-a"
			q.Manifest.Config.Values["secret"] = ApplicationParameter{Type: ApplicationCredentialReference, CredentialRef: &ref}
			p.Descriptor.Configuration.Parameters["secret"] = ApplicationParameterSpec{Type: ApplicationCredentialReference, Required: true}
			p.Descriptor.ConfigurationDigest = applicationSHA(p.Descriptor.Configuration)
			p.ExpectedRevision = p.Revision
			p.Revision = "bound-revision"
			q.ExpectedRevision = p.Revision
			q.DescriptorDigest = applicationSHA(p.Descriptor)
			raw, _ := json.Marshal(policy)
			if err = os.WriteFile(r.PolicyPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = r.Publish(context.Background(), 0, p); err != nil {
				t.Fatal(err)
			}
			if _, err = r.Execute(context.Background(), 1234, q); err != nil {
				t.Fatal(err)
			}
			h.process = ApplicationProcessObservation{State: "inactive"}
			h.generation = ApplicationGeneration{}
			h.loaded = ""
			switch kind {
			case "credential_inode":
				if err = os.Rename(secret, secret+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(secret, []byte("synthetic-bootstrap"), 0600); err != nil {
					t.Fatal(err)
				}
			case "data_inode":
				if err = os.Rename(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "pool_inode":
				if err = os.Rename(pool, pool+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(pool, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "filesystem", "data_owner":
				var i ApplicationInstallation
				key := "installation-" + q.Owner.Instance()
				if err = r.Store.read(key, &i); err != nil {
					t.Fatal(err)
				}
				d := i.Data["files"]
				if kind == "filesystem" {
					d.Filesystem = "different-filesystem"
				} else {
					d.UID++
				}
				i.Data["files"] = d
				if err = r.Store.write(key, i); err != nil {
					t.Fatal(err)
				}
			}
			results, err := r.RestoreCommitted(context.Background())
			if err != nil || len(results) != 1 || results[0].State != "skipped" || results[0].ErrorCode == "" || h.restarts != 1 || h.generation.ID != "" {
				t.Fatalf("boot changed rejected custody: %+v %v starts=%d", results, err, h.restarts)
			}
		})
	}
}

func TestApplicationBootRequiresDurableSuccessfulReceipt(t *testing.T) {
	for _, stage := range []string{"intent:preparing", "effect:preparing", "recorded:preparing", "recorded:applied", "intent:committed", "effect:committed", "recorded:committed", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			r, h, q, _ := applicationTestRuntime(t)
			r.FailureHook = func(at string) error {
				if at == stage {
					return errors.New("fixture lost completion")
				}
				return nil
			}
			if _, err := r.Execute(context.Background(), 1234, q); err == nil {
				t.Fatal("boundary did not execute")
			}
			r.FailureHook = nil
			before := h.restarts
			h.process = ApplicationProcessObservation{State: "inactive"}
			h.loaded = ""
			h.generation = ApplicationGeneration{}
			results, err := r.RestoreCommitted(context.Background())
			if err != nil || len(results) != 1 {
				t.Fatalf("boot refusal: %+v %v", results, err)
			}
			if stage == "receipt" {
				if h.restarts != before+1 || results[0].State != "observed" {
					t.Fatal("durable success with lost reply did not restore")
				}
			} else if h.restarts != before || results[0].State != "skipped" {
				t.Fatal("unfinished installation was revived")
			}
		})
	}
}
