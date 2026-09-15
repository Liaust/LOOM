package lane

import (
	"fmt"
	"math"

	"loom.local/loom/internal/filesystemmeta"
)

const (
	DefaultBundleFileCountAbove        = 10_000
	DefaultBundleSmallFileCountAbove   = 2_000
	DefaultBundleSmallFileAverageBelow = 256 * 1024

	bundleTarBlockBytes       int64 = 512
	bundleTarTrailerBytes     int64 = 2 * bundleTarBlockBytes
	bundlePerEntryBudgetBytes int64 = 8 * 1024
	bundleFixedSafetyMargin   int64 = 64 * 1024 * 1024
	bundleSafetyMarginPercent int64 = 10
)

const (
	BundleReasonEmpty           = "empty"
	BundleReasonFileCount       = "file_count"
	BundleReasonManySmallFiles  = "many_small_files"
	BundleReasonBelowThresholds = "below_thresholds"
)

var DefaultBundleThresholds = BundleThresholds{
	FileCountAbove:        DefaultBundleFileCountAbove,
	SmallFileCountAbove:   DefaultBundleSmallFileCountAbove,
	SmallFileAverageBelow: DefaultBundleSmallFileAverageBelow,
}

// SelectBundleTransport makes the batch-level transport decision from the
// canonical TransferPlan. It does not inspect the filesystem or alter the
// inventory hash.
func SelectBundleTransport(plan TransferPlan, requested TransportMode) (BundleRecommendation, error) {
	if requested == "" {
		requested = TransportModeAuto
	}
	if requested != TransportModeAuto && requested != TransportModeFileTree && requested != TransportModeBundleSeed {
		return BundleRecommendation{}, fmt.Errorf("unsupported Lane transport request %q", requested)
	}

	fileCount, fileBytes := regularFileTotals(plan)
	average := int64(0)
	if fileCount > 0 {
		average = fileBytes / int64(fileCount)
	}
	archiveBytes := estimateBundleArchiveBytes(plan)
	safetyMargin := saturatingAdd(bundleFixedSafetyMargin, archiveBytes/bundleSafetyMarginPercent)

	recommendation := BundleRecommendation{
		SchemaVersion:           BundlePlanSchemaVersion,
		RequestedMode:           requested,
		RecommendedMode:         TransportModeFileTree,
		SelectedMode:            TransportModeFileTree,
		RegularFileCount:        fileCount,
		RegularFileBytes:        fileBytes,
		AverageFileBytes:        average,
		EstimatedArchiveBytes:   archiveBytes,
		TemporarySafetyMargin:   safetyMargin,
		EstimatedTemporaryBytes: saturatingAdd(archiveBytes, safetyMargin),
		Thresholds:              DefaultBundleThresholds,
	}

	switch {
	case fileCount == 0:
		recommendation.ReasonCode = BundleReasonEmpty
		recommendation.Reason = "the batch contains no regular files"
	case fileCount > DefaultBundleThresholds.FileCountAbove:
		recommendation.RecommendedMode = TransportModeBundleSeed
		recommendation.SelectedMode = TransportModeBundleSeed
		recommendation.ReasonCode = BundleReasonFileCount
		recommendation.Reason = fmt.Sprintf("regular file count %d exceeds the bundle threshold %d", fileCount, DefaultBundleThresholds.FileCountAbove)
	case fileCount > DefaultBundleThresholds.SmallFileCountAbove && average < DefaultBundleThresholds.SmallFileAverageBelow:
		recommendation.RecommendedMode = TransportModeBundleSeed
		recommendation.SelectedMode = TransportModeBundleSeed
		recommendation.ReasonCode = BundleReasonManySmallFiles
		recommendation.Reason = fmt.Sprintf("regular file count %d exceeds %d and average size %d bytes is below %d bytes", fileCount, DefaultBundleThresholds.SmallFileCountAbove, average, DefaultBundleThresholds.SmallFileAverageBelow)
	default:
		recommendation.ReasonCode = BundleReasonBelowThresholds
		recommendation.Reason = fmt.Sprintf("regular file count %d and average size %d bytes do not exceed bundle thresholds", fileCount, average)
	}

	if requested == TransportModeAuto {
		return recommendation, nil
	}
	recommendation.Forced = true
	recommendation.SelectedMode = requested
	if requested == TransportModeBundleSeed {
		recommendation.ForceWarning = "bundle_seed was forced; temporary storage and archive consistency checks still apply"
	} else {
		recommendation.ForceWarning = "file_tree was forced; the automatic bundle recommendation was not selected"
	}
	return recommendation, nil
}

func regularFileTotals(plan TransferPlan) (int, int64) {
	count := 0
	bytes := int64(0)
	for _, entry := range plan.Entries {
		if entry.Kind != filesystemmeta.ObjectKindRegularFile {
			continue
		}
		count++
		bytes = saturatingAdd(bytes, entry.Bytes)
	}
	// Synthetic and older callers may populate only summary fields. The
	// canonical planner populates both, and the maximum keeps selection stable
	// without manufacturing another inventory.
	if plan.FileCount > count {
		count = plan.FileCount
	}
	if plan.TotalBytes > bytes {
		bytes = plan.TotalBytes
	}
	return count, bytes
}

func estimateBundleArchiveBytes(plan TransferPlan) int64 {
	total := bundleTarTrailerBytes
	for _, entry := range plan.Entries {
		total = saturatingAdd(total, bundlePerEntryBudgetBytes)
		total = saturatingAdd(total, int64(len(entry.RelativePath))*4)
		if entry.Kind == filesystemmeta.ObjectKindRegularFile {
			total = saturatingAdd(total, roundedTarBytes(entry.Bytes))
		}
	}
	if len(plan.Entries) == 0 && plan.FileCount > 0 {
		total = saturatingAdd(total, saturatingMul(int64(plan.FileCount), bundlePerEntryBudgetBytes))
		total = saturatingAdd(total, roundedTarBytes(plan.TotalBytes))
	}
	return total
}

func roundedTarBytes(value int64) int64 {
	if value <= 0 {
		return 0
	}
	blocks := value / bundleTarBlockBytes
	if value%bundleTarBlockBytes != 0 {
		blocks++
	}
	return saturatingMul(blocks, bundleTarBlockBytes)
}

func saturatingAdd(left, right int64) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func saturatingMul(left, right int64) int64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	if left > math.MaxInt64/right {
		return math.MaxInt64
	}
	return left * right
}
