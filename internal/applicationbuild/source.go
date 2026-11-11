package applicationbuild

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type SourceFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

type Snapshot struct {
	Program        *Program     `json:"program,omitempty"`
	ProducerDigest string       `json:"producer_digest,omitempty"`
	SourceDigest   string       `json:"source_digest"`
	PolicyDigest   string       `json:"policy_digest"`
	Files          []SourceFile `json:"files"`
}

// Snapshot only reads runtime code and its ancestor .loomignore files. It never
// creates staging directories, writes metadata or invokes a command.
func (s *Service) Snapshot(ctx context.Context, input Input) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if !legacyInput(input) {
		return s.captureProgram(ctx, input, nil)
	}
	if err := validateInput(input, s.options); err != nil {
		return Snapshot{}, err
	}
	return captureSource(ctx, input, s.options, nil)
}

func freezeSource(ctx context.Context, stage string, input Input, options Options) ([]SourceFile, string, error) {
	root := filepath.Join(stage, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, "", errors.New("application build: staging unavailable")
	}
	snapshot, err := captureSource(ctx, input, options, func(name string, raw []byte) error {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return errors.New("application build: staging unavailable")
		}
		if err := os.WriteFile(target, raw, 0444); err != nil {
			return errors.New("application build: staging unavailable")
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return snapshot.Files, snapshot.SourceDigest, nil
}

func captureSource(ctx context.Context, input Input, options Options, sink func(string, []byte) error) (Snapshot, error) {
	root, err := os.OpenRoot(input.SourceRoot)
	if err != nil {
		return Snapshot{}, ErrUnsafeSource
	}
	defer root.Close()
	names := append([]string{}, input.SourceFiles...)
	if len(names) == 0 {
		names, err = discoverRuntime(ctx, root, input.PythonModule, options.MaxSourceFiles)
		if err != nil {
			return Snapshot{}, err
		}
	}
	names = append(names, input.PackageFiles...)
	if len(names) > options.MaxSourceFiles {
		return Snapshot{}, ErrUnsafeSource
	}
	policies, err := sourcePolicies(ctx, root, names)
	if err != nil {
		return Snapshot{}, err
	}
	sort.Strings(names)
	snapshot := Snapshot{ProducerDigest: producerDigest(options), PolicyDigest: hashJSON(policies.files), Files: []SourceFile{}}
	var total int64
	for _, name := range names {
		if !policies.includes(name) {
			if len(input.SourceFiles) != 0 || slices.Contains(input.PackageFiles, name) {
				return Snapshot{}, ErrUnsafeSource
			}
			continue
		}
		raw, err := readSourceFile(ctx, root, name, options.MaxSourceBytes-total)
		if err != nil {
			return Snapshot{}, err
		}
		total += int64(len(raw))
		snapshot.Files = append(snapshot.Files, SourceFile{Path: name, Digest: hashBytes(raw), Bytes: int64(len(raw))})
		if sink != nil {
			if err := sink(name, raw); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	eligible := make([]string, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		eligible = append(eligible, file.Path)
	}
	if !hasEntrypoint(input.PythonModule, eligible) {
		return Snapshot{}, ErrInvalidInput
	}
	for _, action := range input.Actions {
		if !hasEntrypoint(action.PythonModule, eligible) {
			return Snapshot{}, ErrInvalidInput
		}
	}
	snapshot.SourceDigest = hashJSON(struct {
		Policy string
		Files  []SourceFile
	}{snapshot.PolicyDigest, snapshot.Files})
	return snapshot, nil
}

func safeRuntimePath(name string) bool {
	return strings.HasSuffix(name, ".py") && safePackageSourcePath(name)
}

func safePackagePath(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "credential") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
		return false
	}
	return slices.Contains([]string{".json", ".yaml", ".yml", ".toml", ".txt", ".csv"}, path.Ext(name)) && safePackageSourcePath(name)
}

func safePackageSourcePath(name string) bool {
	if len(name) > 512 || path.Clean(name) != name || path.IsAbs(name) || strings.ContainsAny(name, "\\\x00\r\n\t ") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 32 {
		return false
	}
	for _, component := range parts {
		if component == "" || strings.HasPrefix(component, ".") {
			return false
		}
		switch strings.ToLower(component) {
		case "data", "secrets", "secret", "credentials", "private", "notes", "mail", "raw-mail", "raw_mail", "profiles", "archive", "archives", "cache", "caches", "__pycache__", "node_modules", "venv", "build", "dist", "tests", "test", "docs":
			return false
		}
		for _, c := range component {
			if c != '.' && c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return !strings.HasPrefix(strings.ToLower(path.Base(name)), "test_")
}

// Pin every parent directory and reject symlinks/special files before reading.
// O_NONBLOCK prevents a racing FIFO replacement from trapping a cancelled job.
func readSourceFile(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := root
	parts := strings.Split(name, "/")
	for _, component := range parts[:len(parts)-1] {
		info, err := current.Lstat(component)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnsafeSource
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, ErrUnsafeSource
		}
		defer next.Close()
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return nil, ErrSourceChanged
		}
		current = next
	}
	leaf := parts[len(parts)-1]
	before, err := current.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || limit < 0 || before.Size() > limit {
		return nil, ErrUnsafeSource
	}
	file, err := current.OpenFile(leaf, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafeSource
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, ErrSourceChanged
	}
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	leafAfter, leafErr := current.Lstat(leaf)
	if err != nil || leafErr != nil || !os.SameFile(before, leafAfter) || leafAfter.Mode()&os.ModeSymlink != 0 || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() || int64(len(raw)) != before.Size() {
		return nil, ErrSourceChanged
	}
	if int64(len(raw)) > limit {
		return nil, ErrUnsafeSource
	}
	return raw, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
