package hermesprofile

import (
	"golang.org/x/sys/unix"
	"os"
)

func renameNoReplace(from *os.File, name string, to *os.File, dest string) error {
	return unix.Renameat2(int(from.Fd()), name, int(to.Fd()), dest, unix.RENAME_NOREPLACE)
}
