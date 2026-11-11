package applicationbuild

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"loom.local/loom/internal/applicationlaunch"
)

const nativeWrapperRecipe = `let
  pkgs = import (builtins.toPath "@NIXPKGS@") { system = "@PLATFORM@"; };
  package = builtins.storePath "@PACKAGE@";
  launcher = builtins.storePath "@LAUNCHER@";
  input = builtins.fromJSON (builtins.readFile ./metadata.json);
  names = if input.run.binary == "" then builtins.attrNames (builtins.readDir "${package}/bin") else input.names;
  selected = assert builtins.length names > 0; assert input.run.binary != "" || builtins.length names == 1; names;
  metadata = pkgs.writeText "loom-native-launch.json" (builtins.toJSON {
    schema = "application.launch.v1";
    programs = builtins.listToAttrs (map (name: { inherit name; value = "${package}/bin/${name}"; }) selected);
    inherit (input) run actions;
  });
in pkgs.runCommand "loom-native-application" { preferLocalBuild = true; allowSubstitutes = false; } ''
  mkdir -p "$out/bin" "$out/share/loom"
  cp ${launcher}/bin/loom-application-launcher "$out/bin/loom-application-launcher"
  cp ${metadata} "$out/share/loom/launch.json"
  main=${package}/bin/${if input.run.binary == "" then builtins.head selected else input.run.binary}
  test -x "$main"
  readlink -f "$main" > "$out/share/loom/runtime-executable"
''
`

func nativeProducerDigest(options Options, kind string) string {
	recipe, compiler := "", ""
	switch kind {
	case "cargo":
		recipe, compiler = cargoRecipe, options.RustVersion
	case "python":
		recipe = pythonProgramRecipe
	}
	return hashJSON(struct{ Producer, Nixpkgs, Platform, Compiler, Launcher, Wrapper, Recipe string }{"native-v1:" + kind, options.NixpkgsPath, options.Platform, compiler, options.LauncherPath, nativeWrapperRecipe, recipe})
}

func (s *Service) buildProgram(ctx context.Context, in Input) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.BuildTimeout)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return Output{}, ctx.Err()
	}
	if !storePattern.MatchString(s.options.LauncherPath) {
		return Output{}, unsupported("native_launcher_unavailable", "the host must supply its trusted native launcher package")
	}
	if _, _, _, _, err := configurationMetadata(in); err != nil {
		return Output{}, err
	}
	if err := os.MkdirAll(s.options.WorkDir, 0700); err != nil {
		return Output{}, err
	}
	stage, err := os.MkdirTemp(s.options.WorkDir, "native-")
	if err != nil {
		return Output{}, err
	}
	defer func() {
		if ctx.Err() == nil {
			_ = os.RemoveAll(stage)
		}
	}()
	sourceRoot := filepath.Join(stage, "source")
	if err := os.Mkdir(sourceRoot, 0700); err != nil {
		return Output{}, err
	}
	snapshot, err := s.captureProgram(ctx, in, func(name string, raw []byte) error {
		target := filepath.Join(sourceRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0444)
	})
	if err != nil {
		return Output{}, err
	}
	p := *snapshot.Program
	schema, literals, data, creds, err := configurationMetadata(in)
	if err != nil {
		return Output{}, err
	}
	formats := sortedUnique(in.CompatibleDataFormats)
	format := in.DataFormat
	if format == "" {
		format = "source-v1"
	}
	buildDigest := hashJSON(struct {
		Source, Producer string
		Program          Program
		Schema           any
		Format           string
		Compatible       []string
	}{snapshot.SourceDigest, snapshot.ProducerDigest, p, schema, format, formats})
	actual, reused := s.cache[buildDigest]
	if reused {
		current, e := s.inspect(ctx, actual.StoreRoot)
		if e != nil {
			if ctx.Err() != nil {
				return Output{}, ctx.Err()
			}
			if errors.Is(e, ErrRealization) {
				return Output{}, e
			}
			reused = false
		} else if hashJSON(current) != hashJSON(actual) {
			return Output{}, ErrRealization
		}
	}
	if !reused {
		var packageRoot string
		switch p.Build.Kind {
		case "python":
		case "cargo":
			packageRoot, err = s.realizeCargo(ctx, stage, p)
		case "nix":
			packageRoot, err = s.realizeNix(ctx, stage, p)
		default:
			return Output{}, ErrInvalidInput
		}
		if err != nil {
			return Output{}, err
		}
		metadata := struct {
			Run     RunSpec            `json:"run"`
			Actions map[string]RunSpec `json:"actions"`
			Names   []string           `json:"names"`
		}{Run: p.Run, Actions: map[string]RunSpec{}, Names: selectedPrograms(p)}
		for name, a := range p.Actions {
			metadata.Actions[name] = RunSpec{Binary: a.Binary, PythonModule: a.PythonModule, Args: a.Arguments}
		}
		raw, _ := json.Marshal(metadata)
		if err := os.WriteFile(filepath.Join(stage, "metadata.json"), raw, 0400); err != nil {
			return Output{}, err
		}
		if p.Build.Kind == "python" {
			actual, err = s.realizePythonProgram(ctx, stage)
		} else {
			recipe := strings.NewReplacer("@NIXPKGS@", s.options.NixpkgsPath, "@PLATFORM@", s.options.Platform, "@PACKAGE@", packageRoot, "@LAUNCHER@", s.options.LauncherPath).Replace(nativeWrapperRecipe)
			if err := os.WriteFile(filepath.Join(stage, "build.nix"), []byte(recipe), 0400); err != nil {
				return Output{}, err
			}
			actual, err = s.realize(ctx, stage)
		}
		if err != nil {
			return Output{}, err
		}
	}
	if err := verifyNativePrograms(actual); err != nil {
		return Output{}, err
	}
	identity := in.Source
	if identity.Revision == "" {
		identity.Revision = snapshot.SourceDigest
	}
	out := assemble(identity, in.PythonModule, snapshot.Files, snapshot.SourceDigest, snapshot.ProducerDigest, buildDigest, actual, schema, literals, data, creds, format, formats, s.options.Platform, p.Actions)
	out.Manifest.SchemaVersion = "application.build.manifest.v2"
	out.Descriptor.SchemaVersion = "application.build.artifact.v2"
	out.Manifest.Program = &p
	out.ManifestJSON, _ = json.Marshal(out.Manifest)
	out.DescriptorJSON, _ = json.Marshal(out.Descriptor)
	out.ManifestDigest = hashBytes(out.ManifestJSON)
	out.DescriptorDigest = hashBytes(out.DescriptorJSON)
	out.Reused = reused
	s.cache[buildDigest] = actual
	if !slices.Contains(s.order, buildDigest) {
		s.order = append(s.order, buildDigest)
	}
	for len(s.order) > s.options.CacheEntries {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
	return out, nil
}

