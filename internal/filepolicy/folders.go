package filepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

// FolderPolicy is authored once at the owning root. Its independent consumer
// decisions never grant access or enable a disabled root consumer.
type FolderPolicy struct {
	Rules []FolderRule `json:"rules" yaml:"rules"`
}

type FolderRule struct {
	Path    string `json:"path" yaml:"path"`
	Search  string `json:"search,omitempty" yaml:"search,omitempty"`
	Protect *bool  `json:"protect,omitempty" yaml:"protect,omitempty"`
	// Nil inherits; an explicit empty list disables automatic enrichment.
	Enrichment *[]string `json:"enrichment,omitempty" yaml:"enrichment,omitempty"`
}

type FolderEnrichment struct {
	Pattern    string    `json:"pattern"`
	Enrichment *[]string `json:"enrichment,omitempty"`
}

type FolderDecision struct {
	Search     string            `json:"search,omitempty"`
	Protect    *bool             `json:"protect,omitempty"`
	Enrichment *[]string         `json:"enrichment,omitempty"`
	Sources    map[string]string `json:"sources,omitempty"`
}

func validFolderPath(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && fs.ValidPath(value) &&
		!strings.ContainsAny(value, "\\\x00\r\n") && strings.TrimSpace(value) == value
}

// NormalizeFolderPolicy canonicalizes sets/order without modifying the input.
func NormalizeFolderPolicy(policy FolderPolicy) (FolderPolicy, error) {
	if policy.Rules == nil {
		return FolderPolicy{}, fmt.Errorf("files.rules must be an array; use [] for no exceptions")
	}
	out := FolderPolicy{Rules: make([]FolderRule, 0, len(policy.Rules))}
	byPath := make(map[string]FolderRule, len(policy.Rules))
	for i, rule := range policy.Rules {
		if !validFolderPath(rule.Path) || rule.Path == "." || !doublestar.ValidatePattern(rule.Path) || strings.ContainsAny(rule.Path, "{}") {
			return FolderPolicy{}, fmt.Errorf("files.rules[%d].path: expected a canonical relative folder or glob, got %q", i, rule.Path)
		}
		if rule.Enrichment != nil {
			stages := append([]string{}, (*rule.Enrichment)...)
			for _, stage := range stages {
				if stage != "ocr" && stage != "vision" && stage != "embeddings" {
					return FolderPolicy{}, fmt.Errorf("files.rules[%d].enrichment: unsupported stage %q (use ocr, vision or embeddings)", i, stage)
				}
			}
			slices.Sort(stages)
			stages = slices.Compact(stages)
			rule.Enrichment = &stages
		}
		if rule.Search != "" && rule.Search != "none" && rule.Search != "metadata" && rule.Search != "text" {
			return FolderPolicy{}, fmt.Errorf("files.rules[%d].search: use none, metadata or text", i)
		}
		if rule.Protect != nil {
			value := *rule.Protect
			rule.Protect = &value
		}
		previous, exists := byPath[rule.Path]
		if exists {
			if previous.Enrichment != nil && rule.Enrichment != nil && !slices.Equal(*previous.Enrichment, *rule.Enrichment) ||
				previous.Protect != nil && rule.Protect != nil && *previous.Protect != *rule.Protect ||
				previous.Search != "" && rule.Search != "" && previous.Search != rule.Search {
				return FolderPolicy{}, fmt.Errorf("files.rules: conflicting values for path %q", rule.Path)
			}
			if rule.Enrichment == nil {
				rule.Enrichment = previous.Enrichment
			}
			if rule.Protect == nil {
				rule.Protect = previous.Protect
			}
			if rule.Search == "" {
				rule.Search = previous.Search
			}
		}
		byPath[rule.Path] = rule
	}
	for _, rule := range byPath {
		if rule.Enrichment != nil || rule.Search != "" || rule.Protect != nil {
			out.Rules = append(out.Rules, rule)
		}
	}
	slices.SortFunc(out.Rules, func(a, b FolderRule) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// DecodeFolderPolicy preserves the omitted/empty distinction and rejects null,
// unknown fields and wrong types. Enclosing evidence readers reject duplicates.
func DecodeFolderPolicy(raw []byte) (FolderPolicy, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return FolderPolicy{}, fmt.Errorf("files: %w", err)
	}
	for key := range fields {
		if key != "rules" {
			return FolderPolicy{}, fmt.Errorf("files: unknown field %q", key)
		}
	}
	var policy FolderPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("files: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return policy, fmt.Errorf("files: expected one policy object")
	}
	var shape struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return policy, fmt.Errorf("files: %w", err)
	}
	for i, rule := range shape.Rules {
		for key := range rule {
			if key != "path" && key != "enrichment" && key != "search" && key != "protect" {
				return policy, fmt.Errorf("files.rules[%d]: unknown field %q", i, key)
			}
		}
		if value, present := rule["enrichment"]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return policy, fmt.Errorf("files.rules[%d].enrichment: omit to inherit or use [] to disable; null is invalid", i)
		}
		for _, key := range []string{"search", "protect"} {
			if value, present := rule[key]; present && (bytes.Equal(bytes.TrimSpace(value), []byte("null")) || bytes.Equal(bytes.TrimSpace(value), []byte(`""`))) {
				return policy, fmt.Errorf("files.rules[%d].%s: omit to inherit; null/empty is invalid", i, key)
			}
		}
	}
	return NormalizeFolderPolicy(policy)
}

