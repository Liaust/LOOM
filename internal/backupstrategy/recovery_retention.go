package backupstrategy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type NotesRecoveryRetentionInput struct {
	Root            string            `json:"root"`
	PublicationRoot string            `json:"publication_root"`
	ProtectionRoot  string            `json:"protection_root"`
	Repository      string            `json:"repository"`
	OperationalRoot string            `json:"operational_root,omitempty"`
	KeepLatest      int               `json:"keep_latest"`
	Protected       map[string]string `json:"protected"`
}

type NotesRecoveryRetirement struct {
	Schema     string                  `json:"schema"`
	ID         string                  `json:"id"`
	Status     string                  `json:"status"`
	PlanHash   string                  `json:"plan_hash"`
	StartedAt  time.Time               `json:"started_at"`
	FinishedAt *time.Time              `json:"finished_at,omitempty"`
	Protection NotesRecoveryProtection `json:"protection"`
}

type NotesRecoveryRetentionResult struct {
	Status      string                     `json:"status"`
	Plan        NotesRecoveryRetentionPlan `json:"plan"`
	Removed     []string                   `json:"removed"`
	ReceiptRoot string                     `json:"receipt_root"`
}

// All owners acquire the operational lifetime lease before the remote lock.
// Normal producers hold it shared; this path refuses while any owner uses it.
func ApplyNotesRecoveryRetention(ctx context.Context, operationalRoot string, input NotesRecoveryRetentionInput, expected string, yes bool,
	verify func(context.Context, NotesRecoveryProtection) error) (NotesRecoveryRetentionResult, error) {
	result := NotesRecoveryRetentionResult{Status: "refused", Removed: []string{}, ReceiptRoot: input.Root + ".retirement-receipts"}
	if !yes || expected == "" || verify == nil {
		return result, fmt.Errorf("exact current plan, confirmation and cloud recheck required")
	}
	lease, err := LockOperationalPackages(operationalRoot, true)
	if err != nil {
		return result, err
	}
	defer lease.Close()
	plan, err := PlanNotesRecoveryRetention(ctx, input)
	result.Plan = plan
	if err != nil {
		return result, err
	}
	if plan.PlanHash != expected {
		return result, fmt.Errorf("capture inventory or evidence changed; review a fresh plan")
	}
	proofs := map[string]NotesRecoveryProtection{}
	// Verify every candidate before removing the first one. A connectivity or
	// history problem must not turn into a partially executed review set.
	for _, entry := range plan.Entries {
		if entry.Action != "candidate" {
			continue
		}
		proof, err := ReadNotesRecoveryProtection(input.ProtectionRoot, entry.ID)
		if err != nil {
			return result, err
		}
		if err := verify(ctx, proof); err != nil {
			return result, fmt.Errorf("capture %s cloud recheck failed: %w", entry.ID, err)
		}
		proofs[entry.ID] = proof
	}
	// Remote checks may take time; refuse any changed local receipt or payload.
	fresh, err := PlanNotesRecoveryRetention(ctx, input)
	if err != nil || fresh.PlanHash != expected {
		return result, fmt.Errorf("capture inventory changed during cloud recheck")
	}
	handle, err := os.OpenRoot(input.Root)
	if err != nil {
		return result, err
	}
	defer handle.Close()
	result.Status = "started"
	for _, entry := range plan.Entries {
		if entry.Action != "candidate" {
			continue
		}
		if err := ctx.Err(); err != nil {
			result.Status = "partial"
			return result, err
		}
		if _, err := os.Lstat(filepath.Join(result.ReceiptRoot, entry.ID+".json")); !os.IsNotExist(err) {
			return result, fmt.Errorf("retirement receipt already exists for %s; inspect or resume that exact operation", entry.ID)
		}
		receipt := NotesRecoveryRetirement{Schema: "loom.notes_recovery.retirement.v1", ID: entry.ID, Status: "started", PlanHash: expected,
			StartedAt: time.Now().UTC(), Protection: proofs[entry.ID]}
		if err := WriteNotesRecoveryRecord(result.ReceiptRoot, entry.ID, receipt, 0600); err != nil {
			return result, err
		}
		result.Status = "partial"
		if err := retireNotesRecoveryPayload(ctx, handle, input.Root, receipt, false); err != nil {
			return result, err
		}
		now := time.Now().UTC()
		receipt.Status, receipt.FinishedAt = "local_retired", &now
		if err := WriteNotesRecoveryRecord(result.ReceiptRoot, entry.ID, receipt, 0600); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, entry.ID)
	}
	result.Status = "applied"
	return result, nil
}

