package backupstrategy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const NotesRecoveryPublicationSchema = "loom.notes_recovery.publication.v1"
const NotesRecoveryProtectionSchema = "loom.notes_recovery.protection.v1"

const NotesRecoveryReferenceSchema = "loom.notes_recovery.operational_reference.v1"

type NotesRecoveryOperationalReference struct {
	Schema         string `json:"schema"`
	PackageID      string `json:"package_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type NotesRecoveryDependency struct {
	Package  DirectArchiveOperationalPackageManifestV2 `json:"package"`
	OwnerUID uint64                                    `json:"owner_uid"`
}

var notesRecoveryID = regexp.MustCompile(`^notes-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

type NotesRecoveryFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

type NotesRecoveryDirectory struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

// Publication is minted by the root capture publisher after verified capture
// and atomic publication. It contains identities, never note or secret contents.
type NotesRecoveryPublication struct {
	Reference        *NotesRecoveryOperationalReference `json:"operational_reference,omitempty"`
	Schema           string                             `json:"schema"`
	ID               string                             `json:"id"`
	Root             string                             `json:"root"`
	CapturedAt       time.Time                          `json:"captured_at"`
	RecordedAt       time.Time                          `json:"recorded_at"`
	PackageID        string                             `json:"package_id"`
	ManifestSHA256   string                             `json:"manifest_sha256"`
	CohortSHA256     string                             `json:"cohort_sha256"`
	EnrollmentSHA256 string                             `json:"enrollment_sha256,omitempty"`
	Identity         string                             `json:"filesystem_identity"`
	AllocatedBytes   int64                              `json:"allocated_bytes"`
	OwnerUID         uint64                             `json:"owner_uid"`
	Files            []NotesRecoveryFile                `json:"files"`
	Directories      []NotesRecoveryDirectory           `json:"directories"`
}

type NotesRecoveryProtection struct {
	Dependency            *NotesRecoveryDependency `json:"operational_dependency,omitempty"`
	Schema                string                   `json:"schema"`
	Publication           NotesRecoveryPublication `json:"publication"`
	PublicationSHA256     string                   `json:"publication_sha256"`
	Repository            string                   `json:"repository"`
	Archive               string                   `json:"archive"`
	ArchiveRef            string                   `json:"archive_ref"`
	ArchiveManifestSHA256 string                   `json:"archive_manifest_sha256"`
	VerifiedAt            time.Time                `json:"verified_at"`
	VerificationBasis     string                   `json:"verification_basis"`
}

func ValidNotesRecoveryID(id string) bool { return notesRecoveryID.MatchString(id) }

func NotesRecoveryPublicationRoot(dataDir string) string {
	return filepath.Join(dataDir, "notes-recovery-publications")
}

func NotesRecoveryProtectionRoot(cloudState string) string {
	return filepath.Join(cloudState, "notes-recovery-protection")
}

func notesRecoveryReadJSON(path string, limit int64, out any) ([]byte, error) {
	file, err := inspectStableFile(path, limit, true)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(file.Raw))
	if err := decoder.Decode(out); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return file.Raw, nil
}

