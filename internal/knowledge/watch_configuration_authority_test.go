package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	localwatch "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/watchedroots"
)

// Reuse the ordinary resolver/owner/ACK fixture, changing only the permitted
// watch configuration in its resolved snapshot. Source snapshots remain intact.
type watchConfigurationResolver struct{ *projectapply.LocalResolver }

func (r watchConfigurationResolver) Resolve(ctx context.Context, p projectapply.Principal, req pc.DeclarationPlanRequest) (projectapply.Resolution, error) {
	x, err := r.LocalResolver.Resolve(ctx, p, req)
	if err != nil {
		return x, err
	}
	for i := range x.Plan.Basis.Actions {
		a := &x.Plan.Basis.Actions[i]
		var payload projects.DeclarationWatchIntentPayload
		if err = json.Unmarshal(x.Payloads[a.ID], &payload); err != nil {
			return x, err
		}
		for j := range payload.Group.Roots {
			root := &payload.Group.Roots[j]
			var portable, effective localwatch.RootConfig
			if err = json.Unmarshal(root.CompilerConfigJSON, &portable); err != nil {
				return x, err
			}
			if err = json.Unmarshal(root.ConfigJSON, &effective); err != nil {
				return x, err
			}
			portable.Watch.DebounceWindow = "3s"
			portable.Scan.FullRescanInterval = "30m"
			effective.Watch, effective.Scan = portable.Watch, portable.Scan
			root.CompilerConfigJSON, err = json.Marshal(portable)
			if err != nil {
				return x, err
			}
			root.CompilerConfigHash = localwatch.ConfigHash(portable)
			root.ConfigJSON, err = json.Marshal(effective)
			if err != nil {
				return x, err
			}
			root.ConfigHash = localwatch.ConfigHash(effective)
			metadata := jsonObject(root.Metadata)
			if source, ok := metadata["knowledge_source"].(map[string]any); ok {
				source["config_hash"] = root.CompilerConfigHash
				root.KnowledgeSource, _ = json.Marshal(source)
			}
			root.Metadata, err = json.Marshal(metadata)
			if err != nil {
				return x, err
			}
		}
		payload.GroupHash, err = projectwatch.DeclarationGroupHash(payload.Group)
		if err != nil {
			return x, err
		}
		x.Payloads[a.ID], err = json.Marshal(payload)
		if err != nil {
			return x, err
		}
		a.InputHash, err = projectapply.PayloadHash(x.Payloads[a.ID])
		if err != nil {
			return x, err
		}
	}
	return x, nil
}

// The fixture models a compiler generation distinct from LocalResolver's
// built-in generation. Revalidate through that same compiler at the owner
// boundary, retaining the real intent transaction, authorization and ACK path.
type watchConfigurationOwner struct {
	projectapply.WatchOwner
	resolver watchConfigurationResolver
}

