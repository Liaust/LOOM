//go:build linux

package notesworkspace

import "golang.org/x/sys/unix"

// Same no-replace primitive used by storagearchive and estatemigration.
func renameNoReplace(fromFD int, from string, toFD int, to string) error {
	return unix.Renameat2(fromFD, from, toFD, to, unix.RENAME_NOREPLACE)
}
