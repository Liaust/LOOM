package supportbundle

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultMaxItems = 50
	DefaultMaxBytes = int64(256 * 1024)
)

const DefaultTimeout = 30 * time.Second

type Options struct {
	OutputPath               string
	Profile                  Profile
	IncludeLogs              bool
	IncludeLive              bool
	IncludeProjects          bool
	Projects                 []string
	IncludeAbsolutePaths     bool
	LogPaths                 []string
	MaxItems                 int
	MaxBytes                 int64
	Timeout                  time.Duration
	DryRun                   bool
	Now                      time.Time
	Runtime                  RuntimeInfo
	SafeConfigFields         map[string]string
	Command                  []string
	CorrelationID            string
	CoreClient               CoreClient
	DoctorDataProvider       DoctorDataProvider
	CloudStatusProvider      CloudStatusProvider
	LiveDiagnosticsProvider  LiveDiagnosticsProvider
	ProvenanceHealthProvider ProvenanceHealthProvider
	RedactionProfile         string
	PathAliases              []PathAlias
	UserFileContentsAllowed  bool
}

func DefaultOptions() Options {
	return Options{
		Profile:          ProfileDefault,
		MaxItems:         DefaultMaxItems,
		MaxBytes:         DefaultMaxBytes,
		Timeout:          DefaultTimeout,
		RedactionProfile: RedactionProfileDefault,
	}
}

func NormalizeOptions(input Options) (Options, error) {
	defaults := DefaultOptions()
	opts := input
	profile, err := NormalizeProfile(string(opts.Profile))
	if err != nil {
		return Options{}, err
	}
	opts.Profile = profile
	if opts.MaxItems == 0 {
		opts.MaxItems = defaults.MaxItems
	}
	if opts.MaxItems < 0 {
		return Options{}, fmt.Errorf("max items must be non-negative")
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaults.MaxBytes
	}
	if opts.MaxBytes < 0 {
		return Options{}, fmt.Errorf("max bytes must be non-negative")
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaults.Timeout
	}
	if opts.Timeout < 0 {
		return Options{}, fmt.Errorf("timeout must be non-negative")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}
	if strings.TrimSpace(opts.RedactionProfile) == "" {
		opts.RedactionProfile = defaults.RedactionProfile
	}
	opts.Projects = compactStrings(opts.Projects)
	opts.LogPaths = compactStrings(opts.LogPaths)
	opts.SafeConfigFields = cloneStringMap(opts.SafeConfigFields)
	if len(opts.PathAliases) == 0 {
		opts.PathAliases = DefaultPathAliases()
	} else {
		opts.PathAliases = NormalizePathAliases(opts.PathAliases)
	}
	return opts, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func limitsFromOptions(opts Options) Limits {
	return Limits{
		MaxItems: opts.MaxItems,
		MaxBytes: opts.MaxBytes,
		Timeout:  opts.Timeout,
		TimeoutS: opts.Timeout.String(),
	}
}

func privacyFlagsFromOptions(opts Options) PrivacyFlags {
	privacy := PrivacyFlags{
		LogsIncluded:             opts.IncludeLogs,
		LiveProbesAllowed:        opts.IncludeLive,
		AbsolutePathsPreserved:   opts.IncludeAbsolutePaths,
		UserFileContentsIncluded: opts.UserFileContentsAllowed,
	}
	if opts.IncludeLive {
		privacy.LiveSections = []string{"cloud"}
	}
	return privacy
}
