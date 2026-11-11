// Package applicationbuild produces Python application artifacts. It does not
// register, publish, pin, install or start them; those effects belong to callers.
package applicationbuild

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	sr "loom.local/loom/internal/serviceregistry"
)

var (
	ErrInvalidInput  = errors.New("application build: invalid input")
	ErrUnsafeSource  = errors.New("application build: unsafe source input")
	ErrSourceChanged = errors.New("application build: source changed")
	ErrCommand       = errors.New("application build: command failed")
	ErrRealization   = errors.New("application build: invalid realization")
)

// SourceIdentity accepts a project without manufacturing repository membership.
// Revision is optional; absent history is represented by the captured digest.
type SourceIdentity struct {
	ProjectID    string `json:"project_id"`
	Resource     string `json:"resource"`
	RepositoryID string `json:"repository_id,omitempty"`
	Revision     string `json:"revision"`
}

type Input struct {
	Build        *BuildSpec `json:"build,omitempty"`
	Run          *RunSpec   `json:"run,omitempty"`
	NixAttribute string     `json:"nix_attribute,omitempty"`
	Source       SourceIdentity
	SourceRoot   string
	PythonModule string
	// SourceFiles optionally supplies an exact runtime allowlist relative to
	// SourceRoot (the Python import root). Without it, only the selected top-level
	// package's safe .py files (or the single module file) are discovered.
	SourceFiles          []string
	PackageFiles         []string
	ExpectedSourceDigest string
	ConfigurationSchema  string
	// Config accepts only nonsecret strings, booleans and exact signed integers
	// (including json.Number). Floats, collections and credential values are not
	// supported. Binding names must not overlap literal keys or one another.
	Config                map[string]any
	DataParameters        []string
	CredentialParameters  []string
	DataFormat            string
	CompatibleDataFormats []string
	Actions               map[string]Action
}

// Action selects another module in the same frozen package and fixed, public
// arguments. ReadOnly is execution policy metadata, not an authority grant.
type Action struct {
	Binary       string     `json:"binary,omitempty"`
	Arguments    []Argument `json:"arguments,omitempty"`
	PythonModule string     `json:"python_module"`
	Args         []string   `json:"args"`
	ReadOnly     bool       `json:"read_only"`
}

// Options is trusted backend configuration, never a project-controlled command.
// NixpkgsPath must be a realized, pinned store root, not <nixpkgs> or a channel.
type Options struct {
	WorkDir        string
	NixpkgsPath    string
	Platform       string
	NixBinary      string
	Runner         CommandRunner
	MaxSourceBytes int64
	MaxSourceFiles int
	CacheEntries   int
	BuildTimeout   time.Duration
	LauncherPath   string
	RustVersion    string
	PrefetchGit    string
	FetchGitHosts  []string
}

// Service serializes its bounded builds and keeps a small, unpinned in-memory
// reuse cache. Existing job/artifact owners retain successful outputs as needed.
type Service struct {
	options Options
	gate    chan struct{}
	cache   map[string]realization
	order   []string
}