func notesRecoveryHash(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// InspectNotesRecoveryPublication checks the fixed v1/v2 layout and declared
// hashes. It reads only small manifests/references, not archive payloads.
func InspectNotesRecoveryPublication(ctx context.Context, root, id string) (NotesRecoveryPublication, error) {
	p := NotesRecoveryPublication{Schema: NotesRecoveryPublicationSchema, ID: id, Root: root}
	if !ValidNotesRecoveryID(id) || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return p, fmt.Errorf("invalid Notes recovery identity or root")
	}
	if err := requireRealDirectory(root); err != nil {
		return p, err
	}
	cohortRoot := filepath.Join(root, id)
	if err := requireRealDirectory(cohortRoot); err != nil {
		return p, err
	}
	initialIdentity, _, err := retentionIdentity(cohortRoot)
	if err != nil {
		return p, err
	}
	var cohort struct {
		Schema, ID              string
		CapturedAt              time.Time `json:"captured_at"`
		PackageID               string    `json:"package_id"`
		ManifestSHA256          string    `json:"manifest_sha256"`
		MainStateSHA256         string    `json:"main_state_sha256"`
		ClientSHA256            string    `json:"client_sha256"`
		EnrollmentSHA256        string    `json:"enrollment_inventory_sha256"`
		RecoveryInventorySHA256 string    `json:"recovery_inventory_sha256"`
		ReferenceSHA256         string    `json:"operational_reference_sha256"`
	}
	cohortRaw, err := notesRecoveryReadJSON(filepath.Join(cohortRoot, "cohort.json"), 8<<20, &cohort)
	if err != nil {
		return p, err
	}
	if (cohort.Schema != "loom.notes_recovery.v1" && cohort.Schema != "loom.notes_recovery.v2") || cohort.ID != id || cohort.CapturedAt.IsZero() ||
		!validPackageID(cohort.PackageID) || normalizeSHA256(cohort.ManifestSHA256) == "" || normalizeSHA256(cohort.MainStateSHA256) == "" {
		return p, fmt.Errorf("incomplete or unsupported Notes capture receipt")
	}
	p.CapturedAt, p.PackageID, p.ManifestSHA256 = cohort.CapturedAt, cohort.PackageID, cohort.ManifestSHA256
	p.CohortSHA256, p.EnrollmentSHA256 = notesRecoveryHash(cohortRaw), cohort.EnrollmentSHA256
	if p.EnrollmentSHA256 != "" && normalizeSHA256(p.EnrollmentSHA256) == "" {
		return p, fmt.Errorf("invalid Notes enrollment hash")
	}
	expected := map[string]NotesRecoveryFile{
		"cohort.json":       {Path: "cohort.json", Size: int64(len(cohortRaw)), SHA256: p.CohortSHA256},
		"main-state.tar.gz": {Path: "main-state.tar.gz", Size: -1, SHA256: cohort.MainStateSHA256},
	}
	if cohort.RecoveryInventorySHA256 != "" {
		var inventory map[string]any
		raw, err := notesRecoveryReadJSON(filepath.Join(cohortRoot, "recovery-enrollment.json"), 4<<20, &inventory)
		if err != nil || normalizeSHA256(cohort.RecoveryInventorySHA256) == "" || notesRecoveryHash(raw) != cohort.RecoveryInventorySHA256 || cohort.RecoveryInventorySHA256 != cohort.EnrollmentSHA256 || inventory["schema_version"] != "notes_workspace.recovery_enrollment.v1" {
			return p, fmt.Errorf("Notes recovery inventory differs from the capture receipt")
		}
		expected["recovery-enrollment.json"] = NotesRecoveryFile{Path: "recovery-enrollment.json", Size: int64(len(raw)), SHA256: cohort.RecoveryInventorySHA256}
	}
	if cohort.Schema == "loom.notes_recovery.v2" {
		var reference NotesRecoveryOperationalReference
		raw, err := notesRecoveryReadJSON(filepath.Join(cohortRoot, "operational-reference.json"), 4096, &reference)
		if err != nil || reference.Schema != NotesRecoveryReferenceSchema || reference.PackageID != p.PackageID || reference.ManifestSHA256 != p.ManifestSHA256 || notesRecoveryHash(raw) != cohort.ReferenceSHA256 {
			return p, fmt.Errorf("operational reference differs from the capture receipt")
		}
		p.Reference = &reference
		expected["operational-reference.json"] = NotesRecoveryFile{Path: "operational-reference.json", Size: int64(len(raw)), SHA256: notesRecoveryHash(raw)}
	} else {
		var manifest OperationalPackageManifest
		manifestRaw, err := notesRecoveryReadJSON(filepath.Join(cohortRoot, "operational-package", OperationalManifestFile), operationalManifestMaxBytes, &manifest)
		if err != nil {
			return p, err
		}
		if notesRecoveryHash(manifestRaw) != p.ManifestSHA256 || manifest.PackageID != p.PackageID || validateOperationalManifest(manifest) != nil || validateRedactionEvidence(manifest) != nil {
			return p, fmt.Errorf("embedded operational manifest differs from the capture receipt")
		}
		expected["operational-package/manifest.json"] = NotesRecoveryFile{Path: "operational-package/manifest.json", Size: int64(len(manifestRaw)), SHA256: p.ManifestSHA256}
		hashLine := p.ManifestSHA256 + "  " + OperationalManifestFile + "\n"
		expected["operational-package/manifest.sha256"] = NotesRecoveryFile{Path: "operational-package/manifest.sha256", Size: int64(len(hashLine)), SHA256: notesRecoveryHash([]byte(hashLine))}
		for _, artifact := range manifest.Artifacts {
			name := "operational-package/" + artifact.Path
			expected[name] = NotesRecoveryFile{Path: name, Size: artifact.SizeBytes, SHA256: artifact.SHA256}
		}
	}
	if cohort.ClientSHA256 != "" {
		if normalizeSHA256(cohort.ClientSHA256) == "" {
			return p, fmt.Errorf("invalid client capture hash")
		}
		expected["mac-client.tar.gz"] = NotesRecoveryFile{Path: "mac-client.tar.gz", Size: -1, SHA256: cohort.ClientSHA256}
	}
	ownerInfo, err := os.Lstat(cohortRoot)
	if err != nil {
		return p, err
	}
	owner, ok := statUint(ownerInfo.Sys(), "Uid")
	if !ok {
		return p, fmt.Errorf("capture owner unavailable")
	}
	p.OwnerUID = owner
	err = filepath.Walk(cohortRoot, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(cohortRoot, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		uid, uidOK := statUint(info.Sys(), "Uid")
		if !uidOK || uid != owner {
			return fmt.Errorf("capture ownership changed")
		}
		if info.IsDir() {
			if (rel != "." && (rel != "operational-package" || p.Reference != nil)) || (info.Mode().Perm() != 0700 && info.Mode().Perm() != 0750) {
				return fmt.Errorf("unexpected capture directory")
			}
			p.Directories = append(p.Directories, NotesRecoveryDirectory{Path: rel, Mode: uint32(info.Mode().Perm())})
			return nil
		}
		file, exists := expected[rel]
		links, linksOK := statUint(info.Sys(), "Nlink")
		if !exists || !info.Mode().IsRegular() || !linksOK || links != 1 ||
			(info.Mode().Perm() != 0600 && info.Mode().Perm() != 0640) || (file.Size >= 0 && file.Size != info.Size()) || info.Size() <= 0 {
			return fmt.Errorf("unexpected or changed capture file")
		}
		file.Size, file.Mode = info.Size(), uint32(info.Mode().Perm())
		p.Files = append(p.Files, file)
		delete(expected, rel)
		return nil
	})
	if err != nil {
		return p, err
	}
	wantDirs := 2
	if p.Reference != nil {
		wantDirs = 1
	}
	if len(expected) != 0 || len(p.Files) > 32 || len(p.Directories) != wantDirs {
		return p, fmt.Errorf("incomplete Notes capture inventory")
	}
	identity, allocated, err := retentionIdentity(cohortRoot)
	if err != nil {
		return p, err
	}
	if identity != initialIdentity {
		return p, fmt.Errorf("capture changed during inventory")
	}
	p.Identity, p.AllocatedBytes = identity, allocated
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	return p, nil
}

