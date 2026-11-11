package applicationbuild

import "strings"

// This recipe only copies frozen runtime code; it does not execute project
// setup.py/build hooks or introduce undeclared Python dependencies. Imports are
// isolated from the live working directory, PYTHONPATH and user site-packages.
const pythonRecipe = `let
  pkgs = import (builtins.toPath "@NIXPKGS@") { system = "@PLATFORM@"; };
  source = builtins.path { path = ./source; name = "loom-application-source"; };
  metadata = builtins.path { path = ./metadata.json; name = "loom-application-metadata.json"; };
  launcher = pkgs.writeText "loom-application-launcher" ''
    #!${pkgs.python3}/bin/python3 -I
    import os
    import json
    import re
    import runpy
    import sys

    sys.dont_write_bytecode = True
    root = os.path.dirname(os.path.dirname(os.path.realpath(__file__)))
    module = "@MODULE@"
    fixed_args = []
    if len(sys.argv) == 2:
        config = sys.argv[1]
    elif len(sys.argv) == 4 and sys.argv[1] == "--action":
        name = sys.argv[2]
        if re.fullmatch(r"[a-z][a-z0-9_-]{0,62}", name) is None:
            raise SystemExit("unknown application action")
        try:
            with open(os.path.join(root, "share", "loom", "build-metadata.json"), "rb") as source:
                raw = source.read(3 * 1024 * 1024 + 1)
            if len(raw) > 3 * 1024 * 1024:
                raise ValueError()
            action = json.loads(raw)["actions"][name]
            action_module = action["python_module"]
            fixed_args = action["args"]
            if (not isinstance(action_module, str)
                    or re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*", action_module) is None
                    or len(action_module) > 256 or action_module.split(".")[0] != module.split(".")[0]
                    or not isinstance(fixed_args, list) or len(fixed_args) > 32
                    or not isinstance(action["read_only"], bool)
                    or any(not isinstance(arg, str) or len(arg.encode("utf-8")) > 4096 or "\x00" in arg for arg in fixed_args)):
                raise ValueError()
            module = action_module
        except (OSError, ValueError, KeyError, TypeError):
            raise SystemExit("invalid application action metadata") from None
        config = sys.argv[3]
    else:
        raise SystemExit("expected one absolute resolved-config JSON file")
    if not os.path.isabs(config) or not os.path.isfile(config):
        raise SystemExit("expected one absolute resolved-config JSON file")
    sys.path.insert(0, os.path.join(root, "lib", "loom-application"))
    sys.argv = [module, config, *fixed_args]
    runpy.run_module(module, run_name="__main__", alter_sys=False)
  '';
in pkgs.runCommand "loom-project-python-application" {
  preferLocalBuild = true;
  allowSubstitutes = false;
} ''
  mkdir -p "$out/bin" "$out/lib/loom-application" "$out/share/loom"
  cp -R ${source}/. "$out/lib/loom-application/"
  cp ${metadata} "$out/share/loom/build-metadata.json"
  cp ${launcher} "$out/bin/loom-application-launcher"
  chmod 0555 "$out/bin/loom-application-launcher"
  readlink -f ${pkgs.python3.interpreter} > "$out/share/loom/runtime-executable"
''
`

func renderPython(options Options, module string) string {
	return strings.NewReplacer("@NIXPKGS@", options.NixpkgsPath, "@PLATFORM@", options.Platform, "@MODULE@", module).Replace(pythonRecipe)
}