var (
	projectPattern    = regexp.MustCompile(`^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	repositoryPattern = regexp.MustCompile(`^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	keyPattern        = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	refPattern        = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	revisionPattern   = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64}|sha256:[0-9a-f]{64})$`)
	formatPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)
	storePattern      = regexp.MustCompile(`^/nix/store/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._?=-]+$`)
	modulePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
)

func NewService(options Options) (*Service, error) {
	if !storePattern.MatchString(options.NixpkgsPath) || (options.Platform != "x86_64-linux" && options.Platform != "aarch64-linux") || !filepath.IsAbs(options.WorkDir) {
		return nil, ErrInvalidInput
	}
	if options.NixBinary == "" {
		options.NixBinary = "nix"
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.MaxSourceBytes == 0 {
		options.MaxSourceBytes = 32 * 1024 * 1024
	}
	if options.MaxSourceFiles == 0 {
		options.MaxSourceFiles = 2048
	}
	if options.CacheEntries == 0 {
		options.CacheEntries = 16
	}
	if options.BuildTimeout == 0 {
		options.BuildTimeout = 15 * time.Minute
	}
	if options.MaxSourceBytes < 1 || options.MaxSourceFiles < 1 || options.CacheEntries < 1 || options.BuildTimeout < 0 {
		return nil, ErrInvalidInput
	}
	return &Service{options: options, gate: make(chan struct{}, 1), cache: map[string]realization{}}, nil
}

func (s *Service) Build(ctx context.Context, input Input) (Output, error) {
	if !legacyInput(input) {
		return s.buildProgram(ctx, input)
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.BuildTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return Output{}, ctx.Err()
	}
	if err := validateInput(input, s.options); err != nil {
		return Output{}, err
	}
	configuration, literals, data, credentials, err := configurationMetadata(input)
	if err != nil {
		return Output{}, err
	}
	if err := os.MkdirAll(s.options.WorkDir, 0700); err != nil {
		return Output{}, errors.New("application build: staging unavailable")
	}
	stage, err := os.MkdirTemp(s.options.WorkDir, "python-")
	if err != nil {
		return Output{}, errors.New("application build: staging unavailable")
	}
	defer os.RemoveAll(stage)
	files, sourceDigest, err := freezeSource(ctx, stage, input, s.options)
	if err != nil {
		return Output{}, err
	}
	if input.ExpectedSourceDigest != "" && input.ExpectedSourceDigest != sourceDigest {
		return Output{}, ErrSourceChanged
	}
	identity := input.Source
	if identity.Revision == "" {
		identity.Revision = sourceDigest
	}
	formats := sortedUnique(input.CompatibleDataFormats)
	dataFormat := input.DataFormat
	if dataFormat == "" {
		dataFormat = "source-v1"
	}
	toolchainDigest := producerDigest(s.options)
	actions := normalizedActions(input.Actions)
	buildDigest := hashJSON(struct {
		Source, Toolchain, Module string
		Configuration             sr.ApplicationConfigurationSchema
		DataFormat                string
		Compatible                []string
		Actions                   map[string]Action
	}{sourceDigest, toolchainDigest, input.PythonModule, configuration, dataFormat, formats, actions})
	packageMetadata := struct {
		BuildDigest   string                            `json:"build_digest"`
		Configuration sr.ApplicationConfigurationSchema `json:"configuration"`
		DataFormat    string                            `json:"data_format"`
		Compatible    []string                          `json:"compatible_data_formats"`
		Actions       map[string]Action                 `json:"actions"`
	}{buildDigest, configuration, dataFormat, formats, actions}
	metadata, _ := json.Marshal(packageMetadata)
	if err := os.WriteFile(filepath.Join(stage, "metadata.json"), metadata, 0400); err != nil {
		return Output{}, errors.New("application build: staging unavailable")
	}
	if err := os.WriteFile(filepath.Join(stage, "build.nix"), []byte(renderPython(s.options, input.PythonModule)), 0400); err != nil {
		return Output{}, errors.New("application build: staging unavailable")
	}
	actual, reused := s.cache[buildDigest]
	if reused {
		var current realization
		current, err = s.inspect(ctx, actual.StoreRoot)
		if err == nil && hashJSON(current) != hashJSON(actual) {
			return Output{}, ErrRealization
		}
		if err != nil {
			if ctx.Err() != nil {
				return Output{}, ctx.Err()
			}
			if errors.Is(err, ErrRealization) {
				return Output{}, err
			}
			// The artifact may have been collected: realize it again, never return
			// stale success or create a permanent GC root owned by this helper.
			reused = false
		}
	}
	if !reused {
		actual, err = s.realize(ctx, stage)
		if err != nil {
			return Output{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	output := assemble(identity, input.PythonModule, files, sourceDigest, toolchainDigest, buildDigest, actual, configuration, literals, data, credentials, dataFormat, formats, s.options.Platform, actions)
	output.Reused = reused
	if _, exists := s.cache[buildDigest]; !exists {
		s.order = append(s.order, buildDigest)
	}
	s.cache[buildDigest] = actual
	for len(s.order) > s.options.CacheEntries {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
	return output, nil
}

func producerDigest(options Options) string {
	return hashJSON(struct{ Producer, Nixpkgs, Platform, Recipe string }{
		"python-stdlib-v2", options.NixpkgsPath, options.Platform, pythonRecipe,
	})
}

func validateInput(in Input, options Options) error {
	if !projectPattern.MatchString(in.Source.ProjectID) || !keyPattern.MatchString(in.Source.Resource) || (in.Source.RepositoryID != "" && !repositoryPattern.MatchString(in.Source.RepositoryID)) || (in.Source.Revision != "" && !revisionPattern.MatchString(in.Source.Revision)) || !filepath.IsAbs(in.SourceRoot) || !modulePattern.MatchString(in.PythonModule) || len(in.PythonModule) > 256 || len(in.SourceFiles) > options.MaxSourceFiles || (in.ExpectedSourceDigest != "" && !digestPattern.MatchString(in.ExpectedSourceDigest)) || len(in.CompatibleDataFormats) > 32 || (in.DataFormat != "" && !formatPattern.MatchString(in.DataFormat)) {
		return ErrInvalidInput
	}
	for _, f := range in.CompatibleDataFormats {
		if !formatPattern.MatchString(f) {
			return ErrInvalidInput
		}
	}
	if info, err := os.Lstat(in.SourceRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeSource
	}
	seen := map[string]bool{}
	for _, name := range in.SourceFiles {
		if !safeRuntimePath(name) || seen[name] {
			return ErrUnsafeSource
		}
		seen[name] = true
	}
	if len(in.PackageFiles) > 64 || len(in.SourceFiles)+len(in.PackageFiles) > options.MaxSourceFiles {
		return ErrInvalidInput
	}
	for _, name := range in.PackageFiles {
		if !safePackagePath(name) || !strings.HasPrefix(name, strings.Split(in.PythonModule, ".")[0]+"/") || seen[name] {
			return ErrUnsafeSource
		}
		seen[name] = true
	}
	if len(in.SourceFiles) != 0 && !hasEntrypoint(in.PythonModule, in.SourceFiles) {
		return ErrInvalidInput
	}
	if len(in.Actions) > 16 {
		return ErrInvalidInput
	}
	top, _, _ := strings.Cut(in.PythonModule, ".")
	for name, action := range in.Actions {
		actionTop, _, _ := strings.Cut(action.PythonModule, ".")
		if action.Binary != "" || len(action.Arguments) > 0 || !keyPattern.MatchString(name) || !modulePattern.MatchString(action.PythonModule) || len(action.PythonModule) > 256 || actionTop != top || len(action.Args) > 32 || (len(in.SourceFiles) > 0 && !hasEntrypoint(action.PythonModule, in.SourceFiles)) {
			return ErrInvalidInput
		}
		for _, arg := range action.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return ErrInvalidInput
			}
		}
	}
	return nil
}

func normalizedActions(actions map[string]Action) map[string]Action {
	result := make(map[string]Action, len(actions))
	for name, action := range actions {
		action.Args = append([]string{}, action.Args...)
		result[name] = action
	}
	return result
}

func hasEntrypoint(module string, names []string) bool {
	module = strings.ReplaceAll(module, ".", "/")
	for _, name := range names {
		if name == module+".py" || name == module+"/__main__.py" {
			return true
		}
	}
	return false
}
