package serviceregistry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"loom.local/loom/internal/projectquiescence"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const ApplicationPrerequisiteSchema = "application.prerequisites.v1"

type ApplicationPrerequisiteQuery struct {
	SchemaVersion string           `json:"schema_version"`
	Owner         ApplicationOwner `json:"owner"`
}

// This snapshot describes scoped existing facts; effects must revalidate them.
// No field attests available quota, reservation, protection or fresh health.
type ApplicationPrerequisiteSnapshot struct {
	SchemaVersion    string                                       `json:"schema_version"`
	Owner            ApplicationOwner                             `json:"owner"`
	IdentityRevision string                                       `json:"identity_revision"`
	CollectedAt      time.Time                                    `json:"collected_at"`
	GrantMissing     bool                                         `json:"grant_missing,omitempty"`
	PolicyRevision   string                                       `json:"policy_revision"`
	LocationRevision string                                       `json:"location_revision"`
	RepositoryID     string                                       `json:"repository_id"`
	Platform         string                                       `json:"platform"`
	Publication      ApplicationPrerequisitePublication           `json:"publication"`
	Installation     ApplicationPrerequisiteInstallation          `json:"installation"`
	Data             map[string]ApplicationPrerequisiteData       `json:"data"`
	Credentials      map[string]ApplicationPrerequisiteCredential `json:"credentials"`
	ApprovedEdgeRef  *string                                      `json:"approved_edge_ref,omitempty"`
	Observation      *ApplicationPrerequisiteObservation          `json:"observation,omitempty"`
}

