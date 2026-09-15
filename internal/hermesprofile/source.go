package hermesprofile

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// This inventory mirrors the exclusions of the accepted 0.21.0 command. A
// different native inventory is a hard failure, never a partial success.
var excludedDirs = map[string]bool{"hermes-agent": true, "__pycache__": true, ".git": true, "node_modules": true, "backups": true, "state-snapshots": true, "checkpoints": true, "browser-profiles": true, "browser-profile": true, ".venv": true, "venv": true, "site-packages": true, ".cache": true, ".tox": true, ".nox": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true}

func excluded(name string) bool {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		if excludedDirs[p] && (p != "hermes-agent" || i == 0) {
			return true
		}
	}
	b := path.Base(name)
	if b == ".backup.lock" || b == "gateway.pid" || b == "cron.pid" {
		return true
	}
	for _, s := range []string{".pyc", ".pyo", ".db-wal", ".db-shm", ".db-journal"} {
		if strings.HasSuffix(b, s) {
			return true
		}
	}
	return false
}
func portable(name string) bool {
	return name != "" && path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.ContainsAny(name, "\\:\x00\r\n")
}

func runtimeSocketMode(name string) (uint32, bool) {
	if name == "gateway.sock" {
		return 0600, true
	}
	const prefix = "state/gateway.loop-tick."
	const suffix = ".sock"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, false
	}
	pid := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if pid == "" || len(pid) > 10 || pid[0] == '0' {
		return 0, false
	}
	value, err := strconv.ParseUint(pid, 10, 31)
	return 0700, err == nil && value > 0
}

func safeRuntimeSocket(info Identity, owner, mode uint32) bool {
	return info.Mode&unix.S_IFMT == unix.S_IFSOCK && info.Mode&07777 == mode && info.Owner == owner && info.Links == 1 && info.Size == 0
}

type sourceFile struct {
	parent  *os.File
	name    string
	initial Identity
	fd      *os.File
	hash    string
	db      bool
	log     bool
}
type sourceTree struct {
	files   map[string]sourceFile
	dirs    []sourceDir
	monitor sourceMonitor
	static  []sourceFile
	count   int
	bytes   int64
	frozen  bool
}

type sourceDir struct {
	heldDir
	prefix  string
	entries map[string]Identity // Includes static exclusions; never blindly discard them.
}

