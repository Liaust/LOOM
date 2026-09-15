package agentpack

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	skillNamePattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	markdownLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
)

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type openAIMetadata struct {
	Interface struct {
		DisplayName      string `yaml:"display_name"`
		ShortDescription string `yaml:"short_description"`
		DefaultPrompt    string `yaml:"default_prompt"`
		IconSmall        string `yaml:"icon_small"`
		IconLarge        string `yaml:"icon_large"`
		BrandColor       string `yaml:"brand_color"`
	} `yaml:"interface"`
	Policy       map[string]any `yaml:"policy"`
	Dependencies map[string]any `yaml:"dependencies"`
}

func Validate(pack *Pack) ValidationReport {
	report := ValidationReport{}
	if pack == nil {
		report.Issues = append(report.Issues, ValidationIssue{Severity: IssueError, Code: "pack.required", Message: "pack is required"})
		return finalizeValidation(report)
	}
	report.Root = pack.Root
	report.PackName = pack.Manifest.Name
	report.PackVersion = pack.Manifest.Version
	report.SkillCount = len(pack.Manifest.Skills)
	report.TemplateCount = len(pack.Manifest.Templates)
	add := func(severity IssueSeverity, code, path, message string) {
		report.Issues = append(report.Issues, ValidationIssue{Severity: severity, Code: code, Path: filepath.ToSlash(path), Message: message})
	}

	if err := inspectPackRegularTree(pack.Root.Path); err != nil {
		add(IssueError, "pack.unsafe", ".", err.Error())
		return finalizeValidation(report)
	}

	if pack.Manifest.SchemaVersion == "" || pack.Manifest.Name == "" || pack.Manifest.Version == "" {
		add(IssueError, "manifest.identity", "manifest.yaml", "schema_version, name, and version are required")
	}
	if pack.Manifest.HashPolicy.Algorithm != "sha256" {
		add(IssueError, "manifest.hash_algorithm", "manifest.yaml", "hash policy algorithm must be sha256")
	}
	for _, harness := range []string{"codex", "orca"} {
		spec, exists := pack.Manifest.Harnesses[harness]
		if !exists || strings.TrimSpace(spec.Mechanism) == "" {
			add(IssueError, "manifest.harness", "manifest.yaml", fmt.Sprintf("harness %q must declare its native deployment mechanism", harness))
		}
	}
	if pack.Catalogue.Pack != pack.Manifest.Name || pack.Catalogue.Version != pack.Manifest.Version {
		add(IssueError, "catalogue.identity", pack.Manifest.Catalogue, "catalogue pack and version must match the manifest")
	}
	visibilityClasses := map[string]bool{}
	for _, class := range pack.Catalogue.VisibilityClasses {
		visibilityClasses[class] = true
	}
	for name, set := range pack.Manifest.RecommendedSkillSets {
		if len(set.Visibility) == 0 {
			add(IssueError, "manifest.skill_set", "manifest.yaml", fmt.Sprintf("recommended skill set %q has no visibility classes", name))
		}
		for _, class := range set.Visibility {
			if !visibilityClasses[class] {
				add(IssueError, "manifest.skill_set_visibility", "manifest.yaml", fmt.Sprintf("recommended skill set %q uses unknown visibility class %q", name, class))
			}
		}
	}

	manifestSkills := map[string]ManifestSkill{}
	for _, skill := range pack.Manifest.Skills {
		if !validSkillName(skill.Name) {
			add(IssueError, "skill.name", "manifest.yaml", fmt.Sprintf("invalid skill name %q", skill.Name))
		}
		if _, exists := manifestSkills[skill.Name]; exists {
			add(IssueError, "skill.duplicate", "manifest.yaml", fmt.Sprintf("duplicate skill %q", skill.Name))
		}
		manifestSkills[skill.Name] = skill
		if strings.TrimSpace(skill.Version) == "" {
			add(IssueError, "skill.version", "manifest.yaml", fmt.Sprintf("skill %q has no version", skill.Name))
		}
		if strings.TrimSpace(skill.Hash) == "" {
			add(IssueError, "skill.hash", "manifest.yaml", fmt.Sprintf("skill %q has no hash or hash-generation marker", skill.Name))
		}
		if !visibilityClasses[skill.Visibility] {
			add(IssueError, "skill.visibility", "manifest.yaml", fmt.Sprintf("skill %q uses unknown visibility class %q", skill.Name, skill.Visibility))
		}
		if skill.Path == "" {
			add(IssueError, "skill.path", "manifest.yaml", fmt.Sprintf("skill %q has no path", skill.Name))
		} else if _, err := secureJoin(pack.Root.Path, skill.Path); err != nil {
			add(IssueError, "skill.path_escape", "manifest.yaml", err.Error())
		}
	}
	catalogueSkills := map[string]CatalogueSkill{}
	for _, skill := range pack.Catalogue.Skills {
		catalogueSkills[skill.Name] = skill
		manifest, exists := manifestSkills[skill.Name]
		if !exists {
			add(IssueError, "catalogue.undeclared_skill", pack.Manifest.Catalogue, fmt.Sprintf("catalogue skill %q is absent from the manifest", skill.Name))
			continue
		}
		if skill.Version != manifest.Version || skill.Visibility != manifest.Visibility {
			add(IssueError, "catalogue.skill_mismatch", pack.Manifest.Catalogue, fmt.Sprintf("catalogue metadata for %q differs from the manifest", skill.Name))
		}
		if !validCatalogueStatus(skill.Status) {
			add(IssueError, "catalogue.skill_status", pack.Manifest.Catalogue, fmt.Sprintf("catalogue skill %q has unsupported status %q", skill.Name, skill.Status))
		}
		if len(skill.PositiveTriggers) == 0 || len(skill.NegativeTriggers) == 0 || len(skill.IntendedRoles) == 0 || skill.SafetyClass == "" {
			add(IssueError, "catalogue.skill_incomplete", pack.Manifest.Catalogue, fmt.Sprintf("catalogue skill %q lacks trigger, role, or safety metadata", skill.Name))
		}
		skillRoot, rootErr := secureJoin(pack.Root.Path, manifest.Path)
		if rootErr == nil && directoryExists(skillRoot) {
			for _, reference := range skill.References {
				path, referenceErr := secureJoin(skillRoot, reference)
				if referenceErr != nil {
					add(IssueError, "catalogue.reference_escape", pack.Manifest.Catalogue, fmt.Sprintf("skill %q reference %q escapes its folder", skill.Name, reference))
					continue
				}
				if !fileExists(path) {
					add(IssueError, "catalogue.reference_missing", filepath.Join(manifest.Path, reference), fmt.Sprintf("skill %q catalogue reference is missing", skill.Name))
				}
			}
		}
	}
	for name, skill := range manifestSkills {
		catalogue, exists := catalogueSkills[name]
		if !exists {
			add(IssueError, "manifest.uncatalogued_skill", "manifest.yaml", fmt.Sprintf("manifest skill %q is absent from the catalogue", name))
			continue
		}
		skillRoot, err := secureJoin(pack.Root.Path, skill.Path)
		if err != nil {
			continue
		}
		if !directoryExists(skillRoot) {
			severity := IssueError
			if catalogue.Status == "hypothesis" {
				severity = IssueWarning
			}
			add(severity, "skill.missing", skill.Path, fmt.Sprintf("skill %q source folder is missing", name))
			continue
		}
		validateSkillFolder(skillRoot, skill, &report, add)
	}

	validateUndeclaredSkillFolders(pack, manifestSkills, add)
	validateTemplates(pack, add)
	return finalizeValidation(report)
}

