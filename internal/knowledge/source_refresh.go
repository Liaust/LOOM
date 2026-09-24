package knowledge

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/projectcontracts"
)

// This clock belongs to source admission, not pipeline progress or file mtime.
// Metadata keeps the additive declaration controls independent of schema rollout.
type sourceRefreshClock struct {
	LastContentChangeAt time.Time  `json:"last_content_change_at"`
	PendingSince        *time.Time `json:"pending_since,omitempty"`
}

func objectRefreshClock(object KnowledgeObject) sourceRefreshClock {
	var m struct {
		Clock sourceRefreshClock `json:"source_refresh"`
	}
	_ = json.Unmarshal(object.Metadata, &m)
	return m.Clock
}

func observeSourceRefresh(existing *KnowledgeObject, next KnowledgeObject, now time.Time) KnowledgeObject {
	policy, err := knowledgeSourcePolicy(next)
	if err != nil || policy == nil || policy.Refresh == nil {
		return next
	}
	// Eligibility is persisted as a PostgreSQL timestamp; keep the JSON clock
	// on the same precision so inspection and replay agree exactly.
	now = now.UTC().Truncate(time.Microsecond)
	clock := sourceRefreshClock{}
	if existing != nil {
		clock = objectRefreshClock(*existing)
	}
	changed := existing == nil || existing.SourceHash != next.SourceHash ||
		(next.SourceHash == "" && existing.SourceRevision != next.SourceRevision) ||
		(existing.DeletedAt != nil && next.DeletedAt == nil)
	if clock.LastContentChangeAt.IsZero() || changed {
		clock.LastContentChangeAt = now.UTC()
		if clock.PendingSince == nil {
			t := now.UTC()
			clock.PendingSince = &t
		}
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(next.Metadata, &metadata)
	if metadata == nil {
		metadata = map[string]json.RawMessage{}
	}
	metadata["source_refresh"], _ = json.Marshal(clock)
	next.Metadata, _ = json.Marshal(metadata)
	return next
}

func sourceRefreshEligibleAt(object KnowledgeObject, policy projectcontracts.KnowledgeRefreshPolicy, now time.Time) time.Time {
	clock := objectRefreshClock(object)
	changed := clock.LastContentChangeAt
	if changed.IsZero() {
		changed = now
	}
	first := changed
	if clock.PendingSince != nil {
		first = *clock.PendingSince
	}
	quiet := changed.Add(time.Duration(policy.QuietForSeconds) * time.Second)
	maximum := first.Add(time.Duration(policy.MaxWaitSeconds) * time.Second)
	if maximum.Before(quiet) {
		return maximum
	}
	return quiet
}
