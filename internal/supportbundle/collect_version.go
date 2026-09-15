package supportbundle

import (
	"context"

	"loom.local/loom/internal/version"
)

func collectVersion(context.Context, CollectionContext) (CollectorOutput, error) {
	return jsonSummary("summaries/version.json", version.Current(), PrivacyMetadata)
}
