package serviceregistry

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	pc "loom.local/loom/internal/projectcontracts"
)

func applicationFixture(t *testing.T) ApplicationPlanInput {
	t.Helper()
	root, err := os.ReadFile("../projectcontracts/testdata/declarations_v05/webdav.yaml")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile("../projectcontracts/testdata/declarations_v05/backup.yaml")
	if err != nil {
		t.Fatal(err)
	}
	project := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	scope := ApplicationScope{ProjectID: project, NodeID: "main", Resource: "webdav", Revision: "facts-1"}
	in := ApplicationPlanInput{
		Resource: "webdav", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile},
		Target:  pc.DeclarationTarget{ProjectID: project, OwnerNodeID: "main", ProjectRoot: "/srv/projects/webdav", LocationRevision: "location-1"},
		Sources: map[string]ApplicationSource{},
		Facts: ApplicationTargetFacts{
			Scope: scope, Available: true, Platform: "aarch64-linux", Systemd: true, LifecycleRevision: "active-1", Active: true,
			Repository: ApplicationRepositoryFact{ProjectID: project, Key: "code", ID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", Path: "repos/webdav", Revision: "repo-1", Owned: true},
			Artifact:   ApplicationArtifactFact{Ref: "webdav.package", Digest: "sha256:" + strings.Repeat("a", 64), Platform: "aarch64-linux", Revision: "artifact-1", Reviewed: true, ConfigurationDeliverySupported: true, Configuration: ApplicationConfigurationSchema{Schema: "webdav.config.v1", Parameters: map[string]ApplicationParameterSpec{"root": {Type: ApplicationDataReference, Required: true}, "auth": {Type: ApplicationCredentialReference, Required: true}, "read_only": {Type: ApplicationBoolean, Required: true}}}},
			User:       "webdav-app", AvailableMemoryBytes: 1073741824, PortAvailable: true, Port: 8080, ListenerAddress: "127.0.0.1",
			Data:          map[pc.ResourceKey]ApplicationDataFact{"library": {Scope: scope, Path: "/srv/projects/webdav/data/library", Persistent: true, CustodyChecked: true, AvailableBytes: 21474836480, CapacityPool: "disk-1", PoolRevision: "pool-1", MonitorSupported: true}},
			Endpoint:      &ApplicationEndpointFact{Scope: scope, Ref: "webdav.private", Exposure: pc.ApplicationPrivate, URL: "https://webdav.internal.example", Purpose: "application", Backend: "127.0.0.1:8080", Private: true, Available: true},
			Credentials:   map[string]ApplicationCredentialFact{"webdav.auth": {Scope: scope, Ref: "webdav.auth", Provider: "protected-host", ProviderSupported: true, ReferenceAvailable: true, DeliverySupported: true}},
			Authorization: ApplicationAuthority{ActorID: "actor-test", NodeID: "main", PolicyRevision: "auth-1", Allowed: true, Level: 4, RequiredLevel: 3},
		},
	}
	applicationSetSource(&in, ".loom/project.yaml", pc.ProjectSchemaV05, root)
	applicationSetSource(&in, ".loom/applications/webdav.yaml", ApplicationContractSchema, applicationManifestFixture(t))
	applicationSetSource(&in, ".loom/policies/backup.yaml", "backup.policy.v0.3", policy)
	return in
}
func applicationSetSource(in *ApplicationPlanInput, ref, schema string, b []byte) {
	in.Sources[ref] = ApplicationSource{Source: pc.DeclarationSource{Ref: ref, SchemaVersion: schema, Revision: "source-1", Hash: ApplicationSourceHash(b)}, Bytes: b}
}
func applicationMutateSource(in *ApplicationPlanInput, ref, from, to string) {
	s := in.Sources[ref]
	s.Bytes = []byte(strings.Replace(string(s.Bytes), from, to, 1))
	s.Source.Hash = ApplicationSourceHash(s.Bytes)
	in.Sources[ref] = s
}
func TestApplicationPlan(t *testing.T) {
	in := applicationFixture(t)
	before, _ := json.Marshal(in)
	plan := CompileApplicationPlan(in)
	if !plan.Ready {
		t.Fatalf("not ready: %+v", plan.Errors)
	}
	if plan.Executable {
		t.Fatal("pure planner advertises execution")
	}
	kinds := []ApplicationStageKind{}
	ids := map[string]bool{}
	for _, s := range plan.Stages {
		kinds = append(kinds, s.Kind)
		for _, d := range s.DependsOn {
			if !ids[d] {
				t.Fatalf("out of order: %s", d)
			}
		}
		ids[s.ID] = true
		if s.Acceptance == "" || s.Rollback == "" || s.Adapter == "" {
			t.Fatalf("incomplete stage: %+v", s)
		}
		if s.Rollback == "delete_data" || s.Rollback == "restore_data" {
			t.Fatal("persistent data rollback")
		}
	}
	want := []ApplicationStageKind{ApplicationArtifactStage, ApplicationDataStage, ApplicationCredentialStage, ApplicationProcessStage, ApplicationHealthStage, ApplicationEndpointStage, ApplicationProtectionStage}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("stages %v", kinds)
	}
	if plan.Readiness.Healthy.State != pc.DeclarationUnknown || plan.Readiness.Protected.State != pc.DeclarationUnknown || plan.Readiness.Applied.State != pc.DeclarationUnknown {
		t.Fatal("fabricated observations")
	}
	if len(plan.ExecutionPrerequisites) == 0 {
		t.Fatal("missing E2 prerequisites")
	}
	if plan.Data["library"].PlannedBytes != 10737418240 || plan.Data["library"].QuotaBytes != 0 || plan.Data["library"].MonitorThresholdBytes != 8589934592 {
		t.Fatal("capacity conflation")
	}
	if plan.Protection["library"].Root.MaxFileBytes != 16777216 {
		t.Fatal("policy limit lost")
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
}

