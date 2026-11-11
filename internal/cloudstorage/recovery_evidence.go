package cloudstorage

import "loom.local/loom/internal/backupstrategy"

// Recovery identities come only from the authenticated archive envelope, never
// current host configuration. Absolute paths and private file contents stay out.
type SnapshotRecoveryEvidence struct {
	ManifestSchema     string                            `json:"manifest_schema"`
	OperationalPackage SnapshotRecoveryPackageEvidence   `json:"operational_package"`
	ProvenancePackage  SnapshotRecoveryPackageEvidence   `json:"provenance_package"`
	HermesRecovery     []SnapshotRecoveryPackageEvidence `json:"hermes_recovery,omitempty"`
	NotesRecovery      []SnapshotNotesRecoveryEvidence   `json:"notes_recovery,omitempty"`
}

type SnapshotRecoveryPackageEvidence struct {
	ID             string                                  `json:"id"`
	ManifestSHA256 string                                  `json:"manifest_sha256"`
	Files          map[string]SnapshotRecoveryFileEvidence `json:"files,omitempty"`
}

type SnapshotRecoveryFileEvidence struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type SnapshotNotesRecoveryEvidence struct {
	ID                 string                                  `json:"id"`
	CohortSHA256       string                                  `json:"cohort_sha256"`
	EnrollmentSHA256   string                                  `json:"enrollment_sha256,omitempty"`
	OperationalPackage SnapshotRecoveryPackageEvidence         `json:"operational_package"`
	Files              map[string]SnapshotRecoveryFileEvidence `json:"files"`
}

func snapshotRecoveryEvidence(prepared backupstrategy.PreparedDirectArchiveManifest) *SnapshotRecoveryEvidence {
	m := prepared.ManifestV2
	if m == nil {
		return nil
	}
	evidence := &SnapshotRecoveryEvidence{
		ManifestSchema: m.Schema,
		OperationalPackage: SnapshotRecoveryPackageEvidence{ID: m.OperationalPackage.PackageID,
			ManifestSHA256: m.OperationalPackage.ManifestSHA256},
		ProvenancePackage: SnapshotRecoveryPackageEvidence{ID: m.ProvenancePackage.PackageID,
			ManifestSHA256: m.ProvenancePackage.ManifestSHA256},
	}
	for _, recovery := range m.HermesRecovery {
		item := SnapshotRecoveryPackageEvidence{ID: recovery.ID, ManifestSHA256: recovery.ManifestSHA256,
			Files: make(map[string]SnapshotRecoveryFileEvidence, len(recovery.Files))}
		for _, file := range recovery.Files {
			item.Files[file.Path] = SnapshotRecoveryFileEvidence{SHA256: file.SHA256, Size: file.Size}
		}
		evidence.HermesRecovery = append(evidence.HermesRecovery, item)
	}
	if prepared.ManifestV3 != nil {
		evidence.ManifestSchema = prepared.ManifestV3.Schema
		for _, recovery := range prepared.ManifestV3.NotesRecovery {
			item := SnapshotNotesRecoveryEvidence{ID: recovery.ID, CohortSHA256: recovery.CohortSHA256,
				EnrollmentSHA256:   recovery.EnrollmentSHA256,
				OperationalPackage: SnapshotRecoveryPackageEvidence{ID: recovery.PackageID, ManifestSHA256: recovery.ManifestSHA256},
				Files:              make(map[string]SnapshotRecoveryFileEvidence, len(recovery.Files))}
			for _, file := range recovery.Files {
				item.Files[file.Path] = SnapshotRecoveryFileEvidence{SHA256: file.SHA256, Size: file.Size}
			}
			evidence.NotesRecovery = append(evidence.NotesRecovery, item)
		}
	}
	return evidence
}
