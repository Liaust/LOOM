package applicationbuild

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPythonLauncherUsesFrozenModuleAndResolvedConfig(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root, launcher := pythonLauncherFixture(t)
	writeFile(t, filepath.Join(root, "lib", "loom-application", "app", "main.py"), `import json, sys
with open(sys.argv[1]) as config:
    assert json.load(config)["answer"] == 42
assert sys.argv[0] == "app.main"
assert len(sys.argv) == 2
print("frozen-runtime")
`)
	config := filepath.Join(t.TempDir(), "resolved-config.json")
	writeFile(t, config, `{"answer":42}`)
	shadow := t.TempDir()
	writeFile(t, filepath.Join(shadow, "app", "main.py"), `raise RuntimeError("live workspace imported")`)
	command := exec.CommandContext(t.Context(), python, "-I", launcher, config)
	command.Dir = shadow
	command.Env = append(os.Environ(), "PYTHONPATH="+shadow)
	raw, err := command.CombinedOutput()
	if err != nil || string(raw) != "frozen-runtime\n" {
		t.Fatalf("launcher: %s %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "loom-application", "app", "__pycache__")); !os.IsNotExist(err) {
		t.Fatalf("launcher wrote cache: %v", err)
	}
	for _, args := range [][]string{{}, {"relative.json"}, {config, "extra"}, {filepath.Join(t.TempDir(), "missing.json")}} {
		command := exec.CommandContext(t.Context(), python, append([]string{"-I", launcher}, args...)...)
		if err := command.Run(); err == nil {
			t.Fatalf("invalid launcher arguments accepted: %v", args)
		}
	}
}

func pythonLauncherFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	// Exercise the generated launcher body without a Nix realization.
	_, body, ok := strings.Cut(pythonRecipe, "  launcher = pkgs.writeText \"loom-application-launcher\" ''\n")
	if !ok {
		t.Fatal("missing launcher")
	}
	body, _, ok = strings.Cut(body, "\n  '';\n")
	if !ok {
		t.Fatal("missing launcher terminator")
	}
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimPrefix(lines[i], "    ")
	}
	script := strings.ReplaceAll(strings.Join(lines[1:], "\n"), "@MODULE@", "app.main")
	launcher := filepath.Join(root, "bin", "loom-application-launcher")
	writeFile(t, launcher, script)
	writeFile(t, filepath.Join(root, "lib", "loom-application", "app", "__init__.py"), "")
	return root, launcher
}

func TestPythonLauncherUsesOnlyDeclaredActionMetadata(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root, launcher := pythonLauncherFixture(t)
	writeFile(t, filepath.Join(root, "lib", "loom-application", "app", "bootstrap.py"), `import json, sys
with open(sys.argv[1]) as config:
    assert json.load(config)["answer"] == 42
print(json.dumps(sys.argv))
`)
	actions := map[string]Action{"bootstrap": {PythonModule: "app.bootstrap", Args: []string{"--mode", "warm", "literal $VALUE; not a command"}, ReadOnly: false}}
	raw, _ := json.Marshal(map[string]any{"actions": actions})
	metadata := filepath.Join(root, "share", "loom", "build-metadata.json")
	writeFile(t, metadata, string(raw))
	config := filepath.Join(t.TempDir(), "resolved-config.json")
	writeFile(t, config, `{"answer":42}`)
	shadow := t.TempDir()
	writeFile(t, filepath.Join(shadow, "app", "bootstrap.py"), `raise RuntimeError("live workspace imported")`)
	writeFile(t, filepath.Join(shadow, "share", "loom", "build-metadata.json"), `{"actions":{}}`)
	command := exec.CommandContext(t.Context(), python, "-I", launcher, "--action", "bootstrap", config)
	command.Dir = shadow
	command.Env = append(os.Environ(), "PYTHONPATH="+shadow)
	raw, err = command.CombinedOutput()
	var argv []string
	if err != nil || json.Unmarshal(raw, &argv) != nil || !slices.Equal(argv, []string{"app.bootstrap", config, "--mode", "warm", "literal $VALUE; not a command"}) {
		t.Fatalf("action argv: %s %v", raw, err)
	}
	for _, args := range [][]string{
		{"--action", "missing", config}, {"--action", "app.bootstrap", config},
		{"--action", "bootstrap", config, "injected"}, {"--action", "bootstrap", "relative.json"},
		{"--module", "app.bootstrap", config}, {"--action", "bootstrap"},
	} {
		command := exec.CommandContext(t.Context(), python, append([]string{"-I", launcher}, args...)...)
		if err := command.Run(); err == nil {
			t.Fatalf("caller-controlled action accepted: %v", args)
		}
	}
	for _, definition := range []any{
		map[string]any{"python_module": "os", "args": []string{}, "read_only": false},
		map[string]any{"python_module": "app.bad;command", "args": []string{}, "read_only": false},
		map[string]any{"python_module": "app.bootstrap", "args": "not-an-array", "read_only": false},
		map[string]any{"python_module": "app.bootstrap", "args": []string{strings.Repeat("x", 4097)}, "read_only": false},
		map[string]any{"python_module": "app.bootstrap", "args": []string{"nul\x00arg"}, "read_only": false},
		map[string]any{"python_module": "app.bootstrap", "args": []string{}, "read_only": 1},
		nil,
	} {
		raw, _ := json.Marshal(map[string]any{"actions": map[string]any{"bootstrap": definition}})
		writeFile(t, metadata, string(raw))
		command := exec.CommandContext(t.Context(), python, "-I", launcher, "--action", "bootstrap", config)
		if err := command.Run(); err == nil {
			t.Fatalf("invalid action metadata accepted: %#v", definition)
		}
	}
}
