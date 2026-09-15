package portal

import (
	"strings"

	"loom.local/loom/internal/loomcli/actions"
)

func RawCommandPreview(action actions.Action) string {
	return strings.Join(action.RawCommand, " ")
}
