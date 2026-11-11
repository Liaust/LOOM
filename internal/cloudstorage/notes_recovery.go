package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"loom.local/loom/internal/backupstrategy"
)

type NotesRecoveryObservation struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Archive string `json:"archive,omitempty"`
}

type notesRecoverySelection struct {
	publication backupstrategy.NotesRecoveryPublication
	hash        string
}

func collectNotesRecoveryPublications(ctx context.Context, roots []backupstrategy.DirectArchiveRoot, records string, cfg Config) ([]notesRecoverySelection, []NotesRecoveryObservation) {
	var selected []notesRecoverySelection
	var observations []NotesRecoveryObservation
	if records == "" {
		return selected, observations
	}
	for _, root := range roots {
		if root.Name != "application_data" {
			continue
		}
		cohorts := filepath.Join(root.Path, "notes-recovery")
		entries, err := os.ReadDir(cohorts)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return selected, append(observations, NotesRecoveryObservation{Status: "held", Reason: "capture_inventory_unavailable"})
		}
		for _, entry := range entries {
			id := entry.Name()
			if !backupstrategy.ValidNotesRecoveryID(id) || !entry.IsDir() {
				observations = append(observations, NotesRecoveryObservation{Status: "held", Reason: "unknown_capture_entry"})
				continue
			}
			pub, hash, err := backupstrategy.ReadNotesRecoveryPublication(records, id)
			if err != nil || pub.Root != cohorts {
				observations = append(observations, NotesRecoveryObservation{ID: id, Status: "held", Reason: "publication_receipt_missing_or_invalid"})
				continue
			}
			current, err := backupstrategy.InspectNotesRecoveryPublication(ctx, cohorts, id)
			if err != nil || current.Identity != pub.Identity || current.CohortSHA256 != pub.CohortSHA256 {
				observations = append(observations, NotesRecoveryObservation{ID: id, Status: "held", Reason: "published_capture_changed"})
				continue
			}
			previous, err := backupstrategy.ReadNotesRecoveryProtection(backupstrategy.NotesRecoveryProtectionRoot(cfg.StateDir), id)
			if err == nil && previous.PublicationSHA256 == hash && previous.Repository == cfg.Snapshots.Borg.Repository {
				observations = append(observations, NotesRecoveryObservation{ID: id, Status: "previously_cloud_protected", Archive: previous.Archive})
				continue // Immutable existing evidence is rechecked at retirement, not every upload.
			}
			selected = append(selected, notesRecoverySelection{pub, hash})
		}
	}
	return selected, observations
}

// Metadata assurance relies on a producer-verified immutable publication and
// authenticated Borg metadata. It does not stream the large archives again.
func verifyArchivedNotesRecovery(ctx context.Context, runner BorgCommandRunner, archive string, selection notesRecoverySelection, dependency *backupstrategy.NotesRecoveryDependency) error {
	pub := selection.publication
	current, err := backupstrategy.InspectNotesRecoveryPublication(ctx, pub.Root, pub.ID)
	if err != nil || current.Identity != pub.Identity || current.CohortSHA256 != pub.CohortSHA256 {
		return fmt.Errorf("capture changed after source selection")
	}
	if err := verifyFrozenNotesRecovery(ctx, runner, archive, pub, dependency); err != nil {
		return err
	}
	current, err = backupstrategy.InspectNotesRecoveryPublication(ctx, pub.Root, pub.ID)
	if err != nil || current.Identity != pub.Identity {
		return fmt.Errorf("capture changed during archive assurance")
	}
	return nil
}

