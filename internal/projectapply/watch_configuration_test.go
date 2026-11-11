package projectapply

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
)

func TestWatchConfigurationOnlyPreservesFunctionalPolicy(t *testing.T) {
	before := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "fixture__notes", SafeRootKey: "project", RootRelativePath: "notes"})
	after := before
	after.Watch = watchedroots.WatchConfig{Enabled: true, Backend: watchedroots.WatchBackendAuto, DebounceWindow: "2s"}
	after.Scan.FullRescanInterval = "15m"
	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	group := projects.DeclarationWatchGroup{ProjectID: "project", NodeID: "node", Roots: []projects.DeclarationWatchRoot{{BackendRootKey: before.RootKey, WorkerKey: "worker", LocalRootKey: "notes", Enabled: true, SourceKinds: []string{"notes"}, Metadata: raw(map[string]any{"knowledge_source": map[string]any{"ocr": false, "config_hash": watchedroots.ConfigHash(after)}}), ConfigJSON: raw(after), ConfigHash: watchedroots.ConfigHash(after)}}}
	row := projects.ProjectWatchedRootRegistration{ProjectID: "project", NodeID: "node", BackendRootKey: before.RootKey, WorkerKey: "worker", LocalRootKey: "notes", ActivationStatus: projects.ProjectWatchedRootRegistrationStatusReported, SourceKinds: raw([]string{"notes"}), Metadata: raw(map[string]any{"knowledge_source": map[string]any{"ocr": false, "config_hash": watchedroots.ConfigHash(before)}, "declaration_adapter": map[string]any{"schema_version": "project.watch.binding.v1", "project_id": "project", "node_id": "node", "effective_config_hash": watchedroots.ConfigHash(before)}}), ConfigJSON: raw(before), ConfigHash: watchedroots.ConfigHash(before)}
	if err := validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{row}); err != nil {
		t.Fatal(err)
	}
	retired := row
	retired.BackendRootKey = "old_trial"
	retired.ActivationStatus = projects.ProjectWatchedRootRegistrationStatusStale
	if err := validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{row, retired}); err != nil {
		t.Fatal("retained retired history prevents active configuration convergence", err)
	}
	retired.ActivationStatus = projects.ProjectWatchedRootRegistrationStatusReported
	if validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{row, retired}) == nil {
		t.Fatal("extra active root admitted")
	}
	for _, status := range []string{projects.ProjectWatchedRootRegistrationStatusPendingAgentApply, projects.ProjectWatchedRootRegistrationStatusDisabled, projects.ProjectWatchedRootRegistrationStatusStale} {
		pending := row
		pending.ActivationStatus = status
		if validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{pending}) == nil {
			t.Fatal("unacknowledged root accepted", status)
		}
	}
	stopped := row
	stopped.ActivationStatus = projects.ProjectWatchedRootRegistrationStatusDisabled
	var audit map[string]json.RawMessage
	if err := json.Unmarshal(stopped.Metadata, &audit); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"source": "project.deactivate", "facet": "watched_roots", "project_id": group.ProjectID, "project_slug": "fixture", "reason": "archive then restore"} {
		audit[key] = raw(value)
	}
	stopped.Metadata = raw(audit)
	if err := validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{stopped}); err != nil {
		t.Fatal("unchanged owner-stopped watch cannot be restarted explicitly", err)
	}
	t.Run("stop_overwrites_compiler_facet", func(t *testing.T) {
		g := group
		g.Roots = append([]projects.DeclarationWatchRoot(nil), group.Roots...)
		var desired map[string]json.RawMessage
		_ = json.Unmarshal(g.Roots[0].Metadata, &desired)
		desired["facet"] = raw("notes")
		desired["project_slug"] = raw("fixture")
		g.Roots[0].Metadata = raw(desired)
		if err := validateWatchConfigurationOnly(g, []projects.ProjectWatchedRootRegistration{stopped}); err != nil {
			t.Fatal("standard owner stop overwrote compiler facet and blocked restart", err)
		}
	})
	for key, value := range map[string]string{"source": "other", "facet": "other", "project_id": "other", "unexpected": "not owner audit"} {
		t.Run("stop_"+key, func(t *testing.T) {
			changed := stopped
			var metadata map[string]json.RawMessage
			_ = json.Unmarshal(changed.Metadata, &metadata)
			metadata[key] = raw(value)
			changed.Metadata = raw(metadata)
			if validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{changed}) == nil {
				t.Fatal("watch-only accepted unowned stop or unrelated metadata")
			}
		})
	}
	changed := stopped
	var changedMetadata map[string]json.RawMessage
	_ = json.Unmarshal(changed.Metadata, &changedMetadata)
	changedMetadata["knowledge_source"] = raw(map[string]any{"ocr": true})
	changed.Metadata = raw(changedMetadata)
	if validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{changed}) == nil {
		t.Fatal("controlled stop bypassed enrichment policy comparison")
	}
	for _, status := range []string{projects.ProjectWatchedRootRegistrationStatusPendingAgentApply, projects.ProjectWatchedRootRegistrationStatusStale} {
		changed := stopped
		changed.ActivationStatus = status
		if validateWatchConfigurationOnly(group, []projects.ProjectWatchedRootRegistration{changed}) == nil {
			t.Fatal("stop audit bypassed non-disabled pending/stale state", status)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*projects.DeclarationWatchGroup)
	}{
		{"membership", func(g *projects.DeclarationWatchGroup) { g.Roots = nil }},
		{"retirement", func(g *projects.DeclarationWatchGroup) {
			g.Contributors = []projects.DeclarationWatchContributor{{Retire: true}}
		}},
		{"disabled", func(g *projects.DeclarationWatchGroup) { g.Roots[0].Enabled = false }},
		{"worker", func(g *projects.DeclarationWatchGroup) { g.Roots[0].WorkerKey = "other" }},
		{"enrichment", func(g *projects.DeclarationWatchGroup) {
			g.Roots[0].Metadata = raw(map[string]any{"knowledge_source": map[string]any{"ocr": true}})
		}},
		{"source_kind", func(g *projects.DeclarationWatchGroup) { g.Roots[0].SourceKinds = []string{"backup"} }},
		{"path", func(g *projects.DeclarationWatchGroup) {
			c := after
			c.RootRelativePath = "other"
			g.Roots[0].ConfigJSON = raw(c)
			g.Roots[0].ConfigHash = watchedroots.ConfigHash(c)
		}},
		{"backup", func(g *projects.DeclarationWatchGroup) {
			c := after
			c.BackupPolicy.MaxFileBytes++
			g.Roots[0].ConfigJSON = raw(c)
			g.Roots[0].ConfigHash = watchedroots.ConfigHash(c)
		}},
		{"scan_budget", func(g *projects.DeclarationWatchGroup) {
			c := after
			c.Scan.MaxHashBytesPerRun++
			g.Roots[0].ConfigJSON = raw(c)
			g.Roots[0].ConfigHash = watchedroots.ConfigHash(c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := group
			g.Roots = append([]projects.DeclarationWatchRoot(nil), group.Roots...)
			tc.change(&g)
			if err := validateWatchConfigurationOnly(g, []projects.ProjectWatchedRootRegistration{row}); err == nil {
				t.Fatal("watch-only scope admitted functional drift")
			}
		})
	}
}

