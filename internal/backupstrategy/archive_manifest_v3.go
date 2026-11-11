package backupstrategy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const DirectArchiveRequestSchemaV3 = "loom.direct_archive.request.v3"
const DirectArchiveManifestSchemaV3 = "loom.direct_archive.manifest.v3"

// V3 wraps the unchanged strict v2 contract; it does not smuggle additional
// fields into an old schema. ManifestV2 is only the shared reader's base view.
type DirectArchiveRequestV3 struct {
	Schema        string                     `json:"schema"`
	Archive       DirectArchiveRequestV2     `json:"archive"`
	NotesRecovery []NotesRecoveryPublication `json:"notes_recovery"`
}

type DirectArchiveManifestV3 struct {
	Schema                  string                     `json:"schema"`
	Archive                 DirectArchiveManifestV2    `json:"archive"`
	NotesRecovery           []NotesRecoveryPublication `json:"notes_recovery"`
	OperationalDependencies []NotesRecoveryDependency  `json:"operational_dependencies"`
}

func NotesRecoveryOperationalDependency(ctx context.Context, root string, ref NotesRecoveryOperationalReference) (NotesRecoveryDependency, error) {
	var dep NotesRecoveryDependency
	if _, err := exactAbsolutePath(root); err != nil || root == "/" || ref.Schema != NotesRecoveryReferenceSchema || !validPackageID(ref.PackageID) || normalizeSHA256(ref.ManifestSHA256) == "" {
		return dep, fmt.Errorf("invalid operational dependency reference")
	}
	packageDir := filepath.Join(root, ref.PackageID)
	verified, err := inspectOperationalVerificationReceipt(ctx, packageDir)
	if err != nil || verified.ManifestSHA256 != ref.ManifestSHA256 {
		return dep, fmt.Errorf("operational dependency lacks unchanged prior verification: %s", ref.PackageID)
	}
	var manifest OperationalPackageManifest
	raw, err := notesRecoveryReadJSON(filepath.Join(packageDir, OperationalManifestFile), operationalManifestMaxBytes, &manifest)
	if err != nil || notesRecoveryHash(raw) != verified.ManifestSHA256 || manifest.PackageID != verified.PackageID || manifest.SchemaHead != verified.SchemaHead || validateOperationalManifest(manifest) != nil || validateRedactionEvidence(manifest) != nil {
		return dep, fmt.Errorf("operational dependency manifest differs from saved verification")
	}
	before, _, err := retentionIdentity(packageDir)
	if err != nil {
		return dep, err
	}
	expected := map[string]NotesRecoveryFile{
		OperationalManifestFile:     {SHA256: ref.ManifestSHA256, Size: -1},
		OperationalManifestHashFile: {SHA256: notesRecoveryHash([]byte(ref.ManifestSHA256 + "  " + OperationalManifestFile + "\n")), Size: int64(len(ref.ManifestSHA256 + "  " + OperationalManifestFile + "\n"))},
	}
	for _, file := range manifest.Artifacts {
		expected[file.Path] = NotesRecoveryFile{SHA256: file.SHA256, Size: file.SizeBytes}
	}
	info, err := os.Lstat(packageDir)
	if err != nil {
		return dep, err
	}
	uid, ok := statUint(info.Sys(), "Uid")
	if !ok {
		return dep, fmt.Errorf("dependency owner unavailable")
	}
	dep.OwnerUID = uid
	entries := []DirectArchiveEntry{{Path: ".", Type: "directory", Mode: uint32(info.Mode().Perm()), ModifiedUnixNano: directArchiveModifiedUnixNano(info.ModTime())}}
	files, err := os.ReadDir(packageDir)
	if err != nil {
		return dep, err
	}
	var total int64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return dep, err
		}
		meta, err := os.Lstat(filepath.Join(packageDir, file.Name()))
		if err != nil {
			return dep, err
		}
		want, exists := expected[file.Name()]
		links, linkOK := statUint(meta.Sys(), "Nlink")
		owner, ownerOK := statUint(meta.Sys(), "Uid")
		if !exists || !meta.Mode().IsRegular() || meta.Mode().Perm() != 0600 || !linkOK || links != 1 || !ownerOK || owner != uid || (want.Size >= 0 && want.Size != meta.Size()) {
			return dep, fmt.Errorf("dependency file inventory changed")
		}
		entries = append(entries, DirectArchiveEntry{Path: file.Name(), Type: "file", Mode: 0600, SizeBytes: meta.Size(), ModifiedUnixNano: directArchiveModifiedUnixNano(meta.ModTime()), SHA256: want.SHA256})
		total += meta.Size()
		delete(expected, file.Name())
	}
	if len(expected) != 0 {
		return dep, fmt.Errorf("dependency files missing")
	}
	after, _, err := retentionIdentity(packageDir)
	if err != nil || before != after {
		return dep, ErrDirectArchiveSourceChanged
	}
	if _, err := inspectOperationalVerificationReceipt(ctx, packageDir); err != nil {
		return dep, ErrDirectArchiveSourceChanged
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	digest, err := directArchiveEntriesDigest(entries)
	if err != nil {
		return dep, err
	}
	dep.Package = DirectArchiveOperationalPackageManifestV2{Schema: OperationalPackageSchema, SourcePath: packageDir, ArchivePath: directArchivePath(packageDir),
		PackageID: ref.PackageID, ManifestSHA256: ref.ManifestSHA256, SchemaHead: verified.SchemaHead, CreatedAt: verified.CreatedAt, ArtifactCount: verified.ArtifactCount,
		EntryCount: int64(len(entries)), TotalBytes: total, EntriesSHA256: digest, Entries: entries}
	if _, err := validateDirectArchiveOperationalPackageManifestV2(dep.Package, nil, map[string]DirectArchiveEntry{}); err != nil {
		return dep, err
	}
	return dep, nil
}

