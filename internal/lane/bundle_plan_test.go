package lane

import (
	"math"
	"strings"
	"testing"

	"loom.local/loom/internal/filesystemmeta"
)

func TestBundlePlanThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		files      int
		bytes      int64
		want       TransportMode
		wantReason string
	}{
		{name: "empty", want: TransportModeFileTree, wantReason: BundleReasonEmpty},
		{name: "file count boundary with large files", files: 10_000, bytes: 10_000 * 1024 * 1024, want: TransportModeFileTree, wantReason: BundleReasonBelowThresholds},
		{name: "file count boundary with small files", files: 10_000, bytes: 10_000, want: TransportModeBundleSeed, wantReason: BundleReasonManySmallFiles},
		{name: "above file count", files: 10_001, bytes: 10_001 * 1024 * 1024, want: TransportModeBundleSeed, wantReason: BundleReasonFileCount},
		{name: "small count boundary", files: 2_000, bytes: 2_000, want: TransportModeFileTree, wantReason: BundleReasonBelowThresholds},
		{name: "small average boundary", files: 2_001, bytes: 2_001 * 256 * 1024, want: TransportModeFileTree, wantReason: BundleReasonBelowThresholds},
		{name: "many small", files: 2_001, bytes: 2_001 * (256*1024 - 1), want: TransportModeBundleSeed, wantReason: BundleReasonManySmallFiles},
		{name: "huge few", files: 4, bytes: 8 * 1024 * 1024 * 1024, want: TransportModeFileTree, wantReason: BundleReasonBelowThresholds},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SelectBundleTransport(TransferPlan{FileCount: test.files, TotalBytes: test.bytes}, TransportModeAuto)
			if err != nil {
				t.Fatal(err)
			}
			if got.SelectedMode != test.want || got.RecommendedMode != test.want || got.ReasonCode != test.wantReason {
				t.Fatalf("recommendation = %#v, want mode %q reason %q", got, test.want, test.wantReason)
			}
			if got.EstimatedTemporaryBytes < got.EstimatedArchiveBytes || got.TemporarySafetyMargin < bundleFixedSafetyMargin {
				t.Fatalf("temporary storage estimate is not conservative: %#v", got)
			}
		})
	}
}

func TestBundlePlanForcedModesAreExplicit(t *testing.T) {
	plan := TransferPlan{FileCount: 2, TotalBytes: 2 << 30}
	forcedBundle, err := SelectBundleTransport(plan, TransportModeBundleSeed)
	if err != nil {
		t.Fatal(err)
	}
	if forcedBundle.RecommendedMode != TransportModeFileTree || forcedBundle.SelectedMode != TransportModeBundleSeed || !forcedBundle.Forced || !strings.Contains(forcedBundle.ForceWarning, "forced") {
		t.Fatalf("forced bundle = %#v", forcedBundle)
	}

	many := TransferPlan{FileCount: 10_001, TotalBytes: 10_001}
	forcedTree, err := SelectBundleTransport(many, TransportModeFileTree)
	if err != nil {
		t.Fatal(err)
	}
	if forcedTree.RecommendedMode != TransportModeBundleSeed || forcedTree.SelectedMode != TransportModeFileTree || !forcedTree.Forced || forcedTree.ForceWarning == "" {
		t.Fatalf("forced file tree = %#v", forcedTree)
	}
	if _, err := SelectBundleTransport(plan, "hybrid"); err == nil {
		t.Fatal("unsupported transport did not fail")
	}
}

func TestBundlePlanUsesCanonicalEntriesAndSaturatesArithmetic(t *testing.T) {
	plan := TransferPlan{
		Entries: []TransferEntry{
			{RelativePath: "empty", Kind: filesystemmeta.ObjectKindDirectory},
			{RelativePath: "huge.bin", Kind: filesystemmeta.ObjectKindRegularFile, Bytes: math.MaxInt64},
		},
	}
	got, err := SelectBundleTransport(plan, TransportModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if got.RegularFileCount != 1 || got.RegularFileBytes != math.MaxInt64 || got.AverageFileBytes != math.MaxInt64 {
		t.Fatalf("canonical totals = %#v", got)
	}
	if got.EstimatedArchiveBytes != math.MaxInt64 || got.EstimatedTemporaryBytes != math.MaxInt64 {
		t.Fatalf("overflow did not saturate: %#v", got)
	}
}

func TestBundlePlanReasonIsDeterministic(t *testing.T) {
	plan := TransferPlan{FileCount: 2_001, TotalBytes: 2_001}
	first, err := SelectBundleTransport(plan, TransportModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SelectBundleTransport(plan, TransportModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("bundle recommendation changed: first=%#v second=%#v", first, second)
	}
}
