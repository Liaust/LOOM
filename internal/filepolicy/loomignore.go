package filepolicy

import "time"

type Rule struct {
	OriginalPattern   string `json:"original_pattern"`
	NormalizedPattern string `json:"normalized_pattern"`
	SourceFile        string `json:"source_file"`
	SourceLine        int    `json:"source_line"`
	ContentHash       string `json:"content_hash"`
	BaseDir           string `json:"base_dir,omitempty"`
	Negated           bool   `json:"negated,omitempty"`
	DirectoryOnly     bool   `json:"directory_only,omitempty"`
	Anchored          bool   `json:"anchored,omitempty"`
}

type PolicyFile struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	BaseDir      string `json:"base_dir,omitempty"`
	ContentHash  string `json:"content_hash"`
	Rules        []Rule `json:"rules"`
}

type ResolverOptions struct {
	DiscoverUserRules bool
	PolicyRoot        string
	ContractIncludes  []string
	ContractExcludes  []string
	// Discovery limits bound callers that inspect an arbitrary tree. Zero
	// values preserve the existing trusted-root behavior.
	MaxDiscoveryEntries int
	MaxPolicyBytes      int64
	DiscoveryDeadline   time.Time
}

type Resolution struct {
	Decision Decision   `json:"decision"`
	Trace    []Decision `json:"trace"`
}
