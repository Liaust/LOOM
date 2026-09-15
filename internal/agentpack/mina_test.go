package agentpack

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// This inventory is deliberately independent of the manifest being tested.
var minaSourceFiles = []string{
	".hermes/SOUL.md", "AGENTS.md", "OPERATING-POLICY.md", "TOOLING.md", "WORKFLOW.md", "WORKSPACE-MAP.md",
	"handoffs/HANDOFF-TEMPLATE.md", "investigations/README.md", "protocols/BASECAMP.md", "protocols/CREDENTIALS.md", "protocols/DEVICE-AND-TOOL-ROUTING.md",
	"protocols/EXTERNAL-MESSAGING.md", "protocols/GITHUB.md", "protocols/LOOM-ROUTING.md", "protocols/MAC-COMPUTER-USE.md", "protocols/PROJECT-DELEGATION.md",
	"protocols/PROVENANCE-AND-MEMORY.md", "protocols/README.md", "protocols/SESSION-RETRIEVAL.md",
	"protocols/SKILL-CREATION-AND-PROMOTION.md", "recovery/README.md", "skills/installed/README.md", "tmp/README.md",
}

func minaInstructions(t *testing.T) map[string][]byte {
	t.Helper()
	pack := loadRepositoryPack(t)
	template, err := findWorkspaceTemplate(pack, "mina")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(template.Files, minaSourceFiles) || template.Path != "templates/mina" {
		t.Fatal("MINA manifest inventory changed")
	}
	payloads, err := inspectWorkspaceTemplate(pack, template)
	if err != nil {
		t.Fatal(err)
	}
	return payloads
}

