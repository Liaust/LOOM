package applicationbuild

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/pelletier/go-toml/v2"
)

type cargoManifest struct {
	Package struct {
		Name        string `toml:"name"`
		DefaultRun  string `toml:"default-run"`
		Autobins    *bool  `toml:"autobins"`
		RustVersion any    `toml:"rust-version"`
	} `toml:"package"`
	Workspace struct {
		Members        []string       `toml:"members"`
		DefaultMembers []string       `toml:"default-members"`
		Exclude        []string       `toml:"exclude"`
		Dependencies   map[string]any `toml:"dependencies"`
		Package        map[string]any `toml:"package"`
	} `toml:"workspace"`
	Bins []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
	Dependencies      map[string]any `toml:"dependencies"`
	BuildDependencies map[string]any `toml:"build-dependencies"`
	DevDependencies   map[string]any `toml:"dev-dependencies"`
	Target            map[string]struct {
		Dependencies      map[string]any `toml:"dependencies"`
		BuildDependencies map[string]any `toml:"build-dependencies"`
		DevDependencies   map[string]any `toml:"dev-dependencies"`
	} `toml:"target"`
	Patch   map[string]map[string]any `toml:"patch"`
	Replace map[string]any            `toml:"replace"`
}

type cargoLock struct {
	Version  int               `toml:"version"`
	Packages []cargoDependency `toml:"package"`
}
type cargoDependency struct {
	Name     string `toml:"name"`
	Version  string `toml:"version"`
	Source   string `toml:"source"`
	Checksum string `toml:"checksum"`
}

