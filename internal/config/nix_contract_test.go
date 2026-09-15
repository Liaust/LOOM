package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestNixBaseExposesSetupIdentityAndBoxOptions(t *testing.T) {
	base := readRepoFile(t, "nix/modules/loom-base.nix")

	for _, want := range []string{
		"nodeKind = lib.mkOption",
		"runtimeClass = lib.mkOption",
		"boxPath = lib.mkOption",
		"boxProfile = lib.mkOption",
		"boxOwner = lib.mkOption",
		"boxGroup = lib.mkOption",
		"manageBoxPaths = lib.mkOption",
		"bootstrapMode = lib.mkOption",
		"workspaceArchive = {",
		"manifestKeyId = lib.mkOption",
		"manifestKeySource = lib.mkOption",
		`default = "workspace-archive-manifest-key-v1";`,
		"serviceRoot = lib.mkOption",
		"storageRoot = lib.mkOption",
		"canonicalStorageRoot = lib.mkOption",
		"importsRoot = lib.mkOption",
		"canonicalImportsRoot = lib.mkOption",
		"userBackupsRoot = lib.mkOption",
		"canonicalUserBackupsRoot = lib.mkOption",
		"archiveRoot = lib.mkOption",
		"canonicalArchiveRoot = lib.mkOption",
		"generatedRoot = lib.mkOption",
		"canonicalGeneratedRoot = lib.mkOption",
		"boxStateRoot = lib.mkOption",
		"canonicalBoxStateRoot = lib.mkOption",
		"canonicalNotesProjectionRoot = lib.mkOption",
		"canonicalBoxPath = lib.mkOption",
		"filesystemCutoverEnabled = lib.mkOption",
		"storageRetentionRoot = lib.mkOption",
		"cloud = {",
		"installRclone = lib.mkOption",
		"installBorg = lib.mkOption",
		"snapshotBackend = lib.mkOption",
		"borgRepository = lib.mkOption",
		"borgPassphraseFile = lib.mkOption",
		"borgCacheDir = lib.mkOption",
		"borgSecurityDir = lib.mkOption",
		`default = "/etc/loom/cloud/config.json";`,
		`default = "/etc/loom/cloud/rclone.conf";`,
		`default = "/etc/loom/cloud/borg.passphrase";`,
		`default = "/var/lib/loom/cloud";`,
		`default = "/var/lib/loom/cloud/borg/cache";`,
		`default = "/var/lib/loom/cloud/borg/security";`,
		`default = "/var/lib/loom/storage-retention";`,
		`default = "/srv/loom";`,
		`default = "/srv/loom/storage";`,
		`default = "/srv/loom/storage/imports";`,
		`default = "/srv/loom/storage/backups";`,
		`default = "/srv/loom/storage/archive";`,
		`default = "/var/lib/loom/generated";`,
		"Deprecated migration input for the legacy main Documents backing directory.",
		"Deprecated migration input for the legacy generated main storage export.",
		`default = "loom-cloud";`,
		`default = "loom";`,
		`default = "legacy_tree";`,
		"lib.types.enum",
		"production",
	} {
		requireContains(t, base, want)
	}
	requireContains(t, base, "LOOM Box path management requires loom.boxPath")
	requireContains(t, base, "LOOM Box path management requires loom.boxOwner")
}

func TestNixServiceRendersSetupIdentityAndScopedBoxAccess(t *testing.T) {
	service := readRepoFile(t, "nix/modules/loom-service.nix")

	for _, want := range []string{
		"LOOM_NODE_KIND=${cfg.nodeKind}",
		"LOOM_RUNTIME_CLASS=${cfg.runtimeClass}",
		"LOOM_SERVICE_ROOT=${cfg.serviceRoot}",
		"LOOM_STORAGE_ROOT=${cfg.storageRoot}",
		"LOOM_CANONICAL_USER_BACKUPS_ROOT=${cfg.canonicalUserBackupsRoot}",
		"LOOM_IMPORTS_ROOT=${cfg.importsRoot}",
		"LOOM_USER_BACKUPS_ROOT=${cfg.userBackupsRoot}",
		"LOOM_ARCHIVE_ROOT=${cfg.archiveRoot}",
		"LOOM_GENERATED_ROOT=${cfg.generatedRoot}",
		"LOOM_BOX_STATE_ROOT=${cfg.boxStateRoot}",
		"LOOM_MAIN_DOCUMENTS_ROOT=${cfg.mainDocumentsDir}",
		"LOOM_NOTES_PROJECTION_ROOT=${cfg.notesProjectionRoot}",
		"LOOM_BOOTSTRAP_MODE=${cfg.bootstrapMode}",
		`LOOM_WORKSPACE_ARCHIVE_ENABLED=${if workspaceArchiveCfg.enable then "true" else "false"}`,
		"LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID=${workspaceArchiveCfg.manifestKeyId}",
		`LoadCredential = lib.optional workspaceArchiveCfg.enable "workspace-archive-manifest-key:${workspaceArchiveCfg.manifestKeySource}";`,
		`!lib.hasPrefix "/nix/store/" workspaceArchiveCfg.manifestKeySource`,
		`workspaceArchiveMoveRoots = [ "${cfg.storageRoot}/archive" ]`,
		`[ "Topics" "Projects" "Library" ]`,
		"loomRuntimeWritePaths",
		"NoNewPrivileges = lib.mkDefault true",
		`LOOM_LEGACY_SPLIT_ROOTS=${if cfg.filesystemCutoverEnabled then "false" else "true"}`,
		"LOOM_EMBEDDINGS_ENABLED=${if embeddingsCfg.enable then \"true\" else \"false\"}",
		"LOOM_EMBEDDING_RUNTIME=${embeddingsCfg.runtime}",
		"LOOM_EMBEDDING_MODEL=${embeddingsCfg.model}",
		"LOOM_EMBEDDING_OLLAMA_URL=${embeddingsCfg.ollama.url}",
		"LOOM_EMBEDDING_DIMENSIONS=${toString embeddingsCfg.dimensions}",
		"LOOM_EMBEDDING_QUIET_WINDOW_SECONDS=${toString embeddingsCfg.quietWindowSeconds}",
		"LOOM_EMBEDDING_CONCURRENCY=${toString embeddingsCfg.concurrency}",
		"LOOM_CLOUD_CONFIG_PATH=${cloudCfg.configPath}",
		"LOOM_CLOUD_STATE_DIR=${cloudCfg.stateDir}",
		"LOOM_STORAGE_RETENTION_ROOT=${cfg.storageRetentionRoot}",
		"LOOM_BOX_PATH=${cfg.boxPath}",
		"LOOM_BOX_PROFILE=${cfg.boxProfile}",
		"Production LOOM service requires loom.boxPath",
		"Production main LOOM service requires loom.boxProfile",
		"ReadWritePaths",
		"pkgs.rclone",
		"cloudCfg.installRclone",
		"pkgs.borgbackup",
		"cloudCfg.installBorg",
		"cloudCfg.borgCacheDir",
		"cloudCfg.borgSecurityDir",
		"embeddingRuntimePackages",
		"embeddingsCfg.ollama.installPackage",
		"embeddingsCfg.ollama.enableService",
		"ollama.service",
		"RequiresMountsFor",
		"boxDocumentsPath",
		"boxNotesPath",
		"loom-box-layout-preflight",
		`systemd.services."loom-box-layout-preflight"`,
		`description = "Validate the LOOM Box layout before starting loomd"`,
		`Type = "oneshot"`,
		`ExecStart = boxLayoutPreflight`,
		`"loom-box-layout-preflight.service"`,
		"canonical Box Documents must not be a separate or bind mount",
		"could not verify canonical Box Documents mount state",
		`[ "$findmnt_status" -ne 1 ] || [ -n "$findmnt_output" ]`,
		"systemdPath = path:",
		`"${cfg.boxPath}/.loom"`,
		`"${cfg.boxPath}/Projects"`,
		`map systemdPath loomRuntimeWritePaths`,
	} {
		requireContains(t, service, want)
	}
	if strings.Contains(service, "loom-main-documents-bind.service") {
		t.Fatal("loom-service must not depend on the removed main Documents bind service")
	}
	if strings.Contains(service, "LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY=") || strings.Contains(service, "LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_SOURCE=") {
		t.Fatal("workspace archive credential bytes/source entered loom.env")
	}
	if strings.Contains(service, "hardware-main") {
		t.Fatal("generic LOOM service module activates hardware-main")
	}
	if strings.Contains(service, "cfg.mainDocumentsDir\n      cfg.notesProjectionRoot") {
		t.Fatal("legacy mainDocumentsDir must not be a daemon write path")
	}
	if strings.Contains(service, "systemdPath cfg.boxPath") {
		t.Fatal("loom-service grants write access to the whole Box path; only scoped Box paths should be writable")
	}
	if strings.Contains(service, "ExecStartPre = lib.optional (cfg.boxPath != null) boxLayoutPreflight") {
		t.Fatal("Box layout preflight must not run inside loomd's sandboxed mount namespace")
	}
}

func TestNixServiceIncludesPopplerForKnowledgeExtraction(t *testing.T) {
	service := readRepoFile(t, "nix/modules/loom-service.nix")
	requireContains(t, service, "pkgs.poppler-utils")

	dev := readRepoFile(t, "nix/profiles/dev.nix")
	requireContains(t, dev, "poppler-utils")

	flake := readRepoFile(t, "flake.nix")
	requireContains(t, flake, "poppler-utils")
}

func TestNixPostgresAndEmbeddingsDeclareDefaultOffPgvectorSupport(t *testing.T) {
	postgres := readRepoFile(t, "nix/modules/loom-postgres.nix")
	embeddings := readRepoFile(t, "nix/modules/loom-embeddings.nix")

	for _, want := range []string{
		"pgvector.enable = lib.mkOption",
		"default = true;",
		"adminUsers = lib.mkOption",
		"default = pkgs.postgresql_17;",
		"ps.pgvector",
		"CREATE EXTENSION IF NOT EXISTS vector;",
		"package = postgresPackage;",
		"GRANT \"${cfg.postgres.user}\" TO \"${adminUser}\";",
	} {
		requireContains(t, postgres, want)
	}
	for _, want := range []string{
		"options.loom.embeddings",
		"enable = lib.mkOption",
		"default = false;",
		`default = "ollama";`,
		`default = "mxbai-embed-large";`,
		"default = 1024;",
		"default = 600;",
		"default = 1;",
		`default = "http://127.0.0.1:11434";`,
		"installPackage = lib.mkOption",
		"enableService = lib.mkOption",
		"services.ollama = lib.mkIf embeddingsCfg.ollama.enableService",
	} {
		requireContains(t, embeddings, want)
	}
}

