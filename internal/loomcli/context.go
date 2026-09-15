package loomcli

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/filesystemlayout"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/setup"
)

type outputMode string

const (
	outputTable outputMode = "table"
	outputJSON  outputMode = "json"
	outputPlain outputMode = "plain"
)

type commandContext struct {
	Config        config.Config
	Filesystem    filesystemlayout.Layout
	Client        localclient.Client
	CorrelationID string
	OutputMode    outputMode
	Mode          ui.Mode
	Verbose       bool
	StartedAt     time.Time
}

func resolveCommandContext(opts *options) (commandContext, error) {
	mode, err := resolveOutputMode(opts)
	if err != nil {
		return commandContext{}, err
	}
	richMode, err := resolveMode(opts)
	if err != nil {
		return commandContext{}, err
	}
	cfg, err := resolveCLIConfig(opts)
	if err != nil {
		return commandContext{}, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err)
	}
	client, err := resolveCLIClient(cfg, opts)
	if err != nil {
		return commandContext{}, loomerrors.Wrap("config.invalid", "runtime", "transport", "Configuration transport is invalid.", err)
	}
	layout, err := cfg.FilesystemLayout()
	if err != nil {
		return commandContext{}, loomerrors.Wrap("config.invalid", "runtime", "filesystem", "Filesystem layout is invalid.", err)
	}
	return commandContext{
		Config:        cfg,
		Filesystem:    layout,
		Client:        client,
		CorrelationID: correlation.Normalize(opts.correlationID),
		OutputMode:    mode,
		Mode:          richMode,
		Verbose:       opts.verboseOutput,
		StartedAt:     time.Now().UTC(),
	}, nil
}

func resolveCLIConfig(opts *options) (config.Config, error) {
	if opts == nil {
		opts = &options{}
	}
	if manifest, ok := explicitInstallManifest(opts.configFile); ok {
		return config.Load(overridesFromInstallManifest(manifest, opts))
	}
	if manifest, ok := explicitInstallManifest(os.Getenv("LOOM_CONFIG_FILE")); ok {
		return config.Load(overridesFromInstallManifest(manifest, opts))
	}
	if strings.TrimSpace(opts.configFile) == "" && strings.TrimSpace(os.Getenv("LOOM_CONFIG_FILE")) == "" {
		if manifest, ok := defaultInstallManifest(); ok {
			return config.Load(overridesFromInstallManifest(manifest, opts))
		}
	}
	return config.Load(config.Overrides{
		ConfigFile: opts.configFile,
		SocketPath: opts.socketPath,
	})
}

func resolveCLIClient(cfg config.Config, opts *options) (localclient.Client, error) {
	if opts != nil && strings.TrimSpace(opts.socketPath) != "" {
		return localclient.New(cfg.SocketPath), nil
	}
	if strings.TrimSpace(os.Getenv("LOOM_MAIN_URL")) != "" && strings.TrimSpace(cfg.MainURL) != "" {
		return localclient.NewHTTP(cfg.MainURL)
	}
	if cfg.NodeKind != "main" && strings.TrimSpace(cfg.MainURL) != "" {
		return localclient.NewHTTP(cfg.MainURL)
	}
	return localclient.New(cfg.SocketPath), nil
}

func explicitInstallManifest(path string) (setup.InstallManifest, bool) {
	path = strings.TrimSpace(path)
	if path == "" || filepath.Base(path) != "install.yaml" {
		return setup.InstallManifest{}, false
	}
	manifest, err := setup.ReadManifest(path)
	if err != nil {
		return setup.InstallManifest{}, false
	}
	return manifest, true
}

func defaultInstallManifest() (setup.InstallManifest, bool) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return setup.InstallManifest{}, false
	}
	for _, path := range []string{
		filepath.Join(home, ".config", "loom", "install.yaml"),
		"/etc/loom/install.yaml",
	} {
		manifest, ok := explicitInstallManifest(path)
		if ok {
			return manifest, true
		}
	}
	return setup.InstallManifest{}, false
}

