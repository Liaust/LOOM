package knowledge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type AbsoluteTimeResolutionInput struct {
	ObservedAt time.Time
	Candidates []AbsoluteTimeCandidate
}

// ParseAbsoluteTimestamp accepts only RFC3339 instants with an explicit
// timezone or calendar dates in YYYY-MM-DD form. Date-only values resolve to
// midnight UTC so filtering and ordering remain deterministic.
func ParseAbsoluteTimestamp(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("timestamp is empty")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	if len(value) == len("2006-01-02") {
		if parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q must use RFC3339 with an explicit timezone or YYYY-MM-DD", raw)
}

func ResolveAbsoluteTime(input AbsoluteTimeResolutionInput) (AbsoluteTime, error) {
	if input.ObservedAt.IsZero() {
		return AbsoluteTime{}, invalid("observed_at is required for absolute-time resolution")
	}

	candidates, warnings, err := normalizeAbsoluteTimeCandidates(input.Candidates)
	if err != nil {
		return AbsoluteTime{}, err
	}
	candidates = append([]AbsoluteTimeCandidate(nil), candidates...)

	observedAt := input.ObservedAt.UTC()
	candidates = append(candidates, AbsoluteTimeCandidate{
		Kind:      AbsoluteTimeKindObserved,
		Basis:     AbsoluteTimeBasisObservedAtFallback,
		RawValue:  observedAt.Format(time.RFC3339Nano),
		Timestamp: &observedAt,
	})
	sortAbsoluteTimeCandidates(candidates)

	result := AbsoluteTime{
		Candidates: candidates,
		Warnings:   warnings,
	}
	for _, candidate := range candidates {
		if candidate.Timestamp == nil {
			continue
		}
		switch candidate.Kind {
		case AbsoluteTimeKindModified:
			if result.SourceModifiedAt == nil {
				value := *candidate.Timestamp
				result.SourceModifiedAt = &value
			}
		case AbsoluteTimeKindCreated:
			if result.SourceCreatedAt == nil {
				value := *candidate.Timestamp
				result.SourceCreatedAt = &value
			}
		}
		if result.RecencyAt.IsZero() {
			result.RecencyAt = *candidate.Timestamp
			result.RecencyBasis = candidate.Basis
		}
	}
	if err := ValidateAbsoluteTime(result); err != nil {
		return AbsoluteTime{}, err
	}
	return result, nil
}

func normalizeAbsoluteTimeCandidates(input []AbsoluteTimeCandidate) ([]AbsoluteTimeCandidate, []AbsoluteTimeWarning, error) {
	candidates := make([]AbsoluteTimeCandidate, 0, len(input))
	warnings := make([]AbsoluteTimeWarning, 0)
	for _, candidate := range input {
		if err := ValidateAbsoluteTimeCandidate(candidate); err != nil {
			return nil, nil, err
		}
		normalized := candidate
		if candidate.Timestamp != nil {
			if candidate.Timestamp.IsZero() {
				normalized.Timestamp = nil
			} else {
				value := candidate.Timestamp.UTC()
				normalized.Timestamp = &value
				if strings.TrimSpace(normalized.RawValue) == "" {
					normalized.RawValue = value.Format(time.RFC3339Nano)
				}
			}
		}
		if normalized.Timestamp == nil {
			value, err := ParseAbsoluteTimestamp(normalized.RawValue)
			if err != nil {
				warnings = append(warnings, AbsoluteTimeWarning{
					Code:     AbsoluteTimeWarningInvalidTimestamp,
					Basis:    normalized.Basis,
					RawValue: normalized.RawValue,
					Message:  err.Error(),
				})
			} else {
				normalized.Timestamp = &value
			}
		}
		candidates = append(candidates, normalized)
	}
	sortAbsoluteTimeCandidates(candidates)
	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Basis != warnings[j].Basis {
			return absoluteTimePriority(warnings[i].Basis, "") < absoluteTimePriority(warnings[j].Basis, "")
		}
		if warnings[i].RawValue != warnings[j].RawValue {
			return warnings[i].RawValue < warnings[j].RawValue
		}
		return warnings[i].Message < warnings[j].Message
	})
	return candidates, warnings, nil
}

