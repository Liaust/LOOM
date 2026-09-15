package supportbundle

import (
	"context"
	"os"
)

type NodeSummary struct {
	Runtime RuntimeInfo `json:"runtime"`
}

func collectNode(_ context.Context, collection CollectionContext) (CollectorOutput, error) {
	runtime := collection.Options.Runtime
	if runtime.Hostname == "" {
		if hostname, err := os.Hostname(); err == nil {
			runtime.Hostname = hostname
		}
	}
	return jsonSummary("summaries/node.json", NodeSummary{Runtime: runtime}, PrivacyMetadata)
}
