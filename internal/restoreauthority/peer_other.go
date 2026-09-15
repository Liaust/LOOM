//go:build !linux && !darwin

package restoreauthority

import "fmt"

func peerUID(_ int) (uint32, error) {
	return 0, fmt.Errorf("Unix peer credentials are unsupported on this platform")
}
