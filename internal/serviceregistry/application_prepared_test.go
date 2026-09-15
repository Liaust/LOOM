package serviceregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/requestctx"
)

type applicationProviderFixture struct {
	provider  *capabilities.Provider
	creates   int
	lostReply bool
}

func (f *applicationProviderFixture) InspectProvider(context.Context, string) (capabilities.ProviderInspection, error) {
	if f.provider == nil {
		return capabilities.ProviderInspection{}, sql.ErrNoRows
	}
	return capabilities.ProviderInspection{Provider: *f.provider}, nil
}
func (f *applicationProviderFixture) RegisterProvider(_ context.Context, _ requestctx.Context, in capabilities.RegisterProviderInput) (capabilities.Provider, error) {
	f.creates++
	p := capabilities.Provider{ProviderID: "provider-fixture", ProviderKey: in.ProviderKey, CompactAddress: in.CompactAddress, ProviderType: in.ProviderType, NodeID: in.NodeRef, ScopeID: in.ScopeRef, Status: in.Status, RuntimeProfileJSON: in.RuntimeProfileJSON, Metadata: in.Metadata}
	f.provider = &p
	if f.lostReply {
		return capabilities.Provider{}, errors.New("lost registration reply")
	}
	return p, nil
}

func TestManagedApplicationServiceRegistrationReplayAndConflicts(t *testing.T) {
	owner := ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "server"}
	f := &applicationProviderFixture{lostReply: true}
	first, err := RegisterApplicationService(t.Context(), requestctx.Context{}, f, owner, "main", "scope-project")
	if err != nil {
		t.Fatal(err)
	}
	again, err := RegisterApplicationService(t.Context(), requestctx.Context{}, f, owner, "main", "scope-project")
	if err != nil || first != again || f.creates != 1 {
		t.Fatalf("replay: %+v %v creates=%d", again, err, f.creates)
	}
	saved := *f.provider
	for name, edit := range map[string]func(*capabilities.Provider){
		"scope":    func(p *capabilities.Provider) { p.ScopeID = "other" },
		"node":     func(p *capabilities.Provider) { p.NodeID = "other" },
		"unit":     func(p *capabilities.Provider) { p.RuntimeProfileJSON = json.RawMessage(`{}`) },
		"owner":    func(p *capabilities.Provider) { p.Metadata = json.RawMessage(`{}`) },
		"disabled": func(p *capabilities.Provider) { p.Status = capabilities.ProviderStatusDisabled },
	} {
		t.Run(name, func(t *testing.T) {
			v := saved
			edit(&v)
			f.provider = &v
			if _, err := RegisterApplicationService(t.Context(), requestctx.Context{}, f, owner, "main", "scope-project"); err == nil || f.creates != 1 {
				t.Fatal("adopted/overwrote conflicting service")
			}
		})
	}
}

type preparedVerifier struct {
	ApplicationSystem
	calls int
	err   error
}

func (v *preparedVerifier) VerifyArtifact(ctx context.Context, d ApplicationArtifactDescriptor) error {
	v.calls++
	if v.err != nil {
		return v.err
	}
	return v.ApplicationSystem.VerifyArtifact(ctx, d)
}

