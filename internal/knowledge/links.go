package knowledge

import (
	"regexp"
	"strings"
)

var (
	markdownLinkPattern = regexp.MustCompile(`(!?)\[([^\]\n]+)\]\(([^)\n]+)\)`)
	wikilinkPattern     = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)
)

type ExtractedLink struct {
	Kind             string `json:"kind"`
	RawTarget        string `json:"raw_target"`
	NormalizedTarget string `json:"normalized_target,omitempty"`
	LinkText         string `json:"link_text,omitempty"`
	StartOffset      int    `json:"start_offset"`
	EndOffset        int    `json:"end_offset"`
}

func ExtractMarkdownLinks(text string) []ExtractedLink {
	links := []ExtractedLink{}
	for _, match := range markdownLinkPattern.FindAllStringSubmatchIndex(text, -1) {
		if len(match) < 8 {
			continue
		}
		if match[2] >= 0 && text[match[2]:match[3]] == "!" {
			continue
		}
		linkText := text[match[4]:match[5]]
		rawTarget := markdownTargetWithoutTitle(text[match[6]:match[7]])
		if strings.TrimSpace(rawTarget) == "" {
			continue
		}
		links = append(links, ExtractedLink{
			Kind:             LinkKindMarkdown,
			RawTarget:        rawTarget,
			NormalizedTarget: normalizeLinkTarget(rawTarget),
			LinkText:         strings.TrimSpace(linkText),
			StartOffset:      match[0],
			EndOffset:        match[1],
		})
	}
	for _, match := range wikilinkPattern.FindAllStringSubmatchIndex(text, -1) {
		if len(match) < 4 {
			continue
		}
		raw := strings.TrimSpace(text[match[2]:match[3]])
		if raw == "" {
			continue
		}
		target, label := splitWikilinkTarget(raw)
		links = append(links, ExtractedLink{
			Kind:             LinkKindWikilink,
			RawTarget:        target,
			NormalizedTarget: normalizeLinkTarget(target),
			LinkText:         label,
			StartOffset:      match[0],
			EndOffset:        match[1],
		})
	}
	return links
}

func markdownTargetWithoutTitle(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "<") {
		if end := strings.Index(raw, ">"); end > 0 {
			return strings.TrimSpace(raw[1:end])
		}
	}
	if fields := strings.Fields(raw); len(fields) > 0 {
		return strings.Trim(fields[0], "<>")
	}
	return raw
}

func splitWikilinkTarget(raw string) (string, string) {
	parts := strings.SplitN(raw, "|", 2)
	target := strings.TrimSpace(parts[0])
	label := ""
	if len(parts) == 2 {
		label = strings.TrimSpace(parts[1])
	}
	if label == "" {
		label = target
	}
	return target, label
}

func normalizeLinkTarget(target string) string {
	target = strings.TrimSpace(target)
	target = strings.Trim(target, "<>")
	target = strings.ReplaceAll(target, "\\", "/")
	for strings.Contains(target, "//") && !strings.Contains(target, "://") {
		target = strings.ReplaceAll(target, "//", "/")
	}
	return target
}
