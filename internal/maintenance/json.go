package maintenance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

func normalizeJSONObject(raw json.RawMessage, field string) (json.RawMessage, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%w: %s is invalid JSON: %w", ErrInvalid, field, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: %s must be a JSON object", ErrInvalid, field)
	}
	return raw, nil
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func decodeDatabaseCompactPlan(raw json.RawMessage) (DatabaseCompactResult, bool, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return DatabaseCompactResult{}, false, nil
	}
	var direct DatabaseCompactResult
	if err := json.Unmarshal(raw, &direct); err == nil && (direct.Status != "" || direct.PlanHash != "" || !direct.CutoffAt.IsZero()) {
		computed := databaseCompactPlanHash(direct)
		if direct.PlanHash != "" && direct.PlanHash != computed {
			return DatabaseCompactResult{}, false, fmt.Errorf("%w: database compact plan hash mismatch", ErrInvalid)
		}
		direct.PlanHash = computed
		direct.PlanID = databaseCompactPlanID(direct.PlanHash)
		return direct, true, nil
	}
	var envelope struct {
		Data DatabaseCompactResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return DatabaseCompactResult{}, false, fmt.Errorf("%w: database compact plan is invalid JSON: %w", ErrInvalid, err)
	}
	if envelope.Data.Status == "" && envelope.Data.PlanHash == "" && envelope.Data.CutoffAt.IsZero() {
		return DatabaseCompactResult{}, false, fmt.Errorf("%w: database compact plan payload is missing data", ErrInvalid)
	}
	computed := databaseCompactPlanHash(envelope.Data)
	if envelope.Data.PlanHash != "" && envelope.Data.PlanHash != computed {
		return DatabaseCompactResult{}, false, fmt.Errorf("%w: database compact plan hash mismatch", ErrInvalid)
	}
	envelope.Data.PlanHash = computed
	envelope.Data.PlanID = databaseCompactPlanID(envelope.Data.PlanHash)
	return envelope.Data, true, nil
}

func databaseCompactPlanHash(result DatabaseCompactResult) string {
	type fingerprint struct {
		RecentSuccessDays       int                          `json:"recent_success_days"`
		CutoffAt                string                       `json:"cutoff_at"`
		AllowedTables           []string                     `json:"allowed_tables,omitempty"`
		RowLimits               map[string]int64             `json:"row_limits,omitempty"`
		MaxRowsPerBatch         int64                        `json:"max_rows_per_batch,omitempty"`
		MaxTotalRows            int64                        `json:"max_total_rows,omitempty"`
		Candidates              map[string]int64             `json:"candidates"`
		Policy                  map[string]string            `json:"policy,omitempty"`
		Plans                   []DatabaseRetentionPlan      `json:"plans,omitempty"`
		AuditCriticalExclusions []DatabaseRetentionExclusion `json:"audit_critical_exclusions,omitempty"`
	}
	raw, _ := json.Marshal(fingerprint{
		RecentSuccessDays:       result.RecentSuccessDays,
		CutoffAt:                result.CutoffAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		AllowedTables:           append([]string{}, result.AllowedTables...),
		RowLimits:               result.RowLimits,
		MaxRowsPerBatch:         result.MaxRowsPerBatch,
		MaxTotalRows:            result.MaxTotalRows,
		Candidates:              result.Candidates,
		Policy:                  result.Policy,
		Plans:                   result.Plans,
		AuditCriticalExclusions: result.AuditCriticalExclusions,
	})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func databaseCompactPlanID(hash string) string {
	hash = strings.TrimPrefix(strings.TrimSpace(hash), "sha256:")
	if len(hash) > 16 {
		hash = hash[:16]
	}
	if hash == "" {
		hash = "unknown"
	}
	return "dbcompact_" + hash
}