func TestApplicationPrerequisites(t *testing.T) {
	cases := map[string]func(*ApplicationPlanInput){
		"missing-policy": func(i *ApplicationPlanInput) { delete(i.Sources, ".loom/policies/backup.yaml") },
		"policy-hash": func(i *ApplicationPlanInput) {
			s := i.Sources[".loom/policies/backup.yaml"]
			s.Bytes = append(s.Bytes, '#')
			i.Sources[s.Source.Ref] = s
		},
		"wrong-policy-root": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/policies/backup.yaml", "data/library", "data/elsewhere")
		},
		"missing-repository":   func(i *ApplicationPlanInput) { i.Facts.Repository.ID = "" },
		"repository-ownership": func(i *ApplicationPlanInput) { i.Facts.Repository.ProjectID = "another" },
		"artifact":             func(i *ApplicationPlanInput) { i.Facts.Artifact.Reviewed = false },
		"platform":             func(i *ApplicationPlanInput) { i.Facts.Platform = "x86_64-linux" },
		"target":               func(i *ApplicationPlanInput) { i.Facts.Scope.NodeID = "other" },
		"unavailable":          func(i *ApplicationPlanInput) { i.Facts.Available = false },
		"capacity": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.AvailableBytes = 1
			i.Facts.Data["library"] = d
		},
		"unresolved-allocation": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/project.yaml", "path: data/library", "binding_ref: data.library")
		},
		"unsafe-root": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/project.yaml", "path: data/library", "path: ../library")
		},
		"overlap": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/project.yaml", "path: data/library", "path: repos/webdav/data")
		},
		"transient": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.Persistent = false
			i.Facts.Data["library"] = d
		},
		"custody": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.CustodyChecked = false
			i.Facts.Data["library"] = d
		},
		"rebind": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.ExistingPath = "/srv/old"
			i.Facts.Data["library"] = d
		},
		"wrong-app": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.Scope.Resource = "other"
			i.Facts.Data["library"] = d
		},
		"endpoint":           func(i *ApplicationPlanInput) { i.Facts.Endpoint = nil },
		"occupied-port":      func(i *ApplicationPlanInput) { i.Facts.PortAvailable = false },
		"endpoint-backend":   func(i *ApplicationPlanInput) { i.Facts.Endpoint.Backend = "127.0.0.1:9999" },
		"api-exposure":       func(i *ApplicationPlanInput) { i.Facts.Endpoint.Purpose = "loom-api" },
		"missing-credential": func(i *ApplicationPlanInput) { delete(i.Facts.Credentials, "webdav.auth") },
		"provider": func(i *ApplicationPlanInput) {
			c := i.Facts.Credentials["webdav.auth"]
			c.ProviderSupported = false
			i.Facts.Credentials[c.Ref] = c
		},
		"delivery": func(i *ApplicationPlanInput) {
			c := i.Facts.Credentials["webdav.auth"]
			c.DeliverySupported = false
			i.Facts.Credentials[c.Ref] = c
		},
		"reference": func(i *ApplicationPlanInput) {
			c := i.Facts.Credentials["webdav.auth"]
			c.ReferenceAvailable = false
			i.Facts.Credentials[c.Ref] = c
		},
		"authority":         func(i *ApplicationPlanInput) { i.Facts.Authorization.Allowed = false },
		"authority-level":   func(i *ApplicationPlanInput) { i.Facts.Authorization.Level = 1 },
		"authority-node":    func(i *ApplicationPlanInput) { i.Facts.Authorization.NodeID = "other" },
		"unknown-effect":    func(i *ApplicationPlanInput) { i.Effects = []pc.DeclarationEffect{"destroy"} },
		"empty-effects":     func(i *ApplicationPlanInput) { i.Effects = []pc.DeclarationEffect{} },
		"duplicate-effects": func(i *ApplicationPlanInput) { i.Effects = append(i.Effects, pc.DeclarationReconcile) },
		"missing-revision":  func(i *ApplicationPlanInput) { i.Facts.Scope.Revision = "" },
		"quota": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/applications/webdav.yaml", "quota_bytes: 0", "quota_bytes: 21474836480")
		},
		"monitor": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.MonitorSupported = false
			i.Facts.Data["library"] = d
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := applicationFixture(t)
			mutate(&in)
			p := CompileApplicationPlan(in)
			if p.Ready || len(p.Errors) == 0 {
				t.Fatal("invalid prerequisite yielded ready plan")
			}
			for _, e := range p.Errors {
				if e.Field == "" || e.CauseCode == "" {
					t.Fatalf("unactionable error: %+v", e)
				}
			}
		})
	}
}