func TestPreparedPublicationVerifierReplayAndAdmission(t *testing.T) {
	r, policy, request := applicationProvisionFixture(t)
	plan, err := r.PlanProvision(t.Context(), policy.InstallerUID, request)
	if err != nil {
		t.Fatal(err)
	}
	d := applicationTestDescriptor()
	target, err := ApplicationServiceTarget(request.Owner, "main", "provider-fixture")
	if err != nil {
		t.Fatal(err)
	}
	q := ApplicationPreparedPublication{GrantRevision: plan.GrantRevision, Publication: ApplicationPublication{Owner: request.Owner, Revision: ApplicationPreparedRevision(plan, d), LocationRevision: request.LocationRevision, PolicyRevision: plan.PolicyRevision, Descriptor: d, ArchiveTarget: &target}}
	if err := r.PublishPrepared(t.Context(), policy.InstallerUID, q); err == nil {
		t.Fatal("published without grant")
	}
	if _, err := r.Provision(t.Context(), policy.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	verifier := &preparedVerifier{ApplicationSystem: &applicationFixtureHost{root: filepath.Dir(r.PolicyPath)}, err: errors.New("invalid realized closure")}
	r.System = verifier
	if err := r.PublishPrepared(t.Context(), policy.InstallerUID, q); err == nil || verifier.calls != 1 {
		t.Fatal("publication bypassed verifier")
	}
	var published ApplicationPublication
	if r.Store.read("publication-"+request.Owner.Instance(), &published) == nil {
		t.Fatal("failed verification published")
	}
	verifier.err = nil
	r.FailureHook = func(stage string) error {
		if stage == "publication:committed" {
			return errors.New("lost reply")
		}
		return nil
	}
	if err := r.PublishPrepared(t.Context(), policy.InstallerUID, q); err == nil {
		t.Fatal("lost reply not injected")
	}
	r.FailureHook = nil
	before := applicationPrerequisitesTree(t, r.Store.Root)
	if err := r.PublishPrepared(t.Context(), policy.InstallerUID, q); err != nil {
		t.Fatal(err)
	}
	q.Publication.ExpectedRevision = q.Publication.Revision
	if err := r.PublishPrepared(t.Context(), policy.InstallerUID, q); err != nil {
		t.Fatal("fresh plan failed replay", err)
	}
	if verifier.calls != 2 || !reflect.DeepEqual(before, applicationPrerequisitesTree(t, r.Store.Root)) {
		t.Fatal("replay rewrote publication or reverified")
	}
	if err := r.Publish(t.Context(), policy.InstallerUID, q.Publication); err == nil {
		t.Fatal("installer acquired arbitrary publisher authority")
	}
	for name, edit := range map[string]func(*ApplicationPreparedPublication){
		"grant": func(q *ApplicationPreparedPublication) { q.GrantRevision = "other" },
		"target": func(q *ApplicationPreparedPublication) {
			v := *q.Publication.ArchiveTarget
			v.Unit = "unrelated.service"
			q.Publication.ArchiveTarget = &v
		},
		"repository": func(q *ApplicationPreparedPublication) {
			q.Publication.Descriptor.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAX"
			q.Publication.Revision = ApplicationPreparedRevision(plan, q.Publication.Descriptor)
		},
		"revision": func(q *ApplicationPreparedPublication) { q.Publication.Revision = "arbitrary" },
	} {
		t.Run(name, func(t *testing.T) {
			v := q
			edit(&v)
			if err := r.PublishPrepared(t.Context(), policy.InstallerUID, v); err == nil {
				t.Fatal("invalid prepared publication accepted")
			}
		})
	}
	for _, peer := range []uint32{policy.PublisherUID, policy.InstallerUID + 1} {
		if err := r.PublishPrepared(t.Context(), peer, q); err == nil {
			t.Fatal("wrong peer accepted")
		}
	}
	raw, _ := json.Marshal(ApplicationHelperEnvelope{Prepared: &q})
	if _, err := decodeApplicationPrerequisiteEnvelope(raw); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(ApplicationHelperEnvelope{Prepared: &q, Provision: &plan})
	if _, err := decodeApplicationPrerequisiteEnvelope(raw); err == nil {
		t.Fatal("mixed envelope accepted")
	}
	policy.Provisioning = nil
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	if err := r.PublishPrepared(t.Context(), policy.InstallerUID, q); err == nil {
		t.Fatal("disabled policy replay accepted")
	}
}

type managedArchiveReader struct {
	facts ApplicationPrerequisiteSnapshot
}

func (r managedArchiveReader) QueryPrerequisites(context.Context, ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error) {
	return r.facts, nil
}
func TestManagedApplicationArchiveIdentity(t *testing.T) {
	owner := ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "server"}
	registry := &applicationProviderFixture{}
	target, err := RegisterApplicationService(t.Context(), requestctx.Context{}, registry, owner, "main", "scope-project")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	facts := ApplicationPrerequisiteSnapshot{SchemaVersion: ApplicationPrerequisiteSchema, Owner: owner, Publication: ApplicationPrerequisitePublication{Present: true, GrantMatches: &yes, ArchiveTarget: &target}}
	got, err := ManagedApplicationArchiveTarget(t.Context(), managedArchiveReader{facts}, *registry.provider)
	if err != nil || got != target {
		t.Fatal(got, err)
	}
	changed := target
	changed.ProviderID = "other-provider"
	facts.Publication.ArchiveTarget = &changed
	if _, err := ManagedApplicationArchiveTarget(t.Context(), managedArchiveReader{facts}, *registry.provider); err == nil {
		t.Fatal("substituted archive identity accepted")
	}
	facts.Publication.ArchiveTarget = &target
	facts.GrantMissing = true
	if _, err := ManagedApplicationArchiveTarget(t.Context(), managedArchiveReader{facts}, *registry.provider); err == nil {
		t.Fatal("missing grant accepted")
	}
}
