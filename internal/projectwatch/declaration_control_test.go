package projectwatch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func declarationWatchControlFixture(t *testing.T) (pc.Analysis, []projects.DeclarationWatchContributor) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".loom/policies", "journal"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw := `kind: loom.project
schema_version: project.contract.v0.5
project:
  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV
  slug: shared-project
  name: Shared Project
  owner_node: main
resources:
  reading:
    kind: knowledge
    knowledge: {path: journal, category: research, protection: retained}
  retained:
    kind: protection
    protection: {policy_ref: .loom/policies/backup.yaml}
`
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
    - key: journal_owner
      path: journal
`
	if err := os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom/policies/backup.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	return pc.Analyze(root), []projects.DeclarationWatchContributor{{ActionID: "enroll_knowledge:reading", Owner: "knowledge", Resource: "reading"}, {ActionID: "reconcile_protection:retained", Owner: "backupcontracts", Resource: "retained"}}
}
func TestDeclarationWatchBindingPreservesCompilerAndRequiresAllOwners(t *testing.T) {
	analysis, contributors := declarationWatchControlFixture(t)
	if len(analysis.Plan.WatchedRoots) != 1 {
		t.Fatalf("shared source did not compile once: %+v", analysis.Report)
	}
	node := ids.NewNodeID()
	group, err := BuildDeclarationWatchGroup(analysis, node, "main", contributors)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BuildDeclarationWatchGroup(analysis, node, "main", contributors[:1]); err == nil {
		t.Fatal("protection applied without its intent")
	}
	root := group.Roots[0]
	if !bytes.Equal(root.CompilerConfigJSON, analysis.Plan.WatchedRoots[0].ConfigJSON) {
		t.Fatal("compiler bytes rewritten")
	}
	var original, effective watchedroots.RootConfig
	if err = json.Unmarshal(root.CompilerConfigJSON, &original); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(root.ConfigJSON, &effective); err != nil {
		t.Fatal(err)
	}
	if original.BackupPolicy.MaxPendingBytes != 9007199254740993 {
		t.Fatal("exact integer was rounded")
	}
	expected := original
	expected.SafeRootKey, _ = DeclarationSafeRootKey(group.ProjectID)
	if !reflect.DeepEqual(expected, effective) {
		t.Fatal("binding changed another policy field")
	}
	hash, err := DeclarationGroupHash(group)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []projects.DeclarationWatchToken{}
	for _, c := range group.Contributors {
		tokens = append(tokens, projects.DeclarationWatchToken{ActionID: c.ActionID, Owner: c.Owner, Resource: c.Resource, Token: hash, InputHash: hash})
	}
	payload := DeclarationWatchPayload{SchemaVersion: DeclarationWatchSchemaVersion, OperationID: ids.NewJobID(), GroupHash: hash, Evidence: communication.NewDesiredStateEvidence(node, 1, hash), Group: group, Tokens: tokens}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	// Journal/transport canonicalization may reorder objects; source config bytes
	// remain a byte string so the exact compiler representation survives.
	decoded, err := pc.DecodeDeclarationEvidenceJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := DecodeDeclarationWatchPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recovered.Group.Roots[0].CompilerConfigJSON, root.CompilerConfigJSON) {
		t.Fatal("transport changed compiler evidence")
	}
}

// Only a representation fixture: this data cannot authorize a domain mutation.
func legacyControlGroup(t *testing.T) projects.DeclarationWatchGroup {
	t.Helper()
	analysis, contributors := declarationWatchControlFixture(t)
	group, err := BuildDeclarationWatchGroup(analysis, ids.NewNodeID(), "main", contributors)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := &projects.DeclarationLegacyPredecessor{SchemaVersion: projects.DeclarationLegacyPredecessorSchema, ProjectID: group.ProjectID, RegistrationID: ids.NewProjectContractRegistrationID(), RegistrationRevision: 1, NodeID: group.NodeID, NodeKey: group.NodeKey, ProjectRoot: group.ProjectRoot, PhysicalRoot: group.ProjectRoot, ContractPath: filepath.Join(group.ProjectRoot, pc.CanonicalRootContractPath), ContractHash: "sha256:" + strings.Repeat("a", 64), ContractSchema: pc.ProjectSchemaV04, Members: []projects.DeclarationLegacyMember{}}
	p.Contract = controlTestJSON(map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV04, "project": map[string]string{"id": p.ProjectID, "owner_node": p.NodeKey}})
	for _, root := range group.Roots {
		var cfg watchedroots.RootConfig
		if err = json.Unmarshal(root.CompilerConfigJSON, &cfg); err != nil {
			t.Fatal(err)
		}
		var metadata map[string]json.RawMessage
		if err = json.Unmarshal(root.Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		delete(metadata, "declaration_sources")
		id := ids.NewWatchedRootID()
		row := projects.ProjectWatchedRootRegistration{ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(), ProjectContractRegistrationID: p.RegistrationID, ProjectID: p.ProjectID, NodeID: p.NodeID, OwnerNodeKey: p.NodeKey, LocalRootKey: root.LocalRootKey, BackendRootKey: root.BackendRootKey, WorkerKey: root.WorkerKey, SourceKinds: controlTestJSON(root.SourceKinds), SafeRootKey: "project", RootRelativePath: cfg.RootRelativePath, DisplayName: cfg.DisplayName, SyncMode: cfg.SyncPolicy.Mode, BackupMode: cfg.BackupPolicy.Mode, IndexMode: cfg.IndexPolicy.Mode, DeleteMode: cfg.DeletePolicy.Mode, ConfigHash: root.CompilerConfigHash, ConfigJSON: root.CompilerConfigJSON, CommandJSON: json.RawMessage(`[]`), WatchedRootID: &id, ActivationStatus: "reported", LastAppliedAt: &now, Metadata: controlTestJSON(metadata), CreatedAt: now}
		p.Members = append(p.Members, projects.DeclarationLegacyMember{Registration: row, Report: projects.DeclarationLegacyReport{WatchedRootID: id, NodeID: p.NodeID, RootKey: row.BackendRootKey, WorkerKey: row.WorkerKey, DisplayName: row.DisplayName, SafeRootKey: "project", Status: "healthy", ConfigHash: row.ConfigHash, ConfigJSON: row.ConfigJSON, CreatedAt: now, Metadata: map[string]json.RawMessage{}}})
	}
	sealLegacyControlPredecessor(t, p)
	group.Predecessor = p
	if err = ValidateDeclarationWatchGroup(group); err != nil {
		t.Fatal(err)
	}
	return group
}
func controlTestJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func sealLegacyControlPredecessor(t *testing.T, p *projects.DeclarationLegacyPredecessor) {
	t.Helper()
	p.Digest = ""
	v, err := pc.DecodeDeclarationEvidenceJSON(controlTestJSON(p))
	if err != nil {
		t.Fatal(err)
	}
	p.Digest, _ = communication.ConfigHash(v)
}
func TestDeclarationLegacyControlEvidenceAndBudget(t *testing.T) {
	for _, mutation := range []string{"digest", "membership", "report_worker", "activation", "precise_limit", "metadata", "oversized"} {
		t.Run(mutation, func(t *testing.T) {
			group := legacyControlGroup(t)
			p := group.Predecessor
			switch mutation {
			case "digest":
				p.Digest = "sha256:" + strings.Repeat("f", 64)
			case "membership":
				p.Members = append(p.Members, p.Members[0])
				sealLegacyControlPredecessor(t, p)
			case "report_worker":
				p.Members[0].Report.WorkerKey += "_foreign"
				sealLegacyControlPredecessor(t, p)
			case "activation":
				p.Members[0].Registration.ActivationStatus = "disabled"
				sealLegacyControlPredecessor(t, p)
			case "precise_limit":
				p.Members[0].Registration.ConfigJSON = []byte(strings.Replace(string(p.Members[0].Registration.ConfigJSON), "9007199254740993", "9007199254740992", 1))
				p.Members[0].Report.ConfigJSON = p.Members[0].Registration.ConfigJSON
				sealLegacyControlPredecessor(t, p)
			case "metadata":
				p.Members[0].Registration.Metadata = json.RawMessage(`{}`)
				sealLegacyControlPredecessor(t, p)
			case "oversized":
				var doc map[string]any
				_ = json.Unmarshal(p.Contract, &doc)
				doc["project"].(map[string]any)["name"] = strings.Repeat("x", communication.MaxProtectedFolderControlPayloadBytes)
				p.Contract = controlTestJSON(doc)
				sealLegacyControlPredecessor(t, p)
			}
			if ValidateDeclarationWatchGroup(group) == nil {
				t.Fatal("changed/oversized predecessor accepted")
			}
		})
	}
	group := legacyControlGroup(t)
	hash, err := DeclarationGroupHash(group)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []projects.DeclarationWatchToken{}
	for _, c := range group.Contributors {
		tokens = append(tokens, projects.DeclarationWatchToken{ActionID: c.ActionID, Owner: c.Owner, Resource: c.Resource, Token: hash, InputHash: hash})
	}
	payload := DeclarationWatchPayload{SchemaVersion: DeclarationWatchSchemaVersion, OperationID: ids.NewJobID(), GroupHash: hash, Evidence: communication.NewDesiredStateEvidence(group.NodeID, 1, hash), Group: group, Tokens: tokens}
	raw := controlTestJSON(payload)
	if _, err = DecodeDeclarationWatchPayload(raw); err != nil {
		t.Fatal(err)
	}
	t.Logf("full control bytes=%d; predecessor bytes=%d", len(raw), len(controlTestJSON(group.Predecessor)))
	var doc map[string]any
	v, err := pc.DecodeDeclarationEvidenceJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc = v.(map[string]any)
	doc["group"].(map[string]any)["predecessor"] = nil
	if _, err = DecodeDeclarationWatchPayload(controlTestJSON(doc)); err == nil {
		t.Fatal("null predecessor accepted")
	}
}
