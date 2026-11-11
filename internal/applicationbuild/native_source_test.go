package applicationbuild

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func cargoFixture(t *testing.T) (*Service, Input, *fakeRunner) {
	t.Helper()
	s, in, runner := fixture(t)
	in.PythonModule, in.SourceFiles = "", nil
	in.Build = &BuildSpec{Kind: "cargo"}
	in.SourceRoot = t.TempDir()
	s.options.RustVersion = "1.95.0"
	writeFile(t, filepath.Join(in.SourceRoot, "Cargo.toml"), `[workspace]
members = ["app", "helper"]
default-members = ["app"]
resolver = "2"
`)
	writeFile(t, filepath.Join(in.SourceRoot, "app/Cargo.toml"), `[package]
name = "fixture"
version = "0.1.0"
edition = "2021"
default-run = "fixture"
[dependencies]
helper = { path = "../helper" }
`)
	writeFile(t, filepath.Join(in.SourceRoot, "app/src/main.rs"), `fn main() { println!("{}", include_str!("../assets/message.txt")); }`)
	writeFile(t, filepath.Join(in.SourceRoot, "app/src/bin/status.rs"), `fn main() { println!("ready"); }`)
	writeFile(t, filepath.Join(in.SourceRoot, "app/assets/message.txt"), "asset")
	writeFile(t, filepath.Join(in.SourceRoot, "helper/Cargo.toml"), "[package]\nname='helper'\nversion='0.1.0'\n")
	writeFile(t, filepath.Join(in.SourceRoot, "helper/src/lib.rs"), "pub fn value() -> u8 { 1 }")
	writeFile(t, filepath.Join(in.SourceRoot, "Cargo.lock"), "version = 4\n[[package]]\nname='fixture'\nversion='0.1.0'\n[[package]]\nname='helper'\nversion='0.1.0'\n")
	in.Actions = map[string]Action{"status": {Binary: "status", ReadOnly: true}}
	return s, in, runner
}

func TestCargoCaptureFreezeAndPureInspection(t *testing.T) {
	s, in, runner := cargoFixture(t)
	writeFile(t, filepath.Join(in.SourceRoot, ".loomignore"), "ignored/\n")
	writeFile(t, filepath.Join(in.SourceRoot, "ignored/unread.txt"), "not captured")
	writeFile(t, filepath.Join(in.SourceRoot, "target/debug/output"), "not captured")
	writeFile(t, filepath.Join(in.SourceRoot, "private/key.txt"), "not captured")
	first, err := s.Snapshot(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Program.Run.Binary != "fixture" || first.Program.Build.Package != "fixture" || first.Program.Actions["status"].Binary != "status" {
		t.Fatalf("selectors: %+v", first.Program)
	}
	names := []string{}
	for _, file := range first.Files {
		names = append(names, file.Path)
	}
	if !slices.Contains(names, "app/assets/message.txt") || !slices.Contains(names, "helper/src/lib.rs") || slices.Contains(names, "ignored/unread.txt") || slices.Contains(names, "target/debug/output") {
		t.Fatalf("files: %v", names)
	}
	if len(runner.commands) != 0 {
		t.Fatal("check executed a command")
	}
	if _, err := os.Stat(s.options.WorkDir); !os.IsNotExist(err) {
		t.Fatal("check wrote staging")
	}
	copy := filepath.Join(t.TempDir(), "frozen")
	if err := s.Freeze(t.Context(), in, copy, first); err != nil {
		t.Fatal(err)
	}
	frozen := in
	frozen.SourceRoot = copy
	second, err := s.Snapshot(t.Context(), frozen)
	if err != nil || hashJSON(first) != hashJSON(second) {
		t.Fatalf("freeze mismatch: %v", err)
	}
	in.ExpectedSourceDigest = first.SourceDigest
	writeFile(t, filepath.Join(in.SourceRoot, "app/assets/message.txt"), "changed")
	if _, err := s.Snapshot(t.Context(), in); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed source: %v", err)
	}
}

func TestCargoRejectsMissingLockAndOutsideDependency(t *testing.T) {
	s, in, _ := cargoFixture(t)
	writeFile(t, filepath.Join(in.SourceRoot, "helper/Cargo.toml"), "[package]\nname='helper'\nversion='0.1.0'\n[dependencies]\nother={path='../../outside'}\n")
	_, err := s.Snapshot(t.Context(), in)
	var diagnostic *Diagnostic
	if !errors.As(err, &diagnostic) || diagnostic.Code != "cargo_dependency_outside_source" {
		t.Fatalf("outside: %v", err)
	}
	if err := os.Remove(filepath.Join(in.SourceRoot, "Cargo.lock")); err != nil {
		t.Fatal(err)
	}
	_, err = s.Snapshot(t.Context(), in)
	if !errors.As(err, &diagnostic) || diagnostic.Code != "cargo_lock_required" {
		t.Fatalf("lock: %v", err)
	}
}

func TestExplicitPythonCapturePreservesLegacySourceDigest(t *testing.T) {
	s, in, _ := fixture(t)
	legacy, err := s.Snapshot(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Build = &BuildSpec{Kind: "python"}
	in.Run = &RunSpec{PythonModule: in.PythonModule, Args: []Argument{{ConfigFile: true}}}
	in.PythonModule = ""
	modern, err := s.Snapshot(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if modern.SourceDigest != legacy.SourceDigest || modern.ProducerDigest == legacy.ProducerDigest || modern.Program.Run.PythonModule != "app.main" {
		t.Fatal("explicit ABI changes source bytes or reuses old producer")
	}
}

func TestCargoLockRejectsVendorCollisionsAndExpressionInjection(t *testing.T) {
	raw := []byte("version=4\n[[package]]\nname='itoa'\nversion='1.0.18'\nsource='registry+https://github.com/rust-lang/crates.io-index'\nchecksum='" + strings.Repeat("a", 64) + "'\n[[package]]\nname='itoa'\nversion='1.0.18'\nsource='git+https://github.com/dtolnay/itoa#" + strings.Repeat("a", 40) + "'\n")
	_, err := cargoDependencies(raw, Options{})
	var diagnostic *Diagnostic
	if !errors.As(err, &diagnostic) || diagnostic.Code != "cargo_vendor_identity_conflict" {
		t.Fatalf("collision: %v", err)
	}
	raw = []byte("version=4\n[[package]]\nname='itoa'\nversion='${builtins.abort}'\n")
	if _, err := cargoDependencies(raw, Options{}); err == nil {
		t.Fatal("version could enter generated Nix expression")
	}
}

func TestNativeProducerKeepsUnrelatedCompilerChangesOut(t *testing.T) {
	a := Options{RustVersion: "1.95.0"}
	b := a
	b.RustVersion = "1.96.0"
	if nativeProducerDigest(a, "cargo") == nativeProducerDigest(b, "cargo") {
		t.Fatal("Rust change not fenced")
	}
	for _, kind := range []string{"python", "nix"} {
		if nativeProducerDigest(a, kind) != nativeProducerDigest(b, kind) {
			t.Fatal("unrelated Rust change invalidates " + kind)
		}
	}
}

func TestFreezeFailureDoesNotPublishPartialCustody(t *testing.T) {
	s, in, _ := cargoFixture(t)
	expected, err := s.Snapshot(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(in.SourceRoot, "app/assets/message.txt"), "changed")
	destination := filepath.Join(t.TempDir(), "snapshot")
	if err := s.Freeze(t.Context(), in, destination, expected); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("expected fence: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("partial snapshot published")
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil || len(entries) != 0 {
		t.Fatal("abandoned capture staging")
	}
}
