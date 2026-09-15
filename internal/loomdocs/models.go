package loomdocs

import (
	"fmt"
	"time"
)

type SourceKind string

const (
	SourceExplicit    SourceKind = "explicit"
	SourceEnvironment SourceKind = "environment"
	SourcePackaged    SourceKind = "packaged"
	SourceRepository  SourceKind = "repository"
)

type ResolvedRoot struct {
	Path           string     `json:"path"`
	Source         SourceKind `json:"source"`
	LoomVersion    string     `json:"loom_version"`
	ReleaseMatched bool       `json:"release_matched"`
}

type Heading struct {
	Title     string `json:"title"`
	Level     int    `json:"level"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
}

type Document struct {
	RelativePath string    `json:"relative_path"`
	Title        string    `json:"title"`
	Aliases      []string  `json:"aliases,omitempty"`
	Description  string    `json:"description"`
	Audience     []string  `json:"audience"`
	Tags         []string  `json:"tags"`
	Status       string    `json:"status"`
	VerifiedAt   string    `json:"verified_at,omitempty"`
	SourceScope  []string  `json:"source_scope,omitempty"`
	Related      []string  `json:"related,omitempty"`
	Headings     []Heading `json:"headings,omitempty"`
	Body         string    `json:"body"`
	FileHash     string    `json:"file_hash"`
}

type IssueSeverity string

const (
	SeverityWarning IssueSeverity = "warning"
	SeverityError   IssueSeverity = "error"
)

type GraphIssue struct {
	Severity IssueSeverity `json:"severity"`
	Kind     string        `json:"kind"`
	Path     string        `json:"path,omitempty"`
	Target   string        `json:"target,omitempty"`
	Message  string        `json:"message"`
}

type Corpus struct {
	Root       ResolvedRoot `json:"root"`
	Documents  []Document   `json:"documents"`
	Issues     []GraphIssue `json:"issues,omitempty"`
	CorpusHash string       `json:"corpus_hash"`

	byPath  map[string][]int
	byTitle map[string][]int
	byAlias map[string][]int
}

type Status struct {
	Root                ResolvedRoot `json:"root"`
	DocumentCount       int          `json:"document_count"`
	CorpusHash          string       `json:"corpus_hash"`
	ParseErrors         int          `json:"parse_errors"`
	DuplicateTitles     int          `json:"duplicate_titles"`
	DuplicateAliases    int          `json:"duplicate_aliases"`
	UnresolvedWikilinks int          `json:"unresolved_wikilinks"`
	Issues              []GraphIssue `json:"issues,omitempty"`
}

type SearchFilters struct {
	Tag      string `json:"tag,omitempty"`
	Audience string `json:"audience,omitempty"`
	Status   string `json:"status,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type SearchResult struct {
	Rank         int      `json:"rank"`
	Title        string   `json:"title"`
	RelativePath string   `json:"relative_path"`
	Status       string   `json:"status"`
	Tags         []string `json:"tags,omitempty"`
	Score        float64  `json:"score"`
	Matches      []string `json:"matches"`
	Snippet      string   `json:"snippet,omitempty"`
}

type SearchResponse struct {
	Root     ResolvedRoot   `json:"root"`
	Query    string         `json:"query"`
	Filters  SearchFilters  `json:"filters"`
	Results  []SearchResult `json:"results"`
	Warnings []GraphIssue   `json:"warnings,omitempty"`
}

type Inspection struct {
	Root         ResolvedRoot `json:"root"`
	Title        string       `json:"title"`
	RelativePath string       `json:"relative_path"`
	Status       string       `json:"status"`
	Heading      string       `json:"heading,omitempty"`
	Content      string       `json:"content"`
	Truncated    bool         `json:"truncated"`
}

type RelatedDocument struct {
	Title        string `json:"title"`
	RelativePath string `json:"relative_path"`
	Status       string `json:"status"`
}

type AmbiguousTargetError struct {
	Target     string
	Candidates []string
}

func (e *AmbiguousTargetError) Error() string {
	return fmt.Sprintf("documentation target %q is ambiguous: %v", e.Target, e.Candidates)
}

type TargetNotFoundError struct {
	Target string
}

func (e *TargetNotFoundError) Error() string {
	return fmt.Sprintf("documentation target %q was not found", e.Target)
}

func (c *Corpus) Status() Status {
	status := Status{Root: c.Root, DocumentCount: len(c.Documents), CorpusHash: c.CorpusHash, Issues: append([]GraphIssue(nil), c.Issues...)}
	for _, issue := range c.Issues {
		switch issue.Kind {
		case "parse_error", "missing_metadata":
			status.ParseErrors++
		case "duplicate_title":
			status.DuplicateTitles++
		case "duplicate_alias", "ambiguous_alias":
			status.DuplicateAliases++
		case "unresolved_wikilink", "directory_wikilink":
			status.UnresolvedWikilinks++
		}
	}
	return status
}

func parseVerifiedDate(value string) bool {
	if value == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}
