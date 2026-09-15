package dropzone

import (
	"path/filepath"
	"sort"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

type StatusInput struct {
	RootPath    string
	StateRoot   string
	Profile     string
	Policy      Policy
	Diagnostics []Diagnostic
	Now         time.Time
}

// BuildStatus inspects only already-recorded Dropzone state. It never scans a
// Dropzone directory for new work and never creates transfer state.
func BuildStatus(input StatusInput) Status {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	policy := NormalizePolicy(input.Policy, input.Profile)
	reader := NewReader(input.RootPath, policy, input.StateRoot)
	status := Status{
		RuntimeState:       RuntimeRetired,
		Profile:            input.Profile,
		RootPath:           input.RootPath,
		DropzonePath:       filepath.Join(input.RootPath, filepath.FromSlash(policy.Path)),
		StatusDir:          reader.StatusDir,
		TransfersDir:       reader.TransfersDir,
		PolicyEnabled:      false,
		PolicySchema:       policy.SchemaVersion,
		SourcePolicySchema: policy.SourceSchemaVersion,
		Target:             RuntimeRetired,
		Counts:             map[string]int{},
		Diagnostics:        append([]Diagnostic{}, input.Diagnostics...),
		InspectedAt:        now,
	}
	records, err := reader.List()
	if err != nil {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "dropzone.history_unavailable",
			Message:    err.Error(),
			Path:       reader.TransfersDir,
			Suggestion: "inspect the node-owned historical Dropzone state permissions",
		})
		return status
	}
	for _, record := range records {
		if record.Status == "" {
			record.Status = StatusDiscovered
		}
		status.Counts[record.Status]++
		summary := summarize(record)
		switch {
		case ActiveStatuses[record.Status]:
			status.Active = append(status.Active, summary)
		case record.Status == StatusAccepted:
			status.Accepted = append(status.Accepted, summary)
		case record.Status == StatusFailed:
			status.Failed = append(status.Failed, summary)
		}
	}
	sortSummaries(status.Active)
	sortSummaries(status.Accepted)
	sortSummaries(status.Failed)
	return status
}

func summarize(record TransferRecord) TransferSummary {
	return TransferSummary{
		TransferID:           record.TransferID,
		RelativeDropzonePath: record.RelativeDropzonePath,
		FileSizeBytes:        record.FileSizeBytes,
		Status:               record.Status,
		UploadedBytes:        record.UploadedBytes,
		SafeToDelete:         record.SafeToDelete,
		RemoteStoragePath:    record.RemoteStoragePath,
		StorageEntryID:       record.StorageEntryID,
		FailureMessage:       record.FailureMessage,
		FidelityWarnings:     fidelityWarnings(record.FilesystemObservation),
		UpdatedAt:            record.UpdatedAt,
		AcceptedAt:           record.AcceptedAt,
	}
}

func fidelityWarnings(observation *filesystemmeta.Observation) []string {
	if observation == nil {
		return nil
	}
	warnings := append([]string(nil), observation.Risks...)
	if observation.IsPackage {
		warnings = append(warnings, "package_directory")
	}
	if observation.IsSparse {
		warnings = append(warnings, "sparse_file")
	}
	if observation.Executable {
		warnings = append(warnings, "executable_bit")
	}
	return warnings
}

func Summarize(record TransferRecord) TransferSummary {
	return summarize(record)
}

func sortSummaries(values []TransferSummary) {
	sort.SliceStable(values, func(i, j int) bool {
		return values[i].UpdatedAt.After(values[j].UpdatedAt)
	})
}
