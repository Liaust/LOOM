package projectcontracts

import (
	"loom.local/loom/internal/hermesschedules"
	"strings"
	"testing"
)

func TestHermesScheduleDeclaration(t *testing.T) {
	s := HermesScheduleDeclaration{Profile: "mina", Prompt: "Read AGENTS.md and report status locally.", EveryMinutes: 60}
	input, err := HermesScheduleInput(s, "/project")
	if err != nil || input.Status != "paused" || input.Workdir != "/project" || input.Schedule != "every 60m" {
		t.Fatalf("%+v %v", input, err)
	}
	for name, change := range map[string]func(*HermesScheduleDeclaration){
		"ambient_profile": func(s *HermesScheduleDeclaration) { s.Profile = "" },
		"unsafe_profile":  func(s *HermesScheduleDeclaration) { s.Profile = "../other" },
		"double_timer":    func(s *HermesScheduleDeclaration) { s.Cron = "* * * * *" },
		"relative_once":   func(s *HermesScheduleDeclaration) { s.EveryMinutes = 0; s.At = "now" },
		"unsafe_skill":    func(s *HermesScheduleDeclaration) { s.Skills = []string{"../../private"} },
		"resume":          func(s *HermesScheduleDeclaration) { s.Status = "resume" },
	} {
		t.Run(name, func(t *testing.T) {
			v := s
			change(&v)
			if _, err := HermesScheduleInput(v, "/project"); err == nil {
				t.Fatal("accepted invalid spec")
			}
		})
	}
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", "resources:\n  review:\n    kind: hermes_schedule\n    hermes_schedule:\n      profile: mina\n      every_minutes: 60\n      prompt: Read AGENTS.md and report status locally.", 1)
	a := Analyze(declarationTestRoot(t, []byte(raw)))
	if !a.Report.OK || a.Loaded.Declaration.Resources["review"].HermesSchedule == nil {
		t.Fatalf("%+v", a.Report)
	}
	raw = strings.Replace(raw, "profile: mina", "profile: mina\n      target: main@system.health.read", 1)
	if a := Analyze(declarationTestRoot(t, []byte(raw))); a.Report.OK {
		t.Fatal("accepted capability timer in Hermes resource")
	}
	s.Target = "codex"
	s.Codex = &hermesschedules.CodexProjectSpec{Model: "gpt-6-astra", TimeoutSeconds: 120, Sandbox: "read-only"}
	if _, err := HermesScheduleInput(s, "/project"); err != nil {
		t.Fatal(err)
	}
	s.Skills = []string{"github"}
	if _, err := HermesScheduleInput(s, "/project"); err == nil {
		t.Fatal("Codex schedule accepted Hermes skills")
	}
}
