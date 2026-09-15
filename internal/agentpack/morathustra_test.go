package agentpack

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func copyMorathustraPack(t *testing.T) *Pack {
	t.Helper()
	source := loadRepositoryPack(t)
	root := filepath.Join(t.TempDir(), "pack")
	if err := os.CopyFS(root, os.DirFS(source.Root.Path)); err != nil {
		t.Fatal(err)
	}
	pack, err := LoadFromPath(root)
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func addMorathustraFile(t *testing.T, pack *Pack, name, content string, declare bool) {
	t.Helper()
	path := filepath.Join(pack.Root.Path, "templates/morathustra", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if declare {
		for i := range pack.Manifest.Templates {
			if pack.Manifest.Templates[i].Name == "morathustra" {
				pack.Manifest.Templates[i].Files = append(pack.Manifest.Templates[i].Files, name)
			}
		}
	}
}

func TestMorathustraHermesCompletePortableScaffoldAndExactReplay(t *testing.T) {
	pack := loadRepositoryPack(t)
	destination := filepath.Join(t.TempDir(), "morathustra")
	allowWorkspaceCleanup(t, destination)
	plan, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWorkspace(plan, true); err != nil {
		t.Fatal(err)
	}

	// Independently enumerate output, not just the manifest's chosen files.
	wantFiles := []string{
		".hermes/SOUL.md", "AGENTS.md", "WORKFLOW.md", "WORKSPACE-MAP.md",
		"OPERATING-POLICY.md", "TOOLING.md", "handoffs/HANDOFF-TEMPLATE.md",
		"investigations/README.md", "recovery/README.md", "tmp/README.md",
		"skills/installed/README.md", "protocols/README.md", "protocols/LOOM-ROUTING.md",
		"protocols/PROVENANCE-AND-MEMORY.md", "protocols/PROJECT-DELEGATION.md",
		"protocols/SESSION-RETRIEVAL.md", "protocols/SKILL-CREATION-AND-PROMOTION.md",
		"protocols/CREDENTIALS.md", "protocols/EXTERNAL-MESSAGING.md",
		"protocols/BASECAMP.md", "protocols/GITHUB.md",
	}
	want := map[string]bool{}
	for _, name := range wantFiles {
		want[name] = true
	}
	count := 0
	err = filepath.WalkDir(destination, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(destination, path)
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !want[relative] {
			t.Errorf("unexpected output entry %s (%s)", relative, entry.Type())
			return nil
		}
		count++
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"/srv/", "/Users/", "/home/", "/var/lib/", "postgres://", "postgresql://", "sqlite://", "pass://", "LOOM_PROVENANCE_DB_URL", "BEGIN PRIVATE KEY", "token=", "password="} {
			if strings.Contains(string(payload), forbidden) {
				t.Errorf("forbidden output content in %s", relative)
			}
		}
		return nil
	})
	if err != nil || count != len(want) {
		t.Fatalf("output inventory count=%d want=%d err=%v", count, len(want), err)
	}
	for _, name := range []string{"MORA.md", "SOUL.md", "memory", "integrations", ".hermes/skills", ".hermes/config.yaml", ".hermes/state.db", ".hermes/memories", ".hermes/sessions", "skills/created", "skills/README.md"} {
		if _, err := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Errorf("unexpected legacy, duplicate or runtime entry %s: %v", name, err)
		}
	}
	for name, mode := range map[string]os.FileMode{".": 0o700, ".hermes": 0o700, ".hermes/SOUL.md": 0o600, "skills": 0o555, "skills/installed": 0o555, "skills/installed/README.md": 0o444} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: expected mode %o, info=%v err=%v", name, mode, info, err)
		}
	}
	if os.Geteuid() != 0 {
		if file, err := os.OpenFile(filepath.Join(destination, "skills/installed/README.md"), os.O_WRONLY, 0); err == nil {
			file.Close()
			t.Error("installed placeholder accepted a write")
		}
		if file, err := os.Create(filepath.Join(destination, "skills/installed/new.md")); err == nil {
			file.Close()
			t.Error("installed directory accepted agent creation")
		}
	}
	custom := []byte("# Morathustra\n\nOperator-curated personality stays intact.\n")
	if err := os.WriteFile(filepath.Join(destination, ".hermes/SOUL.md"), custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "operator-note.txt"), []byte("Do not change this user file.\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := workspaceSnapshot(t, destination)
	for replay := 0; replay < 2; replay++ {
		plan, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination})
		if err != nil {
			t.Fatal(err)
		}
		if !hasWorkspaceAction(plan, ".hermes/SOUL.md", WorkspacePreserveUserFile) {
			t.Fatal("custom SOUL was not preserved")
		}
		if _, err := ApplyWorkspace(plan, true); err != nil {
			t.Fatal(err)
		}
		if after := workspaceSnapshot(t, destination); !reflect.DeepEqual(before, after) {
			t.Fatal("replay changed file bytes, modes, timestamps or identities")
		}
	}
}

func workspaceSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var content []byte
		if !entry.IsDir() {
			content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		stat := info.Sys().(*syscall.Stat_t)
		result[relative] = fmt.Sprintf("%s:%v:%d:%d:%x", info.Mode(), info.ModTime(), stat.Dev, stat.Ino, sha256.Sum256(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMorathustraRejectsPrivateOrRuntimeTemplateMaterial(t *testing.T) {
	cases := []struct {
		name, content string
		declared      bool
	}{
		{"SOUL.md", "duplicate personality", true},
		{"MORA.md", "legacy personality", true},
		{"MORA.backup.md", "legacy personality copy", true},
		{"credentials.md", "synthetic private credentials", true},
		{"secrets.md", "synthetic private secrets", true},
		{"protocols/soul-copy.md", "duplicate personality", true},
		{"IDENTITY.md", "alternate personality", true},
		{"PERSONALITY.md", "alternate personality", true},
		{"memory/private.md", "private relationship notes", true},
		{"integrations/README.md", "old speculative integrations", true},
		{".hermes/memories/MEMORY.md", "private memory", true},
		{".hermes/skills/native/SKILL.md", "native state", true},
		{".hermes/config.yaml", "model: fixture", true},
		{".hermes/state.db", "synthetic database", true},
		{".hermes/state.db-wal", "synthetic WAL", false},
		{".hermes/state.db-shm", "synthetic SHM", false},
		{".hermes/sessions/session.json", "synthetic session", false},
		{".env", "synthetic credential", false},
		{".git/config", "synthetic live-profile Git tracking", false},
		{".git", "gitdir: synthetic-worktree", false},
		{".config/gh/hosts.yml", "synthetic personal GitHub login", true},
		{".config/basecamp/credentials.json", "synthetic Codex OAuth", true},
		{".ssh/id_ed25519", "synthetic personal Git transport", true},
		{".hermes/auth.json", "synthetic OAuth state", true},
		{".hermes/logs/runtime.md", "synthetic host log", true},
		{"credentials/auth.json", "synthetic auth", true},
		{"skills/created/SKILL.md", "wrong store", true},
		{"undeclared.md", "unexpected source", false},
		{"protocols/extra.md", "fixture host /srv/loom/agents/morathustra", true},
		{"protocols/extra.md", "fixture host /Users/example/private", true},
		{"protocols/extra.md", "fixture host /home/example/private", true},
		{"protocols/extra.md", `fixture host C:\Users\example\private`, true},
		{"protocols/extra.md", "postgresql://fixture:example@invalid/database", true},
		{"protocols/extra.md", "sqlite://fixture/state", true},
		{"protocols/extra.md", "pass://fixture/item", true},
		{"protocols/extra.md", "LOOM_PROVENANCE_DB_URL", true},
		{"protocols/extra.md", "token=synthetic-not-a-secret", true},
		{"protocols/extra.md", `{"refresh_token":"synthetic-not-a-secret"}`, true},
		{"protocols/extra.md", "-----BEGIN PRIVATE KEY-----", true},
		{"protocols/extra.md", "binary\x00fixture", true},
	}
	for index, test := range cases {
		t.Run(fmt.Sprintf("%02d_%s", index, test.name), func(t *testing.T) {
			pack := copyMorathustraPack(t)
			addMorathustraFile(t, pack, test.name, test.content, test.declared)
			report := Validate(pack)
			if report.Valid() || !hasValidationCode(report, "template.unsafe") {
				t.Fatalf("unsafe source passed validation: %#v", report)
			}
			destination := filepath.Join(t.TempDir(), "workspace")
			if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination}); err == nil {
				t.Fatal("unsafe source passed planning")
			} else if strings.Contains(err.Error(), test.content) && len(test.content) > 15 {
				t.Fatal("error leaked rejected content")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("rejected plan wrote output")
			}
		})
	}
}

func TestMorathustraSoulCannotBeMissingEmptyOrCommentsOnly(t *testing.T) {
	for _, content := range []string{"", "\n# Morathustra\n", "<!-- legacy template\nonly comments -->\n# SOUL\n"} {
		pack := copyMorathustraPack(t)
		path := filepath.Join(pack.Root.Path, "templates/morathustra/.hermes/SOUL.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if Validate(pack).Valid() {
			t.Fatal("non-substantive SOUL passed")
		}
	}
	pack := copyMorathustraPack(t)
	if err := os.Remove(filepath.Join(pack.Root.Path, "templates/morathustra/.hermes/SOUL.md")); err != nil {
		t.Fatal(err)
	}
	if Validate(pack).Valid() {
		t.Fatal("missing SOUL passed")
	}
}

func TestMorathustraManifestCannotOmitNamedIdentityProtocols(t *testing.T) {
	for _, name := range []string{"protocols/BASECAMP.md", "protocols/GITHUB.md"} {
		t.Run(name, func(t *testing.T) {
			pack := copyMorathustraPack(t)
			if err := os.Remove(filepath.Join(pack.Root.Path, "templates/morathustra", name)); err != nil {
				t.Fatal(err)
			}
			for i := range pack.Manifest.Templates {
				template := &pack.Manifest.Templates[i]
				if template.Name != "morathustra" {
					continue
				}
				var kept []string
				for _, file := range template.Files {
					if file != name {
						kept = append(kept, file)
					}
				}
				template.Files = kept
			}
			if report := Validate(pack); report.Valid() || !hasValidationCode(report, "template.unsafe") {
				t.Fatal("pack silently omitted a required named identity contract")
			}
			destination := filepath.Join(t.TempDir(), "workspace")
			if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination}); err == nil {
				t.Fatal("planned an incomplete named workspace")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("rejected plan wrote output")
			}
		})
	}
}

