package projects

import (
 "context"
 "database/sql"
 "database/sql/driver"
 "encoding/json"
 "errors"
 "testing"
)

func TestProjectFilesystemFenceBlocksRuntimeAndMutation(t *testing.T){
 metadata:=json.RawMessage(`{"workspace_filesystem_operation":{"id":"workspace_path_operation_exact"}}`)
 if err:=EnsureProjectMutable(Project{ProjectID:"project_active",Status:"active",Metadata:metadata},"script","run");!errors.Is(err,ErrProjectArchiveInProgress){t.Fatal(err)}
 store:=newRuntimeGuardFakeStore(t);db:=sql.OpenDB(runtimeGuardFakeConnector{store:store});defer db.Close()
 store.expect("FROM projects.projects p JOIN scopes.scopes",func(args []driver.NamedValue){requireRuntimeGuardArgs(t,args,"project_active")},[]driver.Value{"project_active","active-project","active","scope_active","active",[]byte(`{}`),[]byte(metadata)})
 if err:=EnsureRuntimeActive(context.Background(),db,RuntimeRef{ProjectID:"project_active"});!errors.Is(err,ErrProjectArchiveInProgress){t.Fatal(err)}
 store.requireDone()
}
