package supportbundle

import "context"

func collectHealth(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := coreClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	envelope, err := client.Health(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	return jsonSummary("summaries/health.json", envelope.Data, PrivacyDiagnosticSummary)
}