type ApplicationPrerequisiteArtifact struct {
	DescriptorDigest    string              `json:"descriptor_digest"`
	RepositoryID        string              `json:"repository_id"`
	SourceRevision      string              `json:"source_revision"`
	SourceDigest        string              `json:"source_digest"`
	Artifact            ApplicationArtifact `json:"artifact"`
	ConfigurationSchema string              `json:"configuration_schema"`
	ConfigurationDigest string              `json:"configuration_digest"`
	DataFormat          string              `json:"data_format"`
}
type ApplicationPrerequisitePublication struct {
	ArchiveTarget    *projectquiescence.Target        `json:"archive_target,omitempty"`
	Present          bool                             `json:"present"`
	Revision         string                           `json:"revision,omitempty"`
	PolicyRevision   string                           `json:"policy_revision,omitempty"`
	LocationRevision string                           `json:"location_revision,omitempty"`
	GrantMatches     *bool                            `json:"grant_matches,omitempty"`
	Artifact         *ApplicationPrerequisiteArtifact `json:"artifact,omitempty"`
}
type ApplicationPrerequisiteInstallation struct {
	Present    bool                             `json:"present"`
	Revision   string                           `json:"revision,omitempty"`
	Generation string                           `json:"generation,omitempty"`
	Committed  *bool                            `json:"committed,omitempty"`
	Applied    *bool                            `json:"applied,omitempty"`
	Retired    *bool                            `json:"retired,omitempty"`
	Fenced     *bool                            `json:"fenced,omitempty"`
	Artifact   *ApplicationPrerequisiteArtifact `json:"artifact,omitempty"`
}
type ApplicationPrerequisiteData struct {
	BindingRef   string                               `json:"binding_ref,omitempty"`
	Availability string                               `json:"availability"`
	Custody      string                               `json:"custody"`
	PoolIdentity string                               `json:"pool_identity,omitempty"`
	Identity     *ApplicationPrerequisiteDataIdentity `json:"identity,omitempty"`
}
type ApplicationPrerequisiteDataIdentity struct {
	Inode     uint64 `json:"inode"`
	PoolInode uint64 `json:"pool_inode"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
}
type ApplicationPrerequisiteCredential struct {
	Revision     string `json:"revision"`
	Availability string `json:"availability"`
}

// Observation is retained startup evidence, not health at CollectedAt.
type ApplicationPrerequisiteObservation struct {
	ObservedAt        time.Time            `json:"observed_at"`
	State             string               `json:"state"`
	ProcessState      string               `json:"process_state"`
	Readiness         ApplicationReadiness `json:"readiness"`
	ManagementPresent bool                 `json:"management_present"`
}

// Uncertainty returns no partial facts when bounded reads cannot agree.
type ApplicationPrerequisiteUncertainty struct{}

func (*ApplicationPrerequisiteUncertainty) Error() string {
	return "application.prerequisites.uncertain"
}

func (r ApplicationRuntime) QueryPrerequisites(ctx context.Context, peerUID uint32, query ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error) {
	return r.queryPrerequisites(ctx, peerUID, query, nil)
}

// The inter-pass callback is an in-package fixture seam, absent on the public
// query path. The runtime's effect/fault hook is deliberately not called.
func (r ApplicationRuntime) queryPrerequisites(ctx context.Context, peerUID uint32, query ApplicationPrerequisiteQuery, between func()) (ApplicationPrerequisiteSnapshot, error) {
	if query.SchemaVersion != ApplicationPrerequisiteSchema || !query.Owner.valid() || !applicationAbsolutePath(r.Store.Root) || !applicationAbsolutePath(r.PolicyPath) {
		return ApplicationPrerequisiteSnapshot{}, applicationError("prerequisites.invalid")
	}
	for attempt := 0; attempt < 3; attempt++ {
		if e := ctx.Err(); e != nil {
			return ApplicationPrerequisiteSnapshot{}, e
		}
		first, vector, e := r.collectPrerequisites(ctx, peerUID, query)
		if e != nil {
			var uncertain *ApplicationPrerequisiteUncertainty
			if errors.As(e, &uncertain) {
				continue
			}
			return ApplicationPrerequisiteSnapshot{}, e
		}
		if between != nil {
			between()
		}
		_, after, e := r.collectPrerequisites(ctx, peerUID, query)
		if e != nil {
			var uncertain *ApplicationPrerequisiteUncertainty
			if errors.As(e, &uncertain) {
				continue
			}
			return ApplicationPrerequisiteSnapshot{}, e
		}
		if vector == after {
			first.CollectedAt = time.Now().UTC()
			return first, nil
		}
	}
	return ApplicationPrerequisiteSnapshot{}, &ApplicationPrerequisiteUncertainty{}
}

type applicationPrerequisiteVector struct {
	Policy       string
	Grant        string
	Publication  string
	Installation string
	Observation  string
	Metadata     map[string]string
}

func (r ApplicationRuntime) collectPrerequisites(ctx context.Context, peer uint32, q ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, string, error) {
	var empty ApplicationPrerequisiteSnapshot
	if e := ctx.Err(); e != nil {
		return empty, "", e
	}
	path, e := filepath.EvalSymlinks(r.PolicyPath)
	if e != nil {
		return empty, "", applicationError("policy.unavailable")
	}
	if applicationImmutableParents(path, r.Store.OwnerUID) != nil {
		return empty, "", applicationError("policy.custody")
	}
	if path != r.PolicyPath && (r.Store.OwnerUID != 0 || !strings.HasPrefix(path, "/nix/store/")) {
		return empty, "", applicationError("policy.custody")
	}
	var policy ApplicationHostPolicy
	policyHash, e := applicationPrerequisiteRead(path, r.Store.OwnerUID, true, &policy)
	if e != nil {
		var uncertain *ApplicationPrerequisiteUncertainty
		if errors.As(e, &uncertain) {
			return empty, "", e
		}
		return empty, "", applicationError("policy.custody")
	}
	if peer != policy.InstallerUID {
		return empty, "", applicationError("peer.denied")
	}
	grant, e := r.resolveGrant(policy, q.Owner)
	if e != nil {
		// Absence is a readable fact, not execution authority. A matching but
		// invalid grant must still fail; never disguise a broken policy as absence.
		missing := e.Error() == "application.policy.scope_denied"
		for _, configured := range policy.Grants {
			if configured.Owner == q.Owner {
				missing = false
			}
		}
		if !missing {
			return empty, "", e
		}
	}
	out := ApplicationPrerequisiteSnapshot{SchemaVersion: ApplicationPrerequisiteSchema, Owner: q.Owner, GrantMissing: e != nil, PolicyRevision: grant.PolicyRevision, LocationRevision: grant.LocationRevision, RepositoryID: grant.RepositoryID, Platform: policy.Platform, Data: map[string]ApplicationPrerequisiteData{}, Credentials: map[string]ApplicationPrerequisiteCredential{}}
	vector := applicationPrerequisiteVector{Policy: policyHash, Grant: applicationSHA(grant), Metadata: map[string]string{}}
	record := func(kind string, value any) (bool, string, error) {
		hash, e := applicationPrerequisiteRead(filepath.Join(r.Store.Root, kind+"-"+q.Owner.Instance()+".json"), r.Store.OwnerUID, false, value)
		if errors.Is(e, os.ErrNotExist) {
			return false, "absent", nil
		}
		if e != nil {
			var uncertain *ApplicationPrerequisiteUncertainty
			if errors.As(e, &uncertain) {
				return false, "", e
			}
			return false, "", applicationError("prerequisites.state_custody")
		}
		return true, hash, nil
	}
	var publication ApplicationPublication
	present, hash, e := record("publication", &publication)
	if e != nil {
		return empty, "", e
	}
	vector.Publication = hash
	if present {
		if publication.Owner != q.Owner || !applicationRevisionPattern.MatchString(publication.Revision) || publication.Descriptor.Validate() != nil || !applicationRevisionPattern.MatchString(publication.PolicyRevision) || !applicationRevisionPattern.MatchString(publication.LocationRevision) {
			return empty, "", applicationError("prerequisites.publication_invalid")
		}
		match := !out.GrantMissing && publication.GrantDigest == applicationSHA(grant) && publication.PolicyRevision == grant.PolicyRevision && publication.LocationRevision == grant.LocationRevision && publication.Descriptor.RepositoryID == grant.RepositoryID && publication.Descriptor.Artifact.Platform == policy.Platform
		out.Publication = ApplicationPrerequisitePublication{ArchiveTarget: publication.ArchiveTarget, Present: true, Revision: publication.Revision, PolicyRevision: publication.PolicyRevision, LocationRevision: publication.LocationRevision, GrantMatches: &match, Artifact: applicationPrerequisiteArtifact(publication.Descriptor)}
	}
	var installation ApplicationInstallation
	present, hash, e = record("installation", &installation)
	if e != nil {
		return empty, "", e
	}
	vector.Installation = hash
	if present {
		if installation.Owner != q.Owner || !applicationRevisionPattern.MatchString(installation.Revision) || (installation.Generation != "" && !applicationRevisionPattern.MatchString(installation.Generation)) {
			return empty, "", applicationError("prerequisites.installation_invalid")
		}
		out.Installation = ApplicationPrerequisiteInstallation{Present: true, Revision: installation.Revision, Generation: installation.Generation, Committed: &installation.Committed, Applied: &installation.Applied, Retired: &installation.Retired, Fenced: &installation.Fenced}
		if installation.Descriptor.SchemaVersion != "" {
			if installation.Descriptor.Validate() != nil {
				return empty, "", applicationError("prerequisites.installation_invalid")
			}
			out.Installation.Artifact = applicationPrerequisiteArtifact(installation.Descriptor)
		}
	}
	for alias, p := range grant.Data {
		if p.BindingRef != "" && !applicationPrerequisiteBindingRef(p.BindingRef) {
			return empty, "", applicationError("prerequisites.binding_ref_invalid")
		}
		if e := ctx.Err(); e != nil {
			return empty, "", e
		}
		data, identity := applicationPrerequisiteDataTrusted(r.Store.OwnerUID, p, installation.Data[alias], r.Store.TrustedParentOwners)
		out.Data[alias] = data
		vector.Metadata["data:"+alias] = identity
	}
	credentials := map[string]string{}
	for ref, p := range grant.Credentials {
		availability, identity := applicationPrerequisiteCredential(r.Store.OwnerUID, p)
		out.Credentials[ref] = ApplicationPrerequisiteCredential{Revision: p.Revision, Availability: availability}
		vector.Metadata["credential:"+ref] = identity
		credentials[ref] = identity
	}
	if grant.Edge != nil {
		if grant.Edge.Owner != q.Owner || !applicationRevisionPattern.MatchString(grant.Edge.Revision) {
			return empty, "", applicationError("prerequisites.edge_invalid")
		}
		ref := applicationSHA(grant.Edge)
		out.ApprovedEdgeRef = &ref
	}
	// Startup observations are optional historical evidence. Never read a journal,
	// probe the process, or imply old-generation evidence describes this install.
	var startup ApplicationStartupObservation
	observed, hash, e := record("startup", &startup)
	if e != nil {
		return empty, "", e
	}
	vector.Observation = hash
	if observed {
		if startup.Owner != q.Owner {
			return empty, "", applicationError("prerequisites.observation_invalid")
		}
		if c := startup.Current; c != nil && out.Installation.Present && c.Process.Generation == installation.Generation && !c.ObservedAt.IsZero() {
			out.Observation = &ApplicationPrerequisiteObservation{
				ObservedAt:   c.ObservedAt,
				State:        applicationPrerequisiteKnownState(c.State, "observed", "failed", "skipped"),
				ProcessState: applicationPrerequisiteKnownState(c.Process.State, "active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing"),
				Readiness: ApplicationReadiness{
					Process:    applicationPrerequisiteKnownState(c.Readiness.Process, "satisfied", "pending", "stopped"),
					Protocol:   applicationPrerequisiteKnownState(c.Readiness.Protocol, "satisfied", "pending", "not_applicable"),
					Ingress:    applicationPrerequisiteKnownState(c.Readiness.Ingress, "configured", "pending", "not_applicable"),
					Client:     applicationPrerequisiteKnownState(c.Readiness.Client, "satisfied", "pending", "not_applicable"),
					Protection: applicationPrerequisiteKnownState(c.Readiness.Protection, "satisfied", "pending", "not_applicable"),
				},
				ManagementPresent: c.ManagementPresent,
			}
		}
	}
	stable := out
	stable.Observation = nil
	// Hash private identities without returning their paths or request/config bytes.
	out.IdentityRevision = applicationSHA(struct {
		Snapshot     ApplicationPrerequisiteSnapshot
		Grant        string
		InstallerUID uint32
		Publication  string
		Installation string
		Credentials  map[string]string
	}{stable, applicationSHA(grant), policy.InstallerUID, applicationSHA(publication), applicationSHA(installation), credentials})
	return out, applicationSHA(vector), nil
}

func applicationPrerequisiteArtifact(d ApplicationArtifactDescriptor) *ApplicationPrerequisiteArtifact {
	return &ApplicationPrerequisiteArtifact{DescriptorDigest: applicationSHA(d), RepositoryID: d.RepositoryID, SourceRevision: d.SourceRevision, SourceDigest: d.SourceDigest, Artifact: d.Artifact, ConfigurationSchema: d.Configuration.Schema, ConfigurationDigest: d.ConfigurationDigest, DataFormat: d.DataFormat}
}

// Open each directory relative to a held descriptor. Neither a final symlink
// nor an ancestor replacement redirects a read outside the checked custody.
func applicationPrerequisiteParent(path string, uid uint32, immutable bool) (*os.File, error) {
	return applicationPrerequisiteParentTrusted(path, uid, immutable, nil)
}

func applicationPrerequisiteParentTrusted(path string, uid uint32, immutable bool, owners map[string]uint32) (*os.File, error) {
	if !applicationAbsolutePath(path) {
		return nil, applicationError("prerequisites.path")
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(path), "/"), "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
		var st unix.Stat_t
		if e = unix.Fstat(fd, &st); e != nil {
			unix.Close(fd)
			return nil, e
		}
		allowedOwner := st.Uid == uid || (uid != 0 && st.Uid == 0)
		if owner, ok := owners[current]; ok && st.Uid == owner {
			allowedOwner = true
		}
		immutableStore := immutable && uid == 0 && strings.HasPrefix(path, "/nix/store/") && current == "/nix/store" && st.Mode&07777 == 01775
		fixtureSticky := uid != 0 && st.Uid == 0 && st.Mode&unix.S_ISVTX != 0
		if !allowedOwner || (st.Mode&0022 != 0 && !immutableStore && !fixtureSticky) {
			unix.Close(fd)
			return nil, applicationError("prerequisites.parent_custody")
		}
	}
	return os.NewFile(uintptr(fd), filepath.Dir(path)), nil
}

func applicationPrerequisiteRead(path string, uid uint32, immutable bool, out any) (string, error) {
	parent, e := applicationPrerequisiteParent(path, uid, immutable)
	if e != nil {
		return "", e
	}
	defer parent.Close()
	fd, e := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return "", e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uid || st.Mode&0022 != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return "", applicationError("prerequisites.file_custody")
	}
	before, e := f.Stat()
	if e != nil {
		return "", e
	}
	raw, e := io.ReadAll(io.LimitReader(f, applicationMaxBytes+1))
	if e != nil {
		return "", e
	}
	after, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return "", &ApplicationPrerequisiteUncertainty{}
	}
	if e = applicationDecodeJSON(raw, out); e != nil {
		return "", e
	}
	return applicationSHA(struct {
		Content  string
		Inode    uint64
		Device   uint64
		Mode     uint32
		Modified int64
	}{applicationSHA(raw), st.Ino, uint64(st.Dev), uint32(st.Mode), after.ModTime().UnixNano()}), nil
}

// Metadata only: fstatat with NOFOLLOW never opens credential contents, even if
// a privileged file happens to be readable. FIFOs/devices/symlinks are unavailable.
func applicationPrerequisiteStat(path string, uid uint32) (unix.Stat_t, error) {
	var st unix.Stat_t
	parent, e := applicationPrerequisiteParent(path, uid, false)
	if e != nil {
		return st, e
	}
	defer parent.Close()
	e = unix.Fstatat(int(parent.Fd()), filepath.Base(path), &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, e
}
func applicationPrerequisiteCredential(uid uint32, p ApplicationCredentialPolicy) (string, string) {
	st, e := applicationPrerequisiteStat(p.Path, uid)
	if errors.Is(e, os.ErrNotExist) {
		return "missing", "missing"
	}
	if e != nil {
		return "unavailable", "unavailable"
	}
	// Exclude access time: an independent credential consumer must not churn
	// prerequisite identity. Modification/change times detect in-place rotations.
	identity := applicationSHA(struct {
		Inode, Device, Nlink uint64
		UID, GID, Mode       uint32
		Size                 int64
		Modified, Changed    unix.Timespec
	}{st.Ino, uint64(st.Dev), uint64(st.Nlink), st.Uid, st.Gid, uint32(st.Mode), st.Size, st.Mtim, st.Ctim})
	if strings.HasPrefix(p.Path, "/nix/store/") || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uid || st.Mode&0077 != 0 || st.Size <= 0 || st.Size > 64*1024 || st.Nlink != 1 {
		return "unavailable", identity
	}
	return "available", identity
}
func applicationPrerequisiteDirectory(path string, uid uint32, owners map[string]uint32) (*os.File, unix.Stat_t, unix.Statfs_t, error) {
	var st unix.Stat_t
	var fs unix.Statfs_t
	parent, e := applicationPrerequisiteParentTrusted(path, uid, false, owners)
	if e != nil {
		return nil, st, fs, e
	}
	defer parent.Close()
	fd, e := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, st, fs, e
	}
	f := os.NewFile(uintptr(fd), path)
	if e = unix.Fstat(fd, &st); e == nil {
		e = unix.Fstatfs(fd, &fs)
	}
	if e != nil {
		f.Close()
		return nil, st, fs, e
	}
	return f, st, fs, nil
}
func applicationPrerequisiteData(uid uint32, p ApplicationDataPolicy, prior ApplicationDataIdentity) (ApplicationPrerequisiteData, string) {
	return applicationPrerequisiteDataTrusted(uid, p, prior, nil)
}

func applicationPrerequisiteDataTrusted(uid uint32, p ApplicationDataPolicy, prior ApplicationDataIdentity, owners map[string]uint32) (ApplicationPrerequisiteData, string) {
	out := ApplicationPrerequisiteData{BindingRef: p.BindingRef, Availability: "unavailable", Custody: "unknown"}
	data, st, fs, e := applicationPrerequisiteDirectory(p.Path, uid, owners)
	if errors.Is(e, os.ErrNotExist) {
		out.Availability = "missing"
		return out, "missing"
	}
	if e != nil {
		return out, "unavailable"
	}
	defer data.Close()
	pool, pst, pfs, e := applicationPrerequisiteDirectory(p.Pool, uid, owners)
	if e != nil {
		return out, "pool_unavailable"
	}
	defer pool.Close()
	vector := applicationSHA(struct {
		Inode, PoolInode, Device, PoolDevice uint64
		UID, GID, Mode, PoolUID, PoolMode    uint32
	}{st.Ino, pst.Ino, uint64(st.Dev), uint64(pst.Dev), st.Uid, st.Gid, uint32(st.Mode), pst.Uid, uint32(pst.Mode)})
	if st.Mode&0022 != 0 || pst.Mode&0022 != 0 || pst.Uid != uid || fs.Type != pfs.Type || fs.Fsid != pfs.Fsid {
		return out, vector
	}
	identity := ApplicationDataIdentity{Path: p.Path, Mount: p.Pool, Filesystem: fmt.Sprintf("%x:%v", fs.Type, fs.Fsid), PoolInode: pst.Ino, Inode: st.Ino, UID: st.Uid, GID: st.Gid}
	out.Availability = "available"
	out.Custody = "unclaimed"
	out.PoolIdentity = applicationSHA(struct {
		Path, Filesystem string
		Inode            uint64
	}{p.Pool, identity.Filesystem, pst.Ino})
	out.Identity = &ApplicationPrerequisiteDataIdentity{Inode: st.Ino, PoolInode: pst.Ino, UID: st.Uid, GID: st.Gid, Mode: uint32(st.Mode) & 07777}
	if prior != (ApplicationDataIdentity{}) {
		out.Custody = "conflict"
		if applicationSameDataIdentity(identity, prior) {
			out.Custody = "matches"
		}
	}
	return out, applicationSHA(out)
}

func decodeApplicationPrerequisiteEnvelope(raw []byte) (ApplicationHelperEnvelope, error) {
	var envelope ApplicationHelperEnvelope
	if applicationDecodeJSON(raw, &envelope) != nil {
		return envelope, applicationError("request.envelope")
	}
	count := 0
	if envelope.Prepared != nil {
		count++
	}
	if envelope.ProvisionPlan != nil {
		count++
	}
	if envelope.Provision != nil {
		count++
	}
	if envelope.Archive != nil {
		count++
	}
	if envelope.Publication != nil {
		count++
	}
	if envelope.Request != nil {
		count++
	}
	if envelope.Prerequisites != nil {
		count++
	}
	if count != 1 {
		return ApplicationHelperEnvelope{}, applicationError("request.envelope")
	}
	return envelope, nil
}

// Existing bindings are logical references; a physical path is never projected
// simply because a root policy accidentally placed it in the reference field.
func applicationPrerequisiteBindingRef(ref string) bool {
	if len(ref) > 128 {
		return false
	}
	for _, c := range ref {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

func applicationPrerequisiteKnownState(value string, known ...string) string {
	for _, state := range known {
		if value == state {
			return value
		}
	}
	return "unknown"
}
