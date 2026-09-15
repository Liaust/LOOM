package ui

import "testing"

func TestRankFuzzyFindsExpectedOperationalActions(t *testing.T) {
	candidates := []Candidate{
		{ID: "background.open", Title: "Background Operations", Domain: "workers", Keywords: []string{"worker", "workers", "maintenance"}},
		{ID: "automations.open", Title: "Automation Center", Domain: "automation", Keywords: []string{"schedule", "direct event", "gmail"}},
		{ID: "watched_roots.open", Title: "Nodes And Watched Roots", Domain: "nodes", Keywords: []string{"watched root", "backup", "sync"}},
		{ID: "jobs_search.open", Title: "Jobs", Domain: "jobs", Keywords: []string{"index", "indexes", "jobs"}},
	}
	for query, want := range map[string]string{
		"worker":       "background.open",
		"automation":   "automations.open",
		"gmail":        "automations.open",
		"watched root": "watched_roots.open",
		"backup":       "watched_roots.open",
		"index":        "jobs_search.open",
	} {
		results := RankFuzzy(query, candidates, 1)
		if len(results) != 1 {
			t.Fatalf("query %q returned no results", query)
		}
		if got := results[0].Candidate.ID; got != want {
			t.Fatalf("query %q top result = %s, want %s", query, got, want)
		}
	}
}

func TestRankFuzzyUsesAttentionBoost(t *testing.T) {
	candidates := []Candidate{
		{ID: "worker.normal", Title: "Worker Normal", Keywords: []string{"worker"}},
		{ID: "worker.attention", Title: "Worker Attention", Keywords: []string{"worker"}, Attention: 50},
	}
	results := RankFuzzy("worker", candidates, 2)
	if got := results[0].Candidate.ID; got != "worker.attention" {
		t.Fatalf("top result = %s, want worker.attention", got)
	}
}
