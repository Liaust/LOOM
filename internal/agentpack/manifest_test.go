package agentpack_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type manifestContract struct {
	SchemaVersion string `yaml:"schema_version"`
	Name          string `yaml:"name"`
	Catalogue     string `yaml:"catalogue"`
	Harnesses     map[string]struct {
		Mechanism string `yaml:"mechanism"`
	} `yaml:"harnesses"`
	RecommendedSkillSets map[string]struct {
		Visibility []string `yaml:"visibility"`
	} `yaml:"recommended_skill_sets"`
	Skills []struct {
		Name       string `yaml:"name"`
		Path       string `yaml:"path"`
		Visibility string `yaml:"visibility"`
	} `yaml:"skills"`
	Templates []struct {
		Name  string   `yaml:"name"`
		Path  string   `yaml:"path"`
		Files []string `yaml:"files"`
	} `yaml:"templates"`
	Compatibility map[string]string `yaml:"compatibility"`
}

func TestManifestDeclaresNarrowArchivistWorkspaceAndSkillSet(t *testing.T) {
	manifest := loadManifestContract(t)
	set, ok := manifest.RecommendedSkillSets["archivist"]
	if !ok {
		t.Fatal("manifest must declare the archivist recommended skill set")
	}
	if len(set.Visibility) != 1 || set.Visibility[0] != "shared_provenance" {
		t.Fatalf("archivist visibility=%v, want only shared_provenance", set.Visibility)
	}

	var provenanceSkill *struct {
		Name       string `yaml:"name"`
		Path       string `yaml:"path"`
		Visibility string `yaml:"visibility"`
	}
	for index := range manifest.Skills {
		if manifest.Skills[index].Name == "use-loom-provenance" {
			provenanceSkill = &manifest.Skills[index]
			break
		}
	}
	if provenanceSkill == nil || provenanceSkill.Path != "skills/use-loom-provenance" || provenanceSkill.Visibility != "shared_provenance" {
		t.Fatalf("unexpected provenance skill declaration: %#v", provenanceSkill)
	}

	wantFiles := []string{
		"AGENTS.md", "ARCHIVIST.md", "OPERATING-POLICY.md", "WORKFLOW.md", "WORKSPACE-MAP.md",
		"handoffs/HANDOFF-TEMPLATE.md", "investigations/README.md", "protocols/CANDIDATE-REVIEW.md",
		"protocols/README.md", "protocols/RETRIEVAL-ROUTING.md", "skills/README.md",
	}
	var gotFiles []string
	for _, template := range manifest.Templates {
		if template.Name == "archivist" {
			if template.Path != "templates/archivist" {
				t.Fatalf("archivist path=%q", template.Path)
			}
			gotFiles = append(gotFiles, template.Files...)
		}
	}
	sort.Strings(gotFiles)
	sort.Strings(wantFiles)
	if strings.Join(gotFiles, "\n") != strings.Join(wantFiles, "\n") {
		t.Fatalf("archivist template files=%v, want %v", gotFiles, wantFiles)
	}
	for _, relative := range gotFiles {
		for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
			if strings.EqualFold(component, "memory") {
				t.Fatalf("archivist template must not declare private memory: %q", relative)
			}
		}
	}
}

func loadManifestContract(t *testing.T) manifestContract {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest manifestContract
	if err := yaml.Unmarshal(payload, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return manifest
}

func TestManifestDeclaresAdaptivePackContract(t *testing.T) {
	manifest := loadManifestContract(t)
	if manifest.SchemaVersion == "" || manifest.Name != "ai-loom-pack" || manifest.Catalogue == "" {
		t.Fatalf("incomplete manifest identity: %#v", manifest)
	}
	if len(manifest.Skills) == 0 {
		t.Fatal("manifest must declare candidate skills")
	}
	seen := map[string]bool{}
	for _, skill := range manifest.Skills {
		if skill.Name == "" || skill.Path == "" || skill.Visibility == "" {
			t.Fatalf("incomplete skill entry: %#v", skill)
		}
		if seen[skill.Name] {
			t.Fatalf("duplicate skill %q", skill.Name)
		}
		seen[skill.Name] = true
	}
	manifest.Skills = append(manifest.Skills, struct {
		Name       string `yaml:"name"`
		Path       string `yaml:"path"`
		Visibility string `yaml:"visibility"`
	}{Name: "eighth-test-skill", Path: "skills/eighth-test-skill", Visibility: "shared_core"})
	if got := manifest.Skills[len(manifest.Skills)-1].Name; got != "eighth-test-skill" {
		t.Fatalf("manifest shape rejected arbitrary skill addition: %q", got)
	}
	if manifest.Compatibility["project_layout"] == "" || manifest.Compatibility["worktree_authority"] != "orca" || manifest.Compatibility["credential_authority"] != "proton-pass" {
		t.Fatalf("missing compatibility boundaries: %#v", manifest.Compatibility)
	}
	for _, harness := range []string{"codex", "orca"} {
		if manifest.Harnesses[harness].Mechanism == "" {
			t.Fatalf("missing native harness deployment metadata for %q: %#v", harness, manifest.Harnesses)
		}
	}
	for _, set := range []string{"project", "morathustra"} {
		if len(manifest.RecommendedSkillSets[set].Visibility) == 0 {
			t.Fatalf("missing recommended skill set %q: %#v", set, manifest.RecommendedSkillSets)
		}
	}
}
