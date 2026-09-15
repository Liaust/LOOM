package objects

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

func packageSnapshotTestMetadata(role string) packageVersionMetadata {
	digest := "sha256:" + strings.Repeat("a", 64)
	b := PackageBinding{Role: role, ProjectID: ids.NewProjectID(), ScopeID: ids.NewScopeID(), OwnerNodeID: ids.NewNodeID()}
	if role == "producer" {
		b.Producer = &ProducerPackageBinding{Kind: "script", ProducerID: ids.NewScriptID(), VersionID: ids.NewScriptVersionID(), VersionLabel: "1", ManifestJSON: `{"kind":"loom.script"}`, ManifestHash: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{"kind":"loom.script"}`))), ContentHash: digest}
	} else {
		b.Source = &projects.RetainedSourceFacts{RetainedSourceSelector: projects.RetainedSourceSelector{ProjectID: b.ProjectID, RepositoryID: strings.Replace(ids.NewProjectID(), "project_", "repo_", 1), SourceBindingDigest: digest, LocationDigest: digest, SourceRevision: 1, Selection: []string{"."}}, ProjectScopeID: b.ScopeID, OwnerNodeID: b.OwnerNodeID, MemberPath: "repo", RegistrationID: ids.NewProjectContractRegistrationID(), SemanticDigest: digest}
	}
	return packageVersionMetadata{PackageSnapshotSchema, objectstore.PackageInfo{Format: objectstore.PackageFormat, SHA256: digest, SizeBytes: 2048, EntryCount: 1, ContentBytes: 1}, b}
}
func TestPackageSnapshotMetadata(t *testing.T) {
	for _, role := range []string{"source", "producer"} {
		t.Run(role, func(t *testing.T) {
			m := packageSnapshotTestMetadata(role)
			raw, e := json.Marshal(m)
			if e != nil {
				t.Fatal(e)
			}
			got, e := decodePackageMetadata(raw)
			if e != nil {
				t.Fatal(e)
			}
			round, _ := json.Marshal(got)
			if string(round) != string(raw) {
				t.Fatal("binding did not survive roundtrip")
			}
		})
	}
	for _, kind := range []string{"schema", "format", "hash", "size", "entries", "content", "scope", "project", "node", "role", "union", "parent_project", "parent_scope", "parent_node", "source_revision", "selection_order", "member_path", "producer_kind", "producer_id", "version_id", "manifest_hash", "content_hash", "manifest_json", "producer_limit", "unknown_field", "trailing", "missing"} {
		t.Run(kind, func(t *testing.T) {
			m := packageSnapshotTestMetadata("source")
			if strings.HasPrefix(kind, "producer_") || kind == "version_id" || strings.HasPrefix(kind, "manifest_") || kind == "content_hash" {
				m = packageSnapshotTestMetadata("producer")
			}
			switch kind {
			case "schema":
				m.Schema = "future"
			case "format":
				m.Package.Format = "tar"
			case "hash":
				m.Package.SHA256 = "sha256:wrong"
			case "size":
				m.Package.SizeBytes = objectstore.PackageMaxArchiveBytes + 1
			case "entries":
				m.Package.EntryCount = 20001
			case "content":
				m.Package.ContentBytes = -1
			case "scope":
				m.Binding.ScopeID = "scope_wrong"
			case "project":
				m.Binding.ProjectID = "project_wrong"
			case "node":
				m.Binding.OwnerNodeID = "main"
			case "role":
				m.Binding.Role = "output"
			case "union":
				m.Binding.Producer = &ProducerPackageBinding{}
			case "parent_project":
				m.Binding.Source.ProjectID = ids.NewProjectID()
			case "parent_scope":
				m.Binding.Source.ProjectScopeID = ids.NewScopeID()
			case "parent_node":
				m.Binding.Source.OwnerNodeID = ids.NewNodeID()
			case "source_revision":
				m.Binding.Source.SourceRevision = 0
			case "selection_order":
				m.Binding.Source.Selection = []string{"z", "a"}
			case "member_path":
				m.Binding.Source.MemberPath = "../escape"
			case "producer_kind":
				m.Binding.Producer.Kind = "plugin"
			case "producer_id":
				m.Binding.Producer.ProducerID = ids.NewWorkflowID()
			case "version_id":
				m.Binding.Producer.VersionID = ids.NewWorkflowVersionID()
			case "manifest_hash":
				m.Binding.Producer.ManifestHash = "sha256:" + strings.Repeat("0", 64)
			case "content_hash":
				m.Binding.Producer.ContentHash = "unknown"
			case "manifest_json":
				m.Binding.Producer.ManifestJSON = "bad"
			case "producer_limit":
				m.Package.ContentBytes = objectstore.PackageMaxProducerBytes + 1
			}
			raw, _ := json.Marshal(m)
			switch kind {
			case "unknown_field":
				raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "missing":
				raw = []byte(`{}`)
			}
			if _, e := decodePackageMetadata(raw); e == nil {
				t.Fatal("invalid typed binding accepted")
			}
		})
	}
	t.Run("scope_is_independent", func(t *testing.T) {
		m := packageSnapshotTestMetadata("producer")
		s := Service{}
		if e := s.packageAuthority(t.Context(), requestctx.Context{}, m.Binding, ids.NewScopeID()); e == nil || !strings.Contains(e.Error(), "scope mismatch") {
			t.Fatalf("scope check=%v", e)
		}
	})
}
