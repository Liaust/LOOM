package loomdocs

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type frontmatter struct {
	Title       string     `yaml:"title"`
	Aliases     stringList `yaml:"aliases"`
	Description string     `yaml:"description"`
	Audience    stringList `yaml:"audience"`
	Tags        stringList `yaml:"tags"`
	Status      string     `yaml:"status"`
	VerifiedAt  string     `yaml:"verified_at"`
	SourceScope stringList `yaml:"source_scope"`
	Related     stringList `yaml:"related"`
}

type stringList []string

func (s *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		value := strings.TrimSpace(node.Value)
		if value != "" {
			*s = []string{value}
		}
		return nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode {
				return fmt.Errorf("expected string list")
			}
			if value := strings.TrimSpace(child.Value); value != "" {
				values = append(values, value)
			}
		}
		*s = values
		return nil
	default:
		return fmt.Errorf("expected string or string list")
	}
}

func parseFrontmatter(payload []byte) (frontmatter, string, error) {
	normalized := bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return frontmatter{}, "", fmt.Errorf("missing YAML frontmatter")
	}
	rest := normalized[4:]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return frontmatter{}, "", fmt.Errorf("unterminated YAML frontmatter")
	}
	var metadata frontmatter
	if err := yaml.Unmarshal(rest[:end], &metadata); err != nil {
		return frontmatter{}, "", fmt.Errorf("parse YAML frontmatter: %w", err)
	}
	metadata.Title = strings.TrimSpace(metadata.Title)
	metadata.Description = strings.TrimSpace(metadata.Description)
	metadata.Status = strings.TrimSpace(metadata.Status)
	metadata.VerifiedAt = strings.TrimSpace(metadata.VerifiedAt)
	body := string(rest[end+5:])
	return metadata, body, nil
}

func validateMetadata(metadata frontmatter) []string {
	missing := []string{}
	if metadata.Title == "" {
		missing = append(missing, "title")
	}
	if metadata.Description == "" {
		missing = append(missing, "description")
	}
	if len(metadata.Audience) == 0 {
		missing = append(missing, "audience")
	}
	if len(metadata.Tags) == 0 {
		missing = append(missing, "tags")
	}
	if metadata.Status == "" {
		missing = append(missing, "status")
	}
	if !parseVerifiedDate(metadata.VerifiedAt) {
		missing = append(missing, "verified_at(valid-date)")
	}
	return missing
}
