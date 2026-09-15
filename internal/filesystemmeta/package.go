package filesystemmeta

import (
	"io/fs"
	"path/filepath"
	"strings"
)

var packageDirectoryExtensions = map[string]string{
	".app":           "macos_application",
	".bundle":        "macos_bundle",
	".framework":     "macos_framework",
	".key":           "keynote_document",
	".logicx":        "logic_project",
	".numbers":       "numbers_document",
	".pages":         "pages_document",
	".photoslibrary": "photos_library",
	".playground":    "xcode_playground",
	".rtfd":          "rich_text_package",
	".scriv":         "scrivener_project",
	".xcarchive":     "xcode_archive",
	".xcworkspace":   "xcode_workspace",
	".xcodeproj":     "xcode_project",
}

func IsPackageDirectory(path string, info fs.FileInfo) (bool, string) {
	if info == nil || !info.IsDir() {
		return false, ""
	}
	ext := strings.ToLower(filepath.Ext(filepath.Base(path)))
	if kind, ok := packageDirectoryExtensions[ext]; ok {
		return true, kind
	}
	return false, ""
}
