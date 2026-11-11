package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
)

func executeMembershipCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	opts := &options{}
	root := newRootCommand(opts)
	parent, _, err := root.Find([]string{"box"})
	if err != nil {
		t.Fatal(err)
	}
	registered := false
	for _, child := range parent.Commands() {
		registered = registered || child.Name() == "membership"
	}
	if !registered {
		parent.AddCommand(newBoxMembershipCommand(opts))
	}
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), err
}

func TestBoxMembershipCLIInventoryExactPage(t *testing.T) {
	useIsolatedCLIBoxState(t)
	t.Setenv("LOOM_BOX_ID", "company")
	for _, kind := range []string{"projects", "sources"} {
		t.Run(kind, func(t *testing.T) {
			id := ids.NewProjectID()
			if kind == "sources" {
				id = ids.NewNotesSourceRootID()
			}
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/boxes/membership/inventory" || r.URL.RawQuery != "all_boxes=true" || r.Header.Get("X-Loom-Box") != "" {
					t.Errorf("wrong administrative request: %s %s %v", r.Method, r.URL, r.Header)
				}
				var input struct {
					Kind string `json:"kind"`
					projects.BoxMembershipInventoryInput
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.Kind != kind || input.After != id || input.Limit != 2 {
					t.Errorf("inventory selection changed: %+v", input)
				}
				response.WriteJSON(w, 200, response.Success("membership", localclient.BoxMembershipInventoryResult{Kind: kind, CatalogDigest: "sha256:" + strings.Repeat("a", 64), BoxMembershipInventoryPage: projects.BoxMembershipInventoryPage{IDs: []string{id}, NextAfter: id}}))
			})
			defer stop()
			out, err := executeMembershipCLI(t, "--socket", socket, "--box", "personal", "--json", "box", "membership", "inventory", "--kind", kind, "--after", id, "--limit", "2")
			var page localclient.BoxMembershipInventoryResult
			if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.IDs) != 1 || page.NextAfter != id {
				t.Fatalf("inventory output: %s %v", out, err)
			}
		})
	}
}

