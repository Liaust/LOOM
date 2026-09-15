package supportbundle

import "context"

func collectDatabase(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := coreClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	envelope, err := client.MaintenanceDBStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	return jsonSummary("summaries/database.json", envelope.Data, PrivacyDiagnosticSummary)
}
