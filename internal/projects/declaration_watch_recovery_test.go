package projects

import (
	"encoding/json"
	"testing"
)

func TestCompletedDeclarationWatchRecoversOnlyExactDeactivatedRoot(t *testing.T) {
	root := DeclarationWatchRoot{Enabled: true, BackendRootKey: "pilot__notes", LocalRootKey: "notes", WorkerKey: "node-agent.watched_root.pilot__notes", ConfigHash: "config", ConfigJSON: json.RawMessage(`{"root_key":"pilot__notes"}`)}
	group := DeclarationWatchGroup{ProjectID: "project", NodeID: "node", ProjectRoot: "/box/pilot", Roots: []DeclarationWatchRoot{root}}
	hash, err := declarationValueDigest(group)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(DeclarationWatchIntentPayload{Group: group, GroupHash: hash})
	current := ProjectWatchedRootRegistration{ProjectID: group.ProjectID, NodeID: group.NodeID, ActivationStatus: ProjectWatchedRootRegistrationStatusDisabled, BackendRootKey: root.BackendRootKey, LocalRootKey: root.LocalRootKey, WorkerKey: root.WorkerKey, ConfigHash: root.ConfigHash, ConfigJSON: root.ConfigJSON, Metadata: json.RawMessage(`{"source":"project.deactivate","project_id":"project"}`)}
	if !completedDeclarationWatchOwns(current, group, raw, hash) {
		t.Fatal("exact completed predecessor refused")
	}
	for name, mutate := range map[string]func(*ProjectWatchedRootRegistration){
		"active":           func(r *ProjectWatchedRootRegistration) { r.ActivationStatus = "reported" },
		"foreign_project":  func(r *ProjectWatchedRootRegistration) { r.ProjectID = "other" },
		"foreign_node":     func(r *ProjectWatchedRootRegistration) { r.NodeID = "other" },
		"different_root":   func(r *ProjectWatchedRootRegistration) { r.BackendRootKey = "other" },
		"different_worker": func(r *ProjectWatchedRootRegistration) { r.WorkerKey = "other" },
		"config_drift":     func(r *ProjectWatchedRootRegistration) { r.ConfigHash = "other" },
		"config_bytes":     func(r *ProjectWatchedRootRegistration) { r.ConfigJSON = json.RawMessage(`{}`) },
		"not_deactivated":  func(r *ProjectWatchedRootRegistration) { r.Metadata = json.RawMessage(`{}`) },
		"other_owner": func(r *ProjectWatchedRootRegistration) {
			r.Metadata = json.RawMessage(`{"source":"project.deactivate","project_id":"project","declaration_adapter":{"project_id":"other"}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := current
			mutate(&changed)
			if completedDeclarationWatchOwns(changed, group, raw, hash) {
				t.Fatal("accepted mismatched predecessor")
			}
		})
	}
	if completedDeclarationWatchOwns(current, group, raw, "different") {
		t.Fatal("wrong receipt digest accepted")
	}
	group.ProjectRoot = "/box/other"
	if completedDeclarationWatchOwns(current, group, raw, hash) {
		t.Fatal("other source accepted")
	}
}
