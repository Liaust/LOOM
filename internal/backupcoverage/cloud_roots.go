package backupcoverage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"loom.local/loom/internal/backupstrategy"
)

// CloudRootEvidence describes retained file history, not live source equality,
// application-consistent database recovery, or a successful restore drill.
type CloudRootEvidence struct {
	Name       string    `json:"name"`
	PathSHA256 string    `json:"path_sha256"`
	NodeID     string    `json:"node_id"`
	SnapshotAt time.Time `json:"snapshot_at"`
}

func CloudRootPathSHA256(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])
}

// Called only for a committed successful archive. Never infer coverage from the
// current request: a replay can belong to an earlier frozen source snapshot.
func CloudRootsFromManifest(m *backupstrategy.DirectArchiveManifestV2, digest string) []CloudRootEvidence {
	if m == nil {
		return nil
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil
	}
	raw = append(raw, '\n')
	if _, err := backupstrategy.ParseAuthenticatedDirectArchiveManifest(raw, []byte(digest+"  "+backupstrategy.DirectArchiveManifestFile+"\n")); err != nil {
		return nil
	}
	var out []CloudRootEvidence
	for _, root := range m.Roots {
		excluded := false
		for _, exclusion := range m.Exclusions {
			excluded = excluded || exclusion.Root == root.Name
		}
		if !excluded {
			out = append(out, CloudRootEvidence{Name: root.Name, PathSHA256: CloudRootPathSHA256(root.SourcePath), NodeID: m.NodeID, SnapshotAt: m.CreatedAt})
		}
	}
	return out
}