func TestNixStorageManagesOnlyScopedBoxPaths(t *testing.T) {
	storage := readRepoFile(t, "nix/modules/loom-storage.nix")

	for _, want := range []string{
		"cfg.manageBoxPaths && cfg.boxPath != null",
		"${cfg.boxPath}/.loom",
		"${cfg.boxPath}/Projects",
		"${cfg.boxPath}/Documents",
		"${cfg.boxPath}/Notes",
		"${cfg.serviceRoot} 0755 ${storageCfg.deployUser} ${storageCfg.deployGroup}",
		"${cfg.serviceRoot}/agents",
		"${cfg.storageRoot} 2750",
		"${cfg.importsRoot} 2770",
		"system.activationScripts.loomImportsCustodyParents",
		"-mindepth 1 -maxdepth 2 -type d",
		"chown --no-dereference ${cfg.user}:${cfg.group}",
		"chmod 2770",
		"${cfg.userBackupsRoot} 2770",
		"${cfg.archiveRoot} 2750",
		"${cfg.archiveRoot}/objects 0750",
		"${cfg.archiveRoot}/manifests 0750",
		"${cfg.archiveRoot}/runtime-manifests 0750",
		"${cfg.boxStateRoot}",
		"${cfg.generatedRoot}",
		"humanStorageHomes = lib.mkOption",
		"cloudCfg = cfg.cloud;",
		"cloudConfigDir = builtins.dirOf cloudCfg.configPath;",
		"${cloudCfg.stateDir}/snapshots",
		"${cloudCfg.stateDir}/offload",
		"${cloudCfg.stateDir}/manifests",
		"${cloudCfg.stateDir}/restore-drills",
		"${cloudCfg.stateDir}/temp",
		"${cloudCfg.stateDir}/borg",
		"${cloudCfg.borgCacheDir}",
		"${cloudCfg.borgSecurityDir}",
		"${cfg.storageRetentionRoot}",
		"${cfg.storageRetentionRoot}/main-documents",
		"${cfg.storageRetentionRoot}/main-documents/by-sha256",
		"systemdPath = path:",
		"boxParent = if cfg.boxPath == null then null else builtins.dirOf cfg.boxPath;",
		`a+ ${systemdPath boxParent} - - - - u:${cfg.user}:--x,m::r-x`,
		"0770 ${boxOwner} ${boxServiceGroup}",
		`loom.notesProjectionRoot = lib.mkDefault cfg.canonicalNotesProjectionRoot;`,
		`systemd.tmpfiles.rules = lib.optionals (cfg.storageRoot != cfg.dataDir)`,
		`retiredMainBoxIntakeNames = [ "Dropzone" "loom-lane" "LOOM Lane" ];`,
		`LOOM Main Box tmpfiles must not recreate retired Dropzone or Main Lane paths.`,
	} {
		requireContains(t, storage, want)
	}
	for _, forbidden := range []string{
		"${cfg.boxPath}/.loom/state",
		"${cfg.boxPath}/.loom/storage",
		"${cfg.boxPath}/.loom/logs",
		"storageCfg.humanStorageHomes",
		"${cfg.dataDir}/storage-views",
		"${cfg.storageExportRoot}",
		"${cfg.dataDir}/lane/accepted",
		"${cfg.dataDir}/storage-archive",
		`${cfg.boxPath}/Dropzone`,
		`${cfg.boxPath}/loom-lane`,
		`${cfg.boxPath}/LOOM Lane`,
	} {
		if strings.Contains(storage, forbidden) {
			t.Fatalf("storage module must not create volatile visible Box path %q", forbidden)
		}
	}
}

func TestNixStorageCreatesCanonicalDocumentsAndNotesWithoutBindMount(t *testing.T) {
	storage := readRepoFile(t, "nix/modules/loom-storage.nix")

	for _, want := range []string{
		`"d ${systemdPath "${cfg.boxPath}/Documents"} 2770 ${boxOwner} ${boxServiceGroup} - -"`,
		`"d ${systemdPath "${cfg.boxPath}/Notes"} 2770 ${boxOwner} ${boxServiceGroup} - -"`,
		`loom.notesProjectionRoot = lib.mkDefault cfg.canonicalNotesProjectionRoot;`,
	} {
		requireContains(t, storage, want)
	}
	for _, forbidden := range []string{
		"loom-main-documents-bind",
		"mainDocumentsExportPath",
		"mount --bind",
		`"d ${cfg.mainDocumentsDir}`,
	} {
		if strings.Contains(storage, forbidden) {
			t.Fatalf("loom-storage retains obsolete main Documents bind contract %q", forbidden)
		}
	}
}

func TestNixSmbModuleDefinesPrivateCanonicalShares(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-smb.nix")

	for _, want := range []string{
		"options.loom.smb",
		"enable = lib.mkOption",
		`boxShareName = lib.mkOption`,
		`default = "loom-main-box";`,
		`boxPath = lib.mkOption`,
		`default = cfg.boxPath;`,
		`storageShareName = lib.mkOption`,
		`default = "loom-storage";`,
		`storagePath = lib.mkOption`,
		`default = cfg.storageRoot;`,
		`shareName = lib.mkOption`,
		`path = lib.mkOption`,
		`effectiveStorageShareName =`,
		`effectiveStoragePath =`,
		`default = "loomshare";`,
		`default = [ "wg0" ];`,
		`bindInterfaces = lib.mkOption`,
		`sambaBindInterfaces =`,
		`default = [ ];`,
		`default = [ "10.44.0." ];`,
		`afterServices = lib.mkOption`,
		"users.users.${smbCfg.user}",
		"services.samba",
		"openFirewall = false;",
		`security = "user";`,
		`"map to guest" = "Never";`,
		`"bind interfaces only" = "yes";`,
		`interfaces = sambaList sambaBindInterfaces;`,
		`${smbCfg.boxShareName} =`,
		`${effectiveStorageShareName} =`,
		`"read only" = if readOnly then "yes" else "no";`,
		`shareSettings effectiveBoxPath false`,
		`shareSettings effectiveStoragePath true`,
		`"valid users" = smbCfg.user;`,
		`"wide links" = "no";`,
		`"ea support" = "yes";`,
		`"store dos attributes" = "yes";`,
		`"vfs objects" = "catia fruit streams_xattr";`,
		`"fruit:metadata" = "stream";`,
		`"fruit:resource" = "stream";`,
		`"fruit:posix_rename" = "yes";`,
		"networking.firewall.interfaces = lib.genAttrs smbCfg.interfaces",
		"allowedTCPPorts = [ 445 ];",
		"systemd.services = {",
		"samba-smbd = {",
		"wants = smbCfg.afterServices;",
		"after = smbCfg.afterServices;",
		"serviceConfig.ExecStartPre = [ shareRootCheck ];",
		"unitConfig.RequiresMountsFor = shareRoots;",
		"realpath -e --",
		"symlinked or non-canonical",
	} {
		requireContains(t, module, want)
	}
	for _, forbidden := range []string{`default = cfg.storageExportRoot;`} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("loom-smb module retains unsafe share contract %q", forbidden)
		}
	}
	if strings.Contains(module, "smbpasswd") {
		t.Fatal("loom-smb module must not embed or manage Samba passwords")
	}
	if strings.Contains(module, `"veto files"`) {
		t.Fatal("loom-smb module must not veto Finder metadata; LOOM importers ignore routine platform files after Finder copies complete")
	}
}

func TestNixProfilesWireSmbForHardwareMainAndDevSmoke(t *testing.T) {
	hardware := readRepoFile(t, "nix/profiles/main-hardware-desktop.nix")
	dev := readRepoFile(t, "nix/profiles/dev.nix")

	for _, body := range []string{hardware, dev} {
		requireContains(t, body, "../modules/loom-smb.nix")
		requireContains(t, body, "../modules/loom-embeddings.nix")
	}
	for _, want := range []string{
		`serviceRoot = "/srv/loom";`,
		`canonicalStorageRoot = "/srv/loom/storage";`,
		`canonicalImportsRoot = "/srv/loom/storage/imports";`,
		`importsBackupPolicy = "legacy_complete_physical_custody";`,
		`canonicalUserBackupsRoot = "/srv/loom/storage/backups";`,
		`canonicalArchiveRoot = "/srv/loom/storage/archive";`,
		`canonicalGeneratedRoot = "/var/lib/loom/generated";`,
		`canonicalBoxStateRoot = "/var/lib/loom/box-state";`,
		`canonicalNotesProjectionRoot = "/var/lib/loom/generated/notes";`,
		`canonicalBoxPath = "/srv/loom/box";`,
		`filesystemCutoverEnabled = true;`,
		`else "/home/loomadmin/loom-box";`,
		`boxStateRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalBoxStateRoot else "/home/loomadmin/loom-box/.loom/state";`,
		`storageRoot = config.loom.canonicalStorageRoot;`,
		`importsRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalImportsRoot else "/var/lib/loom/lane/accepted";`,
		`userBackupsRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalUserBackupsRoot else "/var/lib/loom/private-backups";`,
		`archiveRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalArchiveRoot else "/var/lib/loom/storage-archive";`,
		`notesProjectionRoot = if config.loom.filesystemCutoverEnabled then config.loom.canonicalNotesProjectionRoot else "/var/lib/loom/loom-notes";`,
		"smb = {",
		"enable = true;",
		"cloud = {",
		`remoteName = "loom-cloud";`,
		`remoteRoot = "loom";`,
		"installRclone = true;",
		"installBorg = true;",
		`snapshotBackend = "borg";`,
		`borgPassphraseFile = "/etc/loom/cloud/borg.passphrase";`,
		`borgCacheDir = "/var/lib/loom/cloud/borg/cache";`,
		`borgSecurityDir = "/var/lib/loom/cloud/borg/security";`,
		`boxShareName = "loom-main-box";`,
		`boxPath = config.loom.boxPath;`,
		`storageShareName = "loom-storage";`,
		`storagePath = if config.loom.filesystemCutoverEnabled then config.loom.canonicalStorageRoot else config.loom.storageExportRoot;`,
		`user = "loomshare";`,
		`interfaces = [ "wg0" ];`,
		`bindInterfaces = [ "10.44.0.2/24" ];`,
		`allowedHosts = [ "10.44.0." ];`,
		`afterServices = [ "wireguard-wg0.service" ];`,
	} {
		requireContains(t, hardware, want)
	}
	for _, want := range []string{
		"smb = {",
		"enable = true;",
		`shareName = "loom-storage";`,
		`user = "loomshare";`,
		`interfaces = [ "enp0s1" ];`,
		`allowedHosts = [ "192.168.64." ];`,
	} {
		requireContains(t, dev, want)
	}
}

func TestNixMiniDashboardDeclaresHardenedLoopbackCompanion(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-mini-dashboard.nix")
	for _, want := range []string{
		"options.loom.miniDashboard",
		"enable = lib.mkEnableOption",
		"package = lib.mkOption",
		"listenAddress = lib.mkOption",
		"socketPath = lib.mkOption",
		"statePath = lib.mkOption",
		"dataRoot = lib.mkOption",
		`default = "127.0.0.1:8090";`,
		`default = "${stateDirectory}/last-good.json";`,
		`default = config.loom.storageRoot;`,
		`systemd.services."loom-mini-dashboard"`,
		`wantedBy = [ "multi-user.target" ];`,
		`after = [ "loomd.service" ];`,
		`User = config.loom.user;`,
		`Group = config.loom.group;`,
		`StateDirectory = "loom/mini-dashboard";`,
		`ProtectSystem = "strict";`,
		`IPAddressDeny = "any";`,
		`IPAddressAllow = [ "localhost" ];`,
		`ReadOnlyPaths = [ "/run" cfg.dataRoot ];`,
		`ReadWritePaths = [ stateDirectory ];`,
		`"--loomd-socket" cfg.socketPath`,
		`"--data-dir" cfg.dataRoot`,
		"must use an explicit loopback IP",
		"must be a file directly within",
	} {
		requireContains(t, module, want)
	}
	for _, forbidden := range []string{
		`requires = [ "loomd.service" ];`,
		`networking.firewall`,
		`0.0.0.0:8090`,
	} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("mini-dashboard companion contains forbidden contract %q", forbidden)
		}
	}
}

func TestNixHardwareMainEnablesMiniDashboardWithoutDesktopPolicyDrift(t *testing.T) {
	profile := readRepoFile(t, "nix/profiles/main-hardware-desktop.nix")
	host := readRepoFile(t, "nix/hosts/hardware-main/configuration.nix")
	requireContains(t, profile, "../modules/loom-mini-dashboard.nix")
	requireContains(t, host, "loom.miniDashboard = {")
	requireContains(t, host, "kiosk.enable = true;")
	requireContains(t, profile, "services.displayManager.autoLogin.enable = lib.mkDefault false;")
	requireContains(t, profile, "services.gnome.gnome-remote-desktop.enable = lib.mkDefault true;")
}

