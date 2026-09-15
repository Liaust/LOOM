package agentpack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAcceptsCompleteSkill(t *testing.T) {
	report := Validate(loadValidPack(t))
	if !report.Valid() || report.Warnings != 0 {
		t.Fatalf("unexpected validation report: %#v", report)
	}
}

func TestValidateReportsSkillMetadataFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		code   string
	}{
		{"missing skill md", func(t *testing.T, root string) { os.Remove(filepath.Join(root, "skills", "fixture-skill", "SKILL.md")) }, "skill.skill_md_required"},
		{"invalid frontmatter", func(t *testing.T, root string) {
			os.WriteFile(filepath.Join(root, "skills", "fixture-skill", "SKILL.md"), []byte("no frontmatter"), 0o644)
		}, "skill.frontmatter"},
		{"missing openai", func(t *testing.T, root string) {
			os.Remove(filepath.Join(root, "skills", "fixture-skill", "agents", "openai.yaml"))
		}, "skill.openai_metadata"},
		{"escaping reference", func(t *testing.T, root string) {
			replaceFile(t, filepath.Join(root, "skills", "fixture-skill", "SKILL.md"), "references/commands.md", "../outside.md")
		}, "skill.reference_escape"},
		{"manifest catalogue mismatch", func(t *testing.T, root string) {
			replaceFile(t, filepath.Join(root, "catalogue.yaml"), "version: 1.0.0", "version: 2.0.0")
		}, "catalogue.identity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := copyPackFixture(t)
			test.mutate(t, root)
			pack, err := LoadFromPath(root)
			if err != nil {
				t.Fatal(err)
			}
			report := Validate(pack)
			if !hasValidationCode(report, test.code) {
				t.Fatalf("missing code %q: %#v", test.code, report)
			}
		})
	}
}

func TestValidateReportsCatalogueReferenceAndManifestMetadataFailures(t *testing.T) {
	pack := loadValidPack(t)
	pack.Manifest.Skills[0].Hash = ""
	delete(pack.Manifest.Harnesses, "orca")
	pack.Catalogue.Skills[0].Status = "unreviewed"
	pack.Catalogue.Skills[0].References = []string{"references/missing.md"}
	report := Validate(pack)
	for _, code := range []string{"skill.hash", "manifest.harness", "catalogue.skill_status", "catalogue.reference_missing"} {
		if !hasValidationCode(report, code) {
			t.Fatalf("missing code %q: %#v", code, report)
		}
	}
}

func TestValidateTreatsMissingHypothesisAsWarning(t *testing.T) {
	root := copyPackFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "skills", "fixture-skill")); err != nil {
		t.Fatal(err)
	}
	replaceFile(t, filepath.Join(root, "catalogue.yaml"), "status: accepted", "status: hypothesis")
	pack, err := LoadFromPath(root)
	if err != nil {
		t.Fatal(err)
	}
	report := Validate(pack)
	if !report.Valid() || !hasValidationCode(report, "skill.missing") || report.Warnings != 1 {
		t.Fatalf("unexpected hypothesis report: %#v", report)
	}
}

func TestValidateAcceptsArbitraryCandidateCount(t *testing.T) {
	root := copyPackFixture(t)
	pack, err := LoadFromPath(root)
	if err != nil {
		t.Fatal(err)
	}
	baseCatalogue := pack.Catalogue.Skills[0]
	baseCatalogue.Status = "candidate"
	pack.Catalogue.Skills[0].Status = "candidate"
	for index := 2; index <= 8; index++ {
		name := fmt.Sprintf("fixture-skill-%d", index)
		relative := filepath.ToSlash(filepath.Join("skills", name))
		skillRoot := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Join(skillRoot, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		skillBody := fmt.Sprintf("---\nname: %s\ndescription: Use this additional fixture skill for arbitrary catalogue size tests.\n---\n\n# Fixture\n", name)
		if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
			t.Fatal(err)
		}
		openAI := fmt.Sprintf("interface:\n  display_name: \"Fixture %d\"\n  short_description: \"Test arbitrary skill count\"\n  default_prompt: \"Use $%s for its fixture.\"\n", index, name)
		if err := os.WriteFile(filepath.Join(skillRoot, "agents", "openai.yaml"), []byte(openAI), 0o644); err != nil {
			t.Fatal(err)
		}
		pack.Manifest.Skills = append(pack.Manifest.Skills, ManifestSkill{Name: name, Version: "1.0.0", Path: relative, Visibility: "shared_core", Hash: "release-generated"})
		catalogue := baseCatalogue
		catalogue.Name = name
		catalogue.References = nil
		pack.Catalogue.Skills = append(pack.Catalogue.Skills, catalogue)
	}
	report := Validate(pack)
	if !report.Valid() || report.SkillCount != 8 {
		t.Fatalf("unexpected arbitrary-count report: %#v", report)
	}
}

