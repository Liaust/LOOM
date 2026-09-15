package actions

import "testing"

func TestDefaultRegistryFindsCoreActions(t *testing.T) {
	registry := DefaultRegistry()
	for _, id := range []string{
		"home.open",
		"doctor.open",
		"background.open",
		"storage.open",
		"automations.open",
		"jobs_search.open",
		"nodes_watched_roots.open",
		"raw.workers.list",
	} {
		if _, ok := registry.Get(id); !ok {
			t.Fatalf("missing action %s", id)
		}
	}
}

func TestRegistrySearchReturnsExpectedAction(t *testing.T) {
	registry := DefaultRegistry()
	for query, want := range map[string]string{
		"workers":      "background.open",
		"doctor":       "doctor.open",
		"repair":       "doctor.open",
		"automation":   "automations.open",
		"direct event": "automations.open",
		"storage":      "storage.open",
		"watched root": "nodes_watched_roots.open",
		"backup":       "nodes_watched_roots.open",
		"index":        "jobs_search.open",
	} {
		results := registry.Search(query, 1)
		if len(results) != 1 {
			t.Fatalf("query %q returned no results", query)
		}
		if got := results[0].Action.ID; got != want {
			t.Fatalf("query %q returned %s, want %s", query, got, want)
		}
	}
}
