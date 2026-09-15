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
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/ids"
)

// All helpers below are fixture reference checks, NOT a runtime v0.5 loader,
// compiler, authorization validator, owner dispatcher or semantic acceptance.
const declarationFixtureDir = "testdata/declarations_v05/"

func TestDeclarationV05NegativeFixtures(t *testing.T) {
	var corpus struct {
		SchemaVersion string `json:"schema_version"`
		Cases         []struct {
			Name       string          `json:"name"`
			ExpectCode string          `json:"expect_code"`
			Document   json.RawMessage `json:"document"`
		} `json:"cases"`
	}
	if err := fixtureJSON(fixtureRead(t, "invalid.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != "declaration.fixture.v0.5" || len(corpus.Cases) != 34 {
		t.Fatal("unexpected fixture corpus")
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, code := fixtureDeclaration(c.Document)
			if code != c.ExpectCode {
				t.Fatalf("got %q, want %q", code, c.ExpectCode)
			}
		})
	}
}

func TestDeclarationV05RoundTrip(t *testing.T) {
	for _, name := range []string{"minimal.yaml", "webdav.yaml"} {
		t.Run(name, func(t *testing.T) {
			source := fixtureRead(t, name)
			d, code := fixtureDeclaration(source)
			if code != "" {
				t.Fatal(code)
			}
			j, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			decoded, code := fixtureDeclaration(j)
			if code != "" || !reflect.DeepEqual(d, decoded) {
				t.Fatalf("JSON round trip: %s", code)
			}
			y, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			decoded, code = fixtureDeclaration(y)
			if code != "" || !reflect.DeepEqual(d, decoded) {
				t.Fatalf("YAML round trip: %s", code)
			}
			// Reverse insertion order; mapping order must not change serialized bytes.
			keys := fixtureKeys(d.Resources)
			reordered := map[ResourceKey]ResourceDeclaration{}
			for i := len(keys) - 1; i >= 0; i-- {
				reordered[keys[i]] = d.Resources[keys[i]]
			}
			d.Resources = reordered
			again, _ := json.Marshal(d)
			if !bytes.Equal(j, again) {
				t.Fatal("unstable JSON mapping")
			}
			again, _ = yaml.Marshal(d)
			if !bytes.Equal(y, again) {
				t.Fatal("unstable YAML mapping")
			}
			if name == "minimal.yaml" && len(d.Resources) != 0 {
				t.Fatal("implicit enrollment")
			}
			if name == "webdav.yaml" {
				app := d.Resources["webdav"].Application
				if app.Endpoint.Exposure != ApplicationPrivate || app.Data["library"].Path != "data/library" || len(app.Credentials) != 1 {
					t.Fatal("unexpected WebDAV intent")
				}
				delete(d.Resources, "reading") // knowledge is genuinely optional
				raw, _ := json.Marshal(d)
				if _, code := fixtureDeclaration(raw); code != "" {
					t.Fatal(code)
				}
			}
		})
	}
}

func TestDeclarationV05StrictStreams(t *testing.T) {
	source := fixtureRead(t, "minimal.yaml")
	for name, raw := range map[string][]byte{
		"trailing_document": append(append([]byte{}, source...), []byte("---\n{}\n")...),
		"duplicate_key":     append(append([]byte{}, source...), []byte("resources: {}\n")...),
		"duplicate_json":    []byte(`{"kind":"loom.project","kind":"other"}`),
		"yaml_alias":        []byte("kind: &k loom.project\nschema_version: *k\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, code := fixtureDeclaration(raw); code == "" {
				t.Fatal("accepted ambiguous stream")
			}
		})
	}
}

func TestDeclarationV05LegacyIsolation(t *testing.T) {
	// D1 enables only the two frozen v0.5 sources; legacy cases remain unchanged.
	for _, name := range []string{"minimal.yaml", "webdav.yaml"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ProjectMetadataDir), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, CanonicalRootContractPath), fixtureRead(t, name), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadProject(root)
			if err != nil {
				t.Fatal(err)
			}
			raw := fixtureRead(t, name)
			expected, code := fixtureDeclaration(raw)
			if code != "" || loaded.Declaration == nil || !reflect.DeepEqual(*loaded.Declaration, expected) || !bytes.Equal(loaded.Raw, raw) {
				t.Fatal("v0.5 declaration or exact source was not preserved")
			}
		})
	}
	for _, version := range []string{"v03", "v04"} {
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/repository_contracts/project_" + version + ".yaml")
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ProjectMetadataDir), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, CanonicalRootContractPath), raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadProject(root)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Contract.SchemaVersion != map[string]string{"v03": ProjectSchemaV03, "v04": ProjectSchemaV04}[version] {
				t.Fatal("legacy version changed")
			}
		})
	}
}

