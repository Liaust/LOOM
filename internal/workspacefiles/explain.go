package workspacefiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

const maxExplainDescriptorBytes = 1 << 20

// RuleSource identifies authored intent, not evidence of a running consumer.
type RuleSource struct {
	Descriptor string `json:"descriptor"`
	Field      string `json:"field"`
	Pattern    string `json:"pattern,omitempty"`
	Default    bool   `json:"default,omitempty"`
}

type FileIntent struct {
	State      string                `json:"state"`
	Descriptor string                `json:"descriptor,omitempty"`
	OwnerPath  string                `json:"owner_path,omitempty"`
	Search     string                `json:"search"`
	Enrichment *[]string             `json:"enrichment"`
	Protect    *bool                 `json:"protect"`
	Sources    map[string]RuleSource `json:"sources"`
	WatchPlan  string                `json:"watch_plan"`
	Reason     string                `json:"reason"`
}

type Explanation struct {
	Root                    string     `json:"root"`
	Path                    string     `json:"path"`
	SourcePath              string     `json:"source_path"`
	Source                  Entry      `json:"source"`
	ReadOnly                bool       `json:"read_only"`
	Verification            string     `json:"verification"`
	Intent                  FileIntent `json:"intent"`
	RootRegistration        string     `json:"root_registration"`
	DeviceLocalAvailability string     `json:"device_local_availability"`
	AppliedConfig           string     `json:"applied_config"`
	BackupCoverage          string     `json:"backup_coverage"`
	Qualification           string     `json:"qualification"`
	NextAction              string     `json:"next_action"`
}

// Explain inspects one physical source and its owning descriptors only. It has
// no catalog, registration, delivery, or custody lookup and performs no writes.
func (s *Service) Explain(ctx context.Context, ref, relative string) (Explanation, error) {
	metadata, err := s.Stat(ctx, ref, relative)
	if err != nil {
		return Explanation{}, err
	}
	if metadata.Kind != "file" && metadata.Kind != "directory" {
		return Explanation{}, ErrNotRegular
	}
	root, err := s.root(ref)
	if err != nil {
		return Explanation{}, err
	}
	relative = metadata.RelativePath
	result := Explanation{
		Root: ref, Path: relative, SourcePath: filepath.Join(root.SourcePath, filepath.FromSlash(relative)), Source: metadata,
		ReadOnly: root.ReadOnly || ref == "archive", Verification: "not_checked",
		RootRegistration: "unknown", DeviceLocalAvailability: "unknown", AppliedConfig: "not_checked", BackupCoverage: "not_verified",
		Intent:        FileIntent{State: "unknown", Search: "unknown", Sources: map[string]RuleSource{}, WatchPlan: "not_checked", Reason: "No owning descriptor is known; filesystem-first defaults were not applied."},
		Qualification: "Descriptor intent is not evidence of registration, applied configuration, device delivery or backup protection.",
		NextAction:    "Read the original directly; no per-file declaration or indexing is required.",
	}
	if ref == "archive" {
		result.Intent.Reason = "Archive originals are read-only; current-source policy and retained-copy integrity were not verified."
		result.Qualification += " Archive access is read-only and verification is not_checked."
		return result, nil
	}
	if ref != "box" {
		return result, nil
	}
	handle, err := s.openRoot(root)
	if err != nil {
		return Explanation{}, err
	}
	defer handle.Close()
	read := func(relative string) ([]byte, error) { return readExplainDescriptor(ctx, handle, relative) }
	// Projects is a physical source convention, not registration evidence. An
	// exact project descriptor can explain itself without Box initialization.
	if withinExplainPath("Projects", relative) {
		result.Intent.explainProjectAncestors(read, "Projects", relative, metadata.Kind)
		return result, ctx.Err()
	}
	raw, err := read(".loom/box.yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		result.Intent.unavailable(".loom/box.yaml", err)
		return result, ctx.Err()
	}
	contract, err := box.ParseContract(raw)
	if err != nil {
		result.Intent.unavailable(".loom/box.yaml", err)
		return result, nil
	}
	// root_path is descriptive metadata, never authority to open another root.
	if filepath.Clean(contract.RootPath) != filepath.Clean(root.SourcePath) {
		result.Intent.unavailable(".loom/box.yaml", fmt.Errorf("descriptor root_path does not match the configured source root"))
		return result, nil
	}
	area, err := explainBoxArea(contract, relative)
	if err != nil {
		result.Intent.unavailable(".loom/box.yaml", err)
		return result, nil
	}
	if area == "" {
		return result, nil
	}
	if area == box.AreaProjects {
		result.Intent.explainProjectAncestors(read, contract.Areas[area].Path, relative, metadata.Kind)
		return result, ctx.Err()
	}
	switch area {
	case box.AreaNotes, box.AreaDocuments, box.AreaLaunchpad, "topics", "library":
		if err := result.Intent.explainBox(read, contract, area, relative); err != nil {
			result.Intent.unavailable(".loom/box.yaml", err)
		}
	default:
		result.Intent.Reason = "This Box area has a separate owner; no watched-root defaults were applied."
	}
	return result, ctx.Err()
}

