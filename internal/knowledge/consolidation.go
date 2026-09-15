package knowledge

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const defaultConsolidatedTextMaxBytes = 10 * 1024 * 1024

type ConsolidationResult struct {
	Segments       []ConsolidatedSegment
	Text           string
	ArtifactIDs    []string
	SourceLocators []string
	SourceKinds    []string
	Omitted        int
	Warnings       []string
}

type ConsolidatedSegment struct {
	StartByte     int    `json:"start_byte"`
	EndByte       int    `json:"end_byte"`
	ArtifactID    string `json:"artifact_id"`
	SourceLocator string `json:"source_locator"`
	SourceKind    string `json:"source_kind"`
}

func ConsolidateCurrentArtifacts(artifacts []DerivedArtifact, maxBytes int) ConsolidationResult {
	if maxBytes <= 0 {
		maxBytes = defaultConsolidatedTextMaxBytes
	}
	candidates := append([]DerivedArtifact(nil), artifacts...)
	sort.SliceStable(candidates, func(i, j int) bool { return artifactLocatorOrder(candidates[i]) < artifactLocatorOrder(candidates[j]) })
	result := ConsolidationResult{}
	seen := map[string]string{}
	parts := []string{}
	for _, artifact := range candidates {
		if !artifact.Active || artifact.TextContent == nil || artifact.ArtifactKind == ArtifactKindMetadataText || artifact.ArtifactKind == ArtifactKindConsolidatedText {
			continue
		}
		text := normalizeArtifactText(*artifact.TextContent)
		if text == "" {
			continue
		}
		key := dedupeArtifactText(text)
		if _, ok := seen[key]; ok {
			result.Omitted++
			continue
		}
		additional := len(text)
		if len(parts) > 0 {
			additional += 2
		}
		if len(result.Text)+additional > maxBytes {
			result.Omitted++
			result.Warnings = append(result.Warnings, fmt.Sprintf("omitted %s at %s because consolidated byte limit was reached", artifact.ArtifactKind, artifact.SourceLocator))
			continue
		}
		seen[key] = artifact.ArtifactKind
		start := len(result.Text)
		if len(parts) > 0 {
			start += 2
		}
		result.Segments = append(result.Segments, ConsolidatedSegment{StartByte: start, EndByte: start + len(text), ArtifactID: artifact.KnowledgeDerivedArtifactID, SourceLocator: artifact.SourceLocator, SourceKind: artifact.ArtifactKind})
		parts = append(parts, text)
		result.Text = strings.Join(parts, "\n\n")
		result.ArtifactIDs = append(result.ArtifactIDs, artifact.KnowledgeDerivedArtifactID)
		result.SourceLocators = append(result.SourceLocators, artifact.SourceLocator)
		result.SourceKinds = append(result.SourceKinds, artifact.ArtifactKind)
	}
	return result
}

var locatorPagePattern = regexp.MustCompile(`(?:page:|page/)(\d+)`)

func artifactLocatorOrder(artifact DerivedArtifact) string {
	rank := map[string]int{ArtifactKindEmbeddedText: 1, ArtifactKindStructuredText: 1, ArtifactKindOCRText: 2, ArtifactKindVisionDescription: 3}[artifact.ArtifactKind]
	page := 999999
	if match := locatorPagePattern.FindStringSubmatch(artifact.SourceLocator); len(match) == 2 {
		page, _ = strconv.Atoi(match[1])
	}
	return fmt.Sprintf("%09d:%02d:%s", page, rank, artifact.SourceLocator)
}
func dedupeArtifactText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
