package provenance

import (
	"encoding/json"
	"strings"
	"testing"
)

func exactSourcesFixture() SourceReference {
	locator, version, digest := "/private/source?token=synthetic\n\x1b[31m界", "exact-original-version", "sha256:"+strings.Repeat("a", 64)
	return SourceReference{ID: semanticID(1), SchemaVersion: "1.0", SourceKind: "notes_passage", Status: "resolved", VerificationPosture: "content_verified", ResolverName: "stored_fixture", ResolverVersion: "1", ResolutionAt: fixedClock(), CanonicalLocator: &locator, VersionAddress: &version, ContentDigest: &digest}
}

func TestExactSourcesAllowlistAndClosedVariants(t *testing.T) {
	source := exactSourcesFixture()
	source.Submitted = json.RawMessage(`{"submitted_marker":"excluded"}`)
	source.Payload = json.RawMessage(`{"payload_marker":"excluded"}`)
	got, err := projectLinkedSources("candidate", semanticID(2), []SourceReference{source}, 16)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "_marker") || strings.Contains(string(encoded), "source_context") {
		t.Fatalf("unexpected disclosure: %s", encoded)
	}
	var roundtrip LinkedSources
	if err := json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.Items[0].StoredSourceReceipt == nil || *roundtrip.Items[0].CanonicalLocator != *source.CanonicalLocator || *roundtrip.Items[0].VersionAddress != *source.VersionAddress || *roundtrip.Items[0].ContentDigest != *source.ContentDigest || roundtrip.IncompleteItems != 0 || roundtrip.SourcesTruncated || roundtrip.Posture != "stored_resolution" {
		t.Fatalf("changed stored identity: %#v", roundtrip)
	}
	var keys map[string]json.RawMessage
	item, _ := json.Marshal(got.Items[0])
	_ = json.Unmarshal(item, &keys)
	for _, key := range []string{"source_reference_id", "schema_version", "source_kind", "status", "verification_posture", "resolver_name", "resolver_version", "resolution_at", "canonical_locator", "version_address", "content_digest"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing %s", key)
		}
		delete(keys, key)
	}
	if len(keys) != 0 {
		t.Fatalf("unexpected fields: %v", keys)
	}
	source.CanonicalLocator = nil
	source.VersionAddress = nil
	source.ContentDigest = nil
	got, err = projectLinkedSources("record", semanticID(2), []SourceReference{source}, 16)
	if err != nil || got.Items[0].CanonicalLocator != nil {
		t.Fatalf("absent identity invented: %#v %v", got, err)
	}
}

func TestExactSourcesInvalidEncoding(t *testing.T) {
	for _, field := range []string{"schema", "kind", "status", "verification", "resolver", "resolver_version", "locator", "version", "digest", "gap"} {
		t.Run(field, func(t *testing.T) {
			source := exactSourcesFixture()
			bad := string([]byte{0xff})
			switch field {
			case "schema":
				source.SchemaVersion = bad
			case "kind":
				source.SourceKind = bad
			case "status":
				source.Status = bad
			case "verification":
				source.VerificationPosture = bad
			case "resolver":
				source.ResolverName = bad
			case "resolver_version":
				source.ResolverVersion = bad
			case "locator":
				source.CanonicalLocator = &bad
			case "version":
				source.VersionAddress = &bad
			case "digest":
				source.ContentDigest = &bad
			case "gap":
				source.GapReason = &bad
			}
			got, err := projectLinkedSources("candidate", semanticID(2), []SourceReference{source}, 16)
			if err != nil || got.Returned != 1 || got.IncompleteItems != 1 || got.SourcesTruncated {
				t.Fatalf("bad unavailable truth: %#v %v", got, err)
			}
			encoded, _ := json.Marshal(got.Items[0])
			expected := `{"source_reference_id":"` + string(source.ID) + `","receipt_unavailable":"invalid_encoding"}`
			if string(encoded) != expected {
				t.Fatalf("not a closed unavailable item: %s", encoded)
			}
		})
	}
}