func TestNixHardwareMainDeclaresPinnedPrivateOrcaService(t *testing.T) {
	packageFile := readRepoFile(t, "nix/packages/orca-headless.nix")
	protonPassPackage := readRepoFile(t, "nix/packages/proton-pass-cli.nix")
	module := readRepoFile(t, "nix/modules/loom-orca.nix")
	bashProfile := readRepoFile(t, "nix/files/agents.bash_profile")
	bashrc := readRepoFile(t, "nix/files/agents.bashrc")
	passSessionEnsure := readRepoFile(t, "nix/files/agents-pass-session-ensure.sh")
	profile := readRepoFile(t, "nix/profiles/main-hardware-desktop.nix")
	flake := readRepoFile(t, "flake.nix")

	for _, want := range []string{
		`version = "1.4.191";`,
		`releases/download/v${version}/orca-linux.AppImage`,
		`hash = "sha256-GWoQ+dr2eH+iWr63caGoKc39MpWSoadlXKX5dgyZ5kk=";`,
		`appimageTools.extractType2`,
		`buildFHSEnv`,
		`writeShellScript`,
		`appRun = writeShellScript "orca-apprun"`,
		`export APPDIR=${appDir}`,
		`exec ${appDir}/AppRun "$@"`,
		`runScript = appRun;`,
		`targetPkgs = pkgs: with pkgs; [`,
		`platforms = [ "x86_64-linux" ];`,
	} {
		requireContains(t, packageFile, want)
	}

	for _, want := range []string{
		`version = "2.3.3";`,
		`https://proton.me/download/pass-cli/${finalAttrs.version}/pass-cli-linux-x86_64`,
		`hash = "sha256-tbSaiz/Qr4gwwMGXnyjqDJDM7Oc/WQI6i8qCRdS2jak=";`,
		`autoPatchelfHook`,
		`install -Dm755 "$src" "$out/bin/pass-cli"`,
		`license = lib.licenses.gpl3Only;`,
		`platforms = [ "x86_64-linux" ];`,
	} {
		requireContains(t, protonPassPackage, want)
	}

	for _, want := range []string{
		`options.loom.orca`,
		`protonPassPackage = lib.mkOption`,
		`cfg.protonPassPackage`,
		`default = "10.44.0.2";`,
		`default = 6768;`,
		`default = "wg0";`,
		`users.users.agents`,
		`isNormalUser = true;`,
		`extraGroups = [ config.loom.group ];`,
		`accountHome = "/home/agents";`,
		`agentBashProfile = ../files/agents.bash_profile;`,
		`else ../files/agents.bashrc;`,
		`agentPassSessionEnsure = pkgs.writeShellApplication`,
		`name = "pass-session-ensure";`,
		`text = builtins.readFile ../files/agents-pass-session-ensure.sh;`,
		`systemd.tmpfiles.settings."00-loom-orca"`,
		`"/srv/loom/agents".d`,
		`mode = "0770";`,
		`"/srv/loom"."a+".argument = "u:agents:r-x,m::r-x";`,
		`"u:agents:rwx,d:u:agents:rwx,m::rwx,d:m::rwx"`,
		`system.activationScripts.loomOrcaAccess`,
		`system.activationScripts.loomOrcaShell`,
		`${accountHome}/.config/proton-pass-cli`,
		`${accountHome}/.local/state/proton-pass-cli`,
		`token_file=${accountHome}/.config/proton-pass-cli/agent.pat`,
		`agents:agents:600`,
		`agentPassSessionEnsure`,
		`install -o agents -g agents -m 0644`,
		`grant_agent_read_write /srv/loom/box`,
		`grant_agent_read_only /srv/loom/storage`,
		`grant_agent_read_only /var/lib/loom`,
		`protect_selected_recovery_key ${selectedRecoveryRoot} ${selectedRecoveryKey}`,
		`setfacl -m u:agents:--- /etc/loom`,
		`pkgs.bashInteractive`,
		`pkgs.git`,
		`pkgs.nodejs_22`,
		`pkgs.ripgrep`,
		`pkgs.xorg.xorgserver`,
		`networking.firewall.interfaces.${cfg.allowedInterface}.allowedTCPPorts`,
		`systemd.services."orca-serve"`,
		`config.system.path`,
		`StartLimitIntervalSec = 300;`,
		`StartLimitBurst = 5;`,
		`"wireguard-wg0.service"`,
		`XDG_CONFIG_HOME = "${accountHome}/.config";`,
		`LIBGL_ALWAYS_SOFTWARE = "1";`,
		`APPIMAGE = "${cfg.package}/bin/orca";`,
		`"serve"`,
		`"--port"`,
		`"--pairing-address"`,
		`"--json"`,
		`KillMode = "control-group";`,
		`Restart = "on-failure";`,
		`RestartPreventExitStatus = 3;`,
		`NoNewPrivileges = true;`,
	} {
		requireContains(t, module, want)
	}
	requireContains(t, bashProfile, `source "$HOME/.bashrc"`)

	for _, want := range []string{
		`export PATH="$HOME/.local/bin:$PATH"`,
		`export PROTON_PASS_KEY_PROVIDER=fs`,
		`export PASS_LOG_LEVEL=warn`,
		`VIRA_TEAL=$'\e[38;2;128;203;196m'`,
		`alias ll='ls -lah'`,
		`alias gs='git status --short --branch'`,
		`git status --porcelain=v2 --branch --untracked-files=no`,
		`LOOM_SCOPE_TEXT='storage:ro'`,
		`LOOM_SCOPE_TEXT='agents:rw'`,
		`__loom_prompt_command`,
		`__loom_install_prompt_command`,
		`return "$exit_status"`,
		`source "$HOME/.bashrc.local"`,
	} {
		requireContains(t, bashrc, want)
	}

	for _, want := range []string{
		`PROTON_PASS_AGENT_TOKEN_FILE`,
		`PROTON_PASS_AGENT_STATE_DIR`,
		`PROTON_PASS_STAT_BIN`,
		`flock -x 9`,
		`"$pass_cli" info`,
		`PROTON_PASS_PERSONAL_ACCESS_TOKEN=$token`,
		`agent access token file must have mode 0600`,
		`agent access token file must contain exactly one non-empty line`,
	} {
		requireContains(t, passSessionEnsure, want)
	}
	for _, forbidden := range []string{
		`pst_`,
		`--personal-access-token`,
		`echo "$token"`,
	} {
		if strings.Contains(passSessionEnsure, forbidden) {
			t.Fatalf("Proton Pass session helper contains forbidden token handling %q", forbidden)
		}
	}

	for _, forbidden := range []string{
		`/var/lib/orca`,
		`/srv/orca`,
		`networking.firewall.allowedTCPPorts`,
		`caddy`,
		`codex`,
		`virtualisation.oci-containers`,
		`containers.enable`,
		`ExecStartPost`,
	} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("ORCA module contains forbidden first-deployment contract %q", forbidden)
		}
	}

	requireContains(t, profile, `../modules/loom-orca.nix`)
	requireContains(t, profile, `orca.enable = true;`)
	requireContains(t, flake, `orca-headless = pkgs.callPackage ./nix/packages/orca-headless.nix { };`)
	requireContains(t, flake, `proton-pass-cli = pkgs.callPackage ./nix/packages/proton-pass-cli.nix { };`)
	requireContains(t, flake, `nixosModules.loom-orca = import ./nix/modules/loom-orca.nix;`)
}

func TestNixOrcaGrantsLoomServiceReadOnlyAgentWorkspaceAccess(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-orca.nix")

	for _, want := range []string{
		`agentWorkspaceRoot = "/srv/loom/agents";`,
		`morathustraHermesHome = config.loom.morathustra.hermesHome;`,
		`morathustraWorkspaceRecoveryRoot = "${agentWorkspaceRoot}/morathustra/recovery";`,
		`loomServiceUser = config.loom.user;`,
		`"/srv/loom/agents"."a+".argument =`,
		`"u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x";`,
		`grant_loom_service_agent_workspace_read_only()`,
		`if [ "$root" != ${lib.escapeShellArg agentWorkspaceRoot} ]; then`,
		`if [ -L "$root" ] || [ ! -d "$root" ]; then`,
		`${pkgs.coreutils}/bin/readlink -e -- "$root"`,
		`if [ "$resolved" != "$root" ]; then`,
		`if [ "$owner_group_mode" != "root:agents:770" ]; then`,
		`\( -path ${lib.escapeShellArg morathustraHermesHome} -o -path ${lib.escapeShellArg morathustraWorkspaceRecoveryRoot} -o -path ${lib.escapeShellArg minaHermesHome} -o -path ${lib.escapeShellArg minaWorkspaceRecoveryRoot} \)`,
		`-prune -o -type d`,
		`-prune -o -type f`,
		`-exec ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x '{}' +`,
		`-exec ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-- '{}' +`,
		`grant_loom_service_agent_workspace_read_only ${agentWorkspaceRoot}`,
	} {
		requireContains(t, module, want)
	}

	start := strings.Index(module, "grant_loom_service_agent_workspace_read_only()")
	end := strings.Index(module[start:], "grant_agent_read_write /srv/loom/box")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate the LOOM service agent-workspace ACL contract")
	}
	permissionContract := module[start : start+end]
	for _, forbidden := range []string{
		`u:${loomServiceUser}:rwx`,
		`u:${loomServiceUser}:rw-`,
		`/home/agents`,
	} {
		if strings.Contains(permissionContract, forbidden) {
			t.Fatalf("LOOM service agent-workspace ACL contains forbidden permission contract %q", forbidden)
		}
	}
	if strings.Contains(module, `extraGroups = [ "agents" ];`) {
		t.Fatal("LOOM service must not receive broad agents group membership")
	}
	prune := strings.Index(permissionContract, `\( -path ${lib.escapeShellArg morathustraHermesHome} -o -path ${lib.escapeShellArg morathustraWorkspaceRecoveryRoot} -o -path ${lib.escapeShellArg minaHermesHome} -o -path ${lib.escapeShellArg minaWorkspaceRecoveryRoot} \)`)
	mutation := strings.Index(permissionContract, `-exec ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x '{}' +`)
	if prune < 0 || mutation < 0 || prune >= mutation {
		t.Fatal("the live Hermes profile must be pruned before the broad LOOM service ACL mutation")
	}
}

func TestNixOrcaPreservesMorathustraRecoveryEvidenceModes(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-orca.nix")

	for _, want := range []string{
		`grant_loom_service_selected_recovery_read_only()`,
		`if [ "$root" != ${lib.escapeShellArg selectedWorkspaceRecoveryRoot} ]; then`,
		`[ "$(${pkgs.coreutils}/bin/readlink -e -- "$root")" != "$root" ]`,
		`[ "$(${pkgs.coreutils}/bin/readlink -e -- "$readme")" != "$readme" ]`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G:%h:%s' -- "$readme"`,
		`[ "$readme_size" -gt 1048576 ]`,
		`${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x -- "$root"`,
		`${pkgs.acl}/bin/setfacl -b -- "$readme"`,
		`${pkgs.coreutils}/bin/chmod 0644 -- "$readme"`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G:%a:%h:%s' -- "$readme"`,
		`grant_loom_service_selected_recovery_read_only ${selectedWorkspaceRecoveryRoot}`,
	} {
		requireContains(t, module, want)
	}

	start := strings.Index(module, "grant_loom_service_selected_recovery_read_only()")
	end := strings.Index(module[start:], "protect_cloud_private_keys()")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate the Morathustra recovery evidence ACL contract")
	}
	contract := module[start : start+end]
	for _, forbidden := range []string{
		`${pkgs.coreutils}/bin/cat`, `${pkgs.coreutils}/bin/cp`,
		`${pkgs.coreutils}/bin/mv`, `${pkgs.coreutils}/bin/chown`,
		`-R`, `find "$root"`,
	} {
		if strings.Contains(contract, forbidden) {
			t.Fatalf("Morathustra recovery evidence ACL contract mutates package content or custody: %q", forbidden)
		}
	}

	prune := strings.Index(module, `-o -path ${lib.escapeShellArg morathustraWorkspaceRecoveryRoot}`)
	broadMutation := strings.Index(module, `-exec ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x '{}' +`)
	specificGrant := strings.LastIndex(module, `grant_loom_service_selected_recovery_read_only ${selectedWorkspaceRecoveryRoot}`)
	if prune < 0 || broadMutation < 0 || specificGrant < 0 || prune >= broadMutation || specificGrant <= broadMutation {
		t.Fatal("Morathustra recovery evidence must be pruned before broad ACL mutation and reconciled afterward")
	}
}

