package watchedroots

import (
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

func ClassifyPath(obs PathObservation, root ValidatedRoot) Classification {
	policies := EffectivePolicies(root.Config, obs.RelativePath)
	classification := Classification{
		Hidden:   obs.Hidden,
		Symlink:  obs.Symlink,
		Safe:     obs.Safe,
		Policies: policies,
	}
	if !obs.Safe {
		return classification.with(false, ReasonSkippedPathEscape, "path is outside the watched root or safe root")
	}
	if obs.ErrorCode == ReasonSkippedPermissionDenied {
		return classification.with(false, ReasonSkippedPermissionDenied, "path could not be inspected due to permissions")
	}
	if obs.Fidelity != nil && obs.Fidelity.GeneratedMetadata && !root.Config.FidelityPolicy.PreserveGeneratedAppleMetadata {
		return classification.with(false, ReasonExcludedGeneratedMetadata, "generated Apple metadata is observed but excluded from content management")
	}
	if obs.Symlink && !root.Config.Scan.FollowSymlinks {
		return classification.with(false, ReasonSkippedSymlink, "symlinks are skipped by this watched root")
	}
	if obs.Hidden && root.Config.Scan.HiddenPolicy == HiddenPolicyExcludeByDefault && !isHiddenAcceptancePath(obs.RelativePath) {
		return classification.with(false, ReasonExcludedHidden, "hidden paths are excluded by this watched root")
	}
	if obs.Kind == PathKindOther {
		return classification.with(false, ReasonSkippedSpecialFile, "special filesystem entries are skipped")
	}
	if root.PolicyResolver != nil {
		resolution, err := root.PolicyResolver.Resolve(obs.RelativePath, obs.Kind == PathKindDirectory)
		if err != nil {
			return classification.with(false, ReasonExcludedFilePolicy, "file policy could not be resolved: "+err.Error())
		}
		classification = withPolicyEvidence(classification, resolution.Decision, root.PolicyResolver.Fingerprint())
		if !resolution.Decision.Included {
			classification.MatchedExclude = resolution.Decision.Pattern
			return classification.with(false, ReasonExcludedFilePolicy, fmt.Sprintf("excluded by %s policy pattern %q", resolution.Decision.RuleCategory, resolution.Decision.Pattern))
		}
	} else {
		excluded, excludePattern := MatchAny(root.Config.Exclude, obs.RelativePath)
		if excluded {
			classification.MatchedExclude = excludePattern
			return classification.with(false, ReasonExcludedByPattern, fmt.Sprintf("excluded by pattern %q", excludePattern))
		}
	}
	included, includePattern := MatchAny(root.Config.Include, obs.RelativePath)
	if !included {
		return classification.with(false, ReasonExcludedNoInclude, "path did not match any include pattern")
	}
	classification.MatchedInclude = includePattern
	if obs.Fidelity != nil && obs.Fidelity.Kind == filesystemmeta.ObjectKindPackage && root.Config.FidelityPolicy.PackageDirectoryMode == PackageDirectoryModeObserveBoundary {
		return classification.with(false, ReasonExcludedPackageBoundary, "package directory boundary is observed but not descended into")
	}
	if obs.Kind == PathKindDirectory {
		return classification.with(false, ReasonExcludedDirectoryMetadata, "directories are traversed but not content-managed in this slice")
	}
	if obs.Kind == PathKindFile && obs.SizeBytes > root.Config.Scan.MaxHashFileBytes {
		return classification.with(false, ReasonSkippedTooLarge, "file exceeds watched-root hash size limit")
	}
	return classification.with(true, ReasonIncludedByPattern, fmt.Sprintf("included by pattern %q", includePattern))
}

func withPolicyEvidence(classification Classification, decision filepolicy.Decision, fingerprint string) Classification {
	classification.PolicyProfile = string(decision.Profile)
	classification.PolicyVersion = decision.PolicyVersion
	classification.PolicyFingerprint = fingerprint
	classification.PolicyRuleSource = string(decision.RuleCategory)
	classification.PolicyPattern = decision.Pattern
	classification.PolicySourceFile = decision.SourceFile
	classification.PolicySourceLine = decision.SourceLine
	return classification
}

func isHiddenAcceptancePath(relativePath string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(relativePath), "/") {
		if segment == ".loom-acceptance" {
			return true
		}
	}
	return false
}

func EffectivePolicies(config RootConfig, relativePath string) Policies {
	policies := Policies{
		Backup: config.BackupPolicy.Mode,
		Sync:   config.SyncPolicy.Mode,
		Index:  config.IndexPolicy.Mode,
		Delete: config.DeletePolicy.Mode,
	}
	if config.IndexPolicy.Mode == IndexModeMarkdownText && !MarkdownTextCandidate(relativePath) {
		policies.Index = IndexModeMetadataOnly
		if policies.Sync == SyncModeSelectedFiles && policies.Backup != BackupModeNone {
			policies.Sync = SyncModeNone
		}
	}
	return policies
}

func MarkdownTextCandidate(relativePath string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(relativePath)))
	switch ext {
	case ".md", ".markdown", ".mdown", ".txt", ".text", ".rst", ".adoc":
		return true
	default:
		return false
	}
}

func (c Classification) with(included bool, code, reason string) Classification {
	c.Included = included
	c.ReasonCode = code
	c.Reason = reason
	return c
}
