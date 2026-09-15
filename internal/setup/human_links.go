package setup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/box"
)

const (
	humanLinkKindBox = "box"

	humanLinkIntentSymlink                = "symlink"
	humanLinkIntentRemoveBrokenSymlink    = "remove_broken_symlink"
	humanLinkIntentRemoveObsoleteArtifact = "remove_obsolete_artifact"
)

func planHumanLinks(spec SetupSpec, paths PathPlan) []HumanLinkPlan {
	if spec.NodeKind != "main" {
		return nil
	}
	links := []HumanLinkPlan{}
	for _, home := range mainHumanHomePaths(spec) {
		mainBoxPath := filepath.Join(home, box.DefaultMainBoxLinkName)
		links = append(links, HumanLinkPlan{
			Key:        "main_box_" + sanitizeID(filepath.Base(home)),
			Kind:       humanLinkKindBox,
			Intent:     humanLinkIntentSymlink,
			Label:      box.DefaultMainBoxLinkName,
			LinkPath:   mainBoxPath,
			TargetPath: paths.BoxPath,
			Required:   false,
		})
	}
	return links
}

func mainHumanHomePaths(spec SetupSpec) []string {
	home := filepath.Clean(strings.TrimSpace(spec.HomeDir))
	if home == "" || home == "." {
		return nil
	}
	out := []string{home}
	parent := filepath.Dir(home)
	if parent != "" && parent != "." && filepath.Base(home) != "loomdesk" {
		out = append(out, filepath.Join(parent, "loomdesk"))
	}
	return uniqueNonEmptyPaths(out)
}

func uniqueNonEmptyPaths(paths []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" || path == "." || seen[path] {
			continue
		}
		out = append(out, path)
		seen[path] = true
	}
	return out
}

func applyHumanLinks(plan SetupPlan, dryRun bool) (applyChanges, error) {
	out := applyChanges{}
	if plan.Spec.NodeKind != "main" {
		return out, nil
	}
	for _, link := range plan.Paths.HumanLinks {
		var change ApplyChange
		var err error
		switch link.Intent {
		case humanLinkIntentSymlink:
			change, err = ensureHumanSymlinkChange(link, dryRun)
		default:
			change = ApplyChange{ID: "human_link_" + sanitizeID(link.Key), Category: "human_links", Status: ApplyStatusSkipped, Path: link.LinkPath, Message: "unsupported human link intent " + link.Intent}
		}
		if err != nil {
			return out, err
		}
		appendToApplyChanges(&out, change)
	}
	return out, nil
}

func ensureHumanSymlinkChange(link HumanLinkPlan, dryRun bool) (ApplyChange, error) {
	link.LinkPath = filepath.Clean(strings.TrimSpace(link.LinkPath))
	link.TargetPath = filepath.Clean(strings.TrimSpace(link.TargetPath))
	id := "human_link_" + sanitizeID(link.Key)
	if link.LinkPath == "" || link.LinkPath == "." || link.TargetPath == "" || link.TargetPath == "." {
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusSkipped, Path: link.LinkPath, Message: "link path or target path is not configured"}, nil
	}
	if current, err := os.Readlink(link.LinkPath); err == nil && filepath.Clean(current) == link.TargetPath {
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusAlreadySatisfied, Path: link.LinkPath, Message: link.TargetPath}, nil
	}
	if info, err := os.Lstat(link.LinkPath); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusBlocked, Path: link.LinkPath, Message: "path exists and is not a symlink"}, nil
	} else if err != nil && !os.IsNotExist(err) {
		if os.IsPermission(err) {
			return inaccessibleHumanLinkChange(id, link.LinkPath, err), nil
		}
		return ApplyChange{}, err
	}
	if dryRun {
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusWouldChange, Path: link.LinkPath, Message: link.TargetPath}, nil
	}
	if err := os.MkdirAll(filepath.Dir(link.LinkPath), 0o755); err != nil {
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusBlocked, Path: link.LinkPath, Message: err.Error()}, nil
	}
	tmp := link.LinkPath + ".tmp-" + fmt.Sprintf("%d", time.Now().UnixNano())
	_ = os.Remove(tmp)
	if err := os.Symlink(link.TargetPath, tmp); err != nil {
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusBlocked, Path: link.LinkPath, Message: err.Error()}, nil
	}
	if err := os.Rename(tmp, link.LinkPath); err != nil {
		_ = os.Remove(tmp)
		return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusBlocked, Path: link.LinkPath, Message: err.Error()}, nil
	}
	return ApplyChange{ID: id, Category: "human_links", Status: ApplyStatusChanged, Path: link.LinkPath, Message: link.TargetPath}, nil
}