func TestApplicationPlanIdentity(t *testing.T) {
	in := applicationFixture(t)
	p := CompileApplicationPlan(in)
	if !p.Ready {
		t.Fatal(p.Errors)
	}
	second := CompileApplicationPlan(in)
	if p.PlanID != second.PlanID {
		t.Fatal("unstable identity")
	}
	cases := map[string]func(*ApplicationPlanInput){
		"source-comment": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/project.yaml", "# Synthetic", "# Changed synthetic")
		},
		"policy-limit": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/policies/backup.yaml", "16777216", "16777217")
		},
		"source-revision": func(i *ApplicationPlanInput) {
			s := i.Sources[".loom/project.yaml"]
			s.Source.Revision = "source-2"
			i.Sources[s.Source.Ref] = s
		},
		"actor":         func(i *ApplicationPlanInput) { i.Facts.Authorization.ActorID = "actor-other" },
		"authorization": func(i *ApplicationPlanInput) { i.Facts.Authorization.PolicyRevision = "auth-2" },
		"capacity-fact": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.AvailableBytes++
			i.Facts.Data["library"] = d
		},
		"repository": func(i *ApplicationPlanInput) { i.Facts.Repository.Revision = "repo-2" },
		"artifact":   func(i *ApplicationPlanInput) { i.Facts.Artifact.Revision = "artifact-2" },
		"endpoint":   func(i *ApplicationPlanInput) { i.Facts.Endpoint.Scope.Revision = "edge-2" },
		"credential": func(i *ApplicationPlanInput) {
			c := i.Facts.Credentials["webdav.auth"]
			c.Scope.Revision = "credential-2"
			i.Facts.Credentials[c.Ref] = c
		},
		"location": func(i *ApplicationPlanInput) { i.Target.LocationRevision = "location-2" },
		"config": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/applications/webdav.yaml", "boolean: false", "boolean: true")
		},
		"large-integer": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.AvailableBytes = 9007199254740993
			i.Facts.Data["library"] = d
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			i := applicationFixture(t)
			mutate(&i)
			q := CompileApplicationPlan(i)
			if q.Ready && q.PlanID == p.PlanID {
				t.Fatal("effective input omitted from identity")
			}
		})
	}
}

func TestApplicationPlanEdgesAndCapacity(t *testing.T) {
	t.Run("default-loopback", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/project.yaml", "      endpoint: {exposure: private, endpoint_ref: webdav.private}\n", "")
		in.Facts.Endpoint = nil
		p := CompileApplicationPlan(in)
		if !p.Ready {
			t.Fatal(p.Errors)
		}
		for _, s := range p.Stages {
			if s.Kind == ApplicationEndpointStage {
				t.Fatal("default published an edge")
			}
		}
	})
	t.Run("approved-public-edge", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/project.yaml", "exposure: private", "exposure: public_https")
		e := in.Facts.Endpoint
		e.Exposure = pc.ApplicationPublicHTTPS
		e.Private = false
		e.URL = "https://webdav.example.test"
		e.ApprovedApplicationEdge = true
		e.ApprovalRef = "approved-exact-edge-1"
		p := CompileApplicationPlan(in)
		if !p.Ready {
			t.Fatal(p.Errors)
		}
		for _, s := range p.Stages {
			if !s.Authorization.ApprovalRequired {
				t.Fatal("public approval not bound")
			}
		}
		mutations := map[string]func(){"no-approval": func() { e.ApprovalRef = "" }, "http": func() { e.URL = "http://webdav.example.test" }, "orca": func() { e.Purpose = "orca-api" }}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				saved := *e
				mutate()
				defer func() { *e = saved }()
				if CompileApplicationPlan(in).Ready {
					t.Fatal("public edge accepted invalid facts")
				}
			})
		}
	})
	t.Run("quota-request-not-enforcement", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/applications/webdav.yaml", "quota_bytes: 0", "quota_bytes: 21474836480")
		d := in.Facts.Data["library"]
		d.QuotaSupported = true
		in.Facts.Data["library"] = d
		p := CompileApplicationPlan(in)
		if !p.Ready {
			t.Fatal(p.Errors)
		}
		dplan := p.Data["library"]
		if dplan.QuotaBytes != 21474836480 || dplan.QuotaEnforced || dplan.CapacityReserved {
			t.Fatal("capacity truth lost")
		}
	})
	t.Run("resolved-external-allocation", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/project.yaml", "path: data/library", "binding_ref: data.library")
		d := in.Facts.Data["library"]
		d.BindingRef = "data.library"
		d.Path = "/srv/application-data/webdav"
		d.Allocated = true
		d.ProtectionPath = "data/library"
		in.Facts.Data["library"] = d
		p := CompileApplicationPlan(in)
		if !p.Ready {
			t.Fatal(p.Errors)
		}
		if p.Executable {
			t.Fatal("E2 adapter invented")
		}
	})
}

