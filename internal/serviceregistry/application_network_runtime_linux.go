//go:build linux

package serviceregistry

import (
	"context"
	"os"
	"path/filepath"
)

// All service and same-UID action starts must share the caller's owner lock.
// This adapter neither stops/kills processes nor removes systemd address fences.
func (h *ApplicationHostSystem) applicationNetworkRuntime() applicationNetworkRuntime {
	proc := applicationNetworkProc{fs: os.DirFS("/proc"), readlink: func(path string) (string, error) { return os.Readlink(filepath.Join("/proc", path)) }}
	return applicationNetworkRuntime{
		tools: ApplicationNetworkTools{IPTables: h.Config.IPTables, IP6Tables: h.Config.IP6Tables},
		run:   h.command,
		quiescent: func(ctx context.Context, g ApplicationGeneration) error {
			if os.Geteuid() != 0 || !applicationAbsolutePath(h.Config.Systemctl) {
				return applicationError("network.root_host_required")
			}
			raw, err := h.command(ctx, h.Config.Systemctl, "show", g.Owner.Unit(), "--all", "--property="+applicationNetworkUnitProperties)
			if err != nil {
				return applicationError("network.unit_uncertain")
			}
			if err = applicationNetworkStoppedUnit(raw); err != nil {
				return err
			}
			return proc.quiescent(ctx, g.UID)
		},
	}
}

func (h *ApplicationHostSystem) ensureApplicationNetwork(ctx context.Context, g ApplicationGeneration, grant ApplicationGrant) error {
	return h.applicationNetworkRuntime().ensure(ctx, g, grant)
}

func (h *ApplicationHostSystem) checkApplicationNetwork(ctx context.Context, g ApplicationGeneration, grant ApplicationGrant) error {
	return h.applicationNetworkRuntime().check(ctx, g, grant)
}
