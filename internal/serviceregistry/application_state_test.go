package serviceregistry

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationTrustedParentOwnersAreExact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("uses distinct unprivileged fixture ownership")
	}
	root := applicationFixtureDir(t)
	path := filepath.Join(root, "data")
	owners := map[string]uint32{}
	for p := root; p != "/"; p = filepath.Dir(p) {
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			t.Fatal(err)
		}
		if st.Uid != 0 {
			owners[p] = st.Uid
		}
	}
	uid := uint32(os.Geteuid()) + 1
	if applicationCheckParents(path, uid) == nil {
		t.Fatal("unconfigured operator parent was trusted")
	}
	if err := applicationCheckTrustedParents(path, uid, owners); err != nil {
		t.Fatal(err)
	}
	owners[root]++
	if applicationCheckTrustedParents(path, uid, owners) == nil {
		t.Fatal("wrong operator identity accepted")
	}
	owners[root]--
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if applicationCheckTrustedParents(path, uid, owners) == nil {
		t.Fatal("writable operator parent accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "unlisted")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if applicationCheckTrustedParents(filepath.Join(child, "data"), uid, owners) == nil {
		t.Fatal("operator trust extended to an unlisted child")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	owners[link] = uint32(os.Geteuid())
	if applicationCheckTrustedParents(filepath.Join(link, "data"), uid, owners) == nil {
		t.Fatal("trusted parent symlink accepted")
	}
}

func TestApplicationStateRefusesSymlinkAndCorruption(t *testing.T) {
	r, _, q, _ := applicationTestRuntime(t)
	path := filepath.Join(r.Store.Root, "installation-"+q.Owner.Instance()+".json")
	target := filepath.Join(applicationFixtureDir(t), "target")
	if e := os.WriteFile(target, []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Execute(context.Background(), 1234, q); e == nil {
		t.Fatal("followed state symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "{}" {
		t.Fatal("modified unrelated target")
	}
}

func TestApplicationDirectoryPublicationRecovery(t *testing.T) {
	stages := []string{"directory:unclaimed_mkdir", "directory:mkdir", "directory:chown", "directory:before_chmod", "directory:chmod", "directory:before_fsync", "directory:fsync", "directory:published", "directory:parent_fsync"}
	for _, kind := range []string{"data", "config"} {
		for _, stage := range stages {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				root := applicationFixtureDir(t)
				s := ApplicationStateStore{Root: filepath.Join(root, "state"), OwnerUID: uint32(os.Geteuid())}
				path := filepath.Join(root, "target")
				mode := os.FileMode(0700)
				if kind == "config" {
					mode = 0710
				}
				hook := func(at string) error {
					if at == stage {
						return errors.New("fixture interruption")
					}
					return nil
				}
				if err := s.ensureClaimedDirectory(context.Background(), path, kind, uint32(os.Geteuid()), uint32(os.Getegid()), mode, false, hook); err == nil {
					t.Fatal("fault did not execute")
				}
				var claim applicationDirectoryClaim
				claimed := s.read("directory-"+applicationSHA(path)[7:], &claim) == nil
				if stage != "directory:unclaimed_mkdir" && !claimed {
					t.Fatal("claim missing after claimed boundary")
				}
				if err := s.ensureClaimedDirectory(context.Background(), path, kind, uint32(os.Geteuid()), uint32(os.Getegid()), mode, false, nil); err != nil {
					t.Fatal(err)
				}
				var st unix.Stat_t
				if unix.Lstat(path, &st) != nil || uint32(st.Mode)&0777 != uint32(mode.Perm()) {
					t.Fatal("published mode missing")
				}
				if claimed && claim.Inode != st.Ino {
					t.Fatal("retry abandoned a claimed inode")
				}
				if err := s.ensureClaimedDirectory(context.Background(), path, kind, uint32(os.Geteuid()), uint32(os.Getegid()), mode, false, nil); err != nil {
					t.Fatal(err)
				}
				if err := s.ensureClaimedDirectory(context.Background(), path, kind+"-foreign", uint32(os.Geteuid()), uint32(os.Getegid()), mode, false, nil); err == nil {
					t.Fatal("foreign purpose adopted claim")
				}
			})
		}
	}
}
func TestApplicationDirectoryRefusesUnclaimedAndReplacedPaths(t *testing.T) {
	for _, kind := range []string{"unclaimed", "stage_replaced", "destination_race", "parent_replaced", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := applicationFixtureDir(t)
			pool := filepath.Join(root, "pool")
			if err := os.Mkdir(pool, 0700); err != nil {
				t.Fatal(err)
			}
			s := ApplicationStateStore{Root: filepath.Join(root, "state"), OwnerUID: uint32(os.Geteuid())}
			path := filepath.Join(pool, "target")
			if kind == "unclaimed" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				if err := os.Symlink(root, path); err != nil {
					t.Fatal(err)
				}
			}
			hook := func(at string) error {
				if at != "directory:fsync" {
					return nil
				}
				var claim applicationDirectoryClaim
				if err := s.read("directory-"+applicationSHA(path)[7:], &claim); err != nil {
					return err
				}
				switch kind {
				case "stage_replaced":
					if err := os.Rename(claim.Stage, claim.Stage+"-retained"); err != nil {
						return err
					}
					return os.Mkdir(claim.Stage, 0700)
				case "destination_race":
					return os.Mkdir(path, 0700)
				case "parent_replaced":
					if err := os.Rename(pool, pool+"-retained"); err != nil {
						return err
					}
					return os.Mkdir(pool, 0700)
				}
				return nil
			}
			if err := s.ensureClaimedDirectory(context.Background(), path, "fixture", uint32(os.Geteuid()), uint32(os.Getegid()), 0700, false, hook); err == nil {
				t.Fatal("foreign/replaced path accepted")
			}
		})
	}
}

func TestApplicationVolatileDirectoryClaimsUseParentEpoch(t *testing.T) {
	root := applicationFixtureDir(t)
	s := ApplicationStateStore{Root: filepath.Join(root, "state"), OwnerUID: uint32(os.Geteuid())}
	parent := filepath.Join(root, "run")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "owned.service.d")
	ensure := func(epoch string) error {
		return s.ensureClaimedDirectoryEpoch(context.Background(), path, "volatile-fixture", uint32(os.Geteuid()), uint32(os.Getegid()), 0755, false, epoch, nil)
	}
	if err := ensure("boot-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-old-boot"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensure("boot-a"); err == nil {
		t.Fatal("old parent epoch adopted replacement")
	}
	if err := ensure("boot-b"); err != nil {
		t.Fatal(err)
	}
	if err := ensure("boot-b"); err != nil {
		t.Fatal(err)
	}
	if err := ensure("boot-c"); err == nil {
		t.Fatal("new epoch adopted pre-existing unclaimed directory")
	}
}