func sidecarBase(name string) string {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if strings.HasSuffix(name, ".db"+suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return ""
}

func scanSource(ctx context.Context, root *os.File) (*sourceTree, error) {
	return scanSourceMode(ctx, root, false)
}

func scanCapturedSource(ctx context.Context, root *os.File) (*sourceTree, error) {
	return scanSourceMode(ctx, root, true)
}

func scanSourceMode(ctx context.Context, root *os.File, frozen bool) (*sourceTree, error) {
	initial, err := statFD(root)
	if err != nil {
		return nil, err
	}
	monitor, err := newSourceMonitor()
	if err != nil {
		return nil, err
	}
	s := &sourceTree{files: map[string]sourceFile{}, monitor: monitor, frozen: frozen}
	if err := s.walk(ctx, root, nil, "", "", initial); err != nil {
		s.close()
		return nil, err
	}
	for _, required := range []string{"SOUL.md", "config.yaml", "state.db"} {
		if _, ok := s.files[required]; !ok {
			s.close()
			return nil, fmt.Errorf("required Hermes profile entry missing")
		}
	}
	cfg := s.files["config.yaml"].fd
	if _, err := cfg.Seek(0, 0); err != nil {
		s.close()
		return nil, err
	}
	raw, err := readBounded(cfg, 1<<20)
	if err != nil {
		s.close()
		return nil, err
	}
	var settings struct {
		Memory struct {
			Provider string `yaml:"provider"`
		} `yaml:"memory"`
	}
	if yaml.Unmarshal(raw, &settings) != nil || settings.Memory.Provider != "" && settings.Memory.Provider != "builtin" {
		s.close()
		return nil, fmt.Errorf("external Hermes memory providers are outside this recovery boundary")
	}
	if err := s.revalidate(ctx); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *sourceTree) walk(ctx context.Context, dir, parent *os.File, name, prefix string, start Identity) error {
	var err error
	d := sourceDir{heldDir: heldDir{file: dir, parent: parent, name: name, initial: start}, prefix: prefix, entries: map[string]Identity{}}
	s.dirs = append(s.dirs, d)
	if err = s.monitor.add(dir, start, sourceWatch{dir: dir, kind: watchDirectory}); err != nil {
		return err
	}
	ns, err := names(dir)
	if err != nil {
		return err
	}
	sort.Strings(ns)
	for _, n := range ns {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.count++
		if s.count > maxEntries {
			return fmt.Errorf("profile entry limit exceeded")
		}
		rel := path.Join(prefix, n)
		if sidecarBase(strings.ToLower(n)) != "" && sidecarBase(n) == "" {
			return fmt.Errorf("noncanonical SQLite sidecar name")
		}
		if !portable(rel) || strings.HasPrefix(rel, "_external/") || n == "profiles" {
			return fmt.Errorf("unsupported profile path")
		}
		info, err := statAt(dir, n)
		if err != nil {
			return err
		}
		if mode, ok := runtimeSocketMode(rel); ok {
			if !safeRuntimeSocket(info, start.Owner, mode) {
				return fmt.Errorf("invalid Hermes runtime socket")
			}
			d.entries[n] = info
			continue
		}
		if !directory(info) && !regular(info) {
			return fmt.Errorf("profile contains a link or special entry")
		}
		if regular(info) && info.Mode&07000 != 0 {
			return fmt.Errorf("profile file has special permission bits")
		}
		d.entries[n] = info
		if sidecarBase(n) != "" {
			f, e := openAt(dir, n, false)
			if e != nil {
				return e
			}
			s.static = append(s.static, sourceFile{fd: f}) // Lifetime only; endpoint validates sidecars.
			if e = s.monitor.add(f, info, sourceWatch{kind: watchSidecar}); e != nil {
				return e
			}
			continue
		}
		if excluded(rel) {
			// Hold excluded objects too: their names and metadata are not an exception
			// to custody, even though their bytes never enter the archive.
			f, e := openAt(dir, n, directory(info))
			if e != nil {
				return e
			}
			sf := sourceFile{parent: dir, name: n, initial: info, fd: f}
			s.static = append(s.static, sf)
			if e = s.monitor.add(f, info, sourceWatch{kind: watchStatic}); e != nil {
				return e
			}
			continue
		}
		if directory(info) {
			f, err := openAt(dir, n, true)
			if err != nil {
				return err
			}
			held, e := statFD(f)
			if e != nil || held != info {
				f.Close()
				return fmt.Errorf("profile directory substituted while opening")
			}
			if err = s.walk(ctx, f, dir, n, rel, info); err != nil {
				return err
			}
			continue
		}
		sf := sourceFile{parent: dir, name: n, initial: info, db: strings.HasSuffix(n, ".db"), log: strings.HasPrefix(rel, "logs/")}
		// The historical live guard holds DB inodes without reading their bytes.
		// Frozen mode may hash the private SQLite snapshot, never the live DB.
		if sf.log && !s.frozen {
			sf.db = false
		}
		if sf.db && s.frozen {
			sf.log = false
		}
		sf.fd, err = openAt(dir, n, false)
		if err != nil {
			return err
		}
		kind := watchOrdinary
		if sf.db && !s.frozen {
			kind = watchDatabase
		} else if sf.log && !s.frozen {
			kind = watchLog
		}
		if err = s.monitor.add(sf.fd, info, sourceWatch{kind: kind}); err != nil {
			sf.fd.Close()
			return err
		}
		if s.frozen || !sf.db && !sf.log {
			sf.hash, _, err = hashFile(ctx, sf.fd)
			if err != nil {
				sf.fd.Close()
				return err
			}
		}
		held, e := statFD(sf.fd)
		if e != nil || !sameObject(held, info) || ((s.frozen || !sf.db && !sf.log) && held != info) {
			sf.fd.Close()
			return fmt.Errorf("profile substituted while opening")
		}
		s.bytes += held.Size
		if held.Size < 0 || held.Size > maxBytes || s.bytes > maxBytes {
			sf.fd.Close()
			return fmt.Errorf("profile byte limit exceeded")
		}
		s.files[rel] = sf
	}
	end, err := statFD(dir)
	if err != nil || end != start {
		return fmt.Errorf("profile changed during inventory")
	}
	return nil
}
func (s *sourceTree) allowedSidecar(dir *os.File, n string) (sourceFile, bool) {
	base := sidecarBase(n)
	if base == "" {
		return sourceFile{}, false
	}
	for _, f := range s.files {
		if f.parent == dir && f.name == base && f.db {
			return f, true
		}
	}
	return sourceFile{}, false
}
func safeSidecar(info Identity, base sourceFile) bool {
	return regular(info) && info.Mode&07000 == 0 && info.Owner == base.initial.Owner && info.Size >= 0 && info.Size <= maxBytes
}
func (s *sourceTree) namespace(ctx context.Context, changed map[*os.File]uint64) error {
	count, total := 0, int64(0)
	for _, d := range s.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		now, err := statFD(d.file)
		if err != nil || !sameObject(now, d.initial) {
			return fmt.Errorf("profile directory substituted")
		}
		if d.parent != nil {
			named, e := statAt(d.parent, d.name)
			if e != nil || named != now {
				return fmt.Errorf("profile directory pathname substituted")
			}
		}
		hasDB := false
		for _, f := range s.files {
			if f.parent == d.file && f.db {
				hasDB = true
			}
		}
		// Vnode write events have no names on Darwin. With a declared online DB,
		// only exact sidecar differences may survive the full endpoint comparison.
		// Original-object ABA is independently rejected by inode watches.
		if now != d.initial && (!hasDB || changed[d.file] == 0) {
			return fmt.Errorf("profile directory changed")
		}
		ns, err := names(d.file)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, n := range ns {
			count++
			if count > maxEntries {
				return fmt.Errorf("profile entry limit exceeded")
			}
			rel := path.Join(d.prefix, n)
			if sidecarBase(strings.ToLower(n)) != "" && sidecarBase(n) == "" {
				return fmt.Errorf("noncanonical SQLite sidecar name")
			}
			if !portable(rel) || strings.HasPrefix(rel, "_external/") || n == "profiles" {
				return fmt.Errorf("unsupported profile path")
			}
			info, e := statAt(d.file, n)
			if e != nil {
				return e
			}
			seen[n] = true
			if mode, ok := runtimeSocketMode(rel); ok {
				old, present := d.entries[n]
				if !present || info != old || !safeRuntimeSocket(info, d.initial.Owner, mode) {
					return fmt.Errorf("Hermes runtime socket substituted")
				}
			} else if sidecarBase(n) != "" {
				base, ok := s.allowedSidecar(d.file, n)
				if !ok || !safeSidecar(info, base) {
					return fmt.Errorf("unsafe or undeclared SQLite sidecar")
				}
			} else {
				old, ok := d.entries[n]
				if !ok || !sameObject(info, old) {
					return fmt.Errorf("profile namespace substituted")
				}
				if excluded(rel) && info != old {
					return fmt.Errorf("static exclusion changed")
				}
			}
			if regular(info) {
				total += info.Size
				if info.Size < 0 || info.Size > maxBytes || total > maxBytes {
					return fmt.Errorf("profile byte limit exceeded")
				}
			}
		}
		for n, old := range d.entries {
			rel := path.Join(d.prefix, n)
			if mode, ok := runtimeSocketMode(rel); ok {
				if !seen[n] || !safeRuntimeSocket(old, d.initial.Owner, mode) {
					return fmt.Errorf("Hermes runtime socket removed")
				}
			} else if sidecarBase(n) != "" {
				base, ok := s.allowedSidecar(d.file, n)
				if !ok || !safeSidecar(old, base) {
					return fmt.Errorf("unsafe initial SQLite sidecar")
				}
			} else if !seen[n] {
				return fmt.Errorf("profile source removed")
			}
		}
		end, e := statFD(d.file)
		if e != nil || end != now {
			return fmt.Errorf("profile namespace changed during revalidation")
		}
	}
	return nil
}
func (s *sourceTree) revalidate(ctx context.Context) error {
	changed, err := s.monitor.check(s)
	if err != nil {
		return err
	}
	if err = s.namespace(ctx, changed); err != nil {
		return err
	}
	var total int64
	for _, f := range s.files {
		named, err := statAt(f.parent, f.name)
		if err != nil || !sameObject(named, f.initial) {
			return fmt.Errorf("profile source substituted")
		}
		// SQLite owns database contents and WAL activity. Only its pathname/object
		// identity is inspected here; ordinary file bytes must be unchanged.
		total += named.Size
		if named.Size < 0 || named.Size > maxBytes || total > maxBytes {
			return fmt.Errorf("profile byte limit exceeded")
		}
		if (f.db || f.log) && !s.frozen {
			held, e := statFD(f.fd)
			if e != nil || !sameObject(held, named) {
				return fmt.Errorf("profile log substituted")
			}
		} else {
			if named != f.initial {
				return fmt.Errorf("profile source mutated")
			}
			digest, _, err := hashFile(ctx, f.fd)
			if err != nil || digest != f.hash {
				return fmt.Errorf("profile source mutated")
			}
		}
	}
	after, err := s.monitor.check(s)
	if err != nil {
		return err
	}
	if !sameSourceChanges(changed, after) {
		return fmt.Errorf("profile namespace changed during source revalidation")
	}
	return nil
}
func (s *sourceTree) close() {
	if s.monitor != nil {
		s.monitor.close()
	}
	for _, f := range s.static {
		f.fd.Close()
	}
	for _, f := range s.files {
		if f.fd != nil {
			f.fd.Close()
		}
	}
	for i := len(s.dirs) - 1; i > 0; i-- {
		s.dirs[i].file.Close()
	}
}

func (s *sourceTree) filesFor(dir *os.File, name string) (sourceFile, bool) {
	for _, f := range s.files {
		if f.parent == dir && f.name == name {
			return f, true
		}
	}
	return sourceFile{}, false
}
