package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
)

func projectWatchNodeFixture(t *testing.T) (Store, Config, State) {
	t.Helper()
	base := t.TempDir()
	box := filepath.Join(base, "box")
	if err := os.Mkdir(box, 0700); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: filepath.Join(base, "config.json"), StatePath: filepath.Join(base, "state.json"), DataDir: filepath.Join(base, "data")}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	config := Config{MainURL: "http://main.test", NodeKey: "main", DisplayName: "Main", BoxRootPath: box}
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	return store, config, State{NodeID: ids.NewNodeID()}
}
func projectWatchFixturePayload(t *testing.T, config Config, state State, slug string, revision int64) (projectwatch.DeclarationWatchPayload, string) {
	t.Helper()
	root := filepath.Join(config.BoxRootPath, slug)
	for _, dir := range []string{".loom", "material"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	id := ids.NewProjectID()
	raw := projectWatchJSON(map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV05, "project": map[string]string{"id": id, "slug": slug, "name": slug, "owner_node": "main"}, "resources": map[string]any{"reading": map[string]any{"kind": "knowledge", "knowledge": map[string]string{"path": "material", "category": "docs"}}}})
	if err := os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	contributors := []projects.DeclarationWatchContributor{{ActionID: "enroll_knowledge:reading", Owner: "knowledge", Resource: "reading"}}
	group, err := projectwatch.BuildDeclarationWatchGroup(pc.Analyze(root), state.NodeID, config.NodeKey, contributors)
	if err != nil {
		t.Fatal(err)
	}
	return sealProjectWatchFixture(t, group, revision), root
}
func sealProjectWatchFixture(t *testing.T, group projects.DeclarationWatchGroup, revision int64) projectwatch.DeclarationWatchPayload {
	t.Helper()
	hash, err := projectwatch.DeclarationGroupHash(group)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []projects.DeclarationWatchToken{}
	for _, c := range group.Contributors {
		token, _ := communication.ConfigHash(c)
		tokens = append(tokens, projects.DeclarationWatchToken{ActionID: c.ActionID, Owner: c.Owner, Resource: c.Resource, Token: token, InputHash: token})
	}
	return projectwatch.DeclarationWatchPayload{SchemaVersion: projectwatch.DeclarationWatchSchemaVersion, OperationID: ids.NewJobID(), GroupHash: hash, Evidence: communication.NewDesiredStateEvidence(group.NodeID, revision, hash), Group: group, Tokens: tokens}
}
func projectWatchFixtureMessage(payload projectwatch.DeclarationWatchPayload) communication.Message {
	return communication.Message{CommunicationMessageID: ids.NewCommunicationMessageID(), NodeID: payload.Group.NodeID, Direction: communication.DirectionMainToNode, Kind: communication.KindProjectWatchReconcile, PayloadJSON: projectWatchJSON(payload)}
}
func assertProjectWatchAck(t *testing.T, ack communication.AckInput) projectwatch.DeclarationWatchAck {
	t.Helper()
	var result projectwatch.DeclarationWatchAck
	if err := json.Unmarshal(ack.ResultJSON, &result); err != nil || ack.AckStatus != communication.AckStatusCompleted || result.Evidence.Outcome != communication.ControlOutcomeCompleted {
		t.Fatalf("project ACK: status=%s result=%s error=%v", ack.AckStatus, ack.ResultJSON, err)
	}
	return result
}
func TestDeclarationProjectWatchReplayRetirementAndIsolation(t *testing.T) {
	store, config, state := projectWatchNodeFixture(t)
	a, root := projectWatchFixturePayload(t, config, state, "project-a", 1)
	b, _ := projectWatchFixturePayload(t, config, state, "project-b", 1)
	sourcePath := filepath.Join(root, "material", "retained.md")
	if err := os.WriteFile(sourcePath, []byte("retained bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(a)))
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(b)))
	runtimeStore := noderuntime.NewStore(store.DataDir)
	bBefore, err := runtimeStore.LoadInstance(b.Group.Roots[0].WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	aBefore, err := runtimeStore.LoadInstance(a.Group.Roots[0].WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(a)))
	aAfter, err := runtimeStore.LoadInstance(a.Group.Roots[0].WorkerKey)
	if err != nil || !reflect.DeepEqual(aBefore, aAfter) {
		t.Fatal("same revision rewrote actual worker")
	}
	// A recorded successful revision must inspect and repair the actual worker.
	aAfter.Enabled = false
	if err = runtimeStore.SaveInstance(aAfter); err != nil {
		t.Fatal(err)
	}
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(a)))
	raw, err := os.ReadFile(filepath.Join(root, pc.CanonicalRootContractPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["resources"] = map[string]any{}
	if err = os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), projectWatchJSON(doc), 0600); err != nil {
		t.Fatal(err)
	}
	contributors := []projects.DeclarationWatchContributor{{ActionID: "retire_resource:reading:knowledge", Owner: "knowledge", Resource: "reading", Retire: true}}
	empty, err := projectwatch.BuildDeclarationWatchGroup(pc.Analyze(root), state.NodeID, config.NodeKey, contributors)
	if err != nil {
		t.Fatal(err)
	}
	retirement := sealProjectWatchFixture(t, empty, 2)
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(retirement)))
	after, err := runtimeStore.LoadInstance(a.Group.Roots[0].WorkerKey)
	if err != nil || after.Enabled {
		t.Fatalf("retirement: %+v %v", after, err)
	}
	bAfter, err := runtimeStore.LoadInstance(b.Group.Roots[0].WorkerKey)
	if err != nil || !reflect.DeepEqual(bBefore, bAfter) {
		t.Fatal("other project's runtime facts changed")
	}
	retained, err := os.ReadFile(sourcePath)
	if err != nil || string(retained) != "retained bytes" {
		t.Fatal("retirement changed source payload")
	}
	if _, err = os.Stat(watchedroots.NewStore(store.DataDir).RootDir(a.Group.Roots[0].BackendRootKey)); err != nil {
		t.Fatal("retirement removed root evidence")
	}
	stale := BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(a))
	if stale.AckStatus == communication.AckStatusCompleted {
		var parsed projectwatch.DeclarationWatchAck
		_ = json.Unmarshal(stale.ResultJSON, &parsed)
		if parsed.Evidence.Outcome == communication.ControlOutcomeCompleted {
			t.Fatal("stale source activated")
		}
	}
}
func TestDeclarationProjectWatchRejectsForeignMalformedAndSubstitution(t *testing.T) {
	for _, scenario := range []string{"wrong_node", "unknown_field", "duplicate_field", "oversized", "effective_field_swap", "foreign_worker", "foreign_safe_root", "escaped_root", "source_edit"} {
		t.Run(scenario, func(t *testing.T) {
			store, config, state := projectWatchNodeFixture(t)
			payload, root := projectWatchFixturePayload(t, config, state, "project-a", 1)
			message := projectWatchFixtureMessage(payload)
			switch scenario {
			case "wrong_node":
				message.NodeID = ids.NewNodeID()
			case "unknown_field":
				var value map[string]any
				_ = json.Unmarshal(message.PayloadJSON, &value)
				value["allow"] = true
				message.PayloadJSON = projectWatchJSON(value)
			case "duplicate_field":
				message.PayloadJSON = append([]byte(`{"schema_version":"duplicate",`), message.PayloadJSON[1:]...)
			case "oversized":
				message.PayloadJSON = append(message.PayloadJSON, make([]byte, communication.MaxProtectedFolderControlPayloadBytes)...)
			case "effective_field_swap":
				var cfg watchedroots.RootConfig
				_ = json.Unmarshal(payload.Group.Roots[0].ConfigJSON, &cfg)
				cfg.RootRelativePath = "."
				payload.Group.Roots[0].ConfigJSON = projectWatchJSON(cfg)
				payload.Group.Roots[0].ConfigHash = watchedroots.ConfigHash(cfg)
				payload = sealProjectWatchFixture(t, payload.Group, 1)
				message = projectWatchFixtureMessage(payload)
			case "foreign_worker":
				cfg := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: payload.Group.Roots[0].BackendRootKey, SafeRootKey: "manual", RootRelativePath: "."})
				if err := noderuntime.NewStore(store.DataDir).SaveInstance(noderuntime.WorkerInstance{WorkerKey: payload.Group.Roots[0].WorkerKey, Kind: noderuntime.KindWatchedRoot, Enabled: false, ConfigJSON: projectWatchJSON(cfg)}); err != nil {
					t.Fatal(err)
				}
			case "foreign_safe_root":
				key, _ := projectwatch.DeclarationSafeRootKey(payload.Group.ProjectID)
				config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot(key, root)}
				if err := store.SaveConfig(config); err != nil {
					t.Fatal(err)
				}
			case "escaped_root":
				outside := t.TempDir()
				if err := os.Rename(root, filepath.Join(outside, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "moved"), root); err != nil {
					t.Fatal(err)
				}
			case "source_edit":
				file := filepath.Join(root, pc.CanonicalRootContractPath)
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(file, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			ack := BuildProjectWatchReconcileAck(t.Context(), store, state, message)
			if ack.AckStatus == communication.AckStatusCompleted {
				t.Fatalf("invalid message accepted: %s", ack.ResultJSON)
			}
			after, err := store.LoadConfig()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected message changed config")
			}
		})
	}
}
func TestDeclarationProjectWatchConcurrentBoxAndManualConfig(t *testing.T) {
	store, config, state := projectWatchNodeFixture(t)
	a, _ := projectWatchFixturePayload(t, config, state, "project-a", 1)
	b, _ := projectWatchFixturePayload(t, config, state, "project-b", 1)
	boxRoot := desiredRootFixture(t, "photos", t.TempDir(), 1048576)
	boxPayload := reconcilePayload(t, state.NodeID, 1, []backupcontracts.ProtectedFolderDesiredRoot{boxRoot})
	manualPath := t.TempDir()
	errs := make(chan error, 4)
	var start sync.WaitGroup
	start.Add(1)
	for _, payload := range []projectwatch.DeclarationWatchPayload{a, b} {
		go func() {
			start.Wait()
			ack := BuildProjectWatchReconcileAck(context.Background(), store, state, projectWatchFixtureMessage(payload))
			if ack.AckStatus != communication.AckStatusCompleted {
				errs <- fmt.Errorf("project: %s", ack.ResultJSON)
			} else {
				errs <- nil
			}
		}()
	}
	go func() {
		start.Wait()
		out := reconcileProtectedFolders(store, config, boxPayload)
		if out.AckStatus != communication.AckStatusCompleted {
			errs <- fmt.Errorf("Box: %+v", out)
		} else {
			errs <- nil
		}
	}()
	go func() {
		start.Wait()
		opts := rootOptions{configPath: store.ConfigPath, statePath: store.StatePath, dataDir: store.DataDir, out: io.Discard, errOut: io.Discard}
		cmd := newFilesystemCommand(&opts)
		cmd.SetArgs([]string{"roots", "add", "--key", "manual", "--path", manualPath})
		errs <- cmd.Execute()
	}()
	start.Done()
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	aKey, _ := projectwatch.DeclarationSafeRootKey(a.Group.ProjectID)
	bKey, _ := projectwatch.DeclarationSafeRootKey(b.Group.ProjectID)
	for _, key := range []string{aKey, bKey, boxRoot.SafeRootKey, "manual"} {
		if _, found := filesystemconnector.FindSafeRoot(current.Filesystem, key); !found {
			t.Fatalf("concurrent config lost %s", key)
		}
	}
}

