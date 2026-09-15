package loomcli

import (
	"context"
	"encoding/json"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/bootstrapssh"
)

type bootstrapSSHFlags struct {
	sshUser              string
	sshPort              int
	sshKey               string
	kind                 string
	role                 string
	runtimeClass         string
	nodeKey              string
	displayName          string
	mainURL              string
	mainHost             string
	mainSSHUser          string
	mainSSHPort          int
	mainSSHKey           string
	mainSocket           string
	source               string
	sourcePath           string
	gitURL               string
	gitRef               string
	remoteSourceDir      string
	installMode          string
	packageMode          string
	serviceManager       string
	boxPath              string
	boxProfile           string
	homeDir              string
	userName             string
	configDir            string
	dataDir              string
	stateDir             string
	logDir               string
	serviceRoot          string
	storageRoot          string
	importsRoot          string
	userBackupsRoot      string
	archiveRoot          string
	generatedRoot        string
	boxStateRoot         string
	objectStore          string
	mainDocumentsPath    string
	socketPath           string
	httpListenAddr       string
	dbURL                string
	migrationsDir        string
	bootstrapMode        string
	enrollmentTTLSeconds int
	dryRun               bool
	yes                  bool
	resume               bool
}

func newBootstrapCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap LOOM nodes",
	}
	cmd.AddCommand(newBootstrapSSHCommand(opts))
	return cmd
}

