package projectcontracts

import (
	"path/filepath"
	"testing"
)

func TestAgenticScheduleExamples(t *testing.T) {
	for _, name := range []string{"codex-project", "hermes-project"} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.Abs(filepath.Join("..", "..", "examples", "agentic-schedules", name))
			if err != nil {
				t.Fatal(err)
			}
			a := Analyze(root)
			if !a.Report.OK {
				t.Fatalf("invalid example: %+v", a.Report)
			}
			if name == "codex-project" {
				if len(a.Plan.Schedules) != 1 {
					t.Fatalf("schedule discovery: %+v", a.Plan)
				}
			}
		})
	}
}
