package backupstrategy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Backup producers, cloud consumers, update apply and Notes capture hold a
// shared lease. Destructive operator retention holds the exclusive lease.
func LockOperationalPackages(root string, exclusive bool) (*os.File, error) {
	if _, err := exactAbsolutePath(root); err != nil {
		return nil, err
	}
	if err := requireRealDirectory(root); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path := root + ".retention.lock"
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		fd, err = unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0640)
		if os.IsExist(err) {
			fd, err = unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("invalid operational retention lease")
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err = unix.Flock(fd, mode|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("operational packages are in use: %w", err)
	}
	return f, nil
}

type LocalRetentionPlan struct {
	Schema         string                   `json:"schema"`
	PlanHash       string                   `json:"plan_hash"`
	Selection      OperationalRetentionPlan `json:"selection"`
	Protected      map[string]string        `json:"protected"`
	CandidateBytes int64                    `json:"candidate_allocated_bytes"`
	RetainedBytes  int64                    `json:"retained_allocated_bytes"`
	Inventory      map[string]string        `json:"inventory"`
	BoundMet       bool                     `json:"target_bound_after_apply"`
}

// Caller supplies live update/checkpoint pins under its maintenance lease.
// Unknown entries and unverifiable packages are retained, never guessed away.
func PlanLocalOperationalRetention(ctx context.Context, root string, protected map[string]string) (LocalRetentionPlan, error) {
	selection, err := PlanOperationalPackageRetention(ctx, root, nil)
	if err != nil {
		return LocalRetentionPlan{}, err
	}
	p := LocalRetentionPlan{Schema: "loom.operational_retention.v1", Selection: selection,
		Protected: map[string]string{}, Inventory: map[string]string{}}
	for id, reason := range protected {
		if !validPackageID(id) || strings.TrimSpace(reason) == "" {
			return p, fmt.Errorf("invalid protected operational package")
		}
		p.Protected[id] = reason
	}
	remove := []OperationalRetentionEntry{}
	for _, e := range p.Selection.Remove {
		if reason := p.Protected[e.PackageID]; reason != "" {
			e.Reason = "protected: " + reason
			p.Selection.Keep = append(p.Selection.Keep, e)
		} else {
			remove = append(remove, e)
		}
	}
	p.Selection.Remove = remove
	seen := map[string]bool{}
	for _, group := range [][]OperationalRetentionEntry{p.Selection.Keep, p.Selection.Remove} {
		for _, e := range group {
			seen[e.PackageID] = true
			identity, bytes, err := retentionIdentity(e.Path)
			if err != nil {
				return p, err
			}
			p.Inventory[e.PackageID] = identity
			if e.Reason == "older_than_latest_verified" {
				p.CandidateBytes += bytes
			} else {
				p.RetainedBytes += bytes
			}
		}
	}
	protectedIDs := make([]string, 0, len(p.Protected))
	for id := range p.Protected {
		protectedIDs = append(protectedIDs, id)
	}
	sort.Strings(protectedIDs)
	for _, id := range protectedIDs {
		if !seen[id] {
			path := filepath.Join(root, id)
			if err := requireRealDirectory(path); err != nil {
				return p, fmt.Errorf("protected package %s is missing or inaccessible", id)
			}
			identity, bytes, err := retentionIdentity(path)
			if err != nil {
				return p, fmt.Errorf("protected package %s cannot be inventoried: %w", id, err)
			}
			p.Inventory[id] = identity
			p.RetainedBytes += bytes
			p.Selection.Findings = append(p.Selection.Findings, operationalFinding("protected_package_unverified", id, "protected payload remains held; an explicit deep verification receipt is required before it can participate in verified retention"))
		}
	}
	p.BoundMet = len(p.Selection.Keep) <= OperationalRetentionCount && len(p.Selection.Findings) == 0
	raw, _ := json.Marshal(p)
	digest := sha256.Sum256(raw)
	p.PlanHash = "sha256:" + hex.EncodeToString(digest[:])
	return p, nil
}

func validPackageID(id string) bool {
	return strings.HasPrefix(id, "operational-") && validatePackageID(id) == nil
}

