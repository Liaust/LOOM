package filepolicy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type ScanOptions struct {
	MaxEntries  int
	SampleLimit int
}

type CountSummary struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

type InspectionReport struct {
	Root              string               `json:"root"`
	Profile           Profile              `json:"profile"`
	PolicyVersion     string               `json:"policy_version"`
	PolicyFingerprint string               `json:"policy_fingerprint,omitempty"`
	PolicyFiles       []PolicyFile         `json:"policy_files,omitempty"`
	Included          CountSummary         `json:"included"`
	Ignored           CountSummary         `json:"ignored"`
	IgnoredBySource   map[RuleCategory]int `json:"ignored_by_source"`
	IgnoredSamples    []Decision           `json:"ignored_samples,omitempty"`
	ScannedEntries    int                  `json:"scanned_entries"`
	Truncated         bool                 `json:"truncated"`
	Errors            []string             `json:"errors,omitempty"`
}

var errInspectionTruncated = errors.New("file-policy inspection truncated")

func Inspect(root string, profile Profile, resolverOptions ResolverOptions, scanOptions ScanOptions) (InspectionReport, error) {
	if scanOptions.MaxEntries <= 0 {
		scanOptions.MaxEntries = 100000
	}
	if scanOptions.SampleLimit < 0 {
		scanOptions.SampleLimit = 0
	}
	if scanOptions.SampleLimit == 0 {
		scanOptions.SampleLimit = 20
	}
	report := InspectionReport{
		Root:            root,
		Profile:         profile,
		PolicyVersion:   BuiltInPolicyVersion,
		IgnoredBySource: map[RuleCategory]int{},
	}
	resolver, err := NewResolver(root, profile, resolverOptions)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
		return report, err
	}
	report.Root = resolver.Root()
	report.PolicyFingerprint = resolver.Fingerprint()
	report.PolicyFiles = resolver.PolicyFiles()

	err = filepath.WalkDir(resolver.Root(), func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == resolver.Root() {
			return nil
		}
		report.ScannedEntries++
		if report.ScannedEntries > scanOptions.MaxEntries {
			report.Truncated = true
			return errInspectionTruncated
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(resolver.Root(), pathValue)
		if err != nil {
			return err
		}
		resolution, err := resolver.Resolve(filepath.ToSlash(relative), false)
		if err != nil {
			return err
		}
		if resolution.Decision.Included {
			report.Included.Count++
			report.Included.Bytes += policyEntrySize(info)
			return nil
		}
		report.Ignored.Count++
		report.Ignored.Bytes += policyEntrySize(info)
		report.IgnoredBySource[resolution.Decision.RuleCategory]++
		if len(report.IgnoredSamples) < scanOptions.SampleLimit {
			report.IgnoredSamples = append(report.IgnoredSamples, resolution.Decision)
		}
		return nil
	})
	if errors.Is(err, errInspectionTruncated) {
		err = nil
	}
	if err != nil {
		wrapped := fmt.Errorf("inspect file policy under %q: %w", resolver.Root(), err)
		report.Errors = append(report.Errors, wrapped.Error())
		return report, wrapped
	}
	return report, nil
}

func policyEntrySize(info os.FileInfo) int64 {
	if info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}