// RecordNotesRecoveryPublication is called only by the root capture publisher
// after hashing/verification and final publication, or after an explicit deep
// operator verification. Ordinary inventory must never mint these receipts.
func RecordNotesRecoveryPublication(ctx context.Context, root, id, records string, deep bool, operationalRoot ...string) (NotesRecoveryPublication, error) {
	p, err := InspectNotesRecoveryPublication(ctx, root, id)
	if err != nil {
		return p, err
	}
	if p.Reference != nil {
		if len(operationalRoot) != 1 {
			return p, fmt.Errorf("configured operational dependency root required")
		}
		if deep {
			v, err := VerifyAndRecordOperationalPackage(ctx, OperationalPackageVerificationInput{PackageDir: filepath.Join(operationalRoot[0], p.PackageID), ExpectedPackageID: p.PackageID, ExpectedManifestSHA256: p.ManifestSHA256})
			if err != nil || v.Status != "succeeded" {
				return p, fmt.Errorf("operational dependency verification failed")
			}
		}
		if _, err := NotesRecoveryOperationalDependency(ctx, operationalRoot[0], *p.Reference); err != nil {
			return p, err
		}
	}
	if deep {
		for _, file := range p.Files {
			if err := ctx.Err(); err != nil {
				return p, err
			}
			got, err := inspectStableFile(filepath.Join(root, id, filepath.FromSlash(file.Path)), file.Size, false)
			if err != nil || got.Evidence.SHA256 != file.SHA256 {
				return p, fmt.Errorf("capture hash verification failed for %s", file.Path)
			}
		}
	}
	identity, _, err := retentionIdentity(filepath.Join(root, id))
	if err != nil || identity != p.Identity {
		return p, fmt.Errorf("capture changed during publication verification")
	}
	p.RecordedAt = time.Now().UTC()
	if err := WriteNotesRecoveryRecord(records, id, p, 0644); err != nil {
		return p, err
	}
	return p, nil
}

