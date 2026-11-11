package applicationbuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// The explicit ABI is separate: retained legacy recipes and hashes never change.
const pythonProgramRecipe = `let
  pkgs = import (builtins.toPath "@NIXPKGS@") { system = "@PLATFORM@"; };
  launcher = builtins.storePath "@LAUNCHER@";
  input = builtins.fromJSON (builtins.readFile ./metadata.json);
  source = builtins.path { path = ./source; name = "loom-python-source"; };
  bootstrap = pkgs.writeText "loom-python-entry.py" ''
    import os, runpy, sys
    sys.dont_write_bytecode = True
    sys.path.insert(0, sys.argv.pop(1))
    module = sys.argv.pop(1)
    sys.argv[0] = module
    runpy.run_module(module, run_name="__main__", alter_sys=False)
  '';
  metadata = pkgs.writeText "loom-python-launch.json" (builtins.toJSON {
    schema = "application.launch.v1";
    programs = { python = "${pkgs.python3.interpreter}"; };
    python_root = "${source}";
    python_bootstrap = "${bootstrap}";
    inherit (input) run actions;
  });
in pkgs.runCommand "loom-explicit-python-application" { preferLocalBuild = true; allowSubstitutes = false; } ''
  mkdir -p "$out/bin" "$out/share/loom"
  cp ${launcher}/bin/loom-application-launcher "$out/bin/loom-application-launcher"
  cp ${metadata} "$out/share/loom/launch.json"
  readlink -f ${pkgs.python3.interpreter} > "$out/share/loom/runtime-executable"
''
`

func (s *Service) capturePythonProgram(ctx context.Context, in Input, p Program, sink func(string, []byte) error) (Snapshot, error) {
	legacy := in
	legacy.Build, legacy.Run, legacy.NixAttribute = nil, nil, ""
	legacy.PythonModule = p.Run.PythonModule
	legacy.Actions = map[string]Action{}
	for name, a := range p.Actions {
		if a.PythonModule == "" {
			a.PythonModule = p.Run.PythonModule
		}
		p.Actions[name] = a
		legacy.Actions[name] = Action{PythonModule: a.PythonModule, ReadOnly: a.ReadOnly}
	}
	if err := validateInput(legacy, s.options); err != nil {
		return Snapshot{}, err
	}
	got, err := captureSource(ctx, legacy, s.options, sink)
	if err != nil {
		return Snapshot{}, err
	}
	got.Program = &p
	got.ProducerDigest = nativeProducerDigest(s.options, "python")
	if in.ExpectedSourceDigest != "" && got.SourceDigest != in.ExpectedSourceDigest {
		return Snapshot{}, ErrSourceChanged
	}
	if sink != nil {
		root, err := os.OpenRoot(in.SourceRoot)
		if err != nil {
			return Snapshot{}, err
		}
		defer root.Close()
		names := make([]string, 0, len(got.Files))
		for _, file := range got.Files {
			names = append(names, file.Path)
		}
		policies, err := sourcePolicies(ctx, root, names)
		if err != nil || hashJSON(policies.files) != got.PolicyDigest {
			return Snapshot{}, ErrSourceChanged
		}
		for _, policy := range policies.files {
			raw, err := readSourceFile(ctx, root, policy.Path, 1024*1024)
			if err != nil || hashBytes(raw) != policy.Digest {
				return Snapshot{}, ErrSourceChanged
			}
			if err := sink(policy.Path, raw); err != nil {
				return Snapshot{}, err
			}
		}
	}
	return got, nil
}

func (s *Service) realizePythonProgram(ctx context.Context, stage string) (realization, error) {
	recipe := strings.NewReplacer("@NIXPKGS@", s.options.NixpkgsPath, "@PLATFORM@", s.options.Platform, "@LAUNCHER@", s.options.LauncherPath).Replace(pythonProgramRecipe)
	if err := os.WriteFile(filepath.Join(stage, "build.nix"), []byte(recipe), 0400); err != nil {
		return realization{}, err
	}
	return s.realize(ctx, stage)
}