func validateNotesRecoveryDependency(dep NotesRecoveryDependency, pub NotesRecoveryPublication) error {
	if pub.Reference == nil || dep.Package.PackageID != pub.PackageID || dep.Package.ManifestSHA256 != pub.ManifestSHA256 {
		return fmt.Errorf("capture operational dependency identity differs")
	}
	_, err := validateDirectArchiveOperationalPackageManifestV2(dep.Package, nil, map[string]DirectArchiveEntry{})
	return err
}

func PrepareDirectArchiveManifestV3(ctx context.Context, input DirectArchiveManifestPrepareInputV2, request DirectArchiveRequestV3) (PreparedDirectArchiveManifest, error) {
	if request.Schema != DirectArchiveRequestSchemaV3 {
		return PreparedDirectArchiveManifest{}, fmt.Errorf("explicit v3 request required")
	}
	input.Request = request.Archive
	base, err := PrepareDirectArchiveManifestV2(ctx, input)
	if err != nil {
		return base, err
	}
	v3 := DirectArchiveManifestV3{Schema: DirectArchiveManifestSchemaV3, Archive: *base.ManifestV2, NotesRecovery: append([]NotesRecoveryPublication(nil), request.NotesRecovery...)}
	seen := map[string]bool{}
	for _, pub := range v3.NotesRecovery {
		current, err := InspectNotesRecoveryPublication(ctx, pub.Root, pub.ID)
		if err != nil || current.Identity != pub.Identity || current.CohortSHA256 != pub.CohortSHA256 || pub.Reference == nil {
			return base, fmt.Errorf("reference capture changed or incomplete: %s", pub.ID)
		}
		if seen[pub.PackageID] {
			continue
		}
		seen[pub.PackageID] = true
		dep, err := NotesRecoveryOperationalDependency(ctx, filepath.Dir(v3.Archive.OperationalPackage.SourcePath), *pub.Reference)
		if err != nil {
			return base, err
		}
		v3.OperationalDependencies = append(v3.OperationalDependencies, dep)
	}
	sort.Slice(v3.NotesRecovery, func(i, j int) bool { return v3.NotesRecovery[i].ID < v3.NotesRecovery[j].ID })
	sort.Slice(v3.OperationalDependencies, func(i, j int) bool {
		return v3.OperationalDependencies[i].Package.PackageID < v3.OperationalDependencies[j].Package.PackageID
	})
	if err := validateDirectArchiveManifestV3(v3); err != nil {
		return base, err
	}
	raw, err := json.MarshalIndent(v3, "", "  ")
	if err != nil {
		return base, err
	}
	raw = append(raw, '\n')
	base.ManifestV3 = &v3
	base.ManifestBytes = raw
	base.ManifestSHA256 = notesRecoveryHash(raw)
	base.HashFileBytes = []byte(base.ManifestSHA256 + "  " + DirectArchiveManifestFile + "\n")
	return base, nil
}