func TestMorathustraRejectsNonRegularTemplateEntries(t *testing.T) {
	for _, kind := range []string{"file-symlink", "directory-symlink", "fifo", "socket"} {
		t.Run(kind, func(t *testing.T) {
			pack := copyMorathustraPack(t)
			root := filepath.Join(pack.Root.Path, "templates/morathustra")
			name := filepath.Join(root, "unexpected.md")
			switch kind {
			case "file-symlink":
				if err := os.Symlink(".hermes/SOUL.md", name); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				if err := os.Symlink("protocols", name); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(name, 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				// macOS UNIX socket paths are short; bind elsewhere, then move the entry.
				short, err := os.MkdirTemp("", "sock-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(short)
				listener, err := net.Listen("unix", filepath.Join(short, "s"))
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if err := os.Rename(filepath.Join(short, "s"), name); err != nil {
					t.Fatal(err)
				}
			}
			if Validate(pack).Valid() {
				t.Fatal("non-regular source passed validation")
			}
			if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: filepath.Join(t.TempDir(), "workspace")}); err == nil {
				t.Fatal("non-regular source passed planning")
			}
		})
	}
}

func TestMorathustraRefusesLegacyAndRuntimeDestinationWithoutMutation(t *testing.T) {
	for _, name := range []string{"MORA.md", "memory/INDEX.md", "SOUL.md", ".hermes/config.yaml", ".hermes/state.db", ".hermes/skills/local/SKILL.md", ".hermes/memories/MEMORY.md", ".git/config", ".git", ".config/gh/hosts.yml", ".config/basecamp/credentials.json", ".hermes/auth.json"} {
		t.Run(name, func(t *testing.T) {
			pack := loadRepositoryPack(t)
			destination := filepath.Join(t.TempDir(), "workspace")
			path := filepath.Join(destination, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("synthetic state must survive"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := workspaceSnapshot(t, destination)
			if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination}); err == nil {
				t.Fatal("legacy/runtime destination passed")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, destination)) {
				t.Fatal("legacy state changed")
			}
		})
	}
}

