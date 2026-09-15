package supportbundle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type PathAlias struct {
	Label string `json:"label"`
	Root  string `json:"root"`
}

func DefaultPathAliases() []PathAlias {
	aliases := []PathAlias{}
	home, _ := os.UserHomeDir()
	addAlias := func(label, root string) {
		if strings.TrimSpace(root) == "" {
			return
		}
		aliases = append(aliases, PathAlias{Label: label, Root: root})
	}
	if home != "" {
		addAlias("$LOOM_BOX", firstEnv("LOOM_BOX", filepath.Join(home, "loom-box")))
		addAlias("$LOOM_STORAGE", firstEnv("LOOM_STORAGE", filepath.Join(home, "loom-storage")))
		addAlias("$HOME", home)
	}
	addAlias("$LOOM_MAIN_BOX", firstEnv("LOOM_MAIN_BOX", "/home/loomadmin/loom-box"))
	addAlias("$LOOM_MAIN_STORAGE", firstEnv("LOOM_MAIN_STORAGE", "/home/loomadmin/loom-storage"))
	addAlias("$LOOM_DATA", firstEnv("LOOM_DATA", firstEnv("LOOM_DATA_DIR", "/var/lib/loom")))
	addAlias("$TMPDIR", os.TempDir())
	return NormalizePathAliases(aliases)
}

func ProjectPathAlias(slug, root string) PathAlias {
	return PathAlias{Label: "$PROJECT/" + strings.Trim(strings.TrimSpace(slug), "/"), Root: root}
}

func NotesRootPathAlias(label, root string) PathAlias {
	return PathAlias{Label: "$NOTES_ROOT/" + strings.Trim(strings.TrimSpace(label), "/"), Root: root}
}

func NormalizePathAliases(input []PathAlias) []PathAlias {
	aliases := make([]PathAlias, 0, len(input))
	seen := map[string]bool{}
	for _, alias := range input {
		label := strings.TrimSpace(alias.Label)
		root := cleanRoot(alias.Root)
		if label == "" || root == "" || seen[label+"\x00"+root] {
			continue
		}
		seen[label+"\x00"+root] = true
		aliases = append(aliases, PathAlias{Label: label, Root: root})
	}
	sort.SliceStable(aliases, func(i, j int) bool {
		if len(aliases[i].Root) == len(aliases[j].Root) {
			return aliases[i].Label < aliases[j].Label
		}
		return len(aliases[i].Root) > len(aliases[j].Root)
	})
	return aliases
}

func AliasPath(value string, aliases []PathAlias, includeAbsolutePaths bool) (string, bool) {
	if includeAbsolutePaths {
		return value, false
	}
	clean := cleanRoot(value)
	if clean == "" {
		return value, false
	}
	for _, alias := range NormalizePathAliases(aliases) {
		if clean == alias.Root {
			return alias.Label, true
		}
		prefix := alias.Root + string(filepath.Separator)
		if strings.HasPrefix(clean, prefix) {
			rel, err := filepath.Rel(alias.Root, clean)
			if err != nil || rel == "." {
				return alias.Label, true
			}
			return alias.Label + "/" + filepath.ToSlash(rel), true
		}
	}
	return value, false
}

func AliasText(value string, aliases []PathAlias, includeAbsolutePaths bool) (string, int) {
	if includeAbsolutePaths || value == "" {
		return value, 0
	}
	count := 0
	for _, alias := range NormalizePathAliases(aliases) {
		for _, root := range aliasRoots(alias.Root) {
			if root == "" || !strings.Contains(value, root) {
				continue
			}
			var hits int
			value, hits = replaceRootInText(value, root, alias.Label)
			count += hits
		}
	}
	return value, count
}

func firstEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func cleanRoot(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			value = filepath.Join(home, strings.TrimPrefix(value, "~"+string(filepath.Separator)))
		}
	}
	return filepath.Clean(value)
}

func aliasRoots(root string) []string {
	root = cleanRoot(root)
	if root == "" {
		return nil
	}
	slash := filepath.ToSlash(root)
	if slash == root {
		return []string{root}
	}
	return []string{root, slash}
}

func replaceRootInText(value, root, label string) (string, int) {
	var builder strings.Builder
	count := 0
	cursor := 0
	for {
		relative := strings.Index(value[cursor:], root)
		if relative < 0 {
			builder.WriteString(value[cursor:])
			break
		}
		start := cursor + relative
		end := start + len(root)
		if !isPathBoundaryBefore(value, start) || !isPathBoundaryAfter(value, end) {
			builder.WriteString(value[cursor:end])
			cursor = end
			continue
		}
		builder.WriteString(value[cursor:start])
		builder.WriteString(label)
		count++
		cursor = end
	}
	return builder.String(), count
}

func isPathBoundaryBefore(value string, index int) bool {
	if index <= 0 {
		return true
	}
	switch value[index-1] {
	case '=', ':', ' ', '\n', '\r', '\t', '"', '\'', '(', '[', '{', ',', ';':
		return true
	default:
		return false
	}
}

func isPathBoundaryAfter(value string, index int) bool {
	if index >= len(value) {
		return true
	}
	switch value[index] {
	case '/', '\\', ' ', '\n', '\r', '\t', '"', '\'', ')', ']', '}', ',', ';', ':':
		return true
	default:
		return false
	}
}