func TestApplicationPlanPolicySource(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/policies/backup.yaml", "enabled: true", "enabled: false")
		p := CompileApplicationPlan(in)
		if !p.Ready {
			t.Fatal(p.Errors)
		}
		if p.Protection["library"].Enabled || p.Stages[len(p.Stages)-1].Acceptance != "disabled_policy_preserved_not_protected" {
			t.Fatal("disabled policy lost")
		}
		if p.Readiness.Protected.State != pc.DeclarationUnknown {
			t.Fatal("disabled became protected")
		}
	})
	t.Run("only-selected-root-with-defaults", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/policies/backup.yaml", "  roots:", "  defaults:\n    max_batch_bytes: 67108864\n    max_pending_items: 8\n    on_limit: degrade_and_require_manual_action\n    include_deletion_markers: false\n  roots:")
		s := in.Sources[".loom/policies/backup.yaml"]
		s.Bytes = append(s.Bytes, []byte("    - key: unrelated\n      path: material\n      mode: none\n")...)
		s.Source.Hash = ApplicationSourceHash(s.Bytes)
		in.Sources[s.Source.Ref] = s
		p := CompileApplicationPlan(in)
		if !p.Ready {
			t.Fatal(p.Errors)
		}
		pr := p.Protection["library"]
		if len(p.Protection) != 1 || pr.Root.Path != "data/library" || pr.Defaults.MaxBatchBytes != 67108864 || pr.Defaults.IncludeDeletionMarkers == nil || *pr.Defaults.IncludeDeletionMarkers {
			t.Fatal("selection/defaults lost")
		}
		if !bytes.Equal(p.Basis.Input.Sources[s.Source.Ref].Bytes, s.Bytes) {
			t.Fatal("source bytes not retained")
		}
	})
	t.Run("opaque-D0-is-not-manifest", func(t *testing.T) {
		in := applicationFixture(t)
		b, err := os.ReadFile("../projectcontracts/testdata/declarations_v05/webdav-application.fixture.txt")
		if err != nil {
			t.Fatal(err)
		}
		applicationSetSource(&in, ".loom/applications/webdav.yaml", ApplicationContractSchema, b)
		if CompileApplicationPlan(in).Ready {
			t.Fatal("opaque D0 fixture treated as executable declaration")
		}
	})
}

func TestApplicationPlanIdentityCanonical(t *testing.T) {
	in := applicationFixture(t)
	first := CompileApplicationPlan(in)
	// Reverse source map insertion without changing actual source bytes.
	reordered := map[string]ApplicationSource{}
	for _, ref := range []string{".loom/policies/backup.yaml", ".loom/applications/webdav.yaml", ".loom/project.yaml"} {
		reordered[ref] = in.Sources[ref]
	}
	in.Sources = reordered
	second := CompileApplicationPlan(in)
	if first.PlanID != second.PlanID {
		t.Fatal("map order affects identity")
	}
	// Plan owns its source/fact snapshots after compilation.
	in.Sources[".loom/project.yaml"].Bytes[0] = '!'
	in.Facts.Endpoint.URL = "https://changed.example"
	if first.Basis.Input.Facts.Endpoint.URL == in.Facts.Endpoint.URL || first.Basis.Input.Sources[".loom/project.yaml"].Bytes[0] == '!' {
		t.Fatal("plan aliases input")
	}
	a := applicationFixture(t)
	d := a.Facts.Data["library"]
	d.AvailableBytes = 9007199254740992
	a.Facts.Data["library"] = d
	p := CompileApplicationPlan(a)
	d.AvailableBytes++
	a.Facts.Data["library"] = d
	q := CompileApplicationPlan(a)
	if !p.Ready || !q.Ready || p.PlanID == q.PlanID {
		t.Fatal("uint64 rounded through float")
	}
	// Every exported target fact leaf must be in the identity or reject readiness.
	baseline := applicationFixture(t)
	normal := CompileApplicationPlan(baseline)
	var visit func(reflect.Value, []string)
	visit = func(v reflect.Value, trail []string) {
		switch v.Kind() {
		case reflect.Struct:
			for n := 0; n < v.NumField(); n++ {
				visit(v.Field(n), append(append([]string{}, trail...), v.Type().Field(n).Name))
			}
		case reflect.Pointer:
			if !v.IsNil() {
				visit(v.Elem(), trail)
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				visit(v.MapIndex(k), append(append([]string{}, trail...), "@"+k.String()))
			}
		case reflect.String, reflect.Bool, reflect.Uint64, reflect.Uint16, reflect.Int:
			name := strings.Join(trail, ".")
			t.Run(name, func(t *testing.T) {
				candidate := applicationFixture(t)
				var change func(reflect.Value, []string) reflect.Value
				change = func(x reflect.Value, keys []string) reflect.Value {
					out := reflect.New(x.Type()).Elem()
					out.Set(x)
					if len(keys) == 0 {
						switch out.Kind() {
						case reflect.String:
							out.SetString(out.String() + "-changed")
						case reflect.Bool:
							out.SetBool(!out.Bool())
						case reflect.Int:
							out.SetInt(out.Int() + 1)
						default:
							out.SetUint(out.Uint() + 1)
						}
						return out
					}
					if out.Kind() == reflect.Pointer {
						e := reflect.New(out.Type().Elem())
						e.Elem().Set(change(out.Elem(), keys))
						return e
					}
					if out.Kind() == reflect.Map {
						k := reflect.ValueOf(strings.TrimPrefix(keys[0], "@")).Convert(out.Type().Key())
						out.SetMapIndex(k, change(out.MapIndex(k), keys[1:]))
						return out
					}
					f := out.FieldByName(keys[0])
					f.Set(change(f, keys[1:]))
					return out
				}
				candidate.Facts = change(reflect.ValueOf(candidate.Facts), trail).Interface().(ApplicationTargetFacts)
				p := CompileApplicationPlan(candidate)
				if p.Ready && p.PlanID == normal.PlanID {
					t.Fatal("effective fact absent from identity")
				}
			})
		default:
			t.Fatalf("identity test missing kind %v", v.Kind())
		}
	}
	visit(reflect.ValueOf(baseline.Facts), nil)
}