func TestWorkspaceRejectsSourceAndDestinationSubstitution(t *testing.T) {
	for _, kind := range []string{"source-content", "source-parent", "source-file", "destination-parent", "destination-file", "destination-special", "legacy-after-plan", "forged-action"} {
		t.Run(kind, func(t *testing.T) {
			pack := copyMorathustraPack(t)
			container := t.TempDir()
			destination := filepath.Join(container, "workspace")
			allowWorkspaceCleanup(t, destination)
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination})
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(pack.Root.Path, "templates/morathustra")
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel.md")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "source-content":
				if err := os.WriteFile(filepath.Join(source, "WORKFLOW.md"), []byte("Changed after review."), 0o644); err != nil {
					t.Fatal(err)
				}
			case "source-parent":
				path := filepath.Join(source, "protocols")
				if err := os.Rename(path, filepath.Join(outside, "protocols")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "protocols"), path); err != nil {
					t.Fatal(err)
				}
			case "source-file":
				path := filepath.Join(source, "WORKFLOW.md")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, path); err != nil {
					t.Fatal(err)
				}
			case "destination-parent":
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, destination); err != nil {
					t.Fatal(err)
				}
			case "destination-file":
				if err := os.Symlink(sentinel, filepath.Join(destination, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			case "destination-special":
				if err := syscall.Mkfifo(filepath.Join(destination, "AGENTS.md"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "legacy-after-plan":
				if err := os.WriteFile(filepath.Join(destination, "MORA.md"), []byte("preserve legacy"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "forged-action":
				plan.Actions[len(plan.Actions)-1].DestinationPath = sentinel
			}
			if _, err := ApplyWorkspace(plan, true); err == nil {
				t.Fatal("substituted plan applied")
			}
			if payload, err := os.ReadFile(sentinel); err != nil || !bytes.Equal(payload, []byte("untouched")) {
				t.Fatal("outside data changed")
			}
			if _, err := os.Lstat(filepath.Join(destination, ".hermes")); !os.IsNotExist(err) {
				t.Fatal("rejected apply created profile")
			}
		})
	}
}

func TestWorkspaceRejectsSymlinkedAncestorAndTemplateRoot(t *testing.T) {
	pack := copyMorathustraPack(t)
	outside := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: filepath.Join(alias, "workspace")}); err == nil {
		t.Fatal("symlinked parent accepted")
	}
	root := filepath.Join(pack.Root.Path, "templates/morathustra")
	moved := filepath.Join(t.TempDir(), "template")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, root); err != nil {
		t.Fatal(err)
	}
	if Validate(pack).Valid() {
		t.Fatal("symlinked template root accepted")
	}
}

func TestMorathustraExistingInstalledPermissionsAndSoulFailClosed(t *testing.T) {
	for _, name := range []string{"skills", "skills/installed", "skills/installed/README.md", ".hermes/SOUL.md"} {
		t.Run(name, func(t *testing.T) {
			pack := loadRepositoryPack(t)
			destination := filepath.Join(t.TempDir(), "workspace")
			allowWorkspaceCleanup(t, destination)
			plan, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyWorkspace(plan, true); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(destination, name)
			if name == ".hermes/SOUL.md" {
				if err := os.WriteFile(path, []byte("<!-- legacy -->\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
			before := workspaceSnapshot(t, destination)
			if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: destination}); err == nil {
				t.Fatal("unsafe preserved state accepted")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, destination)) {
				t.Fatal("existing content or permissions changed")
			}
		})
	}
}