func validateDirectArchiveManifestV3(v3 DirectArchiveManifestV3) error {
	if v3.Schema != DirectArchiveManifestSchemaV3 || len(v3.NotesRecovery) == 0 || len(v3.NotesRecovery) > 128 || len(v3.OperationalDependencies) > 128 {
		return fmt.Errorf("invalid or unbounded reference archive envelope")
	}
	if err := validateDirectArchiveManifestV2(v3.Archive); err != nil {
		return err
	}
	var captureRoot string
	roots := []string{v3.Archive.OperationalPackage.SourcePath, v3.Archive.ProvenancePackage.SourcePath}
	for _, root := range v3.Archive.Roots {
		roots = append(roots, root.SourcePath)
		if root.Name == "application_data" {
			captureRoot = filepath.Join(root.SourcePath, "notes-recovery")
		}
	}
	packages := map[string]NotesRecoveryDependency{}
	prior := map[string]DirectArchiveEntry{}
	for _, dep := range v3.OperationalDependencies {
		pkg := dep.Package
		if _, exists := packages[pkg.PackageID]; exists {
			return fmt.Errorf("duplicate operational dependency")
		}
		if pkg.PackageID == v3.Archive.OperationalPackage.PackageID {
			a, _ := json.Marshal(pkg)
			b, _ := json.Marshal(v3.Archive.OperationalPackage)
			if string(a) != string(b) {
				return fmt.Errorf("primary dependency differs")
			}
		} else {
			if filepath.Dir(pkg.SourcePath) != filepath.Dir(v3.Archive.OperationalPackage.SourcePath) {
				return fmt.Errorf("dependency escapes the operational owner")
			}
			if _, err := validateDirectArchiveOperationalPackageManifestV2(pkg, roots, prior); err != nil {
				return err
			}
			roots = append(roots, pkg.SourcePath)
		}
		packages[pkg.PackageID] = dep
	}
	ids := map[string]bool{}
	used := map[string]bool{}
	for _, pub := range v3.NotesRecovery {
		if validateNotesRecoveryPublication(pub, pub.ID) != nil || pub.Root != captureRoot || captureRoot == "" || ids[pub.ID] || pub.Reference == nil {
			return fmt.Errorf("invalid reference capture boundary")
		}
		ids[pub.ID] = true
		for _, exclusion := range v3.Archive.Exclusions {
			if exclusion.Root == "application_data" && directArchivePortablePathsOverlap(path.Join("notes-recovery", pub.ID), exclusion.RelativePath) {
				return fmt.Errorf("reference capture overlaps an exclusion")
			}
		}
		dep, exists := packages[pub.PackageID]
		if !exists || validateNotesRecoveryDependency(dep, pub) != nil {
			return fmt.Errorf("missing or mismatched capture dependency")
		}
		used[pub.PackageID] = true
	}
	if len(used) != len(packages) {
		return fmt.Errorf("unused operational dependency")
	}
	return nil
}

func VerifyDirectArchiveV3PackageStability(ctx context.Context, input DirectArchiveManifestPrepareInputV2, expected PreparedDirectArchiveManifest) error {
	if expected.ManifestV3 == nil {
		return fmt.Errorf("v3 manifest required")
	}
	if err := VerifyDirectArchiveV2PackageStability(ctx, input, expected); err != nil {
		return err
	}
	for _, pub := range expected.ManifestV3.NotesRecovery {
		current, err := InspectNotesRecoveryPublication(ctx, pub.Root, pub.ID)
		if err != nil || current.Identity != pub.Identity {
			return ErrDirectArchiveSourceChanged
		}
		dep, err := NotesRecoveryOperationalDependency(ctx, filepath.Dir(input.Request.OperationalPackage.Path), *pub.Reference)
		if err != nil {
			return err
		}
		for _, want := range expected.ManifestV3.OperationalDependencies {
			if want.Package.PackageID == pub.PackageID {
				a, _ := json.Marshal(dep)
				b, _ := json.Marshal(want)
				if string(a) != string(b) {
					return ErrDirectArchiveSourceChanged
				}
			}
		}
	}
	return nil
}

func AdditionalNotesRecoveryPackages(prepared PreparedDirectArchiveManifest) []NotesRecoveryDependency {
	var out []NotesRecoveryDependency
	if prepared.ManifestV3 != nil {
		for _, dep := range prepared.ManifestV3.OperationalDependencies {
			if dep.Package.PackageID != prepared.ManifestV3.Archive.OperationalPackage.PackageID {
				out = append(out, dep)
			}
		}
	}
	return out
}