func TestApplicationPlanAdversarial(t *testing.T) {
	cases := map[string]func(*ApplicationPlanInput){
		"unprobed-port":    func(i *ApplicationPlanInput) { i.Facts.Port = 8081 },
		"unprobed-address": func(i *ApplicationPlanInput) { i.Facts.ListenerAddress = "::1" },
		"policy-duplicate-root": func(i *ApplicationPlanInput) {
			s := i.Sources[".loom/policies/backup.yaml"]
			s.Bytes = append(s.Bytes, []byte("    - key: second\n      path: data/library\n      mode: none\n")...)
			s.Source.Hash = ApplicationSourceHash(s.Bytes)
			i.Sources[s.Source.Ref] = s
		},
		"credential-other-project": func(i *ApplicationPlanInput) {
			c := i.Facts.Credentials["webdav.auth"]
			c.Scope.ProjectID = "another"
			i.Facts.Credentials[c.Ref] = c
		},
		"unknown-data-effect": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/applications/webdav.yaml", "    quota_bytes: 0", "    quota_bytes: 0\n    delete_on_remove: true")
		},
		"secret-in-manifest": func(i *ApplicationPlanInput) {
			applicationMutateSource(i, ".loom/applications/webdav.yaml", "  schema: webdav.config.v1", "  schema: webdav.config.v1\n  password: synthetic-secret")
		},
		"extra-snapshot": func(i *ApplicationPlanInput) {
			applicationSetSource(i, ".loom/unreferenced.yaml", "unknown", []byte("secret: synthetic-secret"))
		},
		"worktree-data": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			d.Path = "/srv/worktrees/webdav/data/library"
			i.Facts.Data["library"] = d
		},
		"unused-data-fact": func(i *ApplicationPlanInput) {
			d := i.Facts.Data["library"]
			i.Facts.Data["second"] = d
		},
		"authority-approval": func(i *ApplicationPlanInput) { i.Facts.Authorization.ApprovalRequired = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := applicationFixture(t)
			mutate(&in)
			p := CompileApplicationPlan(in)
			if p.Ready || p.PlanID != "" || p.Basis != nil || len(p.Stages) != 0 || len(p.Data) != 0 || len(p.Protection) != 0 {
				t.Fatal("invalid plan retained usable output")
			}
			b, _ := json.Marshal(p)
			if bytes.Contains(b, []byte("synthetic-secret")) {
				t.Fatal("invalid plan leaked source")
			}
		})
	}
}

func TestApplicationPlanAdversarialAllocationAndHostname(t *testing.T) {
	t.Run("allocation-reserved-path", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/project.yaml", "path: data/library", "binding_ref: data.library")
		d := in.Facts.Data["library"]
		d.BindingRef = "data.library"
		d.Allocated = true
		d.ProtectionPath = "data/library"
		d.Path = "/srv/.git/library"
		in.Facts.Data["library"] = d
		if CompileApplicationPlan(in).Ready {
			t.Fatal("reserved allocation accepted")
		}
	})
	t.Run("public-invalid-hostname", func(t *testing.T) {
		in := applicationFixture(t)
		applicationMutateSource(&in, ".loom/project.yaml", "exposure: private", "exposure: public_https")
		e := in.Facts.Endpoint
		e.Exposure = pc.ApplicationPublicHTTPS
		e.ApprovedApplicationEdge = true
		e.ApprovalRef = "approved-edge"
		e.URL = "https://*.example.test"
		if CompileApplicationPlan(in).Ready {
			t.Fatal("wildcard URL accepted as exact public hostname")
		}
	})
}

