package projects

import (
	"encoding/json"
	"fmt"
)

func filesystemOperationGuard(metadata json.RawMessage) error {
	var object map[string]json.RawMessage
	if len(metadata) == 0 {
		return nil
	}
	if json.Unmarshal(metadata, &object) != nil {
		return fmt.Errorf("invalid project metadata")
	}
	raw, present := object["workspace_filesystem_operation"]
	if !present {
		return nil
	}
	var pending struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &pending) != nil || pending.ID == "" {
		return fmt.Errorf("%w: invalid pending filesystem operation", ErrProjectArchiveInProgress)
	}
	return fmt.Errorf("%w: recover or cancel the exact filesystem operation %s first", ErrProjectArchiveInProgress, pending.ID)
}
