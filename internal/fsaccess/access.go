package fsaccess

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type Requirement string

const (
	Read    Requirement = "read"
	Write   Requirement = "write"
	Execute Requirement = "execute"
)

type Result struct {
	Path         string        `json:"path"`
	Required     []Requirement `json:"required"`
	OK           bool          `json:"ok"`
	Exists       bool          `json:"exists"`
	IsDir        bool          `json:"is_dir"`
	Readable     bool          `json:"readable,omitempty"`
	Writable     bool          `json:"writable,omitempty"`
	Executable   bool          `json:"executable,omitempty"`
	Error        string        `json:"error,omitempty"`
	MissingModes []Requirement `json:"missing_modes,omitempty"`
}

func Check(path string, requirements ...Requirement) Result {
	result := Result{Path: strings.TrimSpace(path), Required: normalizeRequirements(requirements)}
	if result.Path == "" {
		result.Error = "path is empty"
		result.MissingModes = append([]Requirement{}, result.Required...)
		return result
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		result.Error = err.Error()
		result.MissingModes = append([]Requirement{}, result.Required...)
		return result
	}
	result.Exists = true
	result.IsDir = info.IsDir()
	result.Readable = access(result.Path, Read) == nil
	result.Writable = access(result.Path, Write) == nil
	result.Executable = access(result.Path, Execute) == nil
	for _, requirement := range result.Required {
		if err := access(result.Path, requirement); err != nil {
			result.MissingModes = append(result.MissingModes, requirement)
			if result.Error == "" {
				result.Error = err.Error()
			}
		}
	}
	result.OK = len(result.MissingModes) == 0
	return result
}

func access(path string, requirement Requirement) error {
	mask := uint32(0)
	switch requirement {
	case Read:
		mask = unix.R_OK
	case Write:
		mask = unix.W_OK
	case Execute:
		mask = unix.X_OK
	default:
		return fmt.Errorf("unknown access requirement %q", requirement)
	}
	return unix.Access(path, mask)
}

func normalizeRequirements(requirements []Requirement) []Requirement {
	out := []Requirement{}
	seen := map[Requirement]bool{}
	for _, requirement := range requirements {
		if requirement == "" || seen[requirement] {
			continue
		}
		out = append(out, requirement)
		seen[requirement] = true
	}
	return out
}
