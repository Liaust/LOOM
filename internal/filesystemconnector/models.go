package filesystemconnector

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ProviderKey          = "filesystem"
	DefaultMaxFileBytes  = int64(1024 * 1024)
	ConnectorNamespace   = "filesystem"
	EndpointSafeList     = "safe_list"
	EndpointReadMetadata = "read_metadata"
	EndpointIngestFile   = "ingest_file"
)

var rootKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

type Config struct {
	SafeRoots []SafeRoot `json:"safe_roots,omitempty"`
}

type SafeRoot struct {
	RootKey              string          `json:"root_key"`
	DisplayName          string          `json:"display_name"`
	AbsolutePath         string          `json:"absolute_path"`
	AllowList            bool            `json:"allow_list"`
	AllowMetadata        bool            `json:"allow_metadata"`
	AllowIngest          bool            `json:"allow_ingest"`
	MaxFileBytes         int64           `json:"max_file_bytes"`
	IncludeHiddenDefault bool            `json:"include_hidden_default"`
	PrivateBackupOnly    bool            `json:"private_backup_only"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

type SafeRootSummary struct {
	RootKey              string          `json:"root_key"`
	DisplayName          string          `json:"display_name"`
	AllowList            bool            `json:"allow_list"`
	AllowMetadata        bool            `json:"allow_metadata"`
	AllowIngest          bool            `json:"allow_ingest"`
	MaxFileBytes         int64           `json:"max_file_bytes"`
	IncludeHiddenDefault bool            `json:"include_hidden_default"`
	PrivateBackupOnly    bool            `json:"private_backup_only"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

func DefaultSafeRoot(rootKey, absolutePath string) SafeRoot {
	root := SafeRoot{
		RootKey:       rootKey,
		AbsolutePath:  absolutePath,
		AllowList:     true,
		AllowMetadata: true,
		AllowIngest:   true,
		MaxFileBytes:  DefaultMaxFileBytes,
	}
	return NormalizeSafeRoot(root)
}

func NormalizeConfig(config Config) Config {
	roots := make([]SafeRoot, 0, len(config.SafeRoots))
	for _, root := range config.SafeRoots {
		roots = append(roots, NormalizeSafeRoot(root))
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return roots[i].RootKey < roots[j].RootKey
	})
	config.SafeRoots = roots
	return config
}

func NormalizeSafeRoot(root SafeRoot) SafeRoot {
	root.RootKey = NormalizeRootKey(root.RootKey)
	root.DisplayName = strings.TrimSpace(root.DisplayName)
	root.AbsolutePath = strings.TrimSpace(root.AbsolutePath)
	if root.AbsolutePath != "" {
		root.AbsolutePath = filepath.Clean(root.AbsolutePath)
	}
	if root.DisplayName == "" && root.RootKey != "" {
		root.DisplayName = strings.ReplaceAll(root.RootKey, "_", " ")
	}
	if root.MaxFileBytes <= 0 {
		root.MaxFileBytes = DefaultMaxFileBytes
	}
	root.Metadata = normalizeOptionalObject(root.Metadata)
	return root
}

func NormalizeRootKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	out := strings.Trim(builder.String(), "-_")
	if out == "" {
		return ""
	}
	if out[0] < 'a' || out[0] > 'z' {
		out = "root-" + out
	}
	return out
}

func ValidateConfig(config Config) error {
	config = NormalizeConfig(config)
	seen := map[string]struct{}{}
	for _, root := range config.SafeRoots {
		if err := ValidateSafeRoot(root); err != nil {
			return err
		}
		if _, exists := seen[root.RootKey]; exists {
			return fmt.Errorf("duplicate filesystem safe root %q", root.RootKey)
		}
		seen[root.RootKey] = struct{}{}
	}
	return nil
}

func ValidateSafeRoot(root SafeRoot) error {
	root = NormalizeSafeRoot(root)
	if root.RootKey == "" {
		return errors.New("filesystem safe root root_key is required")
	}
	if !rootKeyPattern.MatchString(root.RootKey) {
		return fmt.Errorf("filesystem safe root %q must be a lowercase slug", root.RootKey)
	}
	if root.AbsolutePath == "" {
		return fmt.Errorf("filesystem safe root %q absolute_path is required", root.RootKey)
	}
	if !filepath.IsAbs(root.AbsolutePath) {
		return fmt.Errorf("filesystem safe root %q absolute_path must be absolute", root.RootKey)
	}
	if root.MaxFileBytes <= 0 {
		return fmt.Errorf("filesystem safe root %q max_file_bytes must be positive", root.RootKey)
	}
	if len(root.Metadata) > 0 && !json.Valid(root.Metadata) {
		return fmt.Errorf("filesystem safe root %q metadata must be valid JSON", root.RootKey)
	}
	if len(root.Metadata) > 0 && root.Metadata[0] != '{' {
		return fmt.Errorf("filesystem safe root %q metadata must be a JSON object", root.RootKey)
	}
	return nil
}

func PublicSummaries(config Config) []SafeRootSummary {
	config = NormalizeConfig(config)
	summaries := make([]SafeRootSummary, 0, len(config.SafeRoots))
	for _, root := range config.SafeRoots {
		summaries = append(summaries, SafeRootSummary{
			RootKey:              root.RootKey,
			DisplayName:          root.DisplayName,
			AllowList:            root.AllowList,
			AllowMetadata:        root.AllowMetadata,
			AllowIngest:          root.AllowIngest,
			MaxFileBytes:         root.MaxFileBytes,
			IncludeHiddenDefault: root.IncludeHiddenDefault,
			PrivateBackupOnly:    root.PrivateBackupOnly,
			Metadata:             root.Metadata,
		})
	}
	return summaries
}

func AvailableSafeRoots(config Config) []SafeRoot {
	config = NormalizeConfig(config)
	roots := make([]SafeRoot, 0, len(config.SafeRoots))
	for _, root := range config.SafeRoots {
		if root.PrivateBackupOnly {
			continue
		}
		if !root.AllowList && !root.AllowMetadata && !root.AllowIngest {
			continue
		}
		roots = append(roots, root)
	}
	return roots
}

func FindSafeRoot(config Config, rootKey string) (SafeRoot, bool) {
	config = NormalizeConfig(config)
	key := NormalizeRootKey(rootKey)
	for _, root := range config.SafeRoots {
		if root.RootKey == key {
			return root, true
		}
	}
	return SafeRoot{}, false
}

func UpsertSafeRoot(config Config, root SafeRoot) Config {
	config = NormalizeConfig(config)
	root = NormalizeSafeRoot(root)
	replaced := false
	for i, existing := range config.SafeRoots {
		if existing.RootKey == root.RootKey {
			config.SafeRoots[i] = root
			replaced = true
			break
		}
	}
	if !replaced {
		config.SafeRoots = append(config.SafeRoots, root)
	}
	return NormalizeConfig(config)
}

func RemoveSafeRoot(config Config, rootKey string) Config {
	config = NormalizeConfig(config)
	key := NormalizeRootKey(rootKey)
	roots := make([]SafeRoot, 0, len(config.SafeRoots))
	for _, root := range config.SafeRoots {
		if root.RootKey != key {
			roots = append(roots, root)
		}
	}
	config.SafeRoots = roots
	return config
}

func normalizeOptionalObject(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}