func verifyFrozenNotesRecovery(ctx context.Context, runner BorgCommandRunner, archive string, pub backupstrategy.NotesRecoveryPublication, dependencies ...*backupstrategy.NotesRecoveryDependency) error {
	root := strings.TrimPrefix(filepath.ToSlash(filepath.Join(pub.Root, pub.ID)), "/")
	files := map[string]backupstrategy.NotesRecoveryFile{}
	dirs := map[string]uint32{}
	for _, file := range pub.Files {
		files[root+"/"+file.Path] = file
	}
	for _, dir := range pub.Directories {
		dirs[path.Join(root, dir.Path)] = dir.Mode
	}
	receipts := []string{"cohort.json", "operational-package/manifest.json", "operational-package/manifest.sha256"}
	if pub.Reference != nil {
		receipts = []string{"cohort.json", "operational-reference.json"}
	}
	if err := verifyFrozenRecoveryInventory(ctx, runner, archive, root, pub.OwnerUID, files, dirs, receipts); err != nil {
		return err
	}
	if pub.Reference == nil {
		return nil
	}
	if len(dependencies) != 1 || dependencies[0] == nil {
		return fmt.Errorf("typed operational dependency missing")
	}
	dep := dependencies[0]
	if dep.Package.PackageID != pub.PackageID || dep.Package.ManifestSHA256 != pub.ManifestSHA256 {
		return fmt.Errorf("operational dependency identity differs")
	}
	root = dep.Package.ArchivePath
	files = map[string]backupstrategy.NotesRecoveryFile{}
	dirs = map[string]uint32{}
	for _, entry := range dep.Package.Entries {
		name := path.Join(root, entry.Path)
		if entry.Type == "directory" {
			dirs[name] = entry.Mode
		} else if entry.Type == "file" {
			files[name] = backupstrategy.NotesRecoveryFile{Path: entry.Path, Size: entry.SizeBytes, Mode: entry.Mode, SHA256: entry.SHA256}
		} else {
			return fmt.Errorf("unsupported dependency entry")
		}
	}
	return verifyFrozenRecoveryInventory(ctx, runner, archive, root, dep.OwnerUID, files, dirs, []string{"manifest.json", "manifest.sha256"})
}

