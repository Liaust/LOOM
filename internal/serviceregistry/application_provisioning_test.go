package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
)

func applicationProvisionFixture(t *testing.T) (ApplicationRuntime, ApplicationHostPolicy, ApplicationProvisionRequest) {
	t.Helper()
	r, query, policy := applicationPrerequisitesFixture(t)
	policy.Grants = []ApplicationGrant{}
	pool := filepath.Join(filepath.Dir(r.PolicyPath), "allocations")
	if err := os.Mkdir(pool, 0711); err != nil {
		t.Fatal(err)
	}
	policy.Provisioning = &ApplicationProvisioningPolicy{DataRoot: pool, MaxMemoryBytes: 128 << 20, MaxPlannedBytes: 1 << 30}
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	q := ApplicationProvisionRequest{SchemaVersion: ApplicationProvisioningSchema, Owner: query.Owner, RepositoryID: applicationTestDescriptor().RepositoryID, LocationRevision: "location-a", Data: map[string]string{"files": "fixture.files"}}
	return r, policy, q
}

func TestApplicationProvisionPlanReadOnlyAndReplay(t *testing.T) {
	r, policy, q := applicationProvisionFixture(t)
	ctx := context.Background()
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	plan, err := r.PlanProvision(ctx, policy.InstallerUID, q)
	if err != nil || plan.ExpectedRevision != "" || plan.GrantRevision == "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("planning wrote files")
	}
	raw, _ := json.Marshal(plan)
	if strings.Contains(string(raw), policy.Provisioning.DataRoot) {
		t.Fatal("plan exposed allocation root")
	}
	for _, envelope := range []ApplicationHelperEnvelope{{ProvisionPlan: &q}, {Provision: &plan}} {
		encoded, _ := json.Marshal(envelope)
		if _, err := decodeApplicationPrerequisiteEnvelope(encoded); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := r.Provision(ctx, policy.InstallerUID, plan); err != nil || !reflect.DeepEqual(got, plan) {
		t.Fatalf("provision=%+v err=%v", got, err)
	}
	stored, err := r.readManagedGrant(q.Owner)
	if err != nil || stored.Revision != plan.GrantRevision || stored.Grant.Data["files"].Path != filepath.Join(policy.Provisioning.DataRoot, q.Owner.Instance()+"-files") {
		t.Fatalf("grant=%+v err=%v", stored, err)
	}
	if _, err := os.Lstat(stored.Grant.Data["files"].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("grant publication allocated data before installer admission")
	}
	before = applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	if _, err := r.Provision(ctx, policy.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("replay rewrote state")
	}
	facts, err := r.QueryPrerequisites(ctx, policy.InstallerUID, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: q.Owner})
	if err != nil || facts.GrantMissing || facts.Publication.Present || facts.Installation.Present || facts.RepositoryID != q.RepositoryID || facts.Data["files"].BindingRef != "fixture.files" {
		t.Fatalf("prerequisites=%+v err=%v", facts, err)
	}
	encoded, _ := json.Marshal(facts)
	if _, err := decodeApplicationPrerequisiteSnapshot(encoded, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: q.Owner}); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationPublicEndpointProvisioning(t *testing.T) {
	r, p, q := applicationProvisionFixture(t)
	p.Provisioning.PublicEndpoints = &ApplicationPublicEndpointPolicy{Suffix: "apps.example.com", IngressIPv4: "192.0.2.1"}
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	q.Endpoint = &ApplicationEndpointRequest{Hostname: "files.apps.example.com", BackendPort: 8088}
	plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Provision(t.Context(), p.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	g, err := r.resolveGrant(p, q.Owner)
	if err != nil || g.Edge == nil || g.Edge.Hostname != q.Endpoint.Hostname || g.Edge.DNSReady {
		t.Fatal("derived grant/DNS truth", g, err)
	}
	other := q
	other.Owner.Resource = "other"
	if _, err := r.PlanProvision(t.Context(), p.InstallerUID, other); err == nil || err.Error() != "application.edge.hostname_in_use" {
		t.Fatal("collision accepted", err)
	}
	// Both contenders may plan before either commits; the inventory lock owns CAS.
	q.Endpoint = &ApplicationEndpointRequest{Hostname: "next.apps.example.com", BackendPort: 8088}
	other.Endpoint = q.Endpoint
	a, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.PlanProvision(t.Context(), p.InstallerUID, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Provision(t.Context(), p.InstallerUID, a); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Provision(t.Context(), p.InstallerUID, b); err == nil {
		t.Fatal("stale competing hostname claim accepted")
	}
	for _, host := range []string{"apps.example.com", "a.b.apps.example.com", "example.com", "x.apps.example.com.evil.com", "x.apps.example.com\nimport /etc/passwd", "*.apps.example.com", "UPPER.apps.example.com"} {
		q.Endpoint = &ApplicationEndpointRequest{Hostname: host, BackendPort: 8088}
		if _, err := r.PlanProvision(t.Context(), p.InstallerUID, q); err == nil {
			t.Fatal("hostname accepted", host)
		}
	}
}

func TestApplicationProvisionCloudBackupPolicy(t *testing.T) {
	r, p, q := applicationProvisionFixture(t)
	q.CloudBackup = true
	if _, err := r.PlanProvision(t.Context(), p.InstallerUID, q); err == nil || err.Error() != "application.backup.not_configured" {
		t.Fatal("unconfigured backup accepted", err)
	}
	p.Provisioning.CloudBackup = true
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Provision(t.Context(), p.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	g, err := r.resolveGrant(p, q.Owner)
	if err != nil || !g.Data["files"].CloudBackup {
		t.Fatal("backup enrollment not bound", err)
	}
	p.Provisioning.CloudBackup = false
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	if _, err := r.Provision(t.Context(), p.InstallerUID, plan); err == nil {
		t.Fatal("backup policy change ignored")
	}
}

func TestApplicationProvisionAdmission(t *testing.T) {
	for _, name := range []string{"disabled", "wrong_peer", "wrong_node", "explicit_grant", "pool_symlink", "pool_writable", "invalid_key", "physical_binding", "duplicate_binding", "invalid_repository", "zero_memory", "zero_capacity"} {
		t.Run(name, func(t *testing.T) {
			r, policy, q := applicationProvisionFixture(t)
			peer := policy.InstallerUID
			switch name {
			case "disabled":
				policy.Provisioning = nil
			case "wrong_peer":
				peer = policy.PublisherUID
			case "wrong_node":
				q.Owner.NodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "explicit_grant":
				policy.Grants = []ApplicationGrant{{Owner: q.Owner}}
			case "pool_symlink":
				link := policy.Provisioning.DataRoot + "-link"
				if err := os.Symlink(policy.Provisioning.DataRoot, link); err != nil {
					t.Fatal(err)
				}
				policy.Provisioning.DataRoot = link
			case "pool_writable":
				if err := os.Chmod(policy.Provisioning.DataRoot, 0777); err != nil {
					t.Fatal(err)
				}
			case "invalid_key":
				q.Data["../escape"] = "other"
			case "physical_binding":
				q.Data["files"] = "/tmp/files"
			case "duplicate_binding":
				q.Data["second"] = q.Data["files"]
			case "invalid_repository":
				q.RepositoryID = "not-a-repository"
			case "zero_memory":
				policy.Provisioning.MaxMemoryBytes = 0
			case "zero_capacity":
				policy.Provisioning.MaxPlannedBytes = 0
			}
			applicationPrerequisitesWrite(t, r.PolicyPath, policy)
			before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
			if _, err := r.PlanProvision(context.Background(), peer, q); err == nil {
				t.Fatal("unsafe plan accepted")
			}
			if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
				t.Fatal("denied planning mutated state")
			}
		})
	}
}

func TestApplicationProvisionStalePolicyAndDurableRetry(t *testing.T) {
	r, policy, q := applicationProvisionFixture(t)
	ctx := context.Background()
	plan, err := r.PlanProvision(ctx, policy.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	policy.Provisioning.MaxMemoryBytes++
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	if _, err := r.Provision(ctx, policy.InstallerUID, plan); err == nil || err.Error() != "application.provisioning.plan_stale" {
		t.Fatalf("stale apply=%v", err)
	}
	plan, err = r.PlanProvision(ctx, policy.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	r.FailureHook = func(phase string) error {
		if phase == "provisioning:committed" {
			return errors.New("lost response after durable grant")
		}
		return nil
	}
	if _, err := r.Provision(ctx, policy.InstallerUID, plan); err == nil {
		t.Fatal("fault did not fire")
	}
	// New runtime object observes the same committed grant; no second effect.
	r.FailureHook = nil
	if got, err := r.Provision(ctx, policy.InstallerUID, plan); err != nil || got.GrantRevision != plan.GrantRevision {
		t.Fatalf("retry=%+v err=%v", got, err)
	}
	policy.Provisioning = nil
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	if _, err := r.resolveGrant(policy, q.Owner); err == nil || err.Error() != "application.provisioning.disabled" {
		t.Fatalf("revoked grant=%v", err)
	}
	if _, err := r.QueryPrerequisites(ctx, policy.InstallerUID, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: q.Owner}); err == nil {
		t.Fatal("revocation presented as missing grant")
	}
	owners, err := r.grantOwners(policy)
	if err != nil || len(owners) != 0 {
		t.Fatalf("revoked boot owners=%v err=%v", owners, err)
	}
	if _, err := r.readManagedGrant(q.Owner); err != nil {
		t.Fatal("revocation removed retained record")
	}
}

func TestApplicationProvisionPreservesDataAndRejectsStaleExpansion(t *testing.T) {
	r, policy, q := applicationProvisionFixture(t)
	ctx := context.Background()
	plan, _ := r.PlanProvision(ctx, policy.InstallerUID, q)
	if _, err := r.Provision(ctx, policy.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	before, _ := r.resolveGrant(policy, q.Owner)
	for _, name := range []string{"remove", "rename", "relocate", "repo", "location"} {
		t.Run(name, func(t *testing.T) {
			changed := q
			changed.Data = map[string]string{"files": "fixture.files"}
			p := policy
			v := *policy.Provisioning
			p.Provisioning = &v
			switch name {
			case "remove":
				delete(changed.Data, "files")
			case "rename":
				changed.Data["files"] = "renamed.files"
			case "relocate":
				v.DataRoot += "-new"
				if err := os.Mkdir(v.DataRoot, 0711); err != nil {
					t.Fatal(err)
				}
			case "repo":
				changed.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "location":
				changed.LocationRevision = "elsewhere"
			}
			if _, _, err := r.provisionPlan(p, p.InstallerUID, changed); err == nil {
				t.Fatal("identity migration accepted as provisioning")
			}
		})
	}
	q.Data["second"] = "fixture.second"
	second, err := r.PlanProvision(ctx, policy.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	q.Data["third"] = "fixture.third"
	third, err := r.PlanProvision(ctx, policy.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Provision(ctx, policy.InstallerUID, second); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Provision(ctx, policy.InstallerUID, third); err == nil || err.Error() != "application.provisioning.plan_stale" {
		t.Fatalf("stale expansion=%v", err)
	}
	after, err := r.resolveGrant(policy, q.Owner)
	if err != nil || before.Data["files"] != after.Data["files"] || len(after.Data) != 2 {
		t.Fatalf("retained data=%+v err=%v", after, err)
	}
}

func TestApplicationManagedGrantUsesExistingPublicationInstallBootAndArchive(t *testing.T) {
	for _, mode := range []string{"without_credentials", "proton_credentials", "cloud_history"} {
		t.Run(mode, func(t *testing.T) {
			r, policy, q := applicationProvisionFixture(t)
			if mode == "proton_credentials" {
				r, policy, q, _ = applicationProtonFixture(t)
			}
			ctx := context.Background()
			if mode == "cloud_history" {
				policy.Provisioning.CloudBackup, q.CloudBackup = true, true
				applicationPrerequisitesWrite(t, r.PolicyPath, policy)
			}
			host := &applicationFixtureHost{root: filepath.Dir(r.PolicyPath)}
			r.System = host
			plan, _ := r.PlanProvision(ctx, policy.InstallerUID, q)
			if _, err := r.Provision(ctx, policy.InstallerUID, plan); err != nil {
				t.Fatal(err)
			}
			d := applicationTestDescriptor()
			d.Configuration.Parameters["files"] = ApplicationParameterSpec{Type: ApplicationDataReference, Required: true}
			if mode == "proton_credentials" {
				d.Configuration.Parameters["auth"] = ApplicationParameterSpec{Type: ApplicationCredentialReference, Required: true}
			}
			d.ConfigurationDigest = applicationSHA(d.Configuration)
			serviceTarget, err := ApplicationServiceTarget(q.Owner, "main", "provider_01ARZ3NDEKTSV4RRFFQ69G5FAV")
			if err != nil {
				t.Fatal(err)
			}
			publication := ApplicationPublication{Owner: q.Owner, Revision: ApplicationPreparedRevision(plan, d), LocationRevision: q.LocationRevision, PolicyRevision: plan.PolicyRevision, Descriptor: d, ArchiveTarget: &serviceTarget}
			if err := r.Publish(ctx, policy.InstallerUID, publication); err == nil {
				t.Fatal("provisioning gave installer arbitrary publication authority")
			}
			if err := r.PublishPrepared(ctx, policy.InstallerUID, ApplicationPreparedPublication{GrantRevision: plan.GrantRevision, Publication: publication}); err != nil {
				t.Fatal(err)
			}
			n := int64(42)
			m := ApplicationManifest{Kind: "loom.application", SchemaVersion: ApplicationContractSchema, Artifact: d.Artifact, Process: ApplicationProcess{Manager: ManagerSystemd, User: "fixture", Restart: "on-failure", MemoryMaxBytes: 64 << 20}, Config: ApplicationConfiguration{Schema: d.Configuration.Schema, Values: map[string]ApplicationParameter{"large": {Type: ApplicationInteger, Integer: &n}}}, Health: ApplicationHealth{Kind: HealthKindManager}, Data: map[pc.ResourceKey]ApplicationDataRequirements{"files": {}}}
			dataKey := pc.ResourceKey("files")
			m.Config.Values["files"] = ApplicationParameter{Type: ApplicationDataReference, DataRef: &dataKey}
			request := ApplicationRuntimeRequest{SchemaVersion: ApplicationRuntimeSchema, Operation: "apply", Owner: q.Owner, OperationToken: "managed-install", ExpectedRevision: publication.Revision, PolicyRevision: plan.PolicyRevision, LocationRevision: q.LocationRevision, CredentialRevisions: map[string]string{}, DescriptorDigest: ApplicationDescriptorDigest(d), Manifest: &m, Data: map[string]ApplicationDataRequest{"files": {BindingRef: "fixture.files", PlannedBytes: 1024}}}
			if mode == "proton_credentials" {
				ref := "fixture.auth"
				m.Config.Values["auth"] = ApplicationParameter{Type: ApplicationCredentialReference, CredentialRef: &ref}
				request.CredentialRevisions[ref] = ApplicationProtonCredentialRevision(q.CredentialSources[ref])
			}
			if mode == "cloud_history" {
				data := request.Data["files"]
				data.Backup = "cloud_history"
				request.Data["files"] = data
			}
			receipt, err := r.Execute(ctx, policy.InstallerUID, request)
			if err != nil || !receipt.Applied {
				t.Fatalf("install=%+v err=%v", receipt, err)
			}
			identity := receipt.Data["files"]
			if identity.Path != filepath.Join(policy.Provisioning.DataRoot, q.Owner.Instance()+"-files") {
				t.Fatalf("wrong allocation: %+v", identity)
			}
			if _, err := r.Execute(ctx, policy.InstallerUID, request); err != nil || host.restarts != 1 {
				t.Fatalf("install replay err=%v restarts=%d", err, host.restarts)
			}
			inspect := request
			inspect.Operation = "inspect"
			observed, err := r.Execute(ctx, policy.InstallerUID, inspect)
			if err != nil || receipt.AppliedAt == nil || observed.AppliedAt == nil || !observed.AppliedAt.Equal(*receipt.AppliedAt) || observed.Readiness.Protection != "unknown" {
				t.Fatal("installation time lost or installation asserted backup success", err)
			}
			boot, err := r.RestoreCommitted(ctx)
			if err != nil || len(boot) != 1 || boot[0].State != "observed" {
				t.Fatalf("boot=%+v err=%v", boot, err)
			}
			if _, err := r.ArchiveControl(ctx, policy.InstallerUID, ApplicationArchiveControl{Target: *publication.ArchiveTarget, Operation: OperationStop}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Execute(ctx, policy.InstallerUID, request); err == nil {
				t.Fatal("archive-fenced managed application restarted")
			}
			if _, err := os.Stat(identity.Path); err != nil {
				t.Fatal("archive control removed data")
			}
		})
	}
}

func TestApplicationManagedCapacityLimit(t *testing.T) {
	r, p, q := applicationProvisionFixture(t)
	q.Data["second"] = "second"
	g, err := p.deriveGrant(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []map[string]ApplicationDataRequest{
		{"files": {BindingRef: "fixture.files", PlannedBytes: p.Provisioning.MaxPlannedBytes}, "second": {BindingRef: "second", PlannedBytes: 1}},
		{"files": {BindingRef: "fixture.files", PlannedBytes: math.MaxUint64}, "second": {BindingRef: "second", PlannedBytes: 1}},
	} {
		if _, _, err := r.admitData(context.Background(), g.Grant, requested); err == nil || err.Error() != "application.data.policy_capacity_exceeded" {
			t.Fatalf("capacity limit bypass: %v", err)
		}
	}
}

func TestApplicationProvisionPlanTamperingAndStateCustody(t *testing.T) {
	r, p, q := applicationProvisionFixture(t)
	ctx := context.Background()
	plan, err := r.PlanProvision(ctx, p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	if _, err := r.Provision(ctx, p.PublisherUID, plan); err == nil {
		t.Fatal("publisher impersonated installer")
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("denied peer created helper state")
	}
	for _, field := range []string{"owner", "revision", "expected", "policy"} {
		changed := plan
		switch field {
		case "owner":
			changed.Request.Owner.Resource = "other"
		case "revision":
			changed.GrantRevision = "sha256:" + strings.Repeat("f", 64)
		case "expected":
			changed.ExpectedRevision = "sha256:" + strings.Repeat("e", 64)
		case "policy":
			changed.PolicyRevision = "sha256:" + strings.Repeat("d", 64)
		}
		// Even a syntactically rehashed envelope cannot substitute derived facts.
		changed.PlanID = applicationProvisionPlanID(changed)
		if _, err := r.Provision(ctx, p.InstallerUID, changed); err == nil {
			t.Fatalf("substituted %s accepted", field)
		}
	}
	if _, err := r.Provision(ctx, p.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.Store.Root, "grant-"+q.Owner.Instance()+".json")
	if err := os.Link(path, path+".alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.QueryPrerequisites(ctx, p.InstallerUID, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: q.Owner}); err == nil {
		t.Fatal("multiply-linked grant accepted")
	}
	if _, err := r.Provision(ctx, p.InstallerUID, plan); err == nil {
		t.Fatal("multiply-linked grant accepted on replay")
	}
}

func TestApplicationProvisionHelperResponseBinding(t *testing.T) {
	r, p, q := applicationProvisionFixture(t)
	plan, err := r.PlanProvision(context.Background(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applicationProvisionResponse(ApplicationHelperResponse{Provision: &plan}, q, nil); err != nil {
		t.Fatal(err)
	}
	for _, response := range []ApplicationHelperResponse{{}, {Provision: &plan, Receipt: &ApplicationRuntimeReceipt{}}, {Provision: &plan, Prerequisites: &ApplicationPrerequisiteSnapshot{}}} {
		if _, err := applicationProvisionResponse(response, q, nil); err == nil {
			t.Fatal("invalid helper response accepted")
		}
	}
	q.Owner.Resource = "other"
	if _, err := applicationProvisionResponse(ApplicationHelperResponse{Provision: &plan}, q, nil); err == nil {
		t.Fatal("helper response for another project resource accepted")
	}
	raw, _ := json.Marshal(ApplicationHelperEnvelope{Provision: &plan, ProvisionPlan: &q})
	if _, err := decodeApplicationPrerequisiteEnvelope(raw); err == nil {
		t.Fatal("ambiguous helper envelope accepted")
	}
}
