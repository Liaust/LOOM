package loomcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/setup"
)

func TestResolveOutputModeDefaultsToTable(t *testing.T) {
	mode, err := resolveOutputMode(&options{})
	if err != nil {
		t.Fatalf("resolveOutputMode returned error: %v", err)
	}
	if mode != outputTable {
		t.Fatalf("mode = %q, want %q", mode, outputTable)
	}
}

func TestResolveOutputModeRejectsJSONAndPlain(t *testing.T) {
	_, err := resolveOutputMode(&options{jsonOutput: true, plainOutput: true})
	if err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestResolveModeKeepsJSONMachineSafe(t *testing.T) {
	mode, err := ui.ResolveMode(ui.ResolveModeOptions{
		Output: ui.OutputJSON,
		Env:    ui.Env{},
		Terminal: ui.TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        120,
			Height:       40,
			Term:         "xterm-256color",
			ColorProfile: ui.ColorTrueColor,
		},
	})
	if err != nil {
		t.Fatalf("ResolveMode returned error: %v", err)
	}
	if mode.CanPrompt() || mode.CanColor() || mode.CanAnimate() || mode.CanUsePortal() {
		t.Fatalf("json mode should be machine-safe, got %#v", mode)
	}
}

func TestResolveCommandContextUsesExplicitCorrelation(t *testing.T) {
	ctx, err := resolveCommandContext(&options{
		correlationID: "corr_explicit",
		socketPath:    "/tmp/loom-test.sock",
	})
	if err != nil {
		t.Fatalf("resolveCommandContext returned error: %v", err)
	}
	if ctx.CorrelationID != "corr_explicit" {
		t.Fatalf("correlation = %q, want corr_explicit", ctx.CorrelationID)
	}
	if ctx.Config.SocketPath != "/tmp/loom-test.sock" {
		t.Fatalf("socket = %q, want /tmp/loom-test.sock", ctx.Config.SocketPath)
	}
	if got, want := ctx.Filesystem.ImportsRoot.String(), "/srv/loom/storage/imports"; got != want {
		t.Fatalf("imports root = %q, want %q", got, want)
	}
	if ctx.Mode.Output != ui.OutputTable {
		t.Fatalf("rich mode output = %q, want table", ctx.Mode.Output)
	}
}

func TestResolveCommandContextUsesWorkspaceInstallManifestHTTPTransport(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "install.yaml")
	if err := setup.WriteManifest(manifestPath, setup.InstallManifest{
		SchemaVersion:     setup.ManifestSchemaVersion,
		NodeKey:           "macbook",
		NodeID:            "node_macbook",
		NodeKind:          "workspace",
		NodeRole:          "primary_workspace",
		RuntimeClass:      "workspace_full",
		MainURL:           "http://10.44.0.2:8080",
		DataDir:           filepath.Join(dir, "data"),
		ServiceRoot:       filepath.Join(dir, "service"),
		StorageRoot:       filepath.Join(dir, "service", "storage"),
		ImportsRoot:       filepath.Join(dir, "service", "storage", "imports-custom"),
		UserBackupsRoot:   filepath.Join(dir, "service", "storage", "backups-custom"),
		ArchiveRoot:       filepath.Join(dir, "service", "storage", "archive-custom"),
		GeneratedRoot:     filepath.Join(dir, "data", "generated-custom"),
		BoxStateRoot:      filepath.Join(dir, "data", "box-state-custom"),
		BoxPath:           filepath.Join(dir, "loom-box"),
		BoxProfile:        "workspace",
		MigrationsDir:     "migrations",
		BootstrapMode:     "none",
		ObjectStorePath:   filepath.Join(dir, "data", "object-store"),
		MainDocumentsPath: filepath.Join(dir, "data", "main-documents"),
		StorageExportRoot: filepath.Join(dir, "data", "storage-views", "main-export"),
	}); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	ctx, err := resolveCommandContext(&options{configFile: manifestPath})
	if err != nil {
		t.Fatalf("resolveCommandContext returned error: %v", err)
	}
	if ctx.Config.MainURL != "http://10.44.0.2:8080" {
		t.Fatalf("main URL = %q, want manifest URL", ctx.Config.MainURL)
	}
	if ctx.Client.BaseURL != "http://10.44.0.2:8080" {
		t.Fatalf("client base URL = %q, want HTTP transport", ctx.Client.BaseURL)
	}
	if ctx.Client.SocketPath != "" {
		t.Fatalf("client socket path = %q, want no socket transport", ctx.Client.SocketPath)
	}
	if got, want := ctx.Filesystem.BoxRoot.String(), filepath.Join(dir, "loom-box"); got != want {
		t.Fatalf("workspace Box root = %q, want %q", got, want)
	}
	if got, want := ctx.Filesystem.GeneratedRoot.String(), filepath.Join(dir, "data", "generated-custom"); got != want {
		t.Fatalf("generated root = %q, want %q", got, want)
	}
	for name, pair := range map[string][2]string{
		"service":      {ctx.Filesystem.ServiceRoot.String(), filepath.Join(dir, "service")},
		"storage":      {ctx.Filesystem.StorageRoot.String(), filepath.Join(dir, "service", "storage")},
		"imports":      {ctx.Filesystem.ImportsRoot.String(), filepath.Join(dir, "service", "storage", "imports-custom")},
		"user backups": {ctx.Filesystem.UserBackupsRoot.String(), filepath.Join(dir, "service", "storage", "backups-custom")},
		"archive":      {ctx.Filesystem.ArchiveRoot.String(), filepath.Join(dir, "service", "storage", "archive-custom")},
		"Box state":    {ctx.Filesystem.BoxStateRoot.String(), filepath.Join(dir, "data", "box-state-custom")},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s root = %q, want %q", name, pair[0], pair[1])
		}
	}
	if got, want := ctx.Filesystem.DeprecatedStorageExportRoot.String(), filepath.Join(dir, "data", "storage-views", "main-export"); got != want {
		t.Fatalf("deprecated storage export = %q, want %q", got, want)
	}
}