func TestValidateAcceptsRepositoryCandidatePack(t *testing.T) {
	pack := loadRepositoryPack(t)
	report := Validate(pack)
	if !report.Valid() || report.Warnings != 0 {
		t.Fatalf("unexpected repository pack report: %#v", report)
	}
	if len(pack.Catalogue.Skills) == 0 {
		t.Fatal("repository pack must contain candidates")
	}
	for _, skill := range pack.Catalogue.Skills {
		if skill.Status != "candidate" {
			t.Fatalf("skill %s status=%q, want candidate", skill.Name, skill.Status)
		}
		if len(skill.References) == 0 {
			t.Fatalf("skill %s has no catalogue references", skill.Name)
		}
	}
}

func TestValidateAcceptsArchivistSkillAndWorkspaceContract(t *testing.T) {
	pack := loadRepositoryPack(t)
	report := Validate(pack)
	if !report.Valid() || report.Warnings != 0 {
		t.Fatalf("unexpected validation report: %#v", report)
	}
	if report.SkillCount != 9 || report.TemplateCount != 6 {
		t.Fatalf("unexpected repository pack counts: %#v", report)
	}
	if !contains(pack.Catalogue.Roles, "archivist") || !contains(pack.Catalogue.VisibilityClasses, "archivist_operations") {
		t.Fatalf("archivist catalogue contract missing: roles=%v visibility=%v", pack.Catalogue.Roles, pack.Catalogue.VisibilityClasses)
	}
	set := pack.Manifest.RecommendedSkillSets["archivist"].Visibility
	if len(set) != 1 || set[0] != "shared_provenance" {
		t.Fatalf("archivist skill set widened: %v", set)
	}
}

func TestArchivistSkillRoutesRetrievalWithoutOperationalSurfaceWidening(t *testing.T) {
	pack := loadRepositoryPack(t)
	root := filepath.Join(pack.Root.Path, "skills", "use-loom-provenance")
	var combined strings.Builder
	for _, relative := range []string{"SKILL.md", "references/retrieval-routing.md", "references/reconciliation.md"} {
		payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		combined.Write(payload)
		combined.WriteByte('\n')
	}
	content := combined.String()
	for _, required := range []string{
		"loom object", "loom notes", "loom provenance", "loom project",
		"one first engine", "exact", "candidates", "manual",
	} {
		if !strings.Contains(strings.ToLower(content), strings.ToLower(required)) {
			t.Errorf("Archivist skill omits routing/control marker %q", required)
		}
	}
	for _, forbidden := range []string{
		"loom storage ", "loom node ", "loom backup ", "loom cloud ", "loom schedule ",
		"loom automation ", "pass-cli", "pass://", "postgres://", "postgresql://",
		"LOOM_PROVENANCE_DB_URL",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("Archivist skill widened into forbidden command or locator %q", forbidden)
		}
	}
}

func TestValidateEnforcesSkillLineLimits(t *testing.T) {
	root := copyPackFixture(t)
	path := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	prefix := "---\nname: fixture-skill\ndescription: Use the fixture workflow for line limit validation.\n---\n"
	if err := os.WriteFile(path, []byte(prefix+strings.Repeat("line\n", 501)), 0o644); err != nil {
		t.Fatal(err)
	}
	pack, _ := LoadFromPath(root)
	if report := Validate(pack); !hasValidationCode(report, "skill.line_limit") {
		t.Fatalf("missing line limit: %#v", report)
	}
}

func hasValidationCode(report ValidationReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
