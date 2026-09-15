package projectcontracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

const dmDir = "testdata/declaration_migration_v1"
const dmProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
const dmOtherProject = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"

type dmCorpus struct {
	Positive []string          `json:"positive"`
	Negative map[string]string `json:"negative"`
}
type dmFixture struct {
	Input    DeclarationMigrationInput
	Root     string
	Analysis Analysis
	Files    map[string]string
}

func dmRead(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dmDir, name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func dmHash(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }
func dmSource(ref string, raw []byte) MigrationSource {
	var h struct {
		Kind   string `yaml:"kind"`
		Schema string `yaml:"schema_version"`
	}
	_ = yaml.Unmarshal(raw, &h)
	return MigrationSource{MigrationSourceClaim: MigrationSourceClaim{Ref: ref, State: "present", Kind: h.Kind, SchemaVersion: h.Schema, Digest: dmHash(raw), Size: int64(len(raw))}, Raw: append([]byte{}, raw...)}
}
func dmAbsent(ref string) MigrationSource {
	return MigrationSource{MigrationSourceClaim: MigrationSourceClaim{Ref: ref, State: "absent"}}
}
func dmIndex(in *DeclarationMigrationInput, ref string) int {
	for i, s := range in.Sources {
		if s.Ref == ref {
			return i
		}
	}
	return -1
}
func dmSet(in *DeclarationMigrationInput, ref string, raw []byte) {
	v := dmSource(ref, raw)
	if i := dmIndex(in, ref); i >= 0 {
		in.Sources[i] = v
	} else {
		in.Sources = append(in.Sources, v)
	}
}
func dmReplace(in *DeclarationMigrationInput, ref, old, new string) {
	i := dmIndex(in, ref)
	if i < 0 {
		panic(ref)
	}
	dmSet(in, ref, []byte(strings.Replace(string(in.Sources[i].Raw), old, new, 1)))
}
func dmRootRef(in *DeclarationMigrationInput) string {
	if i := dmIndex(in, CanonicalRootContractPath); i >= 0 && in.Sources[i].State == "present" {
		return CanonicalRootContractPath
	}
	return LegacyRootContractPath
}
func dmRootAdd(in *DeclarationMigrationInput, raw string) {
	ref := dmRootRef(in)
	i := dmIndex(in, ref)
	dmSet(in, ref, append(append([]byte{}, in.Sources[i].Raw...), []byte(raw)...))
}

// Independently follows the documented wire fingerprint recipe, not the
// implementation's hashing or normalization helpers.
func dmSourceFingerprint(in DeclarationMigrationInput) string {
	sources := make([]MigrationSourceClaim, 0, len(in.Sources))
	for _, s := range in.Sources {
		sources = append(sources, s.MigrationSourceClaim)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Ref < sources[j].Ref })
	collections := make([]MigrationCollection, 0, len(in.Collections))
	for _, c := range in.Collections {
		c.Entries = append([]string{}, c.Entries...)
		sort.Strings(c.Entries)
		collections = append(collections, c)
	}
	sort.Slice(collections, func(i, j int) bool { return collections[i].Ref < collections[j].Ref })
	raw, _ := json.Marshal(struct {
		Sources     []MigrationSourceClaim `json:"sources"`
		Collections []MigrationCollection  `json:"collections"`
	}{sources, collections})
	return dmHash(raw)
}
func dmRebind(in *DeclarationMigrationInput) {
	fingerprint := dmSourceFingerprint(*in)
	for _, b := range []*MigrationFactBasis{&in.Project.Basis, &in.Membership.Basis, &in.Watches.Basis, &in.Coverage.Basis, &in.Legacy.Basis, &in.Evidence.Basis} {
		b.SourceFingerprint = fingerprint
	}
	ref := dmRootRef(in)
	if i := dmIndex(in, ref); i >= 0 {
		in.Project.Source = in.Sources[i].MigrationSourceClaim
	}
	for i := range in.Membership.Members {
		ref := in.Membership.Members[i].Source.Ref
		if j := dmIndex(in, ref); j >= 0 {
			in.Membership.Members[i].Source = in.Sources[j].MigrationSourceClaim
		}
	}
}
func dmPortable(t *testing.T, r ProjectWatchedRootItem) MigrationRootIntent {
	t.Helper()
	var cfg watchedroots.RootConfig
	if e := json.Unmarshal(r.ConfigJSON, &cfg); e != nil {
		t.Fatal(e)
	}
	return MigrationRootIntent{Key: r.Key, BackendRootKey: r.BackendRootKey, WorkerKey: r.WorkerKey, OwnerNode: r.OwnerNode, SourceKinds: append([]string{}, r.SourceKinds...), Config: cfg, ConfigHash: r.ConfigHash}
}
func dmWrite(t *testing.T, root, ref string, raw []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(ref))
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func dmTree(t *testing.T, sources []MigrationSource) string {
	t.Helper()
	root := t.TempDir()
	for _, ref := range []string{"repos/api", "repos/shared", "notes", "data/docs", "data/études"} {
		if e := os.MkdirAll(filepath.Join(root, ref), 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range sources {
		if s.State == "present" {
			dmWrite(t, root, s.Ref, s.Raw)
		}
	}
	dmWrite(t, root, "repos/api/.repo/identity.json", []byte("untouched development state\n"))
	dmWrite(t, root, "repos/api/.git/HEAD", []byte("ref: refs/heads/fixture\n"))
	dmWrite(t, root, "data/docs/payload.txt", []byte("fixture payload café\n"))
	if e := os.Chmod(filepath.Join(root, "data/docs/payload.txt"), 0640); e != nil {
		t.Fatal(e)
	}
	return root
}
func dmFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	e := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		ref, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		value := i.Mode().String()
		if !d.IsDir() {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			value += "/" + dmHash(b)
		}
		out[ref] = value
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func dmUnchanged(t *testing.T, f dmFixture) {
	t.Helper()
	if !reflect.DeepEqual(f.Files, dmFiles(t, f.Root)) {
		t.Fatal("legacy source/payload/.repo/Git bytes or modes changed")
	}
}

func dmMake(t *testing.T, name string) dmFixture {
	t.Helper()
	in := DeclarationMigrationInput{Sources: []MigrationSource{dmSource(CanonicalRootContractPath, dmRead(t, "project-v04.yaml")), dmAbsent(LegacyRootContractPath)}, Collections: []MigrationCollection{}}
	repos := name == "repositories" || name == "combined"
	backup := name == "backup" || name == "backup_disabled" || name == "combined" || name == "legacy_layout" || name == "custom_policy" || name == "equivalent_dual_policy" || name == "default_backup_key" || name == "external_coverage"
	if name == "plain_v03" {
		dmSet(&in, CanonicalRootContractPath, dmRead(t, "project-v03.yaml"))
	}
	if repos {
		dmRootAdd(&in, "facets: {repos: true}\n")
		in.Sources = append(in.Sources, dmSource(".loom/contracts/repos.yaml", dmRead(t, "repos-v04.yaml")), dmAbsent("repos/loom.repos.yaml"))
	}
	if backup {
		ref := ".loom/contracts/backup.yaml"
		if name == "custom_policy" {
			ref = "custom/backup.yaml"
		}
		dmRootAdd(&in, "policies: {backup: "+ref+"}\n")
		in.Sources = append(in.Sources, dmAbsent(".loom/contracts/backup.yaml"), dmAbsent("policies/backup.yaml"))
		file := "backup-enabled.yaml"
		if name == "backup_disabled" || name == "external_coverage" {
			file = "backup-disabled.yaml"
		}
		dmSet(&in, ref, dmRead(t, file))
		if name == "default_backup_key" {
			dmReplace(&in, ref, "    - key: documents\n      path: data/docs", "    - path: data/docs")
		}
	}
	if name == "legacy_layout" {
		raw := in.Sources[0].Raw
		dmSet(&in, LegacyRootContractPath, []byte(strings.Replace(string(raw), "policies: {backup: .loom/contracts/backup.yaml}", "facets: {backup_policy: true}", 1)))
		in.Sources[0] = dmAbsent(CanonicalRootContractPath)
		i := dmIndex(&in, ".loom/contracts/backup.yaml")
		dmSet(&in, "policies/backup.yaml", in.Sources[i].Raw)
		in.Sources[i] = dmAbsent(".loom/contracts/backup.yaml")
	}
	if name == "equivalent_dual_root" {
		dmSet(&in, LegacyRootContractPath, append([]byte("# equivalent older copy\n"), in.Sources[0].Raw...))
	}
	if name == "equivalent_dual_policy" {
		dmSet(&in, "policies/backup.yaml", append([]byte("# retained duplicate\n"), in.Sources[dmIndex(&in, ".loom/contracts/backup.yaml")].Raw...))
	}
	if name == "empty_containers" {
		dmRootAdd(&in, "facets: {}\nprovider_defaults: {}\npolicies: {}\nportal: {}\nmetadata: {}\n")
	}
	if name == "paused" || name == "archived" {
		dmReplace(&in, CanonicalRootContractPath, "status: active", "status: "+name)
	}
	root := dmTree(t, in.Sources)
	analysis := Analyze(root)
	if !analysis.Report.OK || analysis.Loaded == nil {
		t.Fatalf("independent legacy fixture invalid: %+v", analysis.Report.Diagnostics)
	}
	var bindings struct {
		ProjectID         string `json:"project_id"`
		Node              string `json:"node"`
		LocationRevision  string `json:"location_revision"`
		RegistryRevision  string `json:"registry_revision"`
		LifecycleRevision string `json:"lifecycle_revision"`
		SnapshotRevision  string `json:"snapshot_revision"`
	}
	if e := json.Unmarshal(dmRead(t, "existing-bindings.json"), &bindings); e != nil {
		t.Fatal(e)
	}
	basis := MigrationFactBasis{SchemaVersion: DeclarationMigrationFactsSchema, Completeness: "complete", ProjectID: bindings.ProjectID, OwnerNode: bindings.Node, LocationRevision: bindings.LocationRevision, RegistryRevision: bindings.RegistryRevision, LifecycleRevision: bindings.LifecycleRevision, SnapshotRevision: bindings.SnapshotRevision}
	project := NormalizeContract(analysis.Loaded.Contract).Project
	if project.ID == "" {
		project.ID = dmProjectID
	}
	in.Project = MigrationProjectFacts{Basis: basis, Project: project, LocationRef: "fixture-location"}
	in.Membership = MigrationMembershipFacts{Basis: basis, Members: []MigrationMember{}}
	in.Watches = MigrationWatchFacts{Basis: basis, Roots: []MigrationWatch{}}
	in.Coverage = MigrationCoverageFacts{Basis: basis, Coverage: []MigrationCoverage{}}
	in.Legacy = MigrationLegacyFacts{Basis: basis, Facts: []MigrationLegacyFact{}}
	in.Evidence = MigrationEvidenceFacts{Basis: basis, Evidence: []MigrationEvidence{}}
	for _, m := range analysis.Report.RepositoryMembers {
		sourceRef, e := filepath.Rel(root, analysis.Report.RepositorySource.ContractPath)
		if e != nil {
			t.Fatal(e)
		}
		owner := dmProjectID
		if m.Role == RepositoryRoleReference {
			owner = dmOtherProject
		}
		in.Membership.Members = append(in.Membership.Members, MigrationMember{ID: m.ID, Key: m.Key, Path: "repos/" + m.Path, Role: m.Role, OwnerProjectID: owner, Lifecycle: "active", Revision: "membership-1", Source: in.Sources[dmIndex(&in, filepath.ToSlash(sourceRef))].MigrationSourceClaim})
	}
	for _, r := range analysis.Report.WatchedRoots {
		portable := dmPortable(t, r)
		in.Watches.Roots = append(in.Watches.Roots, MigrationWatch{Owner: "projectwatch", Revision: "owner-1", Root: portable})
		if r.BackupMode != watchedroots.BackupModeNone {
			in.Coverage.Coverage = append(in.Coverage.Coverage, MigrationCoverage{Owner: "projectwatch", Revision: "owner-1", Root: portable})
		}
	}
	if name == "archived" {
		in.Evidence.Evidence = append(in.Evidence.Evidence, MigrationEvidence{Kind: "archive", Ref: "archive/record.json", Digest: dmHash([]byte("archive-evidence")), Owner: "projects", Revision: "archive-1"})
	}
	if name == "external_coverage" {
		ext := dmMake(t, "backup").Input.Watches.Roots[0].Root
		ext.Key = "external_docs"
		ext.BackendRootKey = ProjectWatchedRootKey("other", ext.Key)
		ext.WorkerKey = noderuntime.WatchedRootWorkerKey(ext.BackendRootKey)
		ext.Config.RootKey = ext.BackendRootKey
		ext.Config.RootRelativePath = "inherited/data"
		ext.ConfigHash = watchedroots.ConfigHash(ext.Config)
		in.Coverage.Coverage = append(in.Coverage.Coverage, MigrationCoverage{Owner: "external-backup", Revision: "external-1", External: true, Root: ext})
	}
	dmRebind(&in)
	return dmFixture{Input: in, Root: root, Analysis: analysis, Files: dmFiles(t, root)}
}

func dmAssertCandidate(t *testing.T, out DeclarationMigrationPreview) ProjectDeclaration {
	t.Helper()
	if out.Disposition != "candidate" || out.Candidate == nil || len(out.Issues) != 0 {
		t.Fatalf("expected whole candidate, got %+v", out.Issues)
	}
	if out.SchemaVersion != "project.declaration_migration_preview.v1" || out.BasisKind != "supplied_snapshot" || out.RuntimeAssessment != "not_performed" || out.Candidate.Comparison != "equivalent_for_supplied_basis" || out.Candidate.PlannedSourceRef != CanonicalRootContractPath {
		t.Fatal("dishonest result envelope")
	}
	if out.Candidate.Digest != dmHash([]byte(out.Candidate.YAML)) {
		t.Fatal("candidate digest is not exact bytes")
	}
	d, e := ParseProjectDeclaration([]byte(out.Candidate.YAML))
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func dmAssertRefusal(t *testing.T, out DeclarationMigrationPreview, code string) {
	t.Helper()
	var expected struct {
		Disposition string   `json:"disposition"`
		BasisKind   string   `json:"basis_kind"`
		Runtime     string   `json:"runtime_assessment"`
		Categories  []string `json:"categories"`
	}
	if e := json.Unmarshal(dmRead(t, "expected-blockers.json"), &expected); e != nil {
		t.Fatal(e)
	}
	if out.Disposition != expected.Disposition || out.Candidate != nil || out.BasisKind != expected.BasisKind || out.RuntimeAssessment != expected.Runtime {
		t.Fatalf("partial/dishonest refusal: %+v", out)
	}
	found := false
	for _, issue := range out.Issues {
		if issue.CauseCode == code {
			found = true
		}
		valid := false
		for _, category := range expected.Categories {
			if issue.Category == category {
				valid = true
			}
		}
		if !valid || issue.Message == "" || issue.Required == "" {
			t.Fatalf("unusable issue: %+v", issue)
		}
	}
	if !found {
		t.Fatalf("missing frozen cause %s: %+v", code, out.Issues)
	}
}
func dmParity(t *testing.T, f dmFixture, out DeclarationMigrationPreview) {
	t.Helper()
	d := dmAssertCandidate(t, out)
	sources := []MigrationSource{dmSource(CanonicalRootContractPath, []byte(out.Candidate.YAML))}
	for _, s := range f.Input.Sources {
		if s.State == "present" && s.Ref != CanonicalRootContractPath && s.Ref != LegacyRootContractPath {
			sources = append(sources, s)
		}
	}
	root := dmTree(t, sources)
	before := dmFiles(t, root)
	a := Analyze(root)
	if !a.Report.OK || a.Report.Declaration == nil {
		t.Fatalf("normal candidate compiler refused: %+v", a.Report.Diagnostics)
	}
	if !reflect.DeepEqual(d, a.Report.Declaration.Document) {
		t.Fatal("normal parser differs")
	}
	wantMembers := f.Analysis.Report.RepositoryMembers
	gotMembers := a.Report.Declaration.Repositories
	if len(wantMembers) != len(gotMembers) {
		t.Fatal("membership count changed")
	}
	for i, m := range wantMembers {
		if m.ID != gotMembers[i].ID || m.Key != gotMembers[i].Key || m.Role != gotMembers[i].Role || m.StateRoot != gotMembers[i].StateRoot || "repos/"+m.Path != gotMembers[i].Path {
			t.Fatalf("membership location/identity changed: %+v / %+v", m, gotMembers[i])
		}
	}
	if len(f.Analysis.Report.WatchedRoots) != len(a.Report.WatchedRoots) {
		t.Fatal("owner root count changed")
	}
	for i, r := range f.Analysis.Report.WatchedRoots {
		if !reflect.DeepEqual(dmPortable(t, r), dmPortable(t, a.Report.WatchedRoots[i])) {
			t.Fatal("full independent before/after owner config differs")
		}
	}
	for _, source := range a.Report.Declaration.Sources {
		if source.Hash != dmHash(source.Raw) {
			t.Fatal("compiler source bytes changed")
		}
		if source.Ref == CanonicalRootContractPath && source.Hash != out.Candidate.Digest {
			t.Fatal("candidate attribution mismatch")
		}
	}
	if !reflect.DeepEqual(before, dmFiles(t, root)) {
		t.Fatal("candidate parity validator mutated temporary source/payload/modes")
	}
	dmUnchanged(t, f)
}

func TestDeclarationMigrationPositiveParity(t *testing.T) {
	var corpus dmCorpus
	if e := json.Unmarshal(dmRead(t, "cases.json"), &corpus); e != nil {
		t.Fatal(e)
	}
	for _, name := range corpus.Positive {
		t.Run(name, func(t *testing.T) {
			f := dmMake(t, name)
			snapshot := dmInputSnapshot(f.Input)
			out := PreviewDeclarationMigration(f.Input)
			d := dmAssertCandidate(t, out)
			dmParity(t, f, out)
			expected := ""
			switch name {
			case "plain_v04", "plain_v03":
				expected = "expected-plain.yaml"
			case "repositories":
				expected = "expected-repositories.yaml"
			case "backup", "backup_disabled":
				expected = "expected-protection.yaml"
			}
			if expected != "" {
				want, e := ParseProjectDeclaration(dmRead(t, expected))
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(d, want) {
					t.Fatalf("frozen expected document changed: %+v", d)
				}
			}
			if !bytes.Equal(snapshot, dmInputSnapshot(f.Input)) {
				t.Fatal("input mutated")
			}
			again := PreviewDeclarationMigration(f.Input)
			a, _ := json.Marshal(out)
			b, _ := json.Marshal(again)
			if !bytes.Equal(a, b) {
				t.Fatal("preview is not deterministic")
			}
			for i, j := 0, len(f.Input.Sources)-1; i < j; i, j = i+1, j-1 {
				f.Input.Sources[i], f.Input.Sources[j] = f.Input.Sources[j], f.Input.Sources[i]
			}
			reverseMembers := f.Input.Membership.Members
			for i, j := 0, len(reverseMembers)-1; i < j; i, j = i+1, j-1 {
				reverseMembers[i], reverseMembers[j] = reverseMembers[j], reverseMembers[i]
			}
			reverseWatches := f.Input.Watches.Roots
			for i, j := 0, len(reverseWatches)-1; i < j; i, j = i+1, j-1 {
				reverseWatches[i], reverseWatches[j] = reverseWatches[j], reverseWatches[i]
			}
			reverseCoverage := f.Input.Coverage.Coverage
			for i, j := 0, len(reverseCoverage)-1; i < j; i, j = i+1, j-1 {
				reverseCoverage[i], reverseCoverage[j] = reverseCoverage[j], reverseCoverage[i]
			}
			ordered, _ := json.Marshal(PreviewDeclarationMigration(f.Input))
			if !bytes.Equal(a, ordered) {
				t.Fatal("source/fact list order changed report")
			}
			if name == "backup" {
				if out.BeforeRoots[0].Config.BackupPolicy.MaxPendingBytes != 9007199254740993 {
					t.Fatal("large integer lost")
				}
			}
			if name == "external_coverage" && len(out.Retention.ExternalCoverage) != 1 {
				t.Fatal("external coverage lost")
			}
		})
	}
}
func dmInputSnapshot(in DeclarationMigrationInput) []byte {
	type rawSource struct {
		Claim MigrationSourceClaim
		Raw   string
	}
	sources := []rawSource{}
	for _, s := range in.Sources {
		sources = append(sources, rawSource{s.MigrationSourceClaim, string(s.Raw)})
	}
	b, _ := json.Marshal(struct {
		Input   DeclarationMigrationInput
		Sources []rawSource
	}{in, sources})
	return b
}

func dmNegative(t *testing.T, name string) dmFixture {
	t.Helper()
	base := "plain_v04"
	if strings.HasPrefix(name, "member_") || name == "missing_member_id" || strings.HasPrefix(name, "custom_state") || strings.HasPrefix(name, "repos_") || strings.HasPrefix(name, "facts_member") || name == "facts_retired_member" || name == "facts_foreign_owner" {
		base = "repositories"
	}
	if strings.HasPrefix(name, "backup_") || strings.HasPrefix(name, "facts_changed_config") || name == "facts_missing_coverage" || name == "dual_policy_conflict" || name == "unreadable_policy" || name == "missing_policy_fact" || name == "false_backup_facet_explicit_ref" {
		base = "backup"
	}
	if name == "backup_key_collision" {
		base = "combined"
	}
	if name == "backup_invalid_disabled" {
		base = "backup_disabled"
	}
	if name == "facts_missing_archive" {
		base = "archived"
	}
	if name == "facts_invalid_external" {
		base = "external_coverage"
	}
	f := dmMake(t, base)
	in := &f.Input
	root := dmRootRef(in)
	repo := ".loom/contracts/repos.yaml"
	backup := ".loom/contracts/backup.yaml"
	rebind := true
	switch name {
	case "missing_project_id_v04":
		dmReplace(in, root, "  id: "+dmProjectID+"\n", "")
	case "missing_project_binding":
		in.Project.Basis.Completeness = "unavailable"
	case "missing_member_id":
		dmReplace(in, repo, "id: repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", "id: ''")
	case "member_state_root":
		dmReplace(in, repo, "      role: primary", "      role: primary\n      state_root: .repo")
	case "custom_state_root":
		dmReplace(in, repo, "      role: primary", "      role: primary\n      state_root: state")
	case "member_duplicate":
		dmReplace(in, repo, "repo_01ARZ3NDEKTSV4RRFFQ69G5FAW", "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	case "member_role":
		dmReplace(in, repo, "role: primary", "role: unknown")
	case "member_overlap":
		dmReplace(in, repo, "path: shared", "path: api/nested")
	case "repos_v03_default":
		dmSet(in, repo, []byte("kind: loom.repos\nschema_version: repos.contract.v0.3\nrepos: {}\n"))
	case "repos_v04_watch_false":
		dmReplace(in, repo, "repos:\n", "repos:\n  watch_roots: [{key: watch, path: api, sync: false, index: false, backup: false}]\n")
	case "repos_status_disabled":
		dmReplace(in, repo, "repos:\n", "repos:\n  status: disabled\n")
	case "repos_defaults":
		dmReplace(in, repo, "repos:\n", "repos:\n  defaults: {sync: false}\n")
	case "false_facet":
		dmRootAdd(in, "facets: {notes: false}\n")
	case "false_backup_facet_explicit_ref":
		dmRootAdd(in, "facets: {backup_policy: false}\n")
	case "missing_repos":
		dmRootAdd(in, "facets: {repos: true}\n")
		in.Sources = append(in.Sources, dmAbsent(repo), dmAbsent("repos/loom.repos.yaml"))
	case "unreadable_policy":
		in.Sources[dmIndex(in, backup)] = MigrationSource{MigrationSourceClaim: MigrationSourceClaim{Ref: backup, State: "unreadable"}}
	case "missing_policy_fact":
		i := dmIndex(in, "policies/backup.yaml")
		in.Sources = append(in.Sources[:i], in.Sources[i+1:]...)
	case "notes":
		dmRootAdd(in, "facets: {notes: true}\n")
		in.Sources = append(in.Sources, dmSource(".loom/contracts/notes.yaml", []byte("kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes: {sync: false, index: false, backup: false, status: disabled}\n")), dmAbsent("notes/loom.notes.yaml"))
	case "sync":
		dmRootAdd(in, "policies: {sync: .loom/contracts/sync.yaml}\n")
		in.Sources = append(in.Sources, dmSource(".loom/contracts/sync.yaml", []byte("kind: loom.project_sync_policy\nschema_version: sync.policy.v0.3\nsync: {enabled: false, roots: []}\n")), dmAbsent("policies/sync.yaml"))
	case "workers":
		dmRootAdd(in, "policies: {workers: .loom/contracts/workers.yaml}\n")
		in.Sources = append(in.Sources, dmSource(".loom/contracts/workers.yaml", []byte("kind: loom.worker_policy\nschema_version: worker.policy.v0.3\nworkers: {}\n")), dmAbsent("policies/workers.yaml"))
	case "credentials":
		dmRootAdd(in, "policies: {credentials: .loom/contracts/credentials.yaml}\n")
		in.Sources = append(in.Sources, dmSource(".loom/contracts/credentials.yaml", []byte("kind: loom.credentials_policy\nschema_version: credentials.policy.v0.3\ncredentials: {}\n")), dmAbsent("policies/credentials.yaml"))
	case "services":
		dmRootAdd(in, "facets: {services: true}\n")
		ref := ".loom/contracts/services/api.yaml"
		in.Collections = append(in.Collections, MigrationCollection{Ref: ".loom/contracts/services", Completeness: "complete", Entries: []string{ref}})
		in.Sources = append(in.Sources, dmSource(ref, []byte("kind: service_registration\nschema_version: loom.service.v0.1\nservice: {key: api, name: API, target_node: fixture-node}\nruntime: {manager: systemd, unit: api.service}\noperations: {start: false}\n")))
	case "scripts_missing_collection":
		dmRootAdd(in, "facets: {scripts: true}\n")
	case "provider":
		dmRootAdd(in, "provider_defaults: {scripts_provider: project}\n")
	case "portal":
		dmRootAdd(in, "portal: {summary: legacy-display}\n")
	case "metadata":
		dmRootAdd(in, "metadata: {private: forbidden-secret-marker}\n")
	case "null_facet":
		dmRootAdd(in, "facets: {repos: null}\n")
	case "unknown_field":
		dmRootAdd(in, "unknown: forbidden-secret-marker\n")
	case "duplicate_yaml":
		dmRootAdd(in, "kind: loom.project\n")
	case "alias_yaml":
		dmRootAdd(in, "metadata: &alias {x: 1}\nportal: *alias\n")
	case "trailing_yaml":
		dmRootAdd(in, "---\nsecret: forbidden-secret-marker\n")
	case "malformed_utf8":
		dmSet(in, root, append(in.Sources[0].Raw, 0xff))
	case "digest":
		in.Sources[0].Digest = dmHash([]byte("wrong"))
	case "size":
		in.Sources[0].Size++
	case "schema":
		in.Sources[0].SchemaVersion = ProjectSchemaV03
	case "unsafe_ref":
		in.Sources = append(in.Sources, dmSource("../forbidden-secret-marker", []byte("kind: x")))
	case "duplicate_ref":
		in.Sources = append(in.Sources, in.Sources[0])
	case "missing_root_claim":
		in.Sources = in.Sources[:1]
	case "dual_root_conflict":
		dmSet(in, LegacyRootContractPath, []byte(strings.Replace(string(in.Sources[0].Raw), "Existing project", "Different project", 1)))
	case "dual_policy_conflict":
		dmSet(in, "policies/backup.yaml", []byte(strings.Replace(string(in.Sources[dmIndex(in, backup)].Raw), "max_file_bytes: 4096", "max_file_bytes: 3000", 1)))
	case "already_v05":
		dmSet(in, root, dmRead(t, "expected-plain.yaml"))
	case "mixed_v05":
		dmSet(in, LegacyRootContractPath, dmRead(t, "expected-plain.yaml"))
	case "backup_dot":
		dmReplace(in, backup, "path: data/docs", "path: .")
	case "backup_unclean":
		dmReplace(in, backup, "path: data/docs", "path: data/docs/.")
	case "backup_duplicate":
		dmReplace(in, backup, "path: data/études", "path: data/docs")
	case "backup_overlap":
		dmReplace(in, backup, "path: data/études", "path: data/docs/sub")
	case "backup_key_collision":
		dmReplace(in, backup, "key: documents", "key: api")
	case "backup_invalid_disabled":
		dmReplace(in, backup, "  defaults:\n", "  defaults:\n    mode: invalid-mode\n")
	case "backup_empty":
		dmSet(in, backup, []byte("kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup: {enabled: false, roots: []}\n"))
	case "facts_unavailable":
		in.Coverage.Basis.Completeness = "unavailable"
	case "facts_truncated":
		in.Membership.Basis.Completeness = "truncated"
	case "facts_mixed_generation":
		in.Watches.Basis.RegistryRevision = "registry-2"
	case "facts_source_drift":
		in.Coverage.Basis.SourceFingerprint = dmHash([]byte("stale"))
		rebind = false
	case "facts_project_drift":
		in.Project.Project.Name = "another project"
	case "facts_member_drift":
		in.Membership.Members[0].Path = "repos/wrong"
	case "facts_retired_member":
		in.Membership.Members[0].Lifecycle = "retired"
	case "facts_foreign_owner":
		in.Membership.Members[0].OwnerProjectID = dmOtherProject
	case "facts_extra_member":
		in.Membership.Members = append(in.Membership.Members, MigrationMember{ID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", Key: "extra"})
	case "facts_extra_watch":
		in.Watches.Roots = append(in.Watches.Roots, dmMake(t, "backup").Input.Watches.Roots[0])
	case "facts_changed_config":
		in.Watches.Roots[0].Root.Config.BackupPolicy.MaxPendingBytes++
		in.Watches.Roots[0].Root.ConfigHash = watchedroots.ConfigHash(in.Watches.Roots[0].Root.Config)
	case "facts_missing_coverage":
		in.Coverage.Coverage = []MigrationCoverage{}
	case "facts_unknown_service":
		in.Legacy.Facts = append(in.Legacy.Facts, MigrationLegacyFact{Kind: "service", Key: "old", State: "disabled", Revision: "service-1"})
	case "facts_missing_archive":
		in.Evidence.Evidence = []MigrationEvidence{}
	case "facts_duplicate_evidence":
		e := MigrationEvidence{Kind: "disabled", Ref: "retained/evidence", Digest: dmHash([]byte("evidence")), Owner: "projects", Revision: "evidence-1"}
		in.Evidence.Evidence = []MigrationEvidence{e, e}
	case "facts_invalid_external":
		in.Coverage.Coverage[0].Revision = ""
	case "sources_limit":
		for i := len(in.Sources); i < 257; i++ {
			in.Sources = append(in.Sources, dmAbsent(fmt.Sprintf("extra/%d.yaml", i)))
		}
	case "source_size_limit":
		dmSet(in, root, bytes.Repeat([]byte(" "), (4<<20)+1))
	case "total_bytes_limit":
		for i := 0; i < 3; i++ {
			in.Sources = append(in.Sources, dmSource(fmt.Sprintf("extra/%d.yaml", i), bytes.Repeat([]byte(" "), 3<<20)))
		}
	case "facts_limit":
		in.Evidence.Evidence = make([]MigrationEvidence, 4097)
	default:
		t.Fatalf("frozen case has no mutation: %s", name)
	}
	if rebind {
		dmRebind(in)
	}
	return f
}
func TestDeclarationMigrationFrozenRefusals(t *testing.T) {
	var corpus dmCorpus
	if e := json.Unmarshal(dmRead(t, "cases.json"), &corpus); e != nil {
		t.Fatal(e)
	}
	names := make([]string, 0, len(corpus.Negative))
	for name := range corpus.Negative {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			f := dmNegative(t, name)
			before := dmInputSnapshot(f.Input)
			out := PreviewDeclarationMigration(f.Input)
			expected := corpus.Negative[name]
			// D4c represents dot, but this frozen source-only edit deliberately
			// retains the old data/docs owner facts. It must still refuse.
			if name == "backup_dot" {
				expected = "watch_conflict"
			}
			// These frozen edits still disagree with registered omitted state.
			if name == "member_state_root" || name == "custom_state_root" {
				expected = "membership_conflict"
			}
			dmAssertRefusal(t, out, expected)
			if !bytes.Equal(before, dmInputSnapshot(f.Input)) {
				t.Fatal("refused input mutated")
			}
			dmUnchanged(t, f)
			raw, e := json.Marshal(out)
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(raw, []byte("forbidden-secret-marker")) || bytes.Contains(raw, []byte(f.Root)) {
				t.Fatal("refusal exposed arbitrary values or host paths")
			}
		})
	}
}

func TestDeclarationMigrationPureLibraryBoundary(t *testing.T) {
	f := dmMake(t, "combined")
	input := f.Input
	// Remove the entire owned test fixture before the call: all required source
	// and owner data is now in memory. The preview has no root argument.
	if e := os.RemoveAll(f.Root); e != nil {
		t.Fatal(e)
	}
	dmAssertCandidate(t, PreviewDeclarationMigration(input))
	tree, e := parser.ParseFile(token.NewFileSet(), "declaration_migration.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, imp := range tree.Imports {
		for _, forbidden := range []string{`"os"`, `"os/exec"`, `"time"`, `"database/sql"`, `"net"`, `"net/http"`, `"path/filepath"`} {
			if imp.Path.Value == forbidden {
				t.Fatalf("effect/environment dependency: %s", forbidden)
			}
		}
	}
	forbidden := map[string]bool{"Analyze": true, "Validate": true, "LoadProject": true, "ResolveSingletonContract": true, "LoadBackupPolicyContract": true, "LoadReposContract": true, "existingPathEscapesRoot": true, "validateRepoMembers": true, "New": false}
	ast.Inspect(tree, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && forbidden[id.Name] {
				t.Errorf("filesystem-based helper: %s", id.Name)
			}
			if s, ok := c.Fun.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "ids" && s.Sel.Name != "Validate" {
					t.Error("ID generation dependency")
				}
			}
		}
		return true
	})
}

func TestDeclarationMigrationPresentationAndFingerprint(t *testing.T) {
	f := dmMake(t, "backup_disabled")
	before := PreviewDeclarationMigration(f.Input)
	dmAssertCandidate(t, before)
	if len(before.BeforeRoots) != 0 || len(before.AfterRoots) != 0 || len(before.Retention.Sources) != 2 {
		t.Fatal("disabled coverage or source retention changed")
	}
	found := false
	for _, field := range before.Fields {
		if field.Field == "backup.enabled" && field.Before == "false" && field.After == "false" {
			found = true
		}
	}
	if !found {
		t.Fatal("disabled policy state not reported")
	}
	dmReplace(&f.Input, ".loom/contracts/backup.yaml", "enabled: false", "enabled: false # source byte drift")
	stale := PreviewDeclarationMigration(f.Input)
	dmAssertRefusal(t, stale, "facts_source_conflict")
	if stale.SourceFingerprint == before.SourceFingerprint {
		t.Fatal("byte drift did not change fingerprint")
	}
	dmRebind(&f.Input)
	updated := PreviewDeclarationMigration(f.Input)
	dmAssertCandidate(t, updated)
	if updated.BasisFingerprint == before.BasisFingerprint {
		t.Fatal("source revision not in basis")
	}
	bases := []*MigrationFactBasis{&f.Input.Project.Basis, &f.Input.Membership.Basis, &f.Input.Watches.Basis, &f.Input.Coverage.Basis, &f.Input.Legacy.Basis, &f.Input.Evidence.Basis}
	for _, b := range bases {
		b.SnapshotRevision = "snapshot-2"
	}
	next := PreviewDeclarationMigration(f.Input)
	dmAssertCandidate(t, next)
	if next.BasisFingerprint == updated.BasisFingerprint {
		t.Fatal("owner snapshot not in basis")
	}
	if next.Candidate.Digest != updated.Candidate.Digest {
		t.Fatal("candidate syntax changed for unrelated snapshot revision")
	}
	// Invalid UTF-8 must not collapse distinct supplied revisions through JSON's
	// replacement-character encoding when hashing the qualified fact basis.
	for _, b := range bases {
		b.SnapshotRevision = string([]byte{0xff})
	}
	dmAssertRefusal(t, PreviewDeclarationMigration(f.Input), "facts_incomplete")
	raw, _ := json.Marshal(next)
	for _, word := range []string{`"raw"`, `"readiness"`, `"operation_token"`, `"apply_command"`, `"healthy"`, `"verified"`} {
		if bytes.Contains(raw, []byte(word)) {
			t.Fatalf("preview claimed runtime output: %s", word)
		}
	}
}

// One bounded adversarial review of source closure, namespace collisions,
// retained facts and output privacy. Assertions are captured before repairs.
func TestDeclarationMigrationAdversarialReview(t *testing.T) {
	t.Run("equivalent_policy_claim_schema", func(t *testing.T) {
		f := dmMake(t, "equivalent_dual_policy")
		f.Input.Sources[dmIndex(&f.Input, "policies/backup.yaml")].SchemaVersion = "wrong.schema"
		dmRebind(&f.Input)
		dmAssertRefusal(t, PreviewDeclarationMigration(f.Input), "source_identity_mismatch")
	})
	t.Run("equivalent_policy_alias", func(t *testing.T) {
		f := dmMake(t, "equivalent_dual_policy")
		dmReplace(&f.Input, "policies/backup.yaml", "enabled: true", "enabled: &flag true")
		dmRebind(&f.Input)
		dmAssertRefusal(t, PreviewDeclarationMigration(f.Input), "source_shape_invalid")
	})
	t.Run("normalized_duplicate_root_attribution", func(t *testing.T) {
		f := dmMake(t, "equivalent_dual_root")
		dmReplace(&f.Input, LegacyRootContractPath, "status: active", "status: ACTIVE")
		dmRebind(&f.Input)
		out := PreviewDeclarationMigration(f.Input)
		dmAssertCandidate(t, out)
		found := false
		for _, s := range out.Sources {
			if s.Ref == LegacyRootContractPath && s.DuplicateOf == CanonicalRootContractPath {
				found = true
			}
		}
		if !found {
			t.Fatal("normalized duplicate root attribution missing")
		}
	})
	t.Run("policy_ref_named_repos", func(t *testing.T) {
		f := dmMake(t, "combined")
		dmReplace(&f.Input, CanonicalRootContractPath, "backup: .loom/contracts/backup.yaml", "backup: repos")
		i := dmIndex(&f.Input, ".loom/contracts/backup.yaml")
		dmSet(&f.Input, "repos", f.Input.Sources[i].Raw)
		f.Input.Sources[i] = dmAbsent(".loom/contracts/backup.yaml")
		dmRebind(&f.Input)
		out := PreviewDeclarationMigration(f.Input)
		dmAssertCandidate(t, out)
		found := false
		for _, s := range out.Sources {
			if s.Ref == "repos" && reflect.DeepEqual(s.Selection, []string{"policies.backup"}) {
				found = true
			}
		}
		if !found {
			t.Fatal("source ref collided with internal selector kind")
		}
	})
	t.Run("invalid_digest_never_echoed", func(t *testing.T) {
		f := dmMake(t, "plain_v04")
		f.Input.Sources[0].Digest = "forbidden-secret-marker"
		dmRebind(&f.Input)
		out := PreviewDeclarationMigration(f.Input)
		dmAssertRefusal(t, out, "source_digest_mismatch")
		b, _ := json.Marshal(out)
		if bytes.Contains(b, []byte("forbidden-secret-marker")) {
			t.Fatal("unvalidated digest escaped in report")
		}
	})
	t.Run("supplied_present_identity_mismatch", func(t *testing.T) {
		f := dmMake(t, "custom_policy")
		dmSet(&f.Input, "unused/retained.yaml", []byte("kind: loom.project_backup_policy\nschema_version: backup.policy.v0.3\nbackup: {}\n"))
		f.Input.Sources[len(f.Input.Sources)-1].Kind = "wrong_kind"
		dmRebind(&f.Input)
		dmAssertRefusal(t, PreviewDeclarationMigration(f.Input), "source_identity_mismatch")
	})
	t.Run("converted_registered_facet", func(t *testing.T) {
		f := dmMake(t, "repositories")
		f.Input.Legacy.Facts = []MigrationLegacyFact{{Kind: "facet", Key: "repos", State: "enabled", Revision: "facet-1", Source: f.Input.Project.Source}}
		out := PreviewDeclarationMigration(f.Input)
		dmAssertCandidate(t, out)
		found := false
		for _, field := range out.Fields {
			if field.Field == "registered_facets.repos" && field.Disposition == "representation_only" {
				found = true
			}
		}
		if !found {
			t.Fatal("converted registration fact lost")
		}
	})
	t.Run("repository_dot_location", func(t *testing.T) {
		f := dmMake(t, "repositories")
		ref := ".loom/contracts/repos.yaml"
		dmSet(&f.Input, ref, []byte("kind: loom.repos\nschema_version: repos.contract.v0.4\nrepos:\n  members: [{id: repo_01ARZ3NDEKTSV4RRFFQ69G5FAV, key: api, path: '.', role: primary}]\n"))
		f.Input.Membership.Members = f.Input.Membership.Members[:1]
		f.Input.Membership.Members[0].Path = "repos"
		dmRebind(&f.Input)
		d := dmAssertCandidate(t, PreviewDeclarationMigration(f.Input))
		if d.Resources["api"].Repository.Path != "repos" {
			t.Fatal("physical repo root not preserved")
		}
	})
	t.Run("service_default_operations", func(t *testing.T) {
		f := dmNegative(t, "services")
		ref := ".loom/contracts/services/api.yaml"
		dmSet(&f.Input, ref, []byte("kind: service_registration\nschema_version: loom.service.v0.1\nservice: {key: api, name: API, target_node: fixture-node}\nruntime: {manager: systemd, unit: api.service}\n"))
		dmRebind(&f.Input)
		out := PreviewDeclarationMigration(f.Input)
		dmAssertRefusal(t, out, "service_unrepresentable")
		for _, name := range []string{"status", "logs"} {
			found := false
			for _, field := range out.Fields {
				if field.Ref == ref && field.Field == "operations."+name && field.Before == "absent_default_true" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing inspect-only default %s", name)
			}
		}
	})
	t.Run("notes_default_toggles", func(t *testing.T) {
		f := dmNegative(t, "notes")
		ref := ".loom/contracts/notes.yaml"
		dmSet(&f.Input, ref, []byte("kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes: {}\n"))
		dmRebind(&f.Input)
		out := PreviewDeclarationMigration(f.Input)
		dmAssertRefusal(t, out, "facet_unmapped")
		for _, name := range []string{"sync", "index"} {
			found := false
			for _, field := range out.Fields {
				if field.Ref == ref && field.Field == "notes."+name && field.Before == "absent_default_true" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing Notes default %s", name)
			}
		}
	})
}

func TestDeclarationMigrationSourceReportPrivacy(t *testing.T) {
	markers := []struct{ name, value string }{
		{"unix_path", "/Users/SYNTHETIC_D4A_PRIVATE_MARKER/private"},
		{"windows_path", `C:\SYNTHETIC_D4A_PRIVATE_MARKER\private`},
		{"control", "SYNTHETIC_D4A_PRIVATE_MARKER\tprivate"},
		{"invalid_utf8", string([]byte{0xff})},
	}
	for _, field := range []string{"kind", "schema_version", "state"} {
		for _, marker := range markers {
			t.Run(field+"/"+marker.name, func(t *testing.T) {
				f := dmMake(t, "plain_v04")
				ref := CanonicalRootContractPath
				wantCause := "source_identity_mismatch"
				switch field {
				case "kind":
					f.Input.Sources[0].Kind = marker.value
				case "schema_version":
					f.Input.Sources[0].SchemaVersion = marker.value
				case "state":
					f.Input.Sources[1].State = marker.value
					ref = LegacyRootContractPath
					wantCause = "source_state_invalid"
				}
				dmRebind(&f.Input)
				before := dmInputSnapshot(f.Input)
				out := PreviewDeclarationMigration(f.Input)
				dmAssertRefusal(t, out, wantCause)
				if !bytes.Equal(before, dmInputSnapshot(f.Input)) {
					t.Fatal("presentation filtering mutated original claims or raw bytes")
				}
				dmAssertOriginalMigrationFingerprints(t, f.Input, out)
				t.Logf("original source_fingerprint=%s basis_fingerprint=%s", out.SourceFingerprint, out.BasisFingerprint)
				var claim *MigrationSourceClaim
				for i := range out.Sources {
					if out.Sources[i].Ref == ref {
						claim = &out.Sources[i].MigrationSourceClaim
					}
				}
				if claim == nil {
					t.Fatal("refusal lost source location evidence")
				}
				switch field {
				case "kind":
					if claim.Kind != "" {
						t.Fatal("invalid kind remains in sources")
					}
				case "schema_version":
					if claim.SchemaVersion != "" {
						t.Fatal("invalid schema remains in sources")
					}
				case "state":
					if claim.State != "invalid" {
						t.Fatal("invalid state must have the fixed invalid presentation value")
					}
				}
				if field != "state" {
					found := false
					for _, retained := range out.Retention.Sources {
						if retained.Ref == ref {
							found = true
							if retained != *claim {
								t.Fatal("sources and retention.sources use different claim filtering")
							}
							if retained.Digest != f.Input.Sources[0].Digest || retained.Size != f.Input.Sources[0].Size || retained.State != "present" {
								t.Fatal("retention lost original byte evidence")
							}
						}
					}
					if !found {
						t.Fatal("valid digest was removed with invalid presentation metadata")
					}
				}
				encoded, e := json.Marshal(out)
				if e != nil {
					t.Fatal(e)
				}
				if bytes.Contains(encoded, []byte("SYNTHETIC_D4A_PRIVATE_MARKER")) {
					t.Fatal("private claim escaped elsewhere in refused report")
				}
				// Two private claims may have the same sanitized output, but their original
				// source and fact bases must remain distinguishable for refusal evidence.
				switch field {
				case "kind":
					f.Input.Sources[0].Kind += "-changed"
				case "schema_version":
					f.Input.Sources[0].SchemaVersion += "-changed"
				case "state":
					f.Input.Sources[1].State += "-changed"
				}
				dmRebind(&f.Input)
				changed := PreviewDeclarationMigration(f.Input)
				if changed.SourceFingerprint == out.SourceFingerprint || changed.BasisFingerprint == out.BasisFingerprint {
					t.Fatal("sanitized claims replaced original fingerprint input")
				}
				dmUnchanged(t, f)
			})
		}
	}
	t.Run("unknown_state_token", func(t *testing.T) {
		f := dmMake(t, "plain_v04")
		f.Input.Sources[1].State = "future_state"
		dmRebind(&f.Input)
		out := PreviewDeclarationMigration(f.Input)
		dmAssertRefusal(t, out, "source_state_invalid")
		dmAssertOriginalMigrationFingerprints(t, f.Input, out)
		for _, source := range out.Sources {
			if source.Ref == LegacyRootContractPath && source.State != "invalid" {
				t.Fatal("state filtering must use the closed state set, not generic token validity")
			}
		}
	})
}

// Calculate the existing fingerprint recipe from the original plain input,
// independently of finish's report-claim sanitizer. Fact arrays here are empty
// and already in the canonical order; no production fingerprint helper is used.
func dmAssertOriginalMigrationFingerprints(t *testing.T, in DeclarationMigrationInput, out DeclarationMigrationPreview) {
	t.Helper()
	source := dmSourceFingerprint(in)
	basis, e := json.Marshal(struct {
		Source     string                   `json:"source_fingerprint"`
		Project    MigrationProjectFacts    `json:"project"`
		Membership MigrationMembershipFacts `json:"membership"`
		Watches    MigrationWatchFacts      `json:"watches"`
		Coverage   MigrationCoverageFacts   `json:"coverage"`
		Legacy     MigrationLegacyFacts     `json:"legacy"`
		Evidence   MigrationEvidenceFacts   `json:"evidence"`
	}{source, in.Project, in.Membership, in.Watches, in.Coverage, in.Legacy, in.Evidence})
	if e != nil {
		t.Fatal(e)
	}
	if out.SourceFingerprint != source || out.BasisFingerprint != dmHash(basis) {
		t.Fatal("presentation filtering changed original source/basis fingerprint semantics")
	}
}

func TestDeclarationMigrationSourceReportValidClaims(t *testing.T) {
	for _, state := range []string{"present", "absent", "unreadable", "not_supplied"} {
		t.Run(state, func(t *testing.T) {
			f := dmMake(t, "plain_v04")
			ref := LegacyRootContractPath
			if state == "present" {
				ref = CanonicalRootContractPath
			} else {
				f.Input.Sources[1].State = state
			}
			dmRebind(&f.Input)
			before := dmInputSnapshot(f.Input)
			out := PreviewDeclarationMigration(f.Input)
			if state == "present" || state == "absent" {
				d := dmAssertCandidate(t, out)
				want, e := ParseProjectDeclaration(dmRead(t, "expected-plain.yaml"))
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(d, want) {
					t.Fatal("valid conversion equivalence changed")
				}
			}
			if state == "unreadable" {
				dmAssertRefusal(t, out, "source_unreadable")
			}
			if state == "not_supplied" {
				dmAssertRefusal(t, out, "source_not_supplied")
			}
			dmAssertOriginalMigrationFingerprints(t, f.Input, out)
			found := false
			for _, source := range out.Sources {
				if source.Ref == ref {
					found = true
					if source.MigrationSourceClaim != f.Input.Sources[dmIndex(&f.Input, ref)].MigrationSourceClaim {
						t.Fatal("valid source claim changed")
					}
				}
			}
			if !found {
				t.Fatal("valid source claim omitted")
			}
			for _, retained := range out.Retention.Sources {
				if retained != f.Input.Sources[dmIndex(&f.Input, retained.Ref)].MigrationSourceClaim {
					t.Fatal("valid retention claim changed")
				}
			}
			if !bytes.Equal(before, dmInputSnapshot(f.Input)) {
				t.Fatal("original input changed")
			}
		})
	}
}

// Qualified opt-in positive; the original unsupported corpus remains unchanged.
func TestDeclarationLegacyReferenceMigration(t *testing.T) {
	root, old := legacyReferenceFixture(t)
	declaration := Analyze(root)
	if !declaration.Report.OK {
		t.Fatal(declaration.Report.Diagnostics)
	}
	in := dmMake(t, "plain").Input
	in.Project.Project = NormalizeContract(old.Loaded.Contract).Project
	for _, b := range []*MigrationFactBasis{&in.Project.Basis, &in.Membership.Basis, &in.Watches.Basis, &in.Coverage.Basis, &in.Legacy.Basis, &in.Evidence.Basis} {
		b.OwnerNode = in.Project.Project.OwnerNode
		b.ProjectID = in.Project.Project.ID
	}
	in.Sources = []MigrationSource{dmSource(CanonicalRootContractPath, []byte(legacyReferenceProject)), dmAbsent(LegacyRootContractPath), dmAbsent(".loom/contracts/retained-project-v04.yaml")}
	for _, pair := range []struct{ kind, raw string }{{ProjectContractNotes, legacyReferenceNotes}, {ProjectContractRepos, legacyReferenceRepos}, {ProjectContractBackup, legacyReferenceBackup}} {
		def := singletonContractDefinitions[pair.kind]
		in.Sources = append(in.Sources, dmSource(def.Canonical, []byte(pair.raw)), dmAbsent(def.Legacy))
	}
	for _, r := range old.Report.WatchedRoots {
		portable := dmPortable(t, r)
		in.Watches.Roots = append(in.Watches.Roots, MigrationWatch{Owner: "projectwatch", Revision: "owner-1", Root: portable})
		in.Coverage.Coverage = append(in.Coverage.Coverage, MigrationCoverage{Owner: "projectwatch", Revision: "owner-1", Root: portable})
	}
	for _, key := range []string{"notes", "repos", "backup_policy", "portal"} {
		in.Legacy.Facts = append(in.Legacy.Facts, MigrationLegacyFact{Kind: "facet", Key: key, State: "enabled", Revision: "facet-1", Source: in.Sources[0].MigrationSourceClaim})
	}
	dmRebind(&in)
	// No opt-in: preserve the earlier refusal exactly as a separate subset.
	dmAssertRefusal(t, PreviewDeclarationMigration(in), "field_unmapped")
	in.LegacyReferences = declaration.Loaded.Declaration.LegacyContracts
	// Restore the owned fixture to the supplied pre-conversion layout.
	dmWrite(t, root, CanonicalRootContractPath, []byte(legacyReferenceProject))
	if err := os.Remove(filepath.Join(root, in.LegacyReferences.Project.Ref)); err != nil {
		t.Fatal(err)
	}
	files, before := dmFiles(t, root), migrationDigest(in)
	result := PreviewDeclarationMigration(in)
	candidate := dmAssertCandidate(t, result)
	if !declarationJSONEqual(candidate.LegacyContracts, in.LegacyReferences) || !reflect.DeepEqual(result.BeforeRoots, result.AfterRoots) || len(result.BeforeRoots) != 3 {
		t.Fatal("legacy candidate intent drift")
	}
	if len(result.Retention.PlannedCopies) != 1 {
		t.Fatal("missing exact pending copy")
	}
	copy := result.Retention.PlannedCopies[0]
	if copy.State != "pending" || copy.From != in.Sources[0].MigrationSourceClaim || copy.To != in.LegacyReferences.Project || result.Retention.ArchiveCopy != "not_created" || result.Retention.Publication != "not_performed" {
		t.Fatal("copy described as an existing effect")
	}
	found := false
	for _, source := range result.Sources {
		if source.Ref == copy.To.Ref {
			found = source.State == "absent" && source.Digest == ""
		}
	}
	if !found || migrationDigest(in) != before || !reflect.DeepEqual(files, dmFiles(t, root)) {
		t.Fatal("preview changed or fabricated source inventory")
	}
	for _, field := range []string{"kind", "digest"} {
		t.Run("copy_privacy_"+field, func(t *testing.T) {
			changed := in
			changed.Sources = append([]MigrationSource{}, in.Sources...)
			mapping := *in.LegacyReferences
			changed.LegacyReferences = &mapping
			const marker = "/private/legacy-reference-secret"
			if field == "kind" {
				changed.Sources[0].Kind = marker
			} else {
				changed.Sources[0].Digest = marker
				mapping.Project.Digest = marker
			}
			dmRebind(&changed)
			output := PreviewDeclarationMigration(changed)
			encoded, err := json.Marshal(output)
			if err != nil || output.Candidate != nil || len(output.Issues) == 0 || bytes.Contains(encoded, []byte(marker)) {
				t.Fatal("invalid copy claim leaked into preview")
			}
		})
	}
	for _, mutate := range []struct {
		name   string
		change func(*DeclarationMigrationInput)
	}{
		{"destination_present", func(in *DeclarationMigrationInput) {
			dmSet(in, in.LegacyReferences.Project.Ref, []byte(legacyReferenceProject))
			dmRebind(in)
		}},
		{"destination_unknown", func(in *DeclarationMigrationInput) {
			in.Sources[dmIndex(in, in.LegacyReferences.Project.Ref)] = MigrationSource{MigrationSourceClaim: MigrationSourceClaim{Ref: in.LegacyReferences.Project.Ref, State: "not_supplied"}}
			dmRebind(in)
		}},
		{"wrong_original_digest", func(in *DeclarationMigrationInput) {
			mapping := *in.LegacyReferences
			mapping.Project.Digest = dmHash([]byte("different"))
			in.LegacyReferences = &mapping
		}},
		{"wrong_owner_facts", func(in *DeclarationMigrationInput) {
			in.Watches.Roots = append([]MigrationWatch{}, in.Watches.Roots...)
			in.Watches.Roots[0].Root.ConfigHash = dmHash([]byte("wrong"))
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			changed := in
			changed.Sources = append([]MigrationSource{}, in.Sources...)
			mutate.change(&changed)
			output := PreviewDeclarationMigration(changed)
			if output.Candidate != nil || len(output.Issues) == 0 || output.BasisFingerprint == result.BasisFingerprint {
				t.Fatal("invalid mapping or facts accepted/unbound")
			}
		})
	}
}
