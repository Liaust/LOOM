package backupcoverage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"loom.local/loom/internal/backupstrategy"
)

// CloudRootEvidence describes retained file history, not live source equality,
// application-consistent database recovery, or a successful restore drill.
type CloudRootEvidence struct {
	Name           string    `json:"name"`
	PathSHA256     string    `json:"path_sha256"`
	NodeID         string    `json:"node_id"`
	SnapshotAt     time.Time `json:"snapshot_at"`
	ManifestSHA256 string    `json:"manifest_sha256,omitempty"`
}

func CloudRootPathSHA256(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])
}

// Called only for a committed successful archive. Never infer coverage from the
// current request: a replay can belong to an earlier frozen source snapshot.
func CloudRootsFromManifest(m *backupstrategy.DirectArchiveManifestV2, digest string) []CloudRootEvidence {
	return CloudRootsFromVersionedManifest(m, nil, digest)
}

func CloudRootsFromVersionedManifest(m *backupstrategy.DirectArchiveManifestV2, v3 *backupstrategy.DirectArchiveManifestV3, digest string) []CloudRootEvidence {
	if m == nil && v3 == nil {
		return nil
	}
	var envelope any = m
	if v3 != nil {
		envelope = v3
	}
	raw, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil
	}
	raw = append(raw, '\n')
	verified, err := backupstrategy.ParseAuthenticatedDirectArchiveManifest(raw, []byte(digest+"  "+backupstrategy.DirectArchiveManifestFile+"\n"))
	if err != nil || verified.ManifestV2 == nil {
		return nil
	}
	m = verified.ManifestV2
	var out []CloudRootEvidence
	for _, root := range m.Roots {
		if cloudRootEvidenceIncluded(root.Name, m.Exclusions) {
			out = append(out, CloudRootEvidence{Name: root.Name, PathSHA256: CloudRootPathSHA256(root.SourcePath), NodeID: m.NodeID, SnapshotAt: m.CreatedAt, ManifestSHA256: digest})
		}
	}
	return out
}

func cloudRootEvidenceIncluded(name string, exclusions []backupstrategy.DirectArchiveExclusion) bool {
	for _, exclusion := range exclusions {
		if exclusion.Root == name {
			// Owned acceptance scratch is not protected workspace content.
			allowedScratch := backupstrategy.IsBoxArchiveRoot(name) && strings.HasSuffix(name, "_source") && exclusion.RelativePath == ".loom-acceptance"
			if !allowedScratch {
				return false
			}
		}
	}
	return true
}
