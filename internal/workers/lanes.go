package workers

import (
	"context"
	"strings"
	"sync"
)

var supervisorLanes = []string{"jobs", "builds", "notes", "control", "knowledge", "heavy", "background"}

var supervisorLaneKinds = map[string][]string{
	"jobs":      {KindJobRunner},
	"builds":    {KindApplicationBuilder},
	"notes":     {"notes_workspace_sync"},
	"control":   {KindAutomationScheduler, KindAutomationDispatcher, KindDirectEventIngest, KindJobSweeper, KindPolicyExpiry, KindRealtimeExpiry},
	"knowledge": {KindKnowledgeIndexer},
	"heavy":     {KindKnowledgeHeavy, KindKnowledgeEmbedder},
}

func workerLane(kind string) string {
	for _, lane := range supervisorLanes {
		for _, candidate := range supervisorLaneKinds[lane] {
			if candidate == kind {
				return lane
			}
		}
	}
	return "background"
}

// Generate both SQL selectors from the same closed kind map as wake routing.
func workerLaneSQL() string {
	var query strings.Builder
	query.WriteString("CASE")
	for _, lane := range supervisorLanes {
		if len(supervisorLaneKinds[lane]) == 0 {
			continue
		}
		query.WriteString(" WHEN worker_kind IN ('")
		query.WriteString(strings.Join(supervisorLaneKinds[lane], "','"))
		query.WriteString("') THEN '")
		query.WriteString(lane)
		query.WriteString("'")
	}
	query.WriteString(" ELSE 'background' END")
	return query.String()
}

func runSupervisorLanes(ctx context.Context, run func(context.Context, string)) {
	var done sync.WaitGroup
	for _, lane := range supervisorLanes {
		done.Add(1)
		go func() {
			defer done.Done()
			run(ctx, lane)
		}()
	}
	done.Wait()
}