func TestWatchConfigurationScopeRefusesUnrelatedActions(t *testing.T) {
	p := testPrincipal()
	x := testResolution(p, 2)
	x.Plan.Basis.Effects = []pc.DeclarationEffect{pc.DeclarationWatches}
	service := testService(p, x)
	if service.validate(t.Context(), p, &x, x.Plan.Basis.Effects) == nil {
		t.Fatal("project registration admitted in watch-only scope")
	}
	a := x.Plan.Basis.Actions[1]
	a.DependsOn = []string{}
	x.Plan.Basis.Actions = []pc.DeclarationAction{a}
	x.Payloads = map[string]json.RawMessage{a.ID: x.Payloads[a.ID]}
	if err := service.validate(t.Context(), p, &x, x.Plan.Basis.Effects); err != nil {
		t.Fatal(err)
	}
}

func TestWatchConfigurationApplicationReceiptRefresh(t *testing.T) {
	before := projects.DeclarationApplicationDataBinding{Application: "receiver", Data: "exports", Subpath: "notebooks", BindingRef: "exports", Path: "/data/exports", PoolIdentity: "pool", Device: 1, Inode: 1 << 54, PoolInode: 3, UID: 4, GID: 5, Mode: 0700, InstallationRevision: "old", PolicyRevision: "policy", LocationRevision: "location"}
	after := before
	after.InstallationRevision = "new"
	g := projects.DeclarationWatchGroup{ProjectID: "project", NodeID: "node"}
	root := projects.DeclarationWatchRoot{ApplicationData: &after, Metadata: json.RawMessage(`{}`)}
	row := projects.ProjectWatchedRootRegistration{ConfigHash: "config"}
	metadata := func(b any) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"declaration_adapter": map[string]any{"schema_version": "project.watch.binding.v1", "project_id": g.ProjectID, "node_id": g.NodeID, "effective_config_hash": row.ConfigHash, "application_data": b}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	row.Metadata = metadata(before)
	if !watchConfigurationMetadataMatches(g, root, row) {
		t.Fatal("unchanged allocation blocked by installation receipt refresh")
	}
	for _, mutate := range []func(*projects.DeclarationApplicationDataBinding){
		func(b *projects.DeclarationApplicationDataBinding) { b.Application = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.Data = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.Subpath = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.BindingRef = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.Path = "/other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.PoolIdentity = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.Device++ },
		func(b *projects.DeclarationApplicationDataBinding) { b.Inode++ },
		func(b *projects.DeclarationApplicationDataBinding) { b.PoolInode++ },
		func(b *projects.DeclarationApplicationDataBinding) { b.UID++ },
		func(b *projects.DeclarationApplicationDataBinding) { b.GID++ },
		func(b *projects.DeclarationApplicationDataBinding) { b.Mode++ },
		func(b *projects.DeclarationApplicationDataBinding) { b.PolicyRevision = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.LocationRevision = "other" },
		func(b *projects.DeclarationApplicationDataBinding) { b.InstallationRevision = "" },
	} {
		changed := before
		mutate(&changed)
		row.Metadata = metadata(changed)
		if watchConfigurationMetadataMatches(g, root, row) {
			t.Fatal("functional allocation drift accepted", changed)
		}
	}
	row.Metadata = metadata(map[string]any{"installation_revision": "old", "unexpected": true})
	if watchConfigurationMetadataMatches(g, root, row) {
		t.Fatal("unknown binding field accepted")
	}
}

func TestWatchConfigurationEffectIsExclusive(t *testing.T) {
	if err := effects([]pc.DeclarationEffect{pc.DeclarationWatches}); err != nil {
		t.Fatal(err)
	}
	for _, xs := range [][]pc.DeclarationEffect{{pc.DeclarationWatches, pc.DeclarationReconcile}, {pc.DeclarationWatches, pc.DeclarationProjections}, {pc.DeclarationWatches, pc.DeclarationWatches}} {
		if err := effects(xs); err == nil {
			t.Fatal("mixed watch-only effects were accepted")
		}
	}
}

func TestWatchConfigurationWorkStateRebind(t *testing.T) {
	root := t.TempDir()
	declaration := `kind: loom.project
schema_version: project.contract.v0.5
project:
  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV
  slug: work-state
  name: Work State
  owner_node: main
  status: active
resources:
  notes: {kind: knowledge, knowledge: {path: notes, category: notes, protection: retained}}
  retained: {kind: protection, protection: {policy_ref: .loom/policies/backup.yaml, path: .}}
`
	migrationWrite(t, root, "notes/retained.md", "retained source")
	migrationWrite(t, root, ".loom/policies/backup.yaml", `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  defaults: {max_file_bytes: 4096, max_batch_bytes: 8192, max_pending_bytes: 16384}
  roots:
    - {key: notes_owner, path: notes}
    - {key: controls, path: ., include: ['.loom/**']}
`)
	node := ids.NewNodeID()
	compile := func(state string) projects.DeclarationWatchGroup {
		t.Helper()
		raw := declaration
		if state != "" {
			raw = strings.Replace(raw, "project:\n", "project:\n  work_state: "+state+"\n", 1)
		}
		migrationWrite(t, root, pc.CanonicalRootContractPath, raw)
		a := pc.Analyze(root)
		if !a.Report.OK {
			t.Fatal(a.Report.Diagnostics)
		}
		g, err := projectwatch.BuildDeclarationWatchGroup(a, node, "main", []projects.DeclarationWatchContributor{
			{ActionID: "enroll_knowledge:notes", Owner: "knowledge", Resource: "notes"},
			{ActionID: "reconcile_protection:retained", Owner: "backupcontracts", Resource: "retained"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	encode := func(v any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	applied := compile("")
	if len(applied.Roots) != 2 {
		t.Fatal("fixture must exercise knowledge and protection-only roots")
	}
	for _, state := range []string{"passive", "working", ""} {
		desired := compile(state)
		rows := make([]projects.ProjectWatchedRootRegistration, 0, len(applied.Roots))
		for _, r := range applied.Roots {
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(r.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			metadata["declaration_adapter"] = encode(map[string]any{"schema_version": "project.watch.binding.v1", "project_id": applied.ProjectID, "node_id": node, "effective_config_hash": r.ConfigHash})
			rows = append(rows, projects.ProjectWatchedRootRegistration{ProjectID: applied.ProjectID, NodeID: node, BackendRootKey: r.BackendRootKey, WorkerKey: r.WorkerKey, LocalRootKey: r.LocalRootKey, ActivationStatus: projects.ProjectWatchedRootRegistrationStatusReported, SourceKinds: encode(r.SourceKinds), ConfigJSON: r.ConfigJSON, ConfigHash: r.ConfigHash, Metadata: encode(metadata)})
		}
		beforeRows, beforeDesired := encode(rows), encode(desired)
		if err := validateWatchConfigurationOnly(desired, rows); err != nil {
			t.Fatalf("classification-only %q rebind refused: %v", state, err)
		}
		if !same(beforeRows, encode(rows)) || !same(beforeDesired, encode(desired)) {
			t.Fatal("comparison rewrote actual source audit evidence")
		}
		for i, r := range desired.Roots {
			if r.ConfigHash != applied.Roots[i].ConfigHash || !same(r.ConfigJSON, applied.Roots[i].ConfigJSON) || same(r.Metadata, applied.Roots[i].Metadata) {
				t.Fatal("classification must change audit metadata, not watch configuration")
			}
			if len(r.KnowledgeSource) == 0 {
				continue
			}
			for _, drift := range []string{"source_hash", "source_ref", "policy", "unknown", "missing_sources", "raw", "declaration_name"} {
				t.Run(state+"/"+drift, func(t *testing.T) {
					changed := r
					var metadata, source map[string]json.RawMessage
					_ = json.Unmarshal(r.Metadata, &metadata)
					_ = json.Unmarshal(metadata["knowledge_source"], &source)
					switch drift {
					case "source_hash":
						source["source_hash"] = encode("sha256:unbound")
					case "source_ref":
						source["source_ref"] = encode(".loom/other.yaml")
					case "policy":
						source["policy"] = encode(map[string]any{"processing": map[string]any{"ocr": "off"}})
					case "unknown":
						metadata["unexpected"] = encode(true)
					case "missing_sources":
						delete(metadata, "declaration_sources")
					case "raw", "declaration_name":
						var sources []pc.DeclarationSourceSnapshot
						_ = json.Unmarshal(metadata["declaration_sources"], &sources)
						for j := range sources {
							if sources[j].Ref != pc.CanonicalRootContractPath {
								continue
							}
							if drift == "raw" {
								sources[j].Raw = append(sources[j].Raw, '\n')
							} else {
								sources[j].Raw = []byte(strings.Replace(string(sources[j].Raw), "name: Work State", "name: Other", 1))
								sources[j].Hash = hashBytes(sources[j].Raw)
								sources[j].Revision = sources[j].Hash
								source["source_hash"] = encode(sources[j].Hash)
							}
						}
						metadata["declaration_sources"] = encode(sources)
					}
					metadata["knowledge_source"] = encode(source)
					changed.Metadata = encode(metadata)
					if watchConfigurationMetadataMatches(desired, changed, rows[i]) {
						t.Fatal("classification audit refresh admitted drift")
					}
				})
			}
		}
		applied = desired
	}
}
