package setup

import (
	"time"

	"loom.local/loom/internal/enrollmentflow"
)

type RecordEnrollmentInput struct {
	ManifestPath string
	Result       enrollmentflow.Result
	Now          func() time.Time
}

type RecordEnrollmentResult struct {
	Manifest InstallManifest `json:"manifest"`
}

func RecordEnrollment(input RecordEnrollmentInput) (RecordEnrollmentResult, error) {
	manifest, err := ReadManifest(input.ManifestPath)
	if err != nil {
		return RecordEnrollmentResult{}, err
	}
	manifest = ManifestWithEnrollmentResult(manifest, input.Result)
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	checkedAt := now().UTC()
	manifest.LastStatus.Status = applyManifestStatus(manifest).Status
	manifest.LastStatus.LastCheckedAt = &checkedAt
	if err := WriteManifest(input.ManifestPath, manifest); err != nil {
		return RecordEnrollmentResult{}, err
	}
	return RecordEnrollmentResult{Manifest: manifest}, nil
}