func TestDeclarationProjectWatchArchiveConflictAndRestart(t *testing.T) {
	t.Run("archive", func(t *testing.T) {
		store, config, state := projectWatchNodeFixture(t)
		payload, _ := projectWatchFixturePayload(t, config, state, "archive-project", 1)
		root := payload.Group.Roots[0]
		fence := projectquiescence.Fence{SchemaVersion: projectquiescence.FenceSchemaVersion, State: projectquiescence.FenceStateActive, Target: projectquiescence.Target{Kind: projectquiescence.TargetKindWatchedRoot, WorkerKey: root.WorkerKey}}
		raw, _ := projectquiescence.CanonicalFenceBytes(fence)
		if err := noderuntime.NewStore(store.DataDir).PublishProjectArchiveFence(projectquiescence.TargetKindWatchedRoot, root.WorkerKey, raw); err != nil {
			t.Fatal(err)
		}
		before, _ := store.LoadConfig()
		ack := BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload))
		after, _ := store.LoadConfig()
		if ack.AckStatus == communication.AckStatusCompleted || !reflect.DeepEqual(before, after) {
			t.Fatal("archive fence bypassed")
		}
	})
	t.Run("partial_restart", func(t *testing.T) {
		store, config, state := projectWatchNodeFixture(t)
		payload, _ := projectWatchFixturePayload(t, config, state, "restart-project", 1)
		runtime := noderuntime.NewStore(store.DataDir)
		if err := runtime.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{}); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(store.DataDir, "workers", "instances")
		if err := os.Chmod(directory, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
		ack := BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload))
		if ack.AckStatus == communication.AckStatusCompleted {
			t.Fatal("worker write failure was acknowledged")
		}
		pending, err := watchedroots.NewStore(store.DataDir).LoadProjectWatchReconcileState(payload.Group.ProjectID)
		if err != nil || pending.PendingRevision != 1 || pending.AppliedRevision != 0 {
			t.Fatalf("partial state=%+v %v", pending, err)
		}
		if err = os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
		assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload)))
		changed := payload
		changed.OperationID = ids.NewJobID()
		if ack = BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(changed)); ack.AckStatus == communication.AckStatusCompleted {
			t.Fatal("same revision accepted a foreign operation")
		}
		newer := payload
		newer.Evidence.DesiredRevision = 2
		assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(newer)))
		ack = BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload))
		var stale projectwatch.DeclarationWatchAck
		if json.Unmarshal(ack.ResultJSON, &stale) != nil || stale.Evidence.Outcome != communication.ControlOutcomeStale {
			t.Fatal("stale unchanged-source revision applied")
		}
		persisted, err := watchedroots.NewStore(store.DataDir).LoadProjectWatchReconcileState(payload.Group.ProjectID)
		if err != nil || persisted.AppliedRevision != 2 {
			t.Fatal("stale control changed applied revision")
		}
	})
}

