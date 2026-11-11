package applicationbuild

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Native capture shares no-follow reads, ignore semantics and source digests
// with Python; only discovery/package rules differ. No command runs here.
func nativeSourcePath(name string) bool {
	if path.IsAbs(name) || path.Clean(name) != name || len(name) > 512 || strings.ContainsAny(name, "\\\x00") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 32 {
		return false
	}
	for i, part := range parts {
		if strings.HasPrefix(part, ".") && !(i == 0 && part == ".cargo" && len(parts) == 2 && parts[1] == "config.toml") {
			return false
		}
		switch strings.ToLower(part) {
		case "target", "node_modules", "vendor", "__pycache__", "venv", "dist", "cache", "caches", "profiles", "notes", "mail", "raw-mail", "data", "private", "secrets", "credentials", "archive", "archives":
			return false
		}
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".db", ".sqlite", ".sqlite3", ".pem", ".key", ".p12", ".pfx", ".pdf", ".zip", ".tar", ".mp4"} {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}
	if path.Ext(lower) != ".rs" && (strings.Contains(lower, "credential") || strings.Contains(lower, "secret") || strings.Contains(lower, "token")) {
		return false
	}
	return true
}

func discoverNative(ctx context.Context, root *os.Root, options Options) ([]string, error) {
	var names []string
	entries := 0
	var walk func(string) error
	walk = func(dir string) error {
		listing, err := root.Open(dir)
		if err != nil {
			return err
		}
		defer listing.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, err := listing.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range batch {
				entries++
				if entries > options.MaxSourceFiles*8+128 {
					return ErrUnsafeSource
				}
				name := path.Join(dir, entry.Name())
				if entry.Name() == ".cargo" && dir == "." {
					if info, e := root.Lstat(".cargo"); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
						return ErrUnsafeSource
					}
					if _, e := root.Lstat(".cargo/config"); e == nil {
						return unsupported("cargo_config_unsupported", "rename legacy .cargo/config to config.toml for explicit validation")
					}
					if _, e := root.Lstat(".cargo/config.toml"); e == nil {
						names = append(names, ".cargo/config.toml")
					}
					continue
				}
				if !nativeSourcePath(name) {
					continue
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return ErrUnsafeSource
				}
				if entry.IsDir() {
					policies, e := sourcePolicies(ctx, root, []string{path.Join(name, "probe")})
					if e != nil {
						return e
					}
					if !policies.includes(path.Join(name, "probe")) {
						continue
					}
					// OpenRoot pins and rejects a symlink replacement in each ancestor.
					child, e := root.OpenRoot(name)
					if e != nil {
						return ErrUnsafeSource
					}
					child.Close()
					if e := walk(name); e != nil {
						return e
					}
				} else {
					if !entry.Type().IsRegular() {
						return ErrUnsafeSource
					}
					names = append(names, name)
					if len(names) > options.MaxSourceFiles {
						return ErrUnsafeSource
					}
				}
			}
			if err == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (s *Service) captureProgram(ctx context.Context, in Input, sink func(string, []byte) error) (Snapshot, error) {
	if !projectPattern.MatchString(in.Source.ProjectID) || !keyPattern.MatchString(in.Source.Resource) || (in.Source.RepositoryID != "" && !repositoryPattern.MatchString(in.Source.RepositoryID)) || (in.Source.Revision != "" && !revisionPattern.MatchString(in.Source.Revision)) || !filepath.IsAbs(in.SourceRoot) || (in.ExpectedSourceDigest != "" && !digestPattern.MatchString(in.ExpectedSourceDigest)) || len(in.SourceFiles) > s.options.MaxSourceFiles || len(in.PackageFiles) > 64 || len(in.CompatibleDataFormats) > 32 || (in.DataFormat != "" && !formatPattern.MatchString(in.DataFormat)) {
		return Snapshot{}, ErrInvalidInput
	}
	for _, format := range in.CompatibleDataFormats {
		if !formatPattern.MatchString(format) {
			return Snapshot{}, ErrInvalidInput
		}
	}
	if _, _, _, _, err := configurationMetadata(in); err != nil {
		return Snapshot{}, err
	}
	info, err := os.Lstat(in.SourceRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, ErrUnsafeSource
	}
	p, err := normalizeProgram(in)
	if err != nil {
		return Snapshot{}, err
	}
	if p.Build.Kind == "python" {
		return s.capturePythonProgram(ctx, in, p, sink)
	}
	if len(in.SourceFiles) != 0 {
		return Snapshot{}, ErrInvalidInput
	}
	root, err := os.OpenRoot(in.SourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	defer root.Close()
	names, err := discoverNative(ctx, root, s.options)
	if err != nil {
		return Snapshot{}, err
	}
	policies, err := sourcePolicies(ctx, root, names)
	if err != nil {
		return Snapshot{}, err
	}
	files := map[string][]byte{}
	result := Snapshot{PolicyDigest: hashJSON(policies.files), Files: []SourceFile{}}
	var total int64
	for _, name := range names {
		if !policies.includes(name) {
			continue
		}
		raw, err := readSourceFile(ctx, root, name, s.options.MaxSourceBytes-total)
		if err != nil {
			return Snapshot{}, err
		}
		total += int64(len(raw))
		files[name] = raw
		result.Files = append(result.Files, SourceFile{Path: name, Digest: hashBytes(raw), Bytes: int64(len(raw))})
	}
	for _, name := range in.PackageFiles {
		if !nativeSourcePath(name) || files[name] == nil {
			return Snapshot{}, unsupported("required_source_unavailable", name)
		}
	}
	if p.Build.Kind == "" {
		_, cargo := files["Cargo.toml"]
		_, nix := files["flake.nix"]
		if cargo == nix {
			return Snapshot{}, unsupported("select_build_kind", "select source.build.kind: cargo or nix")
		}
		if cargo {
			p.Build.Kind = "cargo"
		} else {
			p.Build.Kind = "nix"
		}
	}
	switch p.Build.Kind {
	case "cargo":
		err = inspectCargo(&p, files, s.options)
	case "nix":
		err = inspectNix(&p, files, s.options)
	default:
		return Snapshot{}, ErrInvalidInput
	}
	if err != nil {
		return Snapshot{}, err
	}
	result.Program = &p
	result.ProducerDigest = nativeProducerDigest(s.options, p.Build.Kind)
	result.SourceDigest = hashJSON(struct {
		Policy string
		Files  []SourceFile
	}{result.PolicyDigest, result.Files})
	if in.ExpectedSourceDigest != "" && in.ExpectedSourceDigest != result.SourceDigest {
		return Snapshot{}, ErrSourceChanged
	}
	if sink != nil {
		for _, policy := range policies.files {
			raw, err := readSourceFile(ctx, root, policy.Path, 1024*1024)
			if err != nil {
				return Snapshot{}, err
			}
			if hashBytes(raw) != policy.Digest {
				return Snapshot{}, ErrSourceChanged
			}
			if err := sink(policy.Path, raw); err != nil {
				return Snapshot{}, err
			}
		}
		for _, file := range result.Files {
			if err := sink(file.Path, files[file.Path]); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

func selectedPrograms(p Program) []string {
	names := []string{p.Run.Binary}
	for _, a := range p.Actions {
		if a.Binary != "" {
			names = append(names, a.Binary)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
