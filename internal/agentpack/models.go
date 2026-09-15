package agentpack

type RootSource string

const (
	RootExplicit    RootSource = "explicit"
	RootEnvironment RootSource = "environment"
	RootPackaged    RootSource = "packaged"
	RootRepository  RootSource = "repository"
)

type ResolvedRoot struct {
	Path   string     `json:"path"`
	Source RootSource `json:"source"`
}

type Manifest struct {
	SchemaVersion        string                         `yaml:"schema_version" json:"schema_version"`
	Name                 string                         `yaml:"name" json:"name"`
	Version              string                         `yaml:"version" json:"version"`
	LoomCompatibility    string                         `yaml:"loom_compatibility" json:"loom_compatibility"`
	Documentation        DocumentationSpec              `yaml:"documentation" json:"documentation"`
	Catalogue            string                         `yaml:"catalogue" json:"catalogue"`
	HashPolicy           HashPolicy                     `yaml:"hash_policy" json:"hash_policy"`
	Harnesses            map[string]HarnessSpec         `yaml:"harnesses" json:"harnesses"`
	RecommendedSkillSets map[string]RecommendedSkillSet `yaml:"recommended_skill_sets" json:"recommended_skill_sets"`
	Compatibility        map[string]string              `yaml:"compatibility" json:"compatibility"`
	Skills               []ManifestSkill                `yaml:"skills" json:"skills"`
	Templates            []ManifestTemplate             `yaml:"templates" json:"templates"`
	Build                map[string]string              `yaml:"build" json:"build"`
}

type DocumentationSpec struct {
	Version string `yaml:"version" json:"version"`
	Path    string `yaml:"path" json:"path"`
}

type HashPolicy struct {
	Algorithm string `yaml:"algorithm" json:"algorithm"`
	Mode      string `yaml:"mode" json:"mode"`
}

type HarnessSpec struct {
	Mechanism string `yaml:"mechanism" json:"mechanism"`
}

type RecommendedSkillSet struct {
	Visibility []string `yaml:"visibility" json:"visibility"`
}

type ManifestSkill struct {
	Name       string `yaml:"name" json:"name"`
	Version    string `yaml:"version" json:"version"`
	Path       string `yaml:"path" json:"path"`
	Visibility string `yaml:"visibility" json:"visibility"`
	Hash       string `yaml:"hash" json:"hash"`
}

type ManifestTemplate struct {
	Name  string   `yaml:"name" json:"name"`
	Path  string   `yaml:"path" json:"path"`
	Files []string `yaml:"files" json:"files"`
}

type Catalogue struct {
	SchemaVersion     string               `yaml:"schema_version" json:"schema_version"`
	Pack              string               `yaml:"pack" json:"pack"`
	Version           string               `yaml:"version" json:"version"`
	Roles             []string             `yaml:"roles" json:"roles"`
	VisibilityClasses []string             `yaml:"visibility_classes" json:"visibility_classes"`
	SafetyClasses     []string             `yaml:"safety_classes" json:"safety_classes"`
	Invariants        []CatalogueInvariant `yaml:"invariants" json:"invariants"`
	Skills            []CatalogueSkill     `yaml:"skills" json:"skills"`
}

type CatalogueInvariant struct {
	ID   string `yaml:"id" json:"id"`
	Rule string `yaml:"rule" json:"rule"`
}

type CatalogueSkill struct {
	Name             string         `yaml:"name" json:"name"`
	Version          string         `yaml:"version" json:"version"`
	Status           string         `yaml:"status" json:"status"`
	Visibility       string         `yaml:"visibility" json:"visibility"`
	IntendedRoles    []string       `yaml:"intended_roles" json:"intended_roles"`
	PositiveTriggers []string       `yaml:"positive_triggers" json:"positive_triggers"`
	NegativeTriggers []string       `yaml:"negative_triggers" json:"negative_triggers"`
	SafetyClass      string         `yaml:"safety_class" json:"safety_class"`
	References       []string       `yaml:"references" json:"references"`
	Overlaps         []string       `yaml:"overlaps" json:"overlaps"`
	Evaluation       map[string]any `yaml:"evaluation" json:"evaluation"`
	History          []any          `yaml:"history" json:"history"`
}

type Pack struct {
	Root      ResolvedRoot `json:"root"`
	Manifest  Manifest     `json:"manifest"`
	Catalogue Catalogue    `json:"catalogue"`
}

type IssueSeverity string

const (
	IssueWarning IssueSeverity = "warning"
	IssueError   IssueSeverity = "error"
)

type ValidationIssue struct {
	Severity IssueSeverity `json:"severity"`
	Code     string        `json:"code"`
	Path     string        `json:"path,omitempty"`
	Message  string        `json:"message"`
}

type ValidationReport struct {
	Root          ResolvedRoot      `json:"root"`
	PackName      string            `json:"pack_name"`
	PackVersion   string            `json:"pack_version"`
	SkillCount    int               `json:"skill_count"`
	TemplateCount int               `json:"template_count"`
	Errors        int               `json:"errors"`
	Warnings      int               `json:"warnings"`
	Issues        []ValidationIssue `json:"issues,omitempty"`
}

func (r ValidationReport) Valid() bool { return r.Errors == 0 }
