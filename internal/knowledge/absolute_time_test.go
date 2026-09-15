package knowledge

import (
	"reflect"
	"testing"
	"time"
)

func TestParseAbsoluteTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "UTC", raw: "2026-08-17T12:30:45Z", want: "2026-08-17T12:30:45Z"},
		{name: "offset normalized", raw: "2026-08-17T14:30:45+02:00", want: "2026-08-17T12:30:45Z"},
		{name: "date only", raw: "2026-08-17", want: "2026-08-17T00:00:00Z"},
		{name: "ambiguous slashes", raw: "17/08/2026", wantErr: true},
		{name: "missing timezone", raw: "2026-08-17T12:30:45", wantErr: true},
		{name: "invalid calendar date", raw: "2026-02-30", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAbsoluteTimestamp(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseAbsoluteTimestamp(%q) unexpectedly succeeded", test.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAbsoluteTimestamp(%q): %v", test.raw, err)
			}
			if got.Format(time.RFC3339) != test.want {
				t.Fatalf("ParseAbsoluteTimestamp(%q) = %s, want %s", test.raw, got.Format(time.RFC3339), test.want)
			}
		})
	}
}

func TestResolveAbsoluteTimeSelection(t *testing.T) {
	t.Parallel()

	observed := mustAbsoluteTime(t, "2026-08-17T12:00:00Z")
	tests := []struct {
		name         string
		candidates   []AbsoluteTimeCandidate
		wantCreated  string
		wantModified string
		wantRecency  string
		wantBasis    string
		wantWarnings int
	}{
		{
			name: "filesystem mtime wins over newer embedded and observation",
			candidates: []AbsoluteTimeCandidate{
				{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisEmbeddedModifiedAt, RawValue: "2026-08-16T12:00:00Z"},
				{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisSourceFilesystemMtime, RawValue: "2025-01-02T03:04:05Z"},
			},
			wantModified: "2025-01-02T03:04:05Z",
			wantRecency:  "2025-01-02T03:04:05Z",
			wantBasis:    AbsoluteTimeBasisSourceFilesystemMtime,
		},
		{
			name: "frontmatter modification fills missing source mtime",
			candidates: []AbsoluteTimeCandidate{
				{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisFrontmatterUpdatedAt, RawValue: "2025-02-03T04:05:06+01:00"},
				{Kind: AbsoluteTimeKindCreated, Basis: AbsoluteTimeBasisSourceFilesystemBirthtime, RawValue: "2024-01-01T00:00:00Z"},
			},
			wantCreated:  "2024-01-01T00:00:00Z",
			wantModified: "2025-02-03T03:05:06Z",
			wantRecency:  "2025-02-03T03:05:06Z",
			wantBasis:    AbsoluteTimeBasisFrontmatterUpdatedAt,
		},
		{
			name: "creation fills missing modification",
			candidates: []AbsoluteTimeCandidate{
				{Kind: AbsoluteTimeKindCreated, Basis: AbsoluteTimeBasisEmbeddedCreatedAt, RawValue: "2020-05-06"},
			},
			wantCreated: "2020-05-06T00:00:00Z",
			wantRecency: "2020-05-06T00:00:00Z",
			wantBasis:   AbsoluteTimeBasisEmbeddedCreatedAt,
		},
		{
			name:        "observation fallback is explicit",
			wantRecency: "2026-08-17T12:00:00Z",
			wantBasis:   AbsoluteTimeBasisObservedAtFallback,
		},
		{
			name: "invalid raw candidate is retained as a warning",
			candidates: []AbsoluteTimeCandidate{
				{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisFrontmatterUpdatedAt, RawValue: "07/08/26"},
			},
			wantRecency:  "2026-08-17T12:00:00Z",
			wantBasis:    AbsoluteTimeBasisObservedAtFallback,
			wantWarnings: 1,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveAbsoluteTime(AbsoluteTimeResolutionInput{ObservedAt: observed, Candidates: test.candidates})
			if err != nil {
				t.Fatalf("ResolveAbsoluteTime: %v", err)
			}
			assertOptionalAbsoluteTime(t, "source_created_at", got.SourceCreatedAt, test.wantCreated)
			assertOptionalAbsoluteTime(t, "source_modified_at", got.SourceModifiedAt, test.wantModified)
			if formatted := got.RecencyAt.Format(time.RFC3339); formatted != test.wantRecency {
				t.Fatalf("recency_at = %s, want %s", formatted, test.wantRecency)
			}
			if got.RecencyBasis != test.wantBasis {
				t.Fatalf("recency_basis = %q, want %q", got.RecencyBasis, test.wantBasis)
			}
			if len(got.Warnings) != test.wantWarnings {
				t.Fatalf("warnings = %#v, want %d", got.Warnings, test.wantWarnings)
			}
			if test.wantWarnings > 0 {
				if got.Candidates[0].RawValue != "07/08/26" || got.Warnings[0].RawValue != "07/08/26" {
					t.Fatalf("invalid raw value was not retained: candidates=%#v warnings=%#v", got.Candidates, got.Warnings)
				}
			}
		})
	}
}

