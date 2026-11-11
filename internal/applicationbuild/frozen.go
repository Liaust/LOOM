package applicationbuild

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// Freeze persists the accepted build inputs in backend-owned custody. Policy
// bytes are retained too, so the same capture function can verify the copy.
func (s *Service) Freeze(ctx context.Context, in Input, destination string, expected Snapshot) error {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return ErrInvalidInput
	}
	if _, err := os.Lstat(destination); err == nil {
		return &os.PathError{Op: "freeze", Path: destination, Err: os.ErrExist}
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".freeze-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	sink := func(name string, raw []byte) error {
		p := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return err
		}
		return os.WriteFile(p, raw, 0400)
	}
	var got Snapshot
	if legacyInput(in) {
		got, err = captureSource(ctx, in, s.options, sink)
	} else {
		got, err = s.captureProgram(ctx, in, sink)
	}
	if err != nil {
		return err
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		return ErrSourceChanged
	}
	// The declaration's project lock serializes publication of this unique key.
	// Failed/cancelled capture never leaves a partial final snapshot to reuse.
	return os.Rename(stage, destination)
}
