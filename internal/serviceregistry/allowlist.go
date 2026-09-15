package serviceregistry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// AllowlistResolver resolves the reviewed target-node declaration that is
// authoritative for endpoint compilation. Node dispatch independently reloads
// and rechecks its local allowlist before executing an operation.
type AllowlistResolver interface {
	ResolveServiceAllowlist(context.Context, string, RuntimeProfile) (AllowlistRecord, error)
}

type StaticAllowlistResolver struct {
	Allowlist Allowlist
}

func (r StaticAllowlistResolver) ResolveServiceAllowlist(_ context.Context, nodeKey string, profile RuntimeProfile) (AllowlistRecord, error) {
	return resolveServiceAllowlist(r.Allowlist, nodeKey, profile)
}

type FileAllowlistResolver struct {
	Path string
}

func (r FileAllowlistResolver) ResolveServiceAllowlist(_ context.Context, nodeKey string, profile RuntimeProfile) (AllowlistRecord, error) {
	if r.Path == "" {
		return AllowlistRecord{}, fmt.Errorf("reviewed service allowlist is not configured")
	}
	list, err := LoadAllowlist(r.Path)
	if err != nil {
		return AllowlistRecord{}, fmt.Errorf("load reviewed service allowlist: %w", err)
	}
	return resolveServiceAllowlist(list, nodeKey, profile)
}

func resolveServiceAllowlist(list Allowlist, nodeKey string, profile RuntimeProfile) (AllowlistRecord, error) {
	var match *AllowlistRecord
	for index := range list.Records {
		record := list.Records[index]
		if record.NodeKey != nodeKey || record.Manager != profile.Manager || record.Unit != profile.Unit {
			continue
		}
		if match != nil {
			return AllowlistRecord{}, fmt.Errorf("multiple reviewed allowlist records match target node and runtime")
		}
		match = &record
	}
	if match == nil {
		return AllowlistRecord{}, fmt.Errorf("service runtime is not present in the reviewed target-node allowlist")
	}
	return *match, nil
}

type Allowlist struct {
	Records []AllowlistRecord `json:"records" yaml:"records"`
}

func LoadAllowlist(path string) (Allowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Allowlist{}, err
	}
	return ParseAllowlist(raw)
}

func ParseAllowlist(raw []byte) (Allowlist, error) {
	var document Allowlist
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return Allowlist{}, fmt.Errorf("decode service allowlist: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Allowlist{}, fmt.Errorf("decode service allowlist: multiple YAML documents are not allowed")
		}
		return Allowlist{}, err
	}
	seen := map[string]bool{}
	for index, record := range document.Records {
		normalized, err := NormalizeAndValidateAllowlistRecord(record)
		if err != nil {
			return Allowlist{}, fmt.Errorf("allowlist record %d: %w", index, err)
		}
		if seen[normalized.Key] {
			return Allowlist{}, fmt.Errorf("duplicate allowlist key %q", normalized.Key)
		}
		seen[normalized.Key] = true
		document.Records[index] = normalized
	}
	sort.Slice(document.Records, func(i, j int) bool { return document.Records[i].Key < document.Records[j].Key })
	return document, nil
}

func (list Allowlist) Lookup(key string) (AllowlistRecord, bool) {
	for _, record := range list.Records {
		if record.Key == key {
			return record, true
		}
	}
	return AllowlistRecord{}, false
}

func IntersectOperations(profile RuntimeProfile, record AllowlistRecord) ([]Operation, error) {
	if profile.Manager != record.Manager || profile.Unit != record.Unit {
		return nil, fmt.Errorf("runtime profile manager/unit does not match reviewed allowlist record")
	}
	allowed := map[Operation]bool{}
	for _, operation := range record.Operations {
		allowed[operation] = true
	}
	result := []Operation{}
	for _, operation := range profile.Operations {
		if allowed[operation] {
			result = append(result, operation)
		}
	}
	return result, nil
}
