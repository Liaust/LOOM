package filepolicy

// RuleCategory identifies the policy layer responsible for a decision.
type RuleCategory string

const (
	RuleCategoryNone            RuleCategory = "none"
	RuleCategoryMandatorySafety RuleCategory = "mandatory_safety"
	RuleCategoryReconstructible RuleCategory = "reconstructible"
	RuleCategoryUser            RuleCategory = "user"
	RuleCategoryContract        RuleCategory = "contract"
)

// Decision explains the effective file-policy result for one path.
type Decision struct {
	Path          string       `json:"path"`
	Included      bool         `json:"included"`
	Profile       Profile      `json:"profile"`
	RuleCategory  RuleCategory `json:"rule_category"`
	Pattern       string       `json:"pattern,omitempty"`
	PolicyVersion string       `json:"policy_version"`
	SourceFile    string       `json:"source_file,omitempty"`
	SourceLine    int          `json:"source_line,omitempty"`
	Negated       bool         `json:"negated,omitempty"`
}
