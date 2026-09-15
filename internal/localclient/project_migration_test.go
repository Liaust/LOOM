package localclient

import (
	"encoding/json"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func migrationClientFixture() pc.DeclarationMigrationAssessment {
	digest := "sha256:" + strings.Repeat("a", 64)
	unknown := pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: digest}
	a := pc.DeclarationMigrationAssessment{SchemaVersion: pc.DeclarationMigrationAssessmentSchema, ProjectID: "project", OwnerNode: "main", LocationRevision: digest, SnapshotRevision: digest, State: "incomplete", Consistency: "observed_reads_not_publication_fence", Issues: []pc.MigrationAssessmentIssue{}, Preview: pc.PreviewDeclarationMigration(pc.DeclarationMigrationInput{}), Readiness: pc.DeclarationReadiness{Desired: unknown, Queued: unknown, Applied: unknown, Processing: unknown, Healthy: unknown, Protected: unknown, Verified: unknown}}
	for _, f := range []string{"project", "membership", "watches", "coverage", "legacy", "evidence"} {
		a.Families = append(a.Families, pc.MigrationAssessmentFamily{Family: f, Completeness: "unavailable", Revision: digest, RevisionKind: "observed_content_digest"})
	}
	return a
}
func TestDeclarationMigrationClientClosedResponse(t *testing.T) {
	for _, name := range []string{"valid", "wrong_schema", "candidate_mismatch", "duplicate_family", "path_identity", "readiness", "unknown_field"} {
		t.Run(name, func(t *testing.T) {
			a := migrationClientFixture()
			switch name {
			case "wrong_schema":
				a.SchemaVersion = pc.DeclarationStatusSchemaV05
			case "candidate_mismatch":
				a.State = "eligible"
			case "duplicate_family":
				a.Families[1].Family = "project"
			case "path_identity":
				a.OwnerNode = "/private/node"
			case "readiness":
				a.Readiness.Healthy.State = pc.DeclarationFactState("true")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/projects/a b/declaration-status" || r.URL.Query().Get("conversion_preview") != "true" || r.URL.Query().Get("node_ref") != "selected" {
					t.Error("route mismatch")
				}
				raw, _ := json.Marshal(response.Success("fixture", a))
				if name == "unknown_field" {
					raw = []byte(strings.Replace(string(raw), `"state":"incomplete"`, `"state":"incomplete","private_path":"/private"`, 1))
				}
				w.Write(raw)
			}))
			defer server.Close()
			client, _ := NewHTTP(server.URL)
			_, e := client.GetProjectConversionAssessment(t.Context(), "request", "a b", "selected")
			if (e == nil) != (name == "valid") {
				t.Fatalf("case %s error %v", name, e)
			}
		})
	}
}