func sortAbsoluteTimeCandidates(candidates []AbsoluteTimeCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareAbsoluteTimeCandidates(candidates[i], candidates[j]) < 0
	})
}

func ResolveAbsoluteTimeFromMetadata(observedAt time.Time, values ...json.RawMessage) (AbsoluteTime, error) {
	candidates := make([]AbsoluteTimeCandidate, 0)
	seen := map[string]bool{}
	for _, raw := range values {
		var decoded any
		if len(raw) == 0 || json.Unmarshal(raw, &decoded) != nil {
			continue
		}
		collectAbsoluteTimeMetadataCandidates(decoded, &candidates, seen)
	}
	return ResolveAbsoluteTime(AbsoluteTimeResolutionInput{ObservedAt: observedAt, Candidates: candidates})
}

// A current owner observation supersedes filesystem times captured with the
// immutable blob, even when its clock moved backwards. Other time bases retain
// their normal precedence; historical version resolution is unchanged.
func resolveCurrentSyncedAbsoluteTime(observedAt time.Time, metadata json.RawMessage, mtime time.Time) (AbsoluteTime, error) {
	prior, err := ResolveAbsoluteTimeFromMetadata(observedAt, metadata)
	if err != nil {
		return AbsoluteTime{}, err
	}
	candidates := make([]AbsoluteTimeCandidate, 0, len(prior.Candidates))
	for _, candidate := range prior.Candidates {
		if candidate.Kind == AbsoluteTimeKindObserved ||
			(candidate.Kind == AbsoluteTimeKindModified && candidate.Basis == AbsoluteTimeBasisSourceFilesystemMtime) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	candidates = append(candidates, AbsoluteTimeCandidate{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisSourceFilesystemMtime, Timestamp: &mtime})
	return ResolveAbsoluteTime(AbsoluteTimeResolutionInput{ObservedAt: observedAt, Candidates: candidates})
}

func collectAbsoluteTimeMetadataCandidates(value any, candidates *[]AbsoluteTimeCandidate, seen map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		appendAbsoluteTimeMetadataCandidate(typed, AbsoluteTimeKindModified, []string{"source_modified_at"}, []string{"source_modified_basis"}, AbsoluteTimeBasisSourceObjectMetadata, candidates, seen)
		appendAbsoluteTimeMetadataCandidate(typed, AbsoluteTimeKindModified, []string{"source_mtime"}, []string{"source_mtime_basis"}, AbsoluteTimeBasisSourceFilesystemMtime, candidates, seen)
		appendAbsoluteTimeMetadataCandidate(typed, AbsoluteTimeKindCreated, []string{"source_created_at"}, []string{"source_created_basis"}, AbsoluteTimeBasisSourceObjectMetadata, candidates, seen)
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectAbsoluteTimeMetadataCandidates(typed[key], candidates, seen)
		}
	case []any:
		for _, child := range typed {
			collectAbsoluteTimeMetadataCandidates(child, candidates, seen)
		}
	}
}

func appendAbsoluteTimeMetadataCandidate(metadata map[string]any, kind string, valueKeys, basisKeys []string, defaultBasis string, candidates *[]AbsoluteTimeCandidate, seen map[string]bool) {
	raw := ""
	for _, key := range valueKeys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			raw = strings.TrimSpace(value)
			break
		}
	}
	if raw == "" {
		return
	}
	basis := ""
	for _, key := range basisKeys {
		if value, ok := metadata[key].(string); ok {
			basis = strings.TrimSpace(value)
			if basis != "" {
				break
			}
		}
	}
	if !validAbsoluteTimeMetadataBasis(kind, basis) {
		basis = defaultBasis
	}
	key := kind + "\x00" + basis + "\x00" + raw
	if seen[key] {
		return
	}
	seen[key] = true
	*candidates = append(*candidates, AbsoluteTimeCandidate{Kind: kind, Basis: basis, RawValue: raw})
}

func validAbsoluteTimeMetadataBasis(kind, basis string) bool {
	candidate := AbsoluteTimeCandidate{Kind: kind, Basis: basis}
	return ValidateAbsoluteTimeCandidate(candidate) == nil
}

