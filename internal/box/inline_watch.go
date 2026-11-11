package box

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/filepolicy"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
)

func isBoxWatchArea(area string) bool {
	switch area {
	case AreaNotes, AreaDocuments, AreaLaunchpad, areaTopics, areaLibrary:
		return true
	default:
		return false
	}
}

func hasAreaWatchSource(contract Contract, area string) bool {
	return contract.Areas[area].Watch != nil || strings.TrimSpace(contract.Policies[area]) != ""
}

// Inline policies inherit identity and location from their containing area,
// while retaining the same typed consumer settings as explicit policy files.
func inlineWatchPolicy(policy WatchPolicy, area string) WatchPolicy {
	policy = normalizeWatchPolicy(policy)
	if policy.SchemaVersion == "" {
		policy.SchemaVersion = WatchPolicySchemaVersion
	}
	if policy.Area == "" {
		policy.Area = area
	}
	if policy.Mode == "" {
		policy.Mode = "watched_root"
	}
	return policy
}

func defaultWatchPolicy(area string) WatchPolicy {
	policy := WatchPolicy{
		SchemaVersion: WatchPolicySchemaVersion, Area: area, Enabled: true, Mode: "watched_root",
		Catalog: WatchPolicyToggle{Enabled: true}, Backup: WatchPolicyToggle{Enabled: true},
		Delete: "local_source_authoritative",
	}
	if area == AreaNotes {
		policy.Path = "Notes"
		policy.Semantics = "knowledge_index"
		policy.Object.Enabled = true
		policy.Text.Enabled = true
		policy.Index.Enabled = true
		policy.Sync.Enabled = true
		policy.Notes = "Markdown/text enters LOOM search/database; attachments are backed up and cataloged without text indexing by default."
	} else {
		policy.Path = "Documents"
		policy.Semantics = "broad_backup"
		policy.Notes = "Documents is broad local storage backed up by main; metadata/catalog visibility is separate from text indexing."
	}
	return policy
}

func defaultAreaWatch(area string) *WatchPolicy {
	policy := defaultWatchPolicy(area)
	policy.SchemaVersion, policy.Area, policy.Path, policy.Mode = "", "", "", ""
	return &policy
}

func renderWatchPolicy(policy WatchPolicy) string {
	raw, _ := yaml.Marshal(policy)
	return string(raw)
}

// Enrichment belongs to knowledge admission, not watch configuration identity.
// Carry only the fields that change watched-root consumer decisions.
func watchConsumerFiles(files *filepolicy.FolderPolicy) (*filepolicy.FolderPolicy, error) {
	if files == nil {
		return nil, nil
	}
	normalized, err := filepolicy.NormalizeFolderPolicy(*files)
	if err != nil {
		return nil, err
	}
	policy := filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{}}
	for _, rule := range normalized.Rules {
		if rule.Search != "" || rule.Protect != nil {
			rule.Enrichment = nil
			policy.Rules = append(policy.Rules, rule)
		}
	}
	if len(policy.Rules) == 0 {
		return nil, nil
	}
	return &policy, nil
}

func independentBoxConsumers(config agentwatchedroots.RootConfig, files *filepolicy.FolderPolicy) agentwatchedroots.RootConfig {
	config.SchemaVersion = agentwatchedroots.IndependentConfigSchemaVersion
	config.Files = files
	if config.IndexPolicy.Mode == agentwatchedroots.IndexModeMarkdownText && config.BackupPolicy.Mode != agentwatchedroots.BackupModeNone && config.SyncPolicy.Mode == agentwatchedroots.SyncModeSelectedFiles {
		// Preserve the legacy text-only delivery selection explicitly. Search and
		// protection exceptions must never broaden device delivery for attachments.
		patterns := []string{}
		for _, extension := range []string{"md", "markdown", "mdown", "txt", "text", "rst", "adoc"} {
			var pattern strings.Builder
			pattern.WriteString("**/*.")
			for _, letter := range extension {
				fmt.Fprintf(&pattern, "[%c%c]", letter, letter-'a'+'A')
			}
			patterns = append(patterns, pattern.String())
		}
		config.ConsumerSelections = &agentwatchedroots.ConsumerSelections{Sync: &agentwatchedroots.PathSelection{Include: patterns, Exclude: []string{}}}
	}
	return config
}
