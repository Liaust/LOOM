package loomcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/filepolicy"
)

func newIgnoreCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "ignore", Short: "Inspect effective LOOM file policy"}
	cmd.AddCommand(newIgnoreInspectCommand(opts))
	cmd.AddCommand(newIgnoreExplainCommand(opts))
	return cmd
}

func newIgnoreInspectCommand(opts *options) *cobra.Command {
	var operation string
	var maxEntries int
	var sampleLimit int
	cmd := &cobra.Command{
		Use:   "inspect <root>",
		Short: "Inspect included and ignored files under a root",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return err
			}
			profile, err := ignoreProfileForOperation(operation)
			if err != nil {
				return err
			}
			report, inspectErr := filepolicy.Inspect(args[0], profile, filepolicy.ResolverOptions{DiscoverUserRules: true}, filepolicy.ScanOptions{
				MaxEntries:  maxEntries,
				SampleLimit: sampleLimit,
			})
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				renderIgnoreInspection(cmd, operation, report)
			}
			return inspectErr
		},
	}
	cmd.Flags().StringVar(&operation, "operation", "backup", "operation profile: backup or lane")
	cmd.Flags().IntVar(&maxEntries, "max-entries", 100000, "maximum filesystem entries to scan")
	cmd.Flags().IntVar(&sampleLimit, "sample-limit", 20, "maximum ignored-path samples")
	return cmd
}

type ignoreExplanation struct {
	Root              string                  `json:"root"`
	Path              string                  `json:"path"`
	Operation         string                  `json:"operation"`
	Profile           filepolicy.Profile      `json:"profile"`
	PolicyVersion     string                  `json:"policy_version"`
	PolicyFingerprint string                  `json:"policy_fingerprint"`
	PolicyFiles       []filepolicy.PolicyFile `json:"policy_files,omitempty"`
	Resolution        filepolicy.Resolution   `json:"resolution"`
}

func newIgnoreExplainCommand(opts *options) *cobra.Command {
	var operation string
	var root string
	var directory bool
	cmd := &cobra.Command{
		Use:   "explain <path>",
		Short: "Explain the ordered policy decisions for one path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return err
			}
			profile, err := ignoreProfileForOperation(operation)
			if err != nil {
				return err
			}
			rootValue, relativePath, isDir, err := ignoreExplanationTarget(root, args[0], directory)
			if err != nil {
				return err
			}
			resolver, err := filepolicy.NewResolver(rootValue, profile, filepolicy.ResolverOptions{DiscoverUserRules: true})
			if err != nil {
				return err
			}
			resolution, err := resolver.Resolve(relativePath, isDir)
			if err != nil {
				return err
			}
			report := ignoreExplanation{
				Root:              resolver.Root(),
				Path:              filepath.ToSlash(relativePath),
				Operation:         strings.ToLower(strings.TrimSpace(operation)),
				Profile:           profile,
				PolicyVersion:     filepolicy.BuiltInPolicyVersion,
				PolicyFingerprint: resolver.Fingerprint(),
				PolicyFiles:       resolver.PolicyFiles(),
				Resolution:        resolution,
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			renderIgnoreExplanation(cmd, report)
			return nil
		},
	}
	cmd.Flags().StringVar(&operation, "operation", "backup", "operation profile: backup or lane")
	cmd.Flags().StringVar(&root, "root", "", "policy root (defaults to the current directory)")
	cmd.Flags().BoolVar(&directory, "directory", false, "treat a missing path as a directory")
	return cmd
}

func ignoreProfileForOperation(operation string) (filepolicy.Profile, error) {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "backup":
		return filepolicy.ProfileManaged, nil
	case "lane":
		return filepolicy.ProfileFaithful, nil
	default:
		return "", fmt.Errorf("unsupported ignore operation %q (want backup or lane)", operation)
	}
}

func ignoreExplanationTarget(rootValue, pathValue string, directory bool) (string, string, bool, error) {
	if strings.TrimSpace(rootValue) == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return "", "", false, err
		}
		rootValue = workingDirectory
	}
	absoluteRoot, err := filepath.Abs(rootValue)
	if err != nil {
		return "", "", false, err
	}
	absolutePath := pathValue
	if !filepath.IsAbs(absolutePath) {
		absolutePath = filepath.Join(absoluteRoot, absolutePath)
	}
	relativePath, err := filepath.Rel(absoluteRoot, filepath.Clean(absolutePath))
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", "", false, fmt.Errorf("path %q is outside policy root %q", pathValue, rootValue)
	}
	if info, statErr := os.Lstat(absolutePath); statErr == nil {
		directory = info.IsDir()
	} else if !os.IsNotExist(statErr) {
		return "", "", false, statErr
	}
	return absoluteRoot, filepath.ToSlash(relativePath), directory, nil
}

func renderIgnoreInspection(cmd *cobra.Command, operation string, report filepolicy.InspectionReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM ignore policy: %s\n", report.Root)
	fmt.Fprintf(cmd.OutOrStdout(), "Operation: %s profile=%s policy=%s\n", operation, report.Profile, report.PolicyVersion)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy fingerprint: %s\n", report.PolicyFingerprint)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy files: %d\n", len(report.PolicyFiles))
	for _, policyFile := range report.PolicyFiles {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s rules=%d\n", policyFile.RelativePath, policyFile.ContentHash, len(policyFile.Rules))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Included: files=%d bytes=%d\n", report.Included.Count, report.Included.Bytes)
	fmt.Fprintf(cmd.OutOrStdout(), "Ignored: files=%d bytes=%d\n", report.Ignored.Count, report.Ignored.Bytes)
	for _, category := range []filepolicy.RuleCategory{filepolicy.RuleCategoryMandatorySafety, filepolicy.RuleCategoryReconstructible, filepolicy.RuleCategoryUser, filepolicy.RuleCategoryContract} {
		if count := report.IgnoredBySource[category]; count > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s=%d\n", category, count)
		}
	}
	for _, sample := range report.IgnoredSamples {
		fmt.Fprintf(cmd.OutOrStdout(), "  ignored %s by %s %q\n", sample.Path, sample.RuleCategory, sample.Pattern)
	}
	if report.Truncated {
		fmt.Fprintf(cmd.OutOrStdout(), "Truncated: yes after %d entries\n", report.ScannedEntries)
	}
	for _, reportErr := range report.Errors {
		fmt.Fprintf(cmd.OutOrStdout(), "Policy error: %s\n", reportErr)
	}
}

func renderIgnoreExplanation(cmd *cobra.Command, report ignoreExplanation) {
	decision := report.Resolution.Decision
	state := "ignored"
	if decision.Included {
		state = "included"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM ignore explanation: %s\n", report.Path)
	fmt.Fprintf(cmd.OutOrStdout(), "Result: %s profile=%s policy=%s\n", state, report.Profile, report.PolicyVersion)
	for index, trace := range report.Resolution.Trace {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d. included=%t source=%s pattern=%q", index+1, trace.Included, trace.RuleCategory, trace.Pattern)
		if trace.SourceFile != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " at %s:%d", trace.SourceFile, trace.SourceLine)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
}
