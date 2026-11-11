package nodeagent

import (
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
)

func validateNodeBoxCatalogConfig(config Config) error {
	for name, value := range map[string]string{"boxes_file": config.BoxesFile, "box_catalog_data_root": config.BoxCatalogDataRoot, "box_catalog_storage_root": config.BoxCatalogStorageRoot} {
		if err := validateOptionalAbsolutePath(name, value); err != nil {
			return err
		}
	}
	if config.BoxesFile != "" && (config.BoxRootPath == "" || config.BoxStateRoot == "" || config.BoxCatalogDataRoot == "" || config.BoxCatalogStorageRoot == "") {
		return fmt.Errorf("Box catalog requires exact configured primary, state, data and storage roots")
	}
	return nil
}

func nodeBoxForPath(config Config, path string) (boxregistry.Box, error) {
	if config.BoxesFile != "" {
		catalog, err := boxregistry.Load(boxregistry.Options{File: config.BoxesFile, PrimaryPath: config.BoxRootPath, PrimaryStateRoot: config.BoxStateRoot, Node: config.NodeKey, DataRoot: config.BoxCatalogDataRoot, StorageRoot: config.BoxCatalogStorageRoot})
		if err != nil {
			return boxregistry.Box{}, err
		}
		return catalog.ForPath(path)
	}
	// Legacy nodes admit only their explicitly configured primary root.
	root, err := canonicalDirectoryPath(config.BoxRootPath)
	if err != nil {
		return boxregistry.Box{}, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return boxregistry.Box{}, fmt.Errorf("path is outside configured Box")
	}
	return boxregistry.Box{RootPath: root, RootRef: "box", StateRoot: config.BoxStateRoot, Primary: true, Available: true}, nil
}

func nodeBoxSafeRootKey(binding boxregistry.Box) string {
	return box.WatchSafeRootKey(binding.ID, binding.RootRef)
}
