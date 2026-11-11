package applicationbuild

import (
	"context"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

type sourcePolicy struct {
	files []SourceFile
	rules []sourceRule
}

type sourceRule struct {
	base, pattern                    string
	negated, anchored, directoryOnly bool
}

// The general filepolicy resolver discovers an entire tree. Source snapshots
// instead read only applicable ancestor files, with the same .loomignore rule
// syntax, so private data trees are neither scanned nor staged.
func sourcePolicies(ctx context.Context, root *os.Root, names []string) (sourcePolicy, error) {
	directories := map[string]bool{".": true}
	for _, name := range names {
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			directories[directory] = true
		}
	}
	if len(directories) > 4096 {
		return sourcePolicy{}, ErrUnsafeSource
	}
	ordered := make([]string, 0, len(directories))
	for directory := range directories {
		ordered = append(ordered, directory)
	}
	sort.Slice(ordered, func(i, j int) bool {
		di, dj := strings.Count(ordered[i], "/"), strings.Count(ordered[j], "/")
		if ordered[i] == "." {
			di = -1
		}
		if ordered[j] == "." {
			dj = -1
		}
		if di != dj {
			return di < dj
		}
		return ordered[i] < ordered[j]
	})
	result := sourcePolicy{files: []SourceFile{}}
	budget := int64(1024 * 1024)
	for _, directory := range ordered {
		name := path.Join(directory, ".loomignore")
		raw, err := readSourceFile(ctx, root, name, budget)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return sourcePolicy{}, err
		}
		budget -= int64(len(raw))
		result.files = append(result.files, SourceFile{Path: name, Digest: hashBytes(raw), Bytes: int64(len(raw))})
		for _, line := range strings.Split(string(raw), "\n") {
			pattern := trimPolicySpaces(strings.TrimSuffix(line, "\r"))
			if pattern == "" || strings.HasPrefix(pattern, "#") {
				continue
			}
			rule := sourceRule{base: directory}
			if rule.base == "." {
				rule.base = ""
			}
			if strings.HasPrefix(pattern, `\!`) || strings.HasPrefix(pattern, `\#`) {
				pattern = pattern[1:]
			} else if strings.HasPrefix(pattern, "!") {
				rule.negated, pattern = true, pattern[1:]
			}
			rule.anchored = strings.HasPrefix(pattern, "/")
			pattern = strings.TrimPrefix(pattern, "/")
			rule.directoryOnly = strings.HasSuffix(pattern, "/")
			rule.pattern = strings.TrimSuffix(pattern, "/")
			if rule.pattern == "" || !doublestar.ValidatePattern(rule.pattern) || len(result.rules) >= 4096 {
				return sourcePolicy{}, ErrUnsafeSource
			}
			result.rules = append(result.rules, rule)
		}
	}
	return result, nil
}

func (p sourcePolicy) includes(name string) bool {
	included := true
	for _, rule := range p.rules {
		candidate := name
		if rule.base != "" {
			var ok bool
			candidate, ok = strings.CutPrefix(candidate, rule.base+"/")
			if !ok {
				continue
			}
		}
		parts := strings.Split(candidate, "/")
		for index := range parts {
			part := strings.Join(parts[:index+1], "/")
			if !rule.anchored && !strings.Contains(rule.pattern, "/") {
				part = parts[index]
			}
			matched, _ := doublestar.Match(rule.pattern, part)
			if matched && (!rule.directoryOnly || index < len(parts)-1) {
				included = rule.negated
				break
			}
		}
	}
	return included
}

func trimPolicySpaces(value string) string {
	for strings.HasSuffix(value, " ") {
		backslashes := 0
		for i := len(value) - 2; i >= 0 && value[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			return value[:len(value)-2] + " "
		}
		value = strings.TrimSuffix(value, " ")
	}
	return value
}