// Corrective outcomes are written against bytes and the supplied-fact wire
// shape before product changes; old code compiles these tests but cannot plan
// the generic manifests. No frozen D0 fixture is edited.
func applicationEditManifest(t *testing.T, in *ApplicationPlanInput, edit func(map[string]any)) {
	t.Helper()
	s := in.Sources[".loom/applications/webdav.yaml"]
	var m map[string]any
	if err := yaml.Unmarshal(s.Bytes, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	applicationSetSource(in, s.Source.Ref, ApplicationContractSchema, b)
}
func applicationEditProject(t *testing.T, in *ApplicationPlanInput, edit func(*pc.ProjectDeclaration)) {
	t.Helper()
	s := in.Sources[".loom/project.yaml"]
	var p pc.ProjectDeclaration
	if err := yaml.Unmarshal(s.Bytes, &p); err != nil {
		t.Fatal(err)
	}
	edit(&p)
	b, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	applicationSetSource(in, s.Source.Ref, pc.ProjectSchemaV05, b)
}
func applicationInstallSchema(t *testing.T, in *ApplicationPlanInput, schema string, parameters map[string]any, supported bool) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	facts := wire["facts"].(map[string]any)
	artifact := facts["artifact"].(map[string]any)
	artifact["configuration"] = map[string]any{"schema": schema, "parameters": parameters}
	artifact["configuration_delivery_supported"] = supported
	for _, d := range facts["data"].(map[string]any) {
		d.(map[string]any)["pool_revision"] = "pool-1"
	}
	b, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var replacement ApplicationPlanInput
	if err = json.Unmarshal(b, &replacement); err != nil {
		t.Fatal(err)
	}
	*in = replacement
}
func applicationCorrectiveFixture(t *testing.T, variant string) ApplicationPlanInput {
	t.Helper()
	in := applicationFixture(t)
	b, err := os.ReadFile("application_testdata/generic-webdav.yaml")
	if err != nil {
		t.Fatal(err)
	}
	applicationSetSource(&in, ".loom/applications/webdav.yaml", ApplicationContractSchema, b)
	schema := "webdav.config.v1"
	spec := func(kind string) any { return map[string]any{"type": kind, "required": true} }
	parameters := map[string]any{"root": spec("data_ref"), "auth": spec("credential_ref"), "read_only": spec("boolean")}
	switch variant {
	case "plain", "manager":
		schema = "status.config.v1"
		parameters = map[string]any{"banner": spec("string"), "workers": spec("integer")}
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"] = map[string]any{"schema": schema, "values": map[string]any{"banner": map[string]any{"type": "string", "string": "ready"}, "workers": map[string]any{"type": "integer", "integer": 2}}}
			m["data"] = map[string]any{}
			m["health"] = map[string]any{"kind": "http", "method": "GET", "path": "/health", "expected_status": 200, "timeout_seconds": 5}
			if variant == "manager" {
				delete(m, "listener")
				m["health"] = map[string]any{"kind": "manager"}
			}
		})
		applicationEditProject(t, &in, func(p *pc.ProjectDeclaration) {
			a := p.Resources["webdav"].Application
			a.Data = nil
			a.Credentials = nil
			a.Endpoint = nil
			delete(p.Resources, "retained")
		})
		in.Facts.Data = map[pc.ResourceKey]ApplicationDataFact{}
		in.Facts.Credentials = map[string]ApplicationCredentialFact{}
		in.Facts.Endpoint = nil
		delete(in.Sources, ".loom/policies/backup.yaml")
		if variant == "manager" {
			in.Facts.Port = 0
			in.Facts.ListenerAddress = ""
			in.Facts.PortAvailable = false
		}
	case "two-data", "pool-exhausted":
		parameters["index"] = spec("data_ref")
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"].(map[string]any)["values"].(map[string]any)["index"] = map[string]any{"type": "data_ref", "data_ref": "index"}
			m["data"].(map[string]any)["index"] = map[string]any{"monitor_threshold_bytes": 0, "quota_bytes": 0}
		})
		applicationEditProject(t, &in, func(p *pc.ProjectDeclaration) {
			p.Resources["webdav"].Application.Data["index"] = pc.ApplicationData{Path: "data/index", Capacity: &pc.ApplicationCapacity{PlannedBytes: 8589934592}, Protection: "retained"}
		})
		d := in.Facts.Data["library"]
		d.Path = "/srv/projects/webdav/data/index"
		in.Facts.Data["index"] = d
		s := in.Sources[".loom/policies/backup.yaml"]
		s.Bytes = append(s.Bytes, []byte("    - key: webdav_index\n      path: data/index\n      mode: incremental_raw\n")...)
		applicationSetSource(&in, s.Source.Ref, s.Source.SchemaVersion, s.Bytes)
		if variant == "pool-exhausted" {
			for k, d := range in.Facts.Data {
				d.AvailableBytes = 17179869184
				in.Facts.Data[k] = d
			}
		}
	case "two-credentials":
		parameters["second_auth"] = spec("credential_ref")
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"].(map[string]any)["values"].(map[string]any)["second_auth"] = map[string]any{"type": "credential_ref", "credential_ref": "webdav.token"}
		})
		applicationEditProject(t, &in, func(p *pc.ProjectDeclaration) {
			p.Resources["webdav"].Application.Credentials = append(p.Resources["webdav"].Application.Credentials, "webdav.token")
		})
		c := in.Facts.Credentials["webdav.auth"]
		c.Ref = "webdav.token"
		in.Facts.Credentials[c.Ref] = c
	}
	applicationInstallSchema(t, &in, schema, parameters, true)
	return in
}

func TestApplicationPlanCorrectiveIndependent(t *testing.T) {
	for _, variant := range []string{"webdav", "plain", "manager", "two-data", "two-credentials"} {
		t.Run(variant, func(t *testing.T) {
			in := applicationCorrectiveFixture(t, variant)
			p := CompileApplicationPlan(in)
			if !p.Ready {
				t.Fatalf("general application rejected: %+v", p.Errors)
			}
			if p.Executable || p.Readiness.Healthy.State != pc.DeclarationUnknown {
				t.Fatal("execution/health fabricated")
			}
			counts := map[ApplicationStageKind]int{}
			for _, s := range p.Stages {
				counts[s.Kind]++
			}
			if variant == "plain" || variant == "manager" {
				if counts[ApplicationCredentialStage] != 0 || counts[ApplicationDataStage] != 0 || counts[ApplicationProtectionStage] != 0 {
					t.Fatal("unused effects invented")
				}
			}
			if variant == "two-data" && counts[ApplicationDataStage] != 2 {
				t.Fatal("binding omitted")
			}
			if variant == "two-credentials" && counts[ApplicationCredentialStage] != 2 {
				t.Fatal("credential omitted")
			}
		})
	}
	t.Run("aggregate-exhaustion", func(t *testing.T) {
		p := CompileApplicationPlan(applicationCorrectiveFixture(t, "pool-exhausted"))
		applicationRequireCause(t, p, "data.pool_capacity")
	})
	t.Run("wrong-schema", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "webdav")
		applicationEditManifest(t, &in, func(m map[string]any) { m["config"].(map[string]any)["schema"] = "wrong.config.v1" })
		applicationRequireCause(t, CompileApplicationPlan(in), "artifact.configuration_schema")
	})
	t.Run("configuration-delivery", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		applicationInstallSchema(t, &in, "status.config.v1", map[string]any{"banner": map[string]any{"type": "string", "required": true}, "workers": map[string]any{"type": "integer", "required": true}}, false)
		applicationRequireCause(t, CompileApplicationPlan(in), "E2.artifact_configuration_delivery")
	})
	t.Run("credential-delivery", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "two-credentials")
		c := in.Facts.Credentials["webdav.token"]
		c.DeliverySupported = false
		in.Facts.Credentials[c.Ref] = c
		applicationRequireCause(t, CompileApplicationPlan(in), "E2.credential_delivery")
	})
}
func applicationRequireCause(t *testing.T, p ApplicationPlan, cause string) {
	t.Helper()
	if p.Ready {
		t.Fatal("invalid application yielded ready plan")
	}
	for _, e := range p.Errors {
		if e.CauseCode == cause {
			return
		}
	}
	t.Fatalf("expected %s, got %+v", cause, p.Errors)
}

