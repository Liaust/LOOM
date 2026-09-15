package backupstrategy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/hermesprofile"
)

// HermesArchiveBoundary adds prefix exclusions before either LOOM's v1 walker
// or Borg's v2 walker sees the profile. Created skills and SOUL are recovered
// inside the verified native ZIP, never by enumerating the live profile.
func HermesArchiveBoundary(ctx context.Context, roots []DirectArchiveRoot, exclusions []DirectArchiveExclusion, policy hermesprofile.Policy, now time.Time) ([]DirectArchiveExclusion, []hermesprofile.Evidence, error) {
	policy, _, err := policy.Resolve()
	if err != nil {
		return nil, nil, err
	}
	workspaces := []string{policy.Workspace}
	if policy.Identity == hermesprofile.MinaIdentity {
		workspaces = append(workspaces, hermesprofile.WorkspaceRoot)
	}
	result := append([]DirectArchiveExclusion(nil), exclusions...)
	found := false
	for _, workspace := range workspaces {
		for _, root := range roots {
			rel, err := filepath.Rel(root.Path, workspace)
			if err != nil {
				return nil, nil, err
			}
			if rel == ".." || strings.HasPrefix(rel, "../") {
				// A root nested inside the native profile cannot be made safe with a
				// child-prefix exclusion: refuse it, including when the gateway is off.
				for _, reserved := range []string{".hermes", "recovery"} {
					nested, e := filepath.Rel(filepath.Join(workspace, reserved), root.Path)
					if e == nil && nested != ".." && !strings.HasPrefix(nested, "../") {
						return nil, nil, fmt.Errorf("Hermes internal path is not a cloud source root")
					}
				}
				continue
			}
			if workspace == policy.Workspace {
				found = true
			}
			paths := []string{filepath.ToSlash(filepath.Join(rel, ".hermes"))}
			if policy.Enabled && workspace == policy.Workspace {
				paths = append(paths, filepath.ToSlash(filepath.Join(rel, "recovery", ".staging")))
			} else {
				paths = append(paths, filepath.ToSlash(filepath.Join(rel, "recovery")))
			}
			for _, p := range paths {
				present := false
				for _, e := range result {
					if e.Root != root.Name {
						continue
					}
					if e.RelativePath == p {
						present = true
						continue
					}
					if strings.HasPrefix(p, e.RelativePath+"/") || strings.HasPrefix(e.RelativePath, p+"/") {
						return nil, nil, fmt.Errorf("Hermes cloud exclusion overlaps another policy")
					}
				}
				if !present {
					result = append(result, DirectArchiveExclusion{root.Name, p})
				}
			}
		}
	}
	if policy.Enabled && !found {
		return nil, nil, fmt.Errorf("selected Hermes workspace is absent from cloud roots")
	}
	evidence, err := hermesprofile.Check(ctx, policy, now)
	if err != nil {
		return nil, nil, err
	}
	return result, evidence, nil
}

func validateHermesEvidence(evidence []hermesprofile.Evidence, roots []DirectArchiveRoot, exclusions []DirectArchiveExclusion) error {
	if len(evidence) > 8192 {
		return fmt.Errorf("Hermes recovery set is too large")
	}
	last := ""
	for _, pkg := range evidence {
		if pkg.Path <= last || !filepath.IsAbs(pkg.Path) || filepath.Clean(pkg.Path) != pkg.Path || filepath.Base(pkg.Path) != pkg.ID || filepath.Base(filepath.Dir(pkg.Path)) != "recovery" || validateArchiveIdentity(pkg.ID) != nil || pkg.CreatedAt.IsZero() || pkg.CreatedAt.Location() != time.UTC || len(pkg.Files) != 2 {
			return fmt.Errorf("invalid Hermes recovery identity")
		}
		last = pkg.Path
		covered := false
		for _, root := range roots {
			rel, err := filepath.Rel(root.Path, pkg.Path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				continue
			}
			covered = true
			for _, ex := range exclusions {
				if ex.Root == root.Name && (rel == ex.RelativePath || strings.HasPrefix(rel, ex.RelativePath+"/")) {
					return fmt.Errorf("Hermes recovery payload is excluded")
				}
			}
		}
		if !covered {
			return fmt.Errorf("Hermes recovery is outside declared roots")
		}
		for i, file := range pkg.Files {
			want := hermesprofile.ManifestFile
			if i == 1 {
				want = hermesprofile.PayloadFile
			}
			if file.Path != want || file.Size <= 0 || file.Size > 8<<30 || file.Mode != 0440 || !validHermesHash(file.SHA256) {
				return fmt.Errorf("invalid Hermes recovery inventory")
			}
		}
		if pkg.ManifestSHA256 != pkg.Files[0].SHA256 {
			return fmt.Errorf("Hermes manifest identity mismatch")
		}
	}
	return nil
}
func validHermesHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}
