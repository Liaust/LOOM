package hermesprofile

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RecoveryIdentity selects a producer; it is not a physical package location.
type RecoveryIdentity string

const (
	MorathustraIdentity RecoveryIdentity = "morathustra"
	MinaIdentity        RecoveryIdentity = "mina"
	MinaWorkspaceRoot                    = "/srv/loom/agents/mina"
)

func (id RecoveryIdentity) WorkspaceRoot() (string, error) {
	switch id {
	case "", MorathustraIdentity:
		return WorkspaceRoot, nil
	case MinaIdentity:
		return MinaWorkspaceRoot, nil
	default:
		return "", fmt.Errorf("unknown Hermes recovery identity")
	}
}

// FixtureWorkspace admits only canonical disposable locations, never a live
// production root or its descendants (even a nested .loom-acceptance folder).
func FixtureWorkspace(workspace string) bool {
	if !exactWorkspacePath(workspace) || !strings.Contains(workspace, "/.loom-acceptance/") {
		return false
	}
	for _, root := range []string{WorkspaceRoot, MinaWorkspaceRoot} {
		if workspace == root || strings.HasPrefix(workspace, root+"/") {
			return false
		}
	}
	return true
}

func exactWorkspacePath(workspace string) bool {
	return filepath.IsAbs(workspace) && filepath.Clean(workspace) == workspace && !strings.ContainsAny(workspace, "\x00\r\n")
}

// ResolveWorkspace separates physical custody from the selected signed origin.
// Old disposable callers retain their exact fixture origin. Explicit selection
// uses the fixed production origin even when exercised in a disposable fixture.
func ResolveWorkspace(id RecoveryIdentity, workspace string) (location, origin string, err error) {
	origin, err = id.WorkspaceRoot()
	if err != nil {
		return "", "", err
	}
	if workspace == "" {
		workspace = origin
	}
	if workspace != origin {
		if !FixtureWorkspace(workspace) {
			return "", "", fmt.Errorf("workspace conflicts with selected Hermes identity")
		}
		if id == "" {
			origin = workspace
		}
	}
	return workspace, origin, nil
}