func TestApplicationPlanConfigurationBindings(t *testing.T) {
	cases := map[string]struct {
		variant string
		change  func(*ApplicationPlanInput)
		cause   string
	}{
		"unknown-key": {"plain", func(i *ApplicationPlanInput) {
			applicationEditManifest(t, i, func(m map[string]any) {
				m["config"].(map[string]any)["values"].(map[string]any)["extra"] = map[string]any{"type": "boolean", "boolean": true}
			})
		}, "artifact.configuration_parameters"},
		"missing-required": {"plain", func(i *ApplicationPlanInput) {
			applicationEditManifest(t, i, func(m map[string]any) { delete(m["config"].(map[string]any)["values"].(map[string]any), "banner") })
		}, "artifact.configuration_parameters"},
		"wrong-schema-type": {"plain", func(i *ApplicationPlanInput) {
			s := i.Facts.Artifact.Configuration.Parameters["banner"]
			s.Type = ApplicationInteger
			i.Facts.Artifact.Configuration.Parameters["banner"] = s
		}, "artifact.configuration_value"},
		"unknown-artifact-type": {"plain", func(i *ApplicationPlanInput) {
			s := i.Facts.Artifact.Configuration.Parameters["banner"]
			s.Type = "shell"
			i.Facts.Artifact.Configuration.Parameters["banner"] = s
		}, "artifact.configuration_schema"},
		"missing-data-ref": {"two-data", func(i *ApplicationPlanInput) { delete(i.Facts.Data, "index") }, "data.binding_set"},
		"extra-data-fact":  {"plain", func(i *ApplicationPlanInput) { i.Facts.Data["extra"] = ApplicationDataFact{} }, "data.binding_set"},
		"wrong-data-key-same-size": {"two-data", func(i *ApplicationPlanInput) {
			d := i.Facts.Data["index"]
			delete(i.Facts.Data, "index")
			i.Facts.Data["other"] = d
		}, "data.custody_capacity"},
		"extra-data-declaration": {"plain", func(i *ApplicationPlanInput) {
			applicationEditProject(t, i, func(p *pc.ProjectDeclaration) {
				p.Resources["webdav"].Application.Data = map[pc.ResourceKey]pc.ApplicationData{"extra": {Path: "data/extra"}}
			})
		}, "data.binding_set"},
		"duplicate-credential": {"two-credentials", func(i *ApplicationPlanInput) {
			applicationEditProject(t, i, func(p *pc.ProjectDeclaration) {
				p.Resources["webdav"].Application.Credentials = []string{"webdav.auth", "webdav.auth"}
			})
		}, "credential.reference_set"},
		"missing-second-credential": {"two-credentials", func(i *ApplicationPlanInput) { delete(i.Facts.Credentials, "webdav.token") }, "E2.credential_delivery"},
		"wrong-second-credential-scope": {"two-credentials", func(i *ApplicationPlanInput) {
			c := i.Facts.Credentials["webdav.token"]
			c.Scope.Resource = "other"
			i.Facts.Credentials[c.Ref] = c
		}, "E2.credential_delivery"},
		"unused-credential": {"plain", func(i *ApplicationPlanInput) { i.Facts.Credentials["unused.auth"] = ApplicationCredentialFact{} }, "credential.reference_set"},
		"inconsistent-pool-capacity": {"two-data", func(i *ApplicationPlanInput) {
			d := i.Facts.Data["index"]
			d.AvailableBytes++
			i.Facts.Data["index"] = d
		}, "data.pool_snapshot"},
		"inconsistent-pool-revision": {"two-data", func(i *ApplicationPlanInput) {
			d := i.Facts.Data["index"]
			d.PoolRevision = "pool-2"
			i.Facts.Data["index"] = d
		}, "data.pool_snapshot"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := applicationCorrectiveFixture(t, c.variant)
			c.change(&in)
			p := CompileApplicationPlan(in)
			applicationRequireCause(t, p, c.cause)
			if p.Basis != nil || len(p.Stages) > 0 {
				t.Fatal("invalid bindings retained usable output")
			}
		})
	}
	t.Run("optional-absent", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		in.Facts.Artifact.Configuration.Parameters["optional"] = ApplicationParameterSpec{Type: ApplicationString}
		if p := CompileApplicationPlan(in); !p.Ready {
			t.Fatal(p.Errors)
		}
	})
	t.Run("reference-aliases", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "webdav")
		in.Facts.Artifact.Configuration.Parameters["also_root"] = ApplicationParameterSpec{Type: ApplicationDataReference, Required: true}
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"].(map[string]any)["values"].(map[string]any)["also_root"] = map[string]any{"type": "data_ref", "data_ref": "library"}
		})
		p := CompileApplicationPlan(in)
		if !p.Ready || len(p.Data) != 1 {
			t.Fatalf("reference alias double-counted: %+v", p.Errors)
		}
	})
	t.Run("no-parameters", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		in.Facts.Artifact.Configuration.Parameters = map[string]ApplicationParameterSpec{}
		applicationEditManifest(t, &in, func(m map[string]any) { m["config"].(map[string]any)["values"] = map[string]any{} })
		if p := CompileApplicationPlan(in); !p.Ready {
			t.Fatal(p.Errors)
		}
	})
}