// Include inode/link/mode/mtime/ctime identity in addition to authenticated contents.
// Hard-linked allocations are not promised as reclaimable bytes.
func retentionIdentity(path string) (string, int64, error) {
	hash := sha256.New()
	var bytes int64
	err := filepath.Walk(path, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular retention entry")
		}
		s := info.Sys().(*syscall.Stat_t)
		rel, err := filepath.Rel(path, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%q:%d:%d:%d:%d:%d:%d:%d\n", rel, s.Dev, s.Ino, s.Nlink, info.Mode(), info.Size(), info.ModTime().UnixNano(), statChangeUnixNano(info.Sys()))
		if s.Nlink == 1 && info.Mode().IsRegular() {
			bytes += s.Blocks * 512
		}
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)), bytes, err
}

type LocalRetentionResult struct {
	Status      string             `json:"status"`
	Plan        LocalRetentionPlan `json:"plan"`
	Removed     []string           `json:"removed"`
	ReceiptPath string             `json:"receipt_path"`
}

// Re-resolve references under an exclusive lease before validating the reviewed
// digest. No recursive removal uses a caller-provided package pathname.
func ApplyLocalOperationalRetention(ctx context.Context, root, expected string, yes bool, resolveProtected func() (map[string]string, error)) (LocalRetentionResult, error) {
	result := LocalRetentionResult{Status: "refused", Removed: []string{}}
	if !yes || expected == "" || resolveProtected == nil {
		return result, fmt.Errorf("retention requires --yes and the exact current --plan-hash")
	}
	lease, err := LockOperationalPackages(root, true)
	if err != nil {
		return result, err
	}
	defer lease.Close()
	protected, err := resolveProtected()
	if err != nil {
		return result, err
	}
	live, err := PlanLocalOperationalRetention(ctx, root, protected)
	result.Plan = live
	if err != nil {
		return result, err
	}
	if live.PlanHash != expected {
		return result, fmt.Errorf("retention inventory or protected references changed; review a fresh plan")
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return result, err
	}
	defer handle.Close()
	receipts := root + ".retention-receipts"
	if err = os.MkdirAll(receipts, 0700); err != nil {
		return result, err
	}
	if err = requireRealDirectory(receipts); err != nil {
		return result, err
	}
	result.ReceiptPath = filepath.Join(receipts, time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
	result.Status = "started"
	raw, _ := json.Marshal(result)
	if err = writeExclusiveFile(result.ReceiptPath, raw, 0600); err != nil {
		return result, err
	}
	if err = syncRetentionDirectory(receipts); err != nil {
		return result, err
	}
	for _, e := range live.Selection.Remove {
		if err = ctx.Err(); err != nil {
			result.Status = "partial"
			return result, err
		}
		identity, _, err := retentionIdentity(e.Path)
		if err != nil || identity != live.Inventory[e.PackageID] {
			result.Status = "partial"
			return result, fmt.Errorf("retention entry changed: %s", e.PackageID)
		}
		staged := ".retired-" + e.PackageID
		if _, err = handle.Lstat(staged); !os.IsNotExist(err) {
			result.Status = "partial"
			return result, fmt.Errorf("retained removal staging already exists")
		}
		if err = handle.Rename(e.PackageID, staged); err == nil {
			err = syncRetentionDirectory(root)
		}
		if err == nil {
			err = handle.RemoveAll(staged)
		}
		if err == nil {
			err = syncRetentionDirectory(root)
		}
		if err != nil {
			result.Status = "partial"
			return result, err
		}
		result.Removed = append(result.Removed, e.PackageID)
		raw, _ = json.Marshal(result)
		if err = writeExclusiveFile(result.ReceiptPath+"."+e.PackageID, raw, 0600); err != nil {
			result.Status = "partial"
			return result, err
		}
	}
	sort.Strings(result.Removed)
	result.Status = "applied"
	raw, _ = json.Marshal(result)
	if err = writeExclusiveFile(result.ReceiptPath+".result", raw, 0600); err != nil {
		return result, err
	}
	return result, syncRetentionDirectory(receipts)
}

func syncRetentionDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
