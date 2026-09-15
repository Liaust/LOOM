package actions

import "loom.local/loom/internal/loomcli/ui"

type RiskLevel string

const (
	RiskReadOnly    RiskLevel = "read_only"
	RiskSafeRun     RiskLevel = "safe_run"
	RiskStateChange RiskLevel = "state_change"
	RiskDestructive RiskLevel = "destructive"
)

type Action struct {
	ID              string
	Title           string
	Description     string
	Domain          string
	Keywords        []string
	Risk            RiskLevel
	RawCommand      []string
	StartScreen     string
	ExecutionKind   string
	ExecutionTarget string
	Enabled         bool
}

func (a Action) Candidate() ui.Candidate {
	return ui.Candidate{
		ID:          a.ID,
		Title:       a.Title,
		Description: a.Description,
		Domain:      a.Domain,
		Keywords:    append([]string{}, a.Keywords...),
	}
}

type SearchResult struct {
	Action Action
	Score  int
}
