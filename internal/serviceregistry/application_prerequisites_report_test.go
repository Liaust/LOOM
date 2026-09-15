package serviceregistry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestApplicationPrerequisiteQueryClosed(t *testing.T) {
	valid := `{"schema_version":"application.prerequisites.v1","owner":{"project_id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV","node_id":"node_01ARZ3NDEKTSV4RRFFQ69G5FAV","resource":"app"}}`
	if _, err := DecodeApplicationPrerequisiteQuery([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"missing": `{}`, "null": `null`, "array": `[]`, "trailing": valid + ` {}`,
		"unknown":        strings.Replace(valid, `"owner":`, `"actor_id":"root","owner":`, 1),
		"duplicate":      strings.Replace(valid, `"resource":"app"`, `"resource":"evil","resource":"app"`, 1),
		"nested_unknown": strings.Replace(valid, `"resource":"app"`, `"resource":"app","path":"/secret"`, 1),
		"case_alias":     strings.Replace(valid, `"owner"`, `"Owner"`, 1),
		"schema":         strings.Replace(valid, "application.prerequisites.v1", "application.runtime.v1", 1),
		"node":           strings.Replace(valid, "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", "main", 1),
		"resource":       strings.Replace(valid, `"resource":"app"`, `"resource":"../other"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeApplicationPrerequisiteQuery([]byte(raw)); err == nil {
				t.Fatal("invalid query accepted")
			}
		})
	}
}

func TestApplicationPrerequisiteReportTypedValidation(t *testing.T) {
	r, q, policy := applicationPrerequisitesFixture(t)
	snapshot, err := r.QueryPrerequisites(t.Context(), policy.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeApplicationPrerequisiteSnapshot(raw, q); err != nil {
		t.Fatal("real E3a snapshot refused", err)
	}
	for name, mutate := range map[string]func(*ApplicationPrerequisiteSnapshot){
		"schema":                  func(s *ApplicationPrerequisiteSnapshot) { s.SchemaVersion = "application.runtime.v1" },
		"owner":                   func(s *ApplicationPrerequisiteSnapshot) { s.Owner.Resource = "other" },
		"repository":              func(s *ApplicationPrerequisiteSnapshot) { s.RepositoryID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV" },
		"identity":                func(s *ApplicationPrerequisiteSnapshot) { s.IdentityRevision = "asserted" },
		"timestamp":               func(s *ApplicationPrerequisiteSnapshot) { s.CollectedAt = time.Time{} },
		"data_null":               func(s *ApplicationPrerequisiteSnapshot) { s.Data = nil },
		"credentials_null":        func(s *ApplicationPrerequisiteSnapshot) { s.Credentials = nil },
		"publication_partial":     func(s *ApplicationPrerequisiteSnapshot) { s.Publication.Present = true },
		"installation_partial":    func(s *ApplicationPrerequisiteSnapshot) { s.Installation.Present = true },
		"publication_absent_data": func(s *ApplicationPrerequisiteSnapshot) { s.Publication.Revision = "hidden" },
		"path_reference": func(s *ApplicationPrerequisiteSnapshot) {
			s.Data["bad"] = ApplicationPrerequisiteData{BindingRef: "/private/path", Availability: "missing", Custody: "unknown"}
		},
		"credential_value": func(s *ApplicationPrerequisiteSnapshot) {
			s.Credentials["/private/credential"] = ApplicationPrerequisiteCredential{Revision: "v1", Availability: "available"}
		},
		"data_false_identity": func(s *ApplicationPrerequisiteSnapshot) {
			s.Data["bad"] = ApplicationPrerequisiteData{Availability: "available", Custody: "matches"}
		},
		"observation_path": func(s *ApplicationPrerequisiteSnapshot) {
			s.Observation = &ApplicationPrerequisiteObservation{ObservedAt: time.Now(), State: "/private/path"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var s ApplicationPrerequisiteSnapshot
			_ = json.Unmarshal(raw, &s)
			mutate(&s)
			bad, _ := json.Marshal(s)
			if _, err := decodeApplicationPrerequisiteSnapshot(bad, q); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	for name, bad := range map[string]string{
		"unknown":                strings.Replace(string(raw), `"data":`, `"secret":"value","data":`, 1),
		"missing_required_false": strings.Replace(string(raw), `"present":false`, ``, 1),
		"duplicate":              strings.Replace(string(raw), `"schema_version":`, `"schema_version":"ignored","schema_version":`, 1),
		"overflow":               strings.Replace(string(raw), `"data":{}`, `"data":{"files":{"availability":"available","custody":"matches","pool_identity":"sha256:`+strings.Repeat("2", 64)+`","identity":{"inode":18446744073709551616,"pool_inode":1,"uid":0,"gid":0,"mode":448}}}`, 1),
		"trailing":               string(raw) + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeApplicationPrerequisiteSnapshot([]byte(bad), q); err == nil {
				t.Fatal("invalid wire shape accepted")
			}
		})
	}
}