func TestCommandClientUsesWorkspaceInstallManifestHTTPTransport(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "install.yaml")
	if err := setup.WriteManifest(manifestPath, setup.InstallManifest{
		SchemaVersion:     setup.ManifestSchemaVersion,
		NodeKey:           "macbook",
		NodeID:            "node_macbook",
		NodeKind:          "workspace",
		NodeRole:          "primary_workspace",
		RuntimeClass:      "workspace_full",
		MainURL:           "http://10.44.0.2:8080",
		DataDir:           filepath.Join(dir, "data"),
		BoxPath:           filepath.Join(dir, "loom-box"),
		BoxProfile:        "workspace",
		MigrationsDir:     "migrations",
		BootstrapMode:     "none",
		ObjectStorePath:   filepath.Join(dir, "data", "object-store"),
		MainDocumentsPath: filepath.Join(dir, "data", "main-documents"),
		StorageExportRoot: filepath.Join(dir, "data", "storage-views", "main-export"),
	}); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	cfg, client, err := commandClient(&options{configFile: manifestPath})
	if err != nil {
		t.Fatalf("commandClient returned error: %v", err)
	}
	if cfg.MainURL != "http://10.44.0.2:8080" {
		t.Fatalf("main URL = %q, want manifest URL", cfg.MainURL)
	}
	if client.BaseURL != "http://10.44.0.2:8080" {
		t.Fatalf("client base URL = %q, want HTTP transport", client.BaseURL)
	}
	if client.SocketPath != "" {
		t.Fatalf("client socket path = %q, want no socket transport", client.SocketPath)
	}
}

