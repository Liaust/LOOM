package knowledge

import (
	"context"
	"html"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type FileSearchResult struct {
	sourceRoot string
	FileID     string `json:"file_id"`
	Title      string `json:"title"`
	Path       string `json:"path"`
	Node       string `json:"node"`
	Project    string `json:"project,omitempty"`
	Category   string `json:"category"`
	Lifecycle  string `json:"lifecycle"`
	Excerpt    string `json:"excerpt,omitempty"`
	Content    string `json:"content"`
}

type FileSearchResultSet struct {
	Query   string             `json:"query"`
	Mode    string             `json:"mode"`
	Results []FileSearchResult `json:"results"`
	Notice  string             `json:"notice,omitempty"`
}

func (s *Service) SearchFiles(ctx context.Context, input NotesSearchInput) (FileSearchResultSet, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	input.RequireCurrent = true
	input.fileView = true
	input.discoveryLimit = limit
	// A bounded extra page accommodates multiple chunks without an unbounded
	// corpus scan. The indexed search remains the candidate owner.
	input.Limit = min(50, limit*4)
	detailed, err := s.SearchNotes(ctx, input)
	if err != nil {
		return FileSearchResultSet{}, err
	}
	out := FileSearchResultSet{Query: detailed.Query, Mode: detailed.Mode, Results: []FileSearchResult{}, Notice: fileSearchNotice(detailed)}
	seen := map[string]bool{}
	for _, hit := range detailed.Results {
		fileID := firstNonEmpty(hit.FileID, hit.KnowledgeObjectID)
		if seen[fileID] {
			continue
		}
		seen[fileID] = true
		path := hit.CanonicalPath
		if path == "" {
			path = hit.SourcePath
		}
		if !filepath.IsAbs(path) && hit.filePath != "" {
			path = hit.filePath
		}
		content := "indexed_text"
		if hit.MetadataOnly || hit.SourceKind == KnowledgeMetadataSearchSourceKind || hit.TextSource == TextSourceMetadataText {
			content = "metadata_only"
		}
		excerpt := compactFileExcerpt(hit.Snippet, 700)
		if content == "metadata_only" {
			excerpt = ""
		}
		out.Results = append(out.Results, FileSearchResult{FileID: fileID, Title: firstNonEmpty(hit.Title, filepath.Base(path)),
			Path: path, Node: hit.SourceNodeKey, Project: hit.ProjectName, Category: hit.SourceCategory,
			Lifecycle: string(hit.SourceLifecycle), Excerpt: excerpt, Content: content})
		if len(out.Results) == limit {
			break
		}
	}
	if len(out.Results) < limit {
		metadata, err := s.metadataFileResults(ctx, input, "", limit*2)
		if err != nil {
			return out, err
		}
		for _, hit := range metadata {
			if seen[hit.FileID] {
				continue
			}
			out.Results = append(out.Results, hit)
			seen[hit.FileID] = true
			if len(out.Results) == limit {
				break
			}
		}
	}
	return out, nil
}

func fileSearchNotice(detailed NotesSearchResultSet) string {
	parts := nonEmptyStrings(detailed.FallbackReason)
	if detailed.CandidateRetrievalTruncated {
		parts = append(parts, "Results are bounded; narrow the query for more matches.")
	}
	return strings.Join(parts, " ")
}

func compactFileExcerpt(value string, limit int) string {
	value = strings.NewReplacer("<b>", "", "</b>", "", "<mark>", "", "</mark>", "", "<loom-hit>", "", "</loom-hit>", "").Replace(value)
	value = html.UnescapeString(value)
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "..."
}
