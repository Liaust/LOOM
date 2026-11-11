package backupstrategy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// CheckOperationalPackageAdmission counts allocation, not payload contents.
// Existing/held files count; refusing a new dump never evicts a recovery point.
func CheckOperationalPackageAdmission(ctx context.Context, root string, budget, freeFloor int64) error {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(root, &filesystem); err != nil {
		return err
	}
	return checkOperationalPackageAdmission(ctx, root, budget, freeFloor, uint64(filesystem.Bavail)*uint64(filesystem.Bsize))
}

func checkOperationalPackageAdmission(ctx context.Context, root string, budget, freeFloor int64, available uint64) error {
	if budget <= 0 || freeFloor <= 0 {
		return fmt.Errorf("positive operational capacity budget and free floor required")
	}
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	var allocated int64
	packages := map[string]int64{}
	seen := map[[2]uint64]bool{}
	entries := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > 65536 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("operational capacity inventory requires attention")
		}
		st := info.Sys().(*syscall.Stat_t)
		key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
		if seen[key] {
			return nil
		}
		seen[key] = true
		bytes := st.Blocks * 512
		if bytes < 0 || allocated > budget || bytes > budget-allocated {
			return fmt.Errorf("operational working set exceeds budget (%d bytes)", budget)
		}
		allocated += bytes
		relative, _ := filepath.Rel(root, path)
		packages[strings.SplitN(relative, string(filepath.Separator), 2)[0]] += bytes
		return nil
	})
	if err != nil {
		return err
	}
	var largest int64
	for _, bytes := range packages {
		if bytes > largest {
			largest = bytes
		}
	}
	if largest < 16<<30 {
		largest = 16 << 30
	}
	forecast := largest + largest/4
	if forecast > budget-allocated {
		return fmt.Errorf("new operational package would exceed local budget: retained=%d forecast=%d budget=%d", allocated, forecast, budget)
	}
	if available < uint64(freeFloor)+2*uint64(forecast) {
		return fmt.Errorf("operational free-space floor would be crossed: available=%d floor=%d staging_peak=%d", available, freeFloor, 2*forecast)
	}
	return nil
}
