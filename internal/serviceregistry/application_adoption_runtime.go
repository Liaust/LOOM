package serviceregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The host shim supplies only protected node configuration and its fixed direct
// command runner. The existing application lock must remain held throughout.
type applicationProjectStateRuntime struct {
	store               ApplicationStateStore
	tools               ApplicationStateACLTools
	systemctl           string
	run                 ApplicationStateACLCommandRunner
	trustedParentOwners map[string]uint32
	trustedParentGroups map[string]uint32
}

type applicationProjectStateJournal struct {
	Schema   string                         `json:"schema"`
	Policy   ApplicationProjectStatePolicy  `json:"policy"`
	UID      uint32                         `json:"uid"`
	State    string                         `json:"state"`
	Adoption ApplicationStateAdoptionRecord `json:"adoption"`
}

func applicationProjectStateJournalKey(p ApplicationProjectStatePolicy) string {
	return "adoption-" + p.Owner.Instance() + "-" + applicationSHA([]string{p.ProjectRoot, p.RelativePath})[7:]
}

func (r applicationProjectStateRuntime) request(p ApplicationProjectStatePolicy, uid uint32) (ApplicationStateAdoptionRequest, error) {
	id := p.DirectoryIdentity
	q := ApplicationStateAdoptionRequest{
		ProjectRoot: p.ProjectRoot, Directory: p.RelativePath, ProjectUID: p.ProjectUID, ProjectGID: p.ProjectGID,
		ApplicationUID: uid, ApplicationGroups: []uint32{uid}, Mode: ApplicationStateAdopt, Expected: &id,
		RootIdentity: p.RootIdentity, Files: append([]ApplicationStateAdoptionFile(nil), p.Files...),
		AllowedParentOwners: map[string]uint32{}, AllowedParentGroups: map[string]uint32{},
		NormalizePrivateDefaults: p.NormalizePrivateDefaults,
	}
	if !p.Owner.valid() {
		return q, applicationError("project_state.owner_invalid")
	}
	if err := applicationAdoptionValidate(q); err != nil {
		return q, err
	}
	// The planner authorizes the exact registered root; ancestors use only
	// exact host-configured owners/groups, not a state-derived ancestor group.
	for path := filepath.Dir(filepath.Join(p.ProjectRoot, p.RelativePath)); path != "/"; path = filepath.Dir(path) {
		if owner, ok := r.trustedParentOwners[path]; ok {
			q.AllowedParentOwners[path] = owner
		}
		if group, ok := r.trustedParentGroups[path]; ok {
			q.AllowedParentGroups[path] = group
		}
	}
	if owner, ok := q.AllowedParentOwners[p.ProjectRoot]; ok && owner != p.RootIdentity.UID {
		return q, applicationError("project_state.parent_policy_conflict")
	}
	q.AllowedParentOwners[p.ProjectRoot] = p.RootIdentity.UID
	if group, ok := q.AllowedParentGroups[p.ProjectRoot]; ok && group != p.RootIdentity.GID {
		return q, applicationError("project_state.parent_policy_conflict")
	}
	q.AllowedParentGroups[p.ProjectRoot] = p.RootIdentity.GID
	return q, applicationAdoptionValidate(q)
}

// Parse a fixed systemctl show result, not is-active exit codes or stderr.
// Unknown/missing properties, queued jobs and any running control process fail
// closed. The template uses its dedicated UID/GID, no supplementary groups.
func applicationProjectStateStopped(raw []byte) bool {
	if len(raw) > 16384 {
		return false
	}
	properties := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false
		}
		switch key {
		case "LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "Job":
		default:
			return false
		}
		if _, exists := properties[key]; exists {
			return false
		}
		properties[key] = value
	}
	return len(properties) == 6 && (properties["LoadState"] == "loaded" || properties["LoadState"] == "not-found") &&
		properties["ActiveState"] == "inactive" && properties["SubState"] == "dead" &&
		properties["MainPID"] == "0" && properties["ControlPID"] == "0" && properties["Job"] == ""
}

func (r applicationProjectStateRuntime) ensure(ctx context.Context, p ApplicationDataPolicy, uid uint32, prior *ApplicationDataIdentity) (ApplicationDataIdentity, error) {
	var empty ApplicationDataIdentity
	if p.StorageClass != ApplicationProjectStateStorageClass || p.ProjectState == nil || r.run == nil || !applicationAbsolutePath(r.systemctl) {
		return empty, applicationError("project_state.policy_invalid")
	}
	policy := *p.ProjectState
	if p.Path != filepath.Join(policy.ProjectRoot, policy.RelativePath) || p.Pool != policy.ProjectRoot {
		return empty, applicationError("project_state.path_conflict")
	}
	q, err := r.request(policy, uid)
	if err != nil {
		return empty, err
	}
	// No re-observation or enumeration here: a runtime-created WAL/file is not
	// a new grant revision. Frozen DB/path identities still fence replacement.
	handles, err := applicationAdoptionOpen(q, false)
	if err != nil {
		return empty, err
	}
	actual, err := applicationAdoptionIdentity(handles, q)
	applicationAdoptionClose(handles)
	if err != nil {
		return empty, err
	}
	if prior != nil && !applicationSameDataIdentity(*prior, actual) {
		return empty, applicationError("data.identity_migration_required")
	}
	if err = r.store.directory(); err != nil {
		return empty, err
	}
	fence := func(ctx context.Context) error {
		raw, err := r.run(ctx, r.systemctl, "show", policy.Owner.Unit(), "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,Job")
		if err != nil || !applicationProjectStateStopped(raw) {
			return applicationError("project_state.service_not_quiesced")
		}
		return nil
	}
	adopter, err := NewApplicationStateAdopter(r.tools, r.run, fence)
	if err != nil {
		return empty, err
	}
	key := applicationProjectStateJournalKey(policy)
	var journal applicationProjectStateJournal
	err = r.store.read(key, &journal)
	if err == nil {
		if journal.Schema != "application.project-state-adoption.v1" || applicationSHA(journal.Policy) != applicationSHA(policy) || journal.UID != uid ||
			applicationSHA(journal.Adoption.Request) != applicationSHA(q) || !applicationSameDataIdentity(journal.Adoption.Identity, actual) ||
			(journal.State != "pending" && journal.State != "ready") {
			return empty, applicationError("project_state.journal_conflict")
		}
		if err = adopter.Check(ctx, journal.Adoption); err != nil {
			if journal.State == "pending" {
				// Preserve the original before ACLs. Never manufacture a new
				// baseline from partially applied access on replay.
				return empty, applicationError("project_state.adoption_recovery_required")
			}
			return empty, err
		}
		if journal.State == "pending" {
			journal.State = "ready"
			if err = r.store.write(key, journal); err != nil {
				return empty, err
			}
		}
		return actual, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return empty, err
	}
	if prior != nil {
		return empty, applicationError("project_state.journal_missing")
	}
	journal = applicationProjectStateJournal{Schema: "application.project-state-adoption.v1", Policy: policy, UID: uid, State: "pending"}
	record, err := adopter.Adopt(ctx, q, func(record ApplicationStateAdoptionRecord) error {
		journal.Adoption = record
		return r.store.write(key, journal)
	})
	if err != nil {
		return empty, err
	}
	journal.Adoption, journal.State = record, "ready"
	if err = r.store.write(key, journal); err != nil {
		return empty, err
	}
	return record.Identity, nil
}