func validateSkillFolder(root string, declared ManifestSkill, report *ValidationReport, add func(IssueSeverity, string, string, string)) {
	relSkill := declared.Path
	skillFile := filepath.Join(root, "SKILL.md")
	payload, err := readWorkspaceFile(skillFile)
	if err != nil {
		add(IssueError, "skill.skill_md_required", filepath.Join(relSkill, "SKILL.md"), err.Error())
		return
	}
	metadata, err := parseSkillFrontmatter(payload)
	if err != nil {
		add(IssueError, "skill.frontmatter", filepath.Join(relSkill, "SKILL.md"), err.Error())
	} else {
		if metadata.Name != declared.Name || !validSkillName(metadata.Name) {
			add(IssueError, "skill.name_mismatch", filepath.Join(relSkill, "SKILL.md"), "frontmatter name must match the skill folder and manifest")
		}
		if strings.TrimSpace(metadata.Description) == "" {
			add(IssueError, "skill.description_required", filepath.Join(relSkill, "SKILL.md"), "frontmatter description is required")
		}
	}
	lines := bytes.Count(payload, []byte("\n")) + 1
	if lines > 500 {
		add(IssueError, "skill.line_limit", filepath.Join(relSkill, "SKILL.md"), fmt.Sprintf("SKILL.md has %d lines; maximum is 500", lines))
	} else if lines > 350 {
		add(IssueWarning, "skill.line_warning", filepath.Join(relSkill, "SKILL.md"), fmt.Sprintf("SKILL.md has %d lines; consider progressive disclosure", lines))
	}

	openAIPath := filepath.Join(root, "agents", "openai.yaml")
	var openAI openAIMetadata
	if err := decodeYAMLFile(openAIPath, &openAI); err != nil {
		add(IssueError, "skill.openai_metadata", filepath.Join(relSkill, "agents", "openai.yaml"), err.Error())
	} else if strings.TrimSpace(openAI.Interface.DisplayName) == "" || strings.TrimSpace(openAI.Interface.ShortDescription) == "" || strings.TrimSpace(openAI.Interface.DefaultPrompt) == "" {
		add(IssueError, "skill.openai_interface", filepath.Join(relSkill, "agents", "openai.yaml"), "display_name, short_description, and default_prompt are required")
	}

	for _, match := range markdownLinkPattern.FindAllSubmatch(payload, -1) {
		target := strings.TrimSpace(string(match[1]))
		if target == "" || strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		if fragment := strings.Index(target, "#"); fragment >= 0 {
			target = target[:fragment]
		}
		if _, err := secureJoin(root, target); err != nil {
			add(IssueError, "skill.reference_escape", filepath.Join(relSkill, "SKILL.md"), fmt.Sprintf("reference %q escapes the skill folder", target))
			continue
		}
		parts := strings.Split(filepath.ToSlash(filepath.Clean(filepath.FromSlash(target))), "/")
		if len(parts) > 2 && parts[0] == "references" {
			add(IssueError, "skill.reference_depth", filepath.Join(relSkill, "SKILL.md"), fmt.Sprintf("reference %q is nested deeper than one level", target))
		}
		targetPath, _ := secureJoin(root, target)
		if !fileExists(targetPath) {
			add(IssueError, "skill.reference_missing", filepath.Join(relSkill, "SKILL.md"), fmt.Sprintf("reference %q does not exist", target))
		}
	}

	_ = filepath.WalkDir(filepath.Join(root, "references"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if path == filepath.Join(root, "references") {
			return nil
		}
		relative, _ := filepath.Rel(filepath.Join(root, "references"), path)
		if entry.IsDir() && strings.Contains(relative, string(filepath.Separator)) {
			add(IssueError, "skill.reference_depth", filepath.Join(relSkill, "references", relative), "references may be only one level below SKILL.md")
			return filepath.SkipDir
		}
		return nil
	})
	_ = report
}

