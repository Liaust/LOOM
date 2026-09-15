package loomdocs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func Load(root ResolvedRoot) (*Corpus, error) {
	if strings.TrimSpace(root.Path) == "" {
		return nil, fmt.Errorf("documentation root path is required")
	}
	absoluteRoot, err := filepath.Abs(filepath.Clean(root.Path))
	if err != nil {
		return nil, fmt.Errorf("resolve documentation root: %w", err)
	}
	root.Path = absoluteRoot
	corpus := &Corpus{Root: root}
	paths := []string{}
	err = filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(absoluteRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() && excludedDocumentationDirectory(relative) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk documentation root: %w", err)
	}
	sort.Strings(paths)

	corpusHasher := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(absoluteRoot, path)
		if err != nil {
			return nil, err
		}
		relative = filepath.ToSlash(relative)
		if !containedRelativePath(relative) {
			corpus.Issues = append(corpus.Issues, GraphIssue{Severity: SeverityError, Kind: "path_escape", Path: relative, Message: "documentation path escapes the corpus root"})
			continue
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			corpus.Issues = append(corpus.Issues, GraphIssue{Severity: SeverityError, Kind: "read_error", Path: relative, Message: err.Error()})
			continue
		}
		fileSum := sha256.Sum256(payload)
		fmt.Fprintf(corpusHasher, "%s\x00%s\x00", relative, hex.EncodeToString(fileSum[:]))
		metadata, body, err := parseFrontmatter(payload)
		if err != nil {
			corpus.Issues = append(corpus.Issues, GraphIssue{Severity: SeverityError, Kind: "parse_error", Path: relative, Message: err.Error()})
			continue
		}
		if missing := validateMetadata(metadata); len(missing) > 0 {
			corpus.Issues = append(corpus.Issues, GraphIssue{Severity: SeverityError, Kind: "missing_metadata", Path: relative, Message: "missing or invalid fields: " + strings.Join(missing, ", ")})
		}
		document := Document{
			RelativePath: relative,
			Title:        metadata.Title,
			Aliases:      append([]string(nil), metadata.Aliases...),
			Description:  metadata.Description,
			Audience:     append([]string(nil), metadata.Audience...),
			Tags:         append([]string(nil), metadata.Tags...),
			Status:       metadata.Status,
			VerifiedAt:   metadata.VerifiedAt,
			SourceScope:  append([]string(nil), metadata.SourceScope...),
			Related:      extractWikilinks(append([]string{body}, metadata.Related...)...),
			Headings:     parseHeadings(body),
			Body:         body,
			FileHash:     hex.EncodeToString(fileSum[:]),
		}
		corpus.Documents = append(corpus.Documents, document)
	}
	corpus.CorpusHash = hex.EncodeToString(corpusHasher.Sum(nil))
	corpus.rebuildIndexes()
	corpus.Issues = append(corpus.Issues, ValidateGraph(corpus)...)
	sortIssues(corpus.Issues)
	return corpus, nil
}

func excludedDocumentationDirectory(relative string) bool {
	return relative == "development/templates"
}

func containedRelativePath(relative string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	return cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../") && !filepath.IsAbs(relative)
}

func (c *Corpus) rebuildIndexes() {
	c.byPath = map[string][]int{}
	c.byTitle = map[string][]int{}
	c.byAlias = map[string][]int{}
	for index, document := range c.Documents {
		c.byPath[normalizePathKey(document.RelativePath)] = append(c.byPath[normalizePathKey(document.RelativePath)], index)
		c.byTitle[normalizeLookup(document.Title)] = append(c.byTitle[normalizeLookup(document.Title)], index)
		for _, alias := range document.Aliases {
			c.byAlias[normalizeLookup(alias)] = append(c.byAlias[normalizeLookup(alias)], index)
		}
	}
}

func normalizeLookup(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizePathKey(value string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))))
}

func sortIssues(issues []GraphIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		left := issues[i].Kind + "\x00" + issues[i].Path + "\x00" + issues[i].Target + "\x00" + issues[i].Message
		right := issues[j].Kind + "\x00" + issues[j].Path + "\x00" + issues[j].Target + "\x00" + issues[j].Message
		return left < right
	})
}
