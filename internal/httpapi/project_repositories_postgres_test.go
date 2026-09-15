package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/requestctx"
)

func TestProjectRepositoryAuthorizationBindsCanonicalProjectBeforeObservationPostgres(t *testing.T) {
	sqlDB, bootstrapRequest := projectRepositoryAuthorizationDatabase(t)
	ctx := context.Background()
	suffix := strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), ids.ProjectPrefix+"_"))
	collisionRef := "authz-collision-" + suffix
	higherProjectID := ids.NewProjectID()
	lowerProjectID := ids.NewProjectID()
	higherScopeID := ids.NewScopeID()
	lowerScopeID := ids.NewScopeID()
	actorID := ids.NewActorID()
	authorizationID := ids.NewActorNodeAuthorizationID()
	lowerMembershipID := ids.NewProjectMembershipID()
	higherMembershipID := ids.NewProjectMembershipID()
	actorKey := "agent:repo-authz-" + suffix

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO identity.actors (
			actor_id, actor_key, display_name, actor_kind, home_node_id,
			default_scope_id, status, metadata
		)
		VALUES ($1, $2, 'Repository authorization fixture', 'agent', $3, $4, 'active', '{"test":"project_repository_authorization"}'::jsonb)
	`, actorID, actorKey, bootstrapRequest.OriginNodeID, bootstrapRequest.ScopeID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO identity.actor_node_authorizations (
			authorization_id, actor_id, node_id, authorization_level, status, metadata
		)
		VALUES ($1, $2, $3, 2, 'active', '{"test":"project_repository_authorization"}'::jsonb)
	`, authorizationID, actorID, bootstrapRequest.OriginNodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO scopes.scopes (
			scope_id, scope_type, scope_key, slug, display_name, owner_actor_id,
			home_node_id, status, created_by_actor_id, metadata
		)
		VALUES
			($1, 'project', $2, $3, 'Higher priority authorization fixture', $4, $5, 'active', $4, '{"test":"project_repository_authorization"}'::jsonb),
			($6, 'project', $7, $8, 'Lower priority authorization fixture', $4, $5, 'active', $4, '{"test":"project_repository_authorization"}'::jsonb)
	`,
		higherScopeID, "project-authz-high-"+suffix, "authz-high-scope-"+suffix,
		actorID, bootstrapRequest.OriginNodeID,
		lowerScopeID, "project-authz-low-"+suffix, collisionRef,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO projects.projects (
			project_id, project_scope_id, slug, name, owner_actor_id,
			created_by_actor_id, home_node_id, status, metadata
		)
		VALUES
			($1, $2, $3, 'Higher priority authorization fixture', $4, $4, $5, 'active', '{"test":"project_repository_authorization"}'::jsonb),
			($6, $7, $8, 'Lower priority authorization fixture', $4, $4, $5, 'active', '{"test":"project_repository_authorization"}'::jsonb)
	`,
		higherProjectID, higherScopeID, collisionRef, actorID, bootstrapRequest.OriginNodeID,
		lowerProjectID, lowerScopeID, "authz-low-project-"+suffix,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO projects.project_memberships (
			project_membership_id, project_id, scope_id, actor_id, role, status,
			authorization_hint, assigned_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, 'agent', 'active', 2, $4, '{"test":"project_repository_authorization"}'::jsonb)
	`, lowerMembershipID, lowerProjectID, lowerScopeID, actorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = sqlDB.ExecContext(cleanup, `DELETE FROM projects.project_memberships WHERE project_membership_id IN ($1, $2)`, lowerMembershipID, higherMembershipID)
		_, _ = sqlDB.ExecContext(cleanup, `DELETE FROM projects.projects WHERE project_id IN ($1, $2)`, higherProjectID, lowerProjectID)
		_, _ = sqlDB.ExecContext(cleanup, `DELETE FROM scopes.scopes WHERE scope_id IN ($1, $2)`, higherScopeID, lowerScopeID)
		_, _ = sqlDB.ExecContext(cleanup, `DELETE FROM identity.actor_node_authorizations WHERE authorization_id = $1`, authorizationID)
		_, _ = sqlDB.ExecContext(cleanup, `DELETE FROM identity.actors WHERE actor_id = $1`, actorID)
	})

	request := requestctx.Context{ActorID: actorID, OriginNodeID: bootstrapRequest.OriginNodeID}
	observer := &projectRepositoryObserverStub{projection: projectstate.ProjectProjection{
		SchemaVersion: projectstate.SchemaVersion,
		Project: projectstate.ProjectIdentityProjection{
			ProjectID: higherProjectID,
			Slug:      collisionRef,
			Lifecycle: "active",
		},
		Source:     projectstate.SourceProjection{Posture: projectstate.SourcePostureNotRegistered},
		Members:    []projectstate.RepositoryProjection{},
		ObservedAt: time.Now().UTC(),
	}}
	handler := NewServer(Services{ProjectRepos: ProjectRepositoryServices{
		State:      observer,
		Authorizer: SQLProjectRepositoryReadAuthorizer{DB: sqlDB, LocalNodeRef: bootstrapRequest.OriginNodeID},
		RequestResolver: func(context.Context, string) (requestctx.Context, error) {
			return request, nil
		},
	}}, slog.Default()).Handler()

	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-namespace collision status=%d body=%s", denied.Code, denied.Body.String())
	}
	if observer.calls != 0 {
		t.Fatalf("cross-namespace collision observed before exact-project authorization: calls=%d ref=%q", observer.calls, observer.ref)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO projects.project_memberships (
			project_membership_id, project_id, scope_id, actor_id, role, status,
			authorization_hint, assigned_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, 'agent', 'active', 2, $4, '{"test":"project_repository_authorization"}'::jsonb)
	`, higherMembershipID, higherProjectID, higherScopeID, actorID); err != nil {
		t.Fatal(err)
	}
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if allowed.Code != http.StatusOK {
		t.Fatalf("exact selected project status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	if observer.calls != 1 || observer.ref != higherProjectID {
		t.Fatalf("exact selected project observation calls=%d ref=%q want=%q", observer.calls, observer.ref, higherProjectID)
	}

	if _, err := sqlDB.ExecContext(ctx, `UPDATE projects.projects SET status = 'archived' WHERE project_id = $1`, higherProjectID); err != nil {
		t.Fatal(err)
	}
	observer.projection.Project.Lifecycle = "archived"
	archived := httptest.NewRecorder()
	handler.ServeHTTP(archived, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos/status", nil))
	if archived.Code != http.StatusOK {
		t.Fatalf("archived exact selected project status=%d body=%s", archived.Code, archived.Body.String())
	}
	if observer.calls != 2 || observer.ref != higherProjectID {
		t.Fatalf("archived observation calls=%d ref=%q want=%q", observer.calls, observer.ref, higherProjectID)
	}

	if _, err := sqlDB.ExecContext(ctx, `UPDATE projects.project_memberships SET expires_at = now() - interval '1 minute' WHERE project_membership_id = $1`, higherMembershipID); err != nil {
		t.Fatal(err)
	}
	expired := httptest.NewRecorder()
	handler.ServeHTTP(expired, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if expired.Code != http.StatusForbidden || observer.calls != 2 {
		t.Fatalf("expired membership status=%d observer_calls=%d body=%s", expired.Code, observer.calls, expired.Body.String())
	}

	if _, err := sqlDB.ExecContext(ctx, `UPDATE projects.project_memberships SET expires_at = NULL, status = 'disabled' WHERE project_membership_id = $1`, higherMembershipID); err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRecorder()
	handler.ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if disabled.Code != http.StatusForbidden || observer.calls != 2 {
		t.Fatalf("disabled membership status=%d observer_calls=%d body=%s", disabled.Code, observer.calls, disabled.Body.String())
	}

	if _, err := sqlDB.ExecContext(ctx, `UPDATE projects.project_memberships SET status = 'active' WHERE project_membership_id = $1`, higherMembershipID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE identity.actors SET status = 'disabled' WHERE actor_id = $1`, actorID); err != nil {
		t.Fatal(err)
	}
	disabledActor := httptest.NewRecorder()
	handler.ServeHTTP(disabledActor, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if disabledActor.Code != http.StatusForbidden || observer.calls != 2 {
		t.Fatalf("disabled actor status=%d observer_calls=%d body=%s", disabledActor.Code, observer.calls, disabledActor.Body.String())
	}

	if _, err := sqlDB.ExecContext(ctx, `UPDATE identity.actors SET status = 'active' WHERE actor_id = $1`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `UPDATE identity.actor_node_authorizations SET expires_at = now() - interval '1 minute' WHERE authorization_id = $1`, authorizationID); err != nil {
		t.Fatal(err)
	}
	expiredAuthorization := httptest.NewRecorder()
	handler.ServeHTTP(expiredAuthorization, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if expiredAuthorization.Code != http.StatusForbidden || observer.calls != 2 {
		t.Fatalf("expired actor-node authorization status=%d observer_calls=%d body=%s", expiredAuthorization.Code, observer.calls, expiredAuthorization.Body.String())
	}

	if _, err := sqlDB.ExecContext(ctx, `UPDATE identity.actor_node_authorizations SET expires_at = NULL, status = 'disabled' WHERE authorization_id = $1`, authorizationID); err != nil {
		t.Fatal(err)
	}
	disabledAuthorization := httptest.NewRecorder()
	handler.ServeHTTP(disabledAuthorization, httptest.NewRequest(http.MethodGet, "/v1/projects/"+collisionRef+"/repos", nil))
	if disabledAuthorization.Code != http.StatusForbidden || observer.calls != 2 {
		t.Fatalf("disabled actor-node authorization status=%d observer_calls=%d body=%s", disabledAuthorization.Code, observer.calls, disabledAuthorization.Body.String())
	}
}

func projectRepositoryAuthorizationDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("LOOM_TEST_DB_URL"))
	if databaseURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set; project repository authorization regression requires a dedicated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := migrations.Up(ctx, databaseURL, projectRepositoryAuthorizationMigrationsDir(t)); err != nil {
		t.Fatalf("migrate disposable project repository authorization database: %v", err)
	}
	sqlDB, err := db.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var databaseName string
	if err := sqlDB.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if databaseName == "postgres" || databaseName == "template0" || databaseName == "template1" {
		t.Fatalf("LOOM_TEST_DB_URL points to reserved database %q", databaseName)
	}
	if _, err := bootstrap.NewService(sqlDB).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	request, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_project_repository_authorization")
	if err != nil {
		t.Fatal(err)
	}
	return sqlDB, request
}

func projectRepositoryAuthorizationMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