func (o watchConfigurationOwner) Apply(ctx context.Context, call projectapply.ActionCall) (projectapply.Observation, error) {
	if err := o.Validate(ctx, call.Action, call.Payload); err != nil {
		return projectapply.Observation{}, err
	}
	fence := watchConfigurationSourceFence{resolver: o.resolver, call: call}
	current, err := o.resolver.Current(ctx, call.Principal, fence.original())
	if err != nil {
		return projectapply.Observation{}, err
	}
	if !reflect.DeepEqual(current, call.Expected) {
		return projectapply.Observation{}, &projectapply.Failure{Code: pc.DeclarationPlanStale, Cause: "watch_prerequisite_changed"}
	}
	fresh, err := o.resolver.Resolve(ctx, call.Principal, pc.DeclarationPlanRequest{
		SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: call.Target.ProjectRoot,
		NodeRef: call.Target.OwnerNodeID, Effects: []pc.DeclarationEffect{pc.DeclarationWatches},
	})
	if err != nil {
		return projectapply.Observation{}, err
	}
	hash, err := projectapply.PayloadHash(fresh.Payloads[call.Action.ID])
	if err != nil || hash != call.Action.InputHash {
		return projectapply.Observation{}, &projectapply.Failure{Code: pc.DeclarationPlanStale, Cause: "watch_source_changed"}
	}
	payload, err := projectwatch.ValidateDeclarationWatchIntent(call.Payload)
	if err != nil {
		return projectapply.Observation{}, err
	}
	state, err := o.Reconciler.Projects.GetDeclarationProjectState(ctx, call.Target.ProjectID)
	if err != nil {
		return projectapply.Observation{}, err
	}
	domain := projects.DeclarationWatchIntentRequest{
		DeclarationOwnerRequest: projects.DeclarationOwnerRequest{
			OperationID: call.OperationID, ActionID: call.Action.ID, Token: call.Token,
			InputHash: call.Action.InputHash, TargetNodeID: call.Target.OwnerNodeID,
			Expected: state, AuthorityRevision: call.Expected.Revisions["authorization:"+call.Principal.ActorID+":"+call.Target.OwnerNodeID],
		},
		Payload: payload, ExpectedRevision: call.Expected.Revisions[string(call.Action.Owner)+":"+string(call.Action.Resource)],
	}
	_, err = o.Reconciler.Commit(ctx, requestctx.Context{ActorID: call.Principal.ActorID, OriginNodeID: call.Principal.OriginNodeID}, domain, fence)
	if err != nil {
		return projectapply.Observation{}, err
	}
	return o.Observe(ctx, call)
}

type watchConfigurationSourceFence struct {
	resolver watchConfigurationResolver
	call     projectapply.ActionCall
}

func (f watchConfigurationSourceFence) original() projectapply.Resolution {
	return projectapply.Resolution{Plan: pc.DeclarationPlan{Basis: pc.DeclarationPlanBasis{
		Target: f.call.Target, Bindings: f.call.Expected.Bindings,
	}}}
}

func (f watchConfigurationSourceFence) Check(ctx context.Context) error {
	if f.call.Fence == nil {
		return errors.New("missing fixture operation fence")
	}
	if err := f.call.Fence.Check(ctx); err != nil {
		return err
	}
	current, err := f.resolver.Current(ctx, f.call.Principal, f.original())
	if err != nil {
		return err
	}
	// The owner transaction fences registry/authority CAS. Its uncommitted
	// rows are invisible here, so recheck only the source closure and target.
	if current.Target != f.call.Target || !reflect.DeepEqual(current.Sources, f.call.Expected.Sources) {
		return &projectapply.Failure{Code: pc.DeclarationPlanStale, Cause: "source_changed_before_watch_commit"}
	}
	return nil
}

