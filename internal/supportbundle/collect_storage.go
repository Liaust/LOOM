package supportbundle

import (
	"context"

	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
)

type StorageSummary struct {
	MainDocuments MainDocumentsSummary           `json:"main_documents"`
	Filesystem    storagedoctor.FilesystemStatus `json:"filesystem"`
	Retention     storagecatalog.RetentionStatus `json:"retention"`
}

type MainDocumentsSummary struct {
	BackingRoot            string `json:"backing_root,omitempty"`
	RetentionRoot          string `json:"retention_root,omitempty"`
	Exists                 bool   `json:"exists"`
	FilesDiscovered        int64  `json:"files_discovered"`
	FilesAccepted          int64  `json:"files_accepted"`
	FilesRemaining         int64  `json:"files_remaining"`
	FilesFailed            int64  `json:"files_failed"`
	FilesMissingCataloged  int64  `json:"files_missing_cataloged"`
	ByteBudgetExhausted    bool   `json:"byte_budget_exhausted,omitempty"`
	RuntimeBudgetExhausted bool   `json:"runtime_budget_exhausted,omitempty"`
}

func collectStorage(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := storageClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	mainDocuments, err := client.GetMainDocumentsStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	filesystemStatus, err := client.GetStorageFilesystemStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	retention, err := client.GetStorageRetentionStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := StorageSummary{
		MainDocuments: MainDocumentsSummary{
			BackingRoot:            mainDocuments.Data.BackingRoot,
			RetentionRoot:          mainDocuments.Data.RetentionRoot,
			Exists:                 mainDocuments.Data.Exists,
			FilesDiscovered:        mainDocuments.Data.FilesDiscovered,
			FilesAccepted:          mainDocuments.Data.FilesAccepted,
			FilesRemaining:         mainDocuments.Data.FilesRemaining,
			FilesFailed:            mainDocuments.Data.FilesFailed,
			FilesMissingCataloged:  mainDocuments.Data.FilesMissingCataloged,
			ByteBudgetExhausted:    mainDocuments.Data.ByteBudgetExhausted,
			RuntimeBudgetExhausted: mainDocuments.Data.RuntimeBudgetExhausted,
		},
		Filesystem: filesystemStatus.Data,
		Retention:  retention.Data,
	}
	return jsonSummary("summaries/storage.json", summary, PrivacyDiagnosticSummary)
}
