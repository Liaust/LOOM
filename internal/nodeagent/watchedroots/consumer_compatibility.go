package watchedroots

import "slices"

// LegacyTextSelection spells out the old backup-coupled text transport filter.
// It is an adapter, not a new device or indexing default.
func LegacyTextSelection() []string {
	patterns := []string{}
	for _, extension := range []string{"md", "markdown", "mdown", "txt", "text", "rst", "adoc"} {
		pattern := "**/*."
		for _, letter := range extension {
			pattern += "[" + string(letter) + string(letter-'a'+'A') + "]"
		}
		patterns = append(patterns, pattern)
	}
	slices.Sort(patterns)
	return patterns
}

func legacyEquivalentConsumerConfig(config RootConfig) RootConfig {
	if config.SchemaVersion != IndependentConfigSchemaVersion || config.Files != nil {
		return config
	}
	coupled := config.IndexPolicy.Mode == IndexModeMarkdownText && config.BackupPolicy.Mode != BackupModeNone && config.SyncPolicy.Mode == SyncModeSelectedFiles
	if config.ConsumerSelections == nil && !coupled {
		config.SchemaVersion = ConfigSchemaVersion
		return config
	}
	if !coupled {
		return config
	}
	selections := config.ConsumerSelections
	if selections == nil || selections.Backup != nil || selections.Index != nil || selections.Sync == nil ||
		len(selections.Sync.Exclude) != 0 || !slices.Equal(selections.Sync.Include, LegacyTextSelection()) {
		return config
	}
	config.SchemaVersion, config.ConsumerSelections = ConfigSchemaVersion, nil
	return config
}
