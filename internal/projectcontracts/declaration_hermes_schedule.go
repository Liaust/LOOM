package projectcontracts

import (
	"fmt"
	"loom.local/loom/internal/hermesschedules"
	"path/filepath"
	"strings"
	"time"
)

type HermesScheduleDeclaration struct {
	Profile      string   `json:"profile" yaml:"profile"`
	Prompt       string   `json:"prompt" yaml:"prompt"`
	Skills       []string `json:"skills,omitempty" yaml:"skills,omitempty"`
	Cron         string   `json:"cron,omitempty" yaml:"cron,omitempty"`
	EveryMinutes int      `json:"every_minutes,omitempty" yaml:"every_minutes,omitempty"`
	At           string   `json:"at,omitempty" yaml:"at,omitempty"`
	Status       string   `json:"status,omitempty" yaml:"status,omitempty"`
}

func HermesScheduleInput(s HermesScheduleDeclaration, root string) (hermesschedules.ProjectSpec, error) {
	fail := func() (hermesschedules.ProjectSpec, error) {
		return hermesschedules.ProjectSpec{}, fmt.Errorf("explicit mina profile, prompt, safe skills, one native timing expression and active/paused status required")
	}
	if s.Profile != "mina" || strings.TrimSpace(s.Prompt) == "" || len(s.Prompt) > 32768 || strings.ContainsRune(s.Prompt, 0) || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fail()
	}
	n := 0
	timing := ""
	if s.Cron != "" {
		n++
		if len(strings.Fields(s.Cron)) != 5 || len(s.Cron) > 256 || strings.ContainsAny(s.Cron, "\r\n") {
			return fail()
		}
		timing = s.Cron
	}
	if s.EveryMinutes != 0 {
		n++
		if s.EveryMinutes < 1 || s.EveryMinutes > 525600 {
			return fail()
		}
		timing = fmt.Sprintf("every %dm", s.EveryMinutes)
	}
	if s.At != "" {
		n++
		if _, err := time.Parse(time.RFC3339, s.At); err != nil {
			return fail()
		}
		timing = s.At
	}
	if n != 1 {
		return fail()
	}
	status := s.Status
	if status == "" {
		status = "paused"
	}
	if status != "paused" && status != "active" {
		return fail()
	}
	if len(s.Skills) > 16 {
		return fail()
	}
	skills := []string{}
	seen := map[string]bool{}
	for _, skill := range s.Skills {
		if !scheduleKeyPattern.MatchString(skill) || seen[skill] {
			return fail()
		}
		seen[skill] = true
		skills = append(skills, skill)
	}
	return hermesschedules.ProjectSpec{Schedule: timing, Prompt: strings.TrimSpace(s.Prompt), Skills: skills, Status: status, Workdir: root}, nil
}