func TestMinaScaffoldCompletePortablePrivateAndExactReplay(t *testing.T) {
	pack := loadRepositoryPack(t)
	payloads := minaInstructions(t)
	destination := filepath.Join(t.TempDir(), "mina")
	allowWorkspaceCleanup(t, destination)
	options := WorkspaceOptions{Template: "mina", Path: destination}
	plan, err := PlanWorkspace(pack, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("planning wrote output")
	}
	if _, err := ApplyWorkspace(plan, true); err != nil {
		t.Fatal(err)
	}
	var got []string
	err = filepath.WalkDir(destination, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(destination, path)
		rel = filepath.ToSlash(rel)
		if !entry.Type().IsRegular() || !slices.Contains(minaSourceFiles, rel) {
			t.Fatalf("unexpected output: %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(data, payloads[rel]) {
			t.Errorf("output differs from source: %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Sys().(*syscall.Stat_t).Nlink != 1 {
			t.Errorf("output must be a new independent file: %s", rel)
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, minaSourceFiles) {
		t.Fatalf("output inventory: %v", got)
	}
	for name, mode := range map[string]os.FileMode{".": 0700, ".hermes": 0700, ".hermes/SOUL.md": 0600, "skills": 0555, "skills/installed": 0555, "skills/installed/README.md": 0444} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode=%o want=%o", name, info.Mode().Perm(), mode)
		}
	}
	for name, content := range map[string]string{
		".hermes/SOUL.md":   "# MINA\n\nOperator-curated personality.\n",
		"WORKFLOW.md":       "Operator-owned workflow edits.\n",
		"operator-note.txt": "Private unrelated note, preserved unread.\n",
	} {
		if err := os.WriteFile(filepath.Join(destination, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := workspaceSnapshot(t, destination)
	for replay := 0; replay < 2; replay++ {
		plan, err = PlanWorkspace(pack, options)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".hermes/SOUL.md", "WORKFLOW.md"} {
			if !hasWorkspaceAction(plan, name, WorkspacePreserveUserFile) {
				t.Fatalf("did not preserve %s", name)
			}
		}
		for _, action := range plan.Actions {
			if action.Kind == WorkspaceCreateFile || action.Kind == WorkspaceCreateDirectory {
				t.Fatalf("replay created %s", action.RelativePath)
			}
		}
		if _, err := ApplyWorkspace(plan, true); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, workspaceSnapshot(t, destination)) {
			t.Fatal("replay changed bytes, modes, timestamps or identities")
		}
	}
}

func TestMinaSelectionDoesNotChangeDefaultsOrGrantArchivistVisibility(t *testing.T) {
	pack := loadRepositoryPack(t)
	if pack.Manifest.Version != "0.6.4" || pack.Catalogue.Version != "0.6.4" {
		t.Fatal("pack/catalogue version mismatch")
	}
	wantSets := map[string][]string{
		"project":     {"shared_core", "shared_development", "shared_provenance"},
		"mina":        {"shared_core", "shared_development", "morathustra_operations", "shared_provenance"},
		"morathustra": {"shared_core", "shared_development", "morathustra_operations", "shared_provenance"},
		"archivist":   {"shared_provenance"},
	}
	if len(pack.Manifest.RecommendedSkillSets) != len(wantSets) {
		t.Fatal("unexpected recommended set")
	}
	for name, want := range wantSets {
		if !reflect.DeepEqual(pack.Manifest.RecommendedSkillSets[name].Visibility, want) {
			t.Errorf("%s set changed", name)
		}
	}
	if !slices.Contains(pack.Catalogue.Roles, "mina") {
		t.Fatal("catalogue omits MINA role")
	}
	want := []string{"search-loom-knowledge", "search-loom-docs", "manage-loom-projects", "operate-loom-storage", "operate-loom-nodes", "investigate-loom-incidents", "orchestrate-loom-work", "use-proton-pass", "use-loom-provenance"}
	var selected []string
	for _, skill := range pack.Catalogue.Skills {
		if slices.Contains(skill.IntendedRoles, "mina") {
			selected = append(selected, skill.Name)
		}
		if slices.Contains(skill.IntendedRoles, "mina") != slices.Contains(skill.IntendedRoles, "morathustra") {
			t.Errorf("role parity lost: %s", skill.Name)
		}
	}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("MINA selection widened or duplicated: %v", selected)
	}
	if len(pack.Manifest.Skills) != 9 || len(pack.Catalogue.Skills) != 9 {
		t.Fatal("skills duplicated or added")
	}
	plan, err := PlanWorkspace(pack, WorkspaceOptions{Path: filepath.Join(t.TempDir(), "default")})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Template != "morathustra" {
		t.Fatal("default changed")
	}
}

func writeMinaFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func minaTemplate(t *testing.T, pack *Pack) *ManifestTemplate {
	t.Helper()
	for i := range pack.Manifest.Templates {
		if pack.Manifest.Templates[i].Name == "mina" {
			return &pack.Manifest.Templates[i]
		}
	}
	t.Fatal("missing MINA template")
	return nil
}

func TestMinaRejectsPrivateRuntimeAndDuplicatePersonalitySource(t *testing.T) {
	for _, name := range []string{
		"MINA.md", "MINA.backup.md", "MORA.md", "SOUL.md", "IDENTITY.md", "protocols/personality.md",
		"memory/INDEX.md", "memories/private.md", "integrations/README.md", ".hermes/config.yaml",
		".hermes/state.db-wal", ".hermes/skills/created/SKILL.md", ".hermes/sessions/session.md",
		".git/config", ".env", ".ssh/key", "credentials.md", "skills/created/SKILL.md",
	} {
		t.Run(name, func(t *testing.T) {
			pack := copyMorathustraPack(t) // Copies the complete pack; never a live profile.
			writeMinaFixture(t, filepath.Join(pack.Root.Path, "templates/mina"), name, "synthetic private state must not be copied")
			minaTemplate(t, pack).Files = append(minaTemplate(t, pack).Files, name)
			assertMinaSourceRefused(t, pack, "synthetic private state must not be copied")
		})
	}
	for i, content := range []string{
		"/srv/loom/agents/mina", "/home/synthetic/private", "/Users/synthetic/private", "/etc/fixture",
		"/var/lib/fixture", "/Volumes/fixture", `C:\Users\fixture`, `\\fixture\share`,
		"postgresql://synthetic@invalid/db", "sqlite://fixture", "pass://synthetic/item", "LOOM_PROVENANCE_DB_URL",
		"token=synthetic-not-secret", `{"refresh_token":"synthetic-not-secret"}`, "-----BEGIN PRIVATE KEY-----",
		"binary\x00fixture", "invalid\xffUTF8", ".loom/state", "checkpoint.json",
	} {
		t.Run(fmt.Sprintf("content-%d", i), func(t *testing.T) {
			pack := copyMorathustraPack(t)
			name := "protocols/extra.md"
			writeMinaFixture(t, filepath.Join(pack.Root.Path, "templates/mina"), name, content)
			minaTemplate(t, pack).Files = append(minaTemplate(t, pack).Files, name)
			assertMinaSourceRefused(t, pack, content)
		})
	}
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("undeclared-directory-%v", directory), func(t *testing.T) {
			pack := copyMorathustraPack(t)
			path := filepath.Join(pack.Root.Path, "templates/mina/undeclared.md")
			if directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeMinaFixture(t, filepath.Dir(path), filepath.Base(path), "not declared")
			}
			assertMinaSourceRefused(t, pack, "")
		})
	}
}

