package capabilities

import (
	"fmt"
	"regexp"
	"strings"
)

// Main already has loomd's seeded "system" provider. The node agent must not
// replace that registry owner when advertising its remote execution endpoints.
func NodeSystemProviderKey(nodeKey string) string {
	if strings.TrimSpace(nodeKey) == "main" {
		return "node-agent-system"
	}
	return "system"
}

func NodeSystemProviderAddress(nodeKey string) string {
	nodeKey = strings.TrimSpace(nodeKey)
	return "workspace/" + nodeKey + "@" + NodeSystemProviderKey(nodeKey)
}

var (
	scopeSegmentPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	providerKeyPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	capabilitySegmentPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

type Address struct {
	ScopePath      string `json:"scope_path"`
	ProviderKey    string `json:"provider_key"`
	CapabilityName string `json:"capability_name"`
	CompactAddress string `json:"compact_address"`
}

type ProviderAddress struct {
	ScopePath      string `json:"scope_path"`
	ProviderKey    string `json:"provider_key"`
	CompactAddress string `json:"compact_address"`
}

func ParseProviderAddress(raw string) (ProviderAddress, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ProviderAddress{}, fmt.Errorf("provider address is required")
	}
	if strings.Count(value, "@") != 1 {
		return ProviderAddress{}, fmt.Errorf("provider address must contain exactly one @")
	}

	scopePath, providerKey, _ := strings.Cut(value, "@")
	if err := validateScopePath(scopePath); err != nil {
		return ProviderAddress{}, err
	}
	if !providerKeyPattern.MatchString(providerKey) {
		return ProviderAddress{}, fmt.Errorf("provider key %q must be a lowercase slug without dots", providerKey)
	}

	return ProviderAddress{
		ScopePath:      scopePath,
		ProviderKey:    providerKey,
		CompactAddress: scopePath + "@" + providerKey,
	}, nil
}

func NormalizeProviderAddress(raw string) (string, error) {
	address, err := ParseProviderAddress(raw)
	if err != nil {
		return "", err
	}
	return address.CompactAddress, nil
}

func ParseAddress(raw string) (Address, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return Address{}, fmt.Errorf("capability address is required")
	}
	if strings.Count(value, "@") != 1 {
		return Address{}, fmt.Errorf("capability address must contain exactly one @")
	}

	scopePath, rest, _ := strings.Cut(value, "@")
	if err := validateScopePath(scopePath); err != nil {
		return Address{}, err
	}

	dot := strings.Index(rest, ".")
	if dot < 0 {
		return Address{}, fmt.Errorf("capability address must include provider and capability separated by the first dot after @")
	}

	providerKey := rest[:dot]
	capabilityName := rest[dot+1:]
	if !providerKeyPattern.MatchString(providerKey) {
		return Address{}, fmt.Errorf("provider key %q must be a lowercase slug without dots", providerKey)
	}
	if err := validateCapabilityName(capabilityName); err != nil {
		return Address{}, err
	}

	compactAddress := scopePath + "@" + providerKey + "." + capabilityName
	return Address{
		ScopePath:      scopePath,
		ProviderKey:    providerKey,
		CapabilityName: capabilityName,
		CompactAddress: compactAddress,
	}, nil
}

func NormalizeAddress(raw string) (string, error) {
	address, err := ParseAddress(raw)
	if err != nil {
		return "", err
	}
	return address.CompactAddress, nil
}

func validateScopePath(scopePath string) error {
	if scopePath == "" {
		return fmt.Errorf("scope path is required")
	}
	if strings.Contains(scopePath, "//") || strings.HasPrefix(scopePath, "/") || strings.HasSuffix(scopePath, "/") {
		return fmt.Errorf("scope path %q must use non-empty slash-separated segments", scopePath)
	}
	for _, segment := range strings.Split(scopePath, "/") {
		if !scopeSegmentPattern.MatchString(segment) {
			return fmt.Errorf("scope path segment %q must be a lowercase slug", segment)
		}
	}
	return nil
}

func validateCapabilityName(name string) error {
	if name == "" {
		return fmt.Errorf("capability name is required")
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("capability name %q must use non-empty dot-separated segments", name)
	}
	for _, segment := range strings.Split(name, ".") {
		if !capabilitySegmentPattern.MatchString(segment) {
			return fmt.Errorf("capability segment %q must be lowercase snake case", segment)
		}
	}
	return nil
}
