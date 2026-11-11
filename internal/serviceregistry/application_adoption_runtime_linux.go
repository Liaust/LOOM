//go:build linux

package serviceregistry

import (
	"context"
	"path/filepath"
)

func (h *ApplicationHostSystem) ensureProjectState(ctx context.Context, p ApplicationDataPolicy, uid uint32, prior *ApplicationDataIdentity) (ApplicationDataIdentity, error) {
	for _, tool := range []string{h.Config.Getfacl, h.Config.Setfacl} {
		resolved, err := filepath.EvalSymlinks(tool)
		if !applicationAbsolutePath(tool) || err != nil || applicationImmutableParents(resolved, 0) != nil || applicationCustody(resolved, 0, false) != nil {
			return ApplicationDataIdentity{}, applicationError("project_state.tool_custody")
		}
	}
	return (applicationProjectStateRuntime{
		store:     ApplicationStateStore{Root: h.Config.StateRoot, OwnerUID: 0, TrustedParentOwners: h.Config.TrustedParentOwners},
		tools:     ApplicationStateACLTools{Getfacl: h.Config.Getfacl, Setfacl: h.Config.Setfacl},
		systemctl: h.Config.Systemctl, run: h.command, trustedParentOwners: h.Config.TrustedParentOwners,
		trustedParentGroups: h.Config.TrustedParentGroups,
	}).ensure(ctx, p, uid, prior)
}
