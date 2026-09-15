package objectstore

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	PackageFormat                 = "loom.package.tar.v1"
	PackageMaxContentBytes  int64 = 512 << 20
	PackageMaxProducerBytes int64 = 64 << 20
	PackageMaxFileBytes     int64 = 128 << 20
	PackageMaxArchiveBytes  int64 = 576 << 20
	PackageMaxEntries             = 20000
	PackageMaxPathBytes           = 1024
	PackageMaxDepth               = 64
	packageSchemaKey              = "LOOM.package.schema"
)

type PackageInfo struct {
	Format       string `json:"format"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	EntryCount   int    `json:"entry_count"`
	ContentBytes int64  `json:"content_bytes"`
}

type PackageCaptureOptions struct {
	Selection          []string
	ExcludeDirectories []string
	ExcludeFiles       []string
	MaxContentBytes    int64
	// Deterministic in-package race counterexamples; never exposed to callers.
	beforeCopy    func(string)
	beforeRecheck func()
}

// CapturedPackage owns private staging. Close after hashing/ingesting its archive.
// TreePath contains inert regular files; original executable bits live in tar headers.
type CapturedPackage struct {
	Info        PackageInfo
	TreePath    string
	ArchivePath string
	directory   string
}

func (p *CapturedPackage) Close() error { return os.RemoveAll(p.directory) }

// PackageSelection permits dot only as an explicit singleton whole-tree selector.
func PackageSelection(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > PackageMaxEntries {
		return nil, fmt.Errorf("package selection required")
	}
	out := append([]string(nil), values...)
	if len(out) == 1 && out[0] == "." {
		return out, nil
	}
	for _, v := range out {
		if err := packagePath(v); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	seen := make(map[string]bool, len(out))
	for _, v := range out {
		if seen[v] {
			return nil, fmt.Errorf("overlapping package selection")
		}
		for parent := path.Dir(v); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return nil, fmt.Errorf("overlapping package selection")
			}
		}
		seen[v] = true
	}
	return out, nil
}
func packagePath(p string) error {
	if p == "" || p == "." || !utf8.ValidString(p) || len(p) > PackageMaxPathBytes || strings.Contains(p, "\\") || !filepath.IsLocal(p) || path.Clean(p) != p || strings.Count(p, "/")+1 > PackageMaxDepth {
		return fmt.Errorf("invalid package path")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid package path")
		}
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." || s == "" {
			return fmt.Errorf("invalid package path")
		}
	}
	return nil
}

// OpenPackageRoot resolves only the trusted configured anchor. Every child is
// opened descriptor-relative with kernel no-follow, including directory leaves.
func OpenPackageRoot(anchor, relative string) (*os.File, error) {
	if relative != "." {
		if err := packagePath(filepath.ToSlash(relative)); err != nil {
			return nil, err
		}
	}
	resolved, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(resolved, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), resolved)
	if relative == "." {
		return root, nil
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		next, err := openPackageChild(root, part)
		root.Close()
		if err != nil {
			return nil, err
		}
		st, err := next.Stat()
		if err != nil || !st.IsDir() {
			next.Close()
			return nil, fmt.Errorf("package parent is not a directory")
		}
		root = next
	}
	return root, nil
}

// OpenPackageDirectory refuses user-controlled symlink components in an exact
// registered producer path. Only the OS-selected temporary anchor is resolved.
func OpenPackageDirectory(p string) (*os.File, error) {
	absolute, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	temp := filepath.Clean(os.TempDir())
	if rel, e := filepath.Rel(temp, absolute); e == nil && filepath.IsLocal(rel) {
		return OpenPackageRoot(temp, rel)
	}
	return OpenPackageRoot(string(filepath.Separator), strings.TrimPrefix(absolute, string(filepath.Separator)))
}

func openPackageChild(parent *os.File, name string) (*os.File, error) {
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	kind := uint32(before.Mode) & unix.S_IFMT
	if kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return nil, fmt.Errorf("unsupported package entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	var opened, after unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || unix.Fstatat(int(parent.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Dev != opened.Dev || before.Ino != opened.Ino || before.Mode != opened.Mode || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode {
		f.Close()
		return nil, fmt.Errorf("package entry changed")
	}
	return f, nil
}

type packageEntry struct {
	name string
	info os.FileInfo
}

func samePackageEntry(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func packageMode(info os.FileInfo) int64 {
	if info.IsDir() || info.Mode().Perm()&0111 != 0 {
		return 0755
	}
	return 0644
}
func packageHeader(name string, mode, size int64, dir bool) *tar.Header {
	t := byte(tar.TypeReg)
	if dir {
		t = tar.TypeDir
		name += "/"
		size = 0
	}
	return &tar.Header{Name: name, Mode: mode, Size: size, Typeflag: t, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX, PAXRecords: map[string]string{packageSchemaKey: PackageFormat}}
}

func packageInventory(ctx context.Context, root *os.File, opts PackageCaptureOptions) (map[string]packageEntry, int64, error) {
	entries := map[string]packageEntry{}
	var total int64
	limit := opts.MaxContentBytes
	if limit == 0 {
		limit = PackageMaxContentBytes
	}
	if limit < 0 || limit > PackageMaxContentBytes {
		return nil, 0, fmt.Errorf("invalid package content limit")
	}
	excluded := func(values []string, name string) bool {
		for _, v := range values {
			if v == name {
				return true
			}
		}
		return false
	}
	var walk func(*os.File, string, string, bool) error
	walk = func(parent *os.File, name, relative string, recurse bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := packagePath(relative); err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		dir := uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR
		if dir && excluded(opts.ExcludeDirectories, name) || !dir && excluded(opts.ExcludeFiles, name) {
			return nil
		}
		f, err := openPackageChild(parent, name)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("package privilege bits")
		}
		if !info.IsDir() && (!info.Mode().IsRegular() || info.Size() < 0 || info.Size() > PackageMaxFileBytes) {
			return fmt.Errorf("package file limit or kind")
		}
		if old, ok := entries[relative]; ok {
			if !samePackageEntry(old.info, info) {
				return fmt.Errorf("package changed")
			}
		} else {
			if len(entries) >= PackageMaxEntries {
				return fmt.Errorf("package entry limit")
			}
			entries[relative] = packageEntry{relative, info}
			if !info.IsDir() {
				if info.Size() > limit-total {
					return fmt.Errorf("package content limit")
				}
				total += info.Size()
			}
		}
		if info.IsDir() && recurse {
			for {
				names, e := f.Readdirnames(128)
				if e != nil && e != io.EOF {
					return e
				}
				for _, child := range names {
					if err := walk(f, child, relative+"/"+child, true); err != nil {
						return err
					}
				}
				if e == io.EOF {
					break
				}
			}
			after, e := f.Stat()
			if e != nil || !samePackageEntry(info, after) {
				return fmt.Errorf("package directory changed")
			}
		}
		return nil
	}
	for _, selection := range opts.Selection {
		if selection == "." {
			// An independent open starts directory enumeration at offset zero.
			fd, e := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if e != nil {
				return nil, 0, e
			}
			f := os.NewFile(uintptr(fd), root.Name())
			for {
				names, e := f.Readdirnames(128)
				if e != nil && e != io.EOF {
					f.Close()
					return nil, 0, e
				}
				for _, name := range names {
					if e := walk(f, name, name, true); e != nil {
						f.Close()
						return nil, 0, e
					}
				}
				if e == io.EOF {
					break
				}
			}
			f.Close()
			continue
		}
		parent := root
		var owned []*os.File
		parts := strings.Split(selection, "/")
		var err error
		for i, part := range parts {
			rel := strings.Join(parts[:i+1], "/")
			err = walk(parent, part, rel, i == len(parts)-1)
			if err != nil {
				break
			}
			if i < len(parts)-1 {
				var next *os.File
				next, err = openPackageChild(parent, part)
				if err != nil {
					break
				}
				owned = append(owned, next)
				parent = next
			}
		}
		for _, f := range owned {
			f.Close()
		}
		if err != nil {
			return nil, 0, err
		}
	}
	return entries, total, nil
}

func openPackageSelected(root *os.File, name string, entries map[string]packageEntry) (*os.File, error) {
	parent := root
	var owned []*os.File
	defer func() {
		for _, f := range owned {
			f.Close()
		}
	}()
	parts := strings.Split(name, "/")
	for i, part := range parts {
		f, err := openPackageChild(parent, part)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		want, ok := entries[strings.Join(parts[:i+1], "/")]
		if err != nil || !ok || !samePackageEntry(want.info, info) {
			f.Close()
			return nil, fmt.Errorf("selected package entry changed")
		}
		if i == len(parts)-1 {
			return f, nil
		}
		owned = append(owned, f)
		parent = f
	}
	return nil, fmt.Errorf("package entry required")
}

type packageContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r packageContextReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(b)
}

func CapturePackage(ctx context.Context, root *os.File, stagingParent string, opts PackageCaptureOptions) (_ *CapturedPackage, err error) {
	opts.Selection, err = PackageSelection(opts.Selection)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("package root required")
	}
	initial, err := root.Stat()
	if err != nil || !initial.IsDir() {
		return nil, fmt.Errorf("package root required")
	}
	stageAnchor, err := filepath.EvalSymlinks(stagingParent)
	if err != nil {
		return nil, err
	}
	sourceAnchor, err := filepath.Abs(root.Name())
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(sourceAnchor, stageAnchor)
	if err != nil || filepath.IsLocal(rel) {
		return nil, fmt.Errorf("package staging must be outside source")
	}
	entries, total, err := packageInventory(ctx, root, opts)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(stageAnchor, "retained-package-")
	if err != nil {
		return nil, err
	}
	capture := &CapturedPackage{directory: stage, TreePath: filepath.Join(stage, "tree"), ArchivePath: filepath.Join(stage, "package.tar")}
	defer func() {
		if err != nil {
			capture.Close()
		}
	}()
	if err = os.Mkdir(capture.TreePath, 0700); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := entries[name]
		dest := filepath.Join(capture.TreePath, filepath.FromSlash(name))
		if entry.info.IsDir() {
			if err = os.Mkdir(dest, 0700); err != nil {
				return nil, err
			}
			continue
		}
		if opts.beforeCopy != nil {
			opts.beforeCopy(name)
		}
		f, e := openPackageSelected(root, name, entries)
		if e != nil {
			return nil, e
		}
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			f.Close()
			return nil, e
		}
		n, e := io.Copy(out, io.LimitReader(packageContextReader{ctx, f}, entry.info.Size()+1))
		after, se := f.Stat()
		f.Close()
		closeErr := out.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if se != nil || n != entry.info.Size() || !samePackageEntry(entry.info, after) {
			return nil, fmt.Errorf("package changed during copy")
		}
	}
	if opts.beforeRecheck != nil {
		opts.beforeRecheck()
	}
	current, currentTotal, err := packageInventory(ctx, root, opts)
	if err != nil {
		return nil, err
	}
	final, e := root.Stat()
	if e != nil || !samePackageEntry(initial, final) || currentTotal != total || len(current) != len(entries) {
		return nil, fmt.Errorf("package selection changed")
	}
	for name, entry := range entries {
		now, ok := current[name]
		if !ok || !samePackageEntry(entry.info, now.info) {
			return nil, fmt.Errorf("package selection changed")
		}
	}
	archive, err := os.OpenFile(capture.ArchivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	count := &packageCountWriter{w: io.MultiWriter(archive, hash), limit: PackageMaxArchiveBytes}
	tw := tar.NewWriter(count)
	for _, name := range names {
		entry := entries[name]
		if err = tw.WriteHeader(packageHeader(name, packageMode(entry.info), entry.info.Size(), entry.info.IsDir())); err != nil {
			break
		}
		if !entry.info.IsDir() {
			var f *os.File
			f, err = os.Open(filepath.Join(capture.TreePath, filepath.FromSlash(name)))
			if err == nil {
				_, err = io.Copy(tw, packageContextReader{ctx, f})
				f.Close()
			}
			if err != nil {
				break
			}
		}
	}
	if e := tw.Close(); err == nil {
		err = e
	}
	if e := archive.Sync(); err == nil {
		err = e
	}
	if e := archive.Close(); err == nil {
		err = e
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	capture.Info = PackageInfo{PackageFormat, fmt.Sprintf("sha256:%x", hash.Sum(nil)), count.n, len(entries), total}
	return capture, nil
}

type packageCountWriter struct {
	w        io.Writer
	n, limit int64
}

func (w *packageCountWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.n {
		return 0, fmt.Errorf("package archive limit")
	}
	n, e := w.w.Write(p)
	w.n += int64(n)
	return n, e
}

// InspectPackage consumes every archive byte, then compares it with the canonical
// re-encoding. tar.Reader alone can hide extension headers and trailing archives.
func InspectPackage(ctx context.Context, source io.Reader) (PackageInfo, error) {
	actualHash := sha256.New()
	actual := &packageCountWriter{w: actualHash, limit: PackageMaxArchiveBytes}
	reader := io.TeeReader(packageContextReader{ctx, source}, actual)
	tr := tar.NewReader(reader)
	canonicalHash := sha256.New()
	canonical := &packageCountWriter{w: canonicalHash, limit: PackageMaxArchiveBytes}
	tw := tar.NewWriter(canonical)
	seen := map[string]bool{}
	previous := ""
	var content int64
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return PackageInfo{}, err
		}
		dir := h.Typeflag == tar.TypeDir
		name := h.Name
		if dir {
			name = strings.TrimSuffix(name, "/")
		}
		if err := packagePath(name); err != nil {
			return PackageInfo{}, err
		}
		if name <= previous || count >= PackageMaxEntries || h.Typeflag != tar.TypeReg && !dir || h.Linkname != "" || h.Format != tar.FormatPAX || h.PAXRecords[packageSchemaKey] != PackageFormat || h.Mode != 0644 && h.Mode != 0755 || dir && (h.Mode != 0755 || h.Size != 0 || h.Name != name+"/") || h.Size < 0 || h.Size > PackageMaxFileBytes || h.Size > PackageMaxContentBytes-content {
			return PackageInfo{}, fmt.Errorf("invalid package header")
		}
		for key, value := range h.PAXRecords {
			if key != packageSchemaKey && (key != "path" || value != h.Name) {
				return PackageInfo{}, fmt.Errorf("unsupported package extension")
			}
		}
		if parent := path.Dir(name); parent != "." && !seen[parent] {
			return PackageInfo{}, fmt.Errorf("missing package parent")
		}
		if err := tw.WriteHeader(packageHeader(name, h.Mode, h.Size, dir)); err != nil {
			return PackageInfo{}, err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return PackageInfo{}, err
		}
		seen[name] = dir
		previous = name
		content += h.Size
		count++
	}
	if err := tw.Close(); err != nil {
		return PackageInfo{}, err
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return PackageInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return PackageInfo{}, err
	}
	if actual.n != canonical.n || !strings.EqualFold(fmt.Sprintf("%x", actualHash.Sum(nil)), fmt.Sprintf("%x", canonicalHash.Sum(nil))) {
		return PackageInfo{}, fmt.Errorf("noncanonical package encoding")
	}
	return PackageInfo{PackageFormat, fmt.Sprintf("sha256:%x", actualHash.Sum(nil)), actual.n, count, content}, nil
}
