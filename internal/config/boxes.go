package config

import "loom.local/loom/internal/boxregistry"

// BoxCatalog is loaded by service owners once per configuration/run, not per file.
func (c Config) BoxCatalog() (boxregistry.Catalog, error) {
	return boxregistry.Load(boxregistry.Options{
		File: c.BoxesFile, PrimaryPath: c.BoxPath, PrimaryStateRoot: c.BoxStateRoot,
		Node: c.NodeID, DataRoot: c.DataDir, StorageRoot: c.StorageRoot,
	})
}
