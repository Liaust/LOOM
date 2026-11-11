package projectcontracts

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	hs "loom.local/loom/internal/hermesschedules"
)

const AgentInstructionsSchema = "agent.instructions.v1"

type AgentDeclaration struct {
	InstructionsRef string `json:"instructions_ref" yaml:"instructions_ref"`
	Model           string `json:"model" yaml:"model"`
	Provider        string `json:"provider" yaml:"provider"`
	Workdir         string `json:"workdir,omitempty" yaml:"workdir,omitempty"`
}

// Native IDs are stable across project renames and contain no caller path.
func AgentProfileID(projectID string, key ResourceKey) string {
	sum := sha256.Sum256([]byte(projectID + "/" + string(key)))
	return fmt.Sprintf("loom-%x", sum[:16])
}

func ValidAgentDeclaration(a AgentDeclaration) bool {
	return declarationPath(a.InstructionsRef, true) && strings.HasSuffix(a.InstructionsRef, ".md") &&
		strings.TrimSpace(a.Model) == a.Model && a.Model != "" && len(a.Model) <= 256 && !strings.ContainsAny(a.Model, "\x00\r\n") &&
		scheduleKeyPattern.MatchString(a.Provider) && (a.Workdir == "" || a.Workdir == "." || declarationPath(a.Workdir, false))
}

func AgentInput(a AgentDeclaration, root string, sources []DeclarationSourceSnapshot) (hs.AgentSpec, error) {
	if !ValidAgentDeclaration(a) || !filepath.IsAbs(root) {
		return hs.AgentSpec{}, fmt.Errorf("agent requires instructions_ref, explicit model/provider and a project-relative workdir")
	}
	for _, source := range sources {
		if source.Ref == a.InstructionsRef && source.SchemaVersion == AgentInstructionsSchema && len(source.Raw) > 0 && len(source.Raw) <= 32768 && utf8.Valid(source.Raw) && !strings.ContainsRune(string(source.Raw), 0) {
			return hs.AgentSpec{Instructions: string(source.Raw), Model: a.Model, Provider: a.Provider, Workdir: filepath.Join(root, a.Workdir)}, nil
		}
	}
	return hs.AgentSpec{}, fmt.Errorf("agent instructions are missing, empty or exceed 32768 bytes")
}
