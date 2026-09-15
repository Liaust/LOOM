package cloudstorage

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreCleanupDarwinACLAuthority(t *testing.T) {
	for _, rule := range []string{"", "everyone deny delete", "everyone allow list,search,readattr", "everyone allow add_file", "everyone allow chown"} {
		t.Run(rule, func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() {
				if out, err := exec.Command("/bin/chmod", "-N", root).CombinedOutput(); err != nil {
					t.Errorf("remove fixture ACL: %v %s", err, out)
				}
			})
			if rule != "" {
				if out, err := exec.Command("/bin/chmod", "+a", rule, root).CombinedOutput(); err != nil {
					t.Fatalf("fixture ACL: %v %s", err, out)
				}
			}
			fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			err = cleanupDirectoryACL(fd, false)
			forbidden := rule == "everyone allow add_file" || rule == "everyone allow chown"
			if (err != nil) != forbidden {
				t.Fatalf("ACL rule %q: %v", rule, err)
			}
		})
	}
}

func TestRestoreCleanupDarwinFileACLRefusal(t *testing.T) {
	for _, leaf := range []string{RestoreFailureReceiptFile, RestoreCleanupPreflightFile, restoreAttemptLockFile, "backup/.loom-direct-archive/manifest.json"} {
		for _, barrier := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%t", leaf, barrier), func(t *testing.T) {
				cfg, attempt, root := cleanupFixture(t, false)
				plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, leaf)
				add := func() {
					if out, err := exec.Command("/bin/chmod", "+a", "everyone allow write,delete,writesecurity", path).CombinedOutput(); err != nil {
						t.Fatalf("ACL fixture: %s %v", out, err)
					}
				}
				t.Cleanup(func() {
					if out, err := exec.Command("/bin/chmod", "-N", path).CombinedOutput(); err != nil {
						t.Errorf("remove fixture ACL: %s %v", out, err)
					}
				})
				hooks := cleanupHooks{}
				if barrier {
					done := false
					hooks.beforeMutation = func(point string, _ int) error {
						if point == "unlink" && !done {
							done = true
							if strings.HasPrefix(leaf, "backup/") {
								path = filepath.Join(root, cleanupQuarantineName, leaf)
							}
							add()
						}
						return nil
					}
				} else {
					add()
				}
				if _, err := applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, hooks); err == nil {
					t.Fatal("mutating file ACL accepted")
				}
				retained := filepath.Join(root, "backup/.loom-direct-archive/manifest.json")
				if barrier {
					retained = filepath.Join(root, cleanupQuarantineName, "backup/.loom-direct-archive/manifest.json")
				}
				if _, err := os.Stat(retained); err != nil {
					t.Fatal("payload unlinked before ACL refusal")
				}
			})
		}
	}
}

func TestRestoreCleanupDarwinPayloadContentACL(t *testing.T) {
	for _, rule := range []string{"everyone allow write,append", "everyone allow delete", "everyone allow writesecurity", "everyone allow chown", "everyone allow writeattr", "everyone allow writeextattr"} {
		t.Run(rule, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)
			payload := filepath.Join(root, "backup/a")
			if out, err := exec.Command("/bin/chmod", "+a", rule, payload).CombinedOutput(); err != nil {
				t.Fatalf("fixture ACL %s %v", out, err)
			}
			t.Cleanup(func() {
				if _, err := os.Lstat(payload); err == nil {
					if out, err := exec.Command("/bin/chmod", "-N", payload).CombinedOutput(); err != nil {
						t.Errorf("remove fixture ACL %s %v", out, err)
					}
				}
			})
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if rule != "everyone allow write,append" {
				if err == nil {
					t.Fatal("non-content ACL authority accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{})
			if err != nil || result.Status != "completed" {
				t.Fatalf("content ACL %+v %v", result, err)
			}
		})
	}
}
