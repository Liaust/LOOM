package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

const (
	DeclarationMigrationPreviewSchema = "project.declaration_migration_preview.v1"
	DeclarationMigrationFactsSchema   = "project.declaration_migration_facts.v1"
	migrationMaxSources               = 256
	migrationMaxSourceBytes           = 4 << 20
	migrationMaxTotalBytes            = 8 << 20
	migrationMaxFacts                 = 4096
)

// MigrationSourceClaim records absence as well as exact content identity.
// Digest is sha256:<hex> of Raw, never a digest of normalized YAML.
type MigrationSourceClaim struct {
	Ref           string `json:"ref"`
	State         string `json:"state"` // present, absent, unreadable, not_supplied
	Kind          string `json:"kind"`
	SchemaVersion string `json:"schema_version"`
	Digest        string `json:"digest"`
	Size          int64  `json:"size"`
}

type MigrationSource struct {
	MigrationSourceClaim
	Raw []byte `json:"-"`
}

type MigrationCollection struct {
	Ref          string   `json:"ref"`
	Completeness string   `json:"completeness"`
	Entries      []string `json:"entries"`
}

// MigrationFactBasis is repeated on every family to prevent joining snapshots
// from different source, registry, location or lifecycle generations. It is
// supplied evidence, not an authentication or live completeness certificate.
type MigrationFactBasis struct {
	SchemaVersion     string `json:"schema_version"`
	Completeness      string `json:"completeness"`
	ProjectID         string `json:"project_id"`
	OwnerNode         string `json:"owner_node"`
	LocationRevision  string `json:"location_revision"`
	RegistryRevision  string `json:"registry_revision"`
	LifecycleRevision string `json:"lifecycle_revision"`
	SnapshotRevision  string `json:"snapshot_revision"`
	SourceFingerprint string `json:"source_fingerprint"`
}

type MigrationProjectFacts struct {
	Basis       MigrationFactBasis   `json:"basis"`
	Project     ProjectSpec          `json:"project"`
	LocationRef string               `json:"location_ref"` // opaque identity, not a host path
	Source      MigrationSourceClaim `json:"source"`
}

type MigrationMember struct {
	ID             string               `json:"id"`
	Key            string               `json:"key"`
	Path           string               `json:"path"` // project-relative physical location
	Role           string               `json:"role"`
	StateRoot      string               `json:"state_root"`
	OwnerProjectID string               `json:"owner_project_id"`
	Lifecycle      string               `json:"lifecycle"`
	Revision       string               `json:"revision"`
	Source         MigrationSourceClaim `json:"source"`
}

type MigrationMembershipFacts struct {
	Basis   MigrationFactBasis `json:"basis"`
	Members []MigrationMember  `json:"members"`
}

// MigrationRootIntent carries the entire portable owner configuration. Typed
// integers deliberately avoid float64 round trips in comparison and hashing.
// Host commands and arbitrary source metadata are not presentation fields.
type MigrationRootIntent struct {
	Key            string                  `json:"key"`
	BackendRootKey string                  `json:"backend_root_key"`
	WorkerKey      string                  `json:"worker_key"`
	OwnerNode      string                  `json:"owner_node"`
	SourceKinds    []string                `json:"source_kinds"`
	Config         watchedroots.RootConfig `json:"config"`
	ConfigHash     string                  `json:"config_hash"`
}

type MigrationWatch struct {
	Owner    string              `json:"owner"`
	Revision string              `json:"revision"`
	Root     MigrationRootIntent `json:"root"`
}

type MigrationWatchFacts struct {
	Basis MigrationFactBasis `json:"basis"`
	Roots []MigrationWatch   `json:"roots"`
}

type MigrationCoverage struct {
	Owner    string              `json:"owner"`
	Revision string              `json:"revision"`
	External bool                `json:"external"`
	Root     MigrationRootIntent `json:"root"`
}

type MigrationCoverageFacts struct {
	Basis    MigrationFactBasis  `json:"basis"`
	Coverage []MigrationCoverage `json:"coverage"`
}

type MigrationLegacyFact struct {
	Kind     string               `json:"kind"` // facet or service
	Key      string               `json:"key"`
	State    string               `json:"state"`
	Revision string               `json:"revision"`
	Source   MigrationSourceClaim `json:"source"`
}

type MigrationLegacyFacts struct {
	Basis MigrationFactBasis    `json:"basis"`
	Facts []MigrationLegacyFact `json:"facts"`
}

type MigrationEvidence struct {
	Kind     string `json:"kind"` // archive or disabled
	Ref      string `json:"ref"`  // opaque relative evidence ref, never payload bytes
	Digest   string `json:"digest"`
	Owner    string `json:"owner"`
	Revision string `json:"revision"`
}

type MigrationEvidenceFacts struct {
	Basis    MigrationFactBasis  `json:"basis"`
	Evidence []MigrationEvidence `json:"evidence"`
}

type DeclarationMigrationInput struct {
	// Explicit opt-in target mapping; omission preserves the original assessment subset.
	LegacyReferences *LegacyContractsDeclaration `json:"legacy_references,omitempty"`
	Sources          []MigrationSource           `json:"sources"`
	Collections      []MigrationCollection       `json:"collections"`
	Project          MigrationProjectFacts       `json:"project"`
	Membership       MigrationMembershipFacts    `json:"membership"`
	Watches          MigrationWatchFacts         `json:"watches"`
	Coverage         MigrationCoverageFacts      `json:"coverage"`
	Legacy           MigrationLegacyFacts        `json:"legacy"`
	Evidence         MigrationEvidenceFacts      `json:"evidence"`
}

type MigrationSourceReport struct {
	MigrationSourceClaim
	Selection   []string `json:"selection"`
	Disposition string   `json:"disposition"`
	DuplicateOf string   `json:"duplicate_of,omitempty"`
}

