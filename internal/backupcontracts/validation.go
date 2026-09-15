package backupcontracts

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

var contractKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$`)

func Normalize(contract Contract) Contract {
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Key = strings.ToLower(strings.TrimSpace(contract.Key))
	contract.DisplayName = strings.TrimSpace(contract.DisplayName)
	contract.OwnerNode = strings.TrimSpace(contract.OwnerNode)
	contract.Status = strings.ToLower(strings.TrimSpace(contract.Status))
	contract.Target.Scope = strings.ToLower(strings.TrimSpace(contract.Target.Scope))
	contract.Target.Path = normalizePath(contract.Target.Path)
	if contract.SchemaVersion == "" {
		contract.SchemaVersion = SchemaVersion
	}
	if contract.Status == "" {
		contract.Status = StatusActive
	}
	if contract.Target.Scope == "" {
		if filepath.IsAbs(contract.Target.Path) {
			contract.Target.Scope = TargetScopeOwnerNodeAbsolute
		} else {
			contract.Target.Scope = TargetScopeBoxRelative
		}
	}
	contract.Include = normalizePatterns(contract.Include)
	if len(contract.Include) == 0 {
		contract.Include = []string{"**/*"}
	}
	contract.Exclude = normalizePatterns(contract.Exclude)
	if contract.SchemaVersion == LegacySchemaVersion && contract.Ignore == nil && len(contract.Exclude) == 0 {
		contract.Exclude = DefaultExcludePatterns()
	}
	if contract.SchemaVersion == SchemaVersion {
		if contract.Ignore == nil {
			contract.Ignore = &IgnorePolicy{Profile: string(filepolicy.ProfileManaged), DiscoverUserRules: true}
		} else {
			contract.Ignore.Profile = strings.ToLower(strings.TrimSpace(contract.Ignore.Profile))
		}
	}
	contract.Backup.Mode = strings.ToLower(strings.TrimSpace(contract.Backup.Mode))
	if contract.Backup.Mode == "" {
		contract.Backup.Mode = BackupModeIncrementalRaw
	}
	if contract.Backup.Mode != BackupModeNone {
		if contract.Backup.MaxFileBytes <= 0 {
			contract.Backup.MaxFileBytes = DefaultMaxFileBytes
		}
		if contract.Backup.MaxBatchBytes <= 0 {
			contract.Backup.MaxBatchBytes = DefaultMaxBatchBytes
		}
	}
	contract.Metadata.CreatedAt = strings.TrimSpace(contract.Metadata.CreatedAt)
	contract.Metadata.UpdatedAt = strings.TrimSpace(contract.Metadata.UpdatedAt)
	contract.Metadata.CreatedBy = strings.TrimSpace(contract.Metadata.CreatedBy)
	contract.Metadata.DisabledAt = strings.TrimSpace(contract.Metadata.DisabledAt)
	contract.Metadata.DisabledBy = strings.TrimSpace(contract.Metadata.DisabledBy)
	return contract
}

func Validate(contract Contract) error {
	if contract.SchemaVersion != SchemaVersion && contract.SchemaVersion != LegacySchemaVersion {
		return fmt.Errorf("backup contract schema_version must be %q or legacy %q", SchemaVersion, LegacySchemaVersion)
	}
	if err := ValidateKey(contract.Key); err != nil {
		return err
	}
	switch contract.Status {
	case StatusActive, StatusDisabled:
	default:
		return fmt.Errorf("backup contract status must be %q or %q", StatusActive, StatusDisabled)
	}
	if err := validateTarget(contract.Target); err != nil {
		return err
	}
	if len(contract.Include) == 0 {
		return fmt.Errorf("backup contract include must contain at least one pattern")
	}
	for _, pattern := range contract.Include {
		if err := validatePattern("include", pattern); err != nil {
			return err
		}
	}
	for _, pattern := range contract.Exclude {
		if err := validatePattern("exclude", pattern); err != nil {
			return err
		}
	}
	if contract.SchemaVersion == SchemaVersion {
		if contract.Ignore == nil {
			return fmt.Errorf("backup contract ignore policy is required")
		}
		profile, err := filepolicy.ParseProfile(contract.Ignore.Profile)
		if err != nil {
			return err
		}
		if profile != filepolicy.ProfileManaged {
			return fmt.Errorf("backup contract ignore profile must be %q", filepolicy.ProfileManaged)
		}
	}
	switch contract.Backup.Mode {
	case BackupModeIncrementalRaw, BackupModeMetadataOnly:
		if contract.Backup.MaxFileBytes <= 0 {
			return fmt.Errorf("backup contract max_file_bytes must be positive when backup mode is enabled")
		}
		if contract.Backup.MaxBatchBytes <= 0 {
			return fmt.Errorf("backup contract max_batch_bytes must be positive when backup mode is enabled")
		}
	case BackupModeNone:
	default:
		return fmt.Errorf("backup contract backup.mode must be %q, %q, or %q", BackupModeIncrementalRaw, BackupModeMetadataOnly, BackupModeNone)
	}
	if contract.Status == StatusActive && contract.Metadata.DisabledAt != "" {
		return fmt.Errorf("backup contract disabled_at must be empty when status is active")
	}
	return nil
}

func ValidateKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("backup contract key is required")
	}
	if !contractKeyPattern.MatchString(key) {
		return fmt.Errorf("backup contract key must be lowercase alphanumeric with optional hyphen or underscore separators")
	}
	return nil
}

func DefaultExcludePatterns() []string {
	patterns := filepolicy.DefaultExcludePatterns()
	patterns = append(patterns,
		".loom",
		".loom/**",
		"**/.loom",
		"**/.loom/**",
	)
	return uniqueStrings(patterns)
}

func validateTarget(target TargetSpec) error {
	if strings.TrimSpace(target.Path) == "" {
		return fmt.Errorf("backup contract target.path is required")
	}
	if strings.ContainsRune(target.Path, '\x00') {
		return fmt.Errorf("backup contract target.path contains NUL")
	}
	switch target.Scope {
	case TargetScopeOwnerNodeAbsolute:
		if !filepath.IsAbs(target.Path) {
			return fmt.Errorf("backup contract target.path must be absolute for %s scope", TargetScopeOwnerNodeAbsolute)
		}
		clean := filepath.Clean(target.Path)
		if clean == "." || clean == string(filepath.Separator) {
			return fmt.Errorf("backup contract target.path must point below the filesystem root")
		}
	case TargetScopeBoxRelative:
		if filepath.IsAbs(target.Path) || strings.HasPrefix(target.Path, "/") {
			return fmt.Errorf("backup contract target.path must be relative for %s scope", TargetScopeBoxRelative)
		}
		for _, segment := range strings.Split(target.Path, "/") {
			if segment == ".." {
				return fmt.Errorf("backup contract target.path must not contain ..")
			}
		}
		clean := filepath.ToSlash(filepath.Clean(target.Path))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("backup contract target.path must resolve inside the box")
		}
	default:
		return fmt.Errorf("backup contract target.scope must be %q or %q", TargetScopeOwnerNodeAbsolute, TargetScopeBoxRelative)
	}
	return nil
}

func validatePattern(kind string, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return fmt.Errorf("backup contract %s pattern is required", kind)
	}
	if strings.ContainsRune(pattern, '\x00') {
		return fmt.Errorf("backup contract %s pattern contains NUL", kind)
	}
	return nil
}

func normalizePatterns(values []string) []string {
	normalized := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	return normalized
}

func normalizePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.ToSlash(filepath.Clean(filepath.ToSlash(value)))
}

func uniqueStrings(values []string) []string {
	unique := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}