func TestDeclarationV05ProtectedIdentity(t *testing.T) {
	previous, _ := fixtureDeclaration(fixtureRead(t, "webdav.yaml"))
	repo := previous.Resources["code"]
	repo.Repository.ID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	previous.Resources["code"] = repo
	cases := []struct {
		name string
		edit func(*ProjectDeclaration)
		want string
	}{
		{"label", func(d *ProjectDeclaration) { d.Project.Name = "Renamed" }, ""},
		{"repository_path", func(d *ProjectDeclaration) { d.Resources["code"].Repository.Path = "repos/renamed" }, ""},
		{"project_id", func(d *ProjectDeclaration) { d.Project.ID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW" }, "declaration.identity_conflict"},
		{"repository_id", func(d *ProjectDeclaration) { d.Resources["code"].Repository.ID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAW" }, "declaration.identity_conflict"},
		{"resource_kind", func(d *ProjectDeclaration) { d.Resources["code"] = ResourceDeclaration{Kind: DeclarationKnowledge} }, "declaration.identity_conflict"},
		{"data_rebinding", func(d *ProjectDeclaration) {
			a := d.Resources["webdav"].Application
			v := a.Data["library"]
			v.BindingRef = "webdav.other"
			a.Data["library"] = v
		}, "declaration.identity_conflict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(previous)
			var next ProjectDeclaration
			if err := json.Unmarshal(raw, &next); err != nil {
				t.Fatal(err)
			}
			c.edit(&next)
			if got := fixtureIdentityTransition(previous, next); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	// A key rename is deliberately not inferred from paths: it is remove/add.
	if fixtureResourceAddress(previous.Project.ID, "code") == fixtureResourceAddress(previous.Project.ID, "renamed") {
		t.Fatal("key rename reused logical address")
	}
}

func TestDeclarationV05PlanIdentity(t *testing.T) {
	var p DeclarationPlan
	if err := fixtureJSON(fixtureRead(t, "plan.json"), &p); err != nil {
		t.Fatal(err)
	}
	if code := fixturePlanShape(p); code != "" {
		t.Fatal(code)
	}
	if got := fixturePlanID(t, p.Basis); got != p.PlanID {
		t.Fatalf("fixed plan fingerprint got %s want %s", got, p.PlanID)
	}
	for _, file := range []struct{ name, ref string }{{"webdav.yaml", CanonicalRootContractPath}, {"webdav-application.fixture.txt", ".loom/applications/webdav.yaml"}, {"backup.yaml", ".loom/policies/backup.yaml"}} {
		found := false
		for _, s := range p.Basis.Sources {
			if s.Ref == file.ref {
				found = true
				if s.Hash != fixtureHash(fixtureRead(t, file.name)) {
					t.Fatalf("unbound source %s", file.name)
				}
			}
		}
		if !found {
			t.Fatalf("missing transitive source %s", file.ref)
		}
	}
	var external struct {
		SchemaVersion string                     `json:"schema_version"`
		Snapshots     map[string]json.RawMessage `json:"snapshots"`
	}
	if err := fixtureJSON(fixtureRead(t, "external-snapshots.json"), &external); err != nil {
		t.Fatal(err)
	}
	for ref, raw := range external.Snapshots {
		found := false
		for _, source := range p.Basis.Sources {
			if source.Ref == ref {
				found = true
				if source.Hash != fixtureHash(fixtureCanonical(t, raw)) {
					t.Fatalf("unbound external dependency %s", ref)
				}
			}
		}
		if !found {
			t.Fatalf("missing external dependency %s", ref)
		}
	}
	original := p.PlanID
	p.GeneratedAt = "2030-01-01T00:00:00Z"
	p.Readiness.Healthy.State = DeclarationSatisfied
	if fixturePlanID(t, p.Basis) != original {
		t.Fatal("display/observation changed identity")
	}
	// Representation-only source ordering and map insertion order are immaterial.
	p.Basis.Sources[0], p.Basis.Sources[1] = p.Basis.Sources[1], p.Basis.Sources[0]
	if fixturePlanID(t, p.Basis) != original {
		t.Fatal("source ordering changed identity")
	}
	cases := []struct {
		name string
		edit func(*DeclarationPlanBasis)
	}{
		{"transitive_content", func(b *DeclarationPlanBasis) { b.Sources[0].Hash = fixtureHash([]byte("changed dependency")) }},
		{"source_revision", func(b *DeclarationPlanBasis) { b.Sources[0].Revision = "2" }},
		{"policy", func(b *DeclarationPlanBasis) { b.Revisions["projectcontracts:.loom/policies/backup.yaml"] = "2" }},
		{"authorization", func(b *DeclarationPlanBasis) { b.Actions[0].Authorization.PolicyRevision = "revoked" }},
		{"actor", func(b *DeclarationPlanBasis) { b.Actions[0].Authorization.ActorID = "actor_01ARZ3NDEKTSV4RRFFQ69G5FAW" }},
		{"target", func(b *DeclarationPlanBasis) { b.Target.LocationRevision = "2" }},
		{"owner", func(b *DeclarationPlanBasis) { b.Target.OwnerNodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAW" }},
		{"effects", func(b *DeclarationPlanBasis) { b.Effects = append(b.Effects, DeclarationProjections) }},
		{"action_input", func(b *DeclarationPlanBasis) { b.Actions[0].InputHash = fixtureHash([]byte("different intent")) }},
		{"existing_binding", func(b *DeclarationPlanBasis) {
			v := b.Bindings["code"]
			v.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAW"
			b.Bindings["code"] = v
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(p.Basis)
			var b DeclarationPlanBasis
			json.Unmarshal(raw, &b)
			c.edit(&b)
			if fixturePlanID(t, b) == original {
				t.Fatal("effectful change did not invalidate plan")
			}
		})
	}
}

func TestDeclarationV05OperationShapes(t *testing.T) {
	for _, c := range []struct {
		name   string
		target any
	}{
		{"plan.json", &DeclarationPlan{}}, {"plan-request.json", &DeclarationPlanRequest{}},
		{"apply-request.json", &DeclarationApplyRequest{}}, {"result.json", &DeclarationResult{}}, {"status.json", &DeclarationStatus{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := fixtureRead(t, c.name)
			if err := fixtureJSON(raw, c.target); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(c.target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(fixtureCanonical(t, raw), fixtureCanonical(t, encoded)) {
				t.Fatal("wire round trip lost or changed fields")
			}
		})
	}
	var p DeclarationPlan
	fixtureJSON(fixtureRead(t, "plan.json"), &p)
	p.Basis.Actions[0].Kind = "run_shell"
	if fixturePlanShape(p) != "declaration.unsupported" {
		t.Fatal("unknown action accepted")
	}
	fixtureJSON(fixtureRead(t, "plan.json"), &p)
	p.Basis.Actions[0].Authorization.Level = 6
	if fixturePlanShape(p) != "declaration.invalid" {
		t.Fatal("invalid authority accepted")
	}
	fixtureJSON(fixtureRead(t, "plan.json"), &p)
	p.Basis.Sources = nil
	if fixturePlanShape(p) != "declaration.invalid" {
		t.Fatal("missing source closure accepted")
	}
	fixtureJSON(fixtureRead(t, "plan.json"), &p)
	p.Basis.Actions[0].Owner = DeclarationOwnerApplication
	if fixturePlanShape(p) != "declaration.unsupported" {
		t.Fatal("wrong owner accepted")
	}
	var result DeclarationResult
	fixtureJSON(fixtureRead(t, "result.json"), &result)
	if result.Readiness.Applied.State != DeclarationPending || result.Readiness.Processing.State != DeclarationPending || result.Readiness.Healthy.State != DeclarationUnknown || result.Readiness.Verified.State != DeclarationPending {
		t.Fatal("readiness dimensions collapsed")
	}
	var status DeclarationStatus
	if err := fixtureJSON(fixtureRead(t, "status.json"), &status); err != nil {
		t.Fatal(err)
	}
	if status.Resources["webdav"].Readiness.Applied.State != DeclarationSatisfied || status.Readiness.Applied.State != DeclarationPending {
		t.Fatal("per-resource and aggregate readiness collapsed")
	}
	if result.State != DeclarationOperationPartial || len(result.Errors[0].CompletedEffects) != 4 || result.Errors[0].CauseCode != "owner.delivery_unavailable" {
		t.Fatal("partial effect/cause lost")
	}
	if err := fixtureJSON([]byte(`{"schema_version":"project.declaration_request.v0.5","plan_id":"x","idempotency_key":"x","grant":true}`), &DeclarationApplyRequest{}); err == nil {
		t.Fatal("request accepted authority grant")
	}
}

func fixtureRead(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(declarationFixtureDir + name)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func fixtureJSON(raw []byte, v any) error {
	// encoding/json otherwise accepts duplicate keys, including escaped aliases.
	stream := json.NewDecoder(bytes.NewReader(raw))
	stream.UseNumber()
	if err := fixtureUniqueJSON(stream); err != nil {
		return err
	}
	if _, err := stream.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON: %v", err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON: %v", err)
	}
	return fixtureRequestShape(raw, v)
}

func fixtureUniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate/invalid JSON key: %v", key)
			}
			seen[name] = true
		}
		if err := fixtureUniqueJSON(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

// Presence checks distinguish omitted plan effects (default reconcile) from
// explicit null/empty effects. Apply/resume always require the reviewed effects.
func fixtureRequestShape(raw []byte, value any) error {
	var schema, projectRef, nodeRef string
	var effects []DeclarationEffect
	var apply *DeclarationApplyRequest
	switch request := value.(type) {
	case *DeclarationPlanRequest:
		schema, projectRef, nodeRef, effects = request.SchemaVersion, request.ProjectRef, request.NodeRef, request.Effects
	case *DeclarationApplyRequest:
		apply = request
		schema, projectRef, nodeRef, effects = request.SchemaVersion, request.ProjectRef, request.NodeRef, request.Effects
	default:
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if schema != DeclarationRequestSchemaV05 || !fixtureNonemptyText(projectRef) {
		return fmt.Errorf("missing/invalid request envelope")
	}
	if _, present := fields["node_ref"]; present && !fixtureNonemptyText(nodeRef) {
		return fmt.Errorf("empty node selector")
	}
	if _, present := fields["effects"]; present || apply != nil {
		if code := fixtureEffects(effects); code != "" {
			return fmt.Errorf("%s", code)
		}
	}
	if apply != nil {
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(apply.PlanID) || !fixtureNonemptyText(apply.IdempotencyKey) {
			return fmt.Errorf("missing/invalid reviewed identity")
		}
		if _, present := fields["operation_id"]; present && ids.Validate(ids.JobPrefix, apply.OperationID) != nil {
			return fmt.Errorf("invalid resume identity")
		}
		if _, present := fields["approval_refs"]; present {
			if len(apply.ApprovalRefs) == 0 {
				return fmt.Errorf("empty approvals")
			}
			seen := map[string]bool{}
			for _, ref := range apply.ApprovalRefs {
				if !fixtureNonemptyText(ref) || seen[ref] {
					return fmt.Errorf("empty/duplicate approval")
				}
				seen[ref] = true
			}
		}
	}
	return nil
}

func fixtureNonemptyText(value string) bool {
	return strings.TrimSpace(value) != "" && strings.IndexFunc(value, unicode.IsControl) < 0
}

func fixtureEffects(effects []DeclarationEffect) string {
	if len(effects) == 0 {
		return "declaration.invalid"
	}
	seen := map[DeclarationEffect]bool{}
	for _, effect := range effects {
		if effect != DeclarationReconcile && effect != DeclarationProjections {
			return "declaration.unsupported"
		}
		if seen[effect] {
			return "declaration.invalid"
		}
		seen[effect] = true
	}
	return ""
}
func fixtureKeys[T any](m map[ResourceKey]T) []ResourceKey {
	keys := make([]ResourceKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func fixtureDeclaration(raw []byte) (ProjectDeclaration, string) {
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
	if len(tree.Content) != 1 || !fixtureNodeShape(tree.Content[0], reflect.TypeOf(d)) {
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
	if d.Project.Slug == "" || d.Project.Name == "" || d.Project.OwnerNode == "" || d.Resources == nil {
		return d, "declaration.required"
	}
	if d.Project.Status != "" && d.Project.Status != ProjectStatusDraft && d.Project.Status != ProjectStatusActive && d.Project.Status != ProjectStatusPaused && d.Project.Status != ProjectStatusArchived {
		return d, "declaration.kind"
	}
	paths := map[DeclarationResourceKind][]string{}
	repoIDs := map[string]bool{}
	primary := 0
	for _, key := range fixtureKeys(d.Resources) {
		r := d.Resources[key]
		if !fixtureKey(string(key)) {
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
			if !fixturePath(r.Protection.PolicyRef, true) {
				return d, "declaration.reference"
			}
			p = r.Protection.Path
		case DeclarationApplication:
			if r.Application == nil {
				return d, "declaration.union"
			}
			a := r.Application
			if !fixtureRef(d, a.Repository, DeclarationRepository) {
				return d, "declaration.reference"
			}
			if !fixturePath(a.Manifest, true) {
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
			if e := a.Endpoint; e != nil {
				switch e.Exposure {
				case ApplicationLoopback:
				case ApplicationPrivate, ApplicationPublicHTTPS:
					if !validCredentialRef(e.EndpointRef) {
						return d, "declaration.reference"
					}
				default:
					return d, "declaration.kind"
				}
				if e.EndpointRef != "" && !validCredentialRef(e.EndpointRef) {
					return d, "declaration.reference"
				}
			}
			for _, dataKey := range fixtureKeys(a.Data) {
				v := a.Data[dataKey]
				if !fixtureKey(string(dataKey)) {
					return d, "declaration.key"
				}
				if (v.Path == "") == (v.BindingRef == "") {
					return d, "declaration.conflict"
				}
				if v.Path != "" {
					if !fixturePath(v.Path, false) {
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
				if v.Protection != "" && !fixtureRef(d, v.Protection, DeclarationProtection) {
					return d, "declaration.reference"
				}
			}
		}
		if protection != "" && !fixtureRef(d, protection, DeclarationProtection) {
			return d, "declaration.reference"
		}
		if r.Kind == DeclarationRepository || r.Kind == DeclarationKnowledge || p != "" {
			if !fixturePath(p, false) {
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
				if fixtureOverlap(p, q) {
					return d, "declaration.overlap"
				}
			}
		}
		if kind == DeclarationApplication {
			for _, p := range ps {
				for _, other := range []DeclarationResourceKind{DeclarationRepository, DeclarationKnowledge} {
					for _, q := range paths[other] {
						if fixtureOverlap(p, q) {
							return d, "declaration.overlap"
						}
					}
				}
			}
		}
	}
	return d, ""
}

var fixtureLocalKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// yaml.v3 deliberately permits scalar coercions. This fixture-only shape
// oracle rejects them so round trips cannot silently change source meaning.
func fixtureNodeShape(n *yaml.Node, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.String:
		return n.Kind == yaml.ScalarNode && n.Tag == "!!str"
	case reflect.Uint64:
		return n.Kind == yaml.ScalarNode && n.Tag == "!!int" && regexp.MustCompile(`^[0-9]+$`).MatchString(n.Value)
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return false
		}
		for _, child := range n.Content {
			if !fixtureNodeShape(child, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return false
		}
		for i := 0; i < len(n.Content); i += 2 {
			if !fixtureNodeShape(n.Content[i], typ.Key()) || !fixtureNodeShape(n.Content[i+1], typ.Elem()) {
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
			if !ok || n.Content[i].Tag != "!!str" || !fixtureNodeShape(n.Content[i+1], field) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func fixtureKey(s string) bool { return fixtureLocalKey.MatchString(s) }
func fixturePath(s string, metadata bool) bool {
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
func fixtureOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func fixtureRef(d ProjectDeclaration, k ResourceKey, kind DeclarationResourceKind) bool {
	r, ok := d.Resources[k]
	return ok && r.Kind == kind
}
func fixtureResourceAddress(projectID string, key ResourceKey) string {
	return projectID + "/" + string(key)
}
func fixtureIdentityTransition(old, next ProjectDeclaration) string {
	if old.Project.ID != next.Project.ID {
		return "declaration.identity_conflict"
	}
	for k, a := range old.Resources {
		b, ok := next.Resources[k]
		if !ok {
			continue
		}
		if a.Kind != b.Kind {
			return "declaration.identity_conflict"
		}
		if a.Repository != nil && b.Repository != nil && a.Repository.ID != "" && b.Repository.ID != "" && a.Repository.ID != b.Repository.ID {
			return "declaration.identity_conflict"
		}
		if a.Application != nil && b.Application != nil {
			for key, x := range a.Application.Data {
				if y, ok := b.Application.Data[key]; ok && (x.Path != y.Path || x.BindingRef != y.BindingRef) {
					return "declaration.identity_conflict"
				}
			}
		}
	}
	return ""
}
func fixtureCanonical(t *testing.T, raw []byte) []byte {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if e := d.Decode(&v); e != nil {
		t.Fatal(e)
	}
	out, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func fixtureHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func fixturePlanID(t *testing.T, b DeclarationPlanBasis) string {
	t.Helper()
	b.Sources = append([]DeclarationSource{}, b.Sources...)
	sort.Slice(b.Sources, func(i, j int) bool { return b.Sources[i].Ref < b.Sources[j].Ref })
	b.Effects = append([]DeclarationEffect{}, b.Effects...)
	sort.Slice(b.Effects, func(i, j int) bool { return b.Effects[i] < b.Effects[j] })
	raw, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	return fixtureHash(fixtureCanonical(t, raw))
}
func fixturePlanShape(p DeclarationPlan) string {
	b := p.Basis
	if code := fixtureEffects(b.Effects); code != "" {
		return code
	}
	if p.SchemaVersion != DeclarationPlanSchemaV05 || b.SchemaVersion != DeclarationPlanSchemaV05 || len(b.Sources) == 0 || len(b.Revisions) == 0 || len(b.Effects) == 0 || b.Actions == nil || b.Bindings == nil || ids.Validate(ids.ProjectPrefix, b.Target.ProjectID) != nil || ids.Validate(ids.NodePrefix, b.Target.OwnerNodeID) != nil || b.Target.LocationRevision == "" || !path.IsAbs(b.Target.ProjectRoot) {
		return "declaration.invalid"
	}
	seen := map[string]bool{}
	root := false
	for _, s := range b.Sources {
		if seen[s.Ref] || s.Ref == "" || s.Revision == "" || s.SchemaVersion == "" || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(s.Hash) {
			return "declaration.invalid"
		}
		seen[s.Ref] = true
		root = root || s.Ref == CanonicalRootContractPath
	}
	if !root {
		return "declaration.invalid"
	}
	actions := map[string]bool{}
	for _, a := range b.Actions {
		owners := map[DeclarationActionKind][]DeclarationOwner{DeclarationRegisterProject: {DeclarationOwnerProjects}, DeclarationRegisterRepository: {DeclarationOwnerProjects}, DeclarationEnrollKnowledge: {DeclarationOwnerKnowledge}, DeclarationReconcileProtection: {DeclarationOwnerProtection}, DeclarationApplyApplication: {DeclarationOwnerApplication}, DeclarationRefreshProjection: {DeclarationOwnerNotes, DeclarationOwnerProvenance}, DeclarationRetireResource: {DeclarationOwnerProjects, DeclarationOwnerKnowledge, DeclarationOwnerProtection, DeclarationOwnerApplication}}
		ok := false
		for _, owner := range owners[a.Kind] {
			ok = ok || a.Owner == owner
		}
		if !ok {
			return "declaration.unsupported"
		}
		if a.ID == "" || actions[a.ID] || a.TargetRef == "" || a.InputHash == "" || a.DependsOn == nil || a.Authorization.Level < 1 || a.Authorization.Level > 5 || ids.Validate(ids.ActorPrefix, a.Authorization.ActorID) != nil || a.Authorization.NodeID != b.Target.OwnerNodeID || a.Authorization.PolicyRevision == "" {
			return "declaration.invalid"
		}
		for _, dep := range a.DependsOn {
			if !actions[dep] {
				return "declaration.invalid"
			}
		}
		actions[a.ID] = true
	}
	return ""
}

// Unchanged integrator assertions, renamed so the original overlay can run too.
func TestDeclarationV05ReviewRejectsUnknownEffect(t *testing.T) {
	var p DeclarationPlan
	if err := fixtureJSON(fixtureRead(t, "plan.json"), &p); err != nil {
		t.Fatal(err)
	}
	p.Basis.Effects = []DeclarationEffect{"run_shell"}
	if code := fixturePlanShape(p); code == "" {
		t.Fatal("unknown effect accepted by contract oracle")
	}
}

func TestDeclarationV05ReviewRejectsControlPath(t *testing.T) {
	d, code := fixtureDeclaration(fixtureRead(t, "webdav.yaml"))
	if code != "" {
		t.Fatal(code)
	}
	d.Resources["reading"].Knowledge.Path = "material/\u0001hidden"
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := fixtureDeclaration(raw); code == "" {
		t.Fatal("control character path accepted by contract oracle")
	}
}

func TestDeclarationV05ReviewRejectsDuplicateRequestKey(t *testing.T) {
	raw := fixtureRead(t, "plan-request.json")
	raw = bytes.Replace(raw, []byte(`"project_ref":`), []byte(`"project_ref":"another-project", "project_ref":`), 1)
	if err := fixtureJSON(raw, &DeclarationPlanRequest{}); err == nil {
		t.Fatal("duplicate request selector accepted by contract oracle")
	}
}

func TestDeclarationV05ReviewExistingFilePolicyReference(t *testing.T) {
	d, code := fixtureDeclaration(fixtureRead(t, "webdav.yaml"))
	if code != "" {
		t.Fatal(code)
	}
	d.Resources["retained"].Protection.PolicyRef = ".loom/policies/backup.yaml"
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := fixtureDeclaration(raw); code != "" {
		t.Fatalf("existing project-relative protection source rejected: %s", code)
	}
}

func TestDeclarationV05InvalidRequestEnvelopes(t *testing.T) {
	var corpus struct {
		SchemaVersion string `json:"schema_version"`
		Cases         []struct {
			Name     string          `json:"name"`
			Request  string          `json:"request"`
			Document json.RawMessage `json:"document"`
		} `json:"cases"`
	}
	if err := fixtureJSON(fixtureRead(t, "invalid-requests.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != "declaration.fixture.v0.5" || len(corpus.Cases) != 31 {
		t.Fatal("unexpected request corpus")
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var target any
			switch c.Request {
			case "plan":
				target = &DeclarationPlanRequest{}
			case "apply":
				target = &DeclarationApplyRequest{}
			default:
				t.Fatal("unknown fixture request")
			}
			if err := fixtureJSON(c.Document, target); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fixtureRead(t, "plan-request.json"), &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "effects")
	encoded, _ := json.Marshal(raw)
	if err := fixtureJSON(encoded, &DeclarationPlanRequest{}); err != nil {
		t.Fatalf("omitted plan effects must retain default: %v", err)
	}
	for _, bad := range []string{
		`{"project_ref":"a","project\u005fref":"b"}`,
		`{"basis":{"target":{"project_id":"a","project_id":"b"}}}`,
	} {
		if err := fixtureJSON([]byte(bad), &map[string]any{}); err == nil {
			t.Fatal("escaped/nested duplicate key accepted")
		}
	}
}

func TestDeclarationV05AllControlPathsAndBadPlanEffects(t *testing.T) {
	for c := rune(0); c <= 0x9f; c++ {
		if unicode.IsControl(c) {
			p := "material/" + string(c) + "hidden"
			if fixturePath(p, false) || fixturePath(p, true) {
				t.Fatalf("accepted control U+%04X", c)
			}
		}
	}
	for _, effects := range [][]DeclarationEffect{nil, {}, {"run_shell"}, {DeclarationReconcile, DeclarationReconcile}} {
		var p DeclarationPlan
		if err := fixtureJSON(fixtureRead(t, "plan.json"), &p); err != nil {
			t.Fatal(err)
		}
		p.Basis.Effects = effects
		if fixturePlanShape(p) == "" {
			t.Fatalf("bad effects accepted: %v", effects)
		}
	}
}

func TestDeclarationV05FilePolicySelection(t *testing.T) {
	d, code := fixtureDeclaration(fixtureRead(t, "webdav.yaml"))
	if code != "" {
		t.Fatal(code)
	}
	// Exercise the existing explicit project-relative resolver and v0.3 loader,
	// never a v0.5 adapter. Only tiny files in the normal test temporary root.
	root := t.TempDir()
	policyRef := d.Resources["retained"].Protection.PolicyRef
	policyPath := filepath.Join(root, filepath.FromSlash(policyRef))
	if err := os.MkdirAll(filepath.Dir(policyPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, fixtureRead(t, "backup.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveSingletonContract(LoadedProject{RootPath: root, Layout: ProjectLayoutCanonical}, ProjectContractBackup, policyRef)
	if err != nil || !resolved.Present || resolved.RelativePath != policyRef {
		t.Fatalf("existing policy file resolution: %#v %v", resolved, err)
	}
	policy, raw, err := LoadBackupPolicyContract(resolved.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, fixtureRead(t, "backup.yaml")) || policy.Kind != BackupPolicyKind || policy.SchemaVersion != BackupPolicySchemaV03 {
		t.Fatal("policy is not the existing supported source shape")
	}
	selected, enabled, code := fixturePolicyRootSelection(d, "retained", policy)
	if code != "" || !enabled || len(selected) != 1 || selected[0].Key != "webdav_library" || selected[0].Path != "data/library" {
		t.Fatalf("selection: %v %v %s", selected, enabled, code)
	}
	// A file is a source of policy settings, not authority to enroll all its roots.
	extra := policy
	extra.Backup.Roots = append(append([]BackupRootPolicySpec{}, policy.Backup.Roots...), BackupRootPolicySpec{Key: "undeclared", Path: "unrelated", Mode: "incremental_raw"})
	selected, _, code = fixturePolicyRootSelection(d, "retained", extra)
	if code != "" || len(selected) != 1 || selected[0].Path != "data/library" {
		t.Fatal("unselected policy root enrolled")
	}
	// Naming the same exact target directly and by attachment is one selection.
	d.Resources["retained"].Protection.Path = "data/library"
	selected, _, code = fixturePolicyRootSelection(d, "retained", policy)
	if code != "" || len(selected) != 1 {
		t.Fatal("direct and attached target double-selected")
	}
	d.Resources["retained"].Protection.Path = ""
	a := d.Resources["webdav"].Application
	data := a.Data["library"]
	delete(a.Data, "library")
	selected, _, code = fixturePolicyRootSelection(d, "retained", extra)
	if code != "" || len(selected) != 0 {
		t.Fatal("unattached file enrolled roots")
	}
	a.Data["library"] = data
	for _, test := range []struct {
		name  string
		roots []BackupRootPolicySpec
	}{
		{"missing", nil},
		{"ancestor", []BackupRootPolicySpec{{Key: "wide", Path: "data"}}},
		{"conflict", []BackupRootPolicySpec{{Key: "first", Path: "data/library"}, {Key: "second", Path: "data/library", Mode: "none"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := policy
			bad.Backup.Roots = test.roots
			if _, _, code := fixturePolicyRootSelection(d, "retained", bad); code == "" {
				t.Fatal("ambiguous or mismatched attachment accepted")
			}
		})
	}
	disabled := policy
	disabled.Backup.Enabled = boolPtr(false)
	selected, enabled, code = fixturePolicyRootSelection(d, "retained", disabled)
	if code != "" || enabled || len(selected) != 1 || selected[0].Key != "webdav_library" {
		t.Fatal("disabled intent or retained root identity lost")
	}
	unresolved := data
	unresolved.Path = ""
	unresolved.BindingRef = "webdav.library"
	a.Data["library"] = unresolved
	if _, _, code := fixturePolicyRootSelection(d, "retained", policy); code != "declaration.reference_missing" {
		t.Fatal("pretended future allocation was resolved")
	}
	// Exact policy bytes and schema, including disabled state, bind plan identity.
	var p DeclarationPlan
	if err := fixtureJSON(fixtureRead(t, "plan.json"), &p); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, s := range p.Basis.Sources {
		if s.Ref == d.Resources["retained"].Protection.PolicyRef {
			found = true
			if s.Hash != fixtureHash(raw) || s.SchemaVersion != BackupPolicySchemaV03 {
				t.Fatal("policy source not bound")
			}
			p.Basis.Sources[i].Hash = fixtureHash(bytes.Replace(raw, []byte("enabled: true"), []byte("enabled: false"), 1))
		}
	}
	if !found || fixturePlanID(t, p.Basis) == p.PlanID {
		t.Fatal("policy bytes did not invalidate plan")
	}
}

// D0 source-only selection oracle. It consumes supplied policy DTOs and explicit
// paths; it performs no file discovery, normalization, compilation or dispatch.
// D1/D2 must implement the actual adapter and custody/semantic checks.
func fixturePolicyRootSelection(d ProjectDeclaration, key ResourceKey, policy ProjectBackupPolicyContract) ([]BackupRootPolicySpec, bool, string) {
	selected := []BackupRootPolicySpec{}
	if policy.Kind != BackupPolicyKind || policy.SchemaVersion != BackupPolicySchemaV03 {
		return selected, false, "declaration.unsupported"
	}
	r, ok := d.Resources[key]
	if !ok || r.Protection == nil {
		return selected, false, "declaration.reference_missing"
	}
	targets := map[string]bool{}
	if r.Protection.Path != "" {
		targets[r.Protection.Path] = true
	}
	for _, resource := range d.Resources {
		if v := resource.Repository; v != nil && v.Protection == key {
			targets[v.Path] = true
		}
		if v := resource.Knowledge; v != nil && v.Protection == key {
			targets[v.Path] = true
		}
		if v := resource.Application; v != nil {
			for _, data := range v.Data {
				if data.Protection == key {
					if data.Path == "" {
						return selected, false, "declaration.reference_missing"
					}
					targets[data.Path] = true
				}
			}
		}
	}
	names := make([]string, 0, len(targets))
	for p := range targets {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, target := range names {
		matches := []BackupRootPolicySpec{}
		for _, root := range policy.Backup.Roots {
			if root.Path == target {
				matches = append(matches, root)
			}
		}
		if len(matches) != 1 {
			return selected, false, "declaration.conflict"
		}
		selected = append(selected, matches[0])
	}
	return selected, policy.Backup.Enabled == nil || *policy.Backup.Enabled, ""
}