func TestWatchConfigurationAuthorityPublishedMetadataPostgres(t *testing.T) {
	f := declarationBoundEnrollmentDB(t)
	object := f.publish(t)
	f.visible(t, 1)
	rootPath := f.analysis.Report.ProjectRoot
	resolver := projectapply.NewLocalResolver(f.db, func() (config.Config, error) {
		return config.Config{NodeID: "main", BoxPath: filepath.Dir(rootPath)}, nil
	})
	compiler := watchConfigurationResolver{resolver}
	owner := watchConfigurationOwner{WatchOwner: projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(f.db)}, resolver: compiler}
	service := projectapply.NewService(f.db, compiler, projectapply.NewCurrentAuthority(f.db),
		map[pc.DeclarationOwner]projectapply.Owner{pc.DeclarationOwnerKnowledge: owner, pc.DeclarationOwnerProtection: owner})
	principal := projectapply.Principal{ActorID: f.req.ActorID, OriginNodeID: f.req.OriginNodeID}
	effects := []pc.DeclarationEffect{pc.DeclarationWatches}
	plan, err := service.Plan(t.Context(), principal, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: rootPath, Effects: effects})
	if err != nil {
		t.Fatal(err)
	}
	apply := pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: rootPath, PlanID: plan.PlanID, Effects: effects, IdempotencyKey: "watch-configuration-successor"}
	pending, err := service.Apply(t.Context(), principal, apply)
	var failure *projectapply.Failure
	if !errors.As(err, &failure) || failure.Cause != "owner_pending" {
		t.Fatalf("pending successor: %+v %v", pending, err)
	}
	f.reconcile(t, SourceRootStatusBlocked)
	f.visible(t, 0)
	var messageID string
	if err = f.db.QueryRow(`SELECT message_id FROM projects.declaration_watch_deliveries WHERE operation_id=$1`, pending.OperationID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	message, err := communication.NewService(f.db).GetMessage(t.Context(), messageID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := projectwatch.DecodeDeclarationWatchPayload(message.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := nodes.NewService(f.db).IssueNodeCredential(t.Context(), f.req, nodes.IssueNodeCredentialInput{NodeRef: f.input.NodeID, Reason: "disposable watch successor fixture"})
	if err != nil {
		t.Fatal(err)
	}
	// This SQL fixture models a different compiler generation on both sides.
	// Authenticate its typed receipt through the real ACK owner; the installed
	// node compiler is deliberately not claimed as observed by this fixture.
	ack := communication.AckInput{NodeRef: f.input.NodeID, CredentialToken: credential.CredentialToken,
		CommunicationMessageRef: messageID, AckStatus: communication.AckStatusCompleted,
		ResultJSON: mustJSON(t, projectwatch.DeclarationWatchAck{SchemaVersion: projectwatch.DeclarationWatchSchemaVersion,
			OperationID: payload.OperationID, ProjectID: payload.Group.ProjectID, NodeID: payload.Group.NodeID, GroupHash: payload.GroupHash,
			Evidence: communication.AppliedStateEvidence{SchemaVersion: communication.ControlEvidenceSchemaVersion,
				DesiredRevision: payload.Evidence.DesiredRevision, AppliedRevision: payload.Evidence.DesiredRevision,
				ConfigHash: payload.GroupHash, Outcome: communication.ControlOutcomeCompleted}}),
		ErrorJSON: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`)}
	if _, err = communication.NewService(f.db).Ack(t.Context(), f.req, ack); err != nil {
		t.Fatal(err)
	}
	apply.OperationID = pending.OperationID
	if _, err = service.Apply(t.Context(), principal, apply); err != nil {
		t.Fatal(err)
	}
	item := payload.Group.Roots[0]
	if _, err = watchedroots.NewService(f.db).Report(t.Context(), watchedroots.ReportInput{NodeRef: f.input.NodeID, CredentialToken: credential.CredentialToken, RootKey: item.BackendRootKey, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Status: watchedroots.StatusHealthy}); err != nil {
		t.Fatal(err)
	}
	f.correlate(t)
	f.reconcile(t, SourceRootStatusActive)
	f.visible(t, 1) // No re-admission: original published metadata must still read.
	if _, err = f.service.store.GetKnowledgeObjectWithLifecycle(t.Context(), object.KnowledgeObjectID, SourceLifecycleFilterAll); err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"disabled":       `UPDATE projects.project_watched_root_registrations SET activation_status='disabled'`,
		"stale":          `UPDATE projects.project_watched_root_registrations SET activation_status='stale'`,
		"wrong_group":    `UPDATE projects.project_watched_root_registrations SET metadata=jsonb_set(metadata,'{declaration_adapter,group_hash}',to_jsonb('sha256:'||repeat('0',64)))`,
		"wrong_hash":     `UPDATE watched_roots.roots SET config_hash='sha256:'||repeat('0',64)`,
		"blocked_report": `UPDATE watched_roots.roots SET status='blocked'`,
		"processing":     `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{registration_metadata,knowledge_source,policy}','{"ocr":true}')`,
		"include":        `UPDATE projects.project_watched_root_registrations SET config_json=jsonb_set(config_json,'{include}','["other.md"]')`,
		"path":           `UPDATE projects.project_watched_root_registrations SET root_relative_path='other'`,
		"member":         `UPDATE projects.project_watched_root_registrations SET backend_root_key=backend_root_key||'_other'`,
		"private":        `UPDATE storage.storage_entries SET metadata='{"private_no_index":true}'`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			var allowed bool
			if err = tx.QueryRow(`SELECT `+visibleNotesCustodyObjectSQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&allowed); err != nil || allowed {
				t.Fatalf("successor fence: %t %v", allowed, err)
			}
		})
	}
}

