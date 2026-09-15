package serviceregistry

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	pc "loom.local/loom/internal/projectcontracts"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationBindingsMultipleTypedReferencesAndCapacity(t *testing.T) {
	r, _, q, p := applicationTestRuntime(t)
	policy, e := r.policy()
	if e != nil {
		t.Fatal(e)
	}
	g := &policy.Grants[0]
	pool := applicationFixtureDir(t)
	q.Data = map[string]ApplicationDataRequest{}
	for _, key := range []string{"files", "index"} {
		g.Data[key] = ApplicationDataPolicy{Path: filepath.Join(pool, key), Pool: pool}
		q.Data[key] = ApplicationDataRequest{PlannedBytes: 1}
		q.Manifest.Data[pc.ResourceKey(key)] = ApplicationDataRequirements{}
		ref := pc.ResourceKey(key)
		q.Manifest.Config.Values[key] = ApplicationParameter{Type: ApplicationDataReference, DataRef: &ref}
		p.Descriptor.Configuration.Parameters[key] = ApplicationParameterSpec{Type: ApplicationDataReference, Required: true}
	}
	for _, ref := range []string{"fixture.first", "fixture.second"} {
		path := filepath.Join(applicationFixtureDir(t), "secret")
		os.WriteFile(path, []byte("fixture-secret-never-render"), 0600)
		g.Credentials[ref] = ApplicationCredentialPolicy{Path: path, Revision: "credential-a"}
		q.CredentialRevisions[ref] = "credential-a"
		key := strings.ReplaceAll(ref, ".", "_")
		refCopy := ref
		q.Manifest.Config.Values[key] = ApplicationParameter{Type: ApplicationCredentialReference, CredentialRef: &refCopy}
		p.Descriptor.Configuration.Parameters[key] = ApplicationParameterSpec{Type: ApplicationCredentialReference, Required: true}
	}
	p.Descriptor.ConfigurationDigest = applicationSHA(p.Descriptor.Configuration)
	config, e := applicationConfiguration(*q.Manifest, p.Descriptor, *g)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(config), "fixture-secret-never-render") || !strings.Contains(string(config), "credential_file") || !strings.Contains(string(config), "9007199254740993") {
		t.Fatalf("invalid config: %s", config)
	}
	// This unit test checks typed rendering, not Linux credential delivery.
	if _, unlock, e := r.admitData(context.Background(), *g, q.Data); e != nil {
		t.Fatal(e)
	} else {
		unlock()
	}
	q.Data["files"] = ApplicationDataRequest{PlannedBytes: math.MaxUint64}
	q.Data["index"] = ApplicationDataRequest{PlannedBytes: 1}
	if _, _, e := r.admitData(context.Background(), *g, q.Data); e == nil {
		t.Fatal("capacity overflow admitted")
	}
	q.Data["files"] = ApplicationDataRequest{QuotaBytes: 1}
	if _, _, e := r.admitData(context.Background(), *g, q.Data); e == nil {
		t.Fatal("unenforced quota admitted")
	}
	raw, _ := json.Marshal(policy)
	_ = raw
}

