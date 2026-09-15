//go:build !linux && !darwin

package hermesprofile

import (
	"fmt"
	"os"
)

func newSourceMonitor() (sourceMonitor, error) {
	return nil, fmt.Errorf("Hermes source custody monitoring is unsupported on this platform")
}

// No publication primitive is available when custody cannot be monitored.
func renameNoReplace(_ *os.File, _ string, _ *os.File, _ string) error {
	return fmt.Errorf("Hermes publication is unsupported on this platform")
}
