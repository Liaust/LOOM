package supportbundle

import "context"

func collectSetup(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := coreClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	envelope, err := client.BootstrapStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	return jsonSummary("summaries/setup.json", envelope.Data, PrivacyDiagnosticSummary)
}
