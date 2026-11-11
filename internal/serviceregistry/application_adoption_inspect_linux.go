//go:build linux

package serviceregistry

import (
	"context"
	"path/filepath"
)

func (h *ApplicationHostSystem) inspectProjectState(ctx context.Context, p ApplicationDataPolicy, uid uint32, prior ApplicationDataIdentity) (ApplicationPrerequisiteData, string) {
	checked := false
	read := func(ctx context.Context, tool string, args ...string) ([]byte, error) {
		if tool != h.Config.Getfacl {
			return nil, applicationError("project_state.inspect_read_only")
		}
		if !checked {
			resolved, err := filepath.EvalSymlinks(tool)
			if !applicationAbsolutePath(tool) || err != nil || applicationImmutableParents(resolved, 0) != nil || applicationCustody(resolved, 0, false) != nil {
				return nil, applicationError("project_state.tool_custody")
			}
			checked = true
		}
		return h.command(ctx, tool, args...)
	}
	return (applicationProjectStateRuntime{
		store: ApplicationStateStore{Root: h.Config.StateRoot, OwnerUID: 0, TrustedParentOwners: h.Config.TrustedParentOwners},
		tools: ApplicationStateACLTools{Getfacl: h.Config.Getfacl, Setfacl: h.Config.Setfacl},
		run:   read, trustedParentOwners: h.Config.TrustedParentOwners, trustedParentGroups: h.Config.TrustedParentGroups,
	}).inspect(ctx, p, uid, prior)
}