func TestDeclarationProjectWatchDisabledProtection(t *testing.T) {
	store, config, state := projectWatchNodeFixture(t)
	payload, root := projectWatchFixturePayload(t, config, state, "disabled-project", 1)
	sourcePath := filepath.Join(root, pc.CanonicalRootContractPath)
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	source["resources"] = map[string]any{"retained": map[string]any{"kind": "protection", "protection": map[string]any{"policy_ref": ".loom/backup.yaml", "path": "material"}}}
	if err = os.WriteFile(sourcePath, projectWatchJSON(source), 0600); err != nil {
		t.Fatal(err)
	}
	policy := `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  defaults:
    max_file_bytes: 1048576
    max_batch_bytes: 2097152
    max_pending_items: 9
    max_pending_bytes: 9007199254740993
    include_deletion_markers: false
    on_limit: degrade_and_require_manual_action
    include: ['**/*.md']
    exclude: ['private/**']
  roots:
    - key: material
      path: material
`
	if err = os.WriteFile(filepath.Join(root, ".loom/backup.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := projectwatch.BuildDeclarationWatchGroup(pc.Analyze(root), state.NodeID, config.NodeKey, []projects.DeclarationWatchContributor{{ActionID: "reconcile_protection:retained", Owner: "backupcontracts", Resource: "retained"}})
	if err != nil {
		t.Fatal(err)
	}

	payload = sealProjectWatchFixture(t, group, 1)
	if len(group.Roots) != 1 || !group.Roots[0].Enabled {
		t.Fatal("enabled compiler fixture missing")
	}
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload)))
	workerKey := group.Roots[0].WorkerKey
	if err = os.WriteFile(filepath.Join(root, ".loom/backup.yaml"), []byte(strings.Replace(policy, "enabled: true", "enabled: false", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	disabled, err := projectwatch.BuildDeclarationWatchGroup(pc.Analyze(root), state.NodeID, config.NodeKey, group.Contributors)
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled.Roots) != 0 {
		t.Fatal("disabled compiler policy unexpectedly requests worker roots")
	}
	payload = sealProjectWatchFixture(t, disabled, 2)
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload)))
	instance, err := noderuntime.NewStore(store.DataDir).LoadInstance(workerKey)
	if err != nil || instance.Enabled {
		t.Fatalf("disabled declaration retained active worker: %v", err)
	}
}

