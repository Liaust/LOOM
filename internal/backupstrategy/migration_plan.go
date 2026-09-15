package backupstrategy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	MigrationEligibilitySchema  = "loom.backupstrategy.migration_eligibility.v1"
	StrictRestoreEvidenceSchema = "loom.backupstrategy.strict_restore_evidence.v1"

	MigrationEligibilityEligible = "eligible"
	MigrationEligibilityBlocked  = "blocked"
	StrictRestoreSucceeded       = "succeeded"
)

type StrictRestoreEvidence struct {
	Schema                    string    `json:"schema"`
	Status                    string    `json:"status"`
	RepositoryID              string    `json:"repository_id"`
	Archive                   string    `json:"archive"`
	DirectArchiveManifestSHA  string    `json:"direct_archive_manifest_sha256"`
	OperationalManifestSHA256 string    `json:"operational_manifest_sha256"`
	ProvenanceManifestSHA256  string    `json:"provenance_manifest_sha256"`
	AcceptedAt                time.Time `json:"accepted_at"`
	Digest                    string    `json:"digest"`
}

type MigrationEligibilityInput struct {
	Inventory               Inventory             `json:"inventory"`
	ExpectedInventoryDigest string                `json:"expected_inventory_digest"`
	StrictRestore           StrictRestoreEvidence `json:"strict_restore"`
	GeneratedAt             time.Time             `json:"generated_at"`
}

type MigrationEligibilityComponent struct {
	RelativePath   string         `json:"relative_path"`
	Class          ComponentClass `json:"class"`
	ReclaimPosture ReclaimPosture `json:"reclaim_posture"`
	AllocatedBytes uint64         `json:"allocated_bytes"`
	Reason         string         `json:"reason"`
}

type MigrationEligibilityReport struct {
	Schema                   string                          `json:"schema"`
	Status                   string                          `json:"status"`
	InventoryDigest          string                          `json:"inventory_digest"`
	RepositoryID             string                          `json:"repository_id"`
	StrictRestoreDigest      string                          `json:"strict_restore_digest"`
	GeneratedAt              time.Time                       `json:"generated_at"`
	Candidates               []MigrationEligibilityComponent `json:"candidates"`
	Protected                []MigrationEligibilityComponent `json:"protected"`
	Blockers                 []string                        `json:"blockers"`
	ExpectedReclaimableBytes uint64                          `json:"expected_reclaimable_bytes"`
	CleanupApplyAllowed      bool                            `json:"cleanup_apply_allowed"`
	Digest                   string                          `json:"digest"`
}

func BindStrictRestoreEvidence(evidence StrictRestoreEvidence) (StrictRestoreEvidence, error) {
	evidence = normalizeStrictRestoreEvidence(evidence)
	digest, err := strictRestoreEvidenceDigest(evidence)
	if err != nil {
		return StrictRestoreEvidence{}, err
	}
	evidence.Digest = digest
	if err := validateStrictRestoreEvidence(evidence); err != nil {
		return StrictRestoreEvidence{}, err
	}
	return evidence, nil
}

