package applicationbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sr "loom.local/loom/internal/serviceregistry"
)

const fixtureDependency = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-python3"
const fixtureNixpkgs = "/nix/store/cccccccccccccccccccccccccccccccc-nixpkgs"
const fixtureNAR = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

type fakeRunner struct {
	t                   *testing.T
	builds, inspections int
	onBuild             func(context.Context, Command) error
	pathInfo            []byte
	inspectError        error
	commands            []Command
}

func (f *fakeRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	f.commands = append(f.commands, command)
	if slices.Contains(command.Args, "build") {
		f.builds++
		if !slices.Contains(command.Args, "--no-link") || !slices.Contains(command.Args, "--json") {
			f.t.Fatal("build must have machine output and no unmanaged GC root")
		}
		if f.onBuild != nil {
			if err := f.onBuild(ctx, command); err != nil {
				return nil, err
			}
		}
		metadata, err := os.ReadFile(filepath.Join(command.Dir, "metadata.json"))
		if err != nil {
			f.t.Fatal(err)
		}
		root := "/nix/store/" + strings.ReplaceAll(hashBytes(metadata)[7:39], "e", "d") + "-application"
		raw, _ := json.Marshal([]any{map[string]any{"outputs": map[string]string{"out": root}}})
		return raw, nil
	}
	if slices.Contains(command.Args, "eval") {
		if !slices.Contains(command.Args, "--impure") || !slices.Contains(command.Args, "--offline") || !strings.HasPrefix(command.Args[len(command.Args)-1], "builtins.readFile ") {
			f.t.Fatal("runtime metadata read must be a fixed offline expression")
		}
		return []byte(fixtureDependency + "/bin/python3.13\n"), nil
	}
	if !slices.Contains(command.Args, "path-info") || !slices.Contains(command.Args, "--recursive") || !slices.Contains(command.Args, "--offline") {
		f.t.Fatalf("unexpected command: %#v", command)
	}
	f.inspections++
	if f.inspectError != nil {
		return nil, f.inspectError
	}
	if f.pathInfo != nil {
		return f.pathInfo, nil
	}
	root := command.Args[len(command.Args)-1]
	raw, _ := json.Marshal(map[string]any{
		root:              map[string]any{"narHash": fixtureNAR, "references": []string{fixtureDependency}},
		fixtureDependency: map[string]any{"narHash": fixtureNAR, "references": []string{}},
	})
	return raw, nil
}