func assertMinaSourceRefused(t *testing.T, pack *Pack, private string) {
	t.Helper()
	report := Validate(pack)
	if report.Valid() || !hasValidationCode(report, "template.unsafe") {
		t.Fatalf("unsafe MINA source accepted: %#v", report)
	}
	destination := filepath.Join(t.TempDir(), "mina")
	_, err := PlanWorkspace(pack, WorkspaceOptions{Template: "mina", Path: destination})
	if err == nil {
		t.Fatal("unsafe MINA source planned")
	}
	if len(private) > 15 && strings.Contains(err.Error(), private) {
		t.Fatal("private content echoed")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("rejected plan wrote output")
	}
}

func TestMinaSoulAndNamedProtocolsCannotBeOmittedOrEmpty(t *testing.T) {
	for _, name := range []string{".hermes/SOUL.md", "protocols/BASECAMP.md", "protocols/GITHUB.md"} {
		t.Run(name, func(t *testing.T) {
			pack := copyMorathustraPack(t)
			template := minaTemplate(t, pack)
			template.Files = slices.DeleteFunc(template.Files, func(s string) bool { return s == name })
			if err := os.Remove(filepath.Join(pack.Root.Path, template.Path, name)); err != nil {
				t.Fatal(err)
			}
			assertMinaSourceRefused(t, pack, "")
		})
	}
	for i, content := range []string{"", "# MINA\n", "<!-- only a comment -->\n", "<!-- unterminated\nprivate"} {
		t.Run(fmt.Sprintf("empty-%d", i), func(t *testing.T) {
			pack := copyMorathustraPack(t)
			writeMinaFixture(t, filepath.Join(pack.Root.Path, "templates/mina"), ".hermes/SOUL.md", content)
			assertMinaSourceRefused(t, pack, "")
		})
	}
}

func TestMinaRefusesExistingRuntimeOrWritableInstalledStateWithoutMutation(t *testing.T) {
	for _, name := range []string{"MINA.md", "MORA.md", "SOUL.md", "memory/INDEX.md", ".hermes/config.yaml", ".hermes/state.db", ".hermes/auth.json", ".hermes/skills/created/SKILL.md", ".git/config", ".config/basecamp/credentials.json"} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "mina")
			writeMinaFixture(t, destination, name, "synthetic state must survive")
			before := workspaceSnapshot(t, destination)
			if _, err := PlanWorkspace(loadRepositoryPack(t), WorkspaceOptions{Template: "mina", Path: destination}); err == nil {
				t.Fatal("existing runtime/legacy state accepted")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, destination)) {
				t.Fatal("rejection changed user state")
			}
		})
	}
	for _, name := range []string{"skills", "skills/installed", "skills/installed/README.md", ".hermes/SOUL.md"} {
		t.Run(name, func(t *testing.T) {
			pack := loadRepositoryPack(t)
			destination := filepath.Join(t.TempDir(), "mina")
			allowWorkspaceCleanup(t, destination)
			options := WorkspaceOptions{Template: "mina", Path: destination}
			plan, err := PlanWorkspace(pack, options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyWorkspace(plan, true); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(destination, name)
			if name == ".hermes/SOUL.md" {
				writeMinaFixture(t, destination, name, "<!-- not a personality -->")
			} else if err := os.Chmod(path, 0755); err != nil {
				t.Fatal(err)
			}
			before := workspaceSnapshot(t, destination)
			if _, err := PlanWorkspace(pack, options); err == nil {
				t.Fatal("unsafe existing ownership/personality accepted")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, destination)) {
				t.Fatal("rejection repaired user state")
			}
		})
	}
}

