package backupcoverage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/boxregistry"
)

func (opts Options) ProtectionBoxes() (boxregistry.Catalog, error) {
	if opts.LoadBoxes != nil {
		return opts.LoadBoxes()
	}
	return opts.Boxes, opts.BoxCatalogError
}

func boxCoverageEntries(ctx context.Context, opts Options, now time.Time) []Entry {
	if opts.BoxCatalogError != nil {
		return []Entry{{Key: "box-catalog", Label: "Box protection catalog", Category: "recovery_package", Status: StatusUnknown, Critical: true, Message: "validated Box catalog unavailable"}}
	}
	var entries []Entry
	for _, box := range opts.Boxes.Boxes {
		if box.Primary {
			continue // Keep all historical Personal entry keys and semantics.
		}
		prefix := "box-" + box.ID
		if box.ID == "" {
			prefix = "box-unavailable-" + box.Key
		}
		if !box.Available {
			entries = append(entries, Entry{BoxID: box.ID, BoxKey: box.Key, Key: prefix + "-source", Label: box.Label + " source", Category: "canonical_custody_root", Path: box.RootPath, Status: StatusMissing, Critical: true, Message: "registered Box unavailable: " + box.Reason})
			continue
		}
		for _, item := range []struct{ kind, path string }{{"source", box.RootPath}, {"state", box.StateRoot}, {"archive", box.ArchiveRoot}} {
			if item.path == "" {
				continue
			}
			if item.kind == "archive" {
				if _, err := os.Lstat(item.path); os.IsNotExist(err) {
					continue // Archive namespaces are created only on use.
				}
			}
			entry := custodyDirectoryEntry(ctx, prefix+"-"+item.kind, box.Label+" "+item.kind, item.path, true)
			entry.BoxID, entry.BoxKey = box.ID, box.Key
			entry.RequirementBasis = "configured_readiness"
			entries = append(entries, entry)
			cloud := Entry{BoxID: box.ID, BoxKey: box.Key, Key: prefix + "-" + item.kind + "-cloud", Label: box.Label + " " + item.kind + " cloud history", Path: item.path, Category: "recovery_package", Critical: true, Status: StatusMissing, Message: "no exact authenticated cloud root evidence supplied"}
			name, err := backupstrategy.BoxArchiveRootName(box.ID, item.kind)
			if err == nil {
				for _, evidence := range opts.CloudRoots {
					if evidence.Name != name || evidence.PathSHA256 != CloudRootPathSHA256(item.path) || evidence.NodeID != box.Node || evidence.SnapshotAt.IsZero() || evidence.SnapshotAt.After(now) || evidence.ManifestSHA256 == "" {
						continue
					}
					if cloud.EvidenceCapturedAt == nil || evidence.SnapshotAt.After(*cloud.EvidenceCapturedAt) {
						captured := evidence.SnapshotAt.UTC()
						cloud.Status = StatusCovered
						cloud.Evidence = Evidence{EvidenceBasis: "authenticated_package_validation", EvidenceRef: evidence.ManifestSHA256, EvidenceCapturedAt: &captured, RequirementBasis: "recovery_assurance"}
						cloud.Message = "dated root inclusion only; not current source equality or a restore"
					}
				}
			}
			entries = append(entries, cloud)
		}
		local := opts
		local.MainBoxPath = box.RootPath
		for _, entry := range standaloneBackupContractEntries(ctx, local) {
			entry.Key = prefix + "-" + entry.Key
			entry.BoxID, entry.BoxKey = box.ID, box.Key
			entries = append(entries, entry)
		}
	}
	if len(opts.Boxes.Boxes) > 1 {
		entries = append(entries, Entry{Key: "box-catalog-recovery", Label: "Box catalog recovery contents", Category: "recovery_package", Status: StatusUnknown, Critical: true, Message: fmt.Sprintf("verify box_context contents in authenticated operational update-state.json for %d registered Boxes; a config filename is not recovery evidence", len(opts.Boxes.Boxes)), Path: filepath.Join(opts.DataDir, "backups", "operational")})
	}
	return entries
}