func inaccessibleHumanLinkChange(id, path string, err error) ApplyChange {
	return ApplyChange{
		ID:       id,
		Category: "human_links",
		Status:   ApplyStatusSkipped,
		Path:     path,
		Message:  "not accessible from current user: " + err.Error(),
	}
}

func isDirEmpty(path string) (bool, error) {
	dir, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	_, err = dir.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

func humanLinkStatuses(links []HumanLinkPlan) []HumanLinkStatus {
	out := make([]HumanLinkStatus, 0, len(links))
	for _, link := range links {
		out = append(out, humanLinkStatus(link))
	}
	return out
}

func humanLinkStatus(link HumanLinkPlan) HumanLinkStatus {
	status := HumanLinkStatus{
		Key:        link.Key,
		Kind:       link.Kind,
		Intent:     link.Intent,
		Label:      link.Label,
		LinkPath:   link.LinkPath,
		TargetPath: link.TargetPath,
		Required:   link.Required,
		Status:     "missing",
	}
	info, err := os.Lstat(link.LinkPath)
	if os.IsNotExist(err) {
		if link.Intent == humanLinkIntentSymlink {
			status.Status = "missing"
		} else {
			status.Status = "absent"
		}
		return status
	}
	if err != nil {
		if os.IsPermission(err) {
			status.Status = "inaccessible"
		} else {
			status.Status = "error"
		}
		status.Message = err.Error()
		return status
	}
	status.Exists = true
	status.IsSymlink = info.Mode()&os.ModeSymlink != 0
	if link.Intent == humanLinkIntentSymlink {
		return desiredHumanSymlinkStatus(status, link)
	}
	return obsoleteHumanLinkStatus(status, link, info)
}

func desiredHumanSymlinkStatus(status HumanLinkStatus, link HumanLinkPlan) HumanLinkStatus {
	status.TargetExists = pathExists(link.TargetPath)
	if !status.IsSymlink {
		status.Status = "wrong_type"
		status.Message = "path exists and is not a symlink"
		return status
	}
	target, err := os.Readlink(link.LinkPath)
	if err != nil {
		if os.IsPermission(err) {
			status.Status = "inaccessible"
		} else {
			status.Status = "error"
		}
		status.Message = err.Error()
		return status
	}
	if filepath.Clean(target) != filepath.Clean(link.TargetPath) {
		status.Status = "wrong_target"
		status.Message = target
		return status
	}
	if !status.TargetExists {
		status.Status = "target_missing"
		return status
	}
	status.Status = "present"
	return status
}

func obsoleteHumanLinkStatus(status HumanLinkStatus, link HumanLinkPlan, info os.FileInfo) HumanLinkStatus {
	if status.IsSymlink {
		target, err := os.Readlink(link.LinkPath)
		if os.IsPermission(err) {
			status.Status = "inaccessible"
			status.Message = err.Error()
			return status
		}
		if err == nil {
			status.Message = target
		}
		if link.Intent == humanLinkIntentRemoveObsoleteArtifact {
			status.Status = "obsolete_symlink"
			return status
		}
		if err == nil && !pathExists(filepath.Clean(target)) {
			status.Status = "obsolete_broken_symlink"
			return status
		}
		status.Status = "present"
		return status
	}
	if info.IsDir() && link.Intent == humanLinkIntentRemoveObsoleteArtifact {
		empty, err := isDirEmpty(link.LinkPath)
		if err != nil {
			if os.IsPermission(err) {
				status.Status = "inaccessible"
			} else {
				status.Status = "error"
			}
			status.Message = err.Error()
			return status
		}
		if empty {
			status.Status = "obsolete_empty_dir"
			return status
		}
	}
	status.Status = "unsafe_existing_path"
	return status
}

func humanLinkNeedsRepair(status HumanLinkStatus) bool {
	switch status.Intent {
	case humanLinkIntentSymlink:
		return status.Status != "present"
	case humanLinkIntentRemoveBrokenSymlink:
		return status.Status == "obsolete_broken_symlink"
	case humanLinkIntentRemoveObsoleteArtifact:
		return status.Status == "obsolete_symlink" || status.Status == "obsolete_empty_dir" || status.Status == "unsafe_existing_path" || status.Status == "error" || status.Status == "inaccessible"
	default:
		return false
	}
}

func humanLinkSeverity(status HumanLinkStatus) string {
	if status.Status == "unsafe_existing_path" || status.Status == "wrong_type" || status.Status == "error" {
		return DiagnosticError
	}
	return DiagnosticWarning
}
