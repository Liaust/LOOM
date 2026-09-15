package lane

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

const (
	bundleArchiveFileName  = "bundle.pax.tar"
	bundleManifestFileName = "manifest.json"
	maxBundleManifestBytes = 256 * 1024 * 1024
)

func marshalBundleManifest(manifest BundleManifest) ([]byte, string, error) {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, "", err
	}
	payload = append(payload, '\n')
	sum := sha256.Sum256(payload)
	return payload, fmt.Sprintf("sha256:%x", sum), nil
}

func ReadBundleManifest(pathValue string) (BundleManifest, error) {
	if err := requireRegularBundleArtifact(pathValue); err != nil {
		return BundleManifest{}, err
	}
	file, err := openRegularBundleArtifact(pathValue)
	if err != nil {
		return BundleManifest{}, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxBundleManifestBytes+1))
	if err != nil {
		return BundleManifest{}, err
	}
	if len(payload) > maxBundleManifestBytes {
		return BundleManifest{}, fmt.Errorf("bundle manifest exceeds %d-byte limit", maxBundleManifestBytes)
	}
	var manifest BundleManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return BundleManifest{}, fmt.Errorf("decode bundle manifest: %w", err)
	}
	if err := validateBundleManifest(manifest); err != nil {
		return BundleManifest{}, err
	}
	return manifest, nil
}

func validateBundleManifest(manifest BundleManifest) error {
	if manifest.SchemaVersion != BundleManifestSchemaVersion {
		return fmt.Errorf("unsupported bundle manifest schema %q", manifest.SchemaVersion)
	}
	if manifest.CreatedAt.IsZero() || strings.TrimSpace(manifest.SourcePath) == "" || !filepath.IsAbs(manifest.SourcePath) {
		return fmt.Errorf("bundle manifest source path or creation time is invalid")
	}
	if strings.TrimSpace(manifest.BatchID) == "" || safeToken(manifest.BatchID, "batch") != manifest.BatchID {
		return fmt.Errorf("bundle manifest batch_id is invalid")
	}
	if strings.TrimSpace(manifest.SourceNodeKey) == "" || safeToken(manifest.SourceNodeKey, "workspace") != manifest.SourceNodeKey {
		return fmt.Errorf("bundle manifest source_node_key is invalid")
	}
	if manifest.ArchiveFormat != BundleArchiveFormatPAXTar || manifest.Compression != BundleCompressionNone {
		return fmt.Errorf("bundle manifest archive format or compression is unsupported")
	}
	if manifest.TriggerKind != BundleTriggerManualLane || manifest.ReadinessBasis != BundleReadinessOperatorTrigger {
		return fmt.Errorf("bundle manifest trigger or readiness basis is invalid")
	}
	if manifest.TransferMode != TransportModeBundleSeed || manifest.CustodyAction != BundleCustodyUnpack {
		return fmt.Errorf("bundle manifest does not describe a Lane bundle_seed custody transfer")
	}
	if manifest.CleanupState != BundleCleanupRetainedForRetry {
		return fmt.Errorf("bundle manifest cleanup state %q is invalid", manifest.CleanupState)
	}
	if strings.TrimSpace(manifest.RestoreInstructions) == "" {
		return fmt.Errorf("bundle manifest restore instructions are required")
	}
	if manifest.Plan.SchemaVersion != TransferPlanSchemaVersion {
		return fmt.Errorf("bundle manifest transfer plan schema %q is invalid", manifest.Plan.SchemaVersion)
	}
	if _, err := filepolicy.ParseProfile(string(manifest.Plan.Profile)); err != nil {
		return fmt.Errorf("bundle manifest transfer profile is invalid: %w", err)
	}
	if manifest.PolicyFingerprint == "" || manifest.PolicyFingerprint != manifest.Plan.PolicyFingerprint || manifest.InventoryHash == "" || manifest.InventoryHash != manifest.Plan.InventoryHash {
		return fmt.Errorf("bundle manifest policy or inventory identity does not match its transfer plan")
	}
	if got := transferInventoryHash(manifest.Plan); got != manifest.InventoryHash {
		return fmt.Errorf("bundle manifest transfer plan inventory hash mismatch")
	}
	if manifest.SourceFingerprintBefore != manifest.InventoryHash || manifest.SourceFingerprintAfter != manifest.InventoryHash {
		return fmt.Errorf("bundle manifest source fingerprints do not match planned inventory")
	}
	if manifest.SelectionReason != manifest.Plan.Transport.Reason || !equalStringMap(manifest.PolicyHashes, manifest.Plan.PolicyHashes) {
		return fmt.Errorf("bundle manifest transport or policy evidence does not match its transfer plan")
	}
	if manifest.TotalSourceFiles != manifest.Plan.FileCount || manifest.TotalSourceBytes != manifest.Plan.TotalBytes || manifest.ExcludedFileCount != manifest.Plan.IgnoredFileCount || manifest.ExcludedBytes != manifest.Plan.IgnoredBytes || manifest.ExcludedEntryCount != len(manifest.Plan.Ignored) {
		return fmt.Errorf("bundle manifest summary does not match its transfer plan")
	}
	if manifest.Plan.Transport.SelectedMode != TransportModeBundleSeed || manifest.Plan.Transport.SchemaVersion != BundlePlanSchemaVersion {
		return fmt.Errorf("bundle manifest transfer plan does not select bundle_seed")
	}
	if len(manifest.ArchiveParts) != 1 {
		return fmt.Errorf("bundle manifest must contain exactly one archive part")
	}
	part := manifest.ArchiveParts[0]
	if part.Name != bundleArchiveFileName || part.SizeBytes < 0 || !validSHA256URI(part.SHA256) {
		return fmt.Errorf("bundle manifest archive part is invalid")
	}
	if filepath.Base(part.Name) != part.Name {
		return fmt.Errorf("bundle archive part must be a base name")
	}
	return validateTransferPlanEntries(manifest.Plan)
}

func validateTransferPlanEntries(plan TransferPlan) error {
	seen := make(map[string]bool, len(plan.Entries))
	fileCount := 0
	dirCount := 0
	fileBytes := int64(0)
	last := ""
	for index, entry := range plan.Entries {
		if err := validateBundleRelativePath(entry.RelativePath); err != nil {
			return fmt.Errorf("bundle plan entry %d: %w", index, err)
		}
		if seen[entry.RelativePath] {
			return fmt.Errorf("bundle plan contains duplicate path %q", entry.RelativePath)
		}
		seen[entry.RelativePath] = true
		if index > 0 && entry.RelativePath <= last {
			return fmt.Errorf("bundle plan entries are not in lexical order")
		}
		last = entry.RelativePath
		switch entry.Kind {
		case "directory":
			if entry.Bytes != 0 {
				return fmt.Errorf("bundle directory %q has non-zero size", entry.RelativePath)
			}
			dirCount++
		case "regular_file":
			if entry.Bytes < 0 {
				return fmt.Errorf("bundle file %q has negative size", entry.RelativePath)
			}
			fileCount++
			fileBytes = saturatingAdd(fileBytes, entry.Bytes)
		default:
			return fmt.Errorf("bundle plan entry %q has unsupported kind %q", entry.RelativePath, entry.Kind)
		}
	}
	if fileCount != plan.FileCount || dirCount != plan.DirCount || fileBytes != plan.TotalBytes {
		return fmt.Errorf("bundle plan entry totals do not match summary")
	}
	return nil
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func validSHA256URI(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
