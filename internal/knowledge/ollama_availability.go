package knowledge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Availability is observation, not configuration or permission to run a stage.
func ollamaModelAvailability(ctx context.Context, endpoint string) (bool, map[string]bool) {
	models := map[string]bool{}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return false, models
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return false, models
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, models
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, models
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tags); err != nil || tags.Models == nil {
		return false, models
	}
	for _, model := range tags.Models {
		if name := ollamaModelName(model.Name); name != "" {
			models[name] = true
		}
	}
	return true, models
}

func ollamaModelName(name string) string {
	name = strings.TrimSpace(name)
	if name != "" && !strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
		name += ":latest"
	}
	return name
}
