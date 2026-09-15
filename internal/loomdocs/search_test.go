package loomdocs

import "testing"

func TestSearchBoostsExactTitleAndAlias(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	response, err := Search(corpus, "Projects And Scopes", SearchFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].RelativePath != "projects.md" || !containsString(response.Results[0].Matches, "exact title") {
		t.Fatalf("unexpected exact-title results: %#v", response.Results)
	}
	response, err = Search(corpus, "Project Lifecycle", SearchFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].RelativePath != "projects.md" || !containsString(response.Results[0].Matches, "exact alias") {
		t.Fatalf("unexpected exact-alias results: %#v", response.Results)
	}
}

func TestSearchFiltersAndStableTieOrder(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	response, err := Search(corpus, "loom", SearchFilters{Audience: "operator", Status: "draft", Tag: "storage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].RelativePath != "storage.md" {
		t.Fatalf("filtered results: %#v", response.Results)
	}

	tieCorpus := &Corpus{Documents: []Document{
		{RelativePath: "b.md", Title: "Beta", Body: "same token"},
		{RelativePath: "a.md", Title: "Alfa", Body: "same token"},
	}}
	response, err = Search(tieCorpus, "same", SearchFilters{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || response.Results[0].RelativePath > response.Results[1].RelativePath {
		t.Fatalf("unstable tie order: %#v", response.Results)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