func TestNixMorathustraRestoresOwnerOnlyProfileCustodyBeforeGateway(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-morathustra.nix")

	for _, want := range []string{
		`profileCustody = pkgs.writeShellScript "loom-${identity}-profile-custody"`,
		`profile=${lib.escapeShellArg hermesHome}`,
		`[ "$profile" != ${lib.escapeShellArg "${workspaceRoot}/.hermes"} ]`,
		`${pkgs.coreutils}/bin/readlink -e -- "$profile"`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$profile"`,
		`! -type d ! -type f ! -type s -print -quit`,
		`\( ! -user agents -o ! -group agents \) -print -quit`,
		`-type f -links +1 -print -quit`,
		`-exec ${pkgs.acl}/bin/setfacl -b -k -- '{}' +`,
		`-exec ${pkgs.acl}/bin/setfacl -b -- '{}' +`,
		`-exec ${pkgs.coreutils}/bin/chmod go= -- '{}' +`,
		`\( -type d -o -type f \) -perm /0077 -print -quit`,
		`${pkgs.acl}/bin/getfacl -cpR "$profile"`,
		`grep -Eq '^(default:|user:loom:)'`,
		`ExecStartPre = profileCustody;`,
	} {
		requireContains(t, module, want)
	}

	start := strings.Index(module, `profileCustody = pkgs.writeShellScript`)
	end := strings.Index(module[start:], `recoveryPublish = pkgs.writeShellScript`)
	if start < 0 || end < 0 {
		t.Fatal("could not isolate the Morathustra profile custody contract")
	}
	contract := module[start : start+end]
	for _, forbidden := range []string{
		`${pkgs.coreutils}/bin/cat`, `${pkgs.coreutils}/bin/cp`,
		`${pkgs.coreutils}/bin/mv`, `${pkgs.coreutils}/bin/chown`,
		`u:${loomServiceUser}:r-x`, `u:${loomServiceUser}:r--`,
	} {
		if strings.Contains(contract, forbidden) {
			t.Fatalf("Morathustra profile custody reads content, changes ownership, or grants LOOM profile access: %q", forbidden)
		}
	}
}

func TestNixOrcaExcludesCloudKeysFromBroadAgentReadOnlyGrant(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-orca.nix")
	prunedRoots := `\( -path ${lib.escapeShellArg cloudKeysRoot} -o -path ${lib.escapeShellArg backupEvidenceRoot} -o -path ${lib.escapeShellArg morathustraRecoveryRoot} -o -path ${lib.escapeShellArg minaRecoveryRoot} -o -path ${lib.escapeShellArg nodeAgentQuiescenceRoot} -o -path ${lib.escapeShellArg nodeAgentApplicationResultsRoot} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/topics"} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/projects"} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/library"} \)`

	for _, want := range []string{
		`cloudKeysRoot = "${config.loom.cloud.stateDir}/keys";`,
		`backupEvidenceRoot = "${config.loom.dataDir}/backups";`,
		`nodeAgentQuiescenceRoot = "${config.loom.nodeAgent.dataDir}/project-archive-quiescence";`,
		`nodeAgentApplicationResultsRoot = "${config.loom.nodeAgent.dataDir}/application-results";`,
		`loomServiceGroup = config.loom.group;`,
		`${pkgs.findutils}/bin/find -P "$root" -xdev`,
		prunedRoots,
		`-prune -o -type d`,
		`-prune -o ! -type d ! -type l`,
		`protect_cloud_private_keys()`,
		`if [ "$root" != ${lib.escapeShellArg cloudKeysRoot} ]; then`,
		`if [ -L "$root" ] || [ ! -d "$root" ]; then`,
		`${pkgs.coreutils}/bin/readlink -e -- "$root"`,
		`if [ "$resolved" != "$root" ]; then`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$root"`,
		`if [ "$owner_group" != "${loomServiceUser}:${loomServiceGroup}" ]; then`,
		`\( -type l -o ! -type d ! -type f \) -print -quit`,
		`-exec ${pkgs.acl}/bin/setfacl -b '{}' +`,
		`-exec ${pkgs.coreutils}/bin/chmod 0700 '{}' +`,
		`-type f ! -name 'known_hosts*'`,
		`-exec ${pkgs.coreutils}/bin/chmod 0600 '{}' +`,
		`${pkgs.acl}/bin/getfacl -cpR "$root"`,
		`grep -Eq '^(default:)?user:agents:'`,
		`! -user ${loomServiceUser} -o ! -group ${loomServiceGroup} -o ! -perm 0700`,
		`! -user ${loomServiceUser} -o ! -group ${loomServiceGroup} -o ! -perm 0600`,
		`grant_agent_read_only /var/lib/loom`,
		`protect_cloud_private_keys ${cloudKeysRoot}`,
	} {
		requireContains(t, module, want)
	}
	grantStart := strings.Index(module, "grant_agent_read_only()")
	grantEnd := strings.Index(module, "grant_loom_service_agent_workspace_read_only()")
	if grantStart < 0 || grantEnd <= grantStart {
		t.Fatal("could not isolate the broad agent read-only ACL contract")
	}
	grantContract := module[grantStart:grantEnd]
	if strings.Count(grantContract, prunedRoots) != 2 {
		t.Fatal("both directory and file ACL grants must prune every protected subtree")
	}
	prune := strings.Index(grantContract, prunedRoots)
	mutation := strings.Index(grantContract, `-exec ${pkgs.acl}/bin/setfacl -m u:agents:r-x,d:u:agents:r-x '{}' +`)
	if prune < 0 || mutation < 0 || prune >= mutation {
		t.Fatal("protected LOOM subtrees must be pruned before the broad agent ACL mutation")
	}

	broadGrant := strings.Index(module, "grant_agent_read_only /var/lib/loom")
	protection := strings.Index(module, "protect_cloud_private_keys ${cloudKeysRoot}")
	if broadGrant < 0 || protection < 0 || protection <= broadGrant {
		t.Fatal("cloud key protection must run after the broad /var/lib/loom agent ACL grant")
	}

	start := strings.Index(module, "protect_cloud_private_keys()")
	end := strings.Index(module, "protect_backup_evidence()")
	if start < 0 || end <= start {
		t.Fatal("could not isolate the cloud private-key protection contract")
	}
	contract := module[start:end]
	for _, forbidden := range []string{
		`cat "$root"`,
		`cp `,
		`mv `,
		`ssh-keygen`,
		`chmod 0600 "$root"`,
	} {
		if strings.Contains(contract, forbidden) {
			t.Fatalf("cloud private-key protection contains forbidden credential handling %q", forbidden)
		}
	}
}

func TestNixOrcaExcludesBackupEvidenceFromBroadAgentReadOnlyGrant(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-orca.nix")

	for _, want := range []string{
		`backupEvidenceRoot = "${config.loom.dataDir}/backups";`,
		`protect_backup_evidence()`,
		`if [ "$root" != ${lib.escapeShellArg backupEvidenceRoot} ]; then`,
		`if [ -L "$root" ] || [ ! -d "$root" ]; then`,
		`${pkgs.coreutils}/bin/readlink -e -- "$root"`,
		`if [ "$resolved" != "$root" ]; then`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$root"`,
		`if [ "$owner_group" != "${loomServiceUser}:${loomServiceGroup}" ]; then`,
		`! -type d ! -type f ! -type l -print -quit`,
		`mode_digest_before=`,
		`mode_digest_after=`,
		`\( -type d -o -type f -o -type l \) -printf '%p\t%y\t%m\t%l\0'`,
		`${pkgs.coreutils}/bin/sort -z`,
		`${pkgs.coreutils}/bin/sha256sum`,
		`setfacl --no-mask`,
		`-m u:agents:---,d:u:agents:--- -- '{}' +`,
		`-m u:agents:--- -- '{}' +`,
		`getfacl -cp -- '{}' +`,
		`$0 == "user:agents:---"`,
		`$0 == "default:user:agents:---"`,
		`protect_backup_evidence ${backupEvidenceRoot}`,
	} {
		requireContains(t, module, want)
	}

	broadGrant := strings.Index(module, "grant_agent_read_only /var/lib/loom")
	protection := strings.Index(module, "protect_backup_evidence ${backupEvidenceRoot}")
	if broadGrant < 0 || protection < 0 || protection <= broadGrant {
		t.Fatal("backup evidence protection must verify the pruned subtree after the broad /var/lib/loom grant")
	}

	start := strings.Index(module, "protect_backup_evidence()")
	end := strings.Index(module, "grant_agent_read_write /srv/loom/box")
	if start < 0 || end <= start {
		t.Fatal("could not isolate the backup evidence protection contract")
	}
	contract := module[start:end]
	for _, want := range []string{
		`${pkgs.findutils}/bin/find -P "$root" -xdev`,
		`! -type d ! -type f ! -type l -print -quit`,
		`\( -type d -o -type f -o -type l \) -printf '%p\t%y\t%m\t%l\0'`,
	} {
		if !strings.Contains(contract, want) {
			t.Fatalf("backup evidence protection is missing no-follow inventory contract %q", want)
		}
	}
	for _, forbidden := range []string{
		`${pkgs.coreutils}/bin/chmod`,
		`${pkgs.coreutils}/bin/chown`,
		`${pkgs.coreutils}/bin/cat`,
		`${pkgs.coreutils}/bin/cp`,
		`${pkgs.coreutils}/bin/mv`,
		`${pkgs.coreutils}/bin/install`,
		`${pkgs.coreutils}/bin/tee`,
		`setfacl -b`,
		`\( -type l -o ! -type d ! -type f \)`,
	} {
		if strings.Contains(contract, forbidden) {
			t.Fatalf("backup evidence protection contains forbidden historical evidence handling %q", forbidden)
		}
	}
}

func TestNixOrcaPreservesMorathustraRecoveryKeyCustody(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-orca.nix")

	for _, want := range []string{
		`morathustraRecoveryRoot = "/var/lib/loom/morathustra-recovery";`,
		`selectedRecoveryKey = runtime.recoverySigningKeyFile;`,
		`-path ${lib.escapeShellArg morathustraRecoveryRoot}`,
		`protect_selected_recovery_key()`,
		`if [ -L "$root" ] || [ ! -d "$root" ]; then`,
		`${pkgs.coreutils}/bin/readlink -e -- "$root"`,
		`! -path "$key" -print -quit`,
		`if [ -L "$key" ] || [ ! -f "$key" ]; then`,
		`${pkgs.coreutils}/bin/readlink -e -- "$key"`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G:%h:%s' -- "$key"`,
		`agents:agents:1:64`,
		`${pkgs.acl}/bin/setfacl -b -- "$root" "$key"`,
		`${pkgs.coreutils}/bin/chmod 0700 -- "$root"`,
		`${pkgs.coreutils}/bin/chmod 0600 -- "$key"`,
		`${pkgs.coreutils}/bin/stat -c '%U:%G:%a:%h:%s' -- "$key"`,
		`grep -q '^user:agents:'`,
		`protect_selected_recovery_key ${selectedRecoveryRoot} ${selectedRecoveryKey}`,
	} {
		requireContains(t, module, want)
	}

	grantStart := strings.Index(module, "grant_agent_read_only()")
	grantEnd := strings.Index(module[grantStart:], "protect_selected_recovery_key()")
	if grantStart < 0 || grantEnd < 0 {
		t.Fatal("could not isolate the broad agent read-only ACL contract")
	}
	grantContract := module[grantStart : grantStart+grantEnd]
	prune := strings.Index(grantContract, `-path ${lib.escapeShellArg morathustraRecoveryRoot}`)
	mutation := strings.Index(grantContract, `-exec ${pkgs.acl}/bin/setfacl -m u:agents:r-x,d:u:agents:r-x '{}' +`)
	if prune < 0 || mutation < 0 || prune >= mutation {
		t.Fatal("Morathustra recovery key root must be pruned before broad ACL mutation")
	}

	broadGrant := strings.Index(module, "grant_agent_read_only /var/lib/loom")
	protection := strings.Index(module, "protect_selected_recovery_key ${selectedRecoveryRoot} ${selectedRecoveryKey}")
	if broadGrant < 0 || protection <= broadGrant {
		t.Fatal("Morathustra recovery key normalization must run after broad ACL reconciliation")
	}

	start := strings.Index(module, "protect_selected_recovery_key()")
	end := strings.Index(module[start:], "grant_loom_service_agent_workspace_read_only()")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate the Morathustra recovery key custody contract")
	}
	contract := module[start : start+end]
	for _, forbidden := range []string{
		`cat "$key"`, `${pkgs.coreutils}/bin/cp`, `${pkgs.coreutils}/bin/mv`,
		`${pkgs.coreutils}/bin/chown`, `ssh-keygen`, `openssl`,
	} {
		if strings.Contains(contract, forbidden) {
			t.Fatalf("Morathustra recovery key custody reads or replaces secret material: %q", forbidden)
		}
	}
}

