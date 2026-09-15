package serviceregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type ApplicationDataRequest struct {
	Backup       string `json:"backup,omitempty"`
	BindingRef   string `json:"binding_ref,omitempty"`
	PlannedBytes uint64 `json:"planned_bytes"`
	QuotaBytes   uint64 `json:"quota_bytes"`
}
type ApplicationDataIdentity struct {
	// MountID and Device are observations, not durable identity across namespaces
	// or reboot. Filesystem plus the configured pool/directory inode owns custody.
	Filesystem string `json:"filesystem"`
	PoolInode  uint64 `json:"pool_inode"`
	MountID    uint64 `json:"mount_id"`
	Path       string `json:"path"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	Mount      string `json:"mount"`
}
type ApplicationCapacityObservation struct {
	Pool           string    `json:"pool"`
	AvailableBytes uint64    `json:"available_bytes"`
	ObservedAt     time.Time `json:"observed_at"`
	State          string    `json:"state"`
	Reserved       bool      `json:"reserved"`
}

func applicationDataIdentity(path, pool string, uid uint32) (ApplicationDataIdentity, error) {
	return applicationDataIdentityOwner(path, pool, uid, uid)
}
func applicationDataIdentityOwner(path, pool string, uid, gid uint32) (ApplicationDataIdentity, error) {
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uid || st.Gid != gid || st.Mode&0022 != 0 {
		return ApplicationDataIdentity{}, applicationError("data.custody_migration_required")
	}
	var parent unix.Stat_t
	var filesystem, poolFilesystem unix.Statfs_t
	if unix.Lstat(pool, &parent) != nil || parent.Mode&unix.S_IFMT != unix.S_IFDIR || parent.Mode&0022 != 0 || unix.Statfs(path, &filesystem) != nil || unix.Statfs(pool, &poolFilesystem) != nil || filesystem.Fsid != poolFilesystem.Fsid || filesystem.Type != poolFilesystem.Type {
		return ApplicationDataIdentity{}, applicationError("data.pool_filesystem_mismatch")
	}
	mount, _ := applicationMountID(path) // A namespace-local diagnostic only.
	return ApplicationDataIdentity{Filesystem: fmt.Sprintf("%x:%v", filesystem.Type, filesystem.Fsid), PoolInode: parent.Ino, MountID: mount, Path: path, Device: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, GID: st.Gid, Mount: pool}, nil
}
func applicationSameDataIdentity(a, b ApplicationDataIdentity) bool {
	return a.Filesystem != "" && a.PoolInode != 0 && a.Path == b.Path && a.Mount == b.Mount && a.Filesystem == b.Filesystem && a.PoolInode == b.PoolInode && a.Inode == b.Inode && a.UID == b.UID && a.GID == b.GID
}

// Only immutable Nix inputs use this validator. Credential, state, socket and
// data paths retain the stricter general ancestor rules below.
func applicationImmutableParents(path string, uid uint32) error {
	if uid != 0 || !strings.HasPrefix(path, "/nix/store/") {
		return applicationCheckParents(path, uid)
	}
	return applicationImmutableParentsWithStat(path, unix.Lstat)
}
func applicationImmutableParentsWithStat(path string, stat func(string, *unix.Stat_t) error) error {
	parts := strings.Split(strings.TrimPrefix(path, "/nix/store/"), "/")
	if !applicationAbsolutePath(path) || len(parts) < 1 || !applicationStorePattern.MatchString("/nix/store/"+parts[0]) {
		return applicationError("immutable.path")
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		var st unix.Stat_t
		if stat(p, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return applicationError("immutable.parent_custody")
		}
		writable := st.Mode&0022 != 0
		nixStore := p == "/nix/store" && st.Mode&07777 == 01775
		if writable && !nixStore {
			return applicationError("immutable.parent_custody")
		}
		if p == "/" {
			break
		}
	}
	return nil
}

func applicationCheckParents(path string, uid uint32) error {
	return applicationCheckTrustedParents(path, uid, nil)
}

func applicationCheckTrustedParents(path string, uid uint32, owners map[string]uint32) error {
	for p := filepath.Dir(path); p != "/"; p = filepath.Dir(p) {
		if err := applicationCustody(p, uid, true); err != nil {
			if owner, ok := owners[p]; ok && applicationCustody(p, owner, true) == nil {
				continue
			}
			// Non-root in-process fixtures may traverse root-owned ancestors. The
			// production helper always passes UID0 and retains strict root custody.
			if uid != 0 && applicationCustody(p, 0, true) == nil {
				continue
			}
			var st unix.Stat_t
			if uid != 0 && unix.Lstat(p, &st) == nil && st.Uid == 0 && st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&unix.S_ISVTX != 0 {
				continue
			}
			return applicationError("path.parent_custody")
		}
	}
	return nil
}

// Pool observations are a bounded statfs, not recursive scans or reservations.
func ObserveApplicationCapacity(pool string) ApplicationCapacityObservation {
	out := ApplicationCapacityObservation{Pool: pool, ObservedAt: time.Now().UTC(), State: "unknown"}
	var s unix.Statfs_t
	if !applicationAbsolutePath(pool) || unix.Statfs(pool, &s) != nil || s.Bsize <= 0 {
		return out
	}
	if uint64(s.Bavail) > math.MaxUint64/uint64(s.Bsize) {
		return out
	}
	out.AvailableBytes = uint64(s.Bavail) * uint64(s.Bsize)
	out.State = "observed"
	return out
}
func (r ApplicationRuntime) admitData(ctx context.Context, grant ApplicationGrant, requested map[string]ApplicationDataRequest) ([]ApplicationCapacityObservation, func(), error) {
	if len(requested) != len(grant.Data) {
		return nil, nil, applicationError("data.bindings_mismatch")
	}
	demand := map[string]uint64{}
	var total uint64
	for key, v := range requested {
		p, ok := grant.Data[key]
		if !ok || v.BindingRef != p.BindingRef {
			return nil, nil, applicationError("data.binding_unsupported")
		}
		if v.QuotaBytes != 0 {
			return nil, nil, applicationError("data.quota_unsupported")
		}
		if grant.MaxPlannedBytes != 0 {
			if v.PlannedBytes > grant.MaxPlannedBytes-total {
				return nil, nil, applicationError("data.policy_capacity_exceeded")
			}
			total += v.PlannedBytes
		}
		if math.MaxUint64-demand[p.Pool] < v.PlannedBytes {
			return nil, nil, applicationError("data.capacity_overflow")
		}
		demand[p.Pool] += v.PlannedBytes
	}
	pools := make([]string, 0, len(demand))
	for p := range demand {
		pools = append(pools, p)
	}
	sort.Strings(pools)
	releases := []func(){}
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	observations := []ApplicationCapacityObservation{}
	for _, p := range pools {
		unlock, err := r.Store.lock(ctx, "pool:"+p)
		if err != nil {
			release()
			return nil, nil, err
		}
		releases = append(releases, unlock)
		o := ObserveApplicationCapacity(p)
		observations = append(observations, o)
		if o.State != "observed" || o.AvailableBytes < demand[p] {
			release()
			return nil, nil, applicationError("data.capacity_unavailable")
		}
	}
	return observations, release, nil
}
func applicationConfiguration(m ApplicationManifest, d ApplicationArtifactDescriptor, grant ApplicationGrant) ([]byte, error) {
	data, credentials, code := applicationConfigurationBindings(m.Config, d.Configuration)
	if code != "" {
		return nil, applicationError(code)
	}
	if len(data) != len(grant.Data) || len(m.Data) != len(grant.Data) || len(credentials) != len(grant.Credentials) {
		return nil, applicationError("configuration.bindings_mismatch")
	}
	values := map[string]any{}
	for key, v := range m.Config.Values {
		switch v.Type {
		case ApplicationString:
			values[key] = *v.String
		case ApplicationInteger:
			values[key] = *v.Integer
		case ApplicationBoolean:
			values[key] = *v.Boolean
		case ApplicationDataReference:
			p, ok := grant.Data[string(*v.DataRef)]
			if !ok {
				return nil, applicationError("configuration.data_missing")
			}
			if _, ok = m.Data[*v.DataRef]; !ok {
				return nil, applicationError("configuration.data_missing")
			}
			values[key] = map[string]string{"data_path": p.Path}
		case ApplicationCredentialReference:
			if _, ok := grant.Credentials[*v.CredentialRef]; !ok {
				return nil, applicationError("configuration.credential_missing")
			}
			values[key] = map[string]string{"credential_file": "/run/credentials/" + grant.Owner.Unit() + "/" + applicationCredentialName(*v.CredentialRef)}
		default:
			return nil, applicationError("configuration.type")
		}
	}
	raw, err := json.Marshal(struct {
		Schema string         `json:"schema"`
		Values map[string]any `json:"values"`
	}{m.Config.Schema, values})
	if err != nil || len(raw) > 64*1024 {
		return nil, applicationError("configuration.size")
	}
	return raw, nil
}
func applicationCredentialName(ref string) string { return "cred-" + applicationSHA(ref)[7:31] }
func applicationValidateCredentials(grant ApplicationGrant, uid uint32) error {
	for _, p := range grant.Credentials {
		var st unix.Stat_t
		if unix.Lstat(p.Path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
			return applicationError("credential.unavailable")
		}
		if strings.HasPrefix(p.Path, "/nix/store/") {
			return applicationError("credential.store_forbidden")
		}
		if err := applicationCheckParents(p.Path, uid); err != nil {
			return applicationError("credential.custody")
		}
		if err := applicationCustody(p.Path, uid, false); err != nil {
			return applicationError("credential.unavailable")
		}
		info, err := os.Lstat(p.Path)
		if err != nil || info.Mode().Perm()&0077 != 0 || info.Size() == 0 || info.Size() > 64*1024 {
			return applicationError("credential.custody")
		}
	}
	return nil
}
func applicationDropIn(grant ApplicationGrant, m ApplicationManifest, descriptor ApplicationArtifactDescriptor, configPath string, uid uint32) string {
	// All paths originate in validated startup policy/published descriptors.
	var b strings.Builder
	fmt.Fprintf(&b, "[Service]\nUser=%d\nGroup=%d\nExecStart=\nExecStart=%s %s\nMemoryMax=%d\nRestart=%s\n", uid, uid, descriptor.Launcher, configPath, m.Process.MemoryMaxBytes, m.Process.Restart)
	fmt.Fprintf(&b, "Environment=LOOM_APPLICATION_GENERATION=%s\n", filepath.Base(filepath.Dir(configPath)))
	keys := make([]string, 0, len(grant.Data))
	for k := range grant.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "ReadWritePaths=%s\n", grant.Data[k].Path)
	}
	keys = keys[:0]
	for k := range grant.Credentials {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "LoadCredential=%s:%s\n", applicationCredentialName(k), grant.Credentials[k].Path)
	}
	return b.String()
}

// Identity and policy revision only. Credential contents are never read here.
type ApplicationCredentialIdentity struct {
	Revision string `json:"revision"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	UID      uint32 `json:"uid"`
	Mode     uint32 `json:"mode"`
	Size     int64  `json:"size"`
}

func applicationCredentialIdentities(grant ApplicationGrant, uid uint32) (map[string]ApplicationCredentialIdentity, error) {
	if e := applicationValidateCredentials(grant, uid); e != nil {
		return nil, e
	}
	out := map[string]ApplicationCredentialIdentity{}
	for ref, p := range grant.Credentials {
		var st unix.Stat_t
		if unix.Lstat(p.Path, &st) != nil {
			return nil, applicationError("credential.unavailable")
		}
		out[ref] = ApplicationCredentialIdentity{Revision: p.Revision, Device: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, Mode: uint32(st.Mode), Size: st.Size}
	}
	return out, nil
}
