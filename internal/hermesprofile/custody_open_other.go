//go:build !linux

package hermesprofile

import "os"

func openDirectoryPathAt(parent *os.File, name string) (*os.File, error) {
	return openAt(parent, name, true)
}