func WriteNotesRecoveryRecord(root, id string, value any, mode os.FileMode) error {
	if !ValidNotesRecoveryID(id) {
		return fmt.Errorf("invalid Notes recovery record identity")
	}
	return writeRecoveryRecord(root, id+".json", value, mode)
}

func writeRecoveryRecord(root, name string, value any, mode os.FileMode) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return fmt.Errorf("invalid Notes recovery record location")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe Notes recovery record directory")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("Notes recovery record exceeds bound")
	}
	file, err := os.CreateTemp(root, ".notes-record-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(file.Name(), filepath.Join(root, name)); err != nil {
		return err
	}
	return syncRetentionDirectory(root)
}

func ReadNotesRecoveryPublication(root, id string) (NotesRecoveryPublication, string, error) {
	var p NotesRecoveryPublication
	if !ValidNotesRecoveryID(id) {
		return p, "", fmt.Errorf("invalid capture ID")
	}
	if err := checkNotesRecoveryRecordCustody(root, id); err != nil {
		return p, "", err
	}
	raw, err := notesRecoveryReadJSON(filepath.Join(root, id+".json"), 64<<10, &p)
	if err != nil {
		return p, "", err
	}
	if validateNotesRecoveryPublication(p, id) != nil {
		return p, "", fmt.Errorf("invalid capture publication record")
	}
	return p, notesRecoveryHash(raw), nil
}

func ReadNotesRecoveryProtection(root, id string) (NotesRecoveryProtection, error) {
	var p NotesRecoveryProtection
	if !ValidNotesRecoveryID(id) {
		return p, fmt.Errorf("invalid capture ID")
	}
	if err := checkNotesRecoveryRecordCustody(root, id); err != nil {
		return p, err
	}
	_, err := notesRecoveryReadJSON(filepath.Join(root, id+".json"), 64<<10, &p)
	if err != nil {
		return p, err
	}
	pub, _ := json.Marshal(p.Publication)
	if p.Schema != NotesRecoveryProtectionSchema || p.Publication.ID != id || p.Repository == "" || p.Archive == "" || p.ArchiveRef == "" || p.VerifiedAt.IsZero() ||
		validateNotesRecoveryPublication(p.Publication, id) != nil ||
		p.PublicationSHA256 != notesRecoveryHash(pub) || normalizeSHA256(p.ArchiveManifestSHA256) == "" ||
		p.VerificationBasis != "stable_publication_and_authenticated_archive_metadata_v1" {
		return p, fmt.Errorf("invalid Notes protection record")
	}
	if p.Publication.Reference != nil && (p.Dependency == nil || validateNotesRecoveryDependency(*p.Dependency, p.Publication) != nil) {
		return p, fmt.Errorf("capture dependency protection is missing or invalid")
	}
	return p, nil
}

