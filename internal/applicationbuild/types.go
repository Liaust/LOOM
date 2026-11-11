package applicationbuild

import (
	"errors"
	"slices"

	pc "loom.local/loom/internal/projectcontracts"
)

var ErrUnsupported = errors.New("application build: unsupported producer requirement")

type BuildSpec = pc.ApplicationBuild
type RunSpec = pc.ApplicationRun
type Argument = pc.ApplicationArgument

// Program is frozen producer selection, not runtime credentials or authority.
type Program struct {
	Build   BuildSpec         `json:"build"`
	Run     RunSpec           `json:"run"`
	Actions map[string]Action `json:"actions"`
}

type Diagnostic struct{ Code, Detail string }

func (e *Diagnostic) Error() string         { return "application build: " + e.Code + ": " + e.Detail }
func (e *Diagnostic) Unwrap() error         { return ErrUnsupported }
func unsupported(code, detail string) error { return &Diagnostic{Code: code, Detail: detail} }

func legacyInput(in Input) bool {
	return in.Build == nil && in.Run == nil && in.NixAttribute == "" && in.PythonModule != ""
}

func normalizeProgram(in Input) (Program, error) {
	decl := pc.ApplicationDeclaration{Source: &pc.ApplicationSource{Path: ".", Build: in.Build,
		PythonModule: in.PythonModule, NixAttribute: in.NixAttribute}, Run: in.Run, Actions: map[string]pc.ApplicationAction{}}
	for name, a := range in.Actions {
		args := slices.Clone(a.Arguments)
		if len(a.Args) > 0 && len(args) > 0 {
			return Program{}, ErrInvalidInput
		}
		for _, literal := range a.Args {
			args = append(args, pc.LiteralApplicationArgument(literal))
		}
		decl.Actions[name] = pc.ApplicationAction{PythonModule: a.PythonModule, Binary: a.Binary, Args: args, ReadOnly: a.ReadOnly}
	}
	if pc.ValidateApplicationSource(decl) != "" {
		return Program{}, ErrInvalidInput
	}
	kind, code := pc.ApplicationBuildKind(decl)
	if code != "" {
		return Program{}, ErrInvalidInput
	}
	p := Program{Build: BuildSpec{Kind: kind}, Actions: map[string]Action{}}
	if in.Build != nil {
		p.Build = *in.Build
		p.Build.Features = slices.Clone(in.Build.Features)
		slices.Sort(p.Build.Features)
	}
	if in.NixAttribute != "" {
		p.Build.Attribute = in.NixAttribute
	}
	if in.Run != nil {
		p.Run = *in.Run
		p.Run.Args = slices.Clone(in.Run.Args)
	}
	if in.PythonModule != "" {
		p.Run.PythonModule = in.PythonModule
	}
	for k, a := range in.Actions {
		if !keyPattern.MatchString(k) || len(a.Arguments) > 32 || len(a.Args) > 0 && len(a.Arguments) > 0 {
			return Program{}, ErrInvalidInput
		}
		for _, arg := range a.Arguments {
			if !arg.Valid() {
				return Program{}, ErrInvalidInput
			}
		}
		p.Actions[k] = a
	}
	return p, nil
}
