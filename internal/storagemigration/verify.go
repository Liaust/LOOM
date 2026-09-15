package storagemigration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

type VerifyFinding struct {
	Code    string `json:"code"`
	EntryID string `json:"storage_entry_id,omitempty"`
	RefID   string `json:"physical_ref_id,omitempty"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail"`
}
type VerifyInput struct {
	Details        []storagecatalog.EntryDetail
	CanonicalRoots []string
	ClassRoots     map[string][]string
}
type VerifyResult struct {
	CheckedAvailableRefs int             `json:"checked_available_refs"`
	Findings             []VerifyFinding `json:"findings,omitempty"`
	OK                   bool            `json:"ok"`
}

func Verify(input VerifyInput) VerifyResult {
	result := VerifyResult{OK: true}
	custodyPaths := map[string]string{}
	for _, detail := range input.Details {
		for _, ref := range detail.PhysicalRefs {
			if !storagecatalog.PhysicalRefAvailable(ref) {
				continue
			}
			result.CheckedAvailableRefs++
			class, ok := storagecatalog.ClassifyPhysicalRef(ref)
			if !ok {
				result.Findings = append(result.Findings, finding("copy_class_mismatch", detail, ref, ref.URI, "ref kind has no canonical class"))
				continue
			}
			pathValue, local := storagecatalog.PhysicalRefLocalPath(ref)
			if !local {
				if class != storagecatalog.PhysicalRefClassCurrentSource && class != storagecatalog.PhysicalRefClassCloudCopy {
					result.Findings = append(result.Findings, finding("copy_class_mismatch", detail, ref, ref.URI, "filesystem copy has no local absolute path"))
				}
				continue
			}
			if _, err := os.Lstat(pathValue); err != nil {
				result.Findings = append(result.Findings, finding("dangling_available_ref", detail, ref, pathValue, err.Error()))
			}
			// Archive refs retain their more specific operational resolver role,
			// but the architecture still treats the archive tree as canonical
			// LOOM custody. Both custody roles therefore share root-escape and
			// duplicate-path verification.
			if class == storagecatalog.PhysicalRefClassCanonicalCustody || class == storagecatalog.PhysicalRefClassArchiveCopy {
				if !withinAny(pathValue, input.CanonicalRoots) {
					result.Findings = append(result.Findings, finding("root_escape", detail, ref, pathValue, "canonical custody ref is outside canonical roots"))
				}
				if prior := custodyPaths[pathValue]; prior != "" && prior != ref.StoragePhysicalRefID {
					result.Findings = append(result.Findings, finding("duplicate_canonical_custody_path", detail, ref, pathValue, fmt.Sprintf("also referenced by %s", prior)))
				} else {
					custodyPaths[pathValue] = ref.StoragePhysicalRefID
				}
			}
			if roots := input.ClassRoots[class]; len(roots) > 0 && !withinAny(pathValue, roots) {
				result.Findings = append(result.Findings, finding("copy_class_mismatch", detail, ref, pathValue, fmt.Sprintf("%s ref is outside its declared roots", class)))
			}
		}
	}
	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].Code != result.Findings[j].Code {
			return result.Findings[i].Code < result.Findings[j].Code
		}
		return result.Findings[i].Path < result.Findings[j].Path
	})
	result.OK = len(result.Findings) == 0
	return result
}
func finding(code string, detail storagecatalog.EntryDetail, ref storagecatalog.PhysicalRef, pathValue, message string) VerifyFinding {
	return VerifyFinding{Code: code, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Path: pathValue, Detail: message}
}
func withinAny(pathValue string, roots []string) bool {
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if !filepath.IsAbs(root) {
			continue
		}
		if _, ok := relativeWithin(pathValue, root); ok && validateManagedPath(root, pathValue, true) == nil {
			return true
		}
	}
	return false
}