// Literal paths cover their descendants. Globs match paths directly. Priority
// is leading literal segments, all literal segments, non-** depth, then fewer
// wildcard segments. Equal-priority overlapping values must agree, not depend
// on YAML order. Omitted fields do not shadow an inherited value.
func (policy FolderPolicy) ResolveEnrichment(relative string) (FolderEnrichment, error) {
	decision, err := policy.Resolve(relative)
	return FolderEnrichment{Pattern: decision.Sources["enrichment"], Enrichment: decision.Enrichment}, err
}

func (policy FolderPolicy) Resolve(relative string) (FolderDecision, error) {
	if !validFolderPath(relative) {
		return FolderDecision{}, fmt.Errorf("files: invalid source-relative path %q", relative)
	}
	var search folderSelection[string]
	var protect folderSelection[bool]
	var enrichment folderSelection[[]string]
	for _, rule := range policy.Rules {
		matched := false
		if !strings.ContainsAny(rule.Path, "*?[") {
			matched = relative == rule.Path || strings.HasPrefix(relative, rule.Path+"/")
		} else {
			var err error
			matched, err = doublestar.Match(rule.Path, relative)
			if err != nil {
				return FolderDecision{}, fmt.Errorf("files.rules path %q: %w", rule.Path, err)
			}
		}
		if !matched {
			continue
		}
		score := folderSpecificity(rule.Path)
		if rule.Search != "" {
			search.consider(&rule.Search, rule.Path, relative, "search", score, func(a, b string) bool { return a == b })
		}
		protect.consider(rule.Protect, rule.Path, relative, "protect", score, func(a, b bool) bool { return a == b })
		enrichment.consider(rule.Enrichment, rule.Path, relative, "enrichment", score, slices.Equal[[]string])
	}
	result := FolderDecision{Protect: protect.value, Enrichment: enrichment.value, Sources: map[string]string{}}
	if search.value != nil {
		result.Search = *search.value
		result.Sources["search"] = search.pattern
	}
	if protect.value != nil {
		result.Sources["protect"] = protect.pattern
	}
	if enrichment.value != nil {
		copy := append([]string{}, (*enrichment.value)...)
		result.Enrichment = &copy
		result.Sources["enrichment"] = enrichment.pattern
	}
	return result, errors.Join(search.conflict, protect.conflict, enrichment.conflict)
}

// WorkspaceFolderDefaults applies only to newly authored filesystem-first
// roots. Legacy extraction/embedding choices are never inferred from it.
func WorkspaceFolderDefaults(overrides *FolderPolicy) (FolderPolicy, error) {
	off := []string{}
	text := []string{"embeddings"}
	rules := map[string]FolderRule{"**": {Path: "**", Search: "metadata", Enrichment: &off}}
	for _, extension := range []string{"md", "markdown", "mdown", "txt", "text", "rst", "adoc"} {
		pattern := "**/*." + extension
		rules[pattern] = FolderRule{Path: pattern, Search: "text", Enrichment: &text}
	}
	for _, name := range []string{".git", ".loom", "credentials", "private_no_index"} {
		pattern := "**/" + name + "/**"
		rules[pattern] = FolderRule{Path: pattern, Search: "none", Enrichment: &off}
	}
	if overrides != nil {
		normalized, err := NormalizeFolderPolicy(*overrides)
		if err != nil {
			return FolderPolicy{}, err
		}
		for _, rule := range normalized.Rules {
			base := rules[rule.Path]
			base.Path = rule.Path
			if rule.Search != "" {
				base.Search = rule.Search
			}
			if rule.Protect != nil {
				base.Protect = rule.Protect
			}
			if rule.Enrichment != nil {
				base.Enrichment = rule.Enrichment
			}
			rules[rule.Path] = base
		}
	}
	policy := FolderPolicy{Rules: make([]FolderRule, 0, len(rules))}
	for _, rule := range rules {
		policy.Rules = append(policy.Rules, rule)
	}
	return NormalizeFolderPolicy(policy)
}

type folderSelection[T any] struct {
	value    *T
	pattern  string
	best     [4]int
	conflict error
}

func (s *folderSelection[T]) consider(value *T, pattern, relative, field string, score [4]int, equal func(T, T) bool) {
	if value == nil {
		return
	}
	priority := slices.Compare(score[:], s.best[:])
	if s.value != nil && priority == 0 && !equal(*s.value, *value) {
		s.conflict = fmt.Errorf("files.rules.%s: conflicting equal-specificity paths %q and %q for %q", field, s.pattern, pattern, relative)
	}
	if s.value == nil || priority > 0 {
		copy := *value
		s.value, s.pattern, s.best, s.conflict = &copy, pattern, score, nil
	}
}

func folderSpecificity(pattern string) (score [4]int) {
	prefix := true
	for _, part := range strings.Split(pattern, "/") {
		wildcard := strings.ContainsAny(part, "*?[")
		if !wildcard {
			score[1]++
			if prefix {
				score[0]++
			}
		} else {
			prefix = false
			score[3]--
		}
		if part != "**" {
			score[2]++
		}
	}
	return score
}
