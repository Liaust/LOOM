package supportbundle

import "context"

func collectStatus(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := coreClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	envelope, err := client.Status(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	return jsonSummary("summaries/status.json", envelope.Data, PrivacyDiagnosticSummary)
}