func validateUndeclaredSkillFolders(pack *Pack, declared map[string]ManifestSkill, add func(IssueSeverity, string, string, string)) {
	skillsRoot := filepath.Join(pack.Root.Path, "skills")
	entries, err := os.ReadDir(skillsRoot)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		add(IssueError, "skills.read", "skills", err.Error())
		return
	}
	declaredPaths := map[string]bool{}
	for _, skill := range declared {
		declaredPaths[filepath.Clean(filepath.FromSlash(skill.Path))] = true
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		relative := filepath.Join("skills", entry.Name())
		if !declaredPaths[relative] {
			add(IssueError, "skill.undeclared_folder", relative, "skill folder is not declared in the manifest")
		}
	}
}

func validateTemplates(pack *Pack, add func(IssueSeverity, string, string, string)) {
	seen := map[string]bool{}
	for _, template := range pack.Manifest.Templates {
		if seen[template.Name] {
			add(IssueError, "template.duplicate", "manifest.yaml", "workspace template names must be unique")
		}
		seen[template.Name] = true
		if _, err := inspectWorkspaceTemplate(pack, template); err != nil {
			add(IssueError, "template.unsafe", template.Path, err.Error())
		}
		root, err := secureJoin(pack.Root.Path, template.Path)
		if err != nil {
			add(IssueError, "template.path_escape", "manifest.yaml", err.Error())
			continue
		}
		for _, file := range template.Files {
			path, err := secureJoin(root, file)
			if err != nil {
				add(IssueError, "template.file_escape", template.Path, err.Error())
				continue
			}
			if !fileExists(path) {
				add(IssueError, "template.file_missing", filepath.Join(template.Path, file), "declared template file is missing")
			}
		}
	}
}

func parseSkillFrontmatter(payload []byte) (skillFrontmatter, error) {
	normalized := bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return skillFrontmatter{}, fmt.Errorf("missing YAML frontmatter")
	}
	rest := normalized[4:]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return skillFrontmatter{}, fmt.Errorf("unterminated YAML frontmatter")
	}
	var metadata skillFrontmatter
	decoder := yaml.NewDecoder(bytes.NewReader(rest[:end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&metadata); err != nil {
		return skillFrontmatter{}, err
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	return metadata, nil
}

func validSkillName(name string) bool {
	return len(name) > 0 && len(name) < 64 && skillNamePattern.MatchString(name)
}

func validCatalogueStatus(status string) bool {
	switch status {
	case "hypothesis", "candidate", "accepted", "split", "merged", "retired":
		return true
	default:
		return false
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func finalizeValidation(report ValidationReport) ValidationReport {
	report.Errors = 0
	report.Warnings = 0
	for _, issue := range report.Issues {
		if issue.Severity == IssueError {
			report.Errors++
		} else {
			report.Warnings++
		}
	}
	sort.SliceStable(report.Issues, func(i, j int) bool {
		left := string(report.Issues[i].Severity) + report.Issues[i].Code + report.Issues[i].Path
		right := string(report.Issues[j].Severity) + report.Issues[j].Code + report.Issues[j].Path
		return left < right
	})
	return report
}