func retireNotesRecoveryPayload(ctx context.Context, handle *os.Root, root string, receipt NotesRecoveryRetirement, resume bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, staged := receipt.ID, ".retired-"+receipt.ID
	original, originalErr := handle.Lstat(id)
	staging, stagingErr := handle.Lstat(staged)
	if originalErr == nil {
		if !original.IsDir() || !os.IsNotExist(stagingErr) {
			return fmt.Errorf("ambiguous retirement paths")
		}
		identity, _, err := retentionIdentity(filepath.Join(root, id))
		if err != nil || identity != receipt.Protection.Publication.Identity {
			return fmt.Errorf("capture changed before retirement")
		}
		if err := handle.Rename(id, staged); err != nil {
			return err
		}
		if err := syncRetentionDirectory(root); err != nil {
			return err
		}
	} else if !resume || !os.IsNotExist(originalErr) || (stagingErr != nil && !os.IsNotExist(stagingErr)) {
		return fmt.Errorf("retirement state unavailable")
	} else if stagingErr == nil {
		owner, ok := statUint(staging.Sys(), "Uid")
		if !staging.IsDir() || !ok || owner != receipt.Protection.Publication.OwnerUID || staging.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("invalid exact retirement staging custody")
		}
	}
	if err := handle.RemoveAll(staged); err != nil {
		return err
	}
	return syncRetentionDirectory(root)
}

// Resume only a named durable retirement, never a sweep of hidden directories.
func ResumeNotesRecoveryRetirement(ctx context.Context, operationalRoot, root, id string,
	verify func(context.Context, NotesRecoveryProtection) error) (NotesRecoveryRetirement, error) {
	var receipt NotesRecoveryRetirement
	if !ValidNotesRecoveryID(id) || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || verify == nil {
		return receipt, fmt.Errorf("exact configured root, capture and cloud recheck required")
	}
	lease, err := LockOperationalPackages(operationalRoot, true)
	if err != nil {
		return receipt, err
	}
	defer lease.Close()
	records := root + ".retirement-receipts"
	if err := checkNotesRecoveryRecordCustody(records, id); err != nil {
		return receipt, err
	}
	if _, err := notesRecoveryReadJSON(filepath.Join(records, id+".json"), 64<<10, &receipt); err != nil {
		return receipt, err
	}
	proof := receipt.Protection
	pubRaw, _ := json.Marshal(proof.Publication)
	if receipt.Schema != "loom.notes_recovery.retirement.v1" || receipt.ID != id || receipt.StartedAt.IsZero() ||
		receipt.PlanHash == "" || proof.Schema != NotesRecoveryProtectionSchema || proof.Publication.Root != root ||
		validateNotesRecoveryPublication(proof.Publication, id) != nil || proof.PublicationSHA256 != notesRecoveryHash(pubRaw) ||
		proof.VerificationBasis != "stable_publication_and_authenticated_archive_metadata_v1" {
		return receipt, fmt.Errorf("invalid exact retirement receipt")
	}
	if receipt.Status == "local_retired" {
		for _, name := range []string{id, ".retired-" + id} {
			if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
				return receipt, fmt.Errorf("completed retirement payload reappeared or is inaccessible")
			}
		}
		return receipt, nil
	}
	if receipt.Status != "started" {
		return receipt, fmt.Errorf("retirement is not resumable")
	}
	if err := verify(ctx, proof); err != nil {
		return receipt, err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return receipt, err
	}
	defer handle.Close()
	if err := retireNotesRecoveryPayload(ctx, handle, root, receipt, true); err != nil {
		return receipt, err
	}
	now := time.Now().UTC()
	receipt.Status, receipt.FinishedAt = "local_retired", &now
	return receipt, WriteNotesRecoveryRecord(records, id, receipt, 0600)
}

type NotesRecoveryRetentionEntry struct {
	ID                string                    `json:"id"`
	Action            string                    `json:"action"`
	Reason            string                    `json:"reason"`
	Publication       *NotesRecoveryPublication `json:"publication,omitempty"`
	PublicationSHA256 string                    `json:"publication_sha256,omitempty"`
	ProtectionSHA256  string                    `json:"protection_sha256,omitempty"`
	AllocatedBytes    int64                     `json:"allocated_bytes"`
}

type NotesRecoveryRetentionPlan struct {
	Schema             string                        `json:"schema"`
	PlanHash           string                        `json:"plan_hash"`
	Input              NotesRecoveryRetentionInput   `json:"input"`
	Entries            []NotesRecoveryRetentionEntry `json:"entries"`
	CandidateBytes     int64                         `json:"candidate_allocated_bytes"`
	RetainedBytes      int64                         `json:"known_retained_allocated_bytes"`
	AccountingComplete bool                          `json:"accounting_complete"`
}

func NotesRecoveryIndependentCopy(ctx context.Context, input NotesRecoveryRetentionInput, id string) (NotesRecoveryProtection, error) {
	var proof NotesRecoveryProtection
	current, err := InspectNotesRecoveryPublication(ctx, input.Root, id)
	if err != nil {
		return proof, err
	}
	if current.Reference != nil {
		return proof, fmt.Errorf("reference capture still requires its original local package")
	}
	pub, hash, err := ReadNotesRecoveryPublication(input.PublicationRoot, id)
	if err != nil || pub.Root != input.Root || pub.Identity != current.Identity || pub.CohortSHA256 != current.CohortSHA256 {
		return proof, fmt.Errorf("capture publication is missing or changed")
	}
	proof, err = ReadNotesRecoveryProtection(input.ProtectionRoot, id)
	if err != nil || proof.Repository != input.Repository || proof.PublicationSHA256 != hash {
		return proof, fmt.Errorf("capture cloud protection is missing or mismatched")
	}
	return proof, nil
}