func TestNixMiniDashboardKioskFailsClosedOntoHDMIOnly(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-mini-dashboard.nix")
	for _, want := range []string{
		`systemd.user.services."loom-mini-dashboard-kiosk"`,
		`assertion = !cfg.kiosk.enable || cfg.enable;`,
		`loom.miniDashboard.kiosk.enable requires loom.miniDashboard.enable.`,
		`wantedBy = [ "graphical-session.target" ];`,
		`unitConfig.ConditionUser = "loomadmin";`,
		`Restart = "always";`,
		`RestartSec = "10s";`,
		`StateDirectory = "loom-mini-dashboard";`,
		`IPAddressDeny = "any";`,
		`IPAddressAllow = [ "localhost" ];`,
		`pkgs.chromium`,
		`pkgs.gnome-session`,
		`pkgs.xorg.xrandr`,
		`.mutter-Xwaylandauth.*`,
		`parse_lcd_geometry`,
		`^HDMI-1 connected 1024x600`,
		`--window-position="$lcd_x,$lcd_y"`,
		`--window-size=1024,600`,
		`--user-data-dir="$profile_dir"`,
		`export XDG_CONFIG_HOME="$config_home"`,
		`export XDG_CACHE_HOME="$cache_home"`,
		`export XDG_DATA_HOME="$data_home"`,
		`gnome-session-inhibit`,
		`--inhibit=idle:suspend`,
		`--ozone-platform=x11`,
		`KillMode = "control-group";`,
		`--disable-background-networking`,
		`--host-resolver-rules="MAP * ~NOTFOUND, EXCLUDE $resolver_exclusion"`,
		`HDMI-1 disappeared or became ambiguous`,
		`HDMI-1 moved`,
	} {
		requireContains(t, module, want)
	}
	for _, forbidden := range []string{
		"loomdisplay",
		"DP-1 connected",
		"--no-sandbox",
		"autoLogin.enable = true",
		"loomdesk",
		`after = [ "graphical-session.target" ];`,
		`systemd-inhibit`,
		`--what=idle:sleep`,
	} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("mini-dashboard kiosk contains forbidden contract %q", forbidden)
		}
	}
}

func TestNixProfilesDeclareExpectedMainBox(t *testing.T) {
	for path, bootstrapMode := range map[string]string{
		"nix/profiles/dev.nix":                   "dev",
		"nix/profiles/main-hardware-desktop.nix": "production",
	} {
		profile := readRepoFile(t, path)
		expectedBox := `boxPath = "/home/loomadmin/loom-box";`
		if path == "nix/profiles/main-hardware-desktop.nix" {
			expectedBox = `boxPath = if config.loom.filesystemCutoverEnabled then config.loom.canonicalBoxPath else "/home/loomadmin/loom-box";`
		}
		for _, want := range []string{
			`nodeKind = "main";`,
			`runtimeClass = "main_full";`,
			expectedBox,
			`boxProfile = "main";`,
			`boxOwner = "loomadmin";`,
			`manageBoxPaths = true;`,
			`bootstrapMode = "` + bootstrapMode + `";`,
		} {
			requireContains(t, profile, want)
		}
	}
}

func TestNixPostgresDeclaresAdminPeerRole(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-postgres.nix")
	for _, want := range []string{
		`adminUsers = lib.mkOption`,
		`default = [ "loomadmin" ];`,
		`GRANT "${cfg.postgres.user}" TO "${adminUser}";`,
	} {
		requireContains(t, module, want)
	}
}

func TestNixPostgresDeclaresIsolatedProvenanceDatabaseAndPeerRole(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-postgres.nix")
	hardware := readRepoFile(t, "nix/profiles/main-hardware-desktop.nix")
	for _, want := range []string{
		`default = "loom_provenance";`,
		`default = "loom_provenance_service";`,
		`provenanceCfg.database != cfg.postgres.database`,
		`provenanceCfg.user != cfg.postgres.user`,
		`!(lib.elem provenanceCfg.user cfg.postgres.adminUsers)`,
		`local "${provenanceCfg.database}" "${provenanceCfg.user}" peer map=${provenanceCfg.peerMap}`,
		`${provenanceCfg.peerMap} ${cfg.user} ${provenanceCfg.user}`,
		`LOOM_PROVENANCE_DB_URL=${provenanceDBURL}`,
		`requires = lib.mkAfter [ "postgresql-setup.service" ];`,
		`after = lib.mkAfter [ "postgresql-setup.service" ];`,
		`ALTER ROLE "${provenanceCfg.user}" NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;`,
		`REVOKE "${cfg.postgres.user}" FROM "${provenanceCfg.user}";`,
		`REVOKE ALL PRIVILEGES ON DATABASE "${provenanceCfg.database}" FROM PUBLIC;`,
		`GRANT CONNECT, TEMPORARY ON DATABASE "${provenanceCfg.database}" TO "${provenanceCfg.user}";`,
	} {
		requireContains(t, module, want)
	}
	for _, want := range []string{
		`database = "loom_provenance";`,
		`user = "loom_provenance";`,
	} {
		requireContains(t, hardware, want)
	}
	if strings.Contains(module, `GRANT "${cfg.postgres.user}" TO "${provenanceCfg.user}"`) ||
		strings.Contains(module, `GRANT "${provenanceCfg.user}" TO "${cfg.postgres.user}"`) {
		t.Fatal("primary and provenance service roles must not inherit one another")
	}
}

func TestNixPostgresScopesRestoreAuthorityPeerRulesToDisposableNamespaces(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-postgres.nix")
	for _, want := range []string{
		`restoreAuthorityPeerMap = "loom_restore_authority";`,
		`local "/^loom_restore_drill_[a-z0-9_]+$" "${cfg.postgres.user}" peer map=${restoreAuthorityPeerMap}`,
		`local "/^loom_provenance_restore_drill_[a-z0-9_]+$" "${provenanceCfg.user}" peer map=${restoreAuthorityPeerMap}`,
		`${restoreAuthorityPeerMap} postgres ${cfg.postgres.user}`,
		`${restoreAuthorityPeerMap} postgres ${provenanceCfg.user}`,
		`${restoreAuthorityPeerMap} ${cfg.user} ${cfg.postgres.user}`,
		`${restoreAuthorityPeerMap} ${cfg.user} ${provenanceCfg.user}`,
		`ALTER ROLE "${cfg.postgres.user}" NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;`,
		`REVOKE "${provenanceCfg.user}" FROM "${cfg.postgres.user}";`,
		`psql -v ON_ERROR_STOP=1 -d template1 -c 'CREATE EXTENSION IF NOT EXISTS vector;'`,
	} {
		requireContains(t, module, want)
	}
	for _, forbidden := range []string{
		`local all "${cfg.postgres.user}" peer map=${restoreAuthorityPeerMap}`,
		`local all "${provenanceCfg.user}" peer map=${restoreAuthorityPeerMap}`,
	} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("restore authority PostgreSQL contract contains widening %q", forbidden)
		}
	}
}

func TestNixHardwareMainDoesNotDeclareHumanStorageLinks(t *testing.T) {
	hardware := readRepoFile(t, "nix/profiles/main-hardware-desktop.nix")

	if strings.Contains(hardware, "humanStorageHomes") {
		t.Fatal("hardware main must use direct canonical paths/bookmarks, not human storage symlinks")
	}
}

func TestNixRestoreAuthorityIsPostgresOwnedLocalOnlyAndHardened(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-restore-authority.nix")
	service := readRepoFile(t, "nix/modules/loom-service.nix")
	flake := readRepoFile(t, "flake.nix")
	binary := readRepoFile(t, "cmd/loom-restore-authority/main.go")
	for _, want := range []string{
		`options.loom.restoreAuthority`,
		`restoreAuthorityRuntimeDirectory = "/run/loom-restore-authority";`,
		`restoreAuthoritySocketPath = "${restoreAuthorityRuntimeDirectory}/restore-authority.sock";`,
		`default = restoreAuthoritySocketPath;`,
		`authorityCfg.socketPath == restoreAuthoritySocketPath`,
		`"r /run/loom/restore-authority.sock - - - - -"`,
		`"d ${restoreAuthorityRuntimeDirectory} 0750 postgres ${cfg.group} - -"`,
		`LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER=root`,
		`LOOM_RESTORE_AUTHORITY_EXECUTOR_USER=postgres`,
		`ListenStream = authorityCfg.socketPath;`,
		`SocketUser = "postgres";`,
		`SocketGroup = cfg.group;`,
		`SocketMode = "0660";`,
		`DirectoryMode = "0750";`,
		`after = [ "systemd-tmpfiles-setup.service" ];`,
		`restoreAuthoritySocketUnit = config.systemd.units."loom-restore-authority.socket".unit;`,
		`systemd.services."loom-restore-authority-socket-reconcile"`,
		`description = "Reconcile the LOOM strict-restore authority socket after activation";`,
		`wantedBy = [ "multi-user.target" ];`,
		`before = [ "loomd.service" ];`,
		`"loom-restore-authority.socket"`,
		`restartTriggers = [ restoreAuthoritySocketUnit ];`,
		`Type = "oneshot";`,
		`RemainAfterExit = true;`,
		`ExecStart = "${pkgs.systemd}/bin/systemctl restart loom-restore-authority.socket";`,
		`User = "postgres";`,
		`Group = "postgres";`,
		`SupplementaryGroups = [ cfg.group ];`,
		`requires = [ "postgresql-setup.service" ];`,
		`after = [ "postgresql-setup.service" ];`,
		`--listen-fd 3`,
		`--peer-user ${lib.escapeShellArg cfg.user}`,
		`--createdb-path ${postgresPackage}/bin/createdb`,
		`--pg-restore-path ${postgresPackage}/bin/pg_restore`,
		`--dropdb-path ${postgresPackage}/bin/dropdb`,
		`--postgres-socket-directory /run/postgresql`,
		`--postgres-port 5432`,
		`NoNewPrivileges = true;`,
		`PrivateNetwork = true;`,
		`RestrictAddressFamilies = [ "AF_UNIX" ];`,
		`requires = lib.mkAfter [ "loom-restore-authority.socket" ];`,
	} {
		requireContains(t, module, want)
	}
	for _, want := range []string{
		`flags.Int("listen-fd", 3`,
		`if listenFD != 3`,
		`net.FileListener(file)`,
	} {
		requireContains(t, binary, want)
	}
	for _, forbidden := range []string{"net.ListenUnix", "os.Chown", "sudo"} {
		if strings.Contains(binary, forbidden) {
			t.Fatalf("restore authority binary contains forbidden fallback %q", forbidden)
		}
	}
	if count := strings.Count(module, `/run/loom/restore-authority.sock`); count != 1 {
		t.Fatalf("old shared restore-authority socket path appears %d times; only the removal rule is allowed", count)
	}
	for _, forbidden := range []string{
		"security.sudo",
		"sudoers",
		"CREATEDB",
		"ListenDatagram",
		"0.0.0.0",
		"AF_INET",
		"AF_INET6",
		"postgresql://",
		`systemd.sockets."loom-restore-authority".restartTriggers`,
		`LOOM_RESTORE_AUTHORITY_SERVER_USER`,
		`activatorUser = lib.mkOption`,
		`executorUser = lib.mkOption`,
	} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("restore authority Nix module contains forbidden contract %q", forbidden)
		}
	}
	for _, want := range []string{
		`NoNewPrivileges = lib.mkDefault true;`,
		`RuntimeDirectory = "loom";`,
		`RuntimeDirectoryMode = "0750";`,
		`RuntimeDirectoryPreserve = "yes";`,
	} {
		requireContains(t, service, want)
	}
	for _, want := range []string{
		`loom-restore-authority = mkGoPackage`,
		`nixosModules.loom-restore-authority`,
	} {
		requireContains(t, flake, want)
	}
	for _, profilePath := range []string{
		"nix/profiles/dev.nix",
		"nix/profiles/main-hardware-desktop.nix",
		"nix/profiles/production-placeholder.nix",
	} {
		profile := readRepoFile(t, profilePath)
		requireContains(t, profile, `../modules/loom-restore-authority.nix`)
		requireContains(t, profile, `restoreAuthority.enable = true;`)
	}
}