func overridesFromInstallManifest(manifest setup.InstallManifest, opts *options) config.Overrides {
	nodeID := firstNonEmpty(manifest.NodeID, manifest.NodeKey)
	overrides := config.Overrides{
		Env:             "production",
		NodeID:          nodeID,
		NodeKind:        manifest.NodeKind,
		NodeRole:        manifest.NodeRole,
		RuntimeClass:    manifest.RuntimeClass,
		DataDir:         manifest.DataDir,
		ObjectStore:     manifest.ObjectStorePath,
		ServiceRoot:     manifest.ServiceRoot,
		StorageRoot:     manifest.StorageRoot,
		ImportsRoot:     manifest.ImportsRoot,
		UserBackupsRoot: manifest.UserBackupsRoot,
		ArchiveRoot:     manifest.ArchiveRoot,
		GeneratedRoot:   manifest.GeneratedRoot,
		BoxStateRoot:    manifest.BoxStateRoot,
		MainDocuments:   manifest.MainDocumentsPath,
		StorageExport:   manifest.StorageExportRoot,
		BoxPath:         manifest.BoxPath,
		BoxProfile:      manifest.BoxProfile,
		MainURL:         manifest.MainURL,
		SocketPath:      manifest.SocketPath,
		HTTPListenAddr:  manifest.HTTPListenAddr,
		MigrationsDir:   manifest.MigrationsDir,
		BootstrapMode:   manifest.BootstrapMode,
	}
	if overrides.StorageRetention == "" && manifest.DataDir != "" {
		overrides.StorageRetention = filepath.Join(manifest.DataDir, "storage-retention")
	}
	if overrides.MainDocuments == "" && manifest.DataDir != "" {
		overrides.MainDocuments = filepath.Join(manifest.DataDir, "main-documents")
	}
	if overrides.ObjectStore == "" && manifest.DataDir != "" {
		overrides.ObjectStore = filepath.Join(manifest.DataDir, "object-store")
	}
	if overrides.MigrationsDir == "" {
		overrides.MigrationsDir = config.DefaultMigrationsDir
	}
	if overrides.BootstrapMode == "" {
		overrides.BootstrapMode = config.DefaultBootstrapMode
	}
	if opts != nil {
		if strings.TrimSpace(opts.socketPath) != "" {
			overrides.SocketPath = opts.socketPath
		}
	}
	return overrides
}

func resolveMode(opts *options) (ui.Mode, error) {
	mode, err := resolveOutputMode(opts)
	if err != nil {
		return ui.Mode{}, err
	}
	if opts == nil {
		opts = &options{}
	}
	env := ui.FromOS()
	return ui.ResolveMode(ui.ResolveModeOptions{
		Output:        ui.OutputMode(mode.String()),
		NoInteractive: opts.noInteractive,
		NoColor:       opts.noColor,
		NoAnimation:   opts.noAnimation,
		ThemeName:     opts.theme,
		Compact:       opts.compact,
		Env:           env,
		Terminal:      ui.DetectTerminal(ui.DefaultStreams(), env),
	})
}

func resolveOutputMode(opts *options) (outputMode, error) {
	if opts == nil {
		return outputTable, nil
	}
	if opts.jsonOutput && opts.plainOutput {
		return "", loomerrors.New("output.invalid", "cli", "output", "Choose only one output mode.")
	}
	if opts.jsonOutput {
		return outputJSON, nil
	}
	if opts.plainOutput {
		return outputPlain, nil
	}
	return outputTable, nil
}

func (m outputMode) String() string {
	if strings.TrimSpace(string(m)) == "" {
		return string(outputTable)
	}
	return string(m)
}

func withEffectIdempotency(client localclient.Client, explicitKey, operation string) (localclient.Client, string) {
	key := resolveIdempotencyKey(explicitKey, operation)
	return client.WithIdempotencyKey(key), key
}

func resolveIdempotencyKey(explicitKey, operation string) string {
	if key := strings.TrimSpace(explicitKey); key != "" {
		return key
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "operation"
	}
	replacer := strings.NewReplacer(".", "_", "/", "_", "@", "_", " ", "_")
	return replacer.Replace(operation) + "_" + ids.NewIdempotencyID()
}
