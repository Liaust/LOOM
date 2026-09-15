package projectcontracts

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const LegacyOwnerTransitionRequired = "legacy_owner_transition_required"

// DeclarationContributor is source-derived planning identity, never an owner
// receipt. Native and imported watches use the same deterministic projection.
type DeclarationContributor struct {
	Key   ResourceKey           `json:"key"`
	Owner DeclarationOwner      `json:"owner"`
	Kind  DeclarationActionKind `json:"kind"`
}

type DeclarationLegacyCompilation struct {
	Notes            []NotesFacetItem         `json:"notes"`
	Repos            []RepoFacetItem          `json:"repos"`
	DerivedProviders []PlanProvider           `json:"derived_providers"`
	Contributors     []DeclarationContributor `json:"contributors"`
}

func legacyReferences(d ProjectDeclaration) []LegacyProjectReference {
	if d.LegacyContracts == nil {
		return nil
	}
	refs := []LegacyProjectReference{d.LegacyContracts.Project}
	for _, r := range []*LegacyWatchReference{d.LegacyContracts.Notes, d.LegacyContracts.Repos} {
		if r != nil {
			refs = append(refs, LegacyProjectReference{Ref: r.Ref, SchemaVersion: r.SchemaVersion, Digest: r.Digest})
		}
	}
	return refs
}

func declarationLegacyShape(d ProjectDeclaration) error {
	if d.LegacyContracts == nil {
		return nil
	}
	invalid := func() error { return fmt.Errorf("declaration.legacy_reference: invalid legacy reference") }
	seen := map[string]bool{}
	for _, r := range legacyReferences(d) {
		if !declarationPath(r.Ref, true) || r.Ref == CanonicalRootContractPath || r.Ref == LegacyRootContractPath || !migrationDigestValid(r.Digest) || seen[r.Ref] {
			return invalid()
		}
		seen[r.Ref] = true
	}
	p := d.LegacyContracts.Project
	if p.SchemaVersion != ProjectSchemaV03 && p.SchemaVersion != ProjectSchemaV04 {
		return invalid()
	}
	keys := map[ResourceKey]bool{}
	for i, r := range []*LegacyWatchReference{d.LegacyContracts.Notes, d.LegacyContracts.Repos} {
		if r == nil {
			continue
		}
		kind, version := ProjectContractNotes, NotesSchemaV03
		if i == 1 {
			kind, version = ProjectContractRepos, ReposSchemaV04
		}
		def := singletonContractDefinitions[kind]
		if r.SchemaVersion != version || (r.Ref != def.Canonical && r.Ref != def.Legacy) || !declarationKey(string(r.Key)) || keys[r.Key] || !declarationRef(d, r.Protection, DeclarationProtection) {
			return invalid()
		}
		if _, exists := d.Resources[r.Key]; exists {
			return invalid()
		}
		keys[r.Key] = true
	}
	return nil
}