// Source publication is fixture setup. Main's predecessor is represented by
// closed typed rows here; the PostgreSQL owner test supplies the authentic path.
func legacyProjectWatchNodeFixture(t *testing.T) (Store, Config, State, projectwatch.DeclarationWatchPayload) {
	t.Helper()
	store, config, state := projectWatchNodeFixture(t)
	root := filepath.Join(config.BoxRootPath, "legacy")
	write := func(ref, raw string) {
		t.Helper()
		path := filepath.Join(root, ref)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := ids.NewProjectID()
	old := fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.4\nproject: {id: %s, slug: legacy, name: Legacy, owner_node: main, status: active}\nfacets: {notes: true, backup_policy: true}\n", id)
	notes := "kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes: {status: active, sync: true, index: true, backup: true, root_key: notes, path: ., include: ['**/*'], exclude: []}\n"
	backup := "kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup:\n  enabled: true\n  defaults: {max_file_bytes: 4096, max_batch_bytes: 8192, max_pending_bytes: 9007199254740993}\n  roots:\n    - {key: notes, path: notes}\n    - {key: controls, path: ., include: ['.loom/**']}\n"
	write(pc.CanonicalRootContractPath, old)
	write(".loom/contracts/notes.yaml", notes)
	write(".loom/contracts/backup.yaml", backup)
	write("notes/preserved.md", "preserved cobalt")
	oldAnalysis := pc.Analyze(root)
	input, err := projectregistration.BuildInput(oldAnalysis, "node-fixture")
	if err != nil {
		t.Fatalf("old: %v %+v", err, oldAnalysis.Report.Diagnostics)
	}
	hash := func(raw string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) }
	write(".loom/contracts/retained.yaml", old)
	write(pc.CanonicalRootContractPath, fmt.Sprintf("kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: %s, slug: legacy, name: Legacy, owner_node: main, status: active}\nresources:\n  notes_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: notes}}\n  controls_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: .}}\nlegacy_contracts:\n  project: {ref: .loom/contracts/retained.yaml, schema_version: project.contract.v0.4, digest: %s}\n  notes: {key: imported_notes, ref: .loom/contracts/notes.yaml, schema_version: notes.contract.v0.3, digest: %s, protection: notes_backup}\n", id, hash(old), hash(notes)))
	a := pc.Analyze(root)
	contributors := []projects.DeclarationWatchContributor{{ActionID: "reconcile_protection:controls_backup", Owner: "backupcontracts", Resource: "controls_backup"}, {ActionID: "reconcile_protection:notes_backup", Owner: "backupcontracts", Resource: "notes_backup"}, {ActionID: "enroll_knowledge:imported_notes", Owner: "knowledge", Resource: "imported_notes"}}
	group, err := projectwatch.BuildDeclarationWatchGroup(a, state.NodeID, config.NodeKey, contributors)
	if err != nil {
		t.Fatalf("desired: %v %+v", err, a.Report.Diagnostics)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	physicalRoot, err := canonicalDirectoryPath(root)
	if err != nil {
		t.Fatal(err)
	}
	p := &projects.DeclarationLegacyPredecessor{SchemaVersion: projects.DeclarationLegacyPredecessorSchema, ProjectID: id, RegistrationID: ids.NewProjectContractRegistrationID(), RegistrationRevision: 1, NodeID: state.NodeID, NodeKey: config.NodeKey, ProjectRoot: root, PhysicalRoot: physicalRoot, ContractPath: filepath.Join(root, pc.CanonicalRootContractPath), ContractHash: hash(old), ContractSchema: pc.ProjectSchemaV04, Contract: input.Contract, Members: []projects.DeclarationLegacyMember{}}
	safe := filesystemconnector.DefaultSafeRoot("project", root)
	safe.AllowList = false
	safe.IncludeHiddenDefault = true
	safe.MaxFileBytes = 1024 * 1024 * 1024
	safe.Metadata = projectWatchJSON(map[string]string{"policy": "preserved"})
	config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{safe}
	if err = store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	for _, w := range oldAnalysis.Plan.WatchedRoots {
		rootID := ids.NewWatchedRootID()
		row := projects.ProjectWatchedRootRegistration{ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(), ProjectContractRegistrationID: p.RegistrationID, ProjectID: id, NodeID: state.NodeID, OwnerNodeKey: config.NodeKey, LocalRootKey: w.Key, BackendRootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, SourceKinds: projectWatchJSON(w.SourceKinds), SafeRootKey: w.SafeRootKey, RootRelativePath: w.RootRelativePath, DisplayName: w.DisplayName, SyncMode: w.SyncMode, BackupMode: w.BackupMode, IndexMode: w.IndexMode, DeleteMode: w.DeleteMode, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CommandJSON: json.RawMessage(`[]`), WatchedRootID: &rootID, ActivationStatus: "reported", LastAppliedAt: &now, Metadata: projectWatchJSON(w.Metadata), CreatedAt: now}
		report := projects.DeclarationLegacyReport{WatchedRootID: rootID, NodeID: state.NodeID, RootKey: w.BackendRootKey, WorkerKey: w.WorkerKey, DisplayName: w.DisplayName, SafeRootKey: w.SafeRootKey, Status: "healthy", ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CreatedAt: now, Metadata: map[string]json.RawMessage{}}
		p.Members = append(p.Members, projects.DeclarationLegacyMember{Registration: row, Report: report})
		instance := noderuntime.WorkerInstance{WorkerKey: w.WorkerKey, Kind: noderuntime.KindWatchedRoot, DisplayName: w.DisplayName, Enabled: true, IntervalSeconds: 83, LeaseTimeoutSeconds: 157, LocalRootKey: w.Key, ConfigHash: w.ConfigHash, ConfigJSON: w.ConfigJSON, CreatedAt: now}
		if err = noderuntime.NewStore(store.DataDir).SaveInstance(instance); err != nil {
			t.Fatal(err)
		}
		if err = watchedroots.NewStore(store.DataDir).EnsureRoot(w.BackendRootKey); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(p.Members, func(i, j int) bool {
		return p.Members[i].Registration.BackendRootKey < p.Members[j].Registration.BackendRootKey
	})
	value, err := pc.DecodeDeclarationEvidenceJSON(projectWatchJSON(p))
	if err != nil {
		t.Fatal(err)
	}
	p.Digest, _ = communication.ConfigHash(value)
	group.Predecessor = p
	payload := sealProjectWatchFixture(t, group, 1)
	if _, err = projectwatch.DecodeDeclarationWatchPayload(projectWatchJSON(payload)); err != nil {
		t.Fatal(err)
	}
	if err = verifyLocalProjectWatchSource(payload); err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	return store, config, state, payload
}

func TestDeclarationLegacyNodeRecovery(t *testing.T) {
	for _, phase := range []string{"pending_before_config", "pending_all_old", "pending_mixed", "pending_all_new", "applied_replay"} {
		t.Run(phase, func(t *testing.T) {
			store, _, state, payload := legacyProjectWatchNodeFixture(t)
			msg := projectWatchFixtureMessage(payload)
			assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, msg))
			local := watchedroots.NewStore(store.DataDir)
			proof, err := local.LoadProjectWatchReconcileState(payload.Group.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			runtimeStore := noderuntime.NewStore(store.DataDir)
			if phase != "applied_replay" {
				proof.PendingRevision = proof.AppliedRevision
				proof.PendingHash = proof.ConfigHash
				proof.AppliedRevision = 0
				proof.ConfigHash = ""
				for i, m := range proof.Legacy.Members {
					if phase == "pending_before_config" || phase == "pending_all_old" || phase == "pending_mixed" && i == 0 {
						// Restore exact saved predecessor bytes, including its original timestamp.
						path := filepath.Join(store.DataDir, "runtime", "instances", m.Predecessor.WorkerKey+".json")
						_ = path
						release, err := runtimeStore.AcquireWorkerExecutionLock(t.Context(), m.Predecessor.WorkerKey)
						if err != nil {
							t.Fatal(err)
						}
						err = runtimeStore.SaveInstanceWhileLocked(m.Predecessor)
						_ = release()
						if err != nil {
							t.Fatal(err)
						}
						actual, err := runtimeStore.LoadInstance(m.Predecessor.WorkerKey)
						if err != nil {
							t.Fatal(err)
						}
						proof.Legacy.Members[i].Predecessor = actual
					}
				}
				if phase == "pending_before_config" {
					cfg, err := store.LoadConfig()
					if err != nil {
						t.Fatal(err)
					}
					cfg.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{proof.Legacy.SafeRoot}
					if err = store.SaveConfig(cfg); err != nil {
						t.Fatal(err)
					}
				}
				proof.Legacy.Digest, _ = watchedroots.ProjectWatchLegacyObservationHash(*proof.Legacy)
				if err = local.SaveProjectWatchReconcileState(proof); err != nil {
					t.Fatal(err)
				}
			}
			assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, msg))
			before, err := runtimeStore.LoadInstances()
			if err != nil {
				t.Fatal(err)
			}
			assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, msg))
			after, err := runtimeStore.LoadInstances()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("replay rewrote workers")
			}
		})
	}
}