type MigrationIssue struct {
	Category  string `json:"category"`
	CauseCode string `json:"cause_code"`
	Ref       string `json:"ref"`
	Digest    string `json:"digest"`
	Field     string `json:"field"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Message   string `json:"message"`
	Required  string `json:"required"`
}

type MigrationFieldComparison struct {
	Ref         string `json:"ref"`
	Field       string `json:"field"`
	Before      string `json:"before"` // safe presence/boolean description, not arbitrary values
	After       string `json:"after"`
	Disposition string `json:"disposition"`
}

type MigrationIdentityComparison struct {
	Before  ProjectSpec       `json:"before"`
	After   ProjectSpec       `json:"after"`
	Members []MigrationMember `json:"members"`
}

// MigrationSourceCopy is a pending byte-for-byte transformation, not a claim
// that the destination exists or that any source has been published.
type MigrationSourceCopy struct {
	From  MigrationSourceClaim   `json:"from"`
	To    LegacyProjectReference `json:"to"`
	State string                 `json:"state"`
}

type MigrationRetention struct {
	PlannedCopies    []MigrationSourceCopy  `json:"planned_copies,omitempty"`
	Sources          []MigrationSourceClaim `json:"sources"`
	Evidence         []MigrationEvidence    `json:"evidence"`
	ExternalCoverage []MigrationCoverage    `json:"external_coverage"`
	Publication      string                 `json:"publication"`
	ArchiveCopy      string                 `json:"archive_copy"`
	ActiveSourceNote string                 `json:"active_source_note"`
}

type MigrationCandidate struct {
	PlannedSourceRef string `json:"planned_source_ref"`
	YAML             string `json:"yaml"`
	Digest           string `json:"digest"`
	Comparison       string `json:"comparison"`
}

type DeclarationMigrationPreview struct {
	SchemaVersion       string                      `json:"schema_version"`
	Disposition         string                      `json:"disposition"`
	BasisKind           string                      `json:"basis_kind"`
	SourceFingerprint   string                      `json:"source_fingerprint"`
	BasisFingerprint    string                      `json:"basis_fingerprint"`
	TargetSchemaVersion string                      `json:"target_schema_version"`
	RuntimeAssessment   string                      `json:"runtime_assessment"`
	Sources             []MigrationSourceReport     `json:"sources"`
	Identity            MigrationIdentityComparison `json:"identity"`
	Fields              []MigrationFieldComparison  `json:"fields"`
	BeforeRoots         []MigrationRootIntent       `json:"before_roots"`
	AfterRoots          []MigrationRootIntent       `json:"after_roots"`
	Issues              []MigrationIssue            `json:"issues"`
	Retention           MigrationRetention          `json:"retention"`
	Candidate           *MigrationCandidate         `json:"candidate"`
}

type migrationPreview struct {
	in          DeclarationMigrationInput
	out         DeclarationMigrationPreview
	sources     map[string]MigrationSource
	trees       map[string]*yaml.Node
	selected    map[string]string
	singletons  map[string]string
	rootRef     string
	contract    ProjectContract
	document    ProjectDeclaration
	before      []ProjectWatchedRootItem
	legacyNotes []NotesFacetItem
	legacyRepos []RepoFacetItem
}

// PreviewDeclarationMigration is pure: it consumes supplied bytes/facts and
// returns either a whole candidate or an explained refusal. It performs no
// source gathering, physical validation, publication, enrollment or readiness
// observation. SourceFingerprint is sha256 of JSON {sources,collections}, with
// claim fields in MigrationSourceClaim order, sources/collections sorted by ref
// and collection entries sorted. Nil collections/entries normalize to []. The
// same fingerprint must qualify each complete fact family.
func PreviewDeclarationMigration(input DeclarationMigrationInput) DeclarationMigrationPreview {
	p := migrationPreview{in: input, sources: map[string]MigrationSource{}, trees: map[string]*yaml.Node{}, selected: map[string]string{}, singletons: map[string]string{}}
	p.out = DeclarationMigrationPreview{
		SchemaVersion: DeclarationMigrationPreviewSchema, Disposition: "refused", BasisKind: "supplied_snapshot",
		TargetSchemaVersion: ProjectSchemaV05, RuntimeAssessment: "not_performed",
		Sources: []MigrationSourceReport{}, Fields: []MigrationFieldComparison{}, Issues: []MigrationIssue{},
		BeforeRoots: []MigrationRootIntent{}, AfterRoots: []MigrationRootIntent{},
		Identity:  MigrationIdentityComparison{Members: []MigrationMember{}},
		Retention: MigrationRetention{Sources: []MigrationSourceClaim{}, Evidence: []MigrationEvidence{}, ExternalCoverage: []MigrationCoverage{}, Publication: "not_performed", ArchiveCopy: "not_created", ActiveSourceNote: "Original sources remain unchanged. A later reviewed active-source transition is required before publication."},
	}
	if !p.bounds() {
		// Do not sort, hash or expand an already over-limit caller inventory.
		return p.out
	}
	p.inventory()
	p.checkBases()
	if p.loadRoot() {
		p.projectIdentity()
		p.legacyFields()
		p.dependencies()
		p.repositories()
		p.backup()
		p.existingFacts()
		p.candidate()
	}
	return p.finish()
}

func (p *migrationPreview) issue(category, code, ref, field, message, required string) {
	x := MigrationIssue{Category: category, CauseCode: code, Ref: ref, Field: field, Message: message, Required: required}
	if source, ok := p.sources[ref]; ok {
		if migrationDigestValid(source.Digest) {
			x.Digest = source.Digest
		}
	}
	if n := migrationNode(p.trees[ref], field); n != nil {
		x.Line, x.Column = n.Line, n.Column
	}
	// Never echo hostile refs/keys or raw parser/owner error strings.
	if ref != "" && !migrationRef(ref) {
		x.Ref = ""
	}
	if !migrationText(field) {
		x.Field = ""
	}
	p.out.Issues = append(p.out.Issues, x)
}

func (p *migrationPreview) bounds() bool {
	n := len(p.in.Membership.Members) + len(p.in.Watches.Roots) + len(p.in.Coverage.Coverage) + len(p.in.Legacy.Facts) + len(p.in.Evidence.Evidence) + len(p.in.Collections)
	for _, c := range p.in.Collections {
		n += len(c.Entries)
	}
	if len(p.in.Sources) > migrationMaxSources || n > migrationMaxFacts {
		p.issue("invalid_source", "input_limit", "", "input", "The supplied inventory exceeds the preview entry limit.", "Supply at most 256 sources and 4096 fact/listing entries; do not truncate a complete snapshot.")
		return false
	}
	total := 0
	for _, s := range p.in.Sources {
		total += len(s.Raw)
		if len(s.Raw) > migrationMaxSourceBytes || total > migrationMaxTotalBytes {
			p.issue("invalid_source", "input_limit", s.Ref, "sources", "The supplied bytes exceed the preview source limit.", "Each source must fit 4 MiB and the full bundle 8 MiB; do not trim source content.")
			return false
		}
	}
	return true
}

func (p *migrationPreview) inventory() {
	for _, s := range p.in.Sources {
		if !migrationRef(s.Ref) {
			p.issue("invalid_source", "source_ref_invalid", "", "sources", "A source ref is not a safe exact project-relative location.", "Supply canonical relative refs without traversal, control characters or host paths.")
			continue
		}
		if _, exists := p.sources[s.Ref]; exists {
			p.issue("invalid_source", "source_ref_duplicate", s.Ref, "sources", "A source location was supplied more than once.", "Supply one qualified claim per exact location.")
			continue
		}
		p.sources[s.Ref] = s
		if s.State == "present" {
			if s.Digest != declarationHash(s.Raw) {
				p.issue("invalid_source", "source_digest_mismatch", s.Ref, "digest", "Source bytes do not match their claimed digest.", "Recapture the complete source bytes and their SHA-256 digest together.")
			}
			if s.Size != int64(len(s.Raw)) {
				p.issue("invalid_source", "source_size_mismatch", s.Ref, "size", "Source bytes do not match the claimed byte count.", "Supply the exact byte count of the unmodified source.")
			}
			n, err := migrationTree(s.Raw)
			if err != nil || n.Kind != yaml.MappingNode {
				p.issue("invalid_source", "source_shape_invalid", s.Ref, "", "Supplied content is not one strict YAML mapping.", "Retain all bytes; resolve malformed, aliased, duplicate or trailing source separately.")
			} else {
				p.trees[s.Ref] = n
				kind, schema := migrationNode(n, "kind"), migrationNode(n, "schema_version")
				if kind == nil || schema == nil || kind.Tag != "!!str" || schema.Tag != "!!str" || kind.Value != s.Kind || schema.Value != s.SchemaVersion {
					p.issue("invalid_source", "source_identity_mismatch", s.Ref, "schema_version", "Supplied kind/schema claims do not match their source document.", "Bind each present source's kind/schema to its exact bytes, including retained duplicates.")
				}
			}
		} else if s.State != "absent" && s.State != "unreadable" && s.State != "not_supplied" {
			p.issue("invalid_source", "source_state_invalid", s.Ref, "state", "Source state is unknown.", "Use present, absent, unreadable or not_supplied.")
		} else if len(s.Raw) != 0 || s.Digest != "" || s.Size != 0 || s.Kind != "" || s.SchemaVersion != "" {
			p.issue("invalid_source", "source_state_conflict", s.Ref, "state", "A non-present source carries content claims.", "Keep absence and unreadability distinct from a present document.")
		}
	}
	claims, collections := migrationSourceBasis(p.in)
	p.out.SourceFingerprint = migrationDigest(struct {
		Sources     []MigrationSourceClaim `json:"sources"`
		Collections []MigrationCollection  `json:"collections"`
	}{claims, collections})
	seen := map[string]bool{}
	for _, c := range collections {
		if !migrationRef(c.Ref) || seen[c.Ref] {
			p.issue("invalid_source", "collection_invalid", "", "collections", "A collection ref is unsafe or duplicated.", "Supply each selected collection once with exact child refs.")
			continue
		}
		seen[c.Ref] = true
		last := ""
		for _, ref := range c.Entries {
			if !migrationRef(ref) || !strings.HasPrefix(ref, c.Ref+"/") || ref == last {
				p.issue("invalid_source", "collection_invalid", c.Ref, "entries", "A collection child is unsafe, duplicated or outside the selected collection.", "Supply exact unique contract refs beneath this collection.")
			}
			last = ref
		}
	}
}

func migrationSourceBasis(in DeclarationMigrationInput) ([]MigrationSourceClaim, []MigrationCollection) {
	claims := make([]MigrationSourceClaim, 0, len(in.Sources))
	for _, s := range in.Sources {
		claims = append(claims, s.MigrationSourceClaim)
	}
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Ref != claims[j].Ref {
			return claims[i].Ref < claims[j].Ref
		}
		return migrationDigest(claims[i]) < migrationDigest(claims[j])
	})
	collections := make([]MigrationCollection, 0, len(in.Collections))
	for _, c := range in.Collections {
		c.Entries = append([]string{}, c.Entries...)
		sort.Strings(c.Entries)
		collections = append(collections, c)
	}
	sort.Slice(collections, func(i, j int) bool {
		if collections[i].Ref != collections[j].Ref {
			return collections[i].Ref < collections[j].Ref
		}
		return migrationDigest(collections[i]) < migrationDigest(collections[j])
	})
	return claims, collections
}

func (p *migrationPreview) checkBases() {
	b := p.in.Project.Basis
	bases := []struct {
		name  string
		basis MigrationFactBasis
	}{{"project", b}, {"membership", p.in.Membership.Basis}, {"watches", p.in.Watches.Basis}, {"coverage", p.in.Coverage.Basis}, {"legacy", p.in.Legacy.Basis}, {"evidence", p.in.Evidence.Basis}}
	for _, x := range bases {
		v := x.basis
		if v.SchemaVersion != DeclarationMigrationFactsSchema || v.Completeness != "complete" || ids.Validate(ids.ProjectPrefix, v.ProjectID) != nil || !nodeKeyPattern.MatchString(v.OwnerNode) || !migrationToken(v.LocationRevision) || !migrationToken(v.RegistryRevision) || !migrationToken(v.LifecycleRevision) || !migrationToken(v.SnapshotRevision) {
			p.issue("missing_evidence", "facts_incomplete", "", x.name, "A fact family lacks a complete qualified project/node/revision basis.", "Supply a complete family, including explicit empty lists, from the same existing-state snapshot.")
		}
		if v.SourceFingerprint != p.out.SourceFingerprint {
			p.issue("conflict", "facts_source_conflict", "", x.name, "Facts belong to different source bytes or location claims.", "Recapture facts against the exact full source bundle, including absence and dual claims.")
		}
		if v != b {
			p.issue("conflict", "facts_generation_conflict", "", x.name, "Fact families disagree about their scope or generation.", "Supply one qualified project/node/location/registry/lifecycle snapshot across all families.")
		}
	}
}

func (p *migrationPreview) claim(ref, trigger string, required bool) (MigrationSource, bool) {
	s, ok := p.sources[ref]
	if !ok {
		s = MigrationSource{MigrationSourceClaim: MigrationSourceClaim{Ref: ref, State: "not_supplied"}}
		p.sources[ref] = s
	}
	if trigger != "" {
		p.selected[ref] = trigger
	}
	switch s.State {
	case "present":
		return s, true
	case "absent":
		if required {
			p.issue("missing_evidence", "source_missing", ref, trigger, "A selected source is absent.", "Supply the required existing contract or a separately accepted resolution of its intent.")
		}
	case "unreadable":
		p.issue("missing_evidence", "source_unreadable", ref, trigger, "A relevant source could not be read.", "Recapture readable complete bytes; unreadable is not absent.")
	case "not_supplied":
		p.issue("missing_evidence", "source_not_supplied", ref, trigger, "A relevant source location was not supplied.", "Supply its exact present, absent or unreadable claim.")
	}
	return s, false
}

func migrationTree(raw []byte) (*yaml.Node, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	var n yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(&n); err != nil {
		return nil, err
	}
	var tail yaml.Node
	if d.Decode(&tail) != io.EOF || len(n.Content) != 1 || !migrationTreeSafe(n.Content[0], 0) {
		return nil, fmt.Errorf("non-single or unsafe YAML shape")
	}
	return n.Content[0], nil
}

func migrationTreeSafe(n *yaml.Node, depth int) bool {
	if depth > 128 || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return false
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag != "!!str" || seen[k.Value] || !migrationText(k.Value) {
				return false
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if !migrationTreeSafe(child, depth+1) {
			return false
		}
	}
	return true
}

func (p *migrationPreview) decode(s MigrationSource, out any) bool {
	n, err := migrationTree(s.Raw)
	if err == nil {
		p.trees[s.Ref] = n
	}
	if err != nil || !declarationNodeShape(n, reflect.TypeOf(out).Elem()) {
		p.issue("invalid_source", "source_shape_invalid", s.Ref, "", "Source must be one strict typed YAML document without unknown/duplicate/aliased fields.", "Retain the original bytes and resolve the specific source shape before conversion.")
		return false
	}
	if err := n.Decode(out); err != nil {
		p.issue("invalid_source", "source_shape_invalid", s.Ref, "", "Source scalar values do not fit the declared types.", "Supply a valid complete contract without coercing or discarding values.")
		return false
	}
	var h struct {
		Kind          string `yaml:"kind"`
		SchemaVersion string `yaml:"schema_version"`
	}
	_ = n.Decode(&h)
	if h.Kind != s.Kind || h.SchemaVersion != s.SchemaVersion {
		p.issue("invalid_source", "source_identity_mismatch", s.Ref, "schema_version", "The claimed kind/schema does not match the source bytes.", "Bind the decoded kind and schema to these exact bytes.")
		return false
	}
	return true
}

func (p *migrationPreview) loadRoot() bool {
	a, ap := p.claim(CanonicalRootContractPath, "project", false)
	b, bp := p.claim(LegacyRootContractPath, "project", false)
	if !ap && !bp {
		p.issue("missing_evidence", "source_missing", CanonicalRootContractPath, "project", "Neither root contract is present.", "Supply the existing .loom/project.yaml or loom.project.yaml and both location claims.")
		return false
	}
	if (ap && a.SchemaVersion == ProjectSchemaV05) || (bp && b.SchemaVersion == ProjectSchemaV05) {
		code := "already_declaration"
		if ap && bp && a.SchemaVersion != b.SchemaVersion {
			code = "root_layout_conflict"
		}
		p.issue("conflict", code, CanonicalRootContractPath, "schema_version", "Existing v0.5 or mixed legacy/declaration roots are not a legacy conversion input.", "Retain all sources; resolve the active-source layout separately without merging claims.")
		return false
	}
	var ca, cb ProjectContract
	valid := true
	if ap {
		valid = p.decode(a, &ca) && valid
	}
	if bp {
		valid = p.decode(b, &cb) && valid
	}
	if !valid {
		return false
	}
	if ap && bp && !projectContractsSemanticallyEqual(ca, cb) {
		p.issue("conflict", "root_layout_conflict", b.Ref, "project", "Canonical and legacy root definitions disagree.", "Retain both digests and resolve the conflicting project definitions separately.")
		return false
	}
	p.rootRef, p.contract = a.Ref, ca
	if !ap {
		p.rootRef, p.contract = b.Ref, cb
	}
	p.contract = NormalizeContract(p.contract)
	if p.contract.Kind != ProjectKind || (p.contract.SchemaVersion != ProjectSchemaV03 && p.contract.SchemaVersion != ProjectSchemaV04) {
		p.issue("invalid_source", "source_identity_mismatch", p.rootRef, "schema_version", "Root is not a supported legacy project contract.", "Supply loom.project using project.contract.v0.3 or project.contract.v0.4.")
		return false
	}
	p.document = ProjectDeclaration{Kind: ProjectKind, SchemaVersion: ProjectSchemaV05, Project: p.contract.Project, Resources: map[ResourceKey]ResourceDeclaration{}}
	return true
}

func (p *migrationPreview) projectIdentity() {
	x := p.contract.Project
	if x.ID == "" && p.contract.SchemaVersion == ProjectSchemaV03 {
		x.ID = p.in.Project.Project.ID
	}
	if ids.Validate(ids.ProjectPrefix, x.ID) != nil {
		p.issue("invalid_source", "project_identity_invalid", p.rootRef, "project.id", "A valid existing project identity is required; no ID will be allocated.", "Only a missing v0.3 ID can come from its matching existing registered project binding.")
	}
	if x != p.in.Project.Project || x.ID != p.in.Project.Basis.ProjectID || x.OwnerNode != p.in.Project.Basis.OwnerNode || !migrationToken(p.in.Project.LocationRef) || p.in.Project.Source != p.sources[p.rootRef].MigrationSourceClaim {
		p.issue("conflict", "project_binding_conflict", p.rootRef, "project", "Source project identity, lifecycle, location or source attribution disagrees with the supplied registration.", "Recapture the exact existing binding; do not repair identity or lifecycle during conversion.")
	}
	p.document.Project = x
	p.out.Identity.Before, p.out.Identity.After = p.contract.Project, x
	p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: p.rootRef, Field: "schema_version", Before: p.contract.SchemaVersion, After: ProjectSchemaV05, Disposition: "representation_only"})
}

func (p *migrationPreview) legacyFields() {
	if p.in.LegacyReferences != nil {
		p.legacyReferenceMapping()
		return
	}
	for _, name := range []string{"provider_defaults", "portal", "metadata"} {
		if n := migrationNode(p.trees[p.rootRef], name); migrationNonempty(n) {
			p.unmapped(p.rootRef, name, "field_unmapped", "Legacy fields have no complete representation in this conversion subset.")
		}
	}
	keys := make([]string, 0, len(p.contract.Facets))
	for k := range p.contract.Facets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		enabled := p.contract.Facets[k]
		field := "facets." + k
		if !enabled {
			p.issue("unrepresentable", "disabled_intent_unmapped", p.rootRef, field, "This explicit false facet has no generic v0.5 disable field; an explicit policy ref may still select its policy.", "Preserve the switch and selected-policy facts; a separately accepted representation is needed.")
		} else if k != "repos" && k != "backup_policy" {
			p.unmapped(p.rootRef, field, "facet_unmapped", "This enabled facet is outside the complete supported conversion subset.")
		}
		p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: p.rootRef, Field: field, Before: fmt.Sprint(enabled), After: "no_generic_facet_field", Disposition: "source_intent"})
	}
}

// This mapping is pure and explicit. The retained destination must be supplied
// as absent; its hypothetical bytes are only introduced during candidate compile.
func (p *migrationPreview) legacyReferenceMapping() {
	raw, _ := json.Marshal(p.in.LegacyReferences)
	_ = json.Unmarshal(raw, &p.document.LegacyContracts)
	mapping := p.document.LegacyContracts
	invalid := func() {
		p.issue("unsupported_subset", "legacy_reference_unmapped", p.rootRef, "legacy_contracts", "The exact retained-source mapping is outside the supported legacy reference subset.", "Supply a complete typed mapping of unchanged sources and native protection attachments; owner transition remains required.")
	}
	original := p.sources[p.rootRef]
	destination, exists := p.sources[mapping.Project.Ref]
	if !declarationPath(mapping.Project.Ref, true) || mapping.Project.Ref == CanonicalRootContractPath || mapping.Project.Ref == LegacyRootContractPath || !exists || destination.State != "absent" || !migrationDigestValid(mapping.Project.Digest) || mapping.Project.Digest != original.Digest || mapping.Project.SchemaVersion != original.SchemaVersion {
		invalid()
		return
	}
	var contract ProjectContract
	if decodeLegacySource(original.Raw, &contract) != nil || legacyProjectSubset(contract, p.document) != nil {
		invalid()
		return
	}
	add := func(d Diagnostic) {
		if d.Severity == SeverityError {
			invalid()
		}
	}
	if r := mapping.Notes; r != nil {
		var notes NotesContract
		if decodeLegacySource(p.sources[r.Ref].Raw, &notes) != nil {
			invalid()
		} else {
			p.legacyNotes = notesFacetItems(notes, p.sources[r.Ref].Raw, r.Ref, nil, add)
		}
	}
	if r := mapping.Repos; r != nil {
		var repos ReposContract
		if decodeLegacySource(p.sources[r.Ref].Raw, &repos) != nil {
			invalid()
		} else {
			p.legacyRepos = repoFacetItems(repos, p.sources[r.Ref].Raw, r.Ref, nil, add)
		}
	}
	p.out.Retention.PlannedCopies = []MigrationSourceCopy{{From: migrationSourceClaimForReport(original.MigrationSourceClaim), To: mapping.Project, State: "pending"}}
	p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: p.rootRef, Field: "legacy_contracts", Before: "exact_original_sources", After: "pending_retained_reference", Disposition: "representation_only"})
}

func (p *migrationPreview) unmapped(ref, field, code, message string) {
	p.issue("unsupported_subset", code, ref, field, message, "Retain this source and its existing behavior; a separate complete target mapping is required.")
	n := migrationNode(p.trees[ref], field)
	p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: ref, Field: field, Before: migrationPresence(n), After: "unmapped", Disposition: "refused"})
}

func (p *migrationPreview) dependencies() {
	for _, kind := range []string{ProjectContractNotes, ProjectContractRepos, ProjectContractSync, ProjectContractBackup, ProjectContractWorkers, ProjectContractCredentials} {
		def := singletonContractDefinitions[kind]
		explicit := ""
		switch kind {
		case ProjectContractSync:
			explicit = p.contract.Policies.Sync
		case ProjectContractBackup:
			explicit = p.contract.Policies.Backup
		case ProjectContractWorkers:
			explicit = p.contract.Policies.Workers
		case ProjectContractCredentials:
			explicit = p.contract.Policies.Credentials
		}
		if !p.contract.Facets[def.Facet] && explicit == "" {
			continue
		}
		field := "facets." + def.Facet
		if explicit != "" {
			field = "policies." + kind
			if kind == ProjectContractCredentials {
				field = "policies.credentials"
			}
		}
		ref, ok := p.singleton(kind, explicit, field)
		if ok && kind != ProjectContractRepos && kind != ProjectContractBackup && !(p.in.LegacyReferences != nil && kind == ProjectContractNotes) {
			p.unmapped(ref, "", "policy_unmapped", "Selected legacy policy or Notes behavior is outside this conversion subset.")
			p.inspectUnsupported(p.sources[ref], kind)
		}
		if ok {
			if mapping := p.document.LegacyContracts; mapping != nil {
				var imported *LegacyWatchReference
				switch kind {
				case ProjectContractNotes:
					imported = mapping.Notes
				case ProjectContractRepos:
					imported = mapping.Repos
				}
				if imported != nil && imported.Ref != ref {
					p.issue("conflict", "legacy_selected_source_conflict", ref, "legacy_contracts", "Imported reference differs from the original selected singleton.", "Retain the original selected source identity and both location claims.")
				}
			}
			p.singletons[kind] = ref
		}
	}
	for _, kind := range []string{"scripts", "workflows", "connectors", "modules", "schedules", "direct_events", "services"} {
		if !p.contract.Facets[kind] {
			continue
		}
		ref := kind
		if kind == "services" {
			ref = ".loom/contracts/services"
		}
		var found *MigrationCollection
		for i := range p.in.Collections {
			if p.in.Collections[i].Ref == ref {
				found = &p.in.Collections[i]
				break
			}
		}
		if found == nil || found.Completeness != "complete" {
			p.issue("missing_evidence", "collection_incomplete", ref, "facets."+kind, "The activated contract collection is not a complete supplied listing.", "Supply the complete selected collection, including an explicit empty listing if empty.")
			continue
		}
		for _, child := range found.Entries {
			s, ok := p.claim(child, "facets."+kind, true)
			if !ok {
				continue
			}
			p.inspectUnsupported(s, kind)
		}
	}
}

func (p *migrationPreview) singleton(kind, explicit, field string) (string, bool) {
	def := singletonContractDefinitions[kind]
	a, ap := p.claim(def.Canonical, field, false)
	b, bp := p.claim(def.Legacy, field, false)
	if explicit != "" && !migrationRef(explicit) {
		p.issue("invalid_source", "source_ref_invalid", p.rootRef, field, "Selected policy ref is not a safe exact relative location.", "Retain the source and resolve its unsafe reference separately.")
		return "", false
	}
	compare := explicit == "" || explicit == a.Ref || explicit == b.Ref
	if compare && ap && bp {
		for _, source := range []MigrationSource{a, b} {
			if target := migrationSingletonType(kind); target != nil {
				p.decode(source, target)
			}
		}
		equal, err := yamlDocumentsSemanticallyEqual(a.Raw, b.Raw)
		if err != nil || !equal {
			p.issue("conflict", "singleton_layout_conflict", b.Ref, field, "Both standard singleton locations contain conflicting or invalid definitions.", "Retain both source digests and resolve the competing definitions separately.")
			return "", false
		}
	}
	ref := explicit
	if ref == "" {
		ref = def.Canonical
		preferCanonical := kind == ProjectContractNotes || kind == ProjectContractRepos || p.rootRef != LegacyRootContractPath
		if !preferCanonical {
			ref = def.Legacy
		}
		if ap && bp {
			ref = def.Canonical
		} else if ap {
			ref = def.Canonical
		} else if bp {
			ref = def.Legacy
		}
	}
	_, ok := p.claim(ref, field, true)
	return ref, ok
}

func migrationSingletonType(kind string) any {
	switch kind {
	case ProjectContractNotes:
		return &NotesContract{}
	case ProjectContractRepos:
		return &ReposContract{}
	case ProjectContractBackup:
		return &ProjectBackupPolicyContract{}
	case ProjectContractSync:
		return &ProjectSyncPolicyContract{}
	case ProjectContractCredentials:
		return &CredentialPolicy{}
	}
	return nil
}

func (p *migrationPreview) inspectUnsupported(s MigrationSource, kind string) {
	target := migrationSingletonType(kind)
	if kind == "services" {
		target = &ServiceRegistrationContract{}
	}
	valid := true
	if target != nil {
		valid = p.decode(s, target)
	}
	defaults := map[string]string{}
	if valid && kind == ProjectContractNotes {
		n := normalizeNotesContract(*target.(*NotesContract))
		defaults["notes.status"] = n.Notes.Status
		defaults["notes.sync"] = fmt.Sprint(boolValue(n.Notes.Sync, true))
		defaults["notes.index"] = fmt.Sprint(boolValue(n.Notes.Index, true))
		defaults["notes.backup"] = fmt.Sprint(boolValue(n.Notes.Backup, false))
	}
	if kind == "services" {
		if valid {
			service, err := ParseServiceRegistrationContract(s.Raw)
			if err != nil {
				p.issue("invalid_source", "service_source_invalid", s.Ref, "service", "The retained service registration does not satisfy its existing closed contract.", "Resolve service identity/path/operation semantics separately; do not synthesize an application.")
			} else {
				for _, op := range []struct {
					name  string
					value *bool
				}{{"status", service.Operations.Status}, {"start", service.Operations.Start}, {"stop", service.Operations.Stop}, {"restart", service.Operations.Restart}, {"logs", service.Operations.Logs}} {
					defaults["operations."+op.name] = fmt.Sprint(boolValue(op.value, false))
				}
				expected, _ := KeyedProjectContractPath("services", service.Service.Key)
				if s.Ref != expected || service.Service.Class == ServiceClassSystem {
					p.issue("invalid_source", "service_source_invalid", s.Ref, "service.key", "The service key/path or project service class conflicts with existing registration rules.", "Retain and resolve the exact project service contract separately.")
				}
			}
		}
		p.issue("unrepresentable", "service_unrepresentable", s.Ref, "runtime.unit", "This contract registers an existing service unit; it does not supply a v0.5 application manifest or executable intent.", "Retain the service registration, operations and references; application conversion requires a separate accepted mapping.")
	}
	for _, field := range []string{"notes.status", "notes.sync", "notes.index", "notes.backup", "material", "sync.enabled", "operations.status", "operations.start", "operations.stop", "operations.restart", "operations.logs", "health", "references"} {
		n := migrationNode(p.trees[s.Ref], field)
		before := migrationPresence(n)
		if value, ok := defaults[field]; ok && n == nil {
			before = "absent_default_" + value
		}
		if n != nil || defaults[field] != "" {
			p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: s.Ref, Field: field, Before: before, After: "unmapped", Disposition: "retain_evidence"})
		}
	}
}

func (p *migrationPreview) repositories() {
	if p.in.LegacyReferences != nil {
		// The shared reference compiler below requires explicit empty membership.
		return
	}
	ref := p.singletons[ProjectContractRepos]
	if ref == "" {
		return
	}
	s := p.sources[ref]
	var r ReposContract
	if !p.decode(s, &r) {
		return
	}
	if r.Kind != ReposContractKind || (r.SchemaVersion != ReposSchemaV03 && r.SchemaVersion != ReposSchemaV04) {
		p.issue("invalid_source", "source_identity_mismatch", ref, "schema_version", "Repository source has an unsupported kind/schema.", "Supply a strict supported repos contract.")
		return
	}
	if r.SchemaVersion == ReposSchemaV03 || len(r.Repos.WatchRoots) > 0 || len(r.Repos.Roots) > 0 {
		p.unmapped(ref, "repos", "watch_intent_unmapped", "Repository watches remain distinct from membership; v0.3 empty roots also create a default watch.")
	}
	for _, x := range []struct{ field, code string }{{"repos.status", "repos_status_unmapped"}, {"repos.defaults", "repos_defaults_unmapped"}, {"metadata", "field_unmapped"}} {
		n := migrationNode(p.trees[ref], x.field)
		if (x.field == "repos.status" && n != nil) || migrationNonempty(n) {
			p.unmapped(ref, x.field, x.code, "Explicit repository status/default/metadata intent is outside the supported membership-only mapping.")
		}
	}
	seenID, seenKey, seenPath := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, m := range r.Repos.Members {
		field := fmt.Sprintf("repos.members[%d]", i)
		if m.ID == "" {
			p.issue("invalid_source", "member_id_required", ref, field+".id", "A v0.4 member must already contain its typed repository ID.", "Resolve invalid legacy source separately; the preview never repairs or allocates member IDs.")
		}
		if !declarationRepositoryStateRoot(m.StateRoot) {
			p.issue("unrepresentable", "state_root_unrepresentable", ref, field+".state_root", "Repository state_root must remain an unchanged canonical safe repository-relative path.", "Retain the member source and resolve unsafe or noncanonical state intent separately.")
		}
		path, err := normalizeRepositoryMemberPath(m.Path)
		if ids.Validate(RepositoryIDPrefix, m.ID) != nil || err != nil || hasReservedRepositoryPath(path, false) || seenID[m.ID] || seenKey[m.Key] || seenPath[path] {
			p.issue("conflict", "member_identity_conflict", ref, field, "Member identity/key/location is invalid or duplicated.", "Supply unique existing source membership without repair or ownership changes.")
			continue
		}
		seenID[m.ID], seenKey[m.Key], seenPath[path] = true, true, true
		key := ResourceKey(m.Key)
		p.document.Resources[key] = ResourceDeclaration{Kind: DeclarationRepository, Repository: &RepositoryDeclaration{ID: m.ID, Path: migrationRepositoryPath(path), Role: m.Role, StateRoot: m.StateRoot}}
		p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: ref, Field: field + ".path", Before: "repos_relative", After: "project_relative_same_location", Disposition: "representation_only"})
	}
}

func (p *migrationPreview) backup() {
	ref := p.singletons[ProjectContractBackup]
	if ref == "" {
		return
	}
	s := p.sources[ref]
	var policy ProjectBackupPolicyContract
	if !p.decode(s, &policy) {
		return
	}
	if policy.Kind != BackupPolicyKind || policy.SchemaVersion != BackupPolicySchemaV03 {
		p.issue("invalid_source", "source_identity_mismatch", ref, "schema_version", "Backup source has an unsupported kind/schema.", "Supply the unchanged backup.policy.v0.3 source.")
		return
	}
	if len(policy.Backup.Roots) == 0 {
		p.unmapped(ref, "backup.roots", "backup_empty_unmapped", "An empty-root policy is not necessarily invalid; its no-target mapping is outside this subset.")
		return
	}
	// Parsing from owned bytes created fresh slices; normalization cannot mutate
	// caller input. The compiler likewise receives its own decoded policy.
	normalized := normalizeBackupPolicy(policy)
	seenPath := map[string]bool{}
	for i, r := range normalized.Backup.Roots {
		field := fmt.Sprintf("backup.roots[%d]", i)
		rawPath := migrationNode(p.trees[ref], field+".path")
		if rawPath == nil || (!declarationPath(rawPath.Value, false) && rawPath.Value != ".") || r.Path != rawPath.Value {
			p.issue("unrepresentable", "backup_target_unrepresentable", ref, field+".path", "v0.5 requires the unchanged exact project-relative policy path; legacy normalization cannot repair it.", "Retain this policy; resolve unclean or implicit paths in a separately accepted change.")
			continue
		}
		if seenPath[r.Path] {
			p.issue("conflict", "backup_target_conflict", ref, field+".path", "Several policy roots select the same exact target.", "Retain both roots and resolve the competing coverage separately.")
			continue
		}
		seenPath[r.Path] = true
		key := r.Key
		if key == "" {
			key = deriveLocalRootKey(r.Path)
		}
		if mapping := p.document.LegacyContracts; mapping != nil {
			for _, n := range p.legacyNotes {
				if n.RootKey == key && n.ProjectPath == r.Path {
					key = string(mapping.Notes.Protection)
				}
			}
			for _, repo := range p.legacyRepos {
				if repo.Key == r.Key && repo.ProjectPath == r.Path {
					key = string(mapping.Repos.Protection)
				}
			}
		}
		if _, exists := p.document.Resources[ResourceKey(key)]; exists || !declarationKey(key) {
			p.issue("conflict", "resource_key_conflict", ref, field+".key", "An existing normalized root key is invalid or collides with another resource.", "Keep owner keys unchanged; resolve the target resource namespace separately.")
			continue
		}
		p.document.Resources[ResourceKey(key)] = ResourceDeclaration{Kind: DeclarationProtection, Protection: &ProtectionDeclaration{PolicyRef: ref, Path: r.Path}}
	}
	p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: ref, Field: "backup.enabled", Before: migrationPresence(migrationNode(p.trees[ref], "backup.enabled")), After: fmt.Sprint(boolValue(policy.Backup.Enabled, true)), Disposition: "unchanged_policy_bytes"})
	loaded := LoadedProject{RootPath: ".", ContractPath: p.rootRef, Contract: p.contract}
	builder := newWatchPolicyBuilder(loaded, p.contract, func(d Diagnostic) {
		if d.Severity == SeverityError {
			p.issue("invalid_source", "backup_compile_invalid", ref, d.Field, "Legacy backup policy cannot compile to a valid owner configuration.", "Resolve the invalid policy separately without altering source in this preview.")
		}
	})
	builder.sourceOnly = true
	for _, n := range p.legacyNotes {
		builder.addNotes(n)
	}
	for _, r := range p.legacyRepos {
		builder.addRepo(r)
	}
	builder.addBackupPolicySource(normalized, ref)
	p.before = builder.items()
	for _, r := range p.before {
		p.out.BeforeRoots = append(p.out.BeforeRoots, migrationRoot(r))
	}
}

func (p *migrationPreview) existingFacts() {
	members := map[string]MigrationMember{}
	for _, m := range p.in.Membership.Members {
		if _, ok := members[m.Key]; ok {
			p.issue("conflict", "membership_conflict", "", "membership", "Existing membership keys are duplicated.", "Supply the complete unique current membership snapshot.")
		}
		members[m.Key] = m
	}
	expectedCount := 0
	for _, key := range declarationKeys(p.document.Resources) {
		r := p.document.Resources[key].Repository
		if r == nil {
			continue
		}
		expectedCount++
		m, ok := members[string(key)]
		validOwner := ids.Validate(ids.ProjectPrefix, m.OwnerProjectID) == nil && (r.Role == RepositoryRoleReference || m.OwnerProjectID == p.document.Project.ID)
		ref := p.singletons[ProjectContractRepos]
		if !ok || m.ID != r.ID || m.Path != r.Path || m.Role != r.Role || m.StateRoot != r.StateRoot || m.Lifecycle != "active" || !migrationToken(m.Revision) || !validOwner || m.Source != p.sources[ref].MigrationSourceClaim {
			p.issue("conflict", "membership_conflict", ref, "repos.members", "Declared member ID/location/role/ownership/lifecycle/source does not match existing membership.", "Supply the same complete existing member set; do not enroll, retire, repair or resurrect members during conversion.")
		} else {
			p.out.Identity.Members = append(p.out.Identity.Members, m)
		}
	}
	if len(members) != expectedCount {
		p.issue("conflict", "membership_conflict", "", "membership", "Source and registered member sets differ.", "Account for every existing member; registry-only rows do not create source membership.")
	}
	watchByKey := map[string]MigrationWatch{}
	for _, w := range p.in.Watches.Roots {
		k := w.Root.BackendRootKey
		if _, ok := watchByKey[k]; ok || !migrationToken(w.Owner) || !migrationToken(w.Revision) || !migrationRootValid(w.Root) {
			p.issue("conflict", "watch_conflict", "", "watches", "Existing watch identity/configuration is invalid or duplicated.", "Supply complete normalized owner configurations with their exact revisions.")
		}
		watchByKey[k] = w
	}
	for _, r := range p.out.BeforeRoots {
		w, ok := watchByKey[r.BackendRootKey]
		if !ok || !reflect.DeepEqual(r, w.Root) {
			p.issue("conflict", "watch_conflict", "", "watches", "Existing watch configuration differs from complete source-derived intent.", "Account for every enrolled root and compare full normalized configuration, including limits, patterns and safe-root binding.")
		}
	}
	if len(watchByKey) != len(p.out.BeforeRoots) {
		p.issue("conflict", "watch_conflict", "", "watches", "Existing enrolled roots are missing or unaccounted for by the supported source subset.", "Retain all watches; supply a complete mapping or refuse conversion.")
	}
	coverage := map[string]MigrationCoverage{}
	for _, c := range p.in.Coverage.Coverage {
		k := c.Owner + "/" + c.Root.BackendRootKey
		if _, ok := coverage[k]; ok || !migrationToken(c.Owner) || !migrationToken(c.Revision) || !migrationRootValid(c.Root) || c.Root.Config.BackupPolicy.Mode == watchedroots.BackupModeNone {
			p.issue("conflict", "coverage_conflict", "", "coverage", "Coverage attribution/configuration is invalid or duplicated.", "Supply exact effective coverage owners, full configs and revisions.")
			continue
		}
		coverage[k] = c
		if c.External {
			conflict := false
			for _, w := range p.in.Watches.Roots {
				if c.Root.BackendRootKey == w.Root.BackendRootKey || (c.Root.OwnerNode == w.Root.OwnerNode && c.Root.Config.SafeRootKey == w.Root.Config.SafeRootKey && declarationOverlap(c.Root.Config.RootRelativePath, w.Root.Config.RootRelativePath)) {
					conflict = true
				}
			}
			if conflict {
				p.issue("conflict", "coverage_conflict", "", "coverage", "External coverage conflicts with project-owned root identity or coverage.", "Retain both owners and resolve the conflicting attribution separately.")
			} else {
				p.out.Retention.ExternalCoverage = append(p.out.Retention.ExternalCoverage, c)
			}
		} else {
			w, ok := watchByKey[c.Root.BackendRootKey]
			if !ok || c.Owner != w.Owner || c.Revision != w.Revision || !reflect.DeepEqual(c.Root, w.Root) {
				p.issue("conflict", "coverage_conflict", "", "coverage", "Project coverage has no matching qualified watch owner/configuration.", "Supply the complete coverage contribution without changing owner identity.")
			}
		}
	}
	for _, w := range p.in.Watches.Roots {
		if w.Root.Config.BackupPolicy.Mode != watchedroots.BackupModeNone {
			c, ok := coverage[w.Owner+"/"+w.Root.BackendRootKey]
			if !ok || c.External {
				p.issue("conflict", "coverage_conflict", "", "coverage", "An owned protected root lacks matching effective coverage evidence.", "Supply complete effective coverage, including independent inherited owners.")
			}
		}
	}
	seenLegacy := map[string]bool{}
	for _, f := range p.in.Legacy.Facts {
		address := f.Kind + "/" + f.Key
		if !seenLegacy[address] && f.Kind == "facet" && (f.Key == "repos" || f.Key == "backup_policy" || (p.in.LegacyReferences != nil && (f.Key == "notes" || f.Key == "portal"))) && p.contract.Facets[f.Key] && f.State == "enabled" && migrationToken(f.Revision) && f.Source == p.sources[p.rootRef].MigrationSourceClaim {
			p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: p.rootRef, Field: "registered_facets." + f.Key, Before: "enabled", After: "explicit_resources", Disposition: "representation_only"})
		} else {
			// Unknown registrations cannot be discarded just because current
			// source no longer contains their facet/service.
			p.issue("unsupported_subset", "legacy_behavior_unmapped", f.Source.Ref, "legacy", "An existing legacy facet/service registration is not represented by this candidate subset.", "Retain its disabled/active state and source evidence; provide a complete accepted mapping.")
		}
		seenLegacy[address] = true
	}
	seenEvidence := map[string]bool{}
	archive := false
	for _, e := range p.in.Evidence.Evidence {
		if (e.Kind != "archive" && e.Kind != "disabled") || !migrationRef(e.Ref) || !migrationDigestValid(e.Digest) || !migrationToken(e.Owner) || !migrationToken(e.Revision) || seenEvidence[e.Ref] {
			p.issue("conflict", "evidence_conflict", "", "evidence", "Archive/disabled evidence is incomplete, unsafe or duplicated.", "Supply unique owner-qualified evidence refs/digests/revisions without payload bytes.")
			continue
		}
		seenEvidence[e.Ref] = true
		archive = archive || e.Kind == "archive"
		p.out.Retention.Evidence = append(p.out.Retention.Evidence, e)
	}
	if p.document.Project.Status == ProjectStatusArchived && !archive {
		p.issue("missing_evidence", "archive_evidence_required", p.rootRef, "project.status", "The archived project has no qualified retained archive evidence.", "Supply its existing opaque archive evidence refs and owner revisions; do not create an archive in this preview.")
	}
}

func (p *migrationPreview) candidate() {
	raw, err := yaml.Marshal(p.document)
	var d ProjectDeclaration
	if err == nil {
		d, err = ParseProjectDeclaration(raw)
	}
	if err != nil {
		p.issue("invalid_source", "candidate_invalid", p.rootRef, "resources", "The complete candidate fails existing v0.5 identity/key/role/path/overlap validation.", "Resolve the target representability conflict separately; no partial candidate is available.")
		return
	}
	if len(p.out.Issues) > 0 {
		return
	}
	loaded := LoadedProject{RootPath: ".", ContractPath: CanonicalRootContractPath, Raw: raw, Contract: ProjectContract{Kind: d.Kind, SchemaVersion: d.SchemaVersion, Project: d.Project}, Declaration: &d}
	c := DeclarationCompilation{Document: d, Sources: []DeclarationSourceSnapshot{declarationSnapshot(CanonicalRootContractPath, ProjectSchemaV05, raw)}, Repositories: []RepoMemberSpec{}, Protection: []DeclarationProtectionSelection{}, Errors: []DeclarationError{}}
	seenSources := map[string]bool{CanonicalRootContractPath: true}
	if mapping := d.LegacyContracts; mapping != nil {
		for _, ref := range legacyReferences(d) {
			source := p.sources[ref.Ref]
			if ref.Ref == mapping.Project.Ref {
				source = p.sources[p.rootRef]
			}
			c.Sources = append(c.Sources, declarationSnapshot(ref.Ref, ref.SchemaVersion, source.Raw))
			seenSources[ref.Ref] = true
		}
		for _, kind := range []string{ProjectContractNotes, ProjectContractRepos} {
			if p.singletons[kind] == "" {
				continue
			}
			def := singletonContractDefinitions[kind]
			for _, ref := range []string{def.Canonical, def.Legacy} {
				if source := p.sources[ref]; source.State == "present" && !seenSources[ref] {
					c.Sources = append(c.Sources, declarationSnapshot(ref, source.SchemaVersion, source.Raw))
					seenSources[ref] = true
				}
			}
		}
	}
	for _, key := range declarationKeys(d.Resources) {
		r := d.Resources[key]
		if r.Repository != nil {
			c.Repositories = append(c.Repositories, RepoMemberSpec{ID: r.Repository.ID, Key: string(key), Path: r.Repository.Path, Role: r.Repository.Role, StateRoot: r.Repository.StateRoot})
		}
		if r.Protection == nil {
			continue
		}
		ref := r.Protection.PolicyRef
		selection, snapshot, err := compileDeclarationProtectionSource(loaded, d, key, p.sources[ref].Raw, func(diag Diagnostic) {
			if diag.Severity == SeverityError {
				p.issue("invalid_source", "backup_compile_invalid", ref, diag.Field, "Selected backup configuration is invalid under the existing target compiler.", "Retain the policy and resolve selected configuration, even when disabled, separately.")
			}
		}, nil)
		if err != nil {
			p.issue("invalid_source", "backup_compile_invalid", ref, "backup", "Selected backup source cannot be compiled losslessly by the v0.5 compiler.", "Preserve policy bytes and resolve exact target/key/configuration semantics separately.")
			continue
		}
		c.Protection = append(c.Protection, selection)
		if !seenSources[ref] {
			c.Sources = append(c.Sources, snapshot)
			seenSources[ref] = true
		}
		def := singletonContractDefinitions[ProjectContractBackup]
		for _, alt := range []string{def.Canonical, def.Legacy} {
			if (ref == def.Canonical || ref == def.Legacy) && !seenSources[alt] && p.sources[alt].State == "present" {
				c.Sources = append(c.Sources, declarationSnapshot(alt, BackupPolicySchemaV03, p.sources[alt].Raw))
				seenSources[alt] = true
			}
		}
	}
	if len(p.out.Issues) > 0 {
		return
	}
	if d.LegacyContracts != nil {
		var err error
		c.LegacyContracts, err = CompileDeclarationLegacyReferences(loaded, c)
		if err != nil {
			p.issue("unsupported_subset", "legacy_reference_unmapped", p.rootRef, "legacy_contracts", "The candidate cannot preserve the complete referenced legacy intent.", "Retain the exact source bytes and resolve the full identity, watch, provider and protection mapping separately.")
			return
		}
		providers := derivedProviders(p.contract, p.rootRef, func(d Diagnostic) {
			if d.Severity == SeverityError {
				p.issue("invalid_source", "provider_invalid", p.rootRef, "provider_defaults", "Legacy provider derivation is invalid.", "Retain the source and resolve provider identity separately.")
			}
		})
		if !declarationJSONEqual(providers, c.LegacyContracts.DerivedProviders) {
			p.issue("conflict", "provider_conflict", p.rootRef, "provider_defaults", "Derived provider identities differ.", "Preserve normalized provider defaults and deduplication.")
			return
		}
	}
	after, err := CompileDeclarationEnrollment(loaded, c)
	if err != nil {
		p.issue("conflict", "enrollment_conflict", p.rootRef, "resources", "Source-bound target enrollment compilation refused the complete candidate.", "Resolve the complete ownership/configuration conflict without partial conversion.")
		return
	}
	for _, r := range after {
		p.out.AfterRoots = append(p.out.AfterRoots, migrationRoot(r))
	}
	if !reflect.DeepEqual(p.out.BeforeRoots, p.out.AfterRoots) {
		p.issue("conflict", "watch_conflict", p.rootRef, "resources", "Candidate changes normalized watch/backup configuration or owner identity.", "Preserve the entire before/after configuration; a changed watch is not syntax conversion.")
		return
	}
	// Only declaration_sources may be added to backup metadata. Unknown owner
	// metadata differences are not excused by equal counts or config hashes.
	for i, r := range after {
		meta := map[string]any{}
		for k, v := range r.Metadata {
			if k != "declaration_sources" {
				meta[k] = v
			}
		}
		if !declarationJSONEqual(p.before[i].Metadata, meta) {
			p.issue("conflict", "watch_conflict", p.rootRef, "resources", "Candidate changes source-kind or owner metadata beyond declaration attribution.", "Retain metadata semantics or define a complete accepted mapping.")
			return
		}
	}
	p.out.Candidate = &MigrationCandidate{PlannedSourceRef: CanonicalRootContractPath, YAML: string(raw), Digest: declarationHash(raw), Comparison: "equivalent_for_supplied_basis"}
	p.out.Disposition = "candidate"
	p.out.Fields = append(p.out.Fields, MigrationFieldComparison{Ref: p.rootRef, Field: "source_attribution", Before: "retained_legacy_sources", After: "hypothetical_candidate_digest", Disposition: "representation_only"})
}

func migrationRoot(r ProjectWatchedRootItem) MigrationRootIntent {
	var config watchedroots.RootConfig
	_ = json.Unmarshal(r.ConfigJSON, &config)
	return MigrationRootIntent{Key: r.Key, BackendRootKey: r.BackendRootKey, WorkerKey: r.WorkerKey, OwnerNode: r.OwnerNode, SourceKinds: append([]string{}, r.SourceKinds...), Config: config, ConfigHash: r.ConfigHash}
}

func migrationRootValid(r MigrationRootIntent) bool {
	if !migrationToken(r.Key) || !migrationToken(r.BackendRootKey) || !migrationToken(r.WorkerKey) || !nodeKeyPattern.MatchString(r.OwnerNode) || r.BackendRootKey != r.Config.RootKey || (r.Config.RootRelativePath != "." && !migrationRef(r.Config.RootRelativePath)) {
		return false
	}
	for _, k := range r.SourceKinds {
		if !migrationToken(k) {
			return false
		}
	}
	config, err := watchedroots.ValidatePortableRootConfig(r.Config)
	return err == nil && reflect.DeepEqual(config, r.Config) && watchedroots.ConfigHash(config) == r.ConfigHash
}

// Filter a presentation copy before either report destination receives it.
// Original source claims remain untouched for validation and fingerprinting.
func migrationSourceClaimForReport(claim MigrationSourceClaim) MigrationSourceClaim {
	if !migrationToken(claim.Kind) {
		claim.Kind = ""
	}
	if !migrationToken(claim.SchemaVersion) {
		claim.SchemaVersion = ""
	}
	if !migrationDigestValid(claim.Digest) {
		claim.Digest = ""
	}
	switch claim.State {
	case "present", "absent", "unreadable", "not_supplied":
	default:
		claim.State = "invalid"
	}
	return claim
}

func (p *migrationPreview) finish() DeclarationMigrationPreview {
	for ref, s := range p.sources {
		if !migrationRef(ref) {
			continue
		}
		claim := migrationSourceClaimForReport(s.MigrationSourceClaim)
		r := MigrationSourceReport{MigrationSourceClaim: claim, Selection: []string{}, Disposition: "dormant_unselected"}
		if trigger := p.selected[ref]; trigger != "" {
			r.Selection = append(r.Selection, trigger)
			r.Disposition = "retain_evidence"
		}
		if ref == p.singletons[ProjectContractBackup] {
			r.Disposition = "retain_dependency"
		}
		if ref == p.rootRef {
			r.Disposition = "candidate_source"
		}
		if s.State == "present" {
			if migrationDigestValid(s.Digest) {
				p.out.Retention.Sources = append(p.out.Retention.Sources, claim)
			}
			for _, pair := range [][2]string{{CanonicalRootContractPath, LegacyRootContractPath}, {singletonContractDefinitions[ProjectContractRepos].Canonical, singletonContractDefinitions[ProjectContractRepos].Legacy}, {singletonContractDefinitions[ProjectContractBackup].Canonical, singletonContractDefinitions[ProjectContractBackup].Legacy}} {
				if ref == pair[1] && p.sources[pair[0]].State == "present" {
					equal, _ := yamlDocumentsSemanticallyEqual(s.Raw, p.sources[pair[0]].Raw)
					if pair[0] == CanonicalRootContractPath {
						var a, b ProjectContract
						if left, right := p.trees[pair[0]], p.trees[ref]; left != nil && right != nil && left.Decode(&a) == nil && right.Decode(&b) == nil {
							equal = projectContractsSemanticallyEqual(a, b)
						}
					}
					if equal {
						r.DuplicateOf = pair[0]
					}
				}
			}
		}
		p.out.Sources = append(p.out.Sources, r)
	}
	sort.Slice(p.out.Sources, func(i, j int) bool { return p.out.Sources[i].Ref < p.out.Sources[j].Ref })
	sort.Slice(p.out.Retention.Sources, func(i, j int) bool { return p.out.Retention.Sources[i].Ref < p.out.Retention.Sources[j].Ref })
	sort.Slice(p.out.Identity.Members, func(i, j int) bool { return p.out.Identity.Members[i].Key < p.out.Identity.Members[j].Key })
	sort.Slice(p.out.Retention.Evidence, func(i, j int) bool { return p.out.Retention.Evidence[i].Ref < p.out.Retention.Evidence[j].Ref })
	sort.Slice(p.out.Retention.ExternalCoverage, func(i, j int) bool {
		return p.out.Retention.ExternalCoverage[i].Owner+"/"+p.out.Retention.ExternalCoverage[i].Root.BackendRootKey < p.out.Retention.ExternalCoverage[j].Owner+"/"+p.out.Retention.ExternalCoverage[j].Root.BackendRootKey
	})
	sort.Slice(p.out.Fields, func(i, j int) bool {
		a, b := p.out.Fields[i], p.out.Fields[j]
		return a.Ref+"\x00"+a.Field+"\x00"+a.Disposition < migrationSortKey(b.Ref, b.Field, b.Disposition)
	})
	sort.Slice(p.out.Issues, func(i, j int) bool {
		a, b := p.out.Issues[i], p.out.Issues[j]
		return migrationSortKey(a.Ref, a.Field, a.Category, a.CauseCode, a.Message) < migrationSortKey(b.Ref, b.Field, b.Category, b.CauseCode, b.Message)
	})
	issues := []MigrationIssue{}
	for _, x := range p.out.Issues {
		if len(issues) == 0 || issues[len(issues)-1] != x {
			issues = append(issues, x)
		}
	}
	p.out.Issues = issues
	if len(issues) > 0 {
		p.out.Candidate = nil
		p.out.Disposition = "refused"
	}
	// Sort private fact copies for fingerprinting without mutating input or
	// erasing false/absent source claims. Raw bytes have their own exact digests.
	in := p.in
	in.Membership.Members = migrationSorted(in.Membership.Members)
	in.Watches.Roots = migrationSorted(in.Watches.Roots)
	in.Coverage.Coverage = migrationSorted(in.Coverage.Coverage)
	in.Legacy.Facts = migrationSorted(in.Legacy.Facts)
	in.Evidence.Evidence = migrationSorted(in.Evidence.Evidence)
	p.out.BasisFingerprint = migrationDigest(struct {
		Source           string                      `json:"source_fingerprint"`
		Project          MigrationProjectFacts       `json:"project"`
		Membership       MigrationMembershipFacts    `json:"membership"`
		Watches          MigrationWatchFacts         `json:"watches"`
		Coverage         MigrationCoverageFacts      `json:"coverage"`
		Legacy           MigrationLegacyFacts        `json:"legacy"`
		Evidence         MigrationEvidenceFacts      `json:"evidence"`
		LegacyReferences *LegacyContractsDeclaration `json:"legacy_references,omitempty"`
	}{p.out.SourceFingerprint, in.Project, in.Membership, in.Watches, in.Coverage, in.Legacy, in.Evidence, in.LegacyReferences})
	return p.out
}

func migrationSortKey(parts ...string) string { return strings.Join(parts, "\x00") }
func migrationSorted[T any](values []T) []T {
	v := append([]T{}, values...)
	sort.Slice(v, func(i, j int) bool { return migrationDigest(v[i]) < migrationDigest(v[j]) })
	return v
}
func migrationDigest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return declarationHash(raw)
}
func migrationDigestValid(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func migrationToken(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "/\\") && strings.IndexFunc(s, unicode.IsControl) < 0
}
func migrationText(s string) bool {
	return len(s) <= 512 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
func migrationRef(s string) bool {
	return len(s) <= 1024 && utf8.ValidString(s) && declarationPath(s, true)
}
func migrationPresence(n *yaml.Node) string {
	if n == nil {
		return "absent"
	}
	if n.Tag == "!!null" {
		return "null"
	}
	if n.Tag == "!!bool" {
		return strings.ToLower(n.Value)
	}
	if len(n.Content) == 0 && (n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode || n.Value == "") {
		return "empty"
	}
	return "present"
}
func migrationNonempty(n *yaml.Node) bool { return n != nil && migrationPresence(n) != "empty" }
func migrationNode(n *yaml.Node, field string) *yaml.Node {
	if n == nil {
		return nil
	}
	if field == "" {
		return n
	}
	for _, part := range strings.Split(field, ".") {
		index := -1
		if at := strings.IndexByte(part, '['); at >= 0 {
			if _, err := fmt.Sscanf(part[at:], "[%d]", &index); err != nil {
				return nil
			}
			part = part[:at]
		}
		if n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Value == part {
				next = n.Content[i+1]
				break
			}
		}
		n = next
		if n == nil {
			return nil
		}
		if index >= 0 {
			if n.Kind != yaml.SequenceNode || index >= len(n.Content) {
				return nil
			}
			n = n.Content[index]
		}
	}
	return n
}

func migrationRepositoryPath(relative string) string { return path.Join("repos", relative) }