func TestWorkspaceRejectsNonPortableManifestPathsAndDuplicateTemplate(t *testing.T) {
	for _, name := range []string{"../escape.md", "nested/../../escape.md", "/absolute.md", `C:\Users\fixture.md`, `protocols\fixture.md`, "AGENTS.md ", "./AGENTS.md", "AGENTS.md"} {
		t.Run(name, func(t *testing.T) {
			pack := copyMorathustraPack(t)
			for i := range pack.Manifest.Templates {
				if pack.Manifest.Templates[i].Name == "morathustra" {
					pack.Manifest.Templates[i].Files = append(pack.Manifest.Templates[i].Files, name)
				}
			}
			if Validate(pack).Valid() {
				t.Fatal("invalid manifest path accepted")
			}
			if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: filepath.Join(t.TempDir(), "workspace")}); err == nil {
				t.Fatal("invalid manifest path planned")
			}
		})
	}
	pack := loadRepositoryPack(t)
	template, err := findWorkspaceTemplate(pack, "morathustra")
	if err != nil {
		t.Fatal(err)
	}
	pack.Manifest.Templates = append(pack.Manifest.Templates, template)
	if Validate(pack).Valid() {
		t.Fatal("duplicate template accepted")
	}
	if _, err := PlanWorkspace(pack, WorkspaceOptions{Path: filepath.Join(t.TempDir(), "workspace")}); err == nil {
		t.Fatal("duplicate template planned")
	}
}

func TestWorkspaceMetadataAndSkillReadersRejectSymlinksAndSpecialFiles(t *testing.T) {
	for _, relative := range []string{"manifest.yaml", "catalogue.yaml", "skills/orchestrate-loom-work/SKILL.md"} {
		for _, special := range []string{"symlink", "fifo"} {
			t.Run(relative+special, func(t *testing.T) {
				pack := copyMorathustraPack(t)
				path := filepath.Join(pack.Root.Path, relative)
				copyPath := filepath.Join(t.TempDir(), "private.md")
				if err := os.Rename(path, copyPath); err != nil {
					t.Fatal(err)
				}
				if special == "symlink" {
					if err := os.Symlink(copyPath, path); err != nil {
						t.Fatal(err)
					}
				} else if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := readWorkspaceFile(path); err == nil {
					t.Fatal("reader accepted non-regular metadata")
				}
				if relative == "manifest.yaml" || relative == "catalogue.yaml" {
					if _, err := LoadFromPath(pack.Root.Path); err == nil {
						t.Fatal("load accepted non-regular metadata")
					}
				} else if Validate(pack).Valid() {
					t.Fatal("validation accepted non-regular skill")
				}
			})
		}
	}
}