func TestDeclarationLegacyNodeRefusesDrift(t *testing.T) {
	for _, scenario := range []string{"manual_kind", "config", "local_key", "disabled", "missing_worker", "other_safe_root", "physical_root", "safe_limit", "missing_source", "pending_worker", "pending_policy", "pending_membership", "pending_digest", "pending_operation", "pending_revision", "applied_worker"} {
		t.Run(scenario, func(t *testing.T) {
			store, config, state, payload := legacyProjectWatchNodeFixture(t)
			msg := projectWatchFixtureMessage(payload)
			runtimeStore := noderuntime.NewStore(store.DataDir)
			local := watchedroots.NewStore(store.DataDir)
			if strings.HasPrefix(scenario, "pending_") || scenario == "applied_worker" {
				assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, msg))
				saved, err := local.LoadProjectWatchReconcileState(payload.Group.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "applied_worker" {
					saved.PendingRevision = saved.AppliedRevision
					saved.PendingHash = saved.ConfigHash
					saved.AppliedRevision = 0
					saved.ConfigHash = ""
				}
				switch scenario {
				case "pending_membership":
					saved.RootKeys = saved.RootKeys[:1]
				case "pending_digest":
					saved.Legacy.Digest = "sha256:" + strings.Repeat("f", 64)
				case "pending_operation":
					saved.OperationID = ids.NewJobID()
				case "pending_revision":
					saved.PendingRevision++
				}
				path := filepath.Join(store.DataDir, "watched-roots", "projects", payload.Group.ProjectID+".json")
				if err = os.WriteFile(path, projectWatchJSON(saved), 0600); err != nil {
					t.Fatal(err)
				}
				config, err = store.LoadConfig()
				if err != nil {
					t.Fatal(err)
				}
			}
			instance, err := runtimeStore.LoadInstance(payload.Group.Roots[0].WorkerKey)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "manual_kind":
				instance.Kind = "manual"
			case "config":
				instance.ConfigHash = "sha256:" + strings.Repeat("f", 64)
			case "local_key":
				instance.LocalRootKey = "manual"
			case "disabled", "applied_worker":
				instance.Enabled = false
			case "pending_worker":
				instance.IntervalSeconds++
			case "missing_worker":
				instance.WorkerKey += ".other" // original file removal is below
			case "other_safe_root":
				config.Filesystem.SafeRoots = append(config.Filesystem.SafeRoots, filesystemconnector.DefaultSafeRoot("manual", payload.Group.ProjectRoot))
			case "physical_root":
				config.Filesystem.SafeRoots[0].AbsolutePath = t.TempDir()
			case "safe_limit":
				config.Filesystem.SafeRoots[0].MaxFileBytes = 1
			case "pending_policy":
				config.Filesystem.SafeRoots[0].AllowList = !config.Filesystem.SafeRoots[0].AllowList
			case "missing_source":
				if err = os.Remove(filepath.Join(payload.Group.ProjectRoot, ".loom/contracts/notes.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing_worker" {
				if err = runtimeStore.DeleteInstance(payload.Group.Roots[0].WorkerKey); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "manual_kind" || scenario == "config" || scenario == "local_key" || scenario == "disabled" || scenario == "applied_worker" || scenario == "pending_worker" {
				if err = runtimeStore.SaveInstance(instance); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.SaveConfig(config); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			ack := BuildProjectWatchReconcileAck(t.Context(), store, state, msg)
			if ack.AckStatus == communication.AckStatusCompleted {
				t.Fatalf("drift accepted: %s", ack.ResultJSON)
			}
			after, err := os.ReadFile(store.ConfigPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("failed preflight wrote config")
			}
		})
	}
}

func TestDeclarationLegacyNodePreservesUnselected(t *testing.T) {
	store, _, state, payload := legacyProjectWatchNodeFixture(t)
	runtimeStore := noderuntime.NewStore(store.DataDir)
	prior, err := runtimeStore.LoadInstance(payload.Group.Roots[0].WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	var cfg watchedroots.RootConfig
	if err = json.Unmarshal(prior.ConfigJSON, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.RootKey = "unrelated_user"
	cfg.DisplayName = "Another user of the old safe key"
	prior.WorkerKey = noderuntime.WatchedRootWorkerKey(cfg.RootKey)
	prior.LocalRootKey = cfg.RootKey
	prior.ConfigJSON = projectWatchJSON(cfg)
	prior.ConfigHash = watchedroots.ConfigHash(cfg)
	if err = runtimeStore.SaveInstance(prior); err != nil {
		t.Fatal(err)
	}
	before, err := runtimeStore.LoadInstance(prior.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(payload)))
	after, err := runtimeStore.LoadInstance(prior.WorkerKey)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("unselected old-key user changed")
	}
}
