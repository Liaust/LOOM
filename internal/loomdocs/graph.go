package loomdocs

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func ValidateGraph(corpus *Corpus) []GraphIssue {
	if corpus == nil {
		return []GraphIssue{{Severity: SeverityError, Kind: "empty_corpus", Message: "documentation corpus is nil"}}
	}
	if corpus.byPath == nil {
		corpus.rebuildIndexes()
	}
	issues := []GraphIssue{}
	for title, indexes := range corpus.byTitle {
		if title == "" || len(indexes) < 2 {
			continue
		}
		issues = append(issues, GraphIssue{Severity: SeverityError, Kind: "duplicate_title", Target: title, Message: fmt.Sprintf("title resolves to %d documents", len(indexes))})
	}
	for alias, indexes := range corpus.byAlias {
		if alias == "" || len(indexes) < 2 {
			continue
		}
		issues = append(issues, GraphIssue{Severity: SeverityError, Kind: "duplicate_alias", Target: alias, Message: fmt.Sprintf("alias resolves to %d documents", len(indexes))})
	}
	for alias, aliasIndexes := range corpus.byAlias {
		if titleIndexes := corpus.byTitle[alias]; len(titleIndexes) > 0 && !sameDocumentSet(aliasIndexes, titleIndexes) {
			issues = append(issues, GraphIssue{Severity: SeverityError, Kind: "ambiguous_alias", Target: alias, Message: "alias conflicts with a document title"})
		}
	}
	for _, document := range corpus.Documents {
		for _, target := range document.Related {
			matches := corpus.lookupIndexes(target)
			if len(matches) > 0 {
				continue
			}
			if directoryTarget(corpus, target) {
				issues = append(issues, GraphIssue{Severity: SeverityError, Kind: "directory_wikilink", Path: document.RelativePath, Target: target, Message: "wikilink points to a directory instead of a page"})
				continue
			}
			issues = append(issues, GraphIssue{Severity: SeverityError, Kind: "unresolved_wikilink", Path: document.RelativePath, Target: target, Message: "wikilink target was not found"})
		}
	}
	sortIssues(issues)
	return issues
}

func (c *Corpus) lookupIndexes(target string) []int {
	if c.byPath == nil {
		c.rebuildIndexes()
	}
	pathKey := normalizePathKey(target)
	pathCandidates := []string{pathKey}
	if filepath.Ext(pathKey) == "" {
		pathCandidates = append(pathCandidates, pathKey+".md", filepath.ToSlash(filepath.Join(pathKey, "README.md")))
	}
	for _, candidate := range pathCandidates {
		if indexes := c.byPath[candidate]; len(indexes) > 0 {
			return append([]int(nil), indexes...)
		}
	}
	key := normalizeLookup(target)
	indexes := append([]int(nil), c.byTitle[key]...)
	indexes = append(indexes, c.byAlias[key]...)
	return uniqueSortedInts(indexes)
}

func (c *Corpus) resolveDocument(target string) (Document, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" || filepath.IsAbs(trimmed) || !containedRelativePath(trimmed) {
		return Document{}, fmt.Errorf("documentation target must be a contained title, alias, or relative path")
	}
	indexes := c.lookupIndexes(trimmed)
	if len(indexes) == 0 {
		return Document{}, &TargetNotFoundError{Target: target}
	}
	if len(indexes) > 1 {
		candidates := make([]string, 0, len(indexes))
		for _, index := range indexes {
			candidates = append(candidates, c.Documents[index].RelativePath)
		}
		sort.Strings(candidates)
		return Document{}, &AmbiguousTargetError{Target: target, Candidates: candidates}
	}
	return c.Documents[indexes[0]], nil
}

func directoryTarget(c *Corpus, target string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(target))))
	if cleaned == "." || cleaned == "" || strings.HasSuffix(strings.TrimSpace(target), "/") {
		return true
	}
	prefix := strings.ToLower(cleaned) + "/"
	for path := range c.byPath {
		if strings.HasPrefix(path, prefix) {
			return filepath.Ext(cleaned) == ""
		}
	}
	return false
}

func sameDocumentSet(left, right []int) bool {
	left = uniqueSortedInts(left)
	right = uniqueSortedInts(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func uniqueSortedInts(values []int) []int {
	sort.Ints(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
