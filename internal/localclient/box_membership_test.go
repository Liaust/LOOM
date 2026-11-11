package localclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
)

func TestBoxMembershipClientExactAdministrativeContext(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	sourceID := "notes_source_root_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.RawQuery != "all_boxes=true" || r.Header.Get("X-Loom-Box") != "" {
			t.Errorf("administrative context conflicted or duplicated: %s %s %v", r.Method, r.URL, r.Header)
		}
		switch r.URL.Path {
		case "/v1/boxes/membership/inventory":
			var input struct {
				Kind string `json:"kind"`
				projects.BoxMembershipInventoryInput
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Kind != "projects" || input.After != projectID || input.Limit != 2 {
				t.Errorf("inventory bounds lost: %+v", input)
			}
			response.WriteJSON(w, 200, response.Success("membership", BoxMembershipInventoryResult{Kind: "projects", CatalogDigest: digest, BoxMembershipInventoryPage: projects.BoxMembershipInventoryPage{IDs: []string{projectID}}}))
		case "/v1/boxes/membership/projects":
			var input projects.BoxMembershipReconcileInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || !reflect.DeepEqual(input.ProjectIDs, []string{projectID}) || !input.Apply || input.CatalogDigest != digest || input.Evidence[projectID] != digest {
				t.Errorf("project evidence lost: %+v", input)
			}
			response.WriteJSON(w, 200, response.Success("membership", projects.BoxMembershipReconcileResult{CatalogDigest: digest, Changes: []projects.BoxMembershipChange{{ProjectID: projectID, Status: "unchanged", EvidenceDigest: digest, HomeBoxID: "box_personal"}}}))
		case "/v1/boxes/membership/sources":
			var input knowledge.SourceBoxMembershipInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || !reflect.DeepEqual(input.SourceRootIDs, []string{sourceID}) || !input.Apply || input.CatalogDigest != digest || input.Evidence[sourceID] != digest {
				t.Errorf("source evidence lost: %+v", input)
			}
			response.WriteJSON(w, 200, response.Success("membership", knowledge.SourceBoxMembershipResult{CatalogDigest: digest, Changes: []knowledge.SourceBoxMembershipChange{{SourceRootID: sourceID, Status: "unassigned", EvidenceDigest: digest}}}))
		default:
			t.Errorf("unexpected route: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client = client.WithBoxContext("company", false)
	if _, err := client.BoxMembershipInventory(t.Context(), "membership", "projects", projects.BoxMembershipInventoryInput{After: projectID, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	project, err := client.ReconcileProjectBoxMembership(t.Context(), "membership", projects.BoxMembershipReconcileInput{ProjectIDs: []string{projectID}, Apply: true, CatalogDigest: digest, Evidence: map[string]string{projectID: digest}})
	if err != nil || project.Data.Changes[0].Status != "unchanged" {
		t.Fatalf("project status changed: %+v %v", project, err)
	}
	source, err := client.ReconcileSourceBoxMembership(t.Context(), "membership", knowledge.SourceBoxMembershipInput{SourceRootIDs: []string{sourceID}, Apply: true, CatalogDigest: digest, Evidence: map[string]string{sourceID: digest}})
	if err != nil || source.Data.Changes[0].Status != "unassigned" {
		t.Fatalf("source status changed: %+v %v", source, err)
	}
	if requests != 3 || client.boxContext != "company" || client.allBoxes {
		t.Fatal("administrative invocation mutated shared client context")
	}
}
