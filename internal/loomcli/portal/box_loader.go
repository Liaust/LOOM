package portal

import (
	"context"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/config"
)

func collectBoxData(ctx context.Context, client Client, correlationID string, options SnapshotOptions) (BoxData, []SnapshotError) {
	resolved, err := resolvePortalBox(options)
	if err != nil {
		return BoxData{}, []SnapshotError{{Source: "box", Message: err.Error()}}
	}
	data := BoxData{Status: portalBoxStatus(box.Inspect(resolved))}
	if data.Status.State != "ok" {
		return data, nil
	}

	plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	data.WatchPlan = plan
	if err != nil {
		return data, []SnapshotError{{Source: "box_watch_plan", Message: err.Error()}}
	}
	if client == nil {
		return data, []SnapshotError{{Source: "box_watch_status", Message: ErrMissingClientFor("load Box watch status").Error()}}
	}
	envelope, err := client.GetBoxWatchStatus(ctx, correlationID, box.WatchStatusInput{Resolved: resolved, Plan: &plan})
	if err != nil {
		return data, []SnapshotError{{Source: "box_watch_status", Message: err.Error()}}
	}
	data.WatchStatus = envelope.Data
	data.WatchStatusAvailable = true
	return data, nil
}

func portalBoxStatus(status box.Status) box.Status {
	// Portal is a current user surface. Historical Dropzone evidence remains
	// available through the bounded compatibility reader, but it is not an
	// active Portal domain. Main also has no visible Lane; workspace Lane is
	// intentionally preserved.
	status.DropzoneState = ""
	status.DropzoneTransfers = nil
	removeLane := status.Profile != box.ProfileWorkspace
	if removeLane {
		status.LaneState = ""
		status.Lane = nil
	}
	status.Areas = portalCurrentBoxPaths(status.Areas, removeLane)
	status.Policies = portalCurrentBoxPaths(status.Policies, removeLane)
	if status.Contract != nil {
		contract := *status.Contract
		contract.Areas = make(map[string]box.Area, len(status.Contract.Areas))
		for key, area := range status.Contract.Areas {
			if key == box.AreaDropzone || (removeLane && key == box.AreaLane) {
				continue
			}
			contract.Areas[key] = area
		}
		contract.Policies = make(map[string]string, len(status.Contract.Policies))
		for key, path := range status.Contract.Policies {
			if key == box.AreaDropzone || (removeLane && key == box.AreaLane) {
				continue
			}
			contract.Policies[key] = path
		}
		status.Contract = &contract
	}
	return status
}

func portalCurrentBoxPaths(items []box.PathStatus, removeLane bool) []box.PathStatus {
	filtered := make([]box.PathStatus, 0, len(items))
	for _, item := range items {
		if item.Key == box.AreaDropzone || (removeLane && item.Key == box.AreaLane) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func portalHasWorkspaceLane(status box.Status) bool {
	return status.Profile == box.ProfileWorkspace && status.Lane != nil
}

func resolvePortalBox(options SnapshotOptions) (box.Resolved, error) {
	if options.BoxResolved != nil {
		return *options.BoxResolved, nil
	}
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return box.Resolved{}, err
	}
	return box.Resolve(box.ResolveInput{
		ConfiguredPath:   cfg.BoxPath,
		RuntimeStateRoot: cfg.BoxStateRoot,
		ConfigProfile:    cfg.BoxProfile,
		NodeID:           cfg.NodeID,
		NodeRole:         cfg.NodeRole,
	})
}
