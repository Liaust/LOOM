package watchedroots

// ContentConfigHash is coverage identity, not the exact configuration receipt.
// Operational pacing/budgets do not turn unchanged source data into new work.
func ContentConfigHash(config RootConfig) string {
	config = legacyEquivalentConsumerConfig(NormalizeRootConfig(config))
	config.DisplayName = ""
	config.Watch.DebounceWindow = ""
	config.Scan.FullRescanInterval = ""
	config.Scan.MaxRuntime = ""
	config.Scan.MaxFilesPerRun = 0
	config.Scan.MaxDirsPerRun = 0
	config.Scan.MaxHashBytesPerRun = 0
	config.Scan.StabilityWindow = ""
	return ConfigHash(config)
}

func coverageConfigMatches(root ValidatedRoot, checkpoint RootCheckpoint) bool {
	return checkpoint.ConfigHash == root.ConfigHash || checkpoint.ContentConfigHash != "" &&
		checkpoint.ContentConfigHash == ContentConfigHash(root.Config)
}
