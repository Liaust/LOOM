package supportbundle

import (
	"fmt"
)

func jsonSummary(path string, value any, privacyClass string) (CollectorOutput, error) {
	data, err := marshalJSON(value)
	if err != nil {
		return CollectorOutput{}, err
	}
	return CollectorOutput{Files: []File{{
		Path:         path,
		ContentType:  "application/json",
		PrivacyClass: firstNonEmpty(privacyClass, PrivacyDiagnosticSummary),
		Data:         data,
	}}}, nil
}

func coreClient(ctx CollectionContext) (CoreClient, error) {
	if ctx.Options.CoreClient == nil {
		return nil, fmt.Errorf("local client unavailable")
	}
	return ctx.Options.CoreClient, nil
}

func workersClient(ctx CollectionContext) (WorkersClient, error) {
	client, ok := ctx.Options.CoreClient.(WorkersClient)
	if !ok {
		return nil, fmt.Errorf("workers client unavailable")
	}
	return client, nil
}

func jobsClient(ctx CollectionContext) (JobsClient, error) {
	client, ok := ctx.Options.CoreClient.(JobsClient)
	if !ok {
		return nil, fmt.Errorf("jobs client unavailable")
	}
	return client, nil
}

func notesClient(ctx CollectionContext) (NotesClient, error) {
	client, ok := ctx.Options.CoreClient.(NotesClient)
	if !ok {
		return nil, fmt.Errorf("notes client unavailable")
	}
	return client, nil
}

func storageClient(ctx CollectionContext) (StorageClient, error) {
	client, ok := ctx.Options.CoreClient.(StorageClient)
	if !ok {
		return nil, fmt.Errorf("storage client unavailable")
	}
	return client, nil
}

func backupClient(ctx CollectionContext) (BackupClient, error) {
	client, ok := ctx.Options.CoreClient.(BackupClient)
	if !ok {
		return nil, fmt.Errorf("backup client unavailable")
	}
	return client, nil
}

func projectsClient(ctx CollectionContext) (ProjectsClient, error) {
	client, ok := ctx.Options.CoreClient.(ProjectsClient)
	if !ok {
		return nil, fmt.Errorf("projects client unavailable")
	}
	return client, nil
}

func correlationID(ctx CollectionContext) string {
	return ctx.Options.CorrelationID
}

func itemLimit(opts Options) int {
	if opts.MaxItems <= 0 {
		return DefaultMaxItems
	}
	return opts.MaxItems
}

func limitItems[T any](items []T, limit int) []T {
	if limit <= 0 {
		limit = DefaultMaxItems
	}
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}