func PlanNotesRecoveryRetention(ctx context.Context, input NotesRecoveryRetentionInput) (NotesRecoveryRetentionPlan, error) {
	p := NotesRecoveryRetentionPlan{Schema: "loom.notes_recovery.retention.v1", Input: input, Entries: []NotesRecoveryRetentionEntry{}, AccountingComplete: true}
	if p.Input.KeepLatest == 0 {
		p.Input.KeepLatest = 1
	}
	if p.Input.OperationalRoot == "" {
		p.Input.OperationalRoot = filepath.Join(filepath.Dir(input.PublicationRoot), "backups", "operational")
	}
	if p.Input.KeepLatest < 1 || input.Repository == "" {
		return p, fmt.Errorf("retention requires a positive local capture count and exact repository")
	}
	if p.Input.Protected == nil {
		p.Input.Protected = map[string]string{}
	}
	for id, reason := range input.Protected {
		if !ValidNotesRecoveryID(id) || reason == "" {
			return p, fmt.Errorf("invalid capture protection pin")
		}
	}
	for _, root := range []string{input.Root, input.PublicationRoot, input.ProtectionRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
			return p, fmt.Errorf("exact configured recovery roots required")
		}
	}
	if err := requireRealDirectory(input.Root); err != nil {
		return p, err
	}
	entries, err := os.ReadDir(input.Root)
	if err != nil {
		return p, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		id := entry.Name()
		e := NotesRecoveryRetentionEntry{ID: id, Action: "hold", Reason: "unknown_capture_entry"}
		if !entry.IsDir() || !ValidNotesRecoveryID(id) {
			p.AccountingComplete = false
			p.Entries = append(p.Entries, e)
			continue
		}
		seen[id] = true
		current, err := InspectNotesRecoveryPublication(ctx, input.Root, id)
		if err != nil {
			e.Reason = "capture_inventory_invalid"
			p.AccountingComplete = false
			p.Entries = append(p.Entries, e)
			continue
		}
		e.AllocatedBytes = current.AllocatedBytes
		pub, hash, err := ReadNotesRecoveryPublication(input.PublicationRoot, id)
		if err != nil || pub.Root != input.Root || pub.Identity != current.Identity || pub.CohortSHA256 != current.CohortSHA256 {
			e.Reason = "publication_receipt_missing_or_changed"
			p.RetainedBytes += e.AllocatedBytes
			p.Entries = append(p.Entries, e)
			continue
		}
		e.Publication, e.PublicationSHA256 = &pub, hash
		proof, err := ReadNotesRecoveryProtection(input.ProtectionRoot, id)
		if err != nil || proof.Repository != input.Repository || proof.PublicationSHA256 != hash {
			e.Reason = "cloud_protection_missing_or_mismatched"
		} else {
			raw, _ := json.Marshal(proof)
			e.ProtectionSHA256 = notesRecoveryHash(raw)
			e.Action, e.Reason = "candidate", "older_than_latest_protected_capture"
		}
		if pub.Reference != nil {
			dep, err := NotesRecoveryOperationalDependency(ctx, p.Input.OperationalRoot, *pub.Reference)
			if err != nil || proof.Dependency == nil || dep.Package.ManifestSHA256 != proof.Dependency.Package.ManifestSHA256 {
				e.Action, e.Reason = "hold", "operational_dependency_missing_or_changed"
				e.ProtectionSHA256 = ""
			}
		}
		if reason := input.Protected[id]; reason != "" {
			e.Action, e.Reason = "keep", "explicit_pin: "+reason
		}
		p.Entries = append(p.Entries, e)
	}
	for id := range input.Protected {
		if !seen[id] {
			return p, fmt.Errorf("protected capture %s is missing", id)
		}
	}
	// Select a verified local recovery point, not merely the newest directory.
	sort.SliceStable(p.Entries, func(i, j int) bool {
		a, b := p.Entries[i].Publication, p.Entries[j].Publication
		if (a == nil) != (b == nil) {
			return a != nil
		}
		if a != nil && b != nil && !a.CapturedAt.Equal(b.CapturedAt) {
			return a.CapturedAt.After(b.CapturedAt)
		}
		return p.Entries[i].ID > p.Entries[j].ID
	})
	kept := 0
	for i := range p.Entries {
		e := &p.Entries[i]
		if e.ProtectionSHA256 != "" && (e.Action == "candidate" || e.Action == "keep") && kept < p.Input.KeepLatest {
			if e.Action == "candidate" {
				e.Action, e.Reason = "keep", "latest_protected_capture"
			}
			kept++
		}
		if e.Action == "candidate" {
			p.CandidateBytes += e.AllocatedBytes
		} else if e.Publication != nil {
			p.RetainedBytes += e.AllocatedBytes
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	p.PlanHash = "sha256:" + notesRecoveryHash(raw)
	return p, nil
}
