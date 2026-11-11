package applicationbuild

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const cargoRecipe = `let
  pkgs = import (builtins.toPath "@NIXPKGS@") { system = "@PLATFORM@"; };
in pkgs.rustPlatform.buildRustPackage {
  pname = "loom-project-cargo";
  version = "1";
  src = builtins.path { path = ./source; name = "loom-cargo-source"; };
  cargoDeps = (pkgs.rustPlatform.importCargoLock.override {
    fetchgit = args: pkgs.fetchgit (args // { fetchSubmodules = false; });
  }) { lockFile = ./source/Cargo.lock; outputHashes = @HASHES@; };
  cargoBuildFlags = @FLAGS@;
  buildFeatures = @FEATURES@;
  buildNoDefaultFeatures = @NO_DEFAULT@;
  doCheck = false;
  auditable = false;
}
`

func nixStrings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return "[ " + strings.Join(quoted, " ") + " ]"
}

func (s *Service) realizeCargo(ctx context.Context, stage string, p Program) (string, error) {
	raw, err := os.ReadFile(filepath.Join(stage, "source/Cargo.lock"))
	if err != nil {
		return "", err
	}
	deps, err := cargoDependencies(raw, s.options)
	if err != nil {
		return "", err
	}
	hashes := map[string]string{}
	fetched := map[string]string{}
	for _, dep := range deps {
		if !strings.HasPrefix(dep.Source, "git+") {
			continue
		}
		url, rev, e := gitDependency(dep.Source, s.options)
		if e != nil {
			return "", e
		}
		if s.options.PrefetchGit == "" {
			return "", unsupported("cargo_git_fetch_unavailable", "host needs its trusted public-Git prefetch adapter")
		}
		key := url + "#" + rev
		hash := fetched[key]
		if hash == "" {
			output, e := s.options.Runner.Run(ctx, Command{Path: s.options.PrefetchGit, Args: []string{"--quiet", "--url", url, "--rev", rev}, Dir: stage})
			if e != nil {
				return "", e
			}
			var result struct {
				SHA256 string `json:"sha256"`
				Rev    string `json:"rev"`
				Path   string `json:"path"`
			}
			if json.Unmarshal(output, &result) != nil || result.Rev != rev || !regexp.MustCompile(`^[0-9a-df-np-sv-z]{52}$`).MatchString(result.SHA256) || !storePattern.MatchString(result.Path) {
				return "", ErrRealization
			}
			hash = result.SHA256
			fetched[key] = hash
		}
		name := dep.Name + "-" + dep.Version
		if existing := hashes[name]; existing != "" && existing != hash {
			return "", unsupported("cargo_git_identity_conflict", name)
		}
		hashes[name] = hash
	}
	keys := make([]string, 0, len(hashes))
	for key := range hashes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	attributes := []string{}
	for _, key := range keys {
		attributes = append(attributes, strconv.Quote(key)+" = "+strconv.Quote(hashes[key])+";")
	}
	flags := []string{"--package", p.Build.Package}
	for _, name := range selectedPrograms(p) {
		flags = append(flags, "--bin", name)
	}
	recipe := strings.NewReplacer("@NIXPKGS@", s.options.NixpkgsPath, "@PLATFORM@", s.options.Platform, "@HASHES@", "{ "+strings.Join(attributes, " ")+" }", "@FLAGS@", nixStrings(flags), "@FEATURES@", nixStrings(p.Build.Features), "@NO_DEFAULT@", strconv.FormatBool(p.Build.NoDefaultFeatures)).Replace(cargoRecipe)
	return s.buildRecipe(ctx, stage, "cargo.nix", recipe)
}