func fixture(t *testing.T) (*Service, Input, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "__init__.py"), "")
	writeFile(t, filepath.Join(root, "app", "main.py"), "print('runtime')\n")
	writeFile(t, filepath.Join(root, ".data", "token.json"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(root, "notes", "private.md"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(root, ".data", ".loomignore"), "[malformed\n")
	runner := &fakeRunner{t: t}
	service, err := NewService(Options{WorkDir: filepath.Join(t.TempDir(), "staging"), NixpkgsPath: fixtureNixpkgs, Platform: "x86_64-linux", Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{
		Source:     SourceIdentity{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "intake"},
		SourceRoot: root, PythonModule: "app.main", SourceFiles: []string{"app/main.py", "app/__init__.py"},
		Config:         map[string]any{"enabled": false, "limit": json.Number("9007199254740993"), "label": ""},
		DataParameters: []string{"database"}, CredentialParameters: []string{"oauth"}, DataFormat: "app-v1",
	}
	return service, input, runner
}

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotIsReadOnlyAndStable(t *testing.T) {
	s, input, runner := fixture(t)
	first, err := s.Snapshot(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 2 || first.Files[0].Path != "app/__init__.py" || !digestPattern.MatchString(first.SourceDigest) {
		t.Fatalf("snapshot: %#v", first)
	}
	slices.Reverse(input.SourceFiles)
	input.Config["label"] = "not a source input"
	second, err := s.Snapshot(t.Context(), input)
	if err != nil || hashJSON(first) != hashJSON(second) {
		t.Fatalf("unstable snapshot: %v", err)
	}
	if _, err := os.Stat(s.options.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot wrote staging: %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatal("snapshot ran a command")
	}
	writeFile(t, filepath.Join(input.SourceRoot, "app", "main.py"), "print('changed')\n")
	third, err := s.Snapshot(t.Context(), input)
	if err != nil || third.SourceDigest == first.SourceDigest {
		t.Fatalf("edit not captured: %v", err)
	}
}

func TestProducerIdentityAndInterpreterArePartOfRealization(t *testing.T) {
	s, input, runner := fixture(t)
	before, err := s.Snapshot(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Build(t.Context(), input)
	if err != nil || first.Descriptor.Executable != fixtureDependency+"/bin/python3.13" || first.Descriptor.Executable == first.Descriptor.Launcher {
		t.Fatalf("runtime interpreter was not preserved: %#v %v", first.Descriptor, err)
	}
	s.options.NixpkgsPath = fixtureDependency
	after, err := s.Snapshot(t.Context(), input)
	if err != nil || before.SourceDigest != after.SourceDigest || before.ProducerDigest == after.ProducerDigest {
		t.Fatalf("producer update did not preserve source/distinguish toolchain: %v", err)
	}
	second, err := s.Build(t.Context(), input)
	if err != nil || second.Reused || first.Descriptor.Artifact == second.Descriptor.Artifact || runner.builds != 2 {
		t.Fatalf("old producer output was reused: %v", err)
	}
}

func TestBuildCanonicalMetadataAndReuse(t *testing.T) {
	s, input, runner := fixture(t)
	snapshot, err := s.Snapshot(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedSourceDigest = snapshot.SourceDigest
	runner.onBuild = func(_ context.Context, command Command) error {
		var names []string
		err := filepath.WalkDir(filepath.Join(command.Dir, "source"), func(p string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				rel, _ := filepath.Rel(filepath.Join(command.Dir, "source"), p)
				names = append(names, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil || !slices.Equal(names, []string{"app/__init__.py", "app/main.py"}) {
			t.Fatalf("staged files: %v, %v", names, err)
		}
		raw, err := os.ReadFile(filepath.Join(command.Dir, "metadata.json"))
		if err != nil || bytes.Contains(raw, []byte("9007199254740993")) || bytes.Contains(raw, []byte("PRIVATE_SENTINEL")) {
			t.Fatalf("literal values/private content entered store metadata: %v", err)
		}
		return nil
	}
	first, err := s.Build(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || runner.builds != 1 || first.Descriptor.Source.RepositoryID != "" || first.Descriptor.SourceDigest != snapshot.SourceDigest || first.Descriptor.Source.Revision != snapshot.SourceDigest {
		t.Fatalf("unexpected output: %#v", first.Descriptor)
	}
	if first.Descriptor.ClosureDigest != sr.ApplicationClosureDigest(first.Descriptor.Closure) || first.Descriptor.ConfigurationDigest != sr.ApplicationConfigurationSchemaDigest(first.Descriptor.Configuration) || first.DescriptorDigest != hashBytes(first.DescriptorJSON) || first.ManifestDigest != hashBytes(first.ManifestJSON) {
		t.Fatal("canonical digests differ from registry helpers")
	}
	if *first.Manifest.Configuration.Values["limit"].Integer != 9007199254740993 || *first.Manifest.Configuration.Values["enabled"].Boolean || *first.Manifest.Configuration.Values["label"].String != "" {
		t.Fatal("literal values lost exact types")
	}
	if first.Descriptor.Configuration.Parameters["oauth"].Type != sr.ApplicationCredentialReference || first.Descriptor.Configuration.Parameters["database"].Type != sr.ApplicationDataReference || len(first.Manifest.Configuration.Values) != 3 {
		t.Fatal("binding names were not separated from literal values")
	}
	second, err := s.Build(t.Context(), input)
	if err != nil || !second.Reused || runner.builds != 1 || !bytes.Equal(first.ManifestJSON, second.ManifestJSON) || !bytes.Equal(first.DescriptorJSON, second.DescriptorJSON) {
		t.Fatalf("unchanged build not reused: %v", err)
	}
	// Output mutation must not mutate cache custody.
	first.Descriptor.Closure[0].NARHash = "bad"
	third, err := s.Build(t.Context(), input)
	if err != nil || !third.Reused || runner.builds != 1 {
		t.Fatalf("cache aliased caller result: %v", err)
	}
	input.Config["label"] = "new effective config"
	fourth, err := s.Build(t.Context(), input)
	if err != nil || !fourth.Reused || fourth.ManifestDigest == second.ManifestDigest || fourth.DescriptorDigest != second.DescriptorDigest {
		t.Fatalf("literal configuration change unnecessarily rebuilt code: %v", err)
	}
	entries, err := os.ReadDir(s.options.WorkDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging not cleaned: %v", err)
	}
}

func TestExpectedDigestAndFrozenBytes(t *testing.T) {
	s, input, runner := fixture(t)
	snapshot, _ := s.Snapshot(t.Context(), input)
	input.ExpectedSourceDigest = snapshot.SourceDigest
	writeFile(t, filepath.Join(input.SourceRoot, "app", "main.py"), "changed before freeze\n")
	if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrSourceChanged) || runner.builds != 0 {
		t.Fatalf("stale plan accepted: %v", err)
	}
	input.ExpectedSourceDigest = ""
	snapshot, _ = s.Snapshot(t.Context(), input)
	input.ExpectedSourceDigest = snapshot.SourceDigest
	runner.onBuild = func(_ context.Context, command Command) error {
		writeFile(t, filepath.Join(input.SourceRoot, "app", "main.py"), "changed after freeze\n")
		staged, err := os.ReadFile(filepath.Join(command.Dir, "source", "app", "main.py"))
		if err != nil || string(staged) != "changed before freeze\n" {
			t.Fatal("build read live code after freeze")
		}
		return nil
	}
	output, err := s.Build(t.Context(), input)
	if err != nil || output.Descriptor.SourceDigest != snapshot.SourceDigest {
		t.Fatalf("frozen identity changed: %v", err)
	}
}

func TestBuildChangeToolchainAndCollectedReuse(t *testing.T) {
	s, input, runner := fixture(t)
	first, err := s.Build(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(input.SourceRoot, "app", "main.py"), "print('new runtime')\n")
	second, err := s.Build(t.Context(), input)
	if err != nil || second.Reused || second.Descriptor.Artifact.Digest == first.Descriptor.Artifact.Digest || second.Descriptor.StoreRoot == first.Descriptor.StoreRoot {
		t.Fatalf("code edit did not change artifact: %v", err)
	}
	s.options.NixpkgsPath = "/nix/store/dddddddddddddddddddddddddddddddd-nixpkgs"
	third, err := s.Build(t.Context(), input)
	if err != nil || third.Reused || third.Descriptor.ToolchainDigest == second.Descriptor.ToolchainDigest {
		t.Fatalf("toolchain change reused: %v", err)
	}
	runner.inspectError = errors.New("collected")
	runner.onBuild = func(context.Context, Command) error { runner.inspectError = nil; return nil }
	fourth, err := s.Build(t.Context(), input)
	if err != nil || fourth.Reused || runner.builds != 4 {
		t.Fatalf("collected output reused: %v", err)
	}
}

func TestCancellationNeverCachesSuccess(t *testing.T) {
	s, input, runner := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	runner.onBuild = func(ctx context.Context, _ Command) error { cancel(); <-ctx.Done(); return ctx.Err() }
	if _, err := s.Build(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if len(s.cache) != 0 {
		t.Fatal("cancelled build cached success")
	}
	entries, err := os.ReadDir(s.options.WorkDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled staging retained: %v", err)
	}
	runner.onBuild = nil
	if _, err := s.Build(t.Context(), input); err != nil || runner.builds != 2 {
		t.Fatalf("cancelled result reused: %v", err)
	}
}

func TestWaitingBuildAndRunnerObserveDeadline(t *testing.T) {
	s, input, _ := fixture(t)
	s.gate <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Build(ctx, input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting build ignored deadline: %v", err)
	}
	<-s.gate
	s.options.BuildTimeout = 10 * time.Millisecond
	s.options.Runner = CommandRunnerFunc(func(ctx context.Context, _ Command) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() })
	if _, err := s.Build(t.Context(), input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runner ignored deadline: %v", err)
	}
}

func TestBuildBindsCompleteActionsToDigestAndMetadata(t *testing.T) {
	s, input, runner := fixture(t)
	writeFile(t, filepath.Join(input.SourceRoot, "app", "bootstrap.py"), "print('bootstrap')\n")
	input.SourceFiles = append(input.SourceFiles, "app/bootstrap.py")
	input.Actions = map[string]Action{
		"bootstrap": {PythonModule: "app.bootstrap", Args: []string{"--mode", "warm"}},
		"inspect":   {PythonModule: "app.main", ReadOnly: true},
	}
	runner.onBuild = func(_ context.Context, command Command) error {
		raw, err := os.ReadFile(filepath.Join(command.Dir, "metadata.json"))
		if err != nil {
			return err
		}
		var metadata struct {
			Actions map[string]Action `json:"actions"`
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
		if hashJSON(normalizedActions(input.Actions)) != hashJSON(metadata.Actions) {
			t.Fatal("full action definition not frozen in metadata")
		}
		return nil
	}
	first, err := s.Build(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Descriptor.Actions["bootstrap"].ReadOnly || !first.Descriptor.Actions["inspect"].ReadOnly {
		t.Fatal("action policy projection lost")
	}
	second, err := s.Build(t.Context(), input)
	if err != nil || !second.Reused || second.DescriptorDigest != first.DescriptorDigest || runner.builds != 1 {
		t.Fatalf("unchanged actions rebuilt: %v", err)
	}
	previous := second
	for _, action := range []Action{
		{PythonModule: "app.bootstrap", Args: []string{"--mode", "cold"}},
		{PythonModule: "app.main", Args: []string{"--mode", "cold"}},
		{PythonModule: "app.main", Args: []string{"--mode", "cold"}, ReadOnly: true},
	} {
		input.Actions["bootstrap"] = action
		current, err := s.Build(t.Context(), input)
		if err != nil || current.Reused || current.Descriptor.BuildDigest == previous.Descriptor.BuildDigest || current.Descriptor.Artifact.Digest == previous.Descriptor.Artifact.Digest || current.Descriptor.SourceDigest != previous.Descriptor.SourceDigest {
			t.Fatalf("action input not bound to build: %v", err)
		}
		previous = current
	}
}

func TestSnapshotRejectsUndeclaredActionCodeAndArguments(t *testing.T) {
	for _, actions := range []map[string]Action{
		{"BadName": {PythonModule: "app.main"}},
		{"other": {PythonModule: "foreign.main"}},
		{"shell": {PythonModule: "app.main;command"}},
		{"missing": {PythonModule: "app.missing"}},
		{"overflow": {PythonModule: "app.main", Args: make([]string, 33)}},
		{"overflow": {PythonModule: "app.main", Args: []string{strings.Repeat("x", 4097)}}},
		{"nul": {PythonModule: "app.main", Args: []string{"nul\x00arg"}}},
	} {
		s, input, runner := fixture(t)
		input.SourceFiles = nil
		input.Actions = actions
		if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid action accepted: %v", err)
		}
		if len(runner.commands) != 0 {
			t.Fatal("invalid action snapshot invoked command")
		}
		if _, err := os.Stat(s.options.WorkDir); !os.IsNotExist(err) {
			t.Fatal("invalid action snapshot wrote staging")
		}
	}
}