func TestNixHardwareMainWorkspaceArchiveUsesOperatorCredential(t *testing.T) {
	host := readRepoFile(t, "nix/hosts/hardware-main/configuration.nix")
	base := readRepoFile(t, "nix/modules/loom-base.nix")
	service := readRepoFile(t, "nix/modules/loom-service.nix")
	requireContains(t, host, `loom.workspaceArchive = {`)
	requireContains(t, host, `manifestKeySource = "/var/lib/loom-workspace-archive-secrets/manifest-key";`)
	requireContains(t, base, `default = false;`)
	requireContains(t, service, `LoadCredential = lib.optional workspaceArchiveCfg.enable "workspace-archive-manifest-key:${workspaceArchiveCfg.manifestKeySource}";`)
	for _, forbidden := range []string{"builtins.readFile", "manifestKey =", "LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY="} {
		if strings.Contains(host, forbidden) {
			t.Fatalf("Main archive provisioning embeds credential material through %q", forbidden)
		}
	}
}

func TestNixWorkspaceArchivePreservesMountTopologyAndPayloadACLs(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable")
		}
	}
	apply := `system: let
  disabled = system.extendModules { modules = [ ({ lib, ... }: { loom.workspaceArchive.enable = lib.mkForce false; }) ]; };
  strict = system.extendModules { modules = [ ({ lib, ... }: { systemd.services.loomd.serviceConfig.ProtectSystem = lib.mkForce "strict"; }) ]; };
in {
  service = system.config.systemd.services.loomd.serviceConfig;
  disabledWrites = disabled.config.systemd.services.loomd.serviceConfig.ReadWritePaths;
  strictRejected = builtins.any (x: !x.assertion && builtins.match ".*Workspace archive requires writable.*" x.message != null) strict.config.assertions;
  access = system.config.system.activationScripts.loomOrcaAccess.text;
  storageShare = system.config.services.samba.settings.loom-storage;
}`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = repoRoot(t)
	payload, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("render archive custody: %v: %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		Service struct {
			User, Group, ProtectSystem, ProtectHome string
			NoNewPrivileges                         bool
			ReadWritePaths                          []string
		}
		DisabledWrites []string
		StrictRejected bool
		Access         string
		StorageShare   map[string]any
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Service.User != "loom" || got.Service.Group != "loom" || got.Service.ProtectSystem != "full" || got.Service.ProtectHome != "read-only" || !got.Service.NoNewPrivileges || !got.StrictRejected {
		t.Fatalf("service protection drift: %+v", got.Service)
	}
	for _, p := range got.Service.ReadWritePaths {
		switch strings.Trim(p, `"`) {
		case "/srv", "/srv/loom", "/srv/loom/box", "/srv/loom/storage", "/srv/loom/box/Topics", "/srv/loom/box/Projects", "/srv/loom/box/Library", "/srv/loom/storage/archive":
			t.Fatalf("archive move root/parent became a separate writable bind: %s", p)
		}
	}
	if !slices.Contains(got.DisabledWrites, `"/srv/loom/storage/archive"`) || !slices.Contains(got.DisabledWrites, `"/srv/loom/box/Projects"`) {
		t.Fatal("disabled archive changed pre-existing write roots")
	}
	for _, root := range []string{"Topics", "Projects", "Library"} {
		if strings.Count(got.Access, "/srv/loom/box/"+root+"/*") != 3 {
			t.Fatalf("active workspace %s is not pruned from every recursive writer grant", root)
		}
	}
	for _, root := range []string{"topics", "projects", "library"} {
		if strings.Count(got.Access, "-path /srv/loom/storage/archive/"+root) != 2 {
			t.Fatalf("archive category %s is not pruned from recursive reader grants", root)
		}
	}
	for _, want := range []string{
		"protect_workspace_archive_categories", "--physical --set",
		"u::rwx,u:loom:r-x,u:agents:r-x,g::---,m::r-x,o::---",
		"d:u::rwx,d:u:loom:r-x,d:u:agents:r-x,d:g::---,d:m::r-x,d:o::---",
		`for container in "$root"/*; do`, "--physical --no-mask",
		`-m u:agents:r-x,d:u:agents:r-x -- "$container"`,
		"750|2750", "category custody is invalid", "category mode changed",
		"container custody is invalid", "container mode changed",
	} {
		if !strings.Contains(got.Access, want) {
			t.Fatalf("missing category protection %q", want)
		}
	}
	if got.StorageShare["force user"] != "loom" || got.StorageShare["read only"] != "yes" {
		t.Fatal("human Storage read-only service-owner access changed")
	}
}

func TestNixHardwareMainStorageExpansion(t *testing.T) {
	root := repoRoot(t)
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable")
		}
	}
	apply := `system: {
   filesystem = system.config.fileSystems."/srv/loom";
   rootDevice = system.config.fileSystems."/".device;
   services = builtins.map (name: system.config.systemd.units."${name}.service".text) [
     "loomd" "loom-node-agent" "loom-box-layout-preflight" "orca-serve"
     "loom-mina" "loom-mina-recovery-publish" "loom-mini-dashboard"
     "samba-smbd" "samba-nmbd" "samba-winbindd" "systemd-tmpfiles-setup"
     "systemd-tmpfiles-resetup"
   ];
 }`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = root
	payload, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("render storage: %v: %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		Filesystem struct {
			Device, FsType string
			NeededForBoot  bool
			Options        []string
		}
		RootDevice string
		Services   []string
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Filesystem.Device != "/dev/disk/by-label/loom-data" || got.Filesystem.FsType != "ext4" || !got.Filesystem.NeededForBoot {
		t.Fatalf("unsafe service filesystem: %+v", got.Filesystem)
	}
	if got.RootDevice != "/dev/disk/by-label/nixos" {
		t.Fatal("original root device changed")
	}
	for _, option := range got.Filesystem.Options {
		if option == "nofail" || option == "noauto" {
			t.Fatalf("unsafe mount option %q", option)
		}
	}
	for _, unit := range got.Services {
		for _, key := range []string{"Requires=", "After=", "BindsTo="} {
			found := false
			for _, line := range strings.Split(unit, "\n") {
				if strings.HasPrefix(line, key) {
					for _, dependency := range strings.Fields(strings.TrimPrefix(line, key)) {
						if dependency == "srv-loom.mount" {
							found = true
						}
					}
				}
			}
			if !found {
				t.Fatalf("missing %s mount dependency: %s", key, unit)
			}
		}
	}
}

func TestNixHardwareMainRendersRestoreAuthoritySocketActivationReconcile(t *testing.T) {
	root := repoRoot(t)
	nix, err := exec.LookPath("nix")
	if err != nil {
		const profileNix = "/nix/var/nix/profiles/default/bin/nix"
		if _, statErr := os.Stat(profileNix); statErr != nil {
			t.Skip("nix is unavailable; source contract remains covered")
		}
		nix = profileNix
	}

	apply := `system:
let
  changed = system.extendModules {
    modules = [
      ({ lib, ... }: {
        systemd.sockets."loom-restore-authority".socketConfig.ListenStream =
          lib.mkForce "/run/loom-restore-authority/transition-test.sock";
      })
    ];
  };
  trigger = evaluated:
    builtins.map builtins.toString
      evaluated.config.systemd.services."loom-restore-authority-socket-reconcile".restartTriggers;
in {
  reconcile = system.config.systemd.units."loom-restore-authority-socket-reconcile.service".text;
  socket = system.config.systemd.units."loom-restore-authority.socket".text;
  authority = system.config.systemd.units."loom-restore-authority.service".text;
  loomd = system.config.systemd.units."loomd.service".text;
  loomEnv = system.config.environment.etc."loom/loom.env".text;
  triggers = trigger system;
  changedSocket = changed.config.systemd.units."loom-restore-authority.socket".text;
  changedLoomd = changed.config.systemd.units."loomd.service".text;
  changedTriggers = trigger changed;
}`
	cmd := exec.Command(
		nix,
		"--extra-experimental-features", "nix-command flakes",
		"eval", "--json",
		".#nixosConfigurations.hardware-main",
		"--apply", apply,
	)
	cmd.Dir = root
	payload, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("render hardware-main restore-authority units: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("render hardware-main restore-authority units: %v", err)
	}

	var rendered struct {
		Reconcile      string   `json:"reconcile"`
		Socket         string   `json:"socket"`
		Authority      string   `json:"authority"`
		Loomd          string   `json:"loomd"`
		LoomEnv        string   `json:"loomEnv"`
		Triggers       []string `json:"triggers"`
		ChangedSocket  string   `json:"changedSocket"`
		ChangedLoomd   string   `json:"changedLoomd"`
		ChangedTrigger []string `json:"changedTriggers"`
	}
	if err := json.Unmarshal(payload, &rendered); err != nil {
		t.Fatalf("decode rendered hardware-main units: %v", err)
	}

	for _, want := range []string{
		"After=systemd-tmpfiles-setup.service loom-restore-authority.socket",
		"Before=loomd.service",
		"ExecStart=/nix/store/",
		"/bin/systemctl restart loom-restore-authority.socket",
		"RemainAfterExit=true",
		"Type=oneshot",
		"WantedBy=multi-user.target",
		"X-Restart-Triggers=/nix/store/",
	} {
		requireContains(t, rendered.Reconcile, want)
	}
	if strings.Contains(rendered.Reconcile, "Requires=") {
		t.Fatal("socket reconcile must not require the socket it restarts")
	}
	if len(rendered.Triggers) != 1 || !strings.HasSuffix(rendered.Triggers[0], "-unit-loom-restore-authority.socket") {
		t.Fatalf("reconcile restart triggers = %#v, want the generated authority socket unit", rendered.Triggers)
	}
	if len(rendered.ChangedTrigger) != 1 || !strings.HasSuffix(rendered.ChangedTrigger[0], "-unit-loom-restore-authority.socket") {
		t.Fatalf("changed reconcile restart triggers = %#v, want the changed generated authority socket unit", rendered.ChangedTrigger)
	}
	if rendered.Triggers[0] == rendered.ChangedTrigger[0] {
		t.Fatal("authority socket definition change must change the reconcile service restart trigger")
	}
	requireContains(t, rendered.ChangedSocket, "ListenStream=/run/loom-restore-authority/transition-test.sock")
	for _, want := range []string{
		"After=systemd-tmpfiles-setup.service",
		"Before=loomd.service",
		"ListenStream=/run/loom-restore-authority/restore-authority.sock",
		"SocketGroup=loom",
		"SocketMode=0660",
		"SocketUser=postgres",
	} {
		requireContains(t, rendered.Socket, want)
	}
	for _, want := range []string{
		"Group=postgres",
		"NoNewPrivileges=true",
		"User=postgres",
	} {
		requireContains(t, rendered.Authority, want)
	}
	for _, want := range []string{
		"LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER=root",
		"LOOM_RESTORE_AUTHORITY_EXECUTOR_USER=postgres",
		"LOOM_RESTORE_AUTHORITY_SOCKET_OWNER=postgres",
		"LOOM_RESTORE_AUTHORITY_SOCKET_GROUP=loom",
	} {
		requireContains(t, rendered.LoomEnv, want)
	}
	if strings.Contains(rendered.LoomEnv, "LOOM_RESTORE_AUTHORITY_SERVER_USER") {
		t.Fatal("rendered hardware-main config conflates the socket activator with the authority executor")
	}
	for _, want := range []string{
		"Group=loom",
		"NoNewPrivileges=true",
		"RuntimeDirectory=loom",
		"RuntimeDirectoryMode=0750",
		"User=loom",
	} {
		requireContains(t, rendered.Loomd, want)
	}
	if strings.Contains(rendered.Loomd, "loom-restore-authority-socket-reconcile") {
		t.Fatal("loomd must not acquire a dependency on the activation reconcile service")
	}
	if rendered.ChangedLoomd != rendered.Loomd {
		t.Fatal("authority socket definition transition must not change the rendered loomd unit")
	}
}

