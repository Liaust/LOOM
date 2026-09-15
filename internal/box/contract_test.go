package box

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseContractAcceptsCurrentWorkspaceAndMain(t *testing.T) {
	for _, profile := range []string{ProfileWorkspace, ProfileMain} {
		payload := validContractPayload(t, profile)
		contract, err := ParseContract([]byte(payload))
		if err != nil {
			t.Fatalf("ParseContract(%s) returned error: %v", profile, err)
		}
		if contract.SchemaVersion != SchemaVersion || contract.Profile != profile {
			t.Fatalf("unexpected contract: %#v", contract)
		}
		if contract.Areas[AreaDocuments].Path != "Documents" {
			t.Fatalf("documents area not normalized: %#v", contract.Areas[AreaDocuments])
		}
		if _, ok := contract.Areas[AreaDropzone]; ok {
			t.Fatalf("current %s contract unexpectedly declares Dropzone: %#v", profile, contract.Areas)
		}
		if profile == ProfileMain {
			if _, ok := contract.Areas[AreaLane]; ok {
				t.Fatalf("current main contract unexpectedly declares Lane: %#v", contract.Areas)
			}
			if contract.Areas[areaTopics].Path != "Topics" || contract.Areas[areaLibrary].Path != "Library" {
				t.Fatalf("current main topology missing Topics or Library: %#v", contract.Areas)
			}
		} else if contract.Areas[AreaLane].Path != DefaultLaneDirName {
			t.Fatalf("workspace lane area not normalized: %#v", contract.Areas[AreaLane])
		}
		if contract.Policies[PolicyBackupContracts] != DefaultBackupContractsRelDir {
			t.Fatalf("backup contract pointer not normalized: %#v", contract.Policies)
		}
	}
}

func TestParseContractAcceptsLegacyLaneAndDropzone(t *testing.T) {
	for _, profile := range []string{ProfileWorkspace, ProfileMain} {
		contract, err := ParseContract([]byte(legacyContractPayload(t, profile)))
		if err != nil {
			t.Fatalf("legacy %s contract should remain readable: %v", profile, err)
		}
		if contract.Areas[AreaLane].Path != DefaultLaneDirName || contract.Areas[AreaDropzone].Path != "Dropzone" {
			t.Fatalf("legacy %s intake areas were not preserved for inspection: %#v", profile, contract.Areas)
		}
		if contract.Policies[AreaLane] == "" || contract.Policies[AreaDropzone] == "" {
			t.Fatalf("legacy %s intake policies were not preserved for inspection: %#v", profile, contract.Policies)
		}
	}
}

func TestParseContractAcceptsLegacyLaunchpadWithoutDocuments(t *testing.T) {
	payload := strings.Replace(legacyContractPayload(t, ProfileWorkspace), `  documents:
    path: Documents
    enabled: true
`, "", 1)
	payload = strings.Replace(payload, `  lane:
    path: loom-lane
    enabled: true
`, "", 1)
	payload = strings.Replace(payload, `  documents: .loom/policies/documents.watch.yaml
`, "", 1)
	payload = strings.Replace(payload, `  lane: .loom/policies/lane.transfer.yaml
`, "", 1)
	payload = strings.Replace(payload, `  dropzone:
    path: Dropzone
`, `  launchpad:
    path: Launchpad
    enabled: true
  dropzone:
    path: Dropzone
`, 1)
	payload = strings.Replace(payload, `  dropzone: .loom/policies/dropzone.transfer.yaml
`, `  launchpad: .loom/policies/launchpad.watch.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
`, 1)

	contract, err := ParseContract([]byte(payload))
	if err != nil {
		t.Fatalf("legacy Launchpad contract should parse: %v\n%s", err, payload)
	}
	if _, ok := contract.Areas[AreaDocuments]; ok {
		t.Fatalf("legacy contract unexpectedly gained documents area: %#v", contract.Areas)
	}
	if contract.Areas[AreaLaunchpad].Path != "Launchpad" {
		t.Fatalf("legacy launchpad area missing: %#v", contract.Areas)
	}
}

func TestParseContractRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{
			name:    "schema",
			replace: "schema_version: loom.box.v0.4.1",
			with:    "schema_version: loom.box.v9",
			want:    "schema_version",
		},
		{
			name:    "profile",
			replace: "profile: workspace",
			with:    "profile: archive",
			want:    "profile",
		},
		{
			name:    "area traversal",
			replace: "path: Notes",
			with:    "path: ../Notes",
			want:    "must not contain",
		},
		{
			name:    "policy traversal",
			replace: "notes: .loom/policies/notes.watch.yaml",
			with:    "notes: ../policies/notes.watch.yaml",
			want:    "policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := strings.Replace(validContractPayload(t, ProfileWorkspace), tt.replace, tt.with, 1)
			_, err := ParseContract([]byte(payload))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestResolvePathAndProfilePrecedence(t *testing.T) {
	home := t.TempDir()
	resolved, err := Resolve(ResolveInput{
		ExplicitPath:    "~/Explicit Box",
		ConfiguredPath:  filepath.Join(home, "Config Box"),
		ExplicitProfile: ProfileWorkspace,
		ConfigProfile:   ProfileMain,
		NodeID:          "macbook",
		NodeRole:        ProfileMain,
		HomeDir:         home,
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.RootPath != filepath.Join(home, "Explicit Box") {
		t.Fatalf("RootPath = %q", resolved.RootPath)
	}
	if resolved.PathSource != "flag" || resolved.Profile != ProfileWorkspace || resolved.ProfileSource != "flag" {
		t.Fatalf("unexpected resolution: %#v", resolved)
	}

	resolved, err = Resolve(ResolveInput{ConfiguredPath: filepath.Join(home, "Config Box"), ConfigProfile: ProfileMain, NodeID: "main", NodeRole: ProfileWorkspace, HomeDir: home})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.PathSource != "config" || resolved.Profile != ProfileMain || resolved.ProfileSource != "config" {
		t.Fatalf("config precedence failed: %#v", resolved)
	}

	resolved, err = Resolve(ResolveInput{NodeID: "main", NodeRole: ProfileMain, HomeDir: home})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.RootPath != "/srv/loom/box" || resolved.PathSource != "default_main" || resolved.Profile != ProfileMain || resolved.ProfileSource != "node_role" {
		t.Fatalf("default resolution failed: %#v", resolved)
	}
}

func TestMainDefaultContractUsesActiveWorkspaceTopology(t *testing.T) {
	contract := DefaultContract(Resolved{RootPath: "/srv/loom/box", Profile: ProfileMain, OwnerNode: "main"})
	if _, ok := contract.Areas[AreaLane]; ok {
		t.Fatalf("main Box must not declare a visible Lane area: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaLane]; ok {
		t.Fatalf("main Box must not declare a Lane transfer policy: %#v", contract.Policies)
	}
	if _, ok := contract.Areas[AreaDropzone]; ok {
		t.Fatalf("main Box must not declare Dropzone: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaDropzone]; ok {
		t.Fatalf("main Box must not declare a Dropzone policy: %#v", contract.Policies)
	}
	if contract.Areas[areaTopics].Path != "Topics" || contract.Areas[areaLibrary].Path != "Library" {
		t.Fatalf("main Box missing active Topics/Library topology: %#v", contract.Areas)
	}
	for _, item := range ExpectedDirectories(contract) {
		if strings.Contains(filepath.ToSlash(item.RelativePath), ".loom/state") {
			t.Fatalf("durable Box contract includes volatile state path: %#v", item)
		}
	}
}

func TestWorkspaceDefaultContractRetainsLaneWithoutDropzone(t *testing.T) {
	contract := DefaultContract(Resolved{RootPath: "/tmp/loom-box", Profile: ProfileWorkspace, OwnerNode: "macbook"})
	if laneArea, ok := contract.Areas[AreaLane]; !ok || !laneArea.Enabled || laneArea.Path != DefaultLaneDirName {
		t.Fatalf("workspace Box must retain enabled Lane: %#v", contract.Areas)
	}
	if contract.Policies[AreaLane] != ".loom/policies/lane.transfer.yaml" {
		t.Fatalf("workspace Box must retain Lane policy: %#v", contract.Policies)
	}
	if _, ok := contract.Areas[AreaDropzone]; ok {
		t.Fatalf("workspace Box must not declare Dropzone: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaDropzone]; ok {
		t.Fatalf("workspace Box must not declare Dropzone policy: %#v", contract.Policies)
	}
}

func TestResolveRuntimeStateCompatibilityAndDivergence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loom-box")
	legacy := filepath.Join(root, ".loom", "state")
	canonical := filepath.Join(t.TempDir(), "box-state")
	if err := os.MkdirAll(filepath.Join(legacy, "lane"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "lane", "batch.json"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved := Resolved{RootPath: root, Profile: ProfileWorkspace, RuntimeStateRoot: canonical, LegacyStateRoot: legacy}
	state, err := ResolveRuntimeState(resolved)
	if err != nil {
		t.Fatalf("legacy compatibility resolution failed: %v", err)
	}
	if state.Source != "legacy" || state.ReadRoot != legacy || state.WriteRoot != legacy || !state.MigrationRequired {
		t.Fatalf("legacy compatibility resolution unexpected: %#v", state)
	}
	if err := os.MkdirAll(filepath.Join(canonical, "lane"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "lane", "batch.json"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err = ResolveRuntimeState(resolved)
	if err != nil || state.Source != "canonical" || state.ReadRoot != canonical {
		t.Fatalf("equivalent dual-root resolution unexpected: state=%#v err=%v", state, err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "lane", "batch.json"), []byte("divergent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRuntimeState(resolved); err == nil || !strings.Contains(err.Error(), "divergent") {
		t.Fatalf("divergent state must fail closed, err=%v", err)
	}
}

func TestResolveRuntimeStateIdenticalRootsSkipFilesystemInspection(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "state-file")
	if err := os.WriteFile(shared, []byte("not a directory; identity must short-circuit inspection"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := ResolveRuntimeState(Resolved{
		RootPath:         filepath.Join(root, "loom-box"),
		Profile:          ProfileMain,
		RuntimeStateRoot: shared,
		LegacyStateRoot:  filepath.Join(root, ".", "state-file"),
	})
	if err != nil {
		t.Fatalf("identical runtime roots must resolve before filesystem inspection: %v", err)
	}
	if state.Source != "shared" || state.ReadRoot != shared || state.WriteRoot != shared || state.MigrationRequired {
		t.Fatalf("identical runtime-root resolution unexpected: %#v", state)
	}
}

func TestResolveRuntimeStateRejectsDistinctSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	canonical := filepath.Join(root, "canonical-link")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(legacy, canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRuntimeState(Resolved{RootPath: filepath.Join(root, "box"), RuntimeStateRoot: canonical, LegacyStateRoot: legacy}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("distinct runtime symlink root error = %v", err)
	}
}

func TestInspectDivergentRuntimeStateStopsBeforeChildStatus(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loom-box")
	legacy := filepath.Join(root, ".loom", "state")
	canonical := filepath.Join(t.TempDir(), "box-state")
	for path, value := range map[string]string{
		filepath.Join(legacy, "lane", "batches", "batch.json"):    "legacy\n",
		filepath.Join(canonical, "lane", "batches", "batch.json"): "canonical\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	status := Inspect(Resolved{
		RootPath:         root,
		Profile:          ProfileWorkspace,
		RuntimeStateRoot: canonical,
		LegacyStateRoot:  legacy,
	})
	if status.State != "invalid" || status.RuntimeStateSource != "conflict" {
		t.Fatalf("divergent status did not fail closed: %#v", status)
	}
	if status.Lane != nil || status.DropzoneTransfers != nil {
		t.Fatalf("divergent status reached child readers: %#v", status)
	}
	if status.RuntimeStateReadRoot != "" || status.RuntimeStateWriteRoot != "" {
		t.Fatalf("divergent status exposed a fallback state root: %#v", status)
	}
}

func TestInspectMissingPartialAndValidBox(t *testing.T) {
	home := t.TempDir()
	resolved, err := Resolve(ResolveInput{ExplicitPath: filepath.Join(home, "LOOM Box"), ExplicitProfile: ProfileWorkspace, NodeID: "macbook", NodeRole: "workspace", HomeDir: home})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	status := Inspect(resolved)
	if status.State != "missing" || status.Initialized {
		t.Fatalf("missing status unexpected: %#v", status)
	}
	if len(status.WouldCreateFolders) == 0 || len(status.WouldCreateFiles) == 0 {
		t.Fatalf("missing status should include planned paths: %#v", status)
	}

	if err := os.MkdirAll(filepath.Join(resolved.RootPath, "Projects"), 0o755); err != nil {
		t.Fatalf("mkdir partial box: %v", err)
	}
	status = Inspect(resolved)
	if status.State != "partial" || status.ContractState != "missing" {
		t.Fatalf("partial status unexpected: %#v", status)
	}

	if err := writeValidBox(resolved); err != nil {
		t.Fatalf("write valid box: %v", err)
	}
	status = Inspect(resolved)
	if status.State != "ok" || !status.Initialized || status.ContractState != "valid" {
		t.Fatalf("valid status unexpected: %#v", status)
	}
	if status.DropzoneState != "" || status.DropzoneTransfers != nil {
		t.Fatalf("current Box should not report Dropzone status: %#v", status)
	}
}

func TestExpectedPolicyFilesSkipsBackupContractDirectory(t *testing.T) {
	resolved := Resolved{RootPath: t.TempDir(), Profile: ProfileWorkspace, OwnerNode: "macbook"}
	contract := DefaultContract(resolved)
	policies := ExpectedPolicyFiles(contract)
	for _, policy := range policies {
		if policy.Key == PolicyBackupContracts || policy.RelativePath == DefaultBackupContractsRelDir {
			t.Fatalf("backup contract directory should not be expected as a policy file: %#v", policy)
		}
	}
}

func validContractPayload(t *testing.T, profile string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), DefaultRootDirName)
	profileAreas := `  lane:
    path: loom-lane
    enabled: true
`
	profilePolicies := `  lane: .loom/policies/lane.transfer.yaml
`
	if profile == ProfileMain {
		profileAreas = `  topics:
    path: Topics
    enabled: true
  library:
    path: Library
    enabled: true
`
		profilePolicies = ""
	}
	return `schema_version: loom.box.v0.4.1
box_id: box_test
owner_node: test-node
profile: ` + profile + `
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  documents:
    path: Documents
    enabled: true
` + profileAreas + `default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  documents: .loom/policies/documents.watch.yaml
` + profilePolicies + `  backup_contracts: .loom/contracts/backup
`
}

func legacyContractPayload(t *testing.T, profile string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), DefaultRootDirName)
	dropzoneStatus := DropzoneTransferFuture
	if profile == ProfileMain {
		dropzoneStatus = DropzoneTransferInactive
	}
	return `schema_version: loom.box.v0.4.1
box_id: box_test
owner_node: test-node
profile: ` + profile + `
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  documents:
    path: Documents
    enabled: true
  lane:
    path: loom-lane
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
    transfer_status: ` + dropzoneStatus + `
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  documents: .loom/policies/documents.watch.yaml
  lane: .loom/policies/lane.transfer.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
  backup_contracts: .loom/contracts/backup
`
}

func writeValidBox(resolved Resolved) error {
	_, err := Init(InitInput{Resolved: resolved, NewBoxID: func() string { return "box_test" }})
	return err
}
