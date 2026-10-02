//go:build linux

package notesworkspace

import "golang.org/x/sys/unix"

func checkPlatformMetadata(_ int, _ unix.Stat_t) error { return nil }
func supportedXattr(name string) bool {
	return ordinaryUserXattr(name) || name == "system.posix_acl_access"
}