func TestNixHermesReleasePin(t *testing.T) {
	var lock struct {
		Root  string
		Nodes map[string]struct {
			Inputs map[string]json.RawMessage
			Locked struct {
				Rev, NarHash, Owner, Repo string
			}
		}
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "flake.lock")), &lock); err != nil {
		t.Fatal(err)
	}
	upstream := lock.Nodes["hermes-upstream"].Locked
	if upstream.Owner != "NousResearch" || upstream.Repo != "hermes-agent" ||
		upstream.Rev != "29112bef099274229cadff79cdff7bf7b99c4b77" ||
		upstream.NarHash != "sha256-Ii9xP2fKUpvCcwWZuxJ0g3CZ+IL2UZH14pUNvBfdclc=" {
		t.Fatalf("Hermes release/source identity changed: %+v", upstream)
	}
	var loomNixpkgs string
	if err := json.Unmarshal(lock.Nodes[lock.Root].Inputs["nixpkgs"], &loomNixpkgs); err != nil {
		t.Fatal(err)
	}
	if lock.Nodes[loomNixpkgs].Locked.Rev != "26ef669cffa904b6f6832ab57b77892a37c1a671" {
		t.Fatal("Hermes packaging must not update LOOM's nixpkgs")
	}
	packageFile := readRepoFile(t, "nix/packages/hermes-agent.nix")
	for _, want := range []string{
		`assert upstream.version == "0.21.0";`,
		`assert hermes-upstream.rev == revision;`,
		`hermes-upstream.packages.${system}.messaging`,
		`--set HERMES_MANAGED true`,
		`--set HERMES_DISABLE_LAZY_INSTALLS 1`,
		`--unset HERMES_LAZY_INSTALL_TARGET`,
		`--set HERMES_SKIP_NODE_BOOTSTRAP 1`,
	} {
		requireContains(t, packageFile, want)
	}
	for _, forbidden := range []string{"HERMES_HOME", "fetchTarball", "pip install", "curl ", "postPatch", "substituteInPlace"} {
		if strings.Contains(packageFile, forbidden) {
			t.Fatalf("Hermes package contains unexpected profile/installer/source mutation: %s", forbidden)
		}
	}
}

func TestNixMorathustraRenderedServiceAndBaseline(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable; release and source boundaries remain covered")
		}
	}
	apply := `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in
let
  disabled = system.extendModules {
    modules = [ ({ lib, ... }: {
      loom.morathustra.enable = lib.mkForce false;
      loom.morathustra.recoveryEnabled = lib.mkForce false;
    }) ];
  };
  enabled = system.extendModules {
    modules = [ ({ lib, ... }: {
      loom.morathustra.enable = lib.mkForce true;
      loom.morathustra.recoveryEnabled = lib.mkForce false;
    }) ];
  };
  off = disabled.config;
  on = enabled.config;
  names = builtins.attrNames;
  extra = before: after: builtins.filter (x: !(builtins.elem x before)) after;
in {
  disabled = !off.loom.morathustra.enable && !(off.systemd.services ? loom-morathustra);
  baseline = on.loom.morathustra.baselineSettings;
  unit = on.systemd.units."loom-morathustra.service".text;
  environment = on.systemd.services.loom-morathustra.environment;
  extraServices = extra (names off.systemd.services) (names on.systemd.services);
  extraActivation = extra (names off.system.activationScripts) (names on.system.activationScripts);
  extraUsers = extra (names off.users.users) (names on.users.users);
  extraGroups = extra (names off.users.groups) (names on.users.groups);
  globalHermesHome = (on.environment.variables ? HERMES_HOME) || (on.environment.sessionVariables ? HERMES_HOME);
  package = toString on.loom.morathustra.package;
  userPackages = map toString on.users.users.agents.packages;
  disabledUserPackages = map toString off.users.users.agents.packages;
  systemPackages = map toString on.environment.systemPackages;
  sameFirewall = on.networking.firewall == off.networking.firewall;
  sameSudo = on.security.sudo.extraRules == off.security.sudo.extraRules;
  sameTmpfiles = on.systemd.tmpfiles.rules == off.systemd.tmpfiles.rules && on.systemd.tmpfiles.settings == off.systemd.tmpfiles.settings;
  failedAssertions = map (x: x.message) (builtins.filter (x: !x.assertion) on.assertions);
}`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes",
		"eval", "--impure", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = repoRoot(t)
	payload, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("evaluate Morathustra contract: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		Disabled, GlobalHermesHome, SameFirewall, SameSudo, SameTmpfiles bool
		Unit, Package                                                    string
		ExtraServices, ExtraActivation, ExtraUsers, ExtraGroups          []string
		UserPackages, DisabledUserPackages, SystemPackages               []string
		FailedAssertions                                                 []string
		Environment                                                      map[string]string
		Baseline                                                         struct {
			Model     struct{ Default, Provider string }
			Terminal  struct{ Cwd string }
			Platforms map[string]struct{ Enabled *bool }
			Curator   map[string]any
			Memory    struct {
				WriteApproval bool `json:"write_approval"`
			}
			Skills struct {
				WriteApproval     bool     `json:"write_approval"`
				GuardAgentCreated bool     `json:"guard_agent_created"`
				Disabled          []string `json:"disabled"`
				ExternalDirs      []string `json:"external_dirs"`
				CreateDir         *string  `json:"create_dir"`
			}
			Sessions struct {
				RetentionDays int   `json:"retention_days"`
				AutoPrune     *bool `json:"auto_prune"`
				AutoArchive   *bool `json:"auto_archive"`
			}
			Security struct {
				AllowLazyInstalls *bool `json:"allow_lazy_installs"`
			}
		}
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Disabled || got.GlobalHermesHome || !got.SameFirewall || !got.SameSudo || !got.SameTmpfiles {
		t.Fatal("Hermes changes disabled defaults, global profile, firewall, sudo or tmpfiles")
	}
	if len(got.ExtraServices) != 1 || got.ExtraServices[0] != "loom-morathustra" ||
		len(got.ExtraActivation)+len(got.ExtraUsers)+len(got.ExtraGroups)+len(got.FailedAssertions) != 0 {
		t.Fatalf("unexpected services, provisioning or failed assertions: %s", payload)
	}
	contains := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	if !contains(got.UserPackages, got.Package) || contains(got.DisabledUserPackages, got.Package) || contains(got.SystemPackages, got.Package) {
		t.Fatal("Hermes PATH must be enabled only for the agents account")
	}
	const root = "/srv/loom/agents/morathustra"
	if got.Environment["HERMES_HOME"] != root+"/.hermes" || got.Environment["HOME"] != "/home/agents" ||
		got.Environment["HERMES_INFERENCE_MODEL"] != "gpt-6-astra" || got.Environment["HERMES_TUI_PROVIDER"] != "openai-codex" {
		t.Fatalf("gateway profile drift: %+v", got.Environment)
	}
	for _, want := range []string{
		"User=agents", "Group=agents", "NoNewPrivileges=true", "UMask=0077",
		"WorkingDirectory=" + root, "ExecStart=" + got.Package + "/bin/hermes gateway",
		"ProtectSystem=strict", "ProtectHome=true", "PrivateTmp=true",
		"ReadWritePaths=" + root, "ReadOnlyPaths=-" + root + "/skills/installed",
		"AssertPathExists=" + root + "/.hermes/config.yaml", "KillMode=control-group",
		"AssertPathExists=" + root + "/.hermes/SOUL.md",
		"AssertPathIsSymbolicLink=!" + root + "/.hermes/skills",
	} {
		requireContains(t, got.Unit, want)
	}
	for _, directory := range []string{"cron", "sessions", "logs", "memories"} {
		requireContains(t, got.Unit, "AssertPathIsDirectory="+root+"/.hermes/"+directory)
	}
	baseline := got.Baseline
	falseExplicit := func(value *bool) bool { return value != nil && !*value }
	if baseline.Model.Default != "gpt-6-astra" || baseline.Model.Provider != "openai-codex" || baseline.Terminal.Cwd != root ||
		!baseline.Memory.WriteApproval || !baseline.Skills.WriteApproval || !baseline.Skills.GuardAgentCreated || baseline.Skills.CreateDir != nil ||
		baseline.Sessions.RetentionDays != 90 || !falseExplicit(baseline.Sessions.AutoPrune) || !falseExplicit(baseline.Sessions.AutoArchive) ||
		!falseExplicit(baseline.Security.AllowLazyInstalls) {
		t.Fatal("unsafe baseline model, writes, installs or maintenance policy")
	}
	wantCurator := map[string]any{
		"enabled": true, "prune_builtins": false, "consolidate": false,
		"interval_hours": float64(168), "min_idle_hours": float64(2),
		"stale_after_days": float64(30), "archive_after_days": float64(90),
		"archive_ttl_days": float64(0),
		"backup":           map[string]any{"enabled": true, "keep": float64(5)},
	}
	if !reflect.DeepEqual(baseline.Curator, wantCurator) {
		t.Fatalf("native curator ownership/maintenance policy drift: %+v", baseline.Curator)
	}
	wantDisabled := strings.Fields(`airtable arxiv ascii-video baoyu-infographic
        box claude-code claude-design codebase-inspection codex competitor-news-monitor
        computer-use design-md dogfood email-inbox-triage gif-search google-workspace
        hermes-agent-skill-authoring himalaya humanizer inspecting-hermes-desktop-dom
        llm-wiki manim-video maps node-inspect-debugger notion obsidian opencode
        p5js popular-web-designs product-price-monitor python-debugpy
        requesting-code-review sdlc-review simplify-code songsee songwriting-and-ai-music
        spike systematic-debugging teams-meeting-pipeline test-driven-development xurl youtube-content`)
	disabled := slices.Clone(baseline.Skills.Disabled)
	slices.Sort(disabled)
	slices.Sort(wantDisabled)
	if len(disabled) != 42 || !slices.Equal(disabled, wantDisabled) {
		t.Fatalf("expected exact 42 disabled bundled skills, without duplicates: %v", disabled)
	}
	// Twelve native + seven installed skills remain visible on Linux; four
	// Apple skills keep their native platform filter. This is not an allowlist
	// that prevents future unique native skills from becoming visible.
	for _, retained := range strings.Fields(`architecture-diagram blocked-page-recovery
        document-to-action-items docx github grounded-citations hermes-agent
        meeting-action-items pdf powerpoint weekly-review-planning xlsx
        search-loom-docs manage-loom-projects operate-loom-storage operate-loom-nodes
        investigate-loom-incidents orchestrate-loom-work use-proton-pass
        apple-notes apple-reminders findmy imessage`) {
		if slices.Contains(disabled, retained) {
			t.Fatalf("retained skill disabled: %s", retained)
		}
	}
	if len(baseline.Skills.ExternalDirs) != 1 || baseline.Skills.ExternalDirs[0] != root+"/skills/installed" {
		t.Fatal("LOOM-installed skill root must be exact and singular")
	}
	for _, platform := range []string{"api_server", "webhook", "msgraph_webhook", "telegram", "discord", "slack", "whatsapp", "matrix", "email", "a2a", "relay"} {
		if _, ok := baseline.Platforms[platform]; !ok {
			t.Fatalf("missing explicit disabled platform: %s", platform)
		}
	}
	for name, platform := range baseline.Platforms {
		if !falseExplicit(platform.Enabled) {
			t.Fatalf("baseline must explicitly disable %s", name)
		}
	}
}

