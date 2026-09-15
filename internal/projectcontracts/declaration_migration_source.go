package projectcontracts

import (
	"errors"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// DeclarationMigrationSources owns an immutable, private metadata snapshot and
// its open root. Check detects observed drift; it is not a publication fence.
type DeclarationMigrationSources struct {
	root     *os.Root
	location string
	input    DeclarationMigrationInput
	observed map[string]os.FileInfo
	listings map[string][]string
	issues   []MigrationAssessmentIssue
	total    int
	handles  map[string]*os.File
}

func CollectDeclarationMigrationSources(location string) (*DeclarationMigrationSources, error) {
	// Pin a directory before constructing os.Root. On Unix Go's OpenRoot first
	// opens the pathname without O_DIRECTORY; a raced FIFO must not block here.
	directory, err := os.OpenFile(location, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("source_root_unavailable")
	}
	// /dev/fd addresses our held descriptor on the supported Linux/macOS hosts.
	// If descriptor access is unavailable, refuse rather than reopening the path.
	root, err := os.OpenRoot("/dev/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10))
	directory.Close()
	if err != nil {
		return nil, errors.New("source_root_unavailable")
	}
	s := &DeclarationMigrationSources{root: root, location: location, observed: map[string]os.FileInfo{}, listings: map[string][]string{}, input: DeclarationMigrationInput{Sources: []MigrationSource{}, Collections: []MigrationCollection{}}, issues: []MigrationAssessmentIssue{}, handles: map[string]*os.File{}}
	s.observe(".")
	s.collect(CanonicalRootContractPath)
	s.collect(LegacyRootContractPath)
	c, _, ok := s.ProjectSource()
	if ok {
		for _, kind := range []string{ProjectContractNotes, ProjectContractRepos, ProjectContractSync, ProjectContractBackup, ProjectContractWorkers, ProjectContractCredentials} {
			def := singletonContractDefinitions[kind]
			explicit := ""
			switch kind {
			case ProjectContractSync:
				explicit = c.Policies.Sync
			case ProjectContractBackup:
				explicit = c.Policies.Backup
			case ProjectContractWorkers:
				explicit = c.Policies.Workers
			case ProjectContractCredentials:
				explicit = c.Policies.Credentials
			}
			if !c.Facets[def.Facet] && explicit == "" {
				continue
			}
			s.collect(def.Canonical)
			s.collect(def.Legacy)
			if explicit != "" {
				s.collect(explicit)
			}
		}
		for _, kind := range []string{"scripts", "workflows", "connectors", "modules", "schedules", "direct_events", "services"} {
			if c.Facets[kind] {
				s.collection(kind)
			}
		}
	}
	sort.Slice(s.input.Sources, func(i, j int) bool { return s.input.Sources[i].Ref < s.input.Sources[j].Ref })
	return s, nil
}

func (s *DeclarationMigrationSources) Close() error {
	var result error
	for _, f := range s.handles {
		result = errors.Join(result, f.Close())
	}
	s.handles = nil
	return errors.Join(result, s.root.Close())
}
func (s *DeclarationMigrationSources) Input() DeclarationMigrationInput {
	in := DeclarationMigrationInput{Sources: []MigrationSource{}, Collections: []MigrationCollection{}}
	for _, v := range s.input.Sources {
		v.Raw = append([]byte{}, v.Raw...)
		in.Sources = append(in.Sources, v)
	}
	for _, v := range s.input.Collections {
		v.Entries = append([]string{}, v.Entries...)
		in.Collections = append(in.Collections, v)
	}
	return in
}
func (s *DeclarationMigrationSources) Issues() []MigrationAssessmentIssue {
	return append([]MigrationAssessmentIssue{}, s.issues...)
}
func (s *DeclarationMigrationSources) Fingerprint() string {
	return DeclarationMigrationSourceFingerprint(s.input)
}
func DeclarationMigrationSourceFingerprint(in DeclarationMigrationInput) string {
	sources, collections := migrationSourceBasis(in)
	return migrationDigest(struct {
		Sources     []MigrationSourceClaim `json:"sources"`
		Collections []MigrationCollection  `json:"collections"`
	}{sources, collections})
}

// ProjectSource uses the exact existing D4a selection/normalization rules.
func (s *DeclarationMigrationSources) ProjectSource() (ProjectContract, MigrationSourceClaim, bool) {
	p := migrationPreview{in: s.input, sources: map[string]MigrationSource{}, trees: map[string]*yaml.Node{}, selected: map[string]string{}, singletons: map[string]string{}}
	p.inventory()
	ok := p.loadRoot()
	return p.contract, p.sources[p.rootRef].MigrationSourceClaim, ok
}

func (s *DeclarationMigrationSources) AlreadyDeclaration() bool {
	var document *ProjectDeclaration
	found := false
	for _, source := range s.input.Sources {
		if source.Ref != CanonicalRootContractPath && source.Ref != LegacyRootContractPath {
			continue
		}
		if source.State == "absent" {
			continue
		}
		if source.State != "present" || source.SchemaVersion != ProjectSchemaV05 {
			return false
		}
		d, err := ParseProjectDeclaration(source.Raw)
		if err != nil {
			return false
		}
		if found && !reflect.DeepEqual(*document, d) {
			return false
		}
		document = &d
		found = true
	}
	return found
}

func (s *DeclarationMigrationSources) issue(code, ref string) {
	if len(s.issues) < 32 {
		if !migrationRef(ref) {
			ref = ""
		}
		s.issues = append(s.issues, MigrationAssessmentIssue{Family: "sources", Code: code, Ref: ref})
	}
}
func (s *DeclarationMigrationSources) observe(ref string) (os.FileInfo, error) {
	if ref != "." {
		if _, err := s.observeParent(path.Dir(ref)); err != nil {
			return nil, err
		}
	}
	info, err := s.root.Lstat(ref)
	if os.IsNotExist(err) {
		s.observed[ref] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.observed[ref] = info
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symlink")
	}
	return info, nil
}
func (s *DeclarationMigrationSources) observeParent(ref string) (os.FileInfo, error) {
	if info, ok := s.observed[ref]; ok {
		if info != nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return nil, errors.New("unsafe_parent")
		}
		return info, nil
	}
	return s.observe(ref)
}
func (s *DeclarationMigrationSources) collect(ref string) {
	if !migrationRef(ref) {
		s.issue("source_ref_invalid", "")
		return
	}
	for _, v := range s.input.Sources {
		if v.Ref == ref {
			return
		}
	}
	if len(s.input.Sources) >= migrationMaxSources {
		s.issue("source_bound_exceeded", "")
		return
	}
	source := MigrationSource{MigrationSourceClaim: MigrationSourceClaim{Ref: ref, State: "absent"}}
	info, err := s.observe(ref)
	if err != nil {
		source.State = "unreadable"
	} else if info != nil {
		raw, e := migrationReadSource(s.root, ref)
		if e != nil {
			source.State = "unreadable"
		} else if s.total+len(raw) > migrationMaxTotalBytes {
			source.State = "unreadable"
			s.issue("source_byte_bound_exceeded", ref)
		} else {
			f, openErr := s.root.OpenFile(ref, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
			if openErr != nil {
				source.State = "unreadable"
				s.issue("source_unreadable", ref)
				s.input.Sources = append(s.input.Sources, source)
				return
			}
			held, statErr := f.Stat()
			heldRaw, readErr := io.ReadAll(io.LimitReader(f, migrationMaxSourceBytes+1))
			if statErr != nil || !os.SameFile(info, held) || readErr != nil || declarationHash(heldRaw) != declarationHash(raw) {
				f.Close()
				source.State = "unreadable"
				s.issue("source_generation_changed", ref)
				s.input.Sources = append(s.input.Sources, source)
				return
			}
			s.handles[ref] = f
			s.total += len(raw)
			source.State = "present"
			source.Raw = raw
			source.Digest = declarationHash(raw)
			source.Size = int64(len(raw))
			if n, e := migrationTree(raw); e == nil {
				if v := migrationNode(n, "kind"); v != nil {
					source.Kind = v.Value
				}
				if v := migrationNode(n, "schema_version"); v != nil {
					source.SchemaVersion = v.Value
				}
			}
		}
	}
	if source.State == "unreadable" {
		s.issue("source_unreadable", ref)
	}
	s.input.Sources = append(s.input.Sources, source)
}
func (s *DeclarationMigrationSources) listing(ref string, observe bool) ([]os.DirEntry, []string, error) {
	var info os.FileInfo
	var err error
	if observe {
		info, err = s.observe(ref)
	} else {
		info, err = s.root.Lstat(ref)
		if os.IsNotExist(err) {
			info = nil
			err = nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if info == nil {
		return []os.DirEntry{}, []string{}, nil
	}
	if err = declarationPhysicalPath(s.root, ref, true); err != nil {
		return nil, nil, err
	}
	f, err := s.root.OpenFile(ref, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(migrationMaxSources + 1)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	if len(entries) > migrationMaxSources {
		return nil, nil, errors.New("collection_bound")
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name()+":"+e.Type().String())
	}
	sort.Strings(names)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, names, nil
}
func (s *DeclarationMigrationSources) collection(kind string) {
	ref := kind
	if kind == "services" {
		ref = ".loom/contracts/services"
	}
	c := MigrationCollection{Ref: ref, Completeness: "complete", Entries: []string{}}
	entries, names, err := s.listing(ref, true)
	if err != nil {
		c.Completeness = "unavailable"
		s.issue("collection_unavailable", ref)
	} else {
		s.listings[ref] = names
		wrappers := map[string]string{"scripts": "loom.script.yaml", "workflows": "loom.workflow.yaml", "connectors": "loom.connector.yaml", "modules": "loom.module_project.yaml", "schedules": "loom.schedule.yaml", "direct_events": "loom.direct_event.yaml"}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			child := path.Join(ref, e.Name())
			if kind != "services" {
				if !e.IsDir() {
					c.Completeness = "unavailable"
					continue
				}
				child = path.Join(child, wrappers[kind])
			}
			if !migrationRef(child) {
				c.Completeness = "unavailable"
				s.issue("collection_entry_invalid", ref)
				continue
			}
			c.Entries = append(c.Entries, child)
			s.collect(child)
		}
	}
	s.input.Collections = append(s.input.Collections, c)
}

func (s *DeclarationMigrationSources) Check() error {
	stale := errors.New("source_snapshot_changed")
	root, err := os.Stat(s.location)
	if err != nil || s.observed["."] == nil || !os.SameFile(root, s.observed["."]) {
		return stale
	}
	// Check parents before using paths; map iteration order is immaterial because
	// every read is still confined to the original open root.
	for ref, before := range s.observed {
		after, err := s.root.Lstat(ref)
		if before == nil {
			if !os.IsNotExist(err) {
				return stale
			}
			continue
		}
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			return stale
		}
	}
	for _, v := range s.input.Sources {
		if v.State == "present" {
			f := s.handles[v.Ref]
			if f == nil {
				return stale
			}
			st, e := f.Stat()
			if e != nil || !os.SameFile(st, s.observed[v.Ref]) {
				return stale
			}
			if _, e = f.Seek(0, io.SeekStart); e != nil {
				return stale
			}
			held, e := io.ReadAll(io.LimitReader(f, migrationMaxSourceBytes+1))
			if e != nil || declarationHash(held) != v.Digest {
				return stale
			}
			raw, err := migrationReadSource(s.root, v.Ref)
			if err != nil || declarationHash(raw) != v.Digest {
				return stale
			}
		}
	}
	for ref, want := range s.listings {
		_, got, err := s.listing(ref, false)
		if err != nil || !reflect.DeepEqual(want, got) {
			return stale
		}
	}
	return nil
}

// Preserve the existing metadata path/type/device/byte checks, with a
// nonblocking leaf open so substitution by a FIFO cannot stall this reader.
func migrationReadSource(root *os.Root, ref string) ([]byte, error) {
	if !declarationPath(ref, true) {
		return nil, errors.New("source_ref_invalid")
	}
	if e := declarationPhysicalPath(root, ref, false); e != nil {
		return nil, e
	}
	f, e := root.OpenFile(ref, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() {
		return nil, errors.New("source_type_changed")
	}
	raw, e := io.ReadAll(io.LimitReader(f, migrationMaxSourceBytes+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > migrationMaxSourceBytes {
		return nil, errors.New("source_byte_bound")
	}
	if e = declarationPhysicalPath(root, ref, false); e != nil {
		return nil, e
	}
	after, e := root.Lstat(ref)
	if e != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("source_generation_changed")
	}
	return raw, nil
}
