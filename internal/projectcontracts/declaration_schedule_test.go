package projectcontracts

import (
	"strings"
	"testing"
)

func TestScheduleDeclaration(t *testing.T) {
	p := ProjectSpec{ID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Slug: "plain-project", Name: "Plain project"}
	s := ScheduleDeclaration{Target: "main@system.health.read", Every: "1m", InputJSON: `{"fraction":0.125,"exact":9007199254740993}`}
	input, err := ScheduleDeclarationInput(p, "check", s)
	if err != nil || input.Status != "disabled" || input.ScopeRef != "project:plain-project" || input.RunAsActorRef != "scheduler:loom" || !strings.Contains(string(input.InputJSON), "9007199254740993") {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	for name, change := range map[string]func(*ScheduleDeclaration){
		"calendar": func(s *ScheduleDeclaration) {
			s.Every = ""
			s.Cron = "15 9,13,18 * * *"
			s.Timezone = "Europe/Amsterdam"
		},
		"once": func(s *ScheduleDeclaration) { s.Every = ""; s.At = "2030-01-01T10:00:00Z" },
	} {
		t.Run(name, func(t *testing.T) {
			v := s
			change(&v)
			if _, err := ScheduleDeclarationInput(p, "check", v); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, change := range map[string]func(*ScheduleDeclaration){
		"multiple":        func(s *ScheduleDeclaration) { s.Cron = "* * * * *" },
		"relative":        func(s *ScheduleDeclaration) { s.Every = ""; s.At = "now" },
		"host_zone":       func(s *ScheduleDeclaration) { s.Timezone = "Local" },
		"invalid_zone":    func(s *ScheduleDeclaration) { s.Timezone = "Unknown/Zone" },
		"duplicate_input": func(s *ScheduleDeclaration) { s.InputJSON = `{"x":1,"x":2}` },
		"array_input":     func(s *ScheduleDeclaration) { s.InputJSON = `[]` },
		"invalid_target":  func(s *ScheduleDeclaration) { s.Target = "not a capability" },
		"invalid_status":  func(s *ScheduleDeclaration) { s.Status = "completed" },
		"owner_shortcut":  func(s *ScheduleDeclaration) { s.RunAs = "owner" },
		"attempts":        func(s *ScheduleDeclaration) { s.MaxAttempts = 11 },
	} {
		t.Run(name, func(t *testing.T) {
			v := s
			change(&v)
			if _, err := ScheduleDeclarationInput(p, "check", v); err == nil {
				t.Fatal("accepted invalid schedule")
			}
		})
	}
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", "resources:\n  check:\n    kind: schedule\n    schedule:\n      target: main@system.health.read\n      every: 1m", 1)
	a := Analyze(declarationTestRoot(t, []byte(raw)))
	if !a.Report.OK || a.Loaded.Declaration.Resources["check"].Schedule == nil {
		t.Fatalf("schedule declaration did not compile: %+v", a.Report)
	}
}
