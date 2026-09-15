package agentpack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const workspaceExportSchema = "loom.workspace-export.v1"
const workspaceExportManifest = "export-manifest.json"
const workspaceExportSkin = "nix/files/mina/skins/mina-matrix-teal.yaml"
const workspaceExportSkinHash = "0175be4213c7deb52710dd993691d5f0c30bacd763d2a854381ca85a019095ef"
const workspaceExportMaxBytes = 2 * 1024 * 1024

// This is a source selection, never a walk of a workspace or live profile.
var workspaceExportSources = []string{
	".hermes/SOUL.md", "AGENTS.md", "WORKFLOW.md", "OPERATING-POLICY.md", "WORKSPACE-MAP.md", "TOOLING.md",
	"protocols/README.md", "protocols/BASECAMP.md", "protocols/GITHUB.md", "protocols/CREDENTIALS.md",
	"protocols/EXTERNAL-MESSAGING.md", "protocols/LOOM-ROUTING.md", "protocols/PROJECT-DELEGATION.md",
	"protocols/PROVENANCE-AND-MEMORY.md", "protocols/SESSION-RETRIEVAL.md", "protocols/SKILL-CREATION-AND-PROMOTION.md",
	"protocols/DEVICE-AND-TOOL-ROUTING.md", "protocols/MAC-COMPUTER-USE.md",
	"handoffs/HANDOFF-TEMPLATE.md",
}

const workspaceExportReadme = `# MINA portable source

This projection contains reviewed LOOM instructions and the supplied MINA skin.
LOOM owns the upstream sources. Propose changes there for review, then export a
new checkpoint into a new directory. This directory is not a live agent,
Hermes profile, sync target or deployment channel. Do not initialize Hermes here.

export-manifest.json records the exact local source revision, pack metadata
input, copied source paths, generated-file provenance and every payload digest.
Its export_id is SHA-256 of compact JSON with export_id empty. The manifest
itself is excluded from its payload census to avoid a recursive digest.

For a portable reconstruction, review this exact inventory and commit only its
listed payloads plus export-manifest.json in a disposable local Git repository.
Reconstruct that commit into another disposable directory and compare every byte
count and SHA-256 against the manifest. Never enroll a live profile in Git.
This restores instructions only: it cannot restore memory, history, config,
authentication or credentials. Existing scaffold replay preserves user files;
it does not migrate or overwrite a live SOUL. Installation of the skin and any
runtime selection belongs to the separately reviewed operator checkpoint.

The first export excludes all agent-created skills. Later skill promotion needs
individual review. Runtime state, private work and credentials are never export
inputs. A private repository does not grant publication authority. Review the
complete history and use the named GitHub protocol before any remote operation.
`

// An explicit include list also keeps accidental local additions out of Git.
func workspaceExportIgnore() []byte {
	lines := []string{"# Defence in depth; the exporter uses a positive allowlist.", "*", "!/.gitignore", "!/README.md", "!/export-manifest.json", "!/.hermes/", "!/protocols/", "!/handoffs/", "!/skins/"}
	for _, path := range workspaceExportSources {
		lines = append(lines, "!/"+path)
	}
	lines = append(lines, "!/skins/mina-matrix-teal.yaml")
	return []byte(strings.Join(lines, "\n") + "\n")
}

type WorkspaceExportOptions struct {
	Template   string `json:"template"`
	SourceRoot string `json:"-"`
	Path       string `json:"-"`
}