func (intent *FileIntent) explainProjectAncestors(read func(string) ([]byte, error), base, relative, kind string) {
	start := relative
	if kind != "directory" {
		start = path.Dir(start)
	}
	for depth := 0; start != base && depth < 64; depth++ {
		found, err := intent.explainProject(read, start, relative)
		if err != nil {
			intent.unavailable(path.Join(start, projectcontracts.CanonicalRootContractPath), err)
			return
		}
		if found {
			return
		}
		start = path.Dir(start)
	}
	intent.Reason = "No owning project descriptor was found within the bounded ancestor lookup; defaults were not applied."
}

func (intent *FileIntent) unavailable(descriptor string, err error) {
	*intent = FileIntent{State: "unknown", Descriptor: descriptor, Search: "unknown", Sources: map[string]RuleSource{}, WatchPlan: "not_checked", Reason: "Descriptor intent unavailable; defaults were not applied: " + err.Error()}
}

func explainBoxArea(contract box.Contract, relative string) (string, error) {
	selected := ""
	for key, area := range contract.Areas {
		if !area.Enabled || !withinExplainPath(area.Path, relative) {
			continue
		}
		if selected != "" {
			return "", fmt.Errorf("overlapping Box areas own this path")
		}
		selected = key
	}
	return selected, nil
}

func withinExplainPath(base, relative string) bool {
	return base == "." || relative == base || strings.HasPrefix(relative, base+"/")
}

