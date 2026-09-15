package supportbundle

import (
	"context"
	"fmt"
	"time"
)

type Collector struct {
	Key          string
	Title        string
	Profiles     []Profile
	PrivacyClass string
	RequiresLogs bool
	RequiresLive bool
	Collect      func(context.Context, CollectionContext) (CollectorOutput, error)
}

type CollectionContext struct {
	Options Options
	Plan    CollectionPlan
}

type CollectorOutput struct {
	Files      []File
	Warnings   []string
	Redactions int
	SkipReason string
	Truncated  bool
}

func DefaultCollectors() []Collector {
	return []Collector{
		{Key: "version", Title: "Version", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyMetadata, Collect: collectVersion},
		{Key: "node", Title: "Node", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyMetadata, Collect: collectNode},
		{Key: "config", Title: "Safe Configuration And Filesystem Layout", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectConfig},
		{Key: "health", Title: "Health", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectHealth},
		{Key: "status", Title: "Status", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectStatus},
		{Key: "doctor", Title: "Doctor", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectDoctor},
		{Key: "setup", Title: "Setup", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectSetup},
		{Key: "database", Title: "Database And Migrations", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectDatabase},
		{Key: "provenance", Title: "Provenance Recovery Health", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectProvenance},
		{Key: "workers", Title: "Workers", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectWorkers},
		{Key: "jobs", Title: "Jobs", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectJobs},
		{Key: "notes", Title: "Notes And Knowledge", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectNotes},
		{Key: "storage", Title: "Storage", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectStorage},
		{Key: "backup_coverage", Title: "Backup Coverage", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectBackup},
		{Key: "cloud", Title: "Cloud", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectCloud},
		{Key: "projects", Title: "Projects", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectProjects},
		{Key: "portal", Title: "Portal", Profiles: []Profile{ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, Collect: collectPortal},
		{Key: "logs", Title: "Logs", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyLogExcerpt, RequiresLogs: true, Collect: collectLogs},
		{Key: "live", Title: "Live Diagnostics", Profiles: []Profile{ProfileMinimal, ProfileDefault, ProfileFull}, PrivacyClass: PrivacyDiagnosticSummary, RequiresLive: true, Collect: collectLive},
	}
}

func collectSections(ctx context.Context, opts Options, plan CollectionPlan, collectors []Collector) ([]File, []SectionResult) {
	files := []File{}
	results := make([]SectionResult, 0, len(plan.Collectors)+len(plan.Skipped))
	byKey := map[string]Collector{}
	for _, collector := range collectors {
		byKey[collector.Key] = collector
	}
	for _, skipped := range plan.Skipped {
		results = append(results, SectionResult{
			Key:          skipped.Key,
			Title:        skipped.Title,
			Status:       SectionStatusSkipped,
			Reason:       skipped.Reason,
			PrivacyClass: skipped.PrivacyClass,
		})
	}
	for _, planned := range plan.Collectors {
		collector := byKey[planned.Key]
		result := SectionResult{
			Key:          planned.Key,
			Title:        planned.Title,
			PrivacyClass: planned.PrivacyClass,
		}
		if collector.Collect == nil {
			result.Status = SectionStatusSkipped
			result.Reason = "collector_not_implemented"
			results = append(results, result)
			continue
		}
		collectorCtx := ctx
		cancel := func() {}
		if opts.Timeout > 0 {
			collectorCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		}
		output, err := collector.Collect(collectorCtx, CollectionContext{Options: opts, Plan: plan})
		cancel()
		if err != nil {
			text, report := RedactDiagnosticText(err.Error(), opts)
			result.Status = SectionStatusFailed
			result.Error = text
			result.Redactions = report.Redactions
			results = append(results, result)
			continue
		}
		result.Status = SectionStatusIncluded
		redactions := output.Redactions
		for _, warning := range output.Warnings {
			text, report := RedactDiagnosticText(warning, opts)
			result.Warnings = append(result.Warnings, text)
			redactions += report.Redactions
		}
		for _, file := range output.Files {
			if file.PrivacyClass == "" {
				file.PrivacyClass = planned.PrivacyClass
			}
			redactedFile, report := RedactFile(file, opts)
			file = redactedFile
			redactions += report.Redactions
			original := int64(len(file.Data))
			if opts.MaxBytes > 0 && original > opts.MaxBytes {
				file.Data = append([]byte{}, file.Data[:opts.MaxBytes]...)
				file.Data = append(file.Data, []byte("\n[truncated]\n")...)
				result.Status = SectionStatusTruncated
				result.Truncated = true
			}
			result.Bytes += int64(len(file.Data))
			if result.Path == "" {
				result.Path = file.Path
			}
			files = append(files, file)
		}
		if output.Truncated && result.Status == SectionStatusIncluded {
			result.Status = SectionStatusTruncated
			result.Truncated = true
		}
		result.Redactions = redactions
		if len(output.Files) == 0 && result.Status == SectionStatusIncluded {
			result.Status = SectionStatusSkipped
			result.Reason = firstNonEmpty(output.SkipReason, "collector_returned_no_files")
		}
		results = append(results, result)
	}
	return files, results
}

func NewJSONCollector(key, title string, profiles []Profile, value any) Collector {
	return Collector{
		Key:          key,
		Title:        title,
		Profiles:     profiles,
		PrivacyClass: PrivacyDiagnosticSummary,
		Collect: func(context.Context, CollectionContext) (CollectorOutput, error) {
			data, err := marshalJSON(value)
			if err != nil {
				return CollectorOutput{}, err
			}
			return CollectorOutput{Files: []File{{
				Path:         fmt.Sprintf("summaries/%s.json", key),
				ContentType:  "application/json",
				PrivacyClass: PrivacyDiagnosticSummary,
				Data:         data,
			}}}, nil
		},
	}
}

func NewFailingCollector(key, title string, err error) Collector {
	return Collector{
		Key:          key,
		Title:        title,
		Profiles:     []Profile{ProfileMinimal, ProfileDefault, ProfileFull},
		PrivacyClass: PrivacyDiagnosticSummary,
		Collect: func(context.Context, CollectionContext) (CollectorOutput, error) {
			if err == nil {
				err = fmt.Errorf("collector failed")
			}
			return CollectorOutput{}, err
		},
	}
}

func fixedTime() time.Time {
	return time.Unix(0, 0).UTC()
}
