//go:build !darwin

package lane

import "os"

func cloneRegularFile(src, dst string, mode os.FileMode) (bool, error) {
	return false, nil
}