// The budget is on encoded JSON, including quotes and escape expansion.
func exactSourcesSizedItem(t *testing.T, size int) SourceReference {
	t.Helper()
	source := exactSourcesFixture()
	empty := ""
	source.CanonicalLocator = &empty
	got, err := projectLinkedSources("candidate", semanticID(2), []SourceReference{source}, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got.Items[0])
	value := strings.Repeat("x", size-len(b))
	source.CanonicalLocator = &value
	return source
}
func TestExactSourcesEncodedBoundsAndTruth(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		t.Run(string(rune('b'+delta)), func(t *testing.T) {
			source := exactSourcesSizedItem(t, MaximumLinkedSourceItemBytes+delta)
			got, err := projectLinkedSources("record", semanticID(2), []SourceReference{source}, 100)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(got.Items[0])
			if delta <= 0 {
				if len(b) != MaximumLinkedSourceItemBytes+delta || got.IncompleteItems != 0 {
					t.Fatalf("boundary rejected: %d %#v", len(b), got)
				}
			} else if got.Items[0].ReceiptUnavailable != "item_exceeds_bound" || got.Items[0].StoredSourceReceipt != nil || got.Returned != 1 || got.IncompleteItems != 1 || got.SourcesTruncated {
				t.Fatalf("bad oversized placeholder: %#v", got)
			}
		})
	}
	source := exactSourcesFixture()
	escaped := strings.Repeat("\x00", 3000)
	source.CanonicalLocator = &escaped
	got, _ := projectLinkedSources("record", semanticID(2), []SourceReference{source}, 100)
	if got.IncompleteItems != 1 {
		t.Fatal("measured code points instead of encoded bytes")
	}
	sources := make([]SourceReference, 101)
	for i := range sources {
		sources[i] = exactSourcesSizedItem(t, MaximumLinkedSourceItemBytes)
		sources[i].ID = semanticID(i + 1)
	}
	got, err := projectLinkedSources("record", semanticID(2), sources, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if len(b) > MaximumLinkedSourcesBytes || !got.SourcesTruncated || got.Returned != 15 || got.Returned != len(got.Items) || got.IncompleteItems != 0 {
		t.Fatalf("bad whole-item expansion bound: bytes=%d returned=%d", len(b), got.Returned)
	}
	// Tune the last item so the complete object lands exactly on the byte cap.
	sources = sources[:16]
	for i := range sources {
		sources[i] = exactSourcesSizedItem(t, 1000)
		sources[i].ID = semanticID(i + 1)
	}
	base, _ := projectLinkedSources("record", semanticID(2), sources, 100)
	baseBytes, _ := json.Marshal(base)
	// Fifteen maximum items plus a shorter final item fill the metadata reserve.
	for i := 0; i < 15; i++ {
		sources[i] = exactSourcesSizedItem(t, MaximumLinkedSourceItemBytes)
		sources[i].ID = semanticID(i + 1)
	}
	lastSize := MaximumLinkedSourcesBytes - len(baseBytes) + 16*1000 - 15*MaximumLinkedSourceItemBytes
	for _, delta := range []int{-1, 0, 1} {
		sources[15] = exactSourcesSizedItem(t, lastSize+delta)
		sources[15].ID = semanticID(16)
		got, err = projectLinkedSources("record", semanticID(2), sources, 100)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(got)
		if delta <= 0 && (len(b) != MaximumLinkedSourcesBytes+delta || got.Returned != 16 || got.SourcesTruncated) {
			t.Fatalf("response boundary delta=%d bytes=%d returned=%d", delta, len(b), got.Returned)
		}
		if delta > 0 && (got.Returned != 15 || !got.SourcesTruncated) {
			t.Fatal("response overflow not set-truncated")
		}
	}
}
func TestExactSourcesIndependentCountsAndLimits(t *testing.T) {
	for _, limit := range []int{1, 16, 50, 100} {
		for _, count := range []int{0, limit, limit + 1} {
			sources := make([]SourceReference, count)
			for i := range sources {
				sources[i] = exactSourcesFixture()
				sources[i].ID = semanticID(i + 1)
			}
			if count > 0 {
				bad := string([]byte{0xff})
				sources[0].GapReason = &bad
			}
			got, err := projectLinkedSources("candidate", semanticID(999), sources, limit)
			if err != nil || got.Items == nil || got.Returned != min(count, limit) || got.SourcesTruncated != (count > limit) || got.IncompleteItems != min(count, 1) {
				t.Fatalf("limit=%d count=%d result=%#v %v", limit, count, got, err)
			}
		}
	}
	source := exactSourcesFixture()
	source.ID = "not-an-id"
	if _, err := projectLinkedSources("record", semanticID(2), []SourceReference{source}, 16); err == nil {
		t.Fatal("invalid source ID accepted")
	}
}