func TestMinaWholeTreeRejectsLinksAndSpecialEntriesBeforeAndAfterPlan(t *testing.T) {
	for _, location := range []string{"source", "destination"} {
		for _, kind := range []string{"file-symlink", "directory-symlink", "fifo", "socket"} {
			t.Run(location+"-"+kind, func(t *testing.T) {
				pack := copyMorathustraPack(t)
				destination := filepath.Join(t.TempDir(), "mina")
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
				options := WorkspaceOptions{Template: "mina", Path: destination}
				plan, err := PlanWorkspace(pack, options)
				if err != nil {
					t.Fatal(err)
				}
				root := destination
				if location == "source" {
					root = filepath.Join(pack.Root.Path, "templates/mina")
				}
				outside := t.TempDir()
				writeMinaFixture(t, outside, "sentinel.md", "untouched")
				before := workspaceSnapshot(t, outside)
				path := filepath.Join(root, "unexpected.md")
				switch kind {
				case "file-symlink":
					err = os.Symlink(filepath.Join(outside, "sentinel.md"), path)
				case "directory-symlink":
					err = os.Symlink(outside, path)
				case "fifo":
					err = syscall.Mkfifo(path, 0600)
				case "socket":
					// macOS UNIX socket paths are short: bind a task-owned short path then move.
					short, e := os.MkdirTemp("", "mina-sock-")
					if e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() { _ = os.RemoveAll(short) })
					listener, e := net.Listen("unix", filepath.Join(short, "s"))
					if e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() { _ = listener.Close() })
					err = os.Rename(filepath.Join(short, "s"), path)
				}
				if err != nil {
					t.Fatal(err)
				}
				if location == "source" && Validate(pack).Valid() {
					t.Fatal("special source validated")
				}
				if _, err := PlanWorkspace(pack, options); err == nil {
					t.Fatal("special entry planned")
				}
				if _, err := ApplyWorkspace(plan, true); err == nil {
					t.Fatal("stale plan accepted special entry")
				}
				if !reflect.DeepEqual(before, workspaceSnapshot(t, outside)) {
					t.Fatal("outside state changed")
				}
				if _, err := os.Lstat(filepath.Join(destination, "AGENTS.md")); !os.IsNotExist(err) {
					t.Fatal("rejected apply wrote instructions")
				}
			})
		}
	}
}

func TestMinaRevalidatesSourceDestinationAndReviewedActions(t *testing.T) {
	for _, kind := range []string{"source-content", "source-root", "source-parent", "source-file", "destination-root", "destination-file", "destination-runtime", "forged-action", "forged-template"} {
		t.Run(kind, func(t *testing.T) {
			pack := copyMorathustraPack(t)
			destination := filepath.Join(t.TempDir(), "mina")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "mina", Path: destination})
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			writeMinaFixture(t, outside, "sentinel.md", "untouched")
			source := filepath.Join(pack.Root.Path, "templates/mina")
			link := func(path, target string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "source-content":
				writeMinaFixture(t, source, "WORKFLOW.md", "Changed after review.")
			case "source-root", "source-parent":
				path := source
				if kind == "source-parent" {
					path = filepath.Join(source, "protocols")
				}
				moved := filepath.Join(outside, "moved")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				link(path, moved)
			case "source-file":
				path := filepath.Join(source, "WORKFLOW.md")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				link(path, filepath.Join(outside, "sentinel.md"))
			case "destination-root":
				if err := os.Remove(destination); err != nil {
					t.Fatal(err)
				}
				link(destination, outside)
			case "destination-file":
				link(filepath.Join(destination, "AGENTS.md"), filepath.Join(outside, "sentinel.md"))
			case "destination-runtime":
				writeMinaFixture(t, destination, ".hermes/config.yaml", "synthetic runtime config")
			case "forged-action":
				plan.Actions[len(plan.Actions)-1].DestinationPath = filepath.Join(outside, "sentinel.md")
			case "forged-template":
				plan.Template = "morathustra"
			}
			before := workspaceSnapshot(t, outside)
			if _, err := ApplyWorkspace(plan, true); err == nil {
				t.Fatal("changed plan applied")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, outside)) {
				t.Fatal("outside content changed")
			}
			if _, err := os.Lstat(filepath.Join(destination, "WORKFLOW.md")); !os.IsNotExist(err) {
				t.Fatal("rejected plan wrote output")
			}
		})
	}
}