func TestApplicationPlanPoolBoundaries(t *testing.T) {
	for _, delta := range []uint64{0, 1} {
		t.Run("capacity-minus-"+string(rune('0'+delta)), func(t *testing.T) {
			in := applicationCorrectiveFixture(t, "two-data")
			for k, d := range in.Facts.Data {
				d.AvailableBytes = 19327352832 - delta
				in.Facts.Data[k] = d
			}
			p := CompileApplicationPlan(in)
			if delta == 0 && !p.Ready {
				t.Fatal(p.Errors)
			}
			if delta == 1 {
				applicationRequireCause(t, p, "data.pool_capacity")
			}
		})
	}
	t.Run("independent-pools", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "pool-exhausted")
		d := in.Facts.Data["index"]
		d.CapacityPool = "disk-2"
		in.Facts.Data["index"] = d
		if p := CompileApplicationPlan(in); !p.Ready {
			t.Fatal(p.Errors)
		}
	})
	t.Run("sum-overflow", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "two-data")
		applicationEditProject(t, &in, func(p *pc.ProjectDeclaration) {
			for k, d := range p.Resources["webdav"].Application.Data {
				d.Capacity.PlannedBytes = 1 << 63
				p.Resources["webdav"].Application.Data[k] = d
			}
		})
		for k, d := range in.Facts.Data {
			d.AvailableBytes = ^uint64(0)
			in.Facts.Data[k] = d
		}
		applicationRequireCause(t, CompileApplicationPlan(in), "data.pool_capacity")
	})
}

func TestApplicationPlanCorrectiveAdversarial(t *testing.T) {
	t.Run("allocated-binding-overlap", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "two-data")
		applicationEditProject(t, &in, func(p *pc.ProjectDeclaration) {
			for k, d := range p.Resources["webdav"].Application.Data {
				d.BindingRef = "allocation." + string(k)
				d.Path = ""
				p.Resources["webdav"].Application.Data[k] = d
			}
		})
		for k, d := range in.Facts.Data {
			d.BindingRef = "allocation." + string(k)
			d.Allocated = true
			d.ProtectionPath = "data/" + string(k)
			d.Path = "/srv/application-data/shared"
			in.Facts.Data[k] = d
		}
		applicationRequireCause(t, CompileApplicationPlan(in), "data.overlap")
	})
	t.Run("health-empty-fragment", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		applicationEditManifest(t, &in, func(m map[string]any) { m["health"].(map[string]any)["path"] = "/health#" })
		applicationRequireCause(t, CompileApplicationPlan(in), "manifest.contract")
	})
	t.Run("unbraced-interpolation", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"].(map[string]any)["values"].(map[string]any)["banner"] = map[string]any{"type": "string", "string": "$TOKEN"}
		})
		applicationRequireCause(t, CompileApplicationPlan(in), "manifest.contract")
	})
	t.Run("input-and-filesystem-unchanged", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		in.Target.ProjectRoot = t.TempDir()
		before, _ := json.Marshal(in)
		p := CompileApplicationPlan(in)
		after, _ := json.Marshal(in)
		if !p.Ready || p.Executable || !bytes.Equal(before, after) {
			t.Fatal("impure plan or invented execution")
		}
		entries, err := os.ReadDir(in.Target.ProjectRoot)
		if err != nil || len(entries) != 0 {
			t.Fatal("pure compiler wrote into supplied root")
		}
		for _, stage := range p.Stages {
			if strings.Contains(stage.Rollback, "delete") || strings.Contains(stage.Rollback, "restore_data") {
				t.Fatal("destructive rollback")
			}
		}
		for _, prerequisite := range p.ExecutionPrerequisites {
			if strings.Contains(prerequisite, "credential") || strings.Contains(prerequisite, "allocation") || strings.Contains(prerequisite, "protection") {
				t.Fatal("unused delivery prerequisite invented")
			}
		}
	})
	t.Run("complete-parameter-identity", func(t *testing.T) {
		in := applicationCorrectiveFixture(t, "plain")
		p := CompileApplicationPlan(in)
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"].(map[string]any)["values"].(map[string]any)["workers"] = map[string]any{"type": "integer", "integer": int64(9007199254740992)}
		})
		a := CompileApplicationPlan(in)
		applicationEditManifest(t, &in, func(m map[string]any) {
			m["config"].(map[string]any)["values"].(map[string]any)["workers"] = map[string]any{"type": "integer", "integer": int64(9007199254740993)}
		})
		b := CompileApplicationPlan(in)
		if !a.Ready || !b.Ready || a.PlanID == p.PlanID || a.PlanID == b.PlanID {
			t.Fatal("parameter identity lost integer precision")
		}
	})
}
