package knowledge

import "time"

type EmbeddingQueueCounts struct {
	Queued             int `json:"queued"`
	Ready              int `json:"ready"`
	Processing         int `json:"processing"`
	Complete           int `json:"complete"`
	Stale              int `json:"stale"`
	Failed             int `json:"failed"`
	DisabledByPolicy   int `json:"disabled_by_policy"`
	SkippedUnsupported int `json:"skipped_unsupported"`
}

type EmbeddingObjectCounts struct {
	NotStarted         int `json:"not_started"`
	Queued             int `json:"queued"`
	Processing         int `json:"processing"`
	Complete           int `json:"complete"`
	Stale              int `json:"stale"`
	Failed             int `json:"failed"`
	DisabledByPolicy   int `json:"disabled_by_policy"`
	SkippedUnsupported int `json:"skipped_unsupported"`
}

type EmbeddingStatus struct {
	Settings        EmbeddingSettings     `json:"settings"`
	Queue           EmbeddingQueueCounts  `json:"queue"`
	Objects         EmbeddingObjectCounts `json:"objects"`
	ActiveVectors   int                   `json:"active_vectors"`
	Historical      int                   `json:"historical_vectors"`
	ReusableVectors int                   `json:"reusable_vectors"`
	GeneratedAt     time.Time             `json:"generated_at"`
}

type SetEmbeddingsEnabledResult struct {
	Settings EmbeddingSettings `json:"settings"`
	Queued   int               `json:"queued"`
	Status   EmbeddingStatus   `json:"status"`
}