func TestResolveCLIClientUsesMainURLEnvOverrideForMainNode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "loom.env")
	if err := os.WriteFile(configPath, []byte("LOOM_NODE_KIND=main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("LOOM_MAIN_URL", "http://10.44.0.2:8080")
	cfg, client, err := commandClient(&options{configFile: configPath})
	if err != nil {
		t.Fatalf("commandClient returned error: %v", err)
	}
	if cfg.NodeKind != "main" {
		t.Fatalf("node kind = %q, want main default", cfg.NodeKind)
	}
	if client.BaseURL != "http://10.44.0.2:8080" {
		t.Fatalf("client base URL = %q, want LOOM_MAIN_URL HTTP transport", client.BaseURL)
	}
	if client.SocketPath != "" {
		t.Fatalf("client socket path = %q, want no socket transport", client.SocketPath)
	}
}

func TestResolveCLIClientSocketFlagOverridesMainURLEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "loom.env")
	if err := os.WriteFile(configPath, []byte("LOOM_NODE_KIND=main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("LOOM_MAIN_URL", "http://10.44.0.2:8080")
	_, client, err := commandClient(&options{configFile: configPath, socketPath: "/tmp/loom-test.sock"})
	if err != nil {
		t.Fatalf("commandClient returned error: %v", err)
	}
	if client.SocketPath != "/tmp/loom-test.sock" {
		t.Fatalf("client socket path = %q, want explicit socket", client.SocketPath)
	}
}

func TestResolveCLIConfigClientEnvFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOOM_CONFIG_FILE", "")
	t.Setenv("LOOM_SOCKET_PATH", "")
	if _, ok := defaultInstallManifest(); ok {
		t.Skip("host install manifest takes precedence over client-env fallback")
	}
	path := filepath.Join(t.TempDir(), "loom-client.env")
	wantSocket := filepath.Join(t.TempDir(), "client.sock")
	if err := os.WriteFile(path, []byte("LOOM_SOCKET_PATH="+wantSocket+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveCLIConfigWithClientEnv(nil, path)
	if err != nil || cfg.ConfigFile != path || cfg.SocketPath != wantSocket {
		t.Fatalf("client-env fallback: config=%+v err=%v", cfg.SafeFields(), err)
	}
	if got := os.Getenv("LOOM_CONFIG_FILE"); got != "" {
		t.Fatalf("fallback mutated process configuration: %q", got)
	}
	t.Setenv("LOOM_SOCKET_PATH", "/tmp/env.sock")
	cfg, err = resolveCLIConfigWithClientEnv(nil, path)
	if err != nil || cfg.SocketPath != "/tmp/env.sock" {
		t.Fatalf("environment lost precedence: socket=%q err=%v", cfg.SocketPath, err)
	}
	cfg, err = resolveCLIConfigWithClientEnv(&options{socketPath: "/tmp/flag.sock"}, path)
	if err != nil || cfg.SocketPath != "/tmp/flag.sock" {
		t.Fatalf("socket flag lost precedence: socket=%q err=%v", cfg.SocketPath, err)
	}
	t.Setenv("LOOM_SOCKET_PATH", "")
	cfg, err = resolveCLIConfigWithClientEnv(nil, path+"-absent")
	if err != nil || cfg.ConfigFile != "" || cfg.SocketPath != config.DefaultSocketPath {
		t.Fatalf("absent client-env changed legacy defaults: config=%+v err=%v", cfg.SafeFields(), err)
	}
	if err := os.WriteFile(path, []byte("not an env file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCLIConfigWithClientEnv(nil, path); err == nil {
		t.Fatal("invalid client configuration silently ignored")
	}
	if _, err := resolveCLIConfigWithClientEnv(nil, filepath.Dir(path)); err == nil {
		t.Fatal("non-file client configuration accepted")
	}
}

func TestResolveCLIConfigExplicitSelectionSkipsClientEnv(t *testing.T) {
	for _, selection := range []string{"flag", "env", "missing-flag", "missing-env", "explicit-manifest", "default-manifest"} {
		t.Run(selection, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("LOOM_CONFIG_FILE", "")
			t.Setenv("LOOM_SOCKET_PATH", "")
			dir := t.TempDir()
			clientEnv := filepath.Join(dir, "loom-client.env")
			if err := os.WriteFile(clientEnv, []byte("must not be read\n"), 0600); err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(dir, "chosen.env")
			wantSocket := filepath.Join(dir, "chosen.sock")
			if err := os.WriteFile(selected, []byte("LOOM_SOCKET_PATH="+wantSocket+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			opts := &options{}
			switch selection {
			case "flag":
				opts.configFile = selected
			case "env":
				t.Setenv("LOOM_CONFIG_FILE", selected)
			case "missing-flag":
				opts.configFile = selected + "-absent"
			case "missing-env":
				t.Setenv("LOOM_CONFIG_FILE", selected+"-absent")
			case "explicit-manifest", "default-manifest":
				selected = filepath.Join(dir, "install.yaml")
				if selection == "default-manifest" {
					selected = filepath.Join(os.Getenv("HOME"), ".config", "loom", "install.yaml")
				} else {
					opts.configFile = selected
				}
				if err := setup.WriteManifest(selected, setup.InstallManifest{
					SchemaVersion: setup.ManifestSchemaVersion,
					NodeID:        "main", NodeKind: "main", NodeRole: "main",
					DataDir: filepath.Join(dir, "data"), BoxPath: filepath.Join(dir, "box"),
					BoxProfile: "main", SocketPath: wantSocket,
				}); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := resolveCLIConfigWithClientEnv(opts, clientEnv)
			if strings.HasPrefix(selection, "missing-") {
				if err == nil || !strings.Contains(err.Error(), selected+"-absent") {
					t.Fatalf("missing explicit config fell back: %v", err)
				}
				return
			}
			if err != nil || cfg.SocketPath != wantSocket {
				t.Fatalf("selected config lost to fallback: socket=%q err=%v", cfg.SocketPath, err)
			}
		})
	}
}

func TestResolveIdempotencyKeyUsesExplicitKey(t *testing.T) {
	key := resolveIdempotencyKey(" retry-key ", "project.create")
	if key != "retry-key" {
		t.Fatalf("key = %q, want retry-key", key)
	}
}

func TestResolveIdempotencyKeyGeneratesOperationPrefixedKey(t *testing.T) {
	key := resolveIdempotencyKey("", "project.create")
	if key == "" {
		t.Fatal("expected generated key")
	}
	if got, want := key[:len("project_create_idempotency_")], "project_create_idempotency_"; got != want {
		t.Fatalf("key prefix = %q, want %q", got, want)
	}
}