func newBootstrapSSHCommand(opts *options) *cobra.Command {
	flags := bootstrapSSHFlags{}
	cmd := &cobra.Command{
		Use:   "ssh <target>",
		Short: "Plan and deliver LOOM source to a trusted SSH target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := bootstrapssh.Spec{
				TargetHost:           args[0],
				SSHUser:              flags.sshUser,
				SSHPort:              flags.sshPort,
				SSHKeyPath:           flags.sshKey,
				NodeKey:              flags.nodeKey,
				DisplayName:          flags.displayName,
				NodeKind:             flags.kind,
				NodeRole:             flags.role,
				RuntimeClass:         flags.runtimeClass,
				MainURL:              flags.mainURL,
				MainHost:             flags.mainHost,
				MainSSHUser:          flags.mainSSHUser,
				MainSSHPort:          flags.mainSSHPort,
				MainSSHKeyPath:       flags.mainSSHKey,
				MainSocket:           flags.mainSocket,
				SourceMode:           flags.source,
				SourcePath:           flags.sourcePath,
				GitURL:               flags.gitURL,
				GitRef:               flags.gitRef,
				RemoteSourceDir:      flags.remoteSourceDir,
				InstallMode:          flags.installMode,
				PackageMode:          flags.packageMode,
				ServiceManager:       flags.serviceManager,
				BoxPath:              flags.boxPath,
				BoxProfile:           flags.boxProfile,
				HomeDir:              flags.homeDir,
				UserName:             flags.userName,
				ConfigDir:            flags.configDir,
				DataDir:              flags.dataDir,
				StateDir:             flags.stateDir,
				LogDir:               flags.logDir,
				ServiceRoot:          flags.serviceRoot,
				StorageRoot:          flags.storageRoot,
				ImportsRoot:          flags.importsRoot,
				UserBackupsRoot:      flags.userBackupsRoot,
				ArchiveRoot:          flags.archiveRoot,
				GeneratedRoot:        flags.generatedRoot,
				BoxStateRoot:         flags.boxStateRoot,
				ObjectStorePath:      flags.objectStore,
				MainDocumentsPath:    flags.mainDocumentsPath,
				SocketPath:           flags.socketPath,
				HTTPListenAddr:       flags.httpListenAddr,
				DBURL:                flags.dbURL,
				MigrationsDir:        flags.migrationsDir,
				BootstrapMode:        flags.bootstrapMode,
				EnrollmentTTLSeconds: flags.enrollmentTTLSeconds,
				DryRun:               flags.dryRun,
				Yes:                  flags.yes,
				Resume:               flags.resume,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := bootstrapssh.Run(ctx, bootstrapssh.RunInput{Spec: spec})
			if opts.jsonOutput {
				if result.Plan.Spec.TargetHost != "" {
					encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
					if err != nil {
						return err
					}
					return encodeErr
				}
				return err
			}
			if result.Plan.Spec.TargetHost != "" {
				bootstrapssh.Render(cmd.OutOrStdout(), result)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&flags.sshUser, "ssh-user", "", "SSH user")
	cmd.Flags().IntVar(&flags.sshPort, "ssh-port", 0, "SSH port")
	cmd.Flags().StringVar(&flags.sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&flags.kind, "kind", "", "node kind")
	cmd.Flags().StringVar(&flags.role, "role", "", "node role")
	cmd.Flags().StringVar(&flags.runtimeClass, "runtime-class", "", "runtime class")
	cmd.Flags().StringVar(&flags.nodeKey, "node-key", "", "node key")
	cmd.Flags().StringVar(&flags.displayName, "display-name", "", "display name")
	cmd.Flags().StringVar(&flags.mainURL, "main-url", "", "main node private URL")
	cmd.Flags().StringVar(&flags.mainHost, "main-host", "", "main SSH/private host used to derive main URL when --main-url is omitted")
	cmd.Flags().StringVar(&flags.mainSSHUser, "main-ssh-user", "", "main SSH user when --main-host is used")
	cmd.Flags().IntVar(&flags.mainSSHPort, "main-ssh-port", 0, "main SSH port when --main-host is used")
	cmd.Flags().StringVar(&flags.mainSSHKey, "main-ssh-key", "", "main SSH private key path when --main-host is used")
	cmd.Flags().StringVar(&flags.mainSocket, "main-socket", "", "main loomd socket path for local or SSH main commands")
	cmd.Flags().StringVar(&flags.source, "source", "current", "source mode: current or git-clone")
	cmd.Flags().StringVar(&flags.sourcePath, "source-path", "", "local source path for current source mode")
	cmd.Flags().StringVar(&flags.gitURL, "git-url", "", "git URL for git-clone planning")
	cmd.Flags().StringVar(&flags.gitRef, "git-ref", "", "git ref for git-clone planning")
	cmd.Flags().StringVar(&flags.remoteSourceDir, "remote-source-dir", "", "remote LOOM source directory")
	cmd.Flags().StringVar(&flags.installMode, "install-mode", "", "remote install mode")
	cmd.Flags().StringVar(&flags.packageMode, "package-mode", "", "remote package mode")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", "", "remote service manager")
	cmd.Flags().StringVar(&flags.boxPath, "box-path", "", "remote LOOM Box path")
	cmd.Flags().StringVar(&flags.boxProfile, "box-profile", "", "remote LOOM Box profile")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "remote home directory override")
	cmd.Flags().StringVar(&flags.userName, "user-name", "", "remote user name override")
	cmd.Flags().StringVar(&flags.configDir, "config-dir", "", "remote config directory")
	cmd.Flags().StringVar(&flags.dataDir, "data-dir", "", "remote data directory")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "remote state directory")
	cmd.Flags().StringVar(&flags.logDir, "log-dir", "", "remote log directory")
	cmd.Flags().StringVar(&flags.serviceRoot, "service-root", "", "remote canonical service root")
	cmd.Flags().StringVar(&flags.storageRoot, "storage-root", "", "remote canonical physical storage root")
	cmd.Flags().StringVar(&flags.importsRoot, "imports-root", "", "remote canonical imports root")
	cmd.Flags().StringVar(&flags.userBackupsRoot, "user-backups-root", "", "remote canonical user-backups root")
	cmd.Flags().StringVar(&flags.archiveRoot, "archive-root", "", "remote canonical archive root")
	cmd.Flags().StringVar(&flags.generatedRoot, "generated-root", "", "remote generated-artifact root")
	cmd.Flags().StringVar(&flags.boxStateRoot, "box-state-root", "", "remote external Box runtime-state root")
	cmd.Flags().StringVar(&flags.objectStore, "object-store", "", "remote main object store path")
	cmd.Flags().StringVar(&flags.mainDocumentsPath, "main-documents-root", "", "deprecated legacy main Documents migration input")
	cmd.Flags().StringVar(&flags.socketPath, "socket-path", "", "remote main loomd socket path")
	cmd.Flags().StringVar(&flags.httpListenAddr, "http-listen-addr", "", "remote main private HTTP listen address")
	cmd.Flags().StringVar(&flags.dbURL, "db-url", "", "remote main database URL used for migration/bootstrap")
	cmd.Flags().StringVar(&flags.migrationsDir, "migrations-dir", "", "remote database migrations directory")
	cmd.Flags().StringVar(&flags.bootstrapMode, "bootstrap-mode", "", "remote bootstrap mode: none, dev, production")
	cmd.Flags().IntVar(&flags.enrollmentTTLSeconds, "enrollment-ttl-seconds", 1800, "one-time enrollment token lifetime in seconds for non-main SSH bootstrap")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "collect facts and render plan without copying source")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm source delivery")
	cmd.Flags().BoolVar(&flags.resume, "resume", false, "resume from existing remote state where future slices support it")
	return cmd
}
