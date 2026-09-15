package projectcontracts

import (
	"fmt"
	"sort"
	"strings"
)

const (
	PresetMinimal    = "minimal"
	PresetResearch   = "research"
	PresetAutomation = "automation"
	PresetConnector  = "connector"
	PresetModule     = "module"
)

type Preset struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Facets      []string `json:"facets"`
}

var scaffoldPresets = []Preset{
	{
		Name:        PresetMinimal,
		Description: "Lightweight project with notes, backup policy, and portal metadata.",
		Facets:      []string{"notes", "backup_policy", "portal"},
	},
	{
		Name:        PresetResearch,
		Description: "Knowledge project with notes, docs, sync, backup, and worker policy placeholders.",
		Facets:      []string{"notes", "docs", "sync_policy", "backup_policy", "worker_policy", "portal"},
	},
	{
		Name:        PresetAutomation,
		Description: "Automation project with scripts, schedules, direct events, tests, secrets, and policies.",
		Facets:      []string{"notes", "scripts", "schedules", "direct_events", "tests", "secrets", "sync_policy", "backup_policy", "worker_policy", "portal"},
	},
	{
		Name:        PresetConnector,
		Description: "Connector project for reusable provider namespaces and script-backed capabilities.",
		Facets:      []string{"notes", "scripts", "connectors", "docs", "tests", "secrets", "sync_policy", "backup_policy", "worker_policy", "portal"},
	},
	{
		Name:        PresetModule,
		Description: "Module project for larger LOOM expansion packages.",
		Facets:      []string{"notes", "repos", "connectors", "modules", "docs", "tests", "sync_policy", "backup_policy", "worker_policy", "portal"},
	},
}

func SupportedPresets() []Preset {
	out := make([]Preset, 0, len(scaffoldPresets))
	for _, preset := range scaffoldPresets {
		preset.Facets = append([]string{}, preset.Facets...)
		out = append(out, preset)
	}
	return out
}

func PresetByName(name string) (Preset, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = PresetMinimal
	}
	for _, preset := range scaffoldPresets {
		if preset.Name == name {
			preset.Facets = append([]string{}, preset.Facets...)
			return preset, true
		}
	}
	return Preset{}, false
}

func ValidFacet(key string) bool {
	return supportedFacets[canonicalFacetName(key)]
}

func CanonicalFacetName(key string) string {
	return canonicalFacetName(key)
}

func NormalizeFacetList(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range values {
		key := canonicalFacetName(value)
		if key == "" {
			continue
		}
		if !supportedFacets[key] {
			return nil, fmt.Errorf("unsupported project facet: %s", value)
		}
		seen[key] = true
	}
	out := []string{}
	for _, key := range facetOrder {
		if seen[key] {
			out = append(out, key)
		}
	}
	extras := []string{}
	for key := range seen {
		if !containsString(facetOrder, key) {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	out = append(out, extras...)
	return out, nil
}

func canonicalFacetName(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	return key
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