func TestWatchConfigurationPolicyParityPostgres(t *testing.T) {
	f := declarationBoundEnrollmentDB(t)
	before := localwatch.NormalizeRootConfig(localwatch.RootConfig{RootKey: "fixture__notes", SafeRootKey: "project", RootRelativePath: "notes"})
	original := mustJSON(t, before)
	for _, tc := range []struct {
		name   string
		change func(*localwatch.RootConfig)
		allow  bool
	}{
		{"original", func(*localwatch.RootConfig) {}, true},
		{"watch", func(c *localwatch.RootConfig) { c.Watch.DebounceWindow = "3s" }, true},
		{"full_rescan", func(c *localwatch.RootConfig) { c.Scan.FullRescanInterval = "30m" }, true},
		{"scan_budget", func(c *localwatch.RootConfig) { c.Scan.MaxHashBytesPerRun++ }, false},
		{"precise_budget", func(c *localwatch.RootConfig) { c.BackupPolicy.MaxPendingBytes = 1<<54 + 1 }, false},
		{"include", func(c *localwatch.RootConfig) { c.Include = []string{"other.md"} }, false},
		{"path", func(c *localwatch.RootConfig) { c.RootRelativePath = "other" }, false},
		{"sync", func(c *localwatch.RootConfig) { c.SyncPolicy.Mode = localwatch.SyncModeSelectedFiles }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := before
			tc.change(&after)
			var allowed bool
			if err := f.db.QueryRow(`SELECT projects.declaration_watch_policy_matches($1::jsonb,$2::jsonb)`, original, mustJSON(t, after)).Scan(&allowed); err != nil || allowed != tc.allow {
				t.Fatalf("SQL/Go allowed distinction: %t %v", allowed, err)
			}
		})
	}
	for _, tc := range []struct {
		after string
		allow bool
	}{
		{`{"knowledge_source":{"config_hash":"new","include":["**/*.md"],"policy":{"ocr":false}}}`, true},
		{`{"knowledge_source":{"config_hash":"new","include":["other.md"],"policy":{"ocr":false}}}`, false},
		{`{"knowledge_source":{"config_hash":"new","include":["**/*.md"],"policy":{"ocr":true}}}`, false},
	} {
		var allowed bool
		if err := f.db.QueryRow(`SELECT projects.declaration_watch_metadata_matches($1::jsonb,$2::jsonb)`,
			`{"knowledge_source":{"config_hash":"old","include":["**/*.md"],"policy":{"ocr":false}}}`, tc.after).Scan(&allowed); err != nil || allowed != tc.allow {
			t.Fatalf("metadata distinction: %t %v", allowed, err)
		}
	}
}

func TestWatchConfigurationAuthorityMetadataGate(t *testing.T) {
	gate := declarationWatchSourceMatchesSQL("r", "published", "current")
	if !strings.Contains(gate, "r.project_id,r.node_id,r.backend_root_key") {
		t.Fatal("compiler hash compatibility must be scoped to the same source owner", gate)
	}
	if !strings.Contains(notesKnowledgeVisibilitySQL("o", false), gate[:strings.Index(gate, "(")+1]) {
		t.Fatal("publication fence lost authenticated source metadata comparison")
	}
}
