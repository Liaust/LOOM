package projectcontracts

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

type LoadError struct {
	Code string
	File string
	Err  error
}

func (e LoadError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Err.Error()
}

func (e LoadError) Unwrap() error {
	return e.Err
}

func LoadProject(root string) (LoadedProject, error) {
	rootPath, err := normalizeRoot(root)
	if err != nil {
		return LoadedProject{}, LoadError{Code: "contract.read_failed", File: root, Err: err}
	}
	canonicalPath := filepath.Join(rootPath, filepath.FromSlash(CanonicalRootContractPath))
	legacyPath := filepath.Join(rootPath, filepath.FromSlash(LegacyRootContractPath))
	canonicalRaw, canonicalPresent, err := readOptionalContract(canonicalPath)
	if err != nil {
		return LoadedProject{}, LoadError{Code: "contract.read_failed", File: canonicalPath, Err: fmt.Errorf("read %s: %w", CanonicalRootContractPath, err)}
	}
	legacyRaw, legacyPresent, err := readOptionalContract(legacyPath)
	if err != nil {
		return LoadedProject{}, LoadError{Code: "contract.read_failed", File: legacyPath, Err: fmt.Errorf("read %s: %w", LegacyRootContractPath, err)}
	}
	discovery := ProjectLayoutDiscovery{
		CanonicalPath:    canonicalPath,
		LegacyPath:       legacyPath,
		CanonicalPresent: canonicalPresent,
		LegacyPresent:    legacyPresent,
	}
	if !canonicalPresent && !legacyPresent {
		return LoadedProject{}, LoadError{
			Code: "contract.read_failed",
			File: canonicalPath,
			Err: fmt.Errorf(
				"project contract not found; expected %s or %s under %s",
				CanonicalRootContractPath,
				LegacyRootContractPath,
				rootPath,
			),
		}
	}

	var canonicalDeclaration, legacyDeclaration *ProjectDeclaration
	var canonicalContract ProjectContract
	if canonicalPresent {
		canonicalContract, canonicalDeclaration, err = parseProjectSource(canonicalRaw, canonicalPath, CanonicalRootContractPath)
		if err != nil {
			return LoadedProject{}, err
		}
	}
	var legacyContract ProjectContract
	if legacyPresent {
		legacyContract, legacyDeclaration, err = parseProjectSource(legacyRaw, legacyPath, LegacyRootContractPath)
		if err != nil {
			return LoadedProject{}, err
		}
	}

	contractPath := canonicalPath
	raw := canonicalRaw
	contract := canonicalContract
	declaration := canonicalDeclaration
	layout := ProjectLayoutCanonical
	switch {
	case canonicalPresent && legacyPresent:
		if !projectContractsSemanticallyEqual(canonicalContract, legacyContract) || !reflect.DeepEqual(canonicalDeclaration, legacyDeclaration) {
			return LoadedProject{}, LoadError{
				Code: "contract.layout_conflict",
				File: canonicalPath,
				Err: fmt.Errorf(
					"project contract layout conflict: %s and %s contain different project definitions",
					canonicalPath,
					legacyPath,
				),
			}
		}
		layout = ProjectLayoutCanonicalWithLegacy
	case legacyPresent:
		contractPath = legacyPath
		raw = legacyRaw
		contract = legacyContract
		declaration = legacyDeclaration
		layout = ProjectLayoutLegacy
	}
	return LoadedProject{
		Declaration:  declaration,
		RootPath:     rootPath,
		ContractPath: contractPath,
		Layout:       layout,
		Discovery:    discovery,
		Raw:          raw,
		Contract:     contract,
	}, nil
}

func readOptionalContract(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func parseProjectContract(raw []byte, absolutePath, displayPath string) (ProjectContract, error) {
	var contract ProjectContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ProjectContract{}, LoadError{Code: "contract.parse_failed", File: absolutePath, Err: fmt.Errorf("parse %s: %w", displayPath, err)}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple YAML documents are not supported")
		}
		return ProjectContract{}, LoadError{Code: "contract.parse_failed", File: absolutePath, Err: fmt.Errorf("parse %s: %w", displayPath, err)}
	}
	return contract, nil
}

func Analyze(root string) Analysis {
	now := nowUTC()
	loaded, err := LoadProject(root)
	if err != nil {
		rootPath, _ := normalizeRoot(root)
		report := emptyReport(rootPath, "", now)
		diag := diagnosticForLoadError(err)
		report.Diagnostics = append(report.Diagnostics, diag)
		report.Summary = summarizeDiagnostics(report.Diagnostics)
		report.OK = false
		report.Registerable = false
		plan := BuildPlan(nil, report)
		return Analysis{Report: report, Plan: plan}
	}
	report := Validate(loaded)
	plan := BuildPlan(&loaded, report)
	return Analysis{Loaded: &loaded, Report: report, Plan: plan}
}

func normalizeRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root must be a directory")
	}
	return filepath.Clean(abs), nil
}

func diagnosticForLoadError(err error) Diagnostic {
	code := "contract.read_failed"
	file := ""
	message := err.Error()
	if loadErr, ok := err.(LoadError); ok {
		code = loadErr.Code
		file = loadErr.File
		message = loadErr.Error()
	}
	return Diagnostic{
		Severity:   SeverityError,
		Code:       code,
		Message:    message,
		File:       file,
		Suggestion: "create a valid " + CanonicalRootContractPath + " file or migrate the legacy " + LegacyRootContractPath + " layout",
	}
}
