package hermesschedules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceSelectionAndLegacyJSON(t *testing.T) {
	for _, tc := range []struct {
		name, operation, profile, box string
		valid                         bool
	}{
		{"personal", "workspace_info", "mina", "", true},
		{"company", "workspace_create", "tyler", "box_company", true},
		{"discover company", "workspace_info", "", "box_company", true},
		{"unbound company", "workspace_info", "tyler", "", false},
		{"unbound discovery", "workspace_info", "", "", false},
		{"action needs profile", "workspace_create", "", "box_company", false},
		{"detail needs profile", "workspace_detail", "", "box_company", false},
		{"invalid box", "workspace_info", "tyler", "company", false},
		{"invalid profile", "workspace_info", "../tyler", "box_company", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := WorkspaceRequest{Operation: tc.operation, Profile: tc.profile, BoxID: tc.box}
			if validWorkspaceSelection(req) != tc.valid {
				t.Fatal("incorrect workspace selection")
			}
			raw, err := json.Marshal(req)
			if err != nil || strings.Contains(string(raw), `"box_id"`) != (tc.box != "") {
				t.Fatalf("BoxID omitempty contract lost: %s %v", raw, err)
			}
			if !tc.valid {
				_, err = (ProjectClient{Source: Source{Host: "main", Profile: "mina", Revision: PlatformRevision}}).WorkspaceCall(context.Background(), req)
				if err != ErrSource {
					t.Fatalf("invalid selection reached transport: %v", err)
				}
			}
		})
	}
	for _, source := range []Source{
		{Host: "main", Profile: "tyler", Revision: PlatformRevision},
		{Host: "main", Profile: "mina", Revision: NativeRevision},
		{Profile: "mina", Revision: PlatformRevision},
	} {
		_, err := (ProjectClient{Source: source}).WorkspaceCall(context.Background(), WorkspaceRequest{Operation: "workspace_info", Profile: "tyler", BoxID: "box_company"})
		if err != ErrSource {
			t.Fatalf("invalid owner source reached transport: %v", err)
		}
	}
}

func TestWorkspaceResponseRequiresParentMINA(t *testing.T) {
	source := Source{Host: "main", Profile: "mina", Revision: PlatformRevision}
	jobs := json.RawMessage(`{"profile":"tyler","box_id":"box_company","timezone":"America/Sao_Paulo","profiles":[{"id":"tyler","name":"Tyler","timezone":"America/Sao_Paulo","box_id":"box_company"}],"items":[]}`)
	raw, _ := json.Marshal(map[string]any{"source": source, "jobs": jobs})
	got, err := decodeWorkspaceResponse(raw, source)
	if err != nil || string(got) != string(jobs) {
		t.Fatalf("Company result lost: %s %v", got, err)
	}
	for _, returned := range []Source{
		{Host: "main", Profile: "tyler", Revision: PlatformRevision},
		{Host: "other", Profile: "mina", Revision: PlatformRevision},
		{Host: "main", Profile: "mina", Revision: NativeRevision},
	} {
		raw, _ := json.Marshal(map[string]any{"source": returned, "jobs": jobs})
		if _, err := decodeWorkspaceResponse(raw, source); err != ErrMalformed {
			t.Fatalf("non-parent source admitted: %v", err)
		}
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"jobs":`, `"private":"not allowed","jobs":`, 1),
		`{"source":{"host":"main","profile":"mina","revision":"` + PlatformRevision + `"},"jobs":null}`,
		string(raw) + string(raw),
	} {
		if _, err := decodeWorkspaceResponse([]byte(invalid), source); err != ErrMalformed {
			t.Fatalf("invalid envelope admitted: %v", err)
		}
	}
}