func TestBoxMembershipCLIPlanApplyExactEvidenceRoundTrip(t *testing.T) {
	useIsolatedCLIBoxState(t)
	t.Setenv("LOOM_BOX_ID", "company")
	for _, kind := range []string{"projects", "sources"} {
		t.Run(kind, func(t *testing.T) {
			selected := []string{ids.NewProjectID(), ids.NewProjectID(), ids.NewProjectID()}
			if kind == "sources" {
				selected = []string{ids.NewNotesSourceRootID(), ids.NewNotesSourceRootID(), ids.NewNotesSourceRootID()}
			}
			catalog := "sha256:" + strings.Repeat("a", 64)
			evidence := map[string]string{selected[0]: "sha256:" + strings.Repeat("b", 64), selected[1]: "sha256:" + strings.Repeat("c", 64), selected[2]: "sha256:" + strings.Repeat("d", 64)}
			calls := 0
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/boxes/membership/"+kind || r.URL.RawQuery != "all_boxes=true" || r.Header.Get("X-Loom-Box") != "" {
					t.Errorf("unexpected effect or context: %s %s", r.Method, r.URL)
				}
				var input struct {
					ProjectIDs []string          `json:"project_ids"`
					SourceIDs  []string          `json:"source_root_ids"`
					Apply      bool              `json:"apply"`
					Catalog    string            `json:"catalog_digest"`
					Evidence   map[string]string `json:"evidence"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				actual := input.ProjectIDs
				if kind == "sources" {
					actual = input.SourceIDs
				}
				if !reflect.DeepEqual(actual, selected) || input.Apply != (calls == 2) || calls > 2 {
					t.Errorf("exact IDs or apply mode changed: %+v", input)
				}
				if input.Apply && (input.Catalog != catalog || !reflect.DeepEqual(input.Evidence, evidence)) {
					t.Errorf("dry-run evidence was not replayed exactly: %+v", input)
				}
				firstStatus := "would_assign"
				if input.Apply {
					firstStatus = "assigned"
				}
				if kind == "projects" {
					result := projects.BoxMembershipReconcileResult{CatalogDigest: catalog, Changes: []projects.BoxMembershipChange{
						{ProjectID: selected[0], EvidenceDigest: evidence[selected[0]], Status: firstStatus, HomeBoxID: "box_personal"},
						{ProjectID: selected[1], EvidenceDigest: evidence[selected[1]], Status: "unchanged", HomeBoxID: "box_personal"},
						{ProjectID: selected[2], EvidenceDigest: evidence[selected[2]], Status: "unassigned", Reason: "no source evidence"},
					}}
					response.WriteJSON(w, 200, response.Success("membership", result))
				} else {
					result := knowledge.SourceBoxMembershipResult{CatalogDigest: catalog, Changes: []knowledge.SourceBoxMembershipChange{
						{SourceRootID: selected[0], EvidenceDigest: evidence[selected[0]], Status: firstStatus, BoxID: "box_personal"},
						{SourceRootID: selected[1], EvidenceDigest: evidence[selected[1]], Status: "unchanged", BoxID: "box_personal"},
						{SourceRootID: selected[2], EvidenceDigest: evidence[selected[2]], Status: "unassigned", Reason: "no source evidence"},
					}}
					response.WriteJSON(w, 200, response.Success("membership", result))
				}
			})
			defer stop()
			base := []string{"--socket", socket, "--json", "box", "membership"}
			out, err := executeMembershipCLI(t, append(append([]string{}, base...), "plan", "--kind", kind, "--ids", strings.Join(selected, ","))...)
			if err != nil {
				t.Fatalf("plan: %s %v", out, err)
			}
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, []byte(out), 0600); err != nil {
				t.Fatal(err)
			}
			args := append(append([]string{}, base...), "apply", "--kind", kind, "--plan-file", path)
			if _, err := executeMembershipCLI(t, args...); err == nil || !strings.Contains(err.Error(), "--yes") || calls != 1 {
				t.Fatal("apply without confirmation reached API")
			}
			out, err = executeMembershipCLI(t, append(args, "--yes")...)
			if err != nil || calls != 2 || strings.Count(out, `"status":"assigned"`) != 1 || !strings.Contains(out, `"status":"unchanged"`) || !strings.Contains(out, `"status":"unassigned"`) {
				t.Fatalf("apply falsely assigned unchanged/unassigned rows: %s %v calls=%d", out, err, calls)
			}
		})
	}
}

func TestBoxMembershipCLIRejectsInvalidPlansAndSelections(t *testing.T) {
	valid := projectBoxMembershipPlan{Kind: "projects", DryRun: true, BoxMembershipReconcileResult: projects.BoxMembershipReconcileResult{CatalogDigest: "sha256:" + strings.Repeat("a", 64), Changes: []projects.BoxMembershipChange{{ProjectID: ids.NewProjectID(), EvidenceDigest: "sha256:" + strings.Repeat("b", 64), Status: "unassigned"}}}}
	good, _ := json.Marshal(valid)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong-kind", func(v map[string]any) { v["kind"] = "sources" }},
		{"not-dry-run", func(v map[string]any) { v["dry_run"] = false }},
		{"no-catalog", func(v map[string]any) { delete(v, "catalog_digest") }},
		{"unknown", func(v map[string]any) { v["raw_content"] = "must not be accepted" }},
		{"empty", func(v map[string]any) { v["changes"] = []any{} }},
		{"duplicate-id", func(v map[string]any) { row := v["changes"].([]any)[0]; v["changes"] = []any{row, row} }},
		{"bad-digest", func(v map[string]any) { v["changes"].([]any)[0].(map[string]any)["evidence_digest"] = "changed" }},
		{"already-applied", func(v map[string]any) { v["changes"].([]any)[0].(map[string]any)["status"] = "assigned" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(good, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			raw, _ := json.Marshal(value)
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := readBoxMembershipPlan(path, "projects"); err == nil {
				t.Fatal("invalid dry-run evidence accepted")
			}
		})
	}
	for _, raw := range [][]byte{append(append([]byte{}, good...), []byte(` {}`)...), bytes.Repeat([]byte(" "), (64<<10)+1), []byte(`{"kind":"projects","kind":"sources"}`)} {
		path := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := readBoxMembershipPlan(path, "projects"); err == nil {
			t.Fatal("trailing, oversized or duplicate-key JSON accepted")
		}
	}
	for _, values := range [][]string{nil, {"my-project"}, {ids.NewNotesSourceRootID()}, {valid.Changes[0].ProjectID, valid.Changes[0].ProjectID}, make([]string, 101)} {
		if err := validateBoxMembershipSelection("projects", values); err == nil {
			t.Fatal("non-exact selection accepted")
		}
	}
}

func TestBoxMembershipCLIRejectsChangedResponseSelection(t *testing.T) {
	one, two := ids.NewProjectID(), ids.NewProjectID()
	for _, returned := range [][]string{nil, {two}, {one, two}, {one, one}} {
		if err := validateBoxMembershipResponse("projects", []string{one}, returned); err == nil {
			t.Fatal("response silently changed the exact requested selection")
		}
	}
	if err := validateBoxMembershipResponse("projects", []string{one, two}, []string{two, one}); err != nil {
		t.Fatalf("server ID ordering incorrectly treated as a broader selection: %v", err)
	}
}