// Inspect every declared program, not only the default service executable.
func verifyNativePrograms(actual realization) error {
	raw, err := os.ReadFile(filepath.Join(actual.StoreRoot, "share/loom/launch.json"))
	if err != nil {
		return ErrRealization
	}
	m, err := applicationlaunch.Decode(raw)
	if err != nil {
		return ErrRealization
	}
	for _, program := range m.Programs {
		resolved, err := filepath.EvalSymlinks(program)
		if err != nil {
			return ErrRealization
		}
		owned := false
		for _, item := range actual.Closure {
			owned = owned || strings.HasPrefix(resolved, item.Path+"/")
		}
		if !owned {
			return ErrRealization
		}
		f, err := os.Open(resolved)
		if err != nil {
			return ErrRealization
		}
		magic := make([]byte, 4)
		_, err = f.ReadAt(magic, 0)
		info, statErr := f.Stat()
		f.Close()
		if err != nil || statErr != nil || string(magic) != "\x7fELF" || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return unsupported("native_executable_required", "selected program must be an executable ELF, not an interpreter wrapper")
		}
	}
	return nil
}

func outputStoreRoot(raw []byte) (string, error) {
	var builds []struct {
		Outputs map[string]string `json:"outputs"`
	}
	if json.Unmarshal(raw, &builds) != nil || len(builds) != 1 || !storePattern.MatchString(builds[0].Outputs["out"]) {
		return "", ErrRealization
	}
	return builds[0].Outputs["out"], nil
}

func (s *Service) buildRecipe(ctx context.Context, stage, file, recipe string) (string, error) {
	if err := os.WriteFile(filepath.Join(stage, file), []byte(recipe), 0400); err != nil {
		return "", err
	}
	raw, err := s.command(ctx, stage, "build", "--no-link", "--json", "--max-jobs", "1", "--cores", "2", "--file", filepath.Join(stage, file))
	if err != nil {
		return "", err
	}
	return outputStoreRoot(raw)
}

func (s *Service) realizeNix(ctx context.Context, stage string, p Program) (string, error) {
	attribute := p.Build.Attribute
	if attribute == "" {
		attribute = "default"
	}
	if !strings.Contains(attribute, ".") {
		attribute = "packages." + s.options.Platform + "." + attribute
	}
	raw, err := s.command(ctx, stage, "build", "--no-link", "--json", "--max-jobs", "1", "--cores", "2", "--no-write-lock-file", "--no-update-lock-file", "path:"+filepath.Join(stage, "source")+"#"+attribute)
	if err != nil {
		return "", err
	}
	return outputStoreRoot(raw)
}

func inspectNix(p *Program, files map[string][]byte, options Options) error {
	if files["flake.nix"] == nil || files["flake.lock"] == nil {
		return unsupported("nix_lock_required", "select source containing flake.nix and flake.lock")
	}
	var lock struct {
		Nodes map[string]struct {
			Locked map[string]any `json:"locked"`
		} `json:"nodes"`
	}
	if json.Unmarshal(files["flake.lock"], &lock) != nil || len(lock.Nodes) == 0 || len(lock.Nodes) > 256 {
		return unsupported("nix_lock_invalid", "flake.lock")
	}
	for _, node := range lock.Nodes {
		l := node.Locked
		if len(l) == 0 {
			continue
		}
		kind, _ := l["type"].(string)
		hash, _ := l["narHash"].(string)
		if hash == "" {
			return unsupported("nix_input_unpinned", "flake.lock input has no narHash")
		}
		switch kind {
		case "github", "gitlab":
			if l["rev"] == nil || l["owner"] == nil || l["repo"] == nil || l["host"] != nil {
				return unsupported("nix_input_unsupported", "only ordinary locked public GitHub/GitLab inputs are supported")
			}
		default:
			return unsupported("nix_input_unsupported", "locked input type "+kind+" requires an approved fetch adapter")
		}
	}
	if p.Run.PythonModule != "" {
		return ErrInvalidInput
	}
	for k, a := range p.Actions {
		if a.PythonModule != "" || len(a.Args) > 0 {
			return ErrInvalidInput
		}
		if a.Binary == "" {
			a.Binary = p.Run.Binary
			p.Actions[k] = a
		}
	}
	return nil
}