func validateNotesRecoveryPublication(p NotesRecoveryPublication, id string) error {
	if p.Schema != NotesRecoveryPublicationSchema || !ValidNotesRecoveryID(id) || p.ID != id ||
		!filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Root == "/" ||
		p.RecordedAt.IsZero() || p.CapturedAt.IsZero() || !validPackageID(p.PackageID) ||
		normalizeSHA256(p.Identity) == "" || normalizeSHA256(p.ManifestSHA256) == "" || normalizeSHA256(p.CohortSHA256) == "" ||
		len(p.Files) < 3 || len(p.Files) > 32 || (p.Reference == nil && len(p.Directories) != 2) || (p.Reference != nil && len(p.Directories) != 1) {
		return fmt.Errorf("invalid capture publication record")
	}
	seen := map[string]bool{}
	for _, file := range p.Files {
		if file.Path == "" || filepath.ToSlash(filepath.Clean(file.Path)) != file.Path || filepath.IsAbs(file.Path) ||
			strings.HasPrefix(file.Path, "../") || seen[file.Path] || file.Size <= 0 || normalizeSHA256(file.SHA256) == "" ||
			(file.Mode != 0600 && file.Mode != 0640) {
			return fmt.Errorf("invalid publication file inventory")
		}
		seen[file.Path] = true
	}
	if !seen["cohort.json"] || !seen["main-state.tar.gz"] {
		return fmt.Errorf("incomplete publication file inventory")
	}
	if p.Reference == nil {
		if !seen["operational-package/manifest.json"] || !seen["operational-package/manifest.sha256"] {
			return fmt.Errorf("embedded package inventory missing")
		}
	} else if p.Reference.Schema != NotesRecoveryReferenceSchema || p.Reference.PackageID != p.PackageID || p.Reference.ManifestSHA256 != p.ManifestSHA256 || !seen["operational-reference.json"] {
		return fmt.Errorf("invalid operational reference inventory")
	}
	seen = map[string]bool{}
	for _, dir := range p.Directories {
		if (dir.Path != "." && (dir.Path != "operational-package" || p.Reference != nil)) || seen[dir.Path] || (dir.Mode != 0700 && dir.Mode != 0750) {
			return fmt.Errorf("invalid publication directory inventory")
		}
		seen[dir.Path] = true
	}
	return nil
}

func checkNotesRecoveryRecordCustody(root, id string) error {
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	dir, err := os.Lstat(root)
	if err != nil || dir.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe recovery record directory")
	}
	owner, ok := statUint(dir.Sys(), "Uid")
	if !ok {
		return fmt.Errorf("recovery record owner unavailable")
	}
	parent, err := os.Lstat(filepath.Dir(root))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe recovery record parent")
	}
	parentOwner, parentOK := statUint(parent.Sys(), "Uid")
	if !parentOK || (owner != 0 && owner != parentOwner) {
		return fmt.Errorf("untrusted recovery record directory owner")
	}
	info, err := os.Lstat(filepath.Join(root, id+".json"))
	if err != nil {
		return err
	}
	uid, uidOK := statUint(info.Sys(), "Uid")
	links, linksOK := statUint(info.Sys(), "Nlink")
	if !uidOK || (uid != 0 && uid != owner) || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !linksOK || links != 1 {
		return fmt.Errorf("unsafe recovery record custody")
	}
	return nil
}
