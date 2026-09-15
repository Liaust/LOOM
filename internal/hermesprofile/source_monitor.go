package hermesprofile

import (
	"fmt"
	"os"
)

type watchKind uint8

const (
	watchDirectory watchKind = iota
	watchOrdinary
	watchDatabase
	watchLog
	watchStatic
	watchSidecar
)

type sourceWatch struct {
	dir     *os.File
	kind    watchKind
	file    *os.File
	initial Identity
}
type sourceMonitor interface {
	add(*os.File, Identity, sourceWatch) error
	check(*sourceTree) (map[*os.File]uint64, error)
	close()
}

// Registration is bracketed by exact metadata equality, including ctime. A
// rename/attribute change between opening and installing the watch therefore
// fails even if it happened before the kernel began reporting events.
func monitorBinding(f *os.File, initial Identity) error {
	now, err := statFD(f)
	if err != nil || now != initial {
		return fmt.Errorf("profile changed while installing custody monitor")
	}
	return nil
}

func sourceChanges(changed map[*os.File]uint64) map[*os.File]uint64 {
	result := make(map[*os.File]uint64, len(changed))
	for f, n := range changed {
		result[f] = n
	}
	return result
}
func sameSourceChanges(a, b map[*os.File]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for f, n := range a {
		if b[f] != n {
			return false
		}
	}
	return true
}

// Allowed mutable events still require fresh metadata when drained, including
// events delivered after the namespace/hash pass. Observing an attribute event
// never by itself vouches for its final mode, owner, links or size.
func validateMutableWatch(w sourceWatch) error {
	if w.kind != watchDatabase && w.kind != watchLog && w.kind != watchSidecar {
		return nil
	}
	now, err := statFD(w.file)
	if err != nil {
		return err
	}
	if w.kind == watchSidecar && now.Links == 0 {
		return nil
	} // Permitted unlinked sidecar.
	if w.kind == watchSidecar {
		if !safeSidecar(now, sourceFile{initial: w.initial}) {
			return fmt.Errorf("unsafe mutable SQLite sidecar")
		}
	} else if !sameObject(now, w.initial) || !regular(now) || now.Size < 0 || now.Size > maxBytes {
		return fmt.Errorf("mutable profile metadata changed")
	}
	return nil
}
