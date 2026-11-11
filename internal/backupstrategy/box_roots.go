package backupstrategy

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"loom.local/loom/internal/boxregistry"
)

var backupBoxIDPattern = regexp.MustCompile(`^box_[A-Za-z0-9][A-Za-z0-9_-]{1,100}$`)

// BoxArchiveRootName is independent of the mutable workspace key and label.
// Personal's historical names are deliberately not handled here.
func BoxArchiveRootName(id, kind string) (string, error) {
	if !backupBoxIDPattern.MatchString(id) || (kind != "source" && kind != "state" && kind != "archive") {
		return "", fmt.Errorf("invalid Box archive identity")
	}
	return id + "_" + kind, nil
}

func IsBoxArchiveRoot(name string) bool {
	for _, kind := range []string{"source", "state", "archive"} {
		id := strings.TrimSuffix(name, "_"+kind)
		if expected, err := BoxArchiveRootName(id, kind); err == nil && expected == name {
			return true
		}
	}
	return false
}

// AdditionalBoxArchiveRoots describes candidates, not proof of capture. Missing
// optional state/archive directories are omitted by the existing cloud selector.
func AdditionalBoxArchiveRoots(catalog boxregistry.Catalog) ([]DirectArchiveRoot, error) {
	var roots []DirectArchiveRoot
	for _, box := range catalog.Boxes {
		if box.Primary || !box.Available {
			continue
		}
		for _, item := range []struct{ kind, path string }{{"source", box.RootPath}, {"state", box.StateRoot}, {"archive", box.ArchiveRoot}} {
			if item.path == "" && item.kind == "archive" {
				continue
			}
			name, err := BoxArchiveRootName(box.ID, item.kind)
			if err != nil {
				return nil, err
			}
			if !filepath.IsAbs(item.path) || filepath.Clean(item.path) != item.path || item.path == "/" {
				return nil, fmt.Errorf("invalid Box archive path")
			}
			roots = append(roots, DirectArchiveRoot{Name: name, Path: item.path})
		}
	}
	return roots, nil
}
