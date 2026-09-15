package projectcontracts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const migrationSourcePlain = "kind: loom.project\nschema_version: project.contract.v0.4\nproject:\n  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV\n  slug: migration\n  name: Migration\n  owner_node: main\n  status: active\n"

func TestDeclarationMigrationSourceSnapshot(t *testing.T) {
	for _, name := range []string{"plain", "absent_appears", "same_size_replace", "parent_replace", "symlink", "fifo", "oversized", "dual_conflict", "invalid_claim", "custom_policy", "collection_change"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
				t.Fatal(err)
			}
			raw := migrationSourcePlain
			if name == "custom_policy" {
				raw += "policies:\n  backup: custom.yaml\nfacets:\n  backup_policy: false\n"
				os.WriteFile(filepath.Join(root, "custom.yaml"), []byte("kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup: {enabled: false, roots: []}\n"), 0600)
			}
			if name == "collection_change" {
				raw += "facets: {services: true}\n"
				os.MkdirAll(filepath.Join(root, ".loom/contracts/services"), 0700)
			}
			if name == "invalid_claim" {
				raw = strings.Replace(raw, "loom.project", "/Users/PRIVATE_MARKER", 1)
			}
			file := filepath.Join(root, CanonicalRootContractPath)
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if name == "symlink" {
				os.Remove(file)
				os.Symlink(filepath.Join(t.TempDir(), "private"), file)
			}
			if name == "fifo" {
				os.Remove(file)
				if err := syscall.Mkfifo(file, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "oversized" {
				os.WriteFile(file, []byte(strings.Repeat("x", (4<<20)+1)), 0600)
			}
			if name == "dual_conflict" {
				os.WriteFile(filepath.Join(root, LegacyRootContractPath), []byte(strings.Replace(raw, "Migration", "Different", 1)), 0600)
			}
			s, err := CollectDeclarationMigrationSources(root)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			in := s.Input()
			if len(in.Sources) < 2 {
				t.Fatal("root closure omitted")
			}
			if name == "symlink" || name == "fifo" || name == "oversized" {
				if in.Sources[0].State != "unreadable" {
					t.Fatal("unsafe source admitted")
				}
				return
			}
			if err = s.Check(); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "absent_appears":
				os.WriteFile(filepath.Join(root, LegacyRootContractPath), []byte(raw), 0600)
			case "same_size_replace":
				os.WriteFile(file, []byte(strings.Replace(raw, "Migration", "MigratioN", 1)), 0600)
			case "parent_replace":
				os.Rename(filepath.Join(root, ".loom"), filepath.Join(root, "old"))
				os.Mkdir(filepath.Join(root, ".loom"), 0700)
				os.WriteFile(file, []byte(raw), 0600)
			case "collection_change":
				os.WriteFile(filepath.Join(root, ".loom/contracts/services/new.yaml"), []byte("kind: test\n"), 0600)
			case "custom_policy":
				found := false
				for _, v := range in.Sources {
					found = found || v.Ref == "custom.yaml"
				}
				if !found {
					t.Fatal("false facet lost explicit dependency")
				}
				return
			default:
				return
			}
			if s.Check() == nil {
				t.Fatal("source drift accepted")
			}
		})
	}
}

func TestDeclarationMigrationSourceBoundsAndOwnership(t *testing.T) {
	for _, count := range []int{254, 256, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root := t.TempDir()
			if e := os.MkdirAll(filepath.Join(root, ".loom/contracts/services"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(root, CanonicalRootContractPath), []byte(migrationSourcePlain+"facets: {services: true}\n"), 0600); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < count; i++ {
				if e := os.WriteFile(filepath.Join(root, ".loom/contracts/services", fmt.Sprintf("%03d.yaml", i)), []byte("kind: unsupported.fixture\nschema_version: service.fixture.v1\n"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			s, e := CollectDeclarationMigrationSources(root)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if len(s.Input().Sources) > 256 {
				t.Fatal("source cap exceeded")
			}
			if count >= 256 && len(s.Issues()) == 0 {
				t.Fatal("overflow asserted complete closure")
			}
			fingerprint := s.Fingerprint()
			in := s.Input()
			in.Sources[0].Raw[0] = 'X'
			if s.Fingerprint() != fingerprint || s.Check() != nil {
				t.Fatal("caller mutated private source snapshot")
			}
		})
	}
}

func TestDeclarationMigrationRootDescriptor(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if e := syscall.Mkfifo(fifo, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		s, e := CollectDeclarationMigrationSources(fifo)
		if s != nil {
			s.Close()
		}
		done <- e
	}()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO root accepted")
		}
	case <-time.After(time.Second):
		fd, e := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
		if e == nil {
			syscall.Close(fd)
		}
		t.Fatal("source root open blocked")
	}
}
