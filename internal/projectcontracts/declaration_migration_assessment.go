package projectcontracts

const DeclarationMigrationAssessmentSchema = "project.declaration_migration_assessment.v1"

// These presentation DTOs carry neither owner rows nor raw source contents.
type MigrationAssessmentIssue struct {
	Family string `json:"family"`
	Code   string `json:"code"`
	Ref    string `json:"ref,omitempty"`
}
type MigrationAssessmentFamily struct {
	Family       string `json:"family"`
	Completeness string `json:"completeness"`
	Revision     string `json:"revision,omitempty"`
	RevisionKind string `json:"revision_kind"`
}
type DeclarationMigrationAssessment struct {
	SchemaVersion     string                      `json:"schema_version"`
	ProjectID         string                      `json:"project_id"`
	OwnerNode         string                      `json:"owner_node"`
	LocationRevision  string                      `json:"location_revision"`
	RegistryRevision  string                      `json:"registry_revision,omitempty"`
	LifecycleRevision string                      `json:"lifecycle_revision,omitempty"`
	SnapshotRevision  string                      `json:"snapshot_revision"`
	State             string                      `json:"state"`
	Consistency       string                      `json:"consistency"`
	Families          []MigrationAssessmentFamily `json:"families"`
	Issues            []MigrationAssessmentIssue  `json:"issues"`
	Preview           DeclarationMigrationPreview `json:"preview"`
	Readiness         DeclarationReadiness        `json:"readiness"`
}

// Validate the new response discriminator and state invariants separately from
// existing declaration status. It must never be mistaken for an executable plan.
func ValidDeclarationMigrationAssessment(a DeclarationMigrationAssessment) bool {
	if a.SchemaVersion != DeclarationMigrationAssessmentSchema || a.Consistency != "observed_reads_not_publication_fence" || a.Preview.SchemaVersion != DeclarationMigrationPreviewSchema || a.Preview.BasisKind != "supplied_snapshot" || a.Preview.RuntimeAssessment != "not_performed" || len(a.Families) != 6 || len(a.Issues) > 64 {
		return false
	}
	if !migrationAssessmentID(a.ProjectID) || !migrationAssessmentID(a.OwnerNode) || !migrationDigestValid(a.LocationRevision) || !migrationDigestValid(a.SnapshotRevision) {
		return false
	}
	for _, v := range []string{a.RegistryRevision, a.LifecycleRevision} {
		if v != "" && !migrationDigestValid(v) {
			return false
		}
	}
	names := map[string]bool{"project": false, "membership": false, "watches": false, "coverage": false, "legacy": false, "evidence": false}
	for _, f := range a.Families {
		seen, ok := names[f.Family]
		if !ok || seen || !migrationDigestValid(f.Revision) || f.RevisionKind != "observed_content_digest" {
			return false
		}
		names[f.Family] = true
		if f.Completeness != "complete" && f.Completeness != "unavailable" && f.Completeness != "truncated" {
			return false
		}
		if a.State == "eligible" && f.Completeness != "complete" {
			return false
		}
	}
	for _, v := range a.Issues {
		if _, ok := names[v.Family]; !ok && v.Family != "sources" {
			return false
		}
		if !migrationAssessmentID(v.Code) || (v.Ref != "" && !migrationRef(v.Ref)) {
			return false
		}
	}
	for _, f := range []DeclarationFact{a.Readiness.Desired, a.Readiness.Queued, a.Readiness.Applied, a.Readiness.Processing, a.Readiness.Healthy, a.Readiness.Protected, a.Readiness.Verified} {
		if f.State != DeclarationUnknown || f.Revision != a.SnapshotRevision {
			return false
		}
	}
	switch a.State {
	case "eligible":
		if a.Preview.Candidate == nil || a.Preview.Disposition != "candidate" {
			return false
		}
	case "incomplete", "refused", "already_declaration":
		if a.Preview.Candidate != nil {
			return false
		}
	default:
		return false
	}
	return true
}

func migrationAssessmentID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
