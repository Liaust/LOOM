package projectcontracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/ids"
)

// ParseProjectDeclaration strictly decodes the frozen v0.5 source contract.
// It does not resolve target ownership or grant execution authority.
func ParseProjectDeclaration(raw []byte) (ProjectDeclaration, error) {
	d, code := parseDeclarationShape(raw)
	if code != "" {
		return d, fmt.Errorf("%s: invalid project declaration", code)
	}
	return d, nil
}

func declarationKeys[T any](m map[ResourceKey]T) []ResourceKey {
	keys := make([]ResourceKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func parseDeclarationShape(raw []byte) (ProjectDeclaration, string) {
	var d ProjectDeclaration
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&d); err != nil {
		return d, "declaration.unknown_field"
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return d, "declaration.stream"
	}
	var tree yaml.Node
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return d, "declaration.stream"
	}
	var aliases func(*yaml.Node) bool
	aliases = func(n *yaml.Node) bool {
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return true
		}
		for _, c := range n.Content {
			if aliases(c) {
				return true
			}
		}
		return false
	}
	if aliases(&tree) {
		return d, "declaration.stream"
	}
	if len(tree.Content) != 1 || !declarationNodeShape(tree.Content[0], reflect.TypeOf(d)) {
		return d, "declaration.shape"
	}
	if d.Kind != ProjectKind {
		return d, "declaration.kind"
	}
	if d.SchemaVersion != ProjectSchemaV05 {
		return d, "declaration.schema"
	}
	if ids.Validate(ids.ProjectPrefix, d.Project.ID) != nil {
		return d, "declaration.identity"
	}
	if !ValidProjectSlug(d.Project.Slug) || strings.TrimSpace(d.Project.Name) == "" || !nodeKeyPattern.MatchString(d.Project.OwnerNode) || d.Resources == nil {
		return d, "declaration.required"
	}
	if d.Project.Status != "" && d.Project.Status != ProjectStatusDraft && d.Project.Status != ProjectStatusActive && d.Project.Status != ProjectStatusPaused && d.Project.Status != ProjectStatusArchived {
		return d, "declaration.kind"
	}
	paths := map[DeclarationResourceKind][]string{}
	repoIDs := map[string]bool{}
	primary := 0
	for _, key := range declarationKeys(d.Resources) {
		r := d.Resources[key]
		if !declarationKey(string(key)) {
			return d, "declaration.key"
		}
		payloads := 0
		for _, present := range []bool{r.Repository != nil, r.Knowledge != nil, r.Protection != nil, r.Application != nil} {
			if present {
				payloads++
			}
		}
		switch r.Kind {
		case DeclarationRepository, DeclarationKnowledge, DeclarationProtection, DeclarationApplication:
		default:
			return d, "declaration.kind"
		}
		if payloads != 1 {
			return d, "declaration.union"
		}
		var p string
		var protection ResourceKey
		switch r.Kind {
		case DeclarationRepository:
			if r.Repository == nil {
				return d, "declaration.union"
			}
			v := r.Repository
			p = v.Path
			protection = v.Protection
			if !declarationRepositoryStateRoot(v.StateRoot) {
				return d, "declaration.path"
			}
			if v.ID != "" {
				if ids.Validate(RepositoryIDPrefix, v.ID) != nil || repoIDs[v.ID] {
					return d, "declaration.identity"
				}
				repoIDs[v.ID] = true
			}
			switch v.Role {
			case RepositoryRolePrimary:
				primary++
			case RepositoryRoleComponent:
			case RepositoryRoleReference:
				if v.ID == "" {
					return d, "declaration.identity"
				}
			default:
				return d, "declaration.kind"
			}
		case DeclarationKnowledge:
			if r.Knowledge == nil {
				return d, "declaration.union"
			}
			p = r.Knowledge.Path
			protection = r.Knowledge.Protection
			switch r.Knowledge.Category {
			case KnowledgeCategoryNotes, KnowledgeCategoryDocs, KnowledgeCategoryResearch:
			default:
				return d, "declaration.kind"
			}
		case DeclarationProtection:
			if r.Protection == nil {
				return d, "declaration.union"
			}
			if !declarationPath(r.Protection.PolicyRef, true) {
				return d, "declaration.reference"
			}
			p = r.Protection.Path
		case DeclarationApplication:
			if r.Application == nil {
				return d, "declaration.union"
			}
			a := r.Application
			if !declarationRef(d, a.Repository, DeclarationRepository) {
				return d, "declaration.reference"
			}
			if !declarationPath(a.Manifest, true) {
				return d, "declaration.path"
			}
			if a.ArtifactDescriptor != "" && (!declarationPath(a.ArtifactDescriptor, true) || a.ArtifactDescriptor == a.Manifest) {
				return d, "declaration.path"
			}
			seenCreds := map[string]bool{}
			for _, ref := range a.Credentials {
				if !validCredentialRef(ref) {
					return d, "declaration.reference"
				}
				if seenCreds[ref] {
					return d, "declaration.conflict"
				}
				seenCreds[ref] = true
			}
			if len(a.CredentialSources) > 16 || (len(a.CredentialSources) != 0 && (a.ArtifactDescriptor == "" || len(a.CredentialSources) != len(a.Credentials))) {
				return d, "declaration.credential_sources"
			}
			for ref, source := range a.CredentialSources {
				if _, _, ok := ApplicationCredentialSourceShare(source); !ok || !seenCreds[ref] {
					return d, "declaration.credential_sources"
				}
			}
			if e := a.Endpoint; e != nil {
				if e.Hostname != "" && (e.Exposure != ApplicationPublicHTTPS || e.EndpointRef != "" || a.ArtifactDescriptor == "" || !ValidApplicationHostname(e.Hostname)) {
					return d, "declaration.application_hostname"
				}
				switch e.Exposure {
				case ApplicationLoopback:
				case ApplicationPrivate:
					if !validCredentialRef(e.EndpointRef) {
						return d, "declaration.reference"
					}
				case ApplicationPublicHTTPS:
					if e.Hostname == "" && !validCredentialRef(e.EndpointRef) {
						return d, "declaration.reference"
					}
				default:
					return d, "declaration.kind"
				}
				if e.EndpointRef != "" && !validCredentialRef(e.EndpointRef) {
					return d, "declaration.reference"
				}
			}
			for _, dataKey := range declarationKeys(a.Data) {
				v := a.Data[dataKey]
				if v.Backup != "" && (v.Backup != "cloud_history" || v.BindingRef == "" || v.Protection != "" || a.ArtifactDescriptor == "") {
					return d, "declaration.application_backup"
				}
				if !declarationKey(string(dataKey)) {
					return d, "declaration.key"
				}
				if (v.Path == "") == (v.BindingRef == "") {
					return d, "declaration.conflict"
				}
				if v.Path != "" {
					if !declarationPath(v.Path, false) {
						return d, "declaration.path"
					}
					paths[DeclarationApplication] = append(paths[DeclarationApplication], v.Path)
				}
				if v.BindingRef != "" && !validCredentialRef(v.BindingRef) {
					return d, "declaration.reference"
				}
				if v.Capacity != nil && v.Capacity.PlannedBytes == 0 {
					return d, "declaration.capacity"
				}
				if v.Protection != "" && !declarationRef(d, v.Protection, DeclarationProtection) {
					return d, "declaration.reference"
				}
			}
		}
		if protection != "" && !declarationRef(d, protection, DeclarationProtection) {
			return d, "declaration.reference"
		}
		if r.Kind == DeclarationRepository || r.Kind == DeclarationKnowledge || p != "" {
			if !declarationPath(p, false) && !(r.Kind == DeclarationProtection && p == ".") {
				return d, "declaration.path"
			}
			paths[r.Kind] = append(paths[r.Kind], p)
		}
	}
	if primary > 1 {
		return d, "declaration.conflict"
	}
	for kind, ps := range paths {
		for i, p := range ps {
			for _, q := range ps[:i] {
				// Policy compilation owns exact sharing and explicit root coverage:
				// it has the source, selected keys and enabled configuration.
				if kind == DeclarationProtection && (p == "." || q == "." || p == q) {
					continue
				}
				if declarationOverlap(p, q) {
					return d, "declaration.overlap"
				}
			}
		}
		if kind == DeclarationApplication {
			for _, p := range ps {
				for _, other := range []DeclarationResourceKind{DeclarationRepository, DeclarationKnowledge} {
					for _, q := range paths[other] {
						if declarationOverlap(p, q) {
							return d, "declaration.overlap"
						}
					}
				}
			}
		}
	}
	if err := declarationLegacyShape(d); err != nil {
		return d, "declaration.legacy_reference"
	}
	return d, ""
}

var declarationLocalKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// yaml.v3 permits scalar coercion; preserve the declared scalar types exactly.
func declarationNodeShape(n *yaml.Node, typ reflect.Type) bool {
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return false
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Tag != "!!str" || seen[key.Value] {
				return false
			}
			seen[key.Value] = true
		}
	}
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.String:
		return n.Kind == yaml.ScalarNode && n.Tag == "!!str"
	case reflect.Bool:
		return n.Kind == yaml.ScalarNode && n.Tag == "!!bool"
	case reflect.Int, reflect.Int64:
		return n.Kind == yaml.ScalarNode && n.Tag == "!!int"
	case reflect.Interface:
		if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
			for _, child := range n.Content {
				if !declarationNodeShape(child, typ) {
					return false
				}
			}
		}
		return n.Kind != yaml.AliasNode
	case reflect.Uint64:
		return n.Kind == yaml.ScalarNode && n.Tag == "!!int" && regexp.MustCompile(`^[0-9]+$`).MatchString(n.Value)
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return false
		}
		for _, child := range n.Content {
			if !declarationNodeShape(child, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return false
		}
		for i := 0; i < len(n.Content); i += 2 {
			if !declarationNodeShape(n.Content[i], typ.Key()) || !declarationNodeShape(n.Content[i+1], typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			fields[strings.Split(field.Tag.Get("yaml"), ",")[0]] = field.Type
		}
		for i := 0; i < len(n.Content); i += 2 {
			field, ok := fields[n.Content[i].Value]
			if !ok || n.Content[i].Tag != "!!str" || !declarationNodeShape(n.Content[i+1], field) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func declarationKey(s string) bool { return declarationLocalKey.MatchString(s) }

// ValidDeclarationRepositoryStateRoot shares the compiler's canonical optional
// development-state rules with declaration owners. It never normalizes input.
func ValidDeclarationRepositoryStateRoot(s string) bool {
	return declarationRepositoryStateRoot(s)
}

// Development-state intent is optional and uses the existing repository owner
// rules. Unlike general resource paths it may name .repo, never implicitly.
func declarationRepositoryStateRoot(s string) bool {
	if s == "" {
		return true
	}
	normalized, err := normalizeRepositoryStateRoot(s)
	return err == nil && normalized == s && !hasReservedRepositoryPath(s, true) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func declarationPath(s string, metadata bool) bool {
	if s == "" || s == "." || path.IsAbs(s) || path.Clean(s) != s || strings.ContainsAny(s, "\\:") || strings.IndexFunc(s, unicode.IsControl) >= 0 || strings.TrimSpace(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." || part == ".git" || part == ".repo" || (!metadata && part == ".loom") {
			return false
		}
	}
	return true
}
func declarationOverlap(a, b string) bool {
	return a == "." || b == "." || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// The same policy can explicitly retain its root and descendant owners. Other
// nested selections still conflict; identical source/root sharing enrolls once.
func declarationProtectionConflict(a, b DeclarationProtectionSelection) bool {
	if !a.Enabled || !b.Enabled {
		return false
	}
	for _, left := range a.Roots {
		for _, right := range b.Roots {
			if a.PolicyRef == b.PolicyRef && (reflect.DeepEqual(left, right) ||
				(left.Key != right.Key && left.Path != right.Path && (left.Path == "." || right.Path == "."))) {
				continue
			}
			if left.Key == right.Key || declarationOverlap(left.Path, right.Path) {
				return true
			}
		}
	}
	return false
}
func declarationRef(d ProjectDeclaration, k ResourceKey, kind DeclarationResourceKind) bool {
	r, ok := d.Resources[k]
	return ok && r.Kind == kind
}

// DeclarationCompilation is a source-only analysis, not an executable plan or
// an enrollment receipt. D2/D3 consume explicit intent; E supplies application
// semantics. Keeping it separate prevents legacy facet consumers from guessing.
type DeclarationCompilation struct {
	LegacyContracts *DeclarationLegacyCompilation    `json:"legacy_contracts,omitempty"`
	Document        ProjectDeclaration               `json:"document"`
	Sources         []DeclarationSourceSnapshot      `json:"sources"`
	Repositories    []RepoMemberSpec                 `json:"repositories"`
	Protection      []DeclarationProtectionSelection `json:"protection"`
	Errors          []DeclarationError               `json:"errors"`
}

type DeclarationSourceSnapshot struct {
	DeclarationSource
	Raw []byte `json:"raw"`
}

type DeclarationProtectionSelection struct {
	Resource     ResourceKey              `json:"resource"`
	PolicyRef    string                   `json:"policy_ref"`
	Enabled      bool                     `json:"enabled"`
	Roots        []BackupRootPolicySpec   `json:"roots"`
	WatchedRoots []ProjectWatchedRootItem `json:"watched_roots"`
}

func declarationHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func declarationSnapshot(ref, version string, raw []byte) DeclarationSourceSnapshot {
	hash := declarationHash(raw)
	return DeclarationSourceSnapshot{DeclarationSource: DeclarationSource{Ref: ref, SchemaVersion: version, Hash: hash, Revision: hash}, Raw: append([]byte{}, raw...)}
}

func parseProjectSource(raw []byte, absolutePath, displayPath string) (ProjectContract, *ProjectDeclaration, error) {
	var version struct {
		SchemaVersion string `yaml:"schema_version"`
	}
	// This probe only selects the strict decoder. It cannot admit a document.
	if err := yaml.Unmarshal(raw, &version); err != nil {
		return ProjectContract{}, nil, LoadError{Code: "contract.parse_failed", File: absolutePath, Err: err}
	}
	if version.SchemaVersion != ProjectSchemaV05 {
		c, err := parseProjectContract(raw, absolutePath, displayPath)
		return c, nil, err
	}
	d, err := ParseProjectDeclaration(raw)
	if err != nil {
		return ProjectContract{}, nil, LoadError{Code: string(DeclarationInvalid), File: absolutePath, Err: err}
	}
	return ProjectContract{Kind: d.Kind, SchemaVersion: d.SchemaVersion, Project: d.Project}, &d, nil
}

// The descriptor stays open through component checks and reads. Refuse symlinks
// and filesystem crossings even when their destinations happen to be in-tree.
func declarationPhysicalPath(root *os.Root, relative string, directory bool) error {
	base, err := root.Stat(".")
	if err != nil {
		return err
	}
	current := ""
	for i, part := range strings.Split(relative, "/") {
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not declaration custody: %s", relative)
		}
		if left, ok := base.Sys().(*syscall.Stat_t); ok {
			if right, ok := info.Sys().(*syscall.Stat_t); ok && left.Dev != right.Dev {
				return fmt.Errorf("declaration path crosses a filesystem: %s", relative)
			}
		}
		last := i == len(strings.Split(relative, "/"))-1
		if (!last || directory) && !info.IsDir() {
			return fmt.Errorf("declaration directory required: %s", relative)
		}
		if last && !directory && !info.Mode().IsRegular() {
			return fmt.Errorf("declaration source must be a regular file: %s", relative)
		}
	}
	return nil
}

func declarationReadSource(root *os.Root, relative string) ([]byte, error) {
	if !declarationPath(relative, true) {
		return nil, fmt.Errorf("unsafe declaration source path")
	}
	if err := declarationPhysicalPath(root, relative, false); err != nil {
		return nil, err
	}
	f, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Source declarations are small metadata, never arbitrary payload scans.
	raw, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4*1024*1024 {
		return nil, fmt.Errorf("declaration source exceeds 4 MiB")
	}
	if err := declarationPhysicalPath(root, relative, false); err != nil {
		return nil, err
	}
	after, err := root.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("declaration source changed during read")
	}
	return raw, nil
}

func validateDeclaration(loaded LoadedProject) ValidationReport {
	report := emptyReport(loaded.RootPath, loaded.ContractPath, nowUTC())
	report.Project = planProject(NormalizeContract(loaded.Contract))
	add := func(d Diagnostic) { report.Diagnostics = append(report.Diagnostics, d) }
	c := &DeclarationCompilation{Sources: []DeclarationSourceSnapshot{}, Repositories: []RepoMemberSpec{}, Protection: []DeclarationProtectionSelection{}, Errors: []DeclarationError{}}
	report.Declaration = c
	fail := func(key ResourceKey, field string, err error) {
		c.Errors = append(c.Errors, DeclarationError{Code: DeclarationInvalid, Resource: key, Field: field, Message: err.Error(), CompletedEffects: []string{}})
		add(Diagnostic{Severity: SeverityError, Code: string(DeclarationInvalid), Message: err.Error(), File: loaded.ContractPath, Field: field})
	}
	finish := func() ValidationReport {
		report.Diagnostics = sortDiagnostics(report.Diagnostics)
		report.Summary = summarizeDiagnostics(report.Diagnostics)
		report.OK = report.Summary.Errors == 0
		report.Registerable = report.OK && c.Document.LegacyContracts == nil
		return report
	}
	d, err := ParseProjectDeclaration(loaded.Raw)
	if err != nil {
		fail("", "", err)
		return finish()
	}
	if loaded.Declaration == nil || !reflect.DeepEqual(d, *loaded.Declaration) || !reflect.DeepEqual(loaded.Contract, ProjectContract{Kind: d.Kind, SchemaVersion: d.SchemaVersion, Project: d.Project}) {
		fail("", "project", fmt.Errorf("loaded declaration and source identity disagree"))
		return finish()
	}
	c.Document = d
	root, err := os.OpenRoot(loaded.RootPath)
	if err != nil {
		fail("", "", err)
		return finish()
	}
	defer root.Close()
	rootRef, err := filepath.Rel(loaded.RootPath, loaded.ContractPath)
	if err != nil {
		fail("", "", err)
		return finish()
	}
	rootRef = filepath.ToSlash(rootRef)
	raw, err := declarationReadSource(root, rootRef)
	if err != nil || !bytes.Equal(raw, loaded.Raw) {
		fail("", "project", fmt.Errorf("project source changed or is unsafe; reload before analysis"))
		return finish()
	}
	c.Sources = append(c.Sources, declarationSnapshot(rootRef, ProjectSchemaV05, raw))
	if loaded.Layout == ProjectLayoutCanonicalWithLegacy {
		other, err := declarationReadSource(root, LegacyRootContractPath)
		if err != nil {
			fail("", "project", err)
		} else if duplicate, err := ParseProjectDeclaration(other); err != nil || !reflect.DeepEqual(d, duplicate) {
			fail("", "project", fmt.Errorf("project declaration layout conflict"))
		} else {
			c.Sources = append(c.Sources, declarationSnapshot(LegacyRootContractPath, ProjectSchemaV05, other))
		}
	}
	seenSources := map[string][]byte{}
	for _, snapshot := range c.Sources {
		seenSources[snapshot.Ref] = snapshot.Raw
	}
	addSource := func(snapshot DeclarationSourceSnapshot) {
		if previous, seen := seenSources[snapshot.Ref]; seen {
			if !bytes.Equal(previous, snapshot.Raw) {
				fail("", "sources", fmt.Errorf("declaration source changed during compilation: %s", snapshot.Ref))
			}
			return
		}
		c.Sources = append(c.Sources, snapshot)
		seenSources[snapshot.Ref] = snapshot.Raw
	}
	for _, ref := range legacyReferences(d) {
		raw, err := declarationReadSource(root, ref.Ref)
		if err != nil {
			fail("", "legacy_contracts", err)
			continue
		}
		addSource(declarationSnapshot(ref.Ref, ref.SchemaVersion, raw))
		for _, kind := range []string{ProjectContractNotes, ProjectContractRepos} {
			def := singletonContractDefinitions[kind]
			alternate := ""
			if ref.Ref == def.Canonical {
				alternate = def.Legacy
			} else if ref.Ref == def.Legacy {
				alternate = def.Canonical
			}
			if alternate != "" {
				if other, err := declarationReadSource(root, alternate); err == nil {
					addSource(declarationSnapshot(alternate, ref.SchemaVersion, other))
				} else if !os.IsNotExist(err) {
					fail("", "legacy_contracts", err)
				}
			}
		}
	}
	for _, key := range declarationKeys(d.Resources) {
		r := d.Resources[key]
		field := "resources." + string(key)
		checkPath := func(p string) {
			if err := declarationPhysicalPath(root, p, true); err != nil {
				fail(key, field, fmt.Errorf("declared path %s is unavailable or unsafe: %w", p, err))
			}
		}
		switch r.Kind {
		case DeclarationRepository:
			v := r.Repository
			checkPath(v.Path)
			c.Repositories = append(c.Repositories, RepoMemberSpec{ID: v.ID, Key: string(key), Path: v.Path, Role: v.Role, StateRoot: v.StateRoot})
		case DeclarationKnowledge:
			checkPath(r.Knowledge.Path)
		case DeclarationProtection:
			if r.Protection.Path != "" {
				checkPath(r.Protection.Path)
			}
			selection, snapshot, err := compileDeclarationProtection(loaded, root, d, key, add)
			if err != nil {
				fail(key, field, err)
				continue
			}
			c.Protection = append(c.Protection, selection)
			addSource(snapshot)
			definition := singletonContractDefinitions[ProjectContractBackup]
			alternate := ""
			if snapshot.Ref == definition.Canonical {
				alternate = definition.Legacy
			} else if snapshot.Ref == definition.Legacy {
				alternate = definition.Canonical
			}
			if alternate != "" {
				if other, err := declarationReadSource(root, alternate); err == nil {
					addSource(declarationSnapshot(alternate, BackupPolicySchemaV03, other))
				} else if !os.IsNotExist(err) {
					fail(key, field, err)
				}
			}

		case DeclarationApplication:
			for _, dataKey := range declarationKeys(r.Application.Data) {
				v := r.Application.Data[dataKey]
				if v.Path != "" {
					checkPath(v.Path)
				}
			}
			// Application semantics are checked by the service-registry adapter;
			// preserve the exact manifest bytes in the reviewed source closure.
			ref := r.Application.Manifest
			raw, err := declarationReadSource(root, ref)
			if err != nil {
				fail(key, field+".application.manifest", err)
			} else {
				addSource(declarationSnapshot(ref, "application.contract.v1", raw))
			}
			if ref := r.Application.ArtifactDescriptor; ref != "" {
				raw, err := declarationReadSource(root, ref)
				if err != nil {
					fail(key, field+".application.artifact_descriptor", err)
				} else {
					addSource(declarationSnapshot(ref, "application.artifact.v1", raw))
				}
			}
		}
	}
	// Reject competing effective coverage; deduplicate identical selections of
	// one source/root while retaining every attachment in the original document.
	for i := range c.Protection {
		for j := 0; j < i; j++ {
			if declarationProtectionConflict(c.Protection[i], c.Protection[j]) {
				fail(c.Protection[i].Resource, "protection", fmt.Errorf("selected protection ownership or coverage conflicts"))
			}
		}
	}
	// One enabled owner config per exact selected source/root; attachments stay
	// in Document and Roots, preserving where the shared coverage came from.
	seenCoverage := map[string]bool{}
	for i := range c.Protection {
		selection := &c.Protection[i]
		unique := []ProjectWatchedRootItem{}
		for _, item := range selection.WatchedRoots {
			address := selection.PolicyRef + "/" + item.Key
			if !seenCoverage[address] {
				unique = append(unique, item)
				seenCoverage[address] = true
			}
		}
		selection.WatchedRoots = unique
	}
	currentLayout, layoutErr := InspectProjectLayout(loaded.RootPath)
	if layoutErr != nil || currentLayout != loaded.Discovery {
		fail("", "project", fmt.Errorf("root source locations changed during declaration compilation"))
	}
	for _, snapshot := range c.Sources {
		current, err := declarationReadSource(root, snapshot.Ref)
		if err != nil || !bytes.Equal(current, snapshot.Raw) {
			fail("", "sources", fmt.Errorf("source changed during declaration compilation: %s", snapshot.Ref))
		}
	}
	sort.Slice(c.Sources, func(i, j int) bool { return c.Sources[i].Ref < c.Sources[j].Ref })
	if summarizeDiagnostics(report.Diagnostics).Errors == 0 && d.LegacyContracts != nil {
		legacy, err := CompileDeclarationLegacyReferences(loaded, *c)
		if err != nil {
			fail("", "legacy_contracts", err)
		} else {
			c.LegacyContracts = legacy
			report.DerivedProviders = append([]PlanProvider{}, legacy.DerivedProviders...)
			for _, n := range legacy.Notes {
				if err := declarationPhysicalPath(root, n.ProjectPath, true); err != nil {
					fail("", "legacy_contracts.notes", err)
				}
			}
			for _, r := range legacy.Repos {
				if err := declarationPhysicalPath(root, r.ProjectPath, true); err != nil {
					fail("", "legacy_contracts.repos", err)
				}
			}
			add(Diagnostic{Severity: SeverityWarning, Code: LegacyOwnerTransitionRequired, Field: "legacy_contracts", Message: "Legacy representation is preserved; registration requires the separate owner transition."})
		}
	}
	if summarizeDiagnostics(report.Diagnostics).Errors == 0 {
		roots, err := CompileDeclarationEnrollment(loaded, *c)
		if err != nil {
			fail("", "watched_roots", err)
		} else {
			report.WatchedRoots = roots
		}
	}
	return finish()
}

func cloneDeclarationCompilation(c *DeclarationCompilation) *DeclarationCompilation {
	if c == nil {
		return nil
	}
	raw, _ := json.Marshal(c)
	var out DeclarationCompilation
	_ = json.Unmarshal(raw, &out)
	// Optional empty filter arrays disappear under omitempty. Keep their exact
	// in-memory shape so the registration owner's report/plan comparison remains
	// valid for unchanged policies with no explicit include or exclude filters.
	copyFilters := func(in []string) []string {
		if in == nil {
			return nil
		}
		return append([]string{}, in...)
	}
	for i, selection := range c.Protection {
		for j, spec := range selection.Roots {
			out.Protection[i].Roots[j].Include = copyFilters(spec.Include)
			out.Protection[i].Roots[j].Exclude = copyFilters(spec.Exclude)
		}
		for j, root := range selection.WatchedRoots {
			out.Protection[i].WatchedRoots[j].Include = copyFilters(root.Include)
			out.Protection[i].WatchedRoots[j].Exclude = copyFilters(root.Exclude)
		}
	}
	return &out
}

func declarationPlanActions(c *DeclarationCompilation) ([]PlanAction, []PlanUnsupportedFeature) {
	actions := []PlanAction{{Action: "would_register_project", Status: "ready", TargetKind: "project", TargetRef: c.Document.Project.ID, Description: "Register the preserved declaration through the existing project registration path."}}
	unsupported := []PlanUnsupportedFeature{}
	for _, key := range declarationKeys(c.Document.Resources) {
		r := c.Document.Resources[key]
		action, status, reason := "", "pending", ""
		switch r.Kind {
		case DeclarationRepository:
			action = "would_register_repository"
			reason = "Explicit repository membership awaits the declaration apply adapter."
		case DeclarationKnowledge:
			action = "would_enroll_knowledge"
			reason = "Explicit knowledge intent awaits declaration enrollment."
		case DeclarationProtection:
			action = "would_reconcile_protection"
			reason = "Selected file policy is compiled; owner enrollment and application remain pending."
		case DeclarationApplication:
			action = "would_apply_application"
			reason = "Application intent requires the owner-node publication and bindings; use project plan to resolve them."
		}
		actions = append(actions, PlanAction{Action: action, Status: status, TargetKind: string(r.Kind), TargetRef: c.Document.Project.ID + "/" + string(key), Description: reason})
	}
	if c.Document.LegacyContracts != nil {
		contributors, err := DeclarationContributors(c.Document)
		if err == nil {
			for _, contributor := range contributors {
				if _, native := c.Document.Resources[contributor.Key]; native {
					continue
				}
				actions = append(actions, PlanAction{Action: "would_" + string(contributor.Kind), Status: "unsupported", TargetKind: string(contributor.Owner), TargetRef: c.Document.Project.ID + "/" + string(contributor.Key), Description: LegacyOwnerTransitionRequired})
			}
		}
		unsupported = append(unsupported, PlanUnsupportedFeature{Feature: "legacy_contracts", Reason: LegacyOwnerTransitionRequired})
	}
	return actions, unsupported
}

func compileDeclarationProtection(loaded LoadedProject, root *os.Root, d ProjectDeclaration, key ResourceKey, add func(Diagnostic)) (DeclarationProtectionSelection, DeclarationSourceSnapshot, error) {
	ref := d.Resources[key].Protection.PolicyRef
	selection := DeclarationProtectionSelection{Resource: key, PolicyRef: ref, Roots: []BackupRootPolicySpec{}, WatchedRoots: []ProjectWatchedRootItem{}}
	invalid := func(err error) (DeclarationProtectionSelection, DeclarationSourceSnapshot, error) {
		return selection, DeclarationSourceSnapshot{}, err
	}
	// Check custody before invoking the existing singleton layout resolver, which
	// preserves conflict handling when either conventional policy path is selected.
	definition := singletonContractDefinitions[ProjectContractBackup]
	for _, p := range []string{ref, definition.Canonical, definition.Legacy} {
		if p != ref && ref != definition.Canonical && ref != definition.Legacy {
			continue
		}
		if _, err := root.Lstat(p); os.IsNotExist(err) && p != ref {
			continue
		}
		if err := declarationPhysicalPath(root, p, false); err != nil {
			return invalid(err)
		}
	}
	resolution, err := ResolveSingletonContract(loaded, ProjectContractBackup, ref)
	if err != nil {
		return invalid(err)
	}
	if !resolution.Present || resolution.RelativePath != ref {
		return invalid(fmt.Errorf("explicit backup policy source is missing"))
	}
	raw, err := declarationReadSource(root, ref)
	if err != nil {
		return invalid(err)
	}
	return compileDeclarationProtectionSource(loaded, d, key, raw, add, func(target string) error {
		return declarationPhysicalPath(root, target, true)
	})
}

// Compile from the captured bytes; registration mapping never reopens sources.
func compileDeclarationProtectionSource(loaded LoadedProject, d ProjectDeclaration, key ResourceKey, raw []byte, add func(Diagnostic), checkPath func(string) error) (DeclarationProtectionSelection, DeclarationSourceSnapshot, error) {
	ref := d.Resources[key].Protection.PolicyRef
	selection := DeclarationProtectionSelection{Resource: key, PolicyRef: ref, Roots: []BackupRootPolicySpec{}, WatchedRoots: []ProjectWatchedRootItem{}}
	invalid := func(err error) (DeclarationProtectionSelection, DeclarationSourceSnapshot, error) {
		return selection, DeclarationSourceSnapshot{}, err
	}
	// Unlike the legacy reader, a v0.5 dependency must be one strict source.
	var tree yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&tree); err != nil {
		return invalid(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return invalid(fmt.Errorf("backup policy contains trailing source"))
	}
	if len(tree.Content) != 1 || !declarationNodeShape(tree.Content[0], reflect.TypeOf(ProjectBackupPolicyContract{})) {
		return invalid(fmt.Errorf("backup policy has unknown, duplicate, aliased or invalid fields"))
	}
	policy, _, err := parseBackupPolicyContract(raw)
	if err != nil {
		return invalid(err)
	}
	if policy.Kind != BackupPolicyKind || policy.SchemaVersion != BackupPolicySchemaV03 {
		return invalid(fmt.Errorf("protection source must use %s", BackupPolicySchemaV03))
	}
	selection.Enabled = boolValue(policy.Backup.Enabled, true)
	targets := map[string]bool{}
	if p := d.Resources[key].Protection.Path; p != "" {
		targets[p] = true
	}
	for _, r := range d.Resources {
		if r.Repository != nil && r.Repository.Protection == key {
			targets[r.Repository.Path] = true
		}
		if r.Knowledge != nil && r.Knowledge.Protection == key {
			targets[r.Knowledge.Path] = true
		}
		if r.Application != nil {
			for _, v := range r.Application.Data {
				if v.Protection == key {
					if v.Path == "" {
						// Retained in the application document and its typed unsupported
						// prerequisite; no path or coverage can be invented before E.
						continue
					}
					targets[v.Path] = true
				}
			}
		}
	}
	targetNames := make([]string, 0, len(targets))
	for p := range targets {
		targetNames = append(targetNames, p)
	}
	sort.Strings(targetNames)
	toNormalize := policy
	toNormalize.Backup.Roots = append([]BackupRootPolicySpec{}, policy.Backup.Roots...)
	normalized := normalizeBackupPolicy(toNormalize)
	seenKeys := map[string]bool{}
	for _, target := range targetNames {
		if checkPath != nil {
			if err := checkPath(target); err != nil {
				return invalid(err)
			}
		}
		match := -1
		for i, spec := range policy.Backup.Roots {
			if spec.Path == target {
				if match >= 0 {
					return invalid(fmt.Errorf("multiple backup policy roots select %s", target))
				}
				match = i
			}
		}
		if match < 0 {
			return invalid(fmt.Errorf("backup policy has no exact root for %s", target))
		}
		spec := normalized.Backup.Roots[match]
		if !declarationPath(spec.Path, false) && spec.Path != "." {
			return invalid(fmt.Errorf("unsafe backup target"))
		}
		if spec.Path == "." && spec.SafeRoot != defaultProjectSafeRootKey {
			return invalid(fmt.Errorf("project-root protection requires the project safe root"))
		}
		if spec.Key == "" {
			spec.Key = deriveLocalRootKey(spec.Path)
		}
		if seenKeys[spec.Key] {
			return invalid(fmt.Errorf("selected backup root key conflict"))
		}
		seenKeys[spec.Key] = true
		for _, prior := range selection.Roots {
			if declarationOverlap(prior.Path, spec.Path) && prior.Path != "." && spec.Path != "." {
				return invalid(fmt.Errorf("overlapping selected backup roots"))
			}
		}
		selection.Roots = append(selection.Roots, spec)
	}
	selected := policy
	selected.Backup.Roots = append([]BackupRootPolicySpec{}, selection.Roots...)
	// Validate selected configuration even while disabled, but retain disabled
	// intent and never generate an enabled owner configuration for it.
	selected.Backup.Enabled = boolPtr(true)
	builder := newWatchPolicyBuilder(loaded, loaded.Contract, add)
	builder.sourceOnly = true
	builder.addBackupPolicySource(selected, filepath.Join(loaded.RootPath, filepath.FromSlash(ref)))
	roots := builder.items()
	if selection.Enabled {
		selection.WatchedRoots = roots
	}
	return selection, declarationSnapshot(ref, BackupPolicySchemaV03, raw), nil
}

// v0.5 selects only explicit sources. Preserve the existing singleton conflict
// rule for the two standard locations without probing unrelated policy files.
func resolveDeclarationSingleton(loaded LoadedProject, kind, explicit string) (ContractPathResolution, error) {
	definition, ok := singletonContractDefinitions[kind]
	if !ok || !declarationPath(explicit, true) {
		return ContractPathResolution{}, ContractPathError{Code: "contract.path_unsafe", Kind: kind, Err: fmt.Errorf("declarations require a safe explicit policy source")}
	}
	result := ContractPathResolution{Kind: kind, Path: filepath.Join(loaded.RootPath, filepath.FromSlash(explicit)), RelativePath: explicit, Explicit: true, Layout: loaded.Layout, CanonicalPath: filepath.Join(loaded.RootPath, definition.Canonical), LegacyPath: filepath.Join(loaded.RootPath, definition.Legacy)}
	root, err := os.OpenRoot(loaded.RootPath)
	if err != nil {
		return result, err
	}
	defer root.Close()
	raw, err := declarationReadSource(root, explicit)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Present = true
	alternate := ""
	switch explicit {
	case definition.Canonical:
		result.CanonicalPresent = true
		result.Layout = ProjectLayoutCanonical
		alternate = definition.Legacy
	case definition.Legacy:
		result.LegacyPresent = true
		result.Layout = ProjectLayoutLegacy
		alternate = definition.Canonical
	}
	if alternate != "" {
		other, err := declarationReadSource(root, alternate)
		if os.IsNotExist(err) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		result.CanonicalPresent = true
		result.LegacyPresent = true
		equal, err := yamlDocumentsSemanticallyEqual(raw, other)
		if err != nil {
			return result, err
		}
		if !equal {
			return result, ContractPathError{Code: "contract.singleton_layout_conflict", Kind: kind, Err: fmt.Errorf("%s contract layout conflict", kind)}
		}
		result.Layout = ProjectLayoutCanonicalWithLegacy
	}
	return result, nil
}

// CompileDeclarationEnrollment reproduces owner configuration exclusively from
// validated source snapshots. It is also the registration mapper's integrity
// check; neither mutable files nor caller-supplied compiled roots are inputs.
func CompileDeclarationEnrollment(loaded LoadedProject, c DeclarationCompilation) ([]ProjectWatchedRootItem, error) {
	d, err := ParseProjectDeclaration(loaded.Raw)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(d, c.Document) {
		return nil, fmt.Errorf("declaration enrollment document mismatch")
	}
	ref, err := filepath.Rel(loaded.RootPath, loaded.ContractPath)
	if err != nil {
		return nil, err
	}
	sources := map[string]DeclarationSourceSnapshot{}
	for _, source := range c.Sources {
		if _, exists := sources[source.Ref]; exists || source.Hash != declarationHash(source.Raw) || source.Revision != source.Hash {
			return nil, fmt.Errorf("declaration enrollment source mismatch")
		}
		sources[source.Ref] = source
	}
	rootSource, ok := sources[filepath.ToSlash(ref)]
	if !ok || rootSource.SchemaVersion != ProjectSchemaV05 || !bytes.Equal(rootSource.Raw, loaded.Raw) {
		return nil, fmt.Errorf("declaration enrollment root source mismatch")
	}
	var compileErr error
	add := func(diag Diagnostic) {
		if diag.Severity == SeverityError && compileErr == nil {
			compileErr = fmt.Errorf("%s: %s", diag.Code, diag.Message)
		}
	}
	contract := NormalizeContract(ProjectContract{Kind: d.Kind, SchemaVersion: d.SchemaVersion, Project: d.Project})
	builder := newWatchPolicyBuilder(loaded, contract, add)
	builder.sourceOnly = true
	legacy, err := CompileDeclarationLegacyReferences(loaded, c)
	if err != nil {
		return nil, err
	}
	if !declarationJSONEqual(legacy, c.LegacyContracts) {
		return nil, fmt.Errorf("legacy compiler evidence mismatch")
	}
	if legacy != nil {
		for _, n := range legacy.Notes {
			builder.addNotes(n)
		}
		for _, r := range legacy.Repos {
			builder.addRepo(r)
		}
	}
	selections := []DeclarationProtectionSelection{}
	seenCoverage := map[string]bool{}
	owners := map[string]string{}
	for _, key := range declarationKeys(d.Resources) {
		resource := d.Resources[key]
		if resource.Protection == nil {
			continue
		}
		source, ok := sources[resource.Protection.PolicyRef]
		if !ok || source.SchemaVersion != BackupPolicySchemaV03 {
			return nil, fmt.Errorf("missing protection source")
		}
		selection, _, err := compileDeclarationProtectionSource(loaded, d, key, source.Raw, add, nil)
		if err != nil {
			return nil, err
		}
		unique := []ProjectWatchedRootItem{}
		for _, item := range selection.WatchedRoots {
			address := selection.PolicyRef + "/" + item.Key
			if !seenCoverage[address] {
				unique = append(unique, item)
				seenCoverage[address] = true
			}
		}
		selection.WatchedRoots = unique
		for _, prior := range selections {
			if declarationProtectionConflict(prior, selection) {
				return nil, fmt.Errorf("declaration.enrollment_protection_conflict: selected policy owners overlap")
			}
		}
		selections = append(selections, selection)
		if !selection.Enabled {
			continue
		}
		for _, spec := range selection.Roots {
			address := selection.PolicyRef + "/" + spec.Key
			if prior, ok := owners[spec.Key]; ok {
				if prior != address {
					return nil, fmt.Errorf("declaration.enrollment_key_conflict: %s", spec.Key)
				}
				continue
			}
			owners[spec.Key] = address
			policy := ProjectBackupPolicyContract{Kind: BackupPolicyKind, SchemaVersion: BackupPolicySchemaV03}
			policy.Backup.Enabled = boolPtr(true)
			policy.Backup.Roots = []BackupRootPolicySpec{spec}
			builder.addBackupPolicySource(policy, source.Ref)
		}
	}
	if !declarationJSONEqual(selections, c.Protection) {
		return nil, fmt.Errorf("declaration enrollment protection evidence mismatch")
	}
	for _, key := range declarationKeys(d.Resources) {
		knowledge := d.Resources[key].Knowledge
		if knowledge == nil {
			continue
		}
		localKey := string(key)
		include, exclude := append([]string{}, defaultNotesIncludes...), append([]string{}, defaultNotesExcludes...)
		for _, selection := range selections {
			if !selection.Enabled {
				continue
			}
			for _, spec := range selection.Roots {
				if spec.Path != knowledge.Path {
					continue
				}
				if knowledge.Protection == "" || selection.PolicyRef != d.Resources[knowledge.Protection].Protection.PolicyRef {
					return nil, fmt.Errorf("declaration.enrollment_protection_conflict: %s needs an explicit attachment", key)
				}
				if spec.SafeRoot != "" && spec.SafeRoot != defaultProjectSafeRootKey {
					return nil, fmt.Errorf("declaration.enrollment_protection_conflict: safe root for %s", key)
				}
				localKey = spec.Key
				include, exclude = append([]string{}, spec.Include...), append([]string{}, spec.Exclude...)
			}
		}
		// Resource keys are stable identity; normalization must never collapse two
		// declarations or overwrite a selected protection owner.
		if existing := builder.roots[localKey]; existing != nil && (existing.path != knowledge.Path || existing.metadata["knowledge_source"] != nil) {
			return nil, fmt.Errorf("declaration.enrollment_key_conflict: %s", key)
		}
		builder.addNotes(NotesFacetItem{RootKey: localKey, ProjectPath: knowledge.Path, Status: "active", Sync: true, Index: true, Include: include, Exclude: exclude, MaterialCategory: string(knowledge.Category), ContractPath: loaded.ContractPath})
		acc := builder.roots[localKey]
		if acc == nil {
			return nil, fmt.Errorf("declaration.enrollment_key_conflict: resource key %s is not a portable owner key", key)
		}
		acc.metadata["knowledge_source"] = map[string]any{
			"schema_version": ProjectSchemaV05, "root_kind": "project_material", "category": "projects", "declaration": string(knowledge.Category),
			"enabled": true, "root_relative_path": knowledge.Path, "include": include, "exclude": exclude,
			"project_id": d.Project.ID, "project_root": loaded.RootPath, "owner_node": d.Project.OwnerNode,
			"resource_key": string(key), "local_root_key": localKey, "backend_root_key": ProjectWatchedRootKey(d.Project.Slug, localKey),
			"source_ref": rootSource.Ref, "source_hash": rootSource.Hash,
		}
	}
	items := builder.items()
	seenBackend := map[string]bool{}
	for i := range items {
		item := &items[i]
		if seenBackend[item.BackendRootKey] {
			return nil, fmt.Errorf("declaration.enrollment_key_conflict: %s", item.BackendRootKey)
		}
		seenBackend[item.BackendRootKey] = true
		// Every selected owner carries the full source closure, including disabled
		// protection intent and shared attachments. No inherited coverage claim.
		item.Metadata["declaration_sources"] = cloneDeclarationCompilation(&c).Sources
		if source, ok := item.Metadata["knowledge_source"].(map[string]any); ok {
			source["config_hash"] = item.ConfigHash
		}
	}
	if compileErr != nil {
		return nil, compileErr
	}
	if legacy != nil {
		if err := validateLegacyEnrollment(loaded, c, legacy, items); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// JSON equality preserves the public evidence shape across typed Go values and
// report/plan decoding (notably []string versus []any inside metadata).
func declarationJSONEqual(a, b any) bool {
	left, e1 := json.Marshal(a)
	right, e2 := json.Marshal(b)
	if e1 != nil || e2 != nil {
		return false
	}
	l, e1 := DecodeDeclarationEvidenceJSON(left)
	r, e2 := DecodeDeclarationEvidenceJSON(right)
	return e1 == nil && e2 == nil && reflect.DeepEqual(l, r)
}

// DecodeDeclarationEvidenceJSON decodes source evidence without losing numbers
// or accepting duplicate fields. float64 aliases distinct int64/uint64 limits.
// Reject duplicate fields even inside RawMessage configurations; a round trip
// must not make a malformed or substituted owner config appear equivalent.
func DecodeDeclarationEvidenceJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func() (any, error)
	read = func() (any, error) {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch token {
		case json.Delim('{'):
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON key")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate JSON field")
				}
				value, err := read()
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			_, err = decoder.Token()
			return object, err
		case json.Delim('['):
			array := []any{}
			for decoder.More() {
				value, err := read()
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			_, err = decoder.Token()
			return array, err
		default:
			return token, nil
		}
	}
	value, err := read()
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON evidence")
	}
	return value, nil
}

func cloneDeclarationWatchedRoots(roots []ProjectWatchedRootItem) []ProjectWatchedRootItem {
	raw, _ := json.Marshal(roots)
	var out []ProjectWatchedRootItem
	_ = json.Unmarshal(raw, &out)
	return out
}

// ValidateDeclarationEnrollment binds both public outputs to the exact sources.
func ValidateDeclarationEnrollment(loaded LoadedProject, report ValidationReport, plan ProjectPlan) error {
	if report.Declaration == nil || plan.Declaration == nil || !declarationJSONEqual(report.Declaration, plan.Declaration) {
		return fmt.Errorf("missing declaration evidence")
	}
	if legacy := report.Declaration.LegacyContracts; legacy != nil {
		if !declarationJSONEqual(legacy.DerivedProviders, report.DerivedProviders) || !declarationJSONEqual(legacy.DerivedProviders, plan.DerivedProviders) {
			return fmt.Errorf("legacy provider evidence mismatch")
		}
	}
	roots, err := CompileDeclarationEnrollment(loaded, *report.Declaration)
	if err != nil {
		return err
	}
	if !declarationJSONEqual(roots, report.WatchedRoots) || !declarationJSONEqual(roots, plan.WatchedRoots) {
		return fmt.Errorf("declaration enrollment roots differ from validated sources")
	}
	return nil
}
