package backupstrategy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"

	"loom.local/loom/internal/boxregistry"
)

// WithBoxRecoveryContext adds only the validated catalog's non-secret fields to
// the existing update-state artifact. The fixed v1 package layout stays intact.
func WithBoxRecoveryContext(updateState []byte, catalog boxregistry.Catalog) ([]byte, error) {
	if err := validateJSONObject(updateState); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	if _, err := decodeBoxRecoveryContext(raw); err != nil {
		return nil, err
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(updateState, &state); err != nil {
		return nil, err
	}
	state["box_context"] = raw
	return json.Marshal(state)
}

// ReadBoxRecoveryContext works without the original config file or source roots.
// A legacy artifact with no context is valid but provides no Box recovery proof.
func ReadBoxRecoveryContext(updateState []byte) (*boxregistry.Catalog, error) {
	if err := validateJSONObject(updateState); err != nil {
		return nil, err
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(updateState, &state); err != nil {
		return nil, err
	}
	raw, ok := state["box_context"]
	if !ok {
		return nil, nil
	}
	return decodeBoxRecoveryContext(raw)
}

func decodeBoxRecoveryContext(raw []byte) (*boxregistry.Catalog, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("Box recovery context exceeds bound")
	}
	var catalog boxregistry.Catalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("invalid Box recovery context")
	}
	if catalog.SchemaVersion != boxregistry.SchemaVersion || len(catalog.Boxes) == 0 || len(catalog.Boxes) > 128 {
		return nil, fmt.Errorf("invalid Box recovery catalog")
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	primary := 0
	for _, box := range catalog.Boxes {
		if box.Key == "" || keys[box.Key] || box.Node == "" || !filepath.IsAbs(box.RootPath) || filepath.Clean(box.RootPath) != box.RootPath || box.RootPath == "/" {
			return nil, fmt.Errorf("invalid Box recovery binding")
		}
		keys[box.Key] = true
		if box.ID != "" {
			if !backupBoxIDPattern.MatchString(box.ID) || ids[box.ID] {
				return nil, fmt.Errorf("invalid Box recovery identity")
			}
			ids[box.ID] = true
		} else if box.Available && !box.Primary {
			return nil, fmt.Errorf("Box recovery identity missing")
		}
		for _, path := range []string{box.StateRoot, box.ArchiveRoot} {
			if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/") {
				return nil, fmt.Errorf("invalid Box recovery state path")
			}
		}
		if box.Available && !box.Primary && box.StateRoot == "" {
			return nil, fmt.Errorf("Box recovery state path missing")
		}
		if box.Primary {
			primary++
			if box.RootRef != "box" || box.Key != catalog.DefaultBox {
				return nil, fmt.Errorf("Box recovery primary differs")
			}
		} else if box.ID != "" && box.RootRef != "box:"+box.ID {
			return nil, fmt.Errorf("Box recovery reference differs")
		}
	}
	if primary != 1 {
		return nil, fmt.Errorf("Box recovery primary missing")
	}
	return &catalog, nil
}
