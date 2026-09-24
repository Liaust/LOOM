package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// NormalizeKnowledgeSourcePolicy validates intent without enabling host services.
func NormalizeKnowledgeSourcePolicy(k KnowledgeDeclaration) (*KnowledgeSourcePolicy, error) {
	if k.Refresh == nil && k.Processing == nil {
		return nil, nil
	}
	out := &KnowledgeSourcePolicy{}
	if k.Refresh != nil {
		quiet, err := knowledgeDuration(k.Refresh.QuietFor, 10*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("knowledge.refresh.quiet_for: %w", err)
		}
		maximum, err := knowledgeDuration(k.Refresh.MaxWait, 30*time.Minute)
		if err != nil || maximum <= 0 || maximum < quiet {
			return nil, fmt.Errorf("knowledge.refresh.max_wait must be positive, at least quiet_for and at most 24h")
		}
		out.Refresh = &KnowledgeRefreshPolicy{QuietForSeconds: int64(quiet / time.Second), MaxWaitSeconds: int64(maximum / time.Second)}
	}
	if k.Processing != nil {
		p := *k.Processing
		if p.OCR != "" && p.OCR != "auto" && p.OCR != "off" {
			return nil, fmt.Errorf("knowledge.processing.ocr must be auto or off")
		}
		out.Processing = &p
	}
	return out, nil
}

// DecodeKnowledgeSourcePolicy accepts only normalized compiler evidence.
func DecodeKnowledgeSourcePolicy(raw []byte) (KnowledgeSourcePolicy, error) {
	value, err := DecodeDeclarationEvidenceJSON(raw)
	if err != nil {
		return KnowledgeSourcePolicy{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return KnowledgeSourcePolicy{}, fmt.Errorf("knowledge policy must be an object")
	}
	if refresh, exists := fields["refresh"]; exists {
		r, ok := refresh.(map[string]any)
		if !ok || r["quiet_for_seconds"] == nil || r["max_wait_seconds"] == nil {
			return KnowledgeSourcePolicy{}, fmt.Errorf("normalized refresh requires quiet_for_seconds and max_wait_seconds")
		}
	}
	if processing, exists := fields["processing"]; exists {
		if _, ok := processing.(map[string]any); !ok {
			return KnowledgeSourcePolicy{}, fmt.Errorf("knowledge processing must be an object")
		}
	}
	var policy KnowledgeSourcePolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if r := policy.Refresh; r != nil {
		if r.QuietForSeconds < 0 || r.MaxWaitSeconds <= 0 || r.MaxWaitSeconds < r.QuietForSeconds || r.MaxWaitSeconds > 86400 {
			return policy, fmt.Errorf("invalid normalized knowledge refresh policy")
		}
	}
	if p := policy.Processing; p != nil && p.OCR != "" && p.OCR != "auto" && p.OCR != "off" {
		return policy, fmt.Errorf("invalid normalized knowledge OCR policy")
	}
	return policy, nil
}

func knowledgeDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 0 || duration > 24*time.Hour || duration%time.Second != 0 {
		return 0, fmt.Errorf("expected a whole-second duration from 0s through 24h")
	}
	return duration, nil
}
