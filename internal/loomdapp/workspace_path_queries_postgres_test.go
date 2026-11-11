package loomdapp

import (
 "context"
 "database/sql"
 "net/url"
 "os"
 "strings"
 "testing"
 "time"
)

func TestWorkspacePathDependencyQueriesPostgres(t *testing.T){
 raw:=os.Getenv("LOOM_WORKSPACE_PATH_TEST_DB_URL");if raw==""{t.Skip("dedicated Main PostgreSQL check only")}
 u,err:=url.Parse(raw);if err!=nil || !strings.HasPrefix(u.Path,"/loom_test_advanced_") || u.Host!="" {t.Fatal("refusing non-disposable database")}
 database,err:=sql.Open("pgx",raw);if err!=nil{t.Fatal(err)};defer database.Close()
 ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
 for _,owner:=range workspacePathRuntimeQueries{
  query:=`WITH target AS (SELECT $1::text AS project_id,$2::text AS scope_id,$3::text AS root,$4::text AS physical_root) `+owner.query+` ORDER BY 1 LIMIT 1`
  statement,err:=database.PrepareContext(ctx,query);if err!=nil{t.Errorf("%s: %v",owner.owner,err);continue};statement.Close()
 }
}
