package storagefidelity

import "time"

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

const (
	DecisionSafe          = "safe"
	DecisionNotSafe       = "not_safe"
	DecisionPartiallySafe = "partially_safe"
	DecisionUnknown       = "unknown"
)

type Finding struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
	Blocking bool   `json:"blocking"`
}

type Evaluation struct {
	StorageEntryID  string    `json:"storage_entry_id,omitempty"`
	ViewPath        string    `json:"view_path,omitempty"`
	FileClass       string    `json:"file_class,omitempty"`
	ProcessingState string    `json:"processing_state,omitempty"`
	Availability    string    `json:"availability_state,omitempty"`
	RetentionState  string    `json:"retention_state,omitempty"`
	Severity        string    `json:"severity"`
	Decision        string    `json:"decision"`
	SafeToDelete    bool      `json:"safe_to_delete"`
	PayloadRetained bool      `json:"payload_retained"`
	CloudVerified   bool      `json:"cloud_verified"`
	Findings        []Finding `json:"findings,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
}

type Report struct {
	Prefix      string       `json:"prefix,omitempty"`
	Node        string       `json:"node,omitempty"`
	Items       []Evaluation `json:"items,omitempty"`
	Summary     Summary      `json:"summary"`
	GeneratedAt time.Time    `json:"generated_at"`
}

type Summary struct {
	Items         int `json:"items"`
	Errors        int `json:"errors"`
	Warnings      int `json:"warnings"`
	Info          int `json:"info"`
	Safe          int `json:"safe"`
	PartiallySafe int `json:"partially_safe"`
	NotSafe       int `json:"not_safe"`
	Unknown       int `json:"unknown"`
}