func TestResolveAbsoluteTimeCandidateAuthorityAndDeterminism(t *testing.T) {
	t.Parallel()

	observed := mustAbsoluteTime(t, "2026-08-17T12:00:00Z")
	newerEmbedded := AbsoluteTimeCandidate{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisEmbeddedModifiedAt, RawValue: "2026-01-01T00:00:00Z"}
	olderFrontmatter := AbsoluteTimeCandidate{Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisFrontmatterUpdatedAt, RawValue: "2020-01-01T00:00:00Z"}
	inputs := [][]AbsoluteTimeCandidate{
		{newerEmbedded, olderFrontmatter},
		{olderFrontmatter, newerEmbedded},
	}
	var first AbsoluteTime
	for index, candidates := range inputs {
		got, err := ResolveAbsoluteTime(AbsoluteTimeResolutionInput{ObservedAt: observed, Candidates: candidates})
		if err != nil {
			t.Fatalf("ResolveAbsoluteTime: %v", err)
		}
		if got.RecencyBasis != AbsoluteTimeBasisFrontmatterUpdatedAt {
			t.Fatalf("recency_basis = %q, want frontmatter authority", got.RecencyBasis)
		}
		if index == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("resolution depends on input order:\nfirst=%#v\nsecond=%#v", first, got)
		}
	}
}

func TestResolveAbsoluteTimeAcceptsNormalizedFormatCandidateAndPreservesRaw(t *testing.T) {
	t.Parallel()

	observed := mustAbsoluteTime(t, "2026-08-17T12:00:00Z")
	normalized := mustAbsoluteTime(t, "2022-03-04T05:06:07Z")
	got, err := ResolveAbsoluteTime(AbsoluteTimeResolutionInput{
		ObservedAt: observed,
		Candidates: []AbsoluteTimeCandidate{{
			Kind:      AbsoluteTimeKindModified,
			Basis:     AbsoluteTimeBasisEmbeddedModifiedAt,
			RawValue:  "D:20220304050607Z",
			Timestamp: &normalized,
		}},
	})
	if err != nil {
		t.Fatalf("ResolveAbsoluteTime: %v", err)
	}
	if got.RecencyAt != normalized || got.Candidates[0].RawValue != "D:20220304050607Z" {
		t.Fatalf("normalized candidate = %#v", got)
	}
}

func TestResolveAbsoluteTimeFromNestedMetadata(t *testing.T) {
	t.Parallel()

	observed := mustAbsoluteTime(t, "2026-08-17T12:00:00Z")
	got, err := ResolveAbsoluteTimeFromMetadata(observed, mustJSON(t, map[string]any{
		"storage_metadata": map[string]any{
			"source_mtime":      "2021-03-04T05:06:07+02:00",
			"source_created_at": "2020-01-02",
		},
		"unrelated": map[string]any{
			"modified_at": "1999-01-01T00:00:00Z",
			"date":        "1998-01-01",
		},
	}))
	if err != nil {
		t.Fatalf("ResolveAbsoluteTimeFromMetadata: %v", err)
	}
	assertOptionalAbsoluteTime(t, "source_created_at", got.SourceCreatedAt, "2020-01-02T00:00:00Z")
	assertOptionalAbsoluteTime(t, "source_modified_at", got.SourceModifiedAt, "2021-03-04T03:06:07Z")
	if got.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("recency_basis = %q, want legacy source_mtime authority", got.RecencyBasis)
	}
}

func TestResolveAbsoluteTimeFromMetadataPreservesInvalidRawWarning(t *testing.T) {
	t.Parallel()

	observed := mustAbsoluteTime(t, "2026-08-17T12:00:00Z")
	got, err := ResolveAbsoluteTimeFromMetadata(observed, mustJSON(t, map[string]any{
		"source_modified_at":    "last Tuesday",
		"source_modified_basis": AbsoluteTimeBasisSourceFilesystemMtime,
	}))
	if err != nil {
		t.Fatalf("ResolveAbsoluteTimeFromMetadata: %v", err)
	}
	if got.RecencyBasis != AbsoluteTimeBasisObservedAtFallback || len(got.Warnings) != 1 {
		t.Fatalf("resolution = %#v, want observation fallback and one warning", got)
	}
	if got.Warnings[0].RawValue != "last Tuesday" || got.Warnings[0].Basis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("warning = %#v, want raw invalid value and basis", got.Warnings[0])
	}
}

func TestResolveAbsoluteTimeRejectsInvalidContract(t *testing.T) {
	t.Parallel()

	observed := mustAbsoluteTime(t, "2026-08-17T12:00:00Z")
	_, err := ResolveAbsoluteTime(AbsoluteTimeResolutionInput{
		ObservedAt: observed,
		Candidates: []AbsoluteTimeCandidate{{
			Kind:     AbsoluteTimeKindCreated,
			Basis:    AbsoluteTimeBasisSourceFilesystemMtime,
			RawValue: "2026-01-01T00:00:00Z",
		}},
	})
	if err == nil {
		t.Fatal("ResolveAbsoluteTime unexpectedly accepted a mismatched kind and basis")
	}
}

func mustAbsoluteTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", value, err)
	}
	return parsed
}

func assertOptionalAbsoluteTime(t *testing.T, name string, got *time.Time, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Fatalf("%s = %s, want nil", name, got.Format(time.RFC3339))
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want %s", name, want)
	}
	if formatted := got.Format(time.RFC3339); formatted != want {
		t.Fatalf("%s = %s, want %s", name, formatted, want)
	}
}