type WorkspaceExportFile struct {
	Origin     string `json:"origin"`
	SourcePath string `json:"source_path,omitempty"`
	Path       string `json:"path"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
}

type WorkspaceExportManifest struct {
	Schema             string                `json:"schema"`
	ExportID           string                `json:"export_id"`
	Template           string                `json:"template"`
	SourceRevision     string                `json:"source_revision"`
	PackVersion        string                `json:"pack_version"`
	PackManifestSHA256 string                `json:"pack_manifest_sha256"`
	Files              []WorkspaceExportFile `json:"files"`
}

// Plans deliberately do not serialize host paths or source content. Apply accepts
// only an intact plan prepared in this process and revalidates all source bytes.
type WorkspaceExportPlan struct {
	Manifest WorkspaceExportManifest `json:"manifest"`
	options  WorkspaceExportOptions
	payloads map[string][]byte
	pins     map[string]os.FileInfo
}

type WorkspaceExportCreated struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Complete bool   `json:"complete"`
}

type WorkspaceExportResult struct {
	Manifest WorkspaceExportManifest  `json:"manifest"`
	Applied  bool                     `json:"applied"`
	Created  []WorkspaceExportCreated `json:"created"`
}

func exportDigest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// pinDirectory uses the existing no-follow descriptor opener and remembers every
// ancestor identity, including ancestors outside the source/destination root.
func (p *WorkspaceExportPlan) pinDirectory(path string) error {
	for {
		root, err := openWorkspaceDirectory(path)
		if err != nil {
			return fmt.Errorf("export directory is unavailable or unsafe")
		}
		info, err := root.Stat(".")
		root.Close()
		if err != nil {
			return fmt.Errorf("export directory inspection failed")
		}
		if old, exists := p.pins[path]; exists && !os.SameFile(old, info) {
			return fmt.Errorf("export directory substituted")
		}
		p.pins[path] = info
		parent := filepath.Dir(path)
		if path == parent {
			return nil
		}
		path = parent
	}
}

// Match only the OS-selected temporary anchor exception in the shared opener.
func exportAbsolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	temp := filepath.Clean(os.TempDir())
	if relative, err := filepath.Rel(temp, absolute); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		if resolved, err := filepath.EvalSymlinks(temp); err == nil {
			absolute = filepath.Join(resolved, relative)
		}
	}
	return absolute, nil
}

func exportSingleFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1 && info.Size() <= maxWorkspaceFileSize
}

func (p *WorkspaceExportPlan) readSource(relative string) ([]byte, error) {
	path := filepath.Join(p.options.SourceRoot, filepath.FromSlash(relative))
	if err := p.pinDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	parent, err := openWorkspaceDirectory(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("unsafe export source parent")
	}
	defer parent.Close()
	before, err := parent.Lstat(filepath.Base(path))
	if err != nil || !exportSingleFile(before) {
		return nil, fmt.Errorf("export source must be a bounded independent regular file: %s", relative)
	}
	if old, exists := p.pins[path]; exists && (!os.SameFile(old, before) || old.Size() != before.Size() || !old.ModTime().Equal(before.ModTime())) {
		return nil, fmt.Errorf("export source substituted or changed: %s", relative)
	}
	payload, err := readWorkspaceFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot safely read export source: %s", relative)
	}
	after, err := parent.Lstat(filepath.Base(path))
	if err != nil || !exportSingleFile(after) || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("export source changed while reading: %s", relative)
	}
	p.pins[path] = after
	return payload, nil
}

func (p WorkspaceExportPlan) checkPins() error {
	for path, before := range p.pins {
		if before.IsDir() {
			root, err := openWorkspaceDirectory(path)
			if err != nil {
				return fmt.Errorf("export ancestor unavailable or substituted")
			}
			after, err := root.Stat(".")
			root.Close()
			if err != nil || !os.SameFile(before, after) {
				return fmt.Errorf("export ancestor substituted")
			}
		} else {
			parent, err := openWorkspaceDirectory(filepath.Dir(path))
			if err != nil {
				return fmt.Errorf("export source parent substituted")
			}
			after, err := parent.Lstat(filepath.Base(path))
			parent.Close()
			if err != nil || !exportSingleFile(after) || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				return fmt.Errorf("export source substituted or changed")
			}
		}
	}
	return nil
}

type exportBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *exportBoundedBuffer) Write(v []byte) (int, error) {
	if len(v) > b.limit-b.Len() {
		return 0, fmt.Errorf("Git output exceeds export bound")
	}
	return b.Buffer.Write(v)
}

// Only fixed local read commands call this helper. No inherited GIT_* settings,
// user/system config, replace objects, optional writes, hooks or lazy fetching.
func exportGit(root string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixed := []string{"--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-C", root}
	cmd := exec.CommandContext(ctx, "git", append(fixed, args...)...)
	// A minimal environment excludes alternate index/object/worktree selection and
	// config injection. PATH selects the operator's installed Git, not a pack tool.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/dev/null", "XDG_CONFIG_HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "GIT_ATTR_NOSYSTEM=1", "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard
	output := &exportBoundedBuffer{limit: workspaceExportMaxBytes + 64*1024}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("local Git source inspection failed")
	}
	return output.Bytes(), nil
}

var exportOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (p *WorkspaceExportPlan) gitRevision() (string, error) {
	// Require the explicitly selected directory itself to be a checkout root.
	gitEntry, err := os.Lstat(filepath.Join(p.options.SourceRoot, ".git"))
	if err != nil || (!gitEntry.IsDir() && !exportSingleFile(gitEntry)) {
		return "", fmt.Errorf("explicit source root must be a Git checkout")
	}
	if gitEntry.IsDir() {
		err = p.pinDirectory(filepath.Join(p.options.SourceRoot, ".git"))
	} else {
		_, err = p.readSource(".git")
	}
	if err != nil {
		return "", err
	}
	common, err := exportGit(p.options.SourceRoot, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonPath := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonPath) {
		return "", fmt.Errorf("Git common directory is not absolute")
	}
	commonPath, err = exportAbsolutePath(commonPath)
	if err != nil {
		return "", fmt.Errorf("invalid Git common directory")
	}
	if err := p.pinDirectory(commonPath); err != nil {
		return "", err
	}
	// Never use repository-level alternates, even if the selected objects happen
	// to be locally present. Worktree gitdir/commondir routing remains supported.
	for _, name := range []string{"alternates", "http-alternates"} {
		path := filepath.Join(commonPath, "objects", "info", name)
		parent, err := openWorkspaceDirectory(filepath.Dir(path))
		if err != nil {
			return "", fmt.Errorf("unsafe Git object metadata")
		}
		_, err = parent.Lstat(name)
		parent.Close()
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("Git object alternates are outside export authority")
		}
	}
	value, err := exportGit(p.options.SourceRoot, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	revision := strings.TrimSpace(string(value))
	if !exportOID.MatchString(revision) {
		return "", fmt.Errorf("invalid Git source revision")
	}
	return revision, p.checkPins()
}

func (p *WorkspaceExportPlan) bindGit(revision string, sources map[string][]byte) error {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	args := append([]string{"ls-tree", "-z", revision, "--"}, paths...)
	tree, err := exportGit(p.options.SourceRoot, nil, args...)
	if err != nil {
		return err
	}
	objects := map[string]string{}
	for _, line := range bytes.Split(tree, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		meta, path, ok := strings.Cut(string(line), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || !exportOID.MatchString(fields[2]) {
			return fmt.Errorf("export source must be tracked regular non-executable blobs")
		}
		if _, ok := sources[path]; !ok {
			return fmt.Errorf("unexpected Git source")
		}
		if _, ok := objects[path]; ok {
			return fmt.Errorf("duplicate Git source")
		}
		objects[path] = fields[2]
	}
	if len(objects) != len(sources) {
		return fmt.Errorf("export source is absent from HEAD")
	}
	var request strings.Builder
	for _, path := range paths {
		request.WriteString(objects[path] + "\n")
	}
	response, err := exportGit(p.options.SourceRoot, []byte(request.String()), "cat-file", "--batch")
	if err != nil {
		return err
	}
	for _, path := range paths {
		index := bytes.IndexByte(response, '\n')
		if index < 0 {
			return fmt.Errorf("invalid Git blob response")
		}
		expected := fmt.Sprintf("%s blob %d", objects[path], len(sources[path]))
		if string(response[:index]) != expected {
			return fmt.Errorf("selected source differs from HEAD: %s", path)
		}
		response = response[index+1:]
		n := len(sources[path])
		if len(response) < n+1 || !bytes.Equal(response[:n], sources[path]) || response[n] != '\n' {
			return fmt.Errorf("selected source differs from HEAD: %s", path)
		}
		response = response[n+1:]
	}
	if len(response) != 0 {
		return fmt.Errorf("unexpected Git blob data")
	}
	return nil
}

func PlanWorkspaceExport(options WorkspaceExportOptions) (WorkspaceExportPlan, error) {
	p := WorkspaceExportPlan{options: options, payloads: map[string][]byte{}, pins: map[string]os.FileInfo{}}
	fail := func(err error) (WorkspaceExportPlan, error) { return WorkspaceExportPlan{}, err }
	if options.Template != "mina" || strings.TrimSpace(options.SourceRoot) == "" || strings.TrimSpace(options.Path) == "" {
		return fail(fmt.Errorf("export requires explicit --template mina, --source-root and --path"))
	}
	var err error
	p.options.SourceRoot, err = exportAbsolutePath(options.SourceRoot)
	if err != nil {
		return fail(fmt.Errorf("invalid source root"))
	}
	p.options.Path, err = exportAbsolutePath(options.Path)
	if err != nil {
		return fail(fmt.Errorf("invalid destination"))
	}
	if err = p.pinDirectory(p.options.SourceRoot); err != nil {
		return fail(err)
	}
	if err = p.pinDirectory(filepath.Dir(p.options.Path)); err != nil {
		return fail(err)
	}
	parent, err := openWorkspaceDirectory(filepath.Dir(p.options.Path))
	if err != nil {
		return fail(fmt.Errorf("unsafe destination parent"))
	}
	_, err = parent.Lstat(filepath.Base(p.options.Path))
	parent.Close()
	if !os.IsNotExist(err) {
		return fail(fmt.Errorf("export destination must be absent; existing data is never replaced"))
	}
	revision, err := p.gitRevision()
	if err != nil {
		return fail(err)
	}
	sources := map[string][]byte{}
	data, err := p.readSource("ai-loom-pack/manifest.yaml")
	if err != nil {
		return fail(err)
	}
	sources["ai-loom-pack/manifest.yaml"] = data
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if decoder.Decode(&manifest) != nil || manifest.SchemaVersion != "loom.ai_pack.v2" || manifest.Name != "ai-loom-pack" || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(manifest.Version) {
		return fail(fmt.Errorf("invalid source pack manifest"))
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fail(fmt.Errorf("multiple pack manifest documents"))
	}
	template, err := findWorkspaceTemplate(&Pack{Manifest: manifest}, "mina")
	if err != nil || template.Path != "templates/mina" {
		return fail(fmt.Errorf("invalid MINA source template"))
	}
	declared := map[string]bool{}
	for _, path := range template.Files {
		normalized, e := normalizeWorkspaceRelativePath(path)
		if e != nil || normalized != path || declared[path] || validateMorathustraPath(path, false) != nil {
			return fail(fmt.Errorf("invalid or duplicate MINA source declaration"))
		}
		declared[path] = true
	}
	// Only the four known scaffold placeholders are outside first publication.
	for _, path := range append(append([]string{}, workspaceExportSources...), "investigations/README.md", "recovery/README.md", "skills/installed/README.md", "tmp/README.md") {
		if !declared[path] {
			return fail(fmt.Errorf("MINA source declaration changed; review export allowlist"))
		}
		delete(declared, path)
	}
	if len(declared) != 0 {
		return fail(fmt.Errorf("new MINA source declaration needs explicit export review"))
	}
	p.Manifest = WorkspaceExportManifest{Schema: workspaceExportSchema, Template: "mina", SourceRevision: revision, PackVersion: manifest.Version, PackManifestSHA256: exportDigest(data)}
	total := 0
	add := func(path, source, origin string, payload []byte) error {
		normalized, e := normalizeWorkspaceRelativePath(path)
		if e != nil || normalized != path || p.payloads[path] != nil {
			return fmt.Errorf("invalid or duplicate export destination")
		}
		total += len(payload)
		if len(payload) > maxWorkspaceFileSize || total > workspaceExportMaxBytes || len(p.payloads) >= 22 {
			return fmt.Errorf("export payload bound exceeded")
		}
		p.payloads[path] = payload
		p.Manifest.Files = append(p.Manifest.Files, WorkspaceExportFile{Origin: origin, SourcePath: source, Path: path, Bytes: len(payload), SHA256: exportDigest(payload)})
		return nil
	}
	for _, path := range workspaceExportSources {
		source := "ai-loom-pack/templates/mina/" + path
		data, err := p.readSource(source)
		if err != nil {
			return fail(err)
		}
		if err = validateMorathustraContent(path, data); err != nil {
			return fail(err)
		}
		sources[source] = data
		if err = add(path, source, "source", data); err != nil {
			return fail(err)
		}
	}
	protocol := string(p.payloads["protocols/GITHUB.md"])
	_, list, ok := strings.Cut(protocol, "Initially allow only these source templates:")
	if !ok {
		return fail(fmt.Errorf("missing GITHUB export contract"))
	}
	list, _, ok = strings.Cut(list, "The projection is versioned source")
	if !ok {
		return fail(fmt.Errorf("missing GITHUB export contract terminator"))
	}
	matches := regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(list, -1)
	wanted := map[string]bool{}
	for _, path := range workspaceExportSources {
		wanted[path] = true
	}
	for _, match := range matches {
		if match[1] == ".hermes" {
			continue
		} // The protocol explicitly forbids copying this directory.
		if !wanted[match[1]] {
			return fail(fmt.Errorf("GITHUB export allowlist changed"))
		}
		delete(wanted, match[1])
	}
	if len(wanted) != 0 {
		return fail(fmt.Errorf("GITHUB export allowlist incomplete"))
	}
	skin, err := p.readSource(workspaceExportSkin)
	if err != nil {
		return fail(err)
	}
	if len(skin) != 10972 || exportDigest(skin) != workspaceExportSkinHash {
		return fail(fmt.Errorf("supplied MINA skin hash mismatch"))
	}
	sources[workspaceExportSkin] = skin
	if err = add("skins/mina-matrix-teal.yaml", workspaceExportSkin, "source", skin); err != nil {
		return fail(err)
	}
	if err = add("README.md", "", "generated", []byte(workspaceExportReadme)); err != nil {
		return fail(err)
	}
	if err = add(".gitignore", "", "generated", workspaceExportIgnore()); err != nil {
		return fail(err)
	}
	if err = p.bindGit(revision, sources); err != nil {
		return fail(err)
	}
	current, err := p.gitRevision()
	if err != nil {
		return fail(err)
	}
	if current != revision {
		return fail(fmt.Errorf("Git HEAD changed during export planning"))
	}
	sort.Slice(p.Manifest.Files, func(i, j int) bool { return p.Manifest.Files[i].Path < p.Manifest.Files[j].Path })
	canonical, _ := json.Marshal(p.Manifest)
	p.Manifest.ExportID = exportDigest(canonical)
	data, _ = json.MarshalIndent(p.Manifest, "", "  ")
	data = append(data, '\n')
	if len(data) > maxWorkspaceFileSize || total+len(data) > workspaceExportMaxBytes {
		return fail(fmt.Errorf("export bound includes the generated manifest"))
	}
	p.payloads[workspaceExportManifest] = data
	return p, nil
}

// Apply writes through verified directory descriptors. A failed publication is
// left for operator inspection; Created records successes even on an error.
func ApplyWorkspaceExport(plan WorkspaceExportPlan, confirmed bool) (WorkspaceExportResult, error) {
	return applyWorkspaceExport(plan, confirmed, nil)
}

// A per-call test seam exercises post-publication failures without global hooks.
func applyWorkspaceExport(plan WorkspaceExportPlan, confirmed bool, afterCreate func(string) error) (WorkspaceExportResult, error) {
	result := WorkspaceExportResult{Manifest: plan.Manifest, Created: []WorkspaceExportCreated{}}
	if !confirmed {
		return result, fmt.Errorf("workspace export requires --yes")
	}
	if plan.payloads == nil || plan.pins == nil {
		return result, fmt.Errorf("export plan must be prepared in this process")
	}
	if err := plan.checkPins(); err != nil {
		return result, err
	}
	fresh, err := PlanWorkspaceExport(plan.options)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(plan.Manifest, fresh.Manifest) || !reflect.DeepEqual(plan.payloads, fresh.payloads) {
		return result, fmt.Errorf("export plan changed; review a fresh plan")
	}
	if err = plan.checkPins(); err != nil {
		return result, err
	}
	parent, err := openWorkspaceDirectory(filepath.Dir(plan.options.Path))
	if err != nil {
		return result, fmt.Errorf("unsafe destination parent")
	}
	defer parent.Close()
	info, err := parent.Stat(".")
	if err != nil || !os.SameFile(plan.pins[filepath.Dir(plan.options.Path)], info) {
		return result, fmt.Errorf("destination parent substituted")
	}
	if err = parent.Mkdir(filepath.Base(plan.options.Path), 0700); err != nil {
		return result, fmt.Errorf("cannot create absent export destination")
	}
	result.Created = append(result.Created, WorkspaceExportCreated{Path: ".", Kind: "directory", Complete: true})
	if err = fresh.pinDirectory(plan.options.Path); err != nil {
		return result, err
	}
	if afterCreate != nil {
		if err = afterCreate("."); err != nil {
			return result, err
		}
	}
	if err = fresh.checkPins(); err != nil {
		return result, err
	}
	dirs := map[string]*os.Root{}
	defer func() {
		for _, root := range dirs {
			root.Close()
		}
	}()
	// Open each created directory independently with the shared no-follow helper;
	// pins revalidate attachment before every effect and again after all writes.
	root, err := openWorkspaceDirectory(plan.options.Path)
	if err != nil {
		return result, fmt.Errorf("export root substituted")
	}
	dirs["."] = root
	paths := make([]string, 0, len(plan.payloads))
	for path := range plan.payloads {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err = fresh.checkPins(); err != nil {
			return result, err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if dirs[dir] == nil {
			// Current positive allowlist has only single-level payload directories.
			if strings.Contains(dir, "/") {
				return result, fmt.Errorf("unsupported export directory depth")
			}
			if err = dirs["."].Mkdir(dir, 0700); err != nil {
				return result, fmt.Errorf("cannot create export directory: %s", dir)
			}
			result.Created = append(result.Created, WorkspaceExportCreated{Path: dir, Kind: "directory", Complete: true})
			if err = fresh.pinDirectory(filepath.Join(plan.options.Path, dir)); err != nil {
				return result, err
			}
			if afterCreate != nil {
				if err = afterCreate(dir); err != nil {
					return result, err
				}
			}
			if err = fresh.checkPins(); err != nil {
				return result, err
			}
			dirs[dir], err = openWorkspaceDirectory(filepath.Join(plan.options.Path, dir))
			if err != nil {
				return result, fmt.Errorf("export directory substituted")
			}
		}
		file, err := dirs[dir].OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return result, fmt.Errorf("cannot create export file: %s", path)
		}
		result.Created = append(result.Created, WorkspaceExportCreated{Path: path, Kind: "file"})
		if afterCreate != nil {
			if err = afterCreate(path); err != nil {
				file.Close()
				return result, err
			}
		}
		_, writeErr := file.Write(plan.payloads[path])
		syncErr := file.Sync()
		writtenInfo, statErr := file.Stat()
		closeErr := file.Close()
		if writeErr != nil || syncErr != nil || statErr != nil || closeErr != nil {
			return result, fmt.Errorf("export file write incomplete: %s", path)
		}
		fresh.pins[filepath.Join(plan.options.Path, path)] = writtenInfo
		result.Created[len(result.Created)-1].Complete = true
	}
	if err = fresh.checkPins(); err != nil {
		return result, err
	}
	current, err := fresh.gitRevision()
	if err != nil {
		return result, err
	}
	if current != plan.Manifest.SourceRevision {
		return result, fmt.Errorf("Git HEAD changed during publication")
	}
	// Recheck selected source bytes as well as inode metadata after publication.
	for _, entry := range plan.Manifest.Files {
		if entry.Origin != "source" {
			continue
		}
		payload, err := fresh.readSource(entry.SourcePath)
		if err != nil || exportDigest(payload) != entry.SHA256 {
			return result, fmt.Errorf("selected source changed during publication")
		}
	}
	metadata, err := fresh.readSource("ai-loom-pack/manifest.yaml")
	if err != nil || exportDigest(metadata) != plan.Manifest.PackManifestSHA256 {
		return result, fmt.Errorf("pack source changed during publication")
	}
	expected := map[string]bool{}
	for _, created := range result.Created {
		expected[created.Path] = true
	}
	for dir, descriptor := range dirs {
		file, err := descriptor.Open(".")
		if err != nil {
			return result, fmt.Errorf("cannot inspect export inventory")
		}
		entries, readErr := file.ReadDir(26)
		file.Close()
		if readErr != nil && readErr != io.EOF || len(entries) >= 26 {
			return result, fmt.Errorf("export inventory bound exceeded")
		}
		for _, entry := range entries {
			if !expected[filepath.ToSlash(filepath.Join(dir, entry.Name()))] {
				return result, fmt.Errorf("unknown entry in published export")
			}
		}
	}
	// Read back only the explicitly created payloads; unknown data is never read
	// or removed. This catches link/byte substitution before claiming success.
	for _, path := range paths {
		parent := dirs[filepath.ToSlash(filepath.Dir(path))]
		info, err := parent.Lstat(filepath.Base(path))
		if err != nil || !exportSingleFile(info) {
			return result, fmt.Errorf("published export file substituted: %s", path)
		}
		data, err := readWorkspaceFile(filepath.Join(plan.options.Path, path))
		if err != nil || !bytes.Equal(data, plan.payloads[path]) {
			return result, fmt.Errorf("published export verification failed: %s", path)
		}
	}
	if err = fresh.checkPins(); err != nil {
		return result, err
	}
	current, err = fresh.gitRevision()
	if err != nil {
		return result, err
	}
	if current != plan.Manifest.SourceRevision {
		return result, fmt.Errorf("Git HEAD changed during publication")
	}
	result.Applied = true
	return result, nil
}