func absoluteTimeMetadataJSON(value AbsoluteTime) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func refineAbsoluteTime(observedAt time.Time, existing AbsoluteTime, additional []AbsoluteTimeCandidate) (AbsoluteTime, error) {
	candidates := make([]AbsoluteTimeCandidate, 0, len(existing.Candidates)+len(additional)+2)
	seen := map[string]bool{}
	appendCandidate := func(candidate AbsoluteTimeCandidate) {
		key := candidate.Kind + "\x00" + candidate.Basis + "\x00" + candidate.RawValue
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, candidate)
	}
	for _, candidate := range existing.Candidates {
		if candidate.Kind != AbsoluteTimeKindObserved && candidate.Basis != AbsoluteTimeBasisObservedAtFallback {
			appendCandidate(candidate)
		}
	}
	if len(candidates) == 0 {
		if existing.SourceModifiedAt != nil {
			basis := existing.RecencyBasis
			if !validAbsoluteTimeMetadataBasis(AbsoluteTimeKindModified, basis) {
				basis = AbsoluteTimeBasisSourceObjectMetadata
			}
			value := existing.SourceModifiedAt.UTC()
			appendCandidate(AbsoluteTimeCandidate{Kind: AbsoluteTimeKindModified, Basis: basis, RawValue: value.Format(time.RFC3339Nano), Timestamp: &value})
		}
		if existing.SourceCreatedAt != nil {
			basis := existing.RecencyBasis
			if !validAbsoluteTimeMetadataBasis(AbsoluteTimeKindCreated, basis) {
				basis = AbsoluteTimeBasisSourceObjectMetadata
			}
			value := existing.SourceCreatedAt.UTC()
			appendCandidate(AbsoluteTimeCandidate{Kind: AbsoluteTimeKindCreated, Basis: basis, RawValue: value.Format(time.RFC3339Nano), Timestamp: &value})
		}
	}
	for _, candidate := range additional {
		appendCandidate(candidate)
	}
	return ResolveAbsoluteTime(AbsoluteTimeResolutionInput{ObservedAt: observedAt, Candidates: candidates})
}

func absoluteTimeFromMetadata(raw json.RawMessage, fallback AbsoluteTime) AbsoluteTime {
	var decoded AbsoluteTime
	if len(raw) > 0 && json.Unmarshal(raw, &decoded) == nil && !decoded.RecencyAt.IsZero() {
		return decoded
	}
	return fallback
}

func compareAbsoluteTimeCandidates(left, right AbsoluteTimeCandidate) int {
	leftPriority := absoluteTimePriority(left.Basis, left.Kind)
	rightPriority := absoluteTimePriority(right.Basis, right.Kind)
	if leftPriority < rightPriority {
		return -1
	}
	if leftPriority > rightPriority {
		return 1
	}
	if left.Timestamp != nil && right.Timestamp == nil {
		return -1
	}
	if left.Timestamp == nil && right.Timestamp != nil {
		return 1
	}
	if left.Timestamp != nil && right.Timestamp != nil && !left.Timestamp.Equal(*right.Timestamp) {
		if left.Kind == AbsoluteTimeKindCreated {
			if left.Timestamp.Before(*right.Timestamp) {
				return -1
			}
			return 1
		}
		if left.Timestamp.After(*right.Timestamp) {
			return -1
		}
		return 1
	}
	if left.Basis < right.Basis {
		return -1
	}
	if left.Basis > right.Basis {
		return 1
	}
	if left.RawValue < right.RawValue {
		return -1
	}
	if left.RawValue > right.RawValue {
		return 1
	}
	return 0
}

func absoluteTimePriority(basis string, kind string) int {
	switch basis {
	case AbsoluteTimeBasisSourceFilesystemMtime:
		return 0
	case AbsoluteTimeBasisSourceObjectMetadata:
		if kind == AbsoluteTimeKindCreated {
			return 5
		}
		return 1
	case AbsoluteTimeBasisFrontmatterUpdatedAt:
		return 2
	case AbsoluteTimeBasisEmbeddedModifiedAt:
		return 3
	case AbsoluteTimeBasisSourceFilesystemBirthtime:
		return 4
	case AbsoluteTimeBasisFrontmatterCreatedAt:
		return 6
	case AbsoluteTimeBasisEmbeddedCreatedAt:
		return 7
	case AbsoluteTimeBasisObservedAtFallback:
		return 8
	default:
		return 100
	}
}