func readExplainDescriptor(ctx context.Context, root *os.Root, relative string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := normalizePath(relative); err != nil {
		return nil, err
	}
	if mandatoryExcluded(relative) {
		return nil, fs.ErrPermission
	}
	file, err := openNonblocking(root, relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if before.Size() > maxExplainDescriptorBytes {
		return nil, fmt.Errorf("descriptor exceeds %d bytes", maxExplainDescriptorBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxExplainDescriptorBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxExplainDescriptorBytes {
		return nil, fmt.Errorf("descriptor exceeds %d bytes", maxExplainDescriptorBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	proof, err := openNonblocking(root, relative)
	if err != nil {
		return nil, err
	}
	defer proof.Close()
	current, err := proof.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, current) || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() || !before.ModTime().Equal(current.ModTime()) || before.Size() != current.Size() {
		return nil, fmt.Errorf("descriptor changed during inspection")
	}
	return raw, ctx.Err()
}

func (intent *FileIntent) applyRules(policy *filepolicy.FolderPolicy, descriptor, relative string, authored *filepolicy.FolderPolicy) error {
	if policy == nil {
		return nil
	}
	decision, err := policy.Resolve(relative)
	if err != nil {
		return err
	}
	if decision.Search != "" {
		intent.Search = decision.Search
	}
	intent.Enrichment, intent.Protect = decision.Enrichment, decision.Protect
	for field, pattern := range decision.Sources {
		isDefault := true
		if authored != nil {
			for _, rule := range authored.Rules {
				if rule.Path == pattern && (field == "search" && rule.Search != "" || field == "protect" && rule.Protect != nil || field == "enrichment" && rule.Enrichment != nil) {
					isDefault = false
				}
			}
		}
		location := "files.rules." + field
		if isDefault {
			location = "schema_version"
		}
		intent.Sources[field] = RuleSource{Descriptor: descriptor, Field: location, Pattern: pattern, Default: isDefault}
	}
	return nil
}

func (intent *FileIntent) explainBox(read func(string) ([]byte, error), contract box.Contract, area, relative string) error {
	declared := contract.Areas[area]
	policy := declared.Watch
	descriptor, field := ".loom/box.yaml", "areas."+area+".watch."
	if policy == nil && contract.Policies[area] != "" {
		descriptor, field = contract.Policies[area], ""
		raw, err := read(descriptor)
		if err != nil {
			return fmt.Errorf("%s: %w", descriptor, err)
		}
		var parsed box.WatchPolicy
		if err := decodeExplainYAML(raw, &parsed); err != nil {
			return fmt.Errorf("%s: %w", descriptor, err)
		}
		if (parsed.SchemaVersion != box.WatchPolicySchemaVersion && parsed.SchemaVersion != box.LegacyWatchPolicySchemaVersion) || parsed.Area != area || parsed.Mode != "watched_root" {
			return fmt.Errorf("%s: unsupported watch policy identity", descriptor)
		}
		// Reuse the Box validator on captured bytes instead of its unconfined
		// filesystem planner. Converting this temporary value writes no source.
		copy := contract
		copy.SchemaVersion = box.FolderPolicySchemaVersion
		copy.Areas = map[string]box.Area{}
		for key, value := range contract.Areas {
			copy.Areas[key] = value
		}
		copy.Policies = map[string]string{}
		for key, value := range contract.Policies {
			if key != area {
				copy.Policies[key] = value
			}
		}
		declared.Watch = &parsed
		copy.Areas[area] = declared
		if err := box.ValidateContract(copy); err != nil {
			return err
		}
		policy = &parsed
	}
	if policy == nil {
		intent.Reason = "The owning Box area has no watch policy; defaults were not applied."
		return nil
	}
	if policy.Path != "" && policy.Path != declared.Path {
		return fmt.Errorf("%s: policy is not rooted at the known Box area", descriptor)
	}
	intent.State, intent.Descriptor, intent.OwnerPath = "known", ".loom/box.yaml", declared.Path
	intent.Reason = "Authored Box policy intent only; .loomignore and runtime consumer admission were not checked."
	intent.Search = "none"
	searchField := "enabled"
	if policy.Enabled {
		if policy.Text.Enabled || policy.Index.Enabled {
			intent.Search, searchField = "text", "index.enabled"
			if policy.Text.Enabled {
				searchField = "text.enabled"
			}
		} else if policy.Catalog.Enabled {
			intent.Search, searchField = "metadata", "catalog.enabled"
		}
	}
	protect := policy.Enabled && policy.Backup.Enabled
	intent.Protect = &protect
	intent.Sources["search"] = RuleSource{Descriptor: descriptor, Field: field + searchField}
	protectField := "backup.enabled"
	if !policy.Enabled {
		protectField = "enabled"
	}
	intent.Sources["protect"] = RuleSource{Descriptor: descriptor, Field: field + protectField}
	baseProtect := intent.Protect
	if err := intent.applyRules(contract.Files, ".loom/box.yaml", relative, contract.Files); err != nil {
		return err
	}
	if intent.Protect == nil {
		intent.Protect = baseProtect
	}
	return nil
}

// decodeExplainYAML also preserves scalar types in compatibility descriptors.
func decodeExplainYAML(raw []byte, value any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one YAML document")
	}
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return err
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	strict := json.NewDecoder(bytes.NewReader(encoded))
	strict.DisallowUnknownFields()
	return strict.Decode(value)
}

func (intent *FileIntent) explainProject(read func(string) ([]byte, error), owner, relative string) (bool, error) {
	canonical := path.Join(owner, projectcontracts.CanonicalRootContractPath)
	legacy := path.Join(owner, projectcontracts.LegacyRootContractPath)
	raw, err := read(canonical)
	old, oldErr := read(legacy)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true, err
	}
	if oldErr != nil && !errors.Is(oldErr, fs.ErrNotExist) {
		return true, oldErr
	}
	if errors.Is(err, fs.ErrNotExist) && errors.Is(oldErr, fs.ErrNotExist) {
		return false, nil
	}
	descriptor := canonical
	if errors.Is(err, fs.ErrNotExist) {
		raw, descriptor = old, legacy
	}
	d, err := projectcontracts.ParseProjectDeclaration(raw)
	if err != nil {
		// Older strict contracts remain readable, but their separate facet/policy
		// closure is not inferred from a filename or filesystem-first defaults.
		var oldContract projectcontracts.ProjectContract
		if decodeExplainYAML(raw, &oldContract) != nil || (oldContract.SchemaVersion != projectcontracts.ProjectSchemaV03 && oldContract.SchemaVersion != projectcontracts.ProjectSchemaV04) {
			return true, fmt.Errorf("%s: %w", descriptor, err)
		}
		intent.Descriptor, intent.OwnerPath = descriptor, owner
		intent.Reason = "Legacy project descriptor found; separate owner policies were not checked and new defaults were not applied."
		return true, nil
	}
	if descriptor == canonical && oldErr == nil {
		other, err := projectcontracts.ParseProjectDeclaration(old)
		if err != nil || !reflect.DeepEqual(d, other) {
			return true, fmt.Errorf("canonical and legacy project descriptors disagree")
		}
	}
	local := strings.TrimPrefix(relative, owner+"/")
	if relative == owner {
		local = "."
	}
	intent.Descriptor, intent.OwnerPath = descriptor, owner
	keys := make([]string, 0, len(d.Resources))
	for key := range d.Resources {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	var selected projectcontracts.ResourceKey
	for _, name := range keys {
		key := projectcontracts.ResourceKey(name)
		knowledge := d.Resources[key].Knowledge
		if knowledge == nil || knowledge.ApplicationData != nil || !withinExplainPath(knowledge.Path, local) {
			continue
		}
		if selected != "" {
			return true, fmt.Errorf("overlapping knowledge resources own this path")
		}
		selected = key
	}
	if selected == "" {
		intent.Reason = "No ordinary knowledge resource owns this path; new defaults were not applied."
		return true, nil
	}
	policy, err := projectcontracts.DeclarationKnowledgePolicy(d, selected)
	if err != nil {
		return true, err
	}
	intent.State = "known"
	intent.Reason = "Authored project policy intent only; consumer admission and runtime state were not checked."
	if policy != nil {
		if err := intent.applyRules(policy.Files, descriptor, local, d.Files); err != nil {
			return true, err
		}
	}
	// The source-only compiler consumes the exact captured root, never a second
	// unbounded filesystem read. Complex external policy closures stay unknown.
	ref := strings.TrimPrefix(descriptor, owner+"/")
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	loaded := projectcontracts.LoadedProject{RootPath: "/project", ContractPath: filepath.Join("/project", ref), Raw: raw, Declaration: &d}
	c := projectcontracts.DeclarationCompilation{Document: d, Sources: []projectcontracts.DeclarationSourceSnapshot{{DeclarationSource: projectcontracts.DeclarationSource{Ref: ref, SchemaVersion: d.SchemaVersion, Hash: hash, Revision: hash}, Raw: raw}}, Protection: []projectcontracts.DeclarationProtectionSelection{}}
	roots, compileErr := projectcontracts.CompileDeclarationEnrollment(loaded, c)
	if compileErr != nil {
		intent.Reason += " Full watch intent needs the existing project owner's separate policy closure."
		return true, nil
	}
	intent.WatchPlan = "compiled_intent_only"
	for _, root := range roots {
		if !withinExplainPath(root.RootRelativePath, local) {
			continue
		}
		var config watchedroots.RootConfig
		if err := json.Unmarshal(root.ConfigJSON, &config); err != nil {
			return true, err
		}
		if intent.Search == "unknown" {
			intent.Search = explainSearchMode(config.IndexPolicy.Mode)
			intent.Sources["search"] = RuleSource{Descriptor: descriptor, Field: "resources." + string(selected) + ".knowledge", Default: true}
		}
		if intent.Protect == nil {
			protect := config.BackupPolicy.Mode != watchedroots.BackupModeNone
			intent.Protect = &protect
			intent.Sources["protect"] = RuleSource{Descriptor: descriptor, Field: "resources." + string(selected) + ".knowledge.protection", Default: true}
		}
	}
	return true, nil
}

func explainSearchMode(mode string) string {
	switch mode {
	case watchedroots.IndexModeNone:
		return "none"
	case watchedroots.IndexModeMetadataOnly:
		return "metadata"
	case watchedroots.IndexModeMarkdownText:
		return "text"
	default:
		return "unknown"
	}
}
