package hermesschedules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProjectOwnershipInventoryPrivacy(t *testing.T) {
	raw := []byte(`{"jobs":[{"id":"aabbccddeeff","prompt":"SECRET","origin":{"private":"SECRET","loom_project_declaration":{"project_id":"project_test","resource":"review","profile":"mina","retired":true,"receipt":{"token":"SECRET"}}}},{"id":"unowned"}]}`)
	jobs, _, err := parse(context.Background(), raw)
	if err != nil || jobs[0].Project == nil || !jobs[0].Project.Retired || jobs[1].Project != nil {
		t.Fatalf("%+v %v", jobs, err)
	}
	out, _ := json.Marshal(jobs)
	if strings.Contains(string(out), "SECRET") {
		t.Fatal("private native fields escaped")
	}
}
func TestProjectResponseIdentityAndReceipt(t *testing.T) {
	source := Source{Host: "main", Profile: "mina", Revision: NativeRevision}
	hash := "sha256:" + strings.Repeat("a", 64)
	j := ProjectJob{ID: "aabbccddeeff", ProjectID: "project_test", Resource: "review", Profile: "mina", Revision: hash, CommittedRevision: hash, DesiredHash: hash, InputHash: hash, BeforeRevision: "absent", Token: "token"}
	req := ProjectRequest{Source: source, ProjectID: "project_test", Resource: "review"}
	raw, _ := json.Marshal(ProjectResponse{Source: source, Jobs: []ProjectJob{j}})
	if _, err := decodeProjectResponse(raw, req); err != nil {
		t.Fatal(err)
	}
	j.ProjectID = "project_other"
	raw, _ = json.Marshal(ProjectResponse{Source: source, Jobs: []ProjectJob{j}})
	if _, err := decodeProjectResponse(raw, req); err == nil {
		t.Fatal("cross-project response accepted")
	}
}

func TestProjectTerminalTimingError(t *testing.T) {
	source := Source{Host: "main", Profile: "mina", Revision: NativeRevision}
	raw, _ := json.Marshal(ProjectResponse{Source: source, Error: "terminal_requires_native_resume"})
	if _, err := decodeProjectResponse(raw, ProjectRequest{Source: source}); !errors.Is(err, ErrTerminalRequiresResume) {
		t.Fatalf("terminal timing edit lost actionable native-resume error: %v", err)
	}
}
