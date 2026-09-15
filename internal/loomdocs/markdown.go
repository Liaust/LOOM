package loomdocs

import (
	"regexp"
	"strings"
)

var (
	headingPattern  = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	wikilinkPattern = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
)

func parseHeadings(body string) []Heading {
	headings := []Heading{}
	offset := 0
	lines := strings.SplitAfter(body, "\n")
	for _, lineWithNewline := range lines {
		line := strings.TrimSuffix(lineWithNewline, "\n")
		if match := headingPattern.FindStringSubmatch(line); match != nil {
			headings = append(headings, Heading{
				Title:     strings.TrimSpace(match[2]),
				Level:     len(match[1]),
				StartByte: offset,
				EndByte:   len(body),
			})
		}
		offset += len(lineWithNewline)
	}
	for index := range headings {
		for next := index + 1; next < len(headings); next++ {
			if headings[next].Level <= headings[index].Level {
				headings[index].EndByte = headings[next].StartByte
				break
			}
		}
	}
	return headings
}

func extractWikilinks(values ...string) []string {
	seen := map[string]bool{}
	links := []string{}
	for _, value := range values {
		for _, match := range wikilinkPattern.FindAllStringSubmatch(value, -1) {
			target := strings.TrimSpace(match[1])
			if separator := strings.IndexAny(target, "|#"); separator >= 0 {
				target = strings.TrimSpace(target[:separator])
			}
			if target == "" || seen[target] {
				continue
			}
			seen[target] = true
			links = append(links, target)
		}
	}
	return links
}
