package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/hermesprofile"
)

func TestMinaCLIRejectsIdentityConflictsBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"--identity", "MINA"},
		{"--identity", "mina", "--workspace", hermesprofile.WorkspaceRoot},
		{"--workspace", hermesprofile.MinaWorkspaceRoot},
		{"--identity", "mina", "--mode", "publish", "--fixture"},
		{"--mode", "publish", "--fixture"},
		{"--mode", "publish", "--identity", "mina", "--workspace", "/tmp/.loom-acceptance/profile"},
		{"--mode", "publish", "--identity", "mina", "--workspace", hermesprofile.MinaWorkspaceRoot + "/.loom-acceptance/profile", "--fixture"},
		{"--identity", "mina", "--workspace", hermesprofile.MinaWorkspaceRoot + "/"},
	} {
		var output bytes.Buffer
		if err := runArgs(args, &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid invocation accepted: %v %v", args, err)
		}
	}
}

func TestMinaNativeRecoveryCLIBackupVerifyImport(t *testing.T) {
	binary, python := os.Getenv("LOOM_TEST_HERMES_BINARY"), os.Getenv("LOOM_TEST_HERMES_PYTHON")
	if binary == "" || python == "" {
		t.Skip("required native gate: run tests/hermes_mina_recovery_local.sh with existing pinned tooling")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, ".loom-acceptance", "native-recovery")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "fixture-signing-key")
	if err := os.WriteFile(keyFile, private, 0600); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"morathustra", "mina"} {
		t.Run(identity, func(t *testing.T) {
			workspace := filepath.Join(root, identity)
			home := filepath.Join(workspace, ".hermes")
			if err := os.MkdirAll(filepath.Join(workspace, "recovery"), 0700); err != nil {
				t.Fatal(err)
			}
			setup := exec.Command(python, "-c", `import pathlib,sqlite3,sys
h=pathlib.Path(sys.argv[1]);h.mkdir(mode=0o700)
for p,v in {'SOUL.md':'synthetic '+sys.argv[2], 'config.yaml':'memory: {}\n', 'memories/MEMORY.md':'retained memory', 'skills/created/SKILL.md':'approved skill', 'sessions/a.json':'{"fixture":"session"}'}.items():
 f=h/p;f.parent.mkdir(parents=True,exist_ok=True);f.write_text(v);f.chmod(0o600)
c=sqlite3.connect(h/'state.db');c.execute('pragma journal_mode=wal');c.execute('create table fixture(k integer primary key,v text)');c.execute("insert into fixture values(1,'committed native recovery')");c.commit();c.close();(h/'state.db').chmod(0o600)
(h/'browser-profiles').mkdir();(h/'browser-profiles/credential-sentinel').write_text('SYNTHETIC_EXCLUDED_SENTINEL=1')
`, home, identity)
			setup.Env = []string{"HOME=" + root, "HERMES_HOME=" + home, "PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1"}
			if out, err := setup.CombinedOutput(); err != nil {
				t.Fatalf("fixture setup: %v %s", err, out)
			}
			created := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano)
			args := []string{"--mode", "publish", "--identity", identity, "--workspace", workspace, "--fixture", "--hermes", binary, "--id", "native-" + identity, "--created-at", created, "--signing-key-file", keyFile}
			var out bytes.Buffer
			if err := runArgs(args, &out); err != nil {
				t.Fatal(err)
			}
			var evidence hermesprofile.Evidence
			if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
				t.Fatal(err)
			}
			origin, _ := hermesprofile.RecoveryIdentity(identity).WorkspaceRoot()
			if _, err := hermesprofile.Verify(context.Background(), evidence.Path, origin, public); err != nil {
				t.Fatal(err)
			}
			if err := runArgs([]string{"--mode", "verify", "--workspace", origin, "--identity", identity, "--package", evidence.Path, "--public-key", hex.EncodeToString(public)}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if err := runArgs([]string{"--workspace", origin, "--package", evidence.Path, "--public-key", hex.EncodeToString(public)}, &bytes.Buffer{}); err != nil {
				t.Fatal("explicit original workspace verification:", err)
			}
			if err := runArgs([]string{"--identity", identity, "--workspace", workspace, "--package", evidence.Path, "--public-key", hex.EncodeToString(public)}, &bytes.Buffer{}); err == nil {
				t.Fatal("physical path silently substituted for requested origin")
			}
			policy := hermesprofile.Policy{Enabled: true, Identity: hermesprofile.RecoveryIdentity(identity), Workspace: workspace, PublicKey: public}
			if evidence, err := hermesprofile.Check(context.Background(), policy, time.Now().UTC()); err != nil || len(evidence) != 1 {
				t.Fatalf("single producer policy: %v %v", evidence, err)
			}
			// Exact native replay verifies the signed request and leaves immutable bytes.
			before, err := os.ReadFile(filepath.Join(evidence.Path, hermesprofile.ManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			if err := runArgs(args, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(evidence.Path, hermesprofile.ManifestFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("replay changed native evidence")
			}
			wrong := "mina"
			if identity == "mina" {
				wrong = "morathustra"
			}
			if err := runArgs([]string{"--mode", "verify", "--identity", wrong, "--package", evidence.Path, "--public-key", hex.EncodeToString(public)}, &bytes.Buffer{}); err == nil {
				t.Fatal("CLI origin fallback")
			}
			// For legacy callers, omitted identity still binds Morathustra exactly.
			defaultErr := runArgs([]string{"--package", evidence.Path, "--public-key", hex.EncodeToString(public)}, &bytes.Buffer{})
			if (defaultErr == nil) != (identity == "morathustra") {
				t.Fatal("default verification identity changed")
			}
			restored := filepath.Join(root, "import-"+identity, ".hermes")
			check := exec.Command(python, "-c", `import pathlib,sqlite3,sys,zipfile
from types import SimpleNamespace
from hermes_cli.backup import run_import
run_import(SimpleNamespace(zipfile=sys.argv[1],force=True))
h=pathlib.Path(sys.argv[2]);source=pathlib.Path(sys.argv[3])
c=sqlite3.connect('file:'+str(h/'state.db')+'?mode=ro',uri=True)
assert c.execute('pragma integrity_check').fetchone()==('ok',)
assert c.execute('select * from fixture').fetchall()==[(1,'committed native recovery')]
with zipfile.ZipFile(sys.argv[1]) as z:
 assert not any(p.startswith('browser-profiles/') for p in z.namelist())
 assert not any('fixture-signing-key' in p for p in z.namelist())
 for p in ('SOUL.md','config.yaml','memories/MEMORY.md','skills/created/SKILL.md','sessions/a.json'):
  assert (h/p).read_bytes()==z.read(p)==(source/p).read_bytes()
assert not (h/'browser-profiles').exists()
`, filepath.Join(evidence.Path, hermesprofile.PayloadFile), restored, home)
			check.Env = []string{"HOME=" + filepath.Dir(restored), "HERMES_HOME=" + restored, "HERMES_MANAGED=true", "PYTHONDONTWRITEBYTECODE=1", "PATH=/usr/bin:/bin"}
			if out, err := check.CombinedOutput(); err != nil {
				t.Fatalf("native import: %v %s", err, out)
			}
			if strings.Contains(string(before), "SYNTHETIC_EXCLUDED_SENTINEL") {
				t.Fatal("credential sentinel in manifest")
			}
			t.Logf("%s native publish/replay/exact verify/import: %d ZIP bytes", identity, evidence.Files[1].Size)
		})
	}
	// The same native legacy bytes now live below the explicitly selected MINA
	// policy. They are retained by ID; only the MINA package supplies freshness.
	mina := filepath.Join(root, "mina")
	old := filepath.Join(root, "morathustra", "recovery", "native-morathustra")
	before, err := os.ReadFile(filepath.Join(old, hermesprofile.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(mina, "recovery", "native-morathustra")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	policy := hermesprofile.Policy{Enabled: true, Workspace: mina, Identity: hermesprofile.MinaIdentity, PublicKey: public, RetainedMorathustra: []string{"native-morathustra"}}
	roots := []backupstrategy.DirectArchiveRoot{{Name: "current", Path: mina}, {Name: "retired", Path: hermesprofile.WorkspaceRoot}}
	exclusions, evidence, err := backupstrategy.HermesArchiveBoundary(context.Background(), roots, nil, policy, time.Now().UTC())
	if err != nil || len(evidence) != 2 {
		t.Fatalf("native transition policy: %v %v", evidence, err)
	}
	want := []backupstrategy.DirectArchiveExclusion{{Root: "current", RelativePath: ".hermes"}, {Root: "current", RelativePath: "recovery/.staging"}, {Root: "retired", RelativePath: ".hermes"}, {Root: "retired", RelativePath: "recovery"}}
	if !reflect.DeepEqual(exclusions, want) {
		t.Fatalf("private boundary: %v", exclusions)
	}
	after, err := os.ReadFile(filepath.Join(moved, hermesprofile.ManifestFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("native retained manifest rewritten")
	}
	if err := runArgs([]string{"--workspace", hermesprofile.WorkspaceRoot, "--package", moved, "--public-key", hex.EncodeToString(public)}, &bytes.Buffer{}); err != nil {
		t.Fatal("relocated native exact-origin CLI verify:", err)
	}
	policy.RetainedMorathustra = nil
	if _, _, err := backupstrategy.HermesArchiveBoundary(context.Background(), roots, nil, policy, time.Now().UTC()); err == nil {
		t.Fatal("native retained origin admitted without binding")
	}

}
