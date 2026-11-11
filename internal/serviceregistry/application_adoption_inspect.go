package serviceregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// inspect observes frozen state and the private ACL journal, never a deployment
// effect. In particular it must not create the store or reconcile pending work.
func (r applicationProjectStateRuntime) inspect(ctx context.Context, p ApplicationDataPolicy, uid uint32, prior ApplicationDataIdentity) (out ApplicationPrerequisiteData, revision string) {
	out = ApplicationPrerequisiteData{BindingRef: p.BindingRef, Availability: "unavailable", Custody: "unknown"}
	var actual ApplicationDataIdentity
	journalHash, issue := "unread", ""
	defer func() {
		revision = applicationSHA(struct {
			Data     ApplicationPrerequisiteData
			Observed ApplicationDataIdentity
			Journal  string
			Issue    string
		}{out, actual, journalHash, issue})
	}()
	if ctx.Err() != nil {
		issue = "cancelled"
		return
	}
	if p.StorageClass != ApplicationProjectStateStorageClass || p.ProjectState == nil {
		issue = "policy_invalid"
		return
	}
	policy := *p.ProjectState
	if uid != applicationAccountUID(policy.Owner) {
		issue = "application_uid_conflict"
		return
	}
	if p.Path != filepath.Join(policy.ProjectRoot, policy.RelativePath) || p.Pool != policy.ProjectRoot {
		issue = "path_conflict"
		return
	}
	q, err := r.request(policy, uid)
	if err != nil {
		issue = applicationSafeError(err)
		return
	}
	handles, err := applicationAdoptionOpen(q, false)
	if err != nil {
		issue = applicationSafeError(err)
		return
	}
	defer applicationAdoptionClose(handles)
	actual, err = applicationAdoptionIdentity(handles, q)
	if err != nil {
		issue = applicationSafeError(err)
		return
	}
	var mode uint32
	for _, h := range handles {
		if h.path == p.Path {
			var st unix.Stat_t
			if unix.Fstat(int(h.file.Fd()), &st) != nil {
				issue = "state_stat_unavailable"
				return
			}
			mode = uint32(st.Mode) & 07777
		}
	}
	// Validate existing store custody without directory(), Mkdir or locks. An
	// absent store is an absent adoption record, not a request to initialize it.
	if !applicationAbsolutePath(r.store.Root) || applicationCheckTrustedParents(r.store.Root, r.store.OwnerUID, r.store.TrustedParentOwners) != nil {
		issue = "journal_parent_custody"
		return
	}
	var journal applicationProjectStateJournal
	err = applicationCustody(r.store.Root, r.store.OwnerUID, true)
	if err == nil {
		err = r.store.read(applicationProjectStateJournalKey(policy), &journal)
	}
	if errors.Is(err, os.ErrNotExist) {
		journalHash = "absent"
	} else if err != nil {
		issue = "journal_unavailable"
		return
	} else {
		journalHash = applicationSHA(journal)
	}
	// Metadata remains bounded to the approved directory/frozen file list.
	// No new WAL, history, payload or directory entry is enumerated here.
	out.Availability, out.Custody = "available", "unclaimed"
	out.PoolIdentity = applicationSHA(struct {
		Path, Filesystem string
		Inode            uint64
	}{p.Pool, actual.Filesystem, actual.PoolInode})
	out.Identity = &ApplicationPrerequisiteDataIdentity{Device: actual.Device, Inode: actual.Inode, PoolInode: actual.PoolInode, UID: actual.UID, GID: actual.GID, Mode: mode}
	if journalHash == "absent" {
		if prior != (ApplicationDataIdentity{}) {
			out.Custody, issue = "conflict", "journal_missing"
		}
		return
	}
	if journal.Schema != "application.project-state-adoption.v1" || applicationSHA(journal.Policy) != applicationSHA(policy) || journal.UID != uid ||
		applicationSHA(journal.Adoption.Request) != applicationSHA(q) || !applicationSameDataIdentity(journal.Adoption.Identity, actual) {
		out.Custody, issue = "conflict", "journal_conflict"
		return
	}
	if journal.State != "ready" {
		out.Custody, issue = "conflict", "adoption_not_ready"
		return
	}
	// Defense in depth: even a later change to Check cannot reach a write tool,
	// service query or effect fence through this read-only adapter.
	read := func(ctx context.Context, tool string, args ...string) ([]byte, error) {
		if tool != r.tools.Getfacl || r.run == nil {
			return nil, applicationError("project_state.inspect_read_only")
		}
		return r.run(ctx, tool, args...)
	}
	adopter, err := NewApplicationStateAdopter(r.tools, read, func(context.Context) error {
		return applicationError("project_state.inspect_read_only")
	})
	if err == nil {
		err = adopter.Check(ctx, journal.Adoption)
	}
	if err != nil {
		issue = applicationSafeError(err)
		out = ApplicationPrerequisiteData{BindingRef: p.BindingRef, Availability: "unavailable", Custody: "unknown"}
		return
	}
	if prior != (ApplicationDataIdentity{}) {
		out.Custody = "conflict"
		if applicationSameDataIdentity(actual, prior) {
			out.Custody, out.Path = "matches", p.Path
		}
	}
	return
}