func TestWorkspaceMetadataParseErrorsDoNotEchoPrivateScalars(t *testing.T) {
	pack := copyMorathustraPack(t)
	fixture := "synthetic-private-scalar-do-not-echo"
	path := filepath.Join(pack.Root.Path, "manifest.yaml")
	if err := os.WriteFile(path, []byte("harnesses: "+fixture+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromPath(pack.Root.Path)
	if err == nil || strings.Contains(err.Error(), fixture) || !strings.Contains(err.Error(), "manifest.yaml") {
		t.Fatalf("unsafe parse diagnostic: %v", err)
	}
}

// Source-contract regression for the accepted Phase 9D guidance. Actual native
// staging/category/collision behavior is separately exercised in a temporary
// Hermes fixture; these text checks are not a runtime authorization sandbox.
func TestMorathustraNativeSkillOwnershipAndNixGuidance(t *testing.T) {
	payloads := morathustraInstructions(t)
	text := func(name string) string { return strings.Join(strings.Fields(string(payloads[name])), " ") }
	for name, requirements := range map[string][]string{
		"AGENTS.md": {
			`category="created"`, ".hermes/skills/created/<name>/SKILL.md",
			"never approve your own pending changes on the operator's behalf",
			"Read-only modes owned by the same user are not a security sandbox",
			"runtime state is not all immutable", "sessions, theme files and bounded task state",
		},
		"TOOLING.md": {
			"Nix owns package versions and dependencies, service definitions, immutable store paths and system security",
			"Pinned Hermes has no `skills.create_dir` setting", "can refuse a change and still return exit zero",
			"re-read effective state", "not an automatic overwrite mechanism for every live config edit",
			"pip, npm, system package installation", "bypass managed mode",
			"current and desired setting, and source owner", "Continue unrelated work",
			"Runtime profile state is not all immutable", "Native skill and memory tools, sessions, theme files",
		},
		"WORKSPACE-MAP.md": {
			".hermes/skills/created/<name>/SKILL.md", `category="created"`,
			"The scaffold supplies only the SOUL there", "does not transfer ownership to the curator",
			"Nineteen visible skills remained after unwanted skills were removed from selection",
			"historical observation, not a curated or fixed catalogue or a ceiling",
			"Do not infer a new-skill approval requirement from it", "four platform-hidden skills remain on disk",
		},
		"protocols/SKILL-CREATION-AND-PROMOTION.md": {
			`skill_manage`, `action="create"`, `category="created"`,
			".hermes/skills/created/<name>/SKILL.md", "Pinned Hermes has no `skills.create_dir` setting",
			"skills.write_approval=true", "memory.write_approval=true", "skills.guard_agent_created=true",
			"heuristic review", "not a security guarantee", "staged=true",
			"/skills pending", "/skills diff <id>", "/skills approve <id>", "/skills reject <id>",
			"Never approve your own pending changes on the operator's behalf",
			"Do not shadow a LOOM-installed skill name", "check both owners before choosing a name",
			"No self-promotion", "Read-only modes owned by the same user are not a security sandbox",
			"prune_builtins=false", "consolidate=false", "interval_hours=168", "min_idle_hours=2",
			"stale_after_days=30", "archive_after_days=90", "archive_ttl_days=0",
			"backup.enabled=true", "backup.keep=5", "Do not force a curator run",
			"not an enforced gateway idle guard", `idle_for_seconds=float("inf")`,
			"A restart is not needed merely to reload these settings", "Preserve active sessions",
			"Foreground-created skills are user-owned", "Category alone does not transfer curator ownership",
			"the operator must explicitly opt", "hermes curator adopt <name>",
			"adoption does not reset the inactivity clock", "Never forge an ownership marker",
			"not deletion", "Nineteen visible skills remained after unwanted skills were removed from selection",
			"historical observation, not a curated or fixed catalogue or a ceiling",
			"Do not infer a new-skill approval requirement from it", "four platform-hidden Apple skills untouched",
		},
	} {
		content := text(name)
		for _, want := range requirements {
			if !strings.Contains(content, want) {
				t.Errorf("%s lost Phase 9D guidance %q", name, want)
			}
		}
	}
	protocol := text("protocols/SKILL-CREATION-AND-PROMOTION.md")
	for _, forbidden := range []string{"curator stays disabled", "skills.create_dir=", "skills.create_dir:", "HERMES_MANAGED=false", "HERMES_MANAGED=0"} {
		if strings.Contains(protocol, forbidden) {
			t.Errorf("skill protocol contains unsupported/bypass instruction %q", forbidden)
		}
	}
	// The selection list is bounded at its own paragraph, so unrelated mentions
	// elsewhere cannot hide an omission, extra name or duplicate.
	start := strings.Index(protocol, "- airtable,")
	end := strings.Index(protocol, "Nineteen visible skills remained")
	if start < 0 || end <= start {
		t.Fatal("missing exact bundled selection list")
	}
	got := strings.Split(strings.TrimSuffix(strings.TrimSpace(protocol[start+2:end]), "."), ",")
	want := strings.Fields(`airtable arxiv ascii-video baoyu-infographic box claude-code
        claude-design codebase-inspection codex competitor-news-monitor computer-use design-md dogfood
        email-inbox-triage gif-search google-workspace hermes-agent-skill-authoring himalaya humanizer
        inspecting-hermes-desktop-dom llm-wiki manim-video maps node-inspect-debugger notion obsidian
        opencode p5js popular-web-designs product-price-monitor python-debugpy requesting-code-review
        sdlc-review simplify-code songsee songwriting-and-ai-music spike systematic-debugging
        teams-meeting-pipeline test-driven-development xurl youtube-content`)
	for i := range got {
		got[i] = strings.TrimSpace(got[i])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("instruction disabled set drift: %v", got)
	}
}