// Render all four combinations against hardware-main without building or
// activating it. Assert the off defaults separately from synthetic overrides.
func TestNixMorathustraRecoveryActivationMatrix(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		nix = "/nix/var/nix/profiles/default/bin/nix"
		if _, err := os.Stat(nix); err != nil {
			t.Skip("nix unavailable")
		}
	}
	apply := `actual: let system = (import ./tests/nix/legacy_agent_host.nix) actual; in
let
  deployed = system.config;
  base = (system.extendModules { modules = [ ({ lib, ... }: {
    loom.morathustra.enable = lib.mkForce false;
    loom.morathustra.recoveryEnabled = lib.mkForce false;
  }) ]; }).config;
  publicKey = builtins.concatStringsSep "" (builtins.genList (_: "aB") 32);
  flags = [ "LOOM_MORATHUSTRA_ENABLED" "LOOM_MORATHUSTRA_RECOVERY_ENABLED" "LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY" ];
  timers = c: map (name: c.systemd.units."${name}.timer".text) (builtins.attrNames c.systemd.timers);
  render = runtime: recovery: key: let
    c = (system.extendModules { modules = [ ({ lib, ... }: {
      loom.morathustra.enable = lib.mkForce runtime;
      loom.morathustra.recoveryEnabled = lib.mkForce recovery;
      loom.morathustra.recoveryPublicKey = lib.mkForce key;
    }) ]; }).config;
    env = c.systemd.services.loomd.environment;
    packages = map toString c.users.users.agents.packages;
  in {
    inherit runtime recovery key;
    flagsPresent = (env ? LOOM_MORATHUSTRA_ENABLED) && (env ? LOOM_MORATHUSTRA_RECOVERY_ENABLED);
    publicKeyPresent = env ? LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY;
    runtimeEnv = env.LOOM_MORATHUSTRA_ENABLED or "false";
    recoveryEnv = env.LOOM_MORATHUSTRA_RECOVERY_ENABLED or "false";
    publicKeyEnv = env.LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY or "";
    gateway = c.systemd.services ? loom-morathustra;
    recoveryPublisher = c.systemd.services ? loom-morathustra-recovery-publish;
    recoveryTimer = c.systemd.timers ? loom-morathustra-recovery-publish;
    recoveryKeyFile = c.loom.morathustra.recoverySigningKeyFile;
    recoveryPublisherUnit = if recovery then c.systemd.units."loom-morathustra-recovery-publish.service".text else "";
    recoveryTimerUnit = if recovery then c.systemd.units."loom-morathustra-recovery-publish.timer".text else "";
    tmpfiles = c.systemd.tmpfiles.rules;
    runtimeInstalled = builtins.elem (toString c.loom.morathustra.package) packages;
    producerInstalled = builtins.elem (toString c.loom.morathustra.recoveryPackage) packages;
    producerGlobal = builtins.elem (toString c.loom.morathustra.recoveryPackage) (map toString c.environment.systemPackages);
    failedAssertions = map (x: x.message) (builtins.filter (x: !x.assertion) c.assertions);
    unchangedTimers = timers c == timers base;
    unchangedDaemon = builtins.removeAttrs env flags == builtins.removeAttrs base.systemd.services.loomd.environment flags;
    unchangedServices = builtins.filter (x: !(builtins.elem x [ "loom-morathustra" "loom-morathustra-recovery-publish" ])) (builtins.attrNames c.systemd.services) == builtins.attrNames base.systemd.services;
  };
in {
  deployedRuntime = deployed.loom.morathustra.enable;
  deployedRecovery = deployed.loom.morathustra.recoveryEnabled;
  cases = builtins.concatMap (runtime: builtins.concatMap (recovery:
    map (key: render runtime recovery key) [ "" publicKey "bad" (builtins.substring 0 62 publicKey) (publicKey + "aa") (builtins.concatStringsSep "" (builtins.genList (_: "z") 64)) ]
  ) [ false true ]) [ false true ];
}`
	cmd := exec.Command(nix, "--extra-experimental-features", "nix-command flakes", "eval", "--impure", "--json", ".#nixosConfigurations.hardware-main", "--apply", apply)
	cmd.Dir = repoRoot(t)
	payload, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("render activation matrix: %v\n%s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		DeployedRuntime, DeployedRecovery bool
		Cases                             []struct {
			Runtime, Recovery, Gateway, RecoveryPublisher, RecoveryTimer, RuntimeInstalled, ProducerInstalled, ProducerGlobal bool
			UnchangedTimers, UnchangedDaemon, UnchangedServices                                                               bool
			FlagsPresent, PublicKeyPresent                                                                                    bool
			Key, RuntimeEnv, RecoveryEnv, PublicKeyEnv, RecoveryKeyFile, RecoveryPublisherUnit, RecoveryTimerUnit             string
			Tmpfiles                                                                                                          []string
			FailedAssertions                                                                                                  []string
		}
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if !got.DeployedRuntime || !got.DeployedRecovery || len(got.Cases) != 24 {
		t.Fatal("hardware-main must deploy the accepted runtime and recovery policy")
	}
	for _, c := range got.Cases {
		if c.FlagsPresent != (c.Runtime || c.Recovery) || c.PublicKeyPresent != c.Recovery || c.RuntimeEnv != strconv.FormatBool(c.Runtime) || c.RecoveryEnv != strconv.FormatBool(c.Recovery) || c.Gateway != c.Runtime || c.RecoveryPublisher != c.Recovery || c.RecoveryTimer != c.Recovery || c.RuntimeInstalled != c.Runtime || c.ProducerInstalled != c.Recovery || c.ProducerGlobal {
			t.Fatalf("coupled activation: %+v", c)
		}
		if c.UnchangedTimers != !c.Recovery || !c.UnchangedDaemon || !c.UnchangedServices {
			t.Fatalf("backup/service policy changed: %+v", c)
		}
		if c.Recovery {
			for _, want := range []string{
				"User=agents", "Group=agents", "NoNewPrivileges=true", "PrivateNetwork=true",
				"WorkingDirectory=/srv/loom/agents/morathustra", "ReadWritePaths=/srv/loom/agents/morathustra",
				"ReadOnlyPaths=/var/lib/loom/morathustra-recovery/signing.key",
				"Requires=loom-morathustra.service", "After=loom-morathustra.service",
			} {
				requireContains(t, c.RecoveryPublisherUnit, want)
			}
			requireContains(t, c.RecoveryTimerUnit, "OnCalendar=*-*-* 02:45:00 Europe/Amsterdam")
			requireContains(t, c.RecoveryTimerUnit, "Persistent=true")
			if c.RecoveryKeyFile != "/var/lib/loom/morathustra-recovery/signing.key" || !slices.Contains(c.Tmpfiles, "d /var/lib/loom/morathustra-recovery 0700 agents agents - -") {
				t.Fatalf("recovery key custody drift: %+v", c)
			}
		} else if c.RecoveryPublisherUnit != "" || c.RecoveryTimerUnit != "" {
			t.Fatalf("recovery service leaked while disabled: %+v", c)
		}
		wantKey := ""
		if c.Recovery {
			wantKey = c.Key
		}
		if c.PublicKeyEnv != wantKey {
			t.Fatalf("public key exposed without recovery: %+v", c)
		}
		wantFailure := c.Recovery && c.Key != strings.Repeat("aB", 32)
		if wantFailure {
			if len(c.FailedAssertions) != 1 || !strings.Contains(c.FailedAssertions[0], "recovery requires") {
				t.Fatalf("missing key assertion: %+v", c)
			}
		} else if len(c.FailedAssertions) != 0 {
			t.Fatalf("runtime-only boot blocked: %+v", c)
		}
	}
}

func TestNixMorathustraHasNoProfileProvisioningOrGlobalEnvironment(t *testing.T) {
	module := readRepoFile(t, "nix/modules/loom-morathustra.nix")
	for _, forbidden := range []string{
		"create_dir", "skills/created", "state.db", "system.activationScripts",
		"environment.systemPackages", "environment.variables",
		"environment.sessionVariables", "security.sudo", "services.hermes-agent",
		"EnvironmentFile", "LoadCredential", "ln -s",
	} {
		if strings.Contains(module, forbidden) {
			t.Fatalf("Morathustra introduces unscoped profile/authority mutation: %s", forbidden)
		}
	}
	for _, want := range []string{
		`recoveryStateRoot = "/var/lib/loom/${identity}-recovery";`,
		`systemd.tmpfiles.rules = [ "d ${recoveryStateRoot} 0700 agents agents - -" ];`,
		`systemd.services."loom-${identity}-recovery-publish"`,
		`systemd.timers."loom-${identity}-recovery-publish"`,
		`OnCalendar = "*-*-* 02:45:00 Europe/Amsterdam";`,
		`PrivateNetwork = true;`,
		`ReadOnlyPaths = [ recoverySigningKeyFile "-${workspaceRoot}/skills/installed" ];`,
	} {
		requireContains(t, module, want)
	}
	orca := readRepoFile(t, "nix/modules/loom-orca.nix")
	for _, want := range []string{
		`agentBashrc = if runtime.enable then`,
		`export HERMES_HOME=${lib.escapeShellArg runtime.hermesHome}`,
		`export HERMES_INFERENCE_MODEL=${lib.escapeShellArg runtime.model}`,
		`export HERMES_TUI_PROVIDER=${lib.escapeShellArg runtime.modelProvider}`,
		`export PATH="/etc/profiles/per-user/agents/bin:$PATH"`,
		`packages = lib.optionals runtime.enable [ runtime.package ];`,
	} {
		requireContains(t, orca, want)
	}
}

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(repoRoot(t), relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(payload)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
func requireContains(t *testing.T, body string, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("expected file to contain %q", want)
	}
}

// Execute the same behavioral contract the Nix package runs with its pinned
// Python during installation. No Hermes/profile import or host action occurs.
func TestNixHermesBackupTimestampCompatibility(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("Python is required for the Hermes packaging contract")
	}
	packageFile := readRepoFile(t, "nix/packages/hermes-agent.nix")
	extract := func(label string) string {
		start := strings.Index(packageFile, "# BEGIN LOOM BACKUP TIMESTAMP "+label)
		end := strings.Index(packageFile, "# END LOOM BACKUP TIMESTAMP "+label)
		if start < 0 || end < start {
			t.Fatal("missing packaging executable contract")
		}
		return packageFile[start:end]
	}
	shim := filepath.Join(t.TempDir(), "sitecustomize.py")
	entry := "/nix/store/fixture-hermes-env/bin/hermes"
	if err := os.WriteFile(shim, []byte(strings.ReplaceAll(extract("SHIM"), "@LOOM_HERMES_ENTRYPOINT@", entry)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"backup", "--version", "gateway", "--tui", "chat", "import", "other-entry"} {
		t.Run(command, func(t *testing.T) {
			cmd := exec.Command(python, "-c", extract("TEST"), shim, entry, command)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		})
	}
	for _, want := range []string{"pinned Hermes wrapper invocation shape changed", "pinned Hermes Python entrypoint changed", `if sys.argv[:2] == ["@LOOM_HERMES_ENTRYPOINT@", "backup"]:`, `kwargs.setdefault("strict_timestamps", False)`, "subprocess.run([py[0]"} {
		requireContains(t, packageFile, want)
	}
}

func TestNixHermesBackupWrapperShapeRefusal(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	raw := readRepoFile(t, "nix/packages/hermes-agent.nix")
	start := strings.Index(raw, "import pathlib, re, subprocess, sys")
	end := strings.Index(raw, "\nPYCOMPAT")
	if start < 0 || end < start {
		t.Fatal("missing packaging validator")
	}
	for _, wrapper := range []string{
		"exec hermes backup\n",
		"export HERMES_PYTHON='/nix/store/one/bin/python3'\nexec \"/nix/store/two/bin/hermes\"  \"$@\"\n",
		"export HERMES_PYTHON='/nix/store/one/bin/python3'\nexec \"/nix/store/one/bin/hermes\" --unexpected \"$@\"\n",
		"export HERMES_PYTHON='/nix/store/one/bin/python3'\nexec \"/nix/store/one/bin/hermes\"  \"$@\"\nexec /unexpected\n",
	} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(root, "bin/hermes")
		if err := os.WriteFile(file, []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(python, "-c", raw[start:end], root)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "pinned Hermes wrapper invocation shape changed") {
			t.Fatalf("wrong wrapper result: %v %s", err, out)
		}
		after, err := os.ReadFile(file)
		if err != nil || string(after) != wrapper {
			t.Fatal("bad wrapper was mutated")
		}
	}
}