// DeclarationContributors is the sole projection of watch action identities.
// Owner adapters remain guarded until the separate adoption slice.
func DeclarationContributors(d ProjectDeclaration) ([]DeclarationContributor, error) {
	if err := declarationLegacyShape(d); err != nil {
		return nil, err
	}
	out := []DeclarationContributor{}
	for key, r := range d.Resources {
		switch r.Kind {
		case DeclarationKnowledge:
			out = append(out, DeclarationContributor{key, DeclarationOwnerKnowledge, DeclarationEnrollKnowledge})
		case DeclarationProtection:
			out = append(out, DeclarationContributor{key, DeclarationOwnerProtection, DeclarationReconcileProtection})
		}
	}
	if d.LegacyContracts != nil {
		if r := d.LegacyContracts.Notes; r != nil {
			out = append(out, DeclarationContributor{r.Key, DeclarationOwnerKnowledge, DeclarationEnrollKnowledge})
		}
		if r := d.LegacyContracts.Repos; r != nil {
			out = append(out, DeclarationContributor{r.Key, DeclarationOwnerProtection, DeclarationReconcileProtection})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func decodeLegacySource(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > 4*1024*1024 {
		return fmt.Errorf("legacy source bound")
	}
	tree, err := migrationTree(raw)
	if err != nil || !declarationNodeShape(tree, reflect.TypeOf(target).Elem()) {
		return fmt.Errorf("legacy source shape")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err = decoder.Decode(target); err != nil {
		return fmt.Errorf("legacy source fields")
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("legacy source stream")
	}
	return nil
}

func legacyProjectSubset(original ProjectContract, d ProjectDeclaration) error {
	fail := func() error { return fmt.Errorf("unsupported legacy project intent") }
	if original.Kind != ProjectKind || original.SchemaVersion != d.LegacyContracts.Project.SchemaVersion {
		return fail()
	}
	before := NormalizeContract(original).Project
	after := NormalizeContract(ProjectContract{Project: d.Project}).Project
	if original.SchemaVersion == ProjectSchemaV03 {
		if original.Project.ID != "" {
			return fail()
		}
		before.ID = after.ID
	}
	if before != after {
		return fmt.Errorf("legacy project identity mismatch")
	}
	for key, enabled := range original.Facets {
		if !enabled || (key != "notes" && key != "repos" && key != "backup_policy" && key != "portal") {
			return fail()
		}
	}
	if original.Facets["notes"] != (d.LegacyContracts.Notes != nil) || original.Facets["repos"] != (d.LegacyContracts.Repos != nil) {
		return fmt.Errorf("legacy selected source mismatch")
	}
	for _, value := range []string{original.ProviderDefaults.ScriptsProvider, original.ProviderDefaults.WorkflowsProvider} {
		if value != "" && value != "project" {
			return fail()
		}
	}
	if original.Policies.Sync != "" || original.Policies.Workers != "" || original.Policies.Credentials != "" {
		return fail()
	}
	for _, value := range []string{original.Portal.DisplayGroup, original.Portal.Summary} {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fail()
		}
	}
	for key, value := range original.Metadata {
		text, ok := value.(string)
		if !ok || (key != "scaffold_preset" && key != "scaffolded_by") || strings.IndexFunc(text, unicode.IsControl) >= 0 {
			return fail()
		}
	}
	return nil
}

// CompileDeclarationLegacyReferences resolves exclusively from captured bytes.
// It does not discover files, create retained sources or authorize registration.
func CompileDeclarationLegacyReferences(loaded LoadedProject, c DeclarationCompilation) (*DeclarationLegacyCompilation, error) {
	d := c.Document
	if d.LegacyContracts == nil {
		return nil, nil
	}
	if err := declarationLegacyShape(d); err != nil {
		return nil, err
	}
	parsed, err := ParseProjectDeclaration(loaded.Raw)
	if err != nil || !reflect.DeepEqual(parsed, d) {
		return nil, fmt.Errorf("legacy active source mismatch")
	}
	rootRef, err := filepath.Rel(loaded.RootPath, loaded.ContractPath)
	if err != nil {
		return nil, fmt.Errorf("legacy active source location mismatch")
	}
	rootRef = filepath.ToSlash(rootRef)
	allowed := map[string]bool{rootRef: true}
	for _, r := range legacyReferences(d) {
		allowed[r.Ref] = true
	}
	for _, r := range d.Resources {
		// The first reference subset has no member/material/application mapping.
		if r.Protection == nil {
			return nil, fmt.Errorf("unsupported native intent in legacy conversion")
		}
		allowed[r.Protection.PolicyRef] = true
	}
	if rootRef == CanonicalRootContractPath {
		allowed[LegacyRootContractPath] = true
	}
	for _, kind := range []string{ProjectContractNotes, ProjectContractRepos, ProjectContractBackup} {
		def := singletonContractDefinitions[kind]
		if allowed[def.Canonical] || allowed[def.Legacy] {
			allowed[def.Canonical], allowed[def.Legacy] = true, true
		}
	}
	sources := map[string]DeclarationSourceSnapshot{}
	for _, s := range c.Sources {
		if _, seen := sources[s.Ref]; seen || !allowed[s.Ref] || s.Hash != declarationHash(s.Raw) || s.Revision != s.Hash {
			return nil, fmt.Errorf("legacy snapshot mismatch")
		}
		sources[s.Ref] = s
	}
	active, ok := sources[rootRef]
	if !ok || active.SchemaVersion != ProjectSchemaV05 || !bytes.Equal(active.Raw, loaded.Raw) {
		return nil, fmt.Errorf("legacy active snapshot mismatch")
	}
	if rootRef == CanonicalRootContractPath {
		if other, ok := sources[LegacyRootContractPath]; ok {
			duplicate, err := ParseProjectDeclaration(other.Raw)
			if err != nil || other.SchemaVersion != ProjectSchemaV05 || !reflect.DeepEqual(duplicate, d) {
				return nil, fmt.Errorf("legacy active layout conflict")
			}
		}
	}
	for _, kind := range []string{ProjectContractNotes, ProjectContractRepos, ProjectContractBackup} {
		def := singletonContractDefinitions[kind]
		a, ap := sources[def.Canonical]
		b, bp := sources[def.Legacy]
		if ap && bp {
			equal, err := yamlDocumentsSemanticallyEqual(a.Raw, b.Raw)
			if err != nil || !equal || a.SchemaVersion != b.SchemaVersion || decodeLegacySource(b.Raw, migrationSingletonType(kind)) != nil {
				return nil, fmt.Errorf("legacy singleton source conflict")
			}
			for _, r := range []*LegacyWatchReference{d.LegacyContracts.Notes, d.LegacyContracts.Repos} {
				if r != nil && r.Ref == def.Legacy {
					return nil, fmt.Errorf("legacy original selection mismatch")
				}
			}
		}
	}
	get := func(r LegacyProjectReference, target any, kind string) error {
		s, ok := sources[r.Ref]
		if !ok || s.Hash != r.Digest || s.SchemaVersion != r.SchemaVersion {
			return fmt.Errorf("legacy selected source missing or changed")
		}
		if err := decodeLegacySource(s.Raw, target); err != nil {
			return err
		}
		var header struct {
			Kind   string `yaml:"kind"`
			Schema string `yaml:"schema_version"`
		}
		if err := yaml.Unmarshal(s.Raw, &header); err != nil || header.Kind != kind || header.Schema != r.SchemaVersion {
			return fmt.Errorf("legacy source identity mismatch")
		}
		return nil
	}
	var original ProjectContract
	if err := get(d.LegacyContracts.Project, &original, ProjectKind); err != nil {
		return nil, err
	}
	if err := legacyProjectSubset(original, d); err != nil {
		return nil, err
	}
	out := &DeclarationLegacyCompilation{Notes: []NotesFacetItem{}, Repos: []RepoFacetItem{}, DerivedProviders: []PlanProvider{}}
	var compileErr error
	add := func(diag Diagnostic) {
		if diag.Severity == SeverityError && compileErr == nil {
			compileErr = fmt.Errorf("legacy compiler: %s", diag.Code)
		}
	}
	out.DerivedProviders = derivedProviders(NormalizeContract(original), d.LegacyContracts.Project.Ref, add)
	if r := d.LegacyContracts.Notes; r != nil {
		var n NotesContract
		if err := get(LegacyProjectReference{r.Ref, r.SchemaVersion, r.Digest}, &n, NotesContractKind); err != nil {
			return nil, err
		}
		if len(n.Material) != 0 || len(n.Metadata) != 0 {
			return nil, fmt.Errorf("unsupported legacy Notes extension")
		}
		out.Notes = notesFacetItems(n, sources[r.Ref].Raw, filepath.Join(loaded.RootPath, filepath.FromSlash(r.Ref)), nil, add)
	}
	if r := d.LegacyContracts.Repos; r != nil {
		var repos ReposContract
		if err := get(LegacyProjectReference{r.Ref, r.SchemaVersion, r.Digest}, &repos, ReposContractKind); err != nil {
			return nil, err
		}
		tree, _ := migrationTree(sources[r.Ref].Raw)
		members := migrationNode(tree, "repos.members")
		normalized := normalizeReposContract(repos)
		if len(repos.Repos.Members) != 0 || members == nil || members.Kind != yaml.SequenceNode || len(repos.Repos.Roots) != 0 || len(repos.Metadata) != 0 || boolValue(normalized.Repos.Defaults.Sync, false) || boolValue(normalized.Repos.Defaults.Index, false) {
			return nil, fmt.Errorf("unsupported legacy repository membership or watch defaults")
		}
		out.Repos = repoFacetItems(repos, sources[r.Ref].Raw, filepath.Join(loaded.RootPath, filepath.FromSlash(r.Ref)), nil, add)
		for _, r := range out.Repos {
			if r.Sync || r.Index {
				return nil, fmt.Errorf("unsupported legacy repository sync/index watch")
			}
		}
	}
	if compileErr != nil {
		return nil, compileErr
	}
	// Exact protection attachment, source identity and local key are mandatory.
	backupRef := original.Policies.Backup
	if backupRef == "" && original.Facets["backup_policy"] {
		backupRef = singletonContractDefinitions[ProjectContractBackup].Canonical
	}
	for _, resource := range d.Resources {
		if resource.Protection.PolicyRef != backupRef {
			return nil, fmt.Errorf("legacy conversion adds an unselected protection source")
		}
	}
	check := func(ref *LegacyWatchReference, key, path string) error {
		protection := d.Resources[ref.Protection].Protection
		if protection == nil || backupRef == "" || protection.PolicyRef != backupRef || protection.Path != path {
			return fmt.Errorf("legacy protection attachment mismatch")
		}
		var match bool
		for _, selection := range c.Protection {
			if selection.Resource == ref.Protection {
				for _, root := range selection.Roots {
					if root.Key == key && root.Path == path {
						match = true
					}
				}
			}
		}
		if !match {
			return fmt.Errorf("legacy protection owner key mismatch")
		}
		return nil
	}
	seen := map[string]string{}
	for _, n := range out.Notes {
		if err := check(d.LegacyContracts.Notes, n.RootKey, n.ProjectPath); err != nil {
			return nil, err
		}
		seen[n.RootKey] = n.ProjectPath
	}
	for _, r := range out.Repos {
		if _, exists := seen[r.Key]; exists {
			return nil, fmt.Errorf("legacy watch owner collision")
		}
		seen[r.Key] = r.ProjectPath
		if err := check(d.LegacyContracts.Repos, r.Key, r.ProjectPath); err != nil {
			return nil, err
		}
	}
	// A retained backup policy cannot silently lose roots during representation.
	if backupRef != "" {
		source, ok := sources[backupRef]
		if !ok {
			return nil, fmt.Errorf("legacy backup source missing")
		}
		var policy ProjectBackupPolicyContract
		if err := decodeLegacySource(source.Raw, &policy); err != nil {
			return nil, err
		}
		if policy.Kind != BackupPolicyKind || policy.SchemaVersion != BackupPolicySchemaV03 || source.SchemaVersion != BackupPolicySchemaV03 {
			return nil, fmt.Errorf("legacy backup identity mismatch")
		}
		normalized := normalizeBackupPolicy(policy)
		for _, root := range normalized.Backup.Roots {
			matched := false
			for _, s := range c.Protection {
				if s.PolicyRef == backupRef {
					for _, target := range s.Roots {
						if declarationJSONEqual(root, target) {
							matched = true
						}
					}
				}
			}
			if !matched {
				return nil, fmt.Errorf("legacy backup root not retained")
			}
		}
	}
	out.Contributors, compileErr = DeclarationContributors(d)
	return out, compileErr
}

// Compare the complete original owner projection, including first-contributor
// metadata and display name. Only new declaration attribution may be added.
func validateLegacyEnrollment(loaded LoadedProject, c DeclarationCompilation, legacy *DeclarationLegacyCompilation, after []ProjectWatchedRootItem) error {
	var original ProjectContract
	sources := map[string]DeclarationSourceSnapshot{}
	for _, source := range c.Sources {
		sources[source.Ref] = source
	}
	if err := decodeLegacySource(sources[c.Document.LegacyContracts.Project.Ref].Raw, &original); err != nil {
		return err
	}
	var compileErr error
	builder := newWatchPolicyBuilder(loaded, NormalizeContract(original), func(d Diagnostic) {
		if d.Severity == SeverityError {
			compileErr = fmt.Errorf("legacy original owner configuration invalid")
		}
	})
	builder.sourceOnly = true
	for _, n := range legacy.Notes {
		builder.addNotes(n)
	}
	for _, r := range legacy.Repos {
		builder.addRepo(r)
	}
	ref := original.Policies.Backup
	if ref == "" && original.Facets["backup_policy"] {
		ref = singletonContractDefinitions[ProjectContractBackup].Canonical
	}
	if ref != "" {
		var policy ProjectBackupPolicyContract
		if err := decodeLegacySource(sources[ref].Raw, &policy); err != nil {
			return err
		}
		builder.addBackupPolicySource(normalizeBackupPolicy(policy), ref)
	}
	before := builder.items()
	if compileErr != nil {
		return compileErr
	}
	if len(before) != len(after) {
		return fmt.Errorf("legacy owner set changed")
	}
	for i, got := range after {
		metadata := map[string]any{}
		for k, v := range got.Metadata {
			if k != "declaration_sources" {
				metadata[k] = v
			}
		}
		got.Metadata = metadata
		if !declarationJSONEqual(before[i], got) {
			return fmt.Errorf("legacy complete owner intent changed")
		}
	}
	return nil
}
