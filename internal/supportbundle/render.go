package supportbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func Create(ctx context.Context, input Options, collectors []Collector) (Result, error) {
	opts, err := NormalizeOptions(input)
	if err != nil {
		return Result{}, err
	}
	if collectors == nil {
		collectors = DefaultCollectors()
	}
	plan, err := BuildPlan(opts, collectors)
	if err != nil {
		return Result{}, err
	}
	bundleID := bundleID(opts)
	if opts.DryRun {
		manifest := buildManifest(opts, bundleID, plan, nil)
		return Result{
			BundleID:   bundleID,
			Status:     "planned",
			OutputPath: opts.OutputPath,
			Profile:    opts.Profile,
			DryRun:     opts.DryRun,
			Plan:       plan,
			Manifest:   manifest,
			Warnings:   manifest.Warnings,
			Errors:     manifest.Errors,
		}, nil
	}
	files, sections := collectSections(ctx, opts, plan, collectors)
	manifest := buildManifest(opts, bundleID, plan, sections)
	result := Result{
		BundleID:   bundleID,
		Status:     "created",
		OutputPath: opts.OutputPath,
		Profile:    opts.Profile,
		DryRun:     opts.DryRun,
		Plan:       plan,
		Manifest:   manifest,
		Sections:   sections,
		Warnings:   manifest.Warnings,
		Errors:     manifest.Errors,
	}
	files = append(standardFiles(plan, manifest, sections), files...)
	if err := WriteArchive(opts.OutputPath, files); err != nil {
		return result, err
	}
	result.ArchivePath = opts.OutputPath
	return result, nil
}

func RenderPlanText(plan CollectionPlan, outputPath string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "LOOM support bundle plan\n")
	fmt.Fprintf(&builder, "Profile: %s\n", plan.Profile)
	if outputPath != "" {
		fmt.Fprintf(&builder, "Would write: %s\n", outputPath)
	}
	fmt.Fprintf(&builder, "Collectors: included=%d skipped=%d\n", len(plan.Collectors), len(plan.Skipped))
	fmt.Fprintf(&builder, "No archive written.\n")
	return builder.String()
}

func RenderResultText(result Result) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "LOOM support bundle: %s\n", result.Status)
	if result.OutputPath != "" {
		fmt.Fprintf(&builder, "Output: %s\n", result.OutputPath)
	}
	fmt.Fprintf(&builder, "Profile: %s\n", result.Profile)
	fmt.Fprintf(&builder, "Sections: included=%d skipped=%d failed=%d truncated=%d\n",
		result.Manifest.Counts.Included,
		result.Manifest.Counts.Skipped,
		result.Manifest.Counts.Failed,
		result.Manifest.Counts.Truncated,
	)
	fmt.Fprintf(&builder, "Warnings: %d\n", result.Manifest.Counts.Warnings)
	fmt.Fprintf(&builder, "Review before sharing.\n")
	return builder.String()
}

func buildManifest(opts Options, bundleID string, plan CollectionPlan, sections []SectionResult) Manifest {
	counts := SectionCounts{}
	warnings := []string{}
	errors := []SectionError{}
	manifestSections := make([]ManifestSection, 0, len(sections))
	for _, section := range sections {
		switch section.Status {
		case SectionStatusIncluded:
			counts.Included++
		case SectionStatusSkipped:
			counts.Skipped++
		case SectionStatusFailed:
			counts.Failed++
		case SectionStatusTruncated:
			counts.Truncated++
			counts.Included++
		}
		if len(section.Warnings) > 0 {
			counts.Warnings += len(section.Warnings)
			for _, warning := range section.Warnings {
				warnings = append(warnings, section.Key+": "+warning)
			}
		}
		counts.Redactions += section.Redactions
		if section.Error != "" {
			errors = append(errors, SectionError{Key: section.Key, Title: section.Title, Message: section.Error})
		}
		manifestSections = append(manifestSections, ManifestSection{
			Key:          section.Key,
			Title:        section.Title,
			Status:       section.Status,
			Path:         section.Path,
			Reason:       section.Reason,
			Error:        section.Error,
			PrivacyClass: section.PrivacyClass,
			Bytes:        section.Bytes,
			Truncated:    section.Truncated,
			Redactions:   section.Redactions,
		})
	}
	return Manifest{
		SchemaVersion:    SchemaVersion,
		BundleID:         bundleID,
		GeneratedAt:      opts.Now,
		Profile:          opts.Profile,
		RedactionProfile: opts.RedactionProfile,
		OutputPath:       opts.OutputPath,
		Runtime:          opts.Runtime,
		Command:          append([]string{}, opts.Command...),
		Limits:           limitsFromOptions(opts),
		Privacy:          privacyFlagsFromOptions(opts),
		Counts:           counts,
		Sections:         manifestSections,
		Warnings:         warnings,
		Errors:           errors,
	}
}

func standardFiles(plan CollectionPlan, manifest Manifest, sections []SectionResult) []File {
	return []File{
		jsonFile("manifest.json", manifest),
		textFile("README.txt", renderReadme(manifest)),
		textFile("human/summary.txt", RenderResultText(Result{Status: "created", OutputPath: manifest.OutputPath, Profile: manifest.Profile, Manifest: manifest})),
		textFile("human/collection_warnings.txt", renderWarnings(manifest)),
		jsonFile("collection/plan.json", plan),
		jsonFile("collection/errors.json", manifest.Errors),
		jsonFile("collection/redaction_report.json", map[string]any{
			"redaction_profile": manifest.RedactionProfile,
			"redactions":        manifest.Counts.Redactions,
			"sections":          sections,
		}),
	}
}

func renderReadme(manifest Manifest) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "LOOM Support Bundle\n\n")
	fmt.Fprintf(&builder, "Bundle: %s\n", manifest.BundleID)
	fmt.Fprintf(&builder, "Profile: %s\n", manifest.Profile)
	fmt.Fprintf(&builder, "Generated: %s\n\n", manifest.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&builder, "This archive contains redacted operational summaries for support and handoff.\n")
	fmt.Fprintf(&builder, "Review before sharing. User file contents are not included by default.\n")
	return builder.String()
}

func renderWarnings(manifest Manifest) string {
	if len(manifest.Warnings) == 0 && len(manifest.Errors) == 0 {
		return "No collection warnings or errors recorded.\n"
	}
	var builder strings.Builder
	for _, warning := range manifest.Warnings {
		fmt.Fprintf(&builder, "warning: %s\n", warning)
	}
	for _, err := range manifest.Errors {
		fmt.Fprintf(&builder, "error: %s: %s\n", err.Key, err.Message)
	}
	return builder.String()
}

func bundleID(opts Options) string {
	return "support_" + opts.Now.UTC().Format("20060102T150405Z")
}

func jsonFile(path string, value any) File {
	return File{
		Path:         path,
		ContentType:  "application/json",
		PrivacyClass: PrivacyMetadata,
		Data:         mustMarshalJSON(value),
	}
}

func textFile(path string, value string) File {
	return File{
		Path:         path,
		ContentType:  "text/plain",
		PrivacyClass: PrivacyMetadata,
		Data:         []byte(value),
	}
}

func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func mustMarshalJSON(value any) []byte {
	data, err := marshalJSON(value)
	if err != nil {
		panic(err)
	}
	return data
}
