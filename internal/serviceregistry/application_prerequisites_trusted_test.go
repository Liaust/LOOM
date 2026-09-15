package serviceregistry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestApplicationPrerequisiteTrustedDataParents(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged fixture owner")
	}
	for _, name := range []string{"missing", "wrong_owner", "no_trust", "unlisted_child", "symlink", "group_writable", "world_writable", "pool_owner_stays_strict"} {
		t.Run(name, func(t *testing.T) {
			root := applicationFixtureDir(t)
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
			policy := ApplicationDataPolicy{Path: filepath.Join(root, "missing-data"), Pool: root, BindingRef: "fixture.files"}
			want := "unavailable"
			switch name {
			case "missing":
				want = "missing"
				if err := applicationCheckTrustedParents(policy.Path, uid, owners); err != nil {
					t.Fatal(err)
				}
			case "wrong_owner":
				owners[root]++
			case "no_trust":
				owners = nil
			case "unlisted_child", "symlink":
				child := filepath.Join(root, "unlisted")
				if err := os.Mkdir(child, 0700); err != nil {
					t.Fatal(err)
				}
				if name == "symlink" {
					link := filepath.Join(root, "link")
					if err := os.Symlink(child, link); err != nil {
						t.Fatal(err)
					}
					owners[link] = uint32(os.Geteuid())
					child = link
				}
				policy.Path = filepath.Join(child, "missing-data")
			case "group_writable":
				if err := os.Chmod(root, 0770); err != nil {
					t.Fatal(err)
				}
			case "world_writable":
				if err := os.Chmod(root, 0702); err != nil {
					t.Fatal(err)
				}
			case "pool_owner_stays_strict":
				if err := os.Mkdir(policy.Path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := applicationPrerequisitesTree(t, root)
			got, _ := applicationPrerequisiteDataTrusted(uid, policy, ApplicationDataIdentity{}, owners)
			if got.Availability != want {
				t.Errorf("availability = %q, want %q", got.Availability, want)
			}
			if got.Custody != "unknown" || got.Identity != nil {
				t.Errorf("unexpected custody claim: %+v", got)
			}
			if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, root)) {
				t.Fatal("read mutated fixture")
			}
		})
	}
}