func verifyFrozenRecoveryInventory(ctx context.Context, runner BorgCommandRunner, archive, root string, ownerUID uint64, files map[string]backupstrategy.NotesRecoveryFile, dirs map[string]uint32, receipts []string) error {
	seen := map[string]bool{}
	command := BorgCommand{Args: []string{"list", "--json-lines", "--format", "{path}{type}{mode}{size}{uid}{linktarget}{health}", "::" + archive, "pp:" + root}}
	err := consumeBorgStream(ctx, runner, command, func(reader io.Reader) error {
		decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
		for {
			var item struct {
				directArchiveListItem
				UID *uint64 `json:"uid"`
			}
			if err := decoder.Decode(&item); err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			if seen[item.Path] || !item.Healthy || item.LinkTarget != "" || len(seen) >= 40 || item.UID == nil || *item.UID != ownerUID {
				return fmt.Errorf("unsafe frozen Notes recovery namespace")
			}
			seen[item.Path] = true
			mode, kind, err := parseDirectArchiveBorgMode(item.Mode)
			if err != nil {
				return err
			}
			if expected, ok := dirs[item.Path]; ok {
				if kind != "directory" || item.Type != "d" || mode != expected {
					return fmt.Errorf("capture directory metadata differs")
				}
				continue
			}
			file, ok := files[item.Path]
			if !ok || kind != "file" || item.Type != "-" || mode != file.Mode || item.Size != file.Size {
				return fmt.Errorf("capture file metadata differs")
			}
		}
		if len(seen) != len(files)+len(dirs) {
			return fmt.Errorf("frozen capture inventory is incomplete")
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Only small receipts are extracted; their authenticated bytes bind the
	// capture/enrollment and independent operational dependency manifests.
	for _, name := range receipts {
		file := files[root+"/"+name]
		if file.Size <= 0 || file.Size > 8<<20 {
			return fmt.Errorf("capture receipt is outside its bound")
		}
		var raw []byte
		err := consumeBorgStream(ctx, runner, BorgCommand{Args: []string{"extract", "--stdout", "::" + archive, root + "/" + name}}, func(reader io.Reader) error {
			var err error
			raw, err = io.ReadAll(io.LimitReader(reader, file.Size+1))
			return err
		})
		digest := sha256.Sum256(raw)
		if err != nil || int64(len(raw)) != file.Size || hex.EncodeToString(digest[:]) != file.SHA256 {
			return fmt.Errorf("authenticated capture receipt differs")
		}
	}
	return nil
}

// RecordExistingNotesRecoveryProtection bootstraps old verified publications
// against an exact existing archive. It never uploads or extracts large files.
// Local identity is checked again by retention; a changed publication stays held.
func RecordExistingNotesRecoveryProtection(ctx context.Context, cfg Config, records, id, archive string) (backupstrategy.NotesRecoveryProtection, error) {
	var proof backupstrategy.NotesRecoveryProtection
	if !cfg.Enabled || cfg.Snapshots.Backend != SnapshotBackendBorg || !isDirectCanonicalBorgArchive(archive) || strings.ContainsAny(archive, "/\\\r\n:") {
		return proof, fmt.Errorf("exact enabled Borg owner and canonical archive required")
	}
	info, err := os.Lstat(cfg.StateDir)
	if err != nil || !info.IsDir() {
		return proof, fmt.Errorf("cloud owner directory unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(os.Geteuid()) != owner.Uid {
		return proof, fmt.Errorf("run cloud assurance as the existing state owner")
	}
	pub, hash, err := backupstrategy.ReadNotesRecoveryPublication(records, id)
	if err != nil {
		return proof, err
	}
	backend := BorgSnapshotBackend{}
	runner := backend.runner(cfg)
	runner.DisableRemoteLock = true
	err = WithRemoteLock(ctx, cfg, RemoteLockOptions{Operation: "notes.retention.bootstrap"}, func(ctx context.Context) error {
		manifest, err := readAuthenticatedDirectArchiveManifest(ctx, runner, cfg, archive)
		if err != nil {
			return err
		}
		if preparedDirectArchiveName(manifest) != archive {
			return fmt.Errorf("authenticated archive identity differs from selected archive")
		}
		dep, err := referenceArchiveDependency(manifest.ManifestV3, pub)
		if err != nil {
			return err
		}
		if err := verifyFrozenNotesRecovery(ctx, runner, archive, pub, dep); err != nil {
			return err
		}
		_, currentHash, err := backupstrategy.ReadNotesRecoveryPublication(records, id)
		if err != nil || currentHash != hash {
			return fmt.Errorf("publication changed during archive assurance")
		}
		ref := manifest.Manifest.ArchiveRef
		if manifest.ManifestV2 != nil {
			ref = manifest.ManifestV2.ArchiveRef
		}
		proof = backupstrategy.NotesRecoveryProtection{Schema: backupstrategy.NotesRecoveryProtectionSchema,
			Publication: pub, PublicationSHA256: hash, Dependency: dep,
			Repository: cfg.Snapshots.Borg.Repository, Archive: archive, ArchiveRef: ref,
			ArchiveManifestSHA256: manifest.ManifestSHA256, VerifiedAt: time.Now().UTC(),
			VerificationBasis: "stable_publication_and_authenticated_archive_metadata_v1"}
		return backupstrategy.WriteNotesRecoveryRecord(backupstrategy.NotesRecoveryProtectionRoot(cfg.StateDir), id, proof, 0600)
	})
	return proof, err
}

// Called under the local exclusive lifetime lease before an explicitly reviewed
// local retirement. This is metadata assurance, not a full restore or download.
func RecheckNotesRecoveryProtection(ctx context.Context, cfg Config, proof backupstrategy.NotesRecoveryProtection) error {
	if !cfg.Enabled || cfg.Snapshots.Backend != SnapshotBackendBorg || proof.Repository != cfg.Snapshots.Borg.Repository {
		return fmt.Errorf("exact enabled Borg protection owner required")
	}
	info, err := os.Lstat(cfg.StateDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("cloud owner directory unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(os.Geteuid()) != owner.Uid {
		return fmt.Errorf("run cloud assurance as the existing state owner to preserve cache and lock custody")
	}
	backend := BorgSnapshotBackend{}
	runner := backend.runner(cfg)
	runner.DisableRemoteLock = true
	return WithRemoteLock(ctx, cfg, RemoteLockOptions{Operation: "notes.retention.verify"}, func(ctx context.Context) error {
		manifest, err := readAuthenticatedDirectArchiveManifest(ctx, runner, cfg, proof.Archive)
		if err != nil {
			return err
		}
		archiveRef := manifest.Manifest.ArchiveRef
		if manifest.ManifestV2 != nil {
			archiveRef = manifest.ManifestV2.ArchiveRef
		}
		if manifest.ManifestSHA256 != proof.ArchiveManifestSHA256 || preparedDirectArchiveName(manifest) != proof.Archive || archiveRef != proof.ArchiveRef {
			return fmt.Errorf("authenticated archive identity differs from saved capture protection")
		}
		dep, err := referenceArchiveDependency(manifest.ManifestV3, proof.Publication)
		if err != nil {
			return err
		}
		a, _ := json.Marshal(dep)
		b, _ := json.Marshal(proof.Dependency)
		if string(a) != string(b) {
			return fmt.Errorf("saved operational dependency differs from authenticated archive")
		}
		return verifyFrozenNotesRecovery(ctx, runner, proof.Archive, proof.Publication, proof.Dependency)
	})
}

func protectArchivedNotesRecovery(ctx context.Context, runner BorgCommandRunner, cfg Config, result *DirectArchiveResult, selected []notesRecoverySelection) {
	for _, capture := range selected {
		observation := NotesRecoveryObservation{ID: capture.publication.ID, Status: "held", Reason: "archive_capture_verification_failed"}
		dep, depErr := referenceArchiveDependency(result.ManifestV3, capture.publication)
		if err := verifyArchivedNotesRecovery(ctx, runner, result.Archive, capture, dep); err == nil && depErr == nil {
			receipt := backupstrategy.NotesRecoveryProtection{Schema: backupstrategy.NotesRecoveryProtectionSchema, Publication: capture.publication, PublicationSHA256: capture.hash,
				Dependency: dep,
				Repository: result.Repository, Archive: result.Archive, ArchiveRef: result.ArchiveRef, ArchiveManifestSHA256: result.ManifestSHA256, VerifiedAt: time.Now().UTC(),
				VerificationBasis: "stable_publication_and_authenticated_archive_metadata_v1"}
			if err := backupstrategy.WriteNotesRecoveryRecord(backupstrategy.NotesRecoveryProtectionRoot(cfg.StateDir), capture.publication.ID, receipt, 0600); err == nil {
				observation.Status, observation.Reason = "cloud_protected", ""
				observation.Archive = result.Archive
			} else {
				observation.Reason = "protection_receipt_write_failed"
			}
		}
		result.NotesRecovery = append(result.NotesRecovery, observation)
	}
}

func referenceArchiveDependency(manifest *backupstrategy.DirectArchiveManifestV3, pub backupstrategy.NotesRecoveryPublication) (*backupstrategy.NotesRecoveryDependency, error) {
	if pub.Reference == nil {
		return nil, nil
	}
	if manifest == nil {
		return nil, fmt.Errorf("reference capture requires a typed v3 archive")
	}
	matched := false
	for _, capture := range manifest.NotesRecovery {
		if capture.ID == pub.ID {
			a, _ := json.Marshal(capture)
			b, _ := json.Marshal(pub)
			matched = string(a) == string(b)
			break
		}
	}
	if !matched {
		return nil, fmt.Errorf("capture differs from authenticated v3 declaration")
	}
	for i := range manifest.OperationalDependencies {
		dep := &manifest.OperationalDependencies[i]
		if dep.Package.PackageID == pub.PackageID && dep.Package.ManifestSHA256 == pub.ManifestSHA256 {
			return dep, nil
		}
	}
	return nil, fmt.Errorf("typed capture dependency is absent")
}

// GFS prune has no per-archive exception in the existing command. Refuse the
// mutation rather than pretending a changed display plan can protect a pin.
func checkNotesRecoveryCloudPrunePins(cfg Config, plan SnapshotRetentionPlan) error {
	root := backupstrategy.NotesRecoveryProtectionRoot(cfg.StateDir)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Notes recovery cloud references unavailable: %w", err)
	}
	remove := map[string]bool{}
	for _, entry := range plan.Remove {
		remove[entry.Archive] = true
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || !backupstrategy.ValidNotesRecoveryID(id) || entry.Name() != id+".json" {
			return fmt.Errorf("unknown Notes recovery protection record; cloud prune held")
		}
		receipt, err := backupstrategy.ReadNotesRecoveryProtection(root, id)
		if err != nil || receipt.Repository != cfg.Snapshots.Borg.Repository {
			return fmt.Errorf("invalid or differently bound Notes recovery reference; cloud prune held")
		}
		if remove[receipt.Archive] {
			return fmt.Errorf("cloud prune would remove the exact recovery archive for %s; preserve it pending an explicit recovery-history policy", id)
		}
	}
	return nil
}
