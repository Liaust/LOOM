package watchedroots

import (
	"fmt"
	"slices"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

type ConsumerSelections struct {
	Backup *PathSelection `json:"backup,omitempty"`
	Sync   *PathSelection `json:"sync,omitempty"`
	Index  *PathSelection `json:"index,omitempty"`
}

type PathSelection struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

func normalizeConsumerSelections(selections *ConsumerSelections) *ConsumerSelections {
	if selections == nil {
		return nil
	}
	return &ConsumerSelections{
		Backup: normalizePathSelection(selections.Backup),
		Sync:   normalizePathSelection(selections.Sync),
		Index:  normalizePathSelection(selections.Index),
	}
}

func normalizePathSelection(selection *PathSelection) *PathSelection {
	if selection == nil {
		return nil
	}
	normalize := func(patterns []string) []string {
		out := make([]string, 0, len(patterns))
		for _, pattern := range patterns {
			out = append(out, strings.TrimSpace(pattern))
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return &PathSelection{Include: normalize(selection.Include), Exclude: normalize(selection.Exclude)}
}

func validateConsumerConfig(config RootConfig) error {
	switch config.SchemaVersion {
	case ConfigSchemaVersion:
		if config.ConsumerSelections != nil || config.Files != nil {
			return fmt.Errorf("consumer_selections and files require watched-root config schema %s", IndependentConfigSchemaVersion)
		}
		return nil
	case IndependentConfigSchemaVersion:
	default:
		return fmt.Errorf("unsupported watched-root config schema %q", config.SchemaVersion)
	}
	if config.ConsumerSelections != nil {
		for _, consumer := range []struct {
			name      string
			selection *PathSelection
		}{
			{"backup", config.ConsumerSelections.Backup},
			{"sync", config.ConsumerSelections.Sync},
			{"index", config.ConsumerSelections.Index},
		} {
			selection := consumer.selection
			if selection == nil {
				continue
			}
			if len(selection.Include) == 0 {
				return fmt.Errorf("consumer_selections.%s.include must be nonempty; disable the consumer with mode none", consumer.name)
			}
			for _, field := range []struct {
				name     string
				patterns []string
			}{{"include", selection.Include}, {"exclude", selection.Exclude}} {
				for _, pattern := range field.patterns {
					if strings.ContainsAny(pattern, "\\\x00\r\n") {
						return fmt.Errorf("consumer_selections.%s.%s: invalid relative pattern %q", consumer.name, field.name, pattern)
					}
				}
				if err := validatePatterns(field.patterns); err != nil {
					return fmt.Errorf("consumer_selections.%s.%s: %w", consumer.name, field.name, err)
				}
			}
		}
	}
	if config.Files != nil {
		if _, err := filepolicy.NormalizeFolderPolicy(*config.Files); err != nil {
			return err
		}
	}
	return nil
}

func consumerSelects(selection *PathSelection, relativePath string) bool {
	if selection == nil {
		return true
	}
	included, _ := MatchAny(selection.Include, relativePath)
	excluded, _ := MatchAny(selection.Exclude, relativePath)
	return included && !excluded
}