func DiscoverNotesRecoveryReferences(ctx context.Context, roots []DirectArchiveRoot, records string) ([]NotesRecoveryPublication, error) {
	var publications []NotesRecoveryPublication
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
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() || !ValidNotesRecoveryID(entry.Name()) {
				continue
			}
			pub, _, err := ReadNotesRecoveryPublication(records, entry.Name())
			if err != nil || pub.Reference == nil || pub.Root != cohorts {
				continue
			}
			current, err := InspectNotesRecoveryPublication(ctx, cohorts, pub.ID)
			if err == nil && current.Identity == pub.Identity && current.CohortSHA256 == pub.CohortSHA256 {
				publications = append(publications, pub)
			}
		}
	}
	return publications, nil
}

type ResolvedNotesRecovery struct {
	CapturePath            string `json:"capture_path"`
	MainStatePath          string `json:"main_state_path"`
	OperationalPackagePath string `json:"operational_package_path"`
}

// Resolves a selected capture within a previously verified extraction. It
// verifies the small cohort identity and the referenced operational package;
// it does not restore services or claim application/client consistency.
func ResolveExtractedNotesRecovery(ctx context.Context, extraction string, prepared PreparedDirectArchiveManifest, id string) (ResolvedNotesRecovery, error) {
	var resolved ResolvedNotesRecovery
	if prepared.ManifestV3 == nil || !ValidNotesRecoveryID(id) {
		return resolved, fmt.Errorf("typed reference archive and exact capture required")
	}
	if err := validateDirectArchiveManifestV3(*prepared.ManifestV3); err != nil {
		return resolved, err
	}
	if _, err := exactAbsolutePath(extraction); err != nil {
		return resolved, err
	}
	for _, pub := range prepared.ManifestV3.NotesRecovery {
		if pub.ID != id {
			continue
		}
		capture, err := extractedNotesRecoveryPath(extraction, path.Join(directArchivePath(pub.Root), pub.ID))
		if err != nil {
			return resolved, err
		}
		if err := requireRealDirectory(capture); err != nil {
			return resolved, err
		}
		var cohort any
		raw, err := notesRecoveryReadJSON(filepath.Join(capture, "cohort.json"), 8<<20, &cohort)
		if err != nil || notesRecoveryHash(raw) != pub.CohortSHA256 {
			return resolved, fmt.Errorf("extracted capture identity differs")
		}
		for _, dep := range prepared.ManifestV3.OperationalDependencies {
			if dep.Package.PackageID != pub.PackageID {
				continue
			}
			packageDir, err := extractedNotesRecoveryPath(extraction, dep.Package.ArchivePath)
			if err != nil {
				return resolved, err
			}
			if err := requireRealDirectory(packageDir); err != nil {
				return resolved, err
			}
			verified, err := VerifyOperationalPackage(ctx, OperationalPackageVerificationInput{PackageDir: packageDir, ExpectedPackageID: pub.PackageID, ExpectedManifestSHA256: pub.ManifestSHA256})
			if err != nil || verified.Status != "succeeded" {
				return resolved, fmt.Errorf("extracted operational dependency is not verified")
			}
			mainState := filepath.Join(capture, "main-state.tar.gz")
			for _, file := range pub.Files {
				if file.Path == "main-state.tar.gz" {
					v, err := inspectStableFile(mainState, file.Size, false)
					if err != nil || v.Evidence.SHA256 != file.SHA256 {
						return resolved, fmt.Errorf("selected Notes state archive differs")
					}
				}
			}
			return ResolvedNotesRecovery{CapturePath: capture, MainStatePath: filepath.Join(capture, "main-state.tar.gz"), OperationalPackagePath: packageDir}, nil
		}
	}
	return resolved, fmt.Errorf("capture is not declared by this archive")
}

func extractedNotesRecoveryPath(root, relative string) (string, error) {
	full, err := confinedOperationalPath(root, relative)
	if err != nil {
		return "", err
	}
	if err := requireRealDirectory(root); err != nil {
		return "", err
	}
	current := root
	for _, part := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := requireRealDirectory(current); err != nil {
			return "", fmt.Errorf("unsafe extracted recovery ancestor: %w", err)
		}
	}
	return full, nil
}
