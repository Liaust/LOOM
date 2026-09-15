package loomdocs

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

func Inspect(corpus *Corpus, target, heading string, maxChars int) (Inspection, error) {
	if corpus == nil {
		return Inspection{}, fmt.Errorf("documentation corpus is required")
	}
	document, err := corpus.resolveDocument(target)
	if err != nil {
		return Inspection{}, err
	}
	content := document.Body
	selectedHeading := ""
	if strings.TrimSpace(heading) != "" {
		matches := []Heading{}
		for _, candidate := range document.Headings {
			if strings.EqualFold(strings.TrimSpace(candidate.Title), strings.TrimSpace(heading)) {
				matches = append(matches, candidate)
			}
		}
		if len(matches) == 0 {
			return Inspection{}, fmt.Errorf("heading %q was not found in %s", heading, document.RelativePath)
		}
		if len(matches) > 1 {
			return Inspection{}, fmt.Errorf("heading %q is ambiguous in %s", heading, document.RelativePath)
		}
		content = document.Body[matches[0].StartByte:matches[0].EndByte]
		selectedHeading = matches[0].Title
	}
	if maxChars <= 0 {
		maxChars = 12000
	}
	bounded, truncated := boundRunes(content, maxChars)
	return Inspection{Root: corpus.Root, Title: document.Title, RelativePath: document.RelativePath, Status: document.Status, Heading: selectedHeading, Content: bounded, Truncated: truncated}, nil
}

func Related(corpus *Corpus, target string) ([]RelatedDocument, error) {
	if corpus == nil {
		return nil, fmt.Errorf("documentation corpus is required")
	}
	document, err := corpus.resolveDocument(target)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := []RelatedDocument{}
	for _, relatedTarget := range document.Related {
		related, err := corpus.resolveDocument(relatedTarget)
		if err != nil || seen[related.RelativePath] {
			continue
		}
		seen[related.RelativePath] = true
		result = append(result, RelatedDocument{Title: related.Title, RelativePath: related.RelativePath, Status: related.Status})
	}
	for _, candidate := range corpus.Documents {
		if candidate.RelativePath == document.RelativePath || seen[candidate.RelativePath] {
			continue
		}
		for _, relatedTarget := range candidate.Related {
			indexes := corpus.lookupIndexes(relatedTarget)
			if len(indexes) == 1 && corpus.Documents[indexes[0]].RelativePath == document.RelativePath {
				seen[candidate.RelativePath] = true
				result = append(result, RelatedDocument{Title: candidate.Title, RelativePath: candidate.RelativePath, Status: candidate.Status})
				break
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	return result, nil
}

func boundRunes(value string, limit int) (string, bool) {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value, false
	}
	runes := []rune(value)
	return string(runes[:limit]), true
}