func TestApplicationImmutableNixCustody(t *testing.T) {
	root := "/nix/store/00000000000000000000000000000000-fixture"
	path := root + "/bin/tool"
	for _, tc := range []struct {
		name, bad string
		mode      uint32
		uid       uint32
		want      bool
	}{
		{"normal_sticky_store", "", 0, 0, true},
		{"nonsticky_store", "/nix/store", 0775, 0, false},
		{"world_writable_store", "/nix/store", 01777, 0, false},
		{"foreign_store", "/nix/store", 01775, 42, false},
		{"substituted_store", "/nix/store", unix.S_IFLNK | 0777, 0, false},
		{"writable_nix_parent", "/nix", 0775, 0, false},
		{"writable_store_item", root, 0775, 0, false},
		{"foreign_descendant", root + "/bin", 0755, 42, false},
		{"substituted_descendant", root + "/bin", unix.S_IFLNK | 0777, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := applicationImmutableParentsWithStat(path, func(p string, st *unix.Stat_t) error {
				mode := uint32(unix.S_IFDIR | 0755)
				uid := uint32(0)
				if p == "/nix/store" {
					mode = unix.S_IFDIR | 01775
				}
				if p == tc.bad {
					mode = tc.mode
					if mode&unix.S_IFMT == 0 {
						mode |= unix.S_IFDIR
					}
					uid = tc.uid
				}
				// Stat_t.Mode differs in width between Darwin and Linux.
				st.Uid = uid
				if mode&unix.S_IFMT == unix.S_IFLNK {
					st.Mode = unix.S_IFLNK | 0777
				} else {
					st.Mode = unix.S_IFDIR | 0755
					if mode&02000 != 0 {
						st.Mode |= 02000
					}
					if mode&01000 != 0 {
						st.Mode |= 01000
					}
					if mode&0020 != 0 {
						st.Mode |= 0020
					}
					if mode&0002 != 0 {
						st.Mode |= 0002
					}
				}
				return nil
			})
			if (err == nil) != tc.want {
				t.Fatalf("custody accepted=%v want=%v err=%v", err == nil, tc.want, err)
			}
		})
	}
	// writeText configurations may themselves be direct regular store entries,
	// rather than descendants of a directory output. Leaf custody is checked by
	// the caller after this parent-chain check.
	if err := applicationImmutableParentsWithStat(root, func(p string, st *unix.Stat_t) error {
		st.Uid = 0
		st.Mode = unix.S_IFDIR | 0755
		if p == "/nix/store" {
			st.Mode = unix.S_IFDIR | 01775
		}
		return nil
	}); err != nil {
		t.Fatalf("direct immutable store file rejected: %v", err)
	}
	// The Nix exception cannot be applied to a credential or state path.
	if applicationImmutableParentsWithStat("/var/lib/credential", unix.Lstat) == nil {
		t.Fatal("non-store path admitted")
	}
}

func TestApplicationDataStableAcrossMountNamespaces(t *testing.T) {
	original := ApplicationDataIdentity{Filesystem: "ext4:fixture-fsid", PoolInode: 17, Path: "/srv/pool/app", Mount: "/srv/pool", Inode: 91, UID: 400001, GID: 400001, MountID: 200, Device: 81}
	other := original
	other.MountID = 803
	other.Device = 92
	if !applicationSameDataIdentity(original, other) {
		t.Fatal("namespace/reboot observations became identity")
	}
	cases := map[string]func(*ApplicationDataIdentity){
		"directory":  func(v *ApplicationDataIdentity) { v.Inode++ },
		"pool_inode": func(v *ApplicationDataIdentity) { v.PoolInode++ },
		"pool_path":  func(v *ApplicationDataIdentity) { v.Mount += "other" },
		"path":       func(v *ApplicationDataIdentity) { v.Path += "other" },
		"filesystem": func(v *ApplicationDataIdentity) { v.Filesystem = "other-fsid" },
		"uid":        func(v *ApplicationDataIdentity) { v.UID++ },
		"gid":        func(v *ApplicationDataIdentity) { v.GID++ },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := original
			change(&v)
			if applicationSameDataIdentity(original, v) {
				t.Fatal("replacement admitted")
			}
		})
	}
	// Also compare real small directories, including an actual replacement kept
	// alive under a different name so inode reuse cannot hide the replacement.
	pool := applicationFixtureDir(t)
	path := filepath.Join(pool, "data")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := applicationDataIdentityOwner(path, pool, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := applicationDataIdentityOwner(path, pool, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil || !applicationSameDataIdentity(a, b) {
		t.Fatalf("real unchanged directory: %v", err)
	}
	if err = os.Rename(path, path+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	b, err = applicationDataIdentityOwner(path, pool, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil || applicationSameDataIdentity(a, b) {
		t.Fatalf("real replacement accepted: %v", err)
	}
}