// PlanMigrationEligibility reports which legacy local generations could be
// considered by a later, separately reviewed cleanup. It deliberately exposes
// no apply operation and always leaves CleanupApplyAllowed false.
func PlanMigrationEligibility(input MigrationEligibilityInput) (MigrationEligibilityReport, error) {
	report := MigrationEligibilityReport{
		Schema: MigrationEligibilitySchema, Status: MigrationEligibilityBlocked,
		InventoryDigest: strings.TrimSpace(input.Inventory.Digest),
		GeneratedAt:     input.GeneratedAt.UTC(), Candidates: []MigrationEligibilityComponent{},
		Protected: []MigrationEligibilityComponent{}, Blockers: []string{},
		CleanupApplyAllowed: false,
	}
	if report.GeneratedAt.IsZero() {
		return MigrationEligibilityReport{}, fmt.Errorf("migration eligibility generated_at is required")
	}
	expectedDigest := strings.TrimSpace(input.ExpectedInventoryDigest)
	if report.InventoryDigest == "" || expectedDigest == "" || report.InventoryDigest != expectedDigest {
		report.Blockers = append(report.Blockers, "inventory_digest_mismatch")
	}
	observedInventoryDigest, digestErr := inventoryDigest(input.Inventory)
	if digestErr != nil || observedInventoryDigest != report.InventoryDigest {
		report.Blockers = append(report.Blockers, "inventory_evidence_invalid")
	}
	if err := validateStrictRestoreEvidence(input.StrictRestore); err != nil {
		report.Blockers = append(report.Blockers, "strict_restore_evidence_invalid")
	} else {
		report.RepositoryID = input.StrictRestore.RepositoryID
		report.StrictRestoreDigest = input.StrictRestore.Digest
	}
	if input.Inventory.Remote == nil || strings.TrimSpace(input.Inventory.Remote.RepositoryID) == "" {
		report.Blockers = append(report.Blockers, "remote_repository_identity_missing")
	} else if report.RepositoryID != "" && input.Inventory.Remote.RepositoryID != report.RepositoryID {
		report.Blockers = append(report.Blockers, "remote_repository_identity_mismatch")
	}

	hasMilestone := false
	hasSharedStore := false
	for _, component := range input.Inventory.Components {
		item := MigrationEligibilityComponent{
			RelativePath: component.RelativePath, Class: component.Class,
			ReclaimPosture: component.ReclaimPosture, AllocatedBytes: component.Accounting.AllocatedBytes,
		}
		switch {
		case !component.Safe:
			item.Reason = "unsafe inventory component remains protected"
			report.Protected = append(report.Protected, item)
			report.Blockers = append(report.Blockers, "unsafe_component:"+component.RelativePath)
		case component.Class == ComponentUnknown:
			item.Reason = "unknown entry remains protected"
			report.Protected = append(report.Protected, item)
			report.Blockers = append(report.Blockers, "unknown_entry:"+component.RelativePath)
		case component.Class == ComponentIncompleteStaging:
			item.Reason = "active or incomplete staging remains protected"
			report.Protected = append(report.Protected, item)
			report.Blockers = append(report.Blockers, "active_staging:"+component.RelativePath)
		case component.Class == ComponentSuccessfulGeneration && !component.Protected && component.ReclaimPosture == ReclaimCandidate && component.Accounting.PotentialReclaimableBytes > 0:
			item.AllocatedBytes = component.Accounting.PotentialReclaimableBytes
			item.Reason = "successful unprotected legacy generation with exact allocated-byte candidate evidence"
			report.Candidates = append(report.Candidates, item)
			if err := addMigrationBytes(&report.ExpectedReclaimableBytes, item.AllocatedBytes); err != nil {
				return MigrationEligibilityReport{}, err
			}
		default:
			item.Reason = "component is not an eligible legacy generation"
			report.Protected = append(report.Protected, item)
		}
		if component.Safe && component.Class == ComponentProtectedMilestone && component.Protected {
			hasMilestone = true
		}
		if component.Safe && component.Class == ComponentSharedStore && component.Protected {
			hasSharedStore = true
		}
	}
	if !hasMilestone {
		report.Blockers = append(report.Blockers, "protected_milestone_missing")
	}
	if !hasSharedStore {
		report.Blockers = append(report.Blockers, "shared_store_missing")
	}
	if len(report.Candidates) == 0 {
		report.Blockers = append(report.Blockers, "no_exact_cleanup_candidates")
	}
	if report.ExpectedReclaimableBytes != input.Inventory.LocalTotals.PotentialReclaimableBytes {
		report.Blockers = append(report.Blockers, "reclaimable_byte_accounting_mismatch")
	}
	sort.Slice(report.Candidates, func(i, j int) bool { return report.Candidates[i].RelativePath < report.Candidates[j].RelativePath })
	sort.Slice(report.Protected, func(i, j int) bool { return report.Protected[i].RelativePath < report.Protected[j].RelativePath })
	report.Blockers = sortedUniqueStrings(report.Blockers)
	if len(report.Blockers) == 0 {
		report.Status = MigrationEligibilityEligible
	}
	digest, err := migrationEligibilityDigest(report)
	if err != nil {
		return MigrationEligibilityReport{}, err
	}
	report.Digest = digest
	return report, nil
}

func validateStrictRestoreEvidence(evidence StrictRestoreEvidence) error {
	evidence = normalizeStrictRestoreEvidence(evidence)
	if evidence.Schema != StrictRestoreEvidenceSchema || evidence.Status != StrictRestoreSucceeded || evidence.RepositoryID == "" || evidence.Archive == "" || evidence.AcceptedAt.IsZero() || evidence.AcceptedAt.Location() != time.UTC {
		return fmt.Errorf("strict restore evidence identity is incomplete")
	}
	for _, value := range []string{evidence.DirectArchiveManifestSHA, evidence.OperationalManifestSHA256, evidence.ProvenanceManifestSHA256, evidence.Digest} {
		if !isSHA256Hex(value) {
			return fmt.Errorf("strict restore evidence contains an invalid SHA-256 identity")
		}
	}
	digest, err := strictRestoreEvidenceDigest(evidence)
	if err != nil || digest != evidence.Digest {
		return fmt.Errorf("strict restore evidence digest mismatch")
	}
	return nil
}

func normalizeStrictRestoreEvidence(evidence StrictRestoreEvidence) StrictRestoreEvidence {
	evidence.Schema = strings.TrimSpace(evidence.Schema)
	evidence.Status = strings.TrimSpace(evidence.Status)
	evidence.RepositoryID = strings.TrimSpace(evidence.RepositoryID)
	evidence.Archive = strings.TrimSpace(evidence.Archive)
	evidence.DirectArchiveManifestSHA = strings.ToLower(strings.TrimSpace(evidence.DirectArchiveManifestSHA))
	evidence.OperationalManifestSHA256 = strings.ToLower(strings.TrimSpace(evidence.OperationalManifestSHA256))
	evidence.ProvenanceManifestSHA256 = strings.ToLower(strings.TrimSpace(evidence.ProvenanceManifestSHA256))
	evidence.Digest = strings.ToLower(strings.TrimSpace(evidence.Digest))
	if !evidence.AcceptedAt.IsZero() {
		evidence.AcceptedAt = evidence.AcceptedAt.UTC()
	}
	return evidence
}

func strictRestoreEvidenceDigest(evidence StrictRestoreEvidence) (string, error) {
	evidence = normalizeStrictRestoreEvidence(evidence)
	evidence.Digest = ""
	return migrationDigest(evidence)
}

func migrationEligibilityDigest(report MigrationEligibilityReport) (string, error) {
	report.Digest = ""
	report.GeneratedAt = time.Time{}
	return migrationDigest(report)
}

func migrationDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func addMigrationBytes(total *uint64, value uint64) error {
	if ^uint64(0)-*total < value {
		return fmt.Errorf("migration eligibility allocated-byte total overflow")
	}
	*total += value
	return nil
}
