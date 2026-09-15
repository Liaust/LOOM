package agentpack

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var morathustraPrivateContent = regexp.MustCompile(`(?i)(/(?:Users|home|srv|etc|Volumes|private|tmp)/|/var/(?:lib|run)/|[a-z]:[\\/]|\\\\[a-z0-9_.-]+\\|(?:postgres(?:ql)?|sqlite|mysql|mongodb|pass|file)://|LOOM_PROVENANCE_DB_URL|\b(?:DATABASE_URL|PGHOST|PGPASSWORD)\s*[:=]|-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:password|passwd|token|api[_-]?key|client[_-]?secret|access[_-]?token|refresh[_-]?token)\s*["']?\s*[:=]\s*\S+|\b(?:sk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9]{16,}|xox[baprs]-[a-z0-9-]{16,})|\.loom/state|checkpoint\.json)`)
var soulComments = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)

// Both named personas share one scaffold contract. Selecting a template never
// provisions a live profile or changes the compatibility default.
func isNamedPersonaTemplate(name string) bool {
	return name == "morathustra" || name == "mina"
}

func substantiveSoul(payload []byte) bool {
	for _, line := range strings.Split(soulComments.ReplaceAllString(string(payload), ""), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") && line != "---" {
			return true
		}
	}
	return false
}

func validateMorathustraPath(relative string, directory bool) error {
	lower := strings.ToLower(relative)
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(lower, "/") {
		switch component {
		case "memory", "memories", "runtime", "state", "credentials", "sessions", "cron", "logs", "integrations", ".ssh", ".env":
			return fmt.Errorf("legacy, private or runtime state is not scaffold material: %s", relative)
		}
	}
	base := strings.ToLower(filepath.Base(relative))
	if relative != ".hermes/SOUL.md" && (strings.Contains(base, "soul") || strings.Contains(base, "personality") || strings.HasPrefix(base, "mora.") || strings.HasPrefix(base, "mina.") || base == "identity.md") {
		return fmt.Errorf("only .hermes/SOUL.md may define personality: %s", relative)
	}
	if relative == ".hermes" && directory || relative == ".hermes/SOUL.md" && !directory {
		return nil
	}
	if strings.HasPrefix(lower, ".hermes") || strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".db") || strings.Contains(base, ".db-") || strings.HasSuffix(base, ".sqlite") || strings.HasSuffix(base, ".sqlite3") || (strings.Contains(base, "credential") || strings.Contains(base, "secret") || strings.Contains(base, "token")) && relative != "protocols/CREDENTIALS.md" {
		return fmt.Errorf("profile, credential or database state is not scaffold material: %s", relative)
	}
	if relative == "skills" && directory || relative == "skills/installed" && directory || strings.HasPrefix(relative, "skills/installed/") {
		return nil
	}
	if strings.HasPrefix(lower, "skills/") {
		return fmt.Errorf("only skills/installed belongs outside the Hermes profile: %s", relative)
	}
	if strings.HasPrefix(base, ".") {
		return fmt.Errorf("scaffold entries must be portable Markdown instructions: %s", relative)
	}
	return nil
}

func validateMorathustraContent(relative string, payload []byte) error {
	if !utf8.Valid(payload) || strings.ContainsRune(string(payload), '\x00') || morathustraPrivateContent.Match(payload) {
		// Never include the matched value in an error or validation report.
		return fmt.Errorf("host path, credential, database locator or non-text state in scaffold file: %s", relative)
	}
	if relative == ".hermes/SOUL.md" && !substantiveSoul(payload) {
		return fmt.Errorf(".hermes/SOUL.md requires substantive personality content")
	}
	return nil
}

// Inspect the entire template tree, including undeclared files and directories.
// A manifest cannot hide private/runtime state from pack validation or copying.
func inspectWorkspaceTemplate(pack *Pack, template ManifestTemplate) (map[string][]byte, error) {
	if _, err := normalizeWorkspaceRelativePath(template.Path); err != nil {
		return nil, err
	}
	root, err := secureJoin(pack.Root.Path, template.Path)
	if err != nil {
		return nil, err
	}
	directory, err := openWorkspaceDirectory(root)
	if err != nil {
		return nil, err
	}
	directory.Close()
	declared := map[string]bool{}
	directories := map[string]bool{".": true}
	for _, name := range template.Files {
		relative, err := normalizeWorkspaceRelativePath(name)
		if err != nil {
			return nil, err
		}
		if declared[relative] {
			return nil, fmt.Errorf("duplicate workspace file: %s", relative)
		}
		declared[relative] = true
		for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			directories[parent] = true
		}
	}
	if isNamedPersonaTemplate(template.Name) {
		for _, required := range []string{".hermes/SOUL.md", "protocols/BASECAMP.md", "protocols/GITHUB.md"} {
			if !declared[required] {
				return nil, fmt.Errorf("named persona %q requires the source contract: %s", template.Name, required)
			}
		}
	}
	payloads := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular workspace template entry: %s", relative)
		}
		if isNamedPersonaTemplate(template.Name) {
			if !entry.IsDir() && filepath.Ext(relative) != ".md" {
				return fmt.Errorf("scaffold entries must be portable Markdown instructions: %s", relative)
			}
			if err := validateMorathustraPath(relative, entry.IsDir()); err != nil {
				return err
			}
		}
		if entry.IsDir() {
			if !directories[relative] {
				return fmt.Errorf("undeclared workspace template directory: %s", relative)
			}
			return nil
		}
		if !declared[relative] {
			return fmt.Errorf("undeclared workspace template file: %s", relative)
		}
		payload, err := readWorkspaceFile(path)
		if err != nil {
			return err
		}
		if isNamedPersonaTemplate(template.Name) {
			if err := validateMorathustraContent(relative, payload); err != nil {
				return err
			}
		}
		payloads[relative] = payload
		return nil
	})
	if err != nil {
		return nil, err
	}
	for relative := range declared {
		if _, ok := payloads[relative]; !ok {
			return nil, fmt.Errorf("declared workspace template file is missing: %s", relative)
		}
	}
	return payloads, nil
}

// A scaffold is not a live provisioner or migration command. Inspect names and
// types only here: unrelated user-file contents must never be loaded as input.
func inspectMorathustraDestination(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && path == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular workspace destination entry: %s", relative)
		}
		// Only scaffold/profile ownership boundaries are reserved at a destination;
		// ordinary user notes and arbitrary task artifacts are preserved unread.
		if err := validateMorathustraPath(relative, entry.IsDir()); err != nil {
			return fmt.Errorf("scaffolding cannot migrate legacy or runtime state; operator review required: %w", err)
		}
		if relative == "skills" || relative == "skills/installed" || strings.HasPrefix(relative, "skills/installed/") {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o222 != 0 {
				return fmt.Errorf("installed skill boundary is writable; operator review required: %s", relative)
			}
		}
		return nil
	})
}
