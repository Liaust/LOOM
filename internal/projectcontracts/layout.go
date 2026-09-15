package projectcontracts

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func InspectProjectLayout(root string) (ProjectLayoutDiscovery, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return ProjectLayoutDiscovery{}, err
	}
	discovery := ProjectLayoutDiscovery{
		CanonicalPath: filepath.Join(rootPath, filepath.FromSlash(CanonicalRootContractPath)),
		LegacyPath:    filepath.Join(rootPath, filepath.FromSlash(LegacyRootContractPath)),
	}
	for _, item := range []struct {
		path    string
		present *bool
	}{
		{path: discovery.CanonicalPath, present: &discovery.CanonicalPresent},
		{path: discovery.LegacyPath, present: &discovery.LegacyPresent},
	} {
		path, present := item.path, item.present
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return discovery, err
		}
		if !info.Mode().IsRegular() {
			return discovery, &os.PathError{Op: "inspect project contract", Path: path, Err: os.ErrInvalid}
		}
		*present = true
	}
	return discovery, nil
}

func projectContractsSemanticallyEqual(left, right ProjectContract) bool {
	left = normalizeContractForComparison(left)
	right = normalizeContractForComparison(right)
	return reflect.DeepEqual(left, right)
}

func normalizeContractForComparison(contract ProjectContract) ProjectContract {
	contract = NormalizeContract(contract)
	if contract.Facets == nil {
		contract.Facets = map[string]bool{}
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}