var cargoName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,127}$`)
var cargoVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func inspectCargo(p *Program, files map[string][]byte, options Options) error {
	if files["Cargo.toml"] == nil {
		return unsupported("cargo_manifest_missing", "select a source root containing Cargo.toml")
	}
	if files["Cargo.lock"] == nil {
		return unsupported("cargo_lock_required", "generate and save Cargo.lock with ordinary Cargo before deployment")
	}
	var manifest cargoManifest
	if err := toml.Unmarshal(files["Cargo.toml"], &manifest); err != nil {
		return unsupported("cargo_manifest_invalid", "Cargo.toml")
	}
	if err := checkCargoConfig(files); err != nil {
		return err
	}
	members := map[string]cargoManifest{".": manifest}
	for file, raw := range files {
		if file == "Cargo.toml" || !strings.HasSuffix(file, "/Cargo.toml") {
			continue
		}
		dir := path.Dir(file)
		included := false
		for _, pattern := range manifest.Workspace.Members {
			match, err := doublestar.Match(pattern, dir)
			if err != nil {
				return unsupported("cargo_workspace_pattern", pattern)
			}
			included = included || match
		}
		for _, pattern := range manifest.Workspace.Exclude {
			match, _ := doublestar.Match(pattern, dir)
			if match {
				included = false
			}
		}
		if !included {
			continue
		}
		var m cargoManifest
		if toml.Unmarshal(raw, &m) != nil {
			return unsupported("cargo_manifest_invalid", file)
		}
		members[dir] = m
	}
	if len(manifest.Workspace.Members) > 256 {
		return ErrUnsafeSource
	}
	for _, pattern := range manifest.Workspace.Members {
		if path.IsAbs(pattern) || strings.Contains(pattern, "..") {
			return unsupported("cargo_workspace_outside_source", pattern)
		}
		matched := false
		for dir := range members {
			match, _ := doublestar.Match(pattern, dir)
			matched = matched || match
		}
		if !matched {
			return unsupported("cargo_workspace_member_missing", pattern)
		}
	}
	choices := map[string][]string{}
	defaults := []string{}
	for dir, m := range members {
		if m.Package.Name == "" {
			continue
		}
		if !cargoName.MatchString(m.Package.Name) {
			return unsupported("cargo_manifest_invalid", "invalid package name")
		}
		version := m.Package.RustVersion
		if v, ok := version.(map[string]any); ok && v["workspace"] == true {
			version = manifest.Workspace.Package["rust-version"]
		}
		if required, ok := version.(string); ok && options.RustVersion != "" && !rustVersionAtLeast(options.RustVersion, required) {
			return unsupported("cargo_toolchain_unsupported", "rust-version "+required+" exceeds pinned "+options.RustVersion)
		}
		if err := inspectLocalDependencies(dir, m, manifest, files, map[string]bool{}); err != nil {
			return err
		}
		if len(manifest.Workspace.DefaultMembers) > 0 && p.Build.Package == "" {
			selected := false
			for _, pattern := range manifest.Workspace.DefaultMembers {
				match, _ := doublestar.Match(pattern, dir)
				selected = selected || match
			}
			if !selected {
				continue
			}
		}
		if p.Build.Package != "" && p.Build.Package != m.Package.Name {
			continue
		}
		bins := []string{}
		for _, bin := range m.Bins {
			name := bin.Name
			if name == "" {
				name = m.Package.Name
			}
			file := bin.Path
			if file == "" {
				if name == m.Package.Name {
					file = "src/main.rs"
				} else {
					file = "src/bin/" + name + ".rs"
				}
			}
			if !cargoName.MatchString(name) || !insideCargoSource(dir, file) || files[path.Join(dir, file)] == nil {
				return unsupported("cargo_binary_source_missing", path.Join(dir, file))
			}
			bins = append(bins, name)
		}
		if m.Package.Autobins == nil || *m.Package.Autobins {
			if files[path.Join(dir, "src/main.rs")] != nil {
				bins = append(bins, m.Package.Name)
			}
			for file := range files {
				prefix := path.Join(dir, "src/bin") + "/"
				if tail, ok := strings.CutPrefix(file, prefix); ok {
					if !strings.Contains(tail, "/") && strings.HasSuffix(tail, ".rs") {
						bins = append(bins, strings.TrimSuffix(tail, ".rs"))
					}
					if strings.Count(tail, "/") == 1 && strings.HasSuffix(tail, "/main.rs") {
						bins = append(bins, strings.TrimSuffix(tail, "/main.rs"))
					}
				}
			}
		}
		slices.Sort(bins)
		bins = slices.Compact(bins)
		if p.Run.Binary == "" && m.Package.DefaultRun != "" {
			if slices.Contains(bins, m.Package.DefaultRun) {
				defaults = append(defaults, m.Package.DefaultRun)
			} else {
				return unsupported("cargo_default_run_missing", m.Package.DefaultRun)
			}
		}
		for _, bin := range bins {
			if !cargoName.MatchString(bin) {
				return ErrInvalidInput
			}
			choices[bin] = append(choices[bin], m.Package.Name)
		}
	}
	if p.Run.Binary == "" {
		if len(defaults) == 1 {
			p.Run.Binary = defaults[0]
		} else {
			if len(choices) != 1 {
				names := make([]string, 0, len(choices))
				for name := range choices {
					names = append(names, name)
				}
				slices.Sort(names)
				if len(names) > 16 {
					names = names[:16]
				}
				return unsupported("cargo_select_binary", fmt.Sprintf("select run.binary and source.build.package; executable choices: %s", strings.Join(names, ", ")))
			}
			for name := range choices {
				p.Run.Binary = name
			}
		}
	}
	packages := choices[p.Run.Binary]
	if len(packages) != 1 {
		return unsupported("cargo_select_package", "select one source.build.package for "+p.Run.Binary)
	}
	p.Build.Package = packages[0]
	for name, a := range p.Actions {
		if a.PythonModule != "" || len(a.Args) != 0 {
			return ErrInvalidInput
		}
		if a.Binary == "" {
			a.Binary = p.Run.Binary
			p.Actions[name] = a
		}
		if !slices.Contains(choices[a.Binary], p.Build.Package) {
			return unsupported("cargo_action_binary_missing", a.Binary)
		}
	}
	for _, toolchain := range []string{"rust-toolchain.toml", "rust-toolchain"} {
		if raw := files[toolchain]; raw != nil {
			channel := strings.TrimSpace(string(raw))
			if toolchain == "rust-toolchain.toml" {
				var cfg struct {
					Toolchain struct {
						Channel    string   `toml:"channel"`
						Components []string `toml:"components"`
						Targets    []string `toml:"targets"`
					} `toml:"toolchain"`
				}
				if toml.Unmarshal(raw, &cfg) != nil || len(cfg.Toolchain.Components) > 0 || len(cfg.Toolchain.Targets) > 0 {
					return unsupported("cargo_toolchain_unsupported", toolchain)
				}
				channel = cfg.Toolchain.Channel
			}
			if channel != "stable" && channel != options.RustVersion {
				return unsupported("cargo_toolchain_unsupported", "use pinned Rust "+options.RustVersion+", not "+channel)
			}
		}
	}
	_, err := cargoDependencies(files["Cargo.lock"], options)
	return err
}

func insideCargoSource(dir, ref string) bool {
	resolved := path.Join(dir, ref)
	return !path.IsAbs(ref) && resolved != ".." && !strings.HasPrefix(resolved, "../") && nativeSourcePath(path.Join(resolved, "Cargo.toml"))
}

func inspectLocalDependencies(dir string, m, workspace cargoManifest, files map[string][]byte, seen map[string]bool) error {
	if seen[dir] {
		return nil
	}
	seen[dir] = true
	groups := []map[string]any{m.Dependencies, m.BuildDependencies, m.DevDependencies, m.Replace}
	for _, target := range m.Target {
		groups = append(groups, target.Dependencies, target.BuildDependencies, target.DevDependencies)
	}
	for _, patch := range m.Patch {
		groups = append(groups, patch)
	}
	for _, deps := range groups {
		for name, raw := range deps {
			d, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			base := dir
			if d["workspace"] == true {
				d, _ = workspace.Workspace.Dependencies[name].(map[string]any)
				base = "."
				if d == nil {
					return unsupported("cargo_workspace_dependency_missing", name)
				}
			}
			ref, ok := d["path"].(string)
			if !ok {
				continue
			}
			if !insideCargoSource(base, ref) {
				return unsupported("cargo_dependency_outside_source", name+": "+ref+"; select a common enclosing source root")
			}
			dir := path.Join(base, ref)
			file := path.Join(dir, "Cargo.toml")
			var dep cargoManifest
			if files[file] == nil || toml.Unmarshal(files[file], &dep) != nil {
				return unsupported("cargo_path_dependency_missing", file)
			}
			if err := inspectLocalDependencies(dir, dep, workspace, files, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func cargoDependencies(raw []byte, options Options) ([]cargoDependency, error) {
	var lock cargoLock
	if toml.Unmarshal(raw, &lock) != nil || lock.Version < 3 || lock.Version > 4 || len(lock.Packages) > 4096 {
		return nil, unsupported("cargo_lock_invalid", "expected Cargo.lock version 3 or 4")
	}
	identities := map[string]string{}
	for _, dep := range lock.Packages {
		if !cargoName.MatchString(dep.Name) || !cargoVersion.MatchString(dep.Version) || len(dep.Version) > 128 {
			return nil, unsupported("cargo_lock_invalid", "invalid package identity")
		}
		if dep.Source != "" {
			key := dep.Name + "-" + dep.Version
			if previous := identities[key]; previous != "" && previous != dep.Source {
				return nil, unsupported("cargo_vendor_identity_conflict", "the pinned vendor adapter cannot represent different sources for "+key+"; select distinct dependency versions or a project Nix package")
			}
			identities[key] = dep.Source
		}
		if dep.Source == "" {
			continue
		}
		if dep.Source == "registry+https://github.com/rust-lang/crates.io-index" {
			checksum, err := hex.DecodeString(dep.Checksum)
			if err != nil || len(checksum) != 32 {
				return nil, unsupported("cargo_lock_checksum_missing", dep.Name)
			}
			continue
		}
		if _, _, err := gitDependency(dep.Source, options); err != nil {
			return nil, err
		}
	}
	return lock.Packages, nil
}

func gitDependency(source string, options Options) (string, string, error) {
	u, err := url.Parse(strings.TrimPrefix(source, "git+"))
	hosts := options.FetchGitHosts
	if len(hosts) == 0 {
		hosts = []string{"github.com", "gitlab.com", "codeberg.org"}
	}
	if !strings.HasPrefix(source, "git+") || err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !slices.Contains(hosts, u.Hostname()) || len(strings.Trim(u.Path, "/")) == 0 {
		return "", "", unsupported("cargo_fetch_origin_unsupported", "only approved public HTTPS Git origins and crates.io are supported")
	}
	rev := u.Fragment
	decoded, err := hex.DecodeString(rev)
	if err != nil || (len(decoded) != 20 && len(decoded) != 32) {
		return "", "", unsupported("cargo_git_revision_required", "Cargo.lock must pin an exact Git revision")
	}
	u.Fragment = ""
	u.RawQuery = ""
	return u.String(), rev, nil
}

func rustVersionAtLeast(actual, required string) bool {
	parts := func(s string) []int {
		v := []int{0, 0, 0}
		for i, p := range strings.Split(s, ".") {
			if i >= 3 {
				break
			}
			n, e := strconv.Atoi(p)
			if e != nil {
				return nil
			}
			v[i] = n
		}
		return v
	}
	a, b := parts(actual), parts(required)
	if a == nil || b == nil {
		return false
	}
	return slices.Compare(a, b) >= 0
}

func checkCargoConfig(files map[string][]byte) error {
	if raw := files[".cargo/config.toml"]; raw != nil {
		var cfg map[string]map[string]any
		if toml.Unmarshal(raw, &cfg) != nil {
			return unsupported("cargo_config_unsupported", ".cargo/config.toml")
		}
		for group, fields := range cfg {
			for key, value := range fields {
				if group == "net" && key == "offline" && value == true {
					continue
				}
				if group == "term" && key == "color" {
					if s, ok := value.(string); ok && slices.Contains([]string{"auto", "always", "never"}, s) {
						continue
					}
				}
				return unsupported("cargo_config_unsupported", group+"."+key+"; custom registries, host commands, env and cross targets need a supported Nix package")
			}
		}
	}
	return nil
}
