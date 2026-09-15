package policy

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/requestctx"
)

func previewDatabase(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires disposable LOOM_TEST_DB_URL")
	}
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && !(u.Hostname() == "" && strings.HasPrefix(u.Query().Get("host"), "/tmp/")) {
		t.Fatal("refuse nonlocal PostgreSQL fixture")
	}
	admin, e := sql.Open("pgx", raw)
	if e != nil {
		t.Fatal(e)
	}
	name := "preview_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, e = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	u.Path = "/" + name
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		if _, e := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	result, e := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations"))
	if e != nil || result.CurrentVersion != 71 {
		t.Fatalf("fresh migration: %+v %v", result, e)
	}
	return db
}
func previewSQL(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, e := db.ExecContext(t.Context(), q, args...); e != nil {
		t.Fatal(e)
	}
}

// Capture every ordinary table, including jobs/calls/messages/idempotency rows,
// rather than inferring purity from only the policy decision row count.
func previewRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, e := db.Query(`SELECT schemaname,tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY 1,2`)
	if e != nil {
		t.Fatal(e)
	}
	names := [][2]string{}
	for rows.Next() {
		var s, n string
		if e := rows.Scan(&s, &n); e != nil {
			t.Fatal(e)
		}
		names = append(names, [2]string{s, n})
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	out := map[string]string{}
	for _, n := range names {
		quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
		name := quote(n[0]) + "." + quote(n[1])
		var value string
		if e := db.QueryRow(`SELECT coalesce(jsonb_agg(row_value ORDER BY row_value::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) row_value FROM ` + name + ` t) s`).Scan(&value); e != nil {
			t.Fatal(e)
		}
		out[name] = value
	}
	return out
}
func TestPolicyPreviewPostgres(t *testing.T) {
	db := previewDatabase(t)
	svc := NewService(db)
	previewSQL(t, db, `INSERT INTO identity.actors(actor_id,actor_key,display_name,actor_kind,status) VALUES ('actor_preview','preview','Preview','agent','active'),('actor_approver','approver','Approver','human','active');
 INSERT INTO nodes.nodes(node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES ('node_origin','origin','Origin','computer','workspace','full','active'),('node_target','target','Target','computer','workspace','full','active');
 INSERT INTO scopes.scopes(scope_id,scope_type,scope_key,slug,display_name,status) VALUES ('scope_preview','project','preview','preview','Preview','active'),('scope_other','project','other','other','Other','active');
 INSERT INTO identity.actor_node_authorizations(authorization_id,actor_id,node_id,authorization_level,status) VALUES ('auth_preview','actor_preview','node_target',2,'active'),('auth_approver','actor_approver','node_target',5,'active');
 INSERT INTO capabilities.providers(provider_id,provider_key,compact_address,display_name,provider_type,node_id,scope_id,status,created_by_actor_id) VALUES ('prov_preview','system','target@system','Preview','system','node_target','scope_preview','active','actor_preview');
 INSERT INTO capabilities.capability_classes(capability_class_id,namespace,name,display_name,form,default_risk_level,status) VALUES ('cls_preview','system','read','Read','query','low','active');
 INSERT INTO capabilities.capability_endpoints(capability_endpoint_id,provider_id,capability_class_id,endpoint_name,compact_address,form,risk_level,execution_authorization_level,status) VALUES ('endp_preview','prov_preview','cls_preview','read','target@system.read','query','low',3,'active');`)
	req := requestctx.Context{ActorID: "actor_preview", OriginNodeID: "node_origin", ScopeID: "scope_preview"}
	input := PreviewInput{Operation: " capability:target@system.read "}
	reset := func() {
		previewSQL(t, db, `UPDATE identity.actors SET status='active';UPDATE nodes.nodes SET status='active';UPDATE capabilities.providers SET status='active';UPDATE capabilities.capability_endpoints SET status='active';UPDATE identity.actor_node_authorizations SET status='active',authorization_level=2,expires_at=NULL WHERE actor_id='actor_preview';DELETE FROM policy.grants;`)
	}
	grant := func(extra string) {
		previewSQL(t, db, `INSERT INTO policy.grants(grant_id,grant_key,grant_type,granted_by_actor_id,granted_to_actor_id,status,max_risk_level,max_authorization_level,scope_constraints,node_constraints,capability_constraints,max_uses,expires_at) VALUES ('grant_preview','preview','one_shot','actor_approver','actor_preview','active','low',3,'{"scope_id":"scope_preview"}','{"node_id":"node_target"}','{"capability_endpoint_id":"endp_preview"}',1,now()+interval '1 hour');`+extra)
	}
	cases := []struct {
		name, sql, decision, reason string
		grant                       bool
	}{
		{"insufficient_level", "", DecisionApprovalRequired, "actor_level_below_capability_level", false},
		{"sufficient_level", "UPDATE identity.actor_node_authorizations SET authorization_level=3 WHERE actor_id='actor_preview'", DecisionAllow, "actor_allowed_by_level", false},
		{"inactive_actor", "UPDATE identity.actors SET status='disabled' WHERE actor_id='actor_preview'", DecisionDeny, "actor_inactive", false},
		{"inactive_origin", "UPDATE nodes.nodes SET status='quarantined' WHERE node_id='node_origin'", DecisionDeny, "node_inactive", false},
		{"inactive_target", "UPDATE nodes.nodes SET status='retired' WHERE node_id='node_target'", DecisionDeny, "node_inactive", false},
		{"inactive_provider", "UPDATE capabilities.providers SET status='disabled'", DecisionDeny, "provider_inactive", false},
		{"inactive_endpoint", "UPDATE capabilities.capability_endpoints SET status='disabled'", DecisionDeny, "capability_inactive", false},
		{"absent_authorization", "UPDATE identity.actor_node_authorizations SET status='revoked' WHERE actor_id='actor_preview'", DecisionDeny, "actor_not_authorized_on_target_node", false},
		{"expired_authorization", "UPDATE identity.actor_node_authorizations SET expires_at=now()-interval '1 day' WHERE actor_id='actor_preview'", DecisionDeny, "actor_not_authorized_on_target_node", false},
		{"active_grant", "", DecisionAllow, "actor_allowed_by_grant", true},
		{"wrong_scope_grant", `UPDATE policy.grants SET scope_constraints='{"scope_id":"scope_other"}'`, DecisionApprovalRequired, "actor_level_below_capability_level", true},
		{"wrong_node_grant", `UPDATE policy.grants SET node_constraints='{"node_id":"node_origin"}'`, DecisionApprovalRequired, "actor_level_below_capability_level", true},
		{"wrong_capability_grant", `UPDATE policy.grants SET capability_constraints='{"capability_endpoint_id":"endp_other"}'`, DecisionApprovalRequired, "actor_level_below_capability_level", true},
		{"expired_grant", "UPDATE policy.grants SET expires_at=now()-interval '1 day'", DecisionApprovalRequired, "actor_level_below_capability_level", true},
		{"revoked_grant", "UPDATE policy.grants SET status='revoked'", DecisionApprovalRequired, "actor_level_below_capability_level", true},
		{"exhausted_grant", "UPDATE policy.grants SET uses_count=1", DecisionApprovalRequired, "actor_level_below_capability_level", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			if tc.grant {
				grant("")
			}
			if tc.sql != "" {
				previewSQL(t, db, tc.sql)
			}
			before := previewRows(t, db)
			var first PolicyPreview
			for i := 0; i < 3; i++ {
				a, e := svc.Preview(t.Context(), req, input)
				if e != nil {
					t.Fatal(e)
				}
				if a.Decision != tc.decision || a.ReasonCode != tc.reason {
					t.Fatalf("outcome: %+v", a)
				}
				if i == 0 {
					first = a
				} else if !reflect.DeepEqual(first, a) {
					t.Fatal("unstable preview")
				}
			}
			after := previewRows(t, db)
			if !reflect.DeepEqual(before, after) {
				for name, v := range before {
					if v != after[name] {
						t.Errorf("Preview mutated %s", name)
					}
				}
			}
			explanation, e := svc.Explain(t.Context(), req, DecisionInput{Operation: input.Operation})
			if e != nil {
				t.Fatal(e)
			}
			if explanation.Decision.Decision != first.Decision || explanation.Decision.ReasonCode != first.ReasonCode || explanation.Decision.ContextHash != first.ContextHash || explanation.Decision.PolicyDecisionID == "" {
				t.Fatalf("Explain parity: %+v %+v", first, explanation)
			}
			if first.ActorID == nil || *first.ActorID != req.ActorID || first.OriginNodeID == nil || *first.OriginNodeID != req.OriginNodeID || first.TargetNodeID == nil || *first.TargetNodeID != "node_target" || first.RequiredLevel == nil || *first.RequiredLevel != 3 {
				t.Fatal("lost resolved identity")
			}
			if tc.reason == "actor_allowed_by_grant" {
				if first.GrantRef == nil || *first.GrantRef != "grant_preview" || first.GrantExpiresAt == nil {
					t.Fatal("grant identity missing")
				}
			} else if first.GrantRef != nil || first.GrantExpiresAt != nil {
				t.Fatal("fabricated grant")
			}
			if reflect.DeepEqual(after, previewRows(t, db)) {
				t.Fatal("Explain stopped recording decisions/events")
			}
		})
	}
	t.Run("missing_identity_and_target", func(t *testing.T) {
		reset()
		for _, tc := range []struct {
			req        requestctx.Context
			op, reason string
		}{{requestctx.Context{}, input.Operation, "actor_inactive"}, {req, "capability:missing@system.read", "capability_inactive"}, {req, "unsupported", "policy.unsupported_operation"}} {
			a, e := svc.Preview(t.Context(), tc.req, PreviewInput{Operation: tc.op})
			if e != nil || a.ReasonCode != tc.reason {
				t.Fatalf("missing facts: %+v %v", a, e)
			}
			if tc.req.ActorID == "" && (a.ActorID != nil || a.OriginNodeID != nil || a.ActorLevel != nil) {
				t.Fatal("fabricated identity")
			}
			if tc.op != input.Operation && (a.EndpointID != nil || a.RequiredLevel != nil || a.TargetNodeID != nil) {
				t.Fatal("fabricated target")
			}
		}
	})
	t.Run("explicit_scope", func(t *testing.T) {
		reset()
		grant("")
		a, e := svc.Preview(t.Context(), req, PreviewInput{Operation: input.Operation, ScopeRef: "other"})
		if e != nil || a.Decision != DecisionApprovalRequired || a.ScopeID == nil || *a.ScopeID != "scope_other" {
			t.Fatalf("scope: %+v %v", a, e)
		}
	})
	t.Run("Explain_approval_reuse_grant_consumption", func(t *testing.T) {
		reset()
		a, e := svc.Explain(t.Context(), req, DecisionInput{Operation: input.Operation, CreateApprovalRequest: true})
		if e != nil || a.Approval == nil {
			t.Fatalf("approval: %+v %v", a, e)
		}
		b, e := svc.Explain(t.Context(), req, DecisionInput{Operation: input.Operation, CreateApprovalRequest: true})
		if e != nil || b.Approval == nil || b.Approval.ApprovalID != a.Approval.ApprovalID {
			t.Fatal("approval reuse changed")
		}
		previewSQL(t, db, `UPDATE policy.approvals SET expires_at=now()-interval '1 day' WHERE approval_id=$1`, a.Approval.ApprovalID)
		grant("UPDATE policy.grants SET expires_at=now()-interval '1 day'")
		before := previewRows(t, db)
		if _, e = svc.Preview(t.Context(), req, input); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(before, previewRows(t, db)) {
			t.Fatal("Preview expired records")
		}
		previewSQL(t, db, `UPDATE policy.grants SET expires_at=now()+interval '1 hour'`)
		consumed, e := svc.ConsumeGrant(t.Context(), req, GrantConsumeInput{GrantRef: "grant_preview", Reason: "fixture"})
		if e != nil || consumed.UsesCount != 1 || consumed.Status != GrantConsumed {
			t.Fatalf("grant consumption: %+v %v", consumed, e)
		}
	})
	t.Run("authenticated_context_and_Explain_inspection", func(t *testing.T) {
		reset()
		display := req
		display.ActorKey = "approver"
		display.OriginNodeKey = "target"
		a, e := svc.Preview(t.Context(), display, input)
		if e != nil || a.Decision != DecisionApprovalRequired || a.ActorID == nil || *a.ActorID != req.ActorID {
			t.Fatalf("context identity substituted: %+v %v", a, e)
		}
		b, e := svc.Explain(t.Context(), req, DecisionInput{Operation: input.Operation, ActorRef: "approver"})
		if e != nil || b.Decision.Decision != DecisionAllow {
			t.Fatalf("existing Explain inspection changed: %+v %v", b, e)
		}
	})
	t.Run("Explain_approval_issues_grant", func(t *testing.T) {
		reset()
		a, e := svc.Explain(t.Context(), req, DecisionInput{Operation: input.Operation, CreateApprovalRequest: true})
		if e != nil || a.Approval == nil {
			t.Fatal(e)
		}
		approver := req
		approver.ActorID = "actor_approver"
		decided, e := svc.DecideApproval(t.Context(), approver, ApprovalDecisionInput{ApprovalRef: a.Approval.ApprovalID, Decision: ApprovalDecisionApprove, DecidingActorRef: "actor_approver"})
		if e != nil || decided.Grant == nil || decided.Grant.Status != GrantActive {
			t.Fatalf("approval grant: %+v %v", decided, e)
		}
		b, e := svc.Preview(t.Context(), req, input)
		if e != nil || b.ReasonCode != "actor_allowed_by_grant" {
			t.Fatalf("issued grant: %+v %v", b, e)
		}
	})

	t.Run("read_only_snapshot_transaction", func(t *testing.T) {
		// A read-only role cannot write even if the evaluator accidentally grows one.
		reset()
		role := "preview_reader_" + fmt.Sprint(time.Now().UnixNano())
		previewSQL(t, db, `CREATE ROLE `+role)
		t.Cleanup(func() {
			if _, e := db.ExecContext(context.Background(), `DROP OWNED BY `+role+`; DROP ROLE `+role); e != nil {
				t.Error(e)
			}
		})
		rows, e := db.Query(`SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema'`)
		if e != nil {
			t.Fatal(e)
		}
		schemas := []string{}
		for rows.Next() {
			var n string
			if e = rows.Scan(&n); e != nil {
				t.Fatal(e)
			}
			schemas = append(schemas, n)
		}
		rows.Close()
		for _, n := range schemas {
			previewSQL(t, db, `GRANT USAGE ON SCHEMA "`+n+`" TO `+role+`; GRANT SELECT ON ALL TABLES IN SCHEMA "`+n+`" TO `+role)
		}
		// Serialize onto one connection to bind SET ROLE to the Service pool.
		db.SetMaxOpenConns(1)
		previewSQL(t, db, `SET ROLE `+role)
		defer previewSQL(t, db, `RESET ROLE`)
		if _, e = svc.Preview(t.Context(), req, input); e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Explain(t.Context(), req, DecisionInput{Operation: input.Operation}); e == nil {
			t.Fatal("read-only role unexpectedly wrote Explain")
		}
	})
}
