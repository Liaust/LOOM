{ config, lib, pkgs, self, ... }:

let
  cfg = config.loom.orca;
  minaSelected = config.loom.mina.selected || config.loom.mina.enable || config.loom.mina.recoveryEnabled;
  identity = if minaSelected then "mina" else "morathustra";
  runtime = config.loom.${identity};
  accountHome = "/home/agents";
  agentWorkspaceRoot = "/srv/loom/agents";
  workspaceBoxRoot = if config.loom.boxPath == null then "/srv/loom/box" else config.loom.boxPath;
  workspaceArchiveRoot = "${config.loom.storageRoot}/archive";
  loomServiceUser = config.loom.user;
  loomServiceGroup = config.loom.group;
  cloudKeysRoot = "${config.loom.cloud.stateDir}/keys";
  backupEvidenceRoot = "${config.loom.dataDir}/backups";
  nodeAgentQuiescenceRoot = "${config.loom.nodeAgent.dataDir}/project-archive-quiescence";
  nodeAgentApplicationResultsRoot = "${config.loom.nodeAgent.dataDir}/application-results";
  morathustraRecoveryRoot = "/var/lib/loom/morathustra-recovery";
  minaRecoveryRoot = "/var/lib/loom/mina-recovery";
  selectedRecoveryRoot = if minaSelected then minaRecoveryRoot else morathustraRecoveryRoot;
  selectedRecoveryKey = runtime.recoverySigningKeyFile;
  morathustraHermesHome = config.loom.morathustra.hermesHome;
  morathustraWorkspaceRecoveryRoot = "${agentWorkspaceRoot}/morathustra/recovery";
  minaHermesHome = config.loom.mina.hermesHome;
  minaWorkspaceRecoveryRoot = "${agentWorkspaceRoot}/mina/recovery";
  selectedWorkspaceRecoveryRoot = "${runtime.workspaceRoot}/recovery";
  agentBashProfile = ../files/agents.bash_profile;
  agentBashrc = if runtime.enable then
    pkgs.writeText "agents.bashrc" (builtins.readFile ../files/agents.bashrc + ''

      # The selected Hermes profile is scoped to this account's interactive shell.
      ${if runtime.macComputerUseEnabled then ''
        export HERMES_HOME="''${HERMES_HOME:-${runtime.hermesHome}}"
        ${lib.optionalString (runtime.model != "") ''
          if [ "$HERMES_HOME" = ${lib.escapeShellArg runtime.hermesHome} ]; then
            export HERMES_INFERENCE_MODEL=${lib.escapeShellArg runtime.model}
            export HERMES_TUI_PROVIDER=${lib.escapeShellArg runtime.modelProvider}
          fi
        ''}
      '' else ''
        export HERMES_HOME=${lib.escapeShellArg runtime.hermesHome}
        ${lib.optionalString (runtime.model != "") ''
          export HERMES_INFERENCE_MODEL=${lib.escapeShellArg runtime.model}
          export HERMES_TUI_PROVIDER=${lib.escapeShellArg runtime.modelProvider}
        ''}
      ''}
      export PATH="/etc/profiles/per-user/agents/bin:$PATH"
    '')
  else ../files/agents.bashrc;
  agentPassSessionEnsure = pkgs.writeShellApplication {
    name = "pass-session-ensure";
    runtimeInputs = [
      cfg.protonPassPackage
      pkgs.coreutils
      pkgs.util-linux
    ];
    text = builtins.readFile ../files/agents-pass-session-ensure.sh;
  };
in
{
  imports = [ ./loom-morathustra.nix ];

  options.loom.orca = {
    enable = lib.mkEnableOption "the private headless ORCA runtime";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.orca-headless.override {
        agentIdentity = identity;
        macComputerUseEnabled = runtime.enable && runtime.macComputerUseEnabled;
      };
      description = "Pinned ORCA AppImage package used by the headless service.";
    };

    protonPassPackage = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.proton-pass-cli;
      description = "Pinned official Proton Pass CLI package used by agents.";
    };

    pairingAddress = lib.mkOption {
      type = lib.types.str;
      default = "10.44.0.2";
      description = "Private address advertised to ORCA clients.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 6768;
      description = "Private ORCA WebSocket listener port.";
    };

    allowedInterface = lib.mkOption {
      type = lib.types.str;
      default = "wg0";
      description = "WireGuard interface allowed to reach the ORCA listener.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.groups.agents = { };
    users.users.agents = {
      isNormalUser = true;
      description = "Shared LOOM agent service account";
      group = "agents";
      extraGroups = [ config.loom.group ];
      home = accountHome;
      createHome = true;
      homeMode = "0750";
      shell = pkgs.bashInteractive;
      packages = lib.optionals runtime.enable [ runtime.package ];
    } // lib.optionalAttrs (runtime.enable && runtime.macComputerUseEnabled) {
      packages = [ runtime.interactivePackage ];
    };

    # This file sorts before the generic nixos tmpfiles rules, so the accepted
    # ORCA boundary wins over the pre-deployment /srv/loom/agents placeholder.
    systemd.tmpfiles.settings."00-loom-orca" = {
      # Apply this after activation as well: the Box parent ACL deliberately
      # reduces the shared mask to execute-only before tmpfiles is reset.
      "/srv/loom"."a+".argument = "u:agents:r-x,m::r-x";
      "/srv/loom/agents".d = {
        mode = "0770";
        user = "root";
        group = "agents";
      };
      "/srv/loom/agents"."a+".argument =
        "u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x";
      "/srv/loom/box"."a+".argument =
        "u:agents:rwx,d:u:agents:rwx,m::rwx,d:m::rwx";
    };

    # ORCA agents are normal LOOM operators, not narrowly sandboxed service
    # workers. Keep broad read access explicit while named-user ACLs override
    # the shared loom group anywhere direct writes or secrets are unsafe.
    system.activationScripts.loomOrcaAccess = lib.stringAfter [
      "loomBoxParentAcl"
      "loomImportsCustodyParents"
    ] ''
      grant_agent_read_write() {
        root="$1"
        if [ -L "$root" ]; then
          echo "LOOM agent-writable root must not be a symlink: $root" >&2
          exit 1
        fi
        if [ ! -d "$root" ]; then
          return
        fi

        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg "${workspaceBoxRoot}/Topics/*"} -o -path ${lib.escapeShellArg "${workspaceBoxRoot}/Projects/*"} -o -path ${lib.escapeShellArg "${workspaceBoxRoot}/Library/*"} \) -prune -o -type d \
          -exec ${pkgs.acl}/bin/setfacl -m u:agents:rwx,d:u:agents:rwx '{}' +
        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg "${workspaceBoxRoot}/Topics/*"} -o -path ${lib.escapeShellArg "${workspaceBoxRoot}/Projects/*"} -o -path ${lib.escapeShellArg "${workspaceBoxRoot}/Library/*"} \) -prune -o -type f -perm /111 \
          -exec ${pkgs.acl}/bin/setfacl -m u:agents:rwx '{}' +
        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg "${workspaceBoxRoot}/Topics/*"} -o -path ${lib.escapeShellArg "${workspaceBoxRoot}/Projects/*"} -o -path ${lib.escapeShellArg "${workspaceBoxRoot}/Library/*"} \) -prune -o -type f ! -perm /111 \
          -exec ${pkgs.acl}/bin/setfacl -m u:agents:rw- '{}' +
      }

      grant_agent_read_only() {
        root="$1"
        if [ -L "$root" ]; then
          echo "LOOM agent-visible root must not be a symlink: $root" >&2
          exit 1
        fi
        if [ ! -d "$root" ]; then
          return
        fi

        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg cloudKeysRoot} -o -path ${lib.escapeShellArg backupEvidenceRoot} -o -path ${lib.escapeShellArg morathustraRecoveryRoot} -o -path ${lib.escapeShellArg minaRecoveryRoot} -o -path ${lib.escapeShellArg nodeAgentQuiescenceRoot} -o -path ${lib.escapeShellArg nodeAgentApplicationResultsRoot} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/topics"} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/projects"} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/library"} \) \
          -prune -o -type d \
          -exec ${pkgs.acl}/bin/setfacl -m u:agents:r-x,d:u:agents:r-x '{}' +
        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg cloudKeysRoot} -o -path ${lib.escapeShellArg backupEvidenceRoot} -o -path ${lib.escapeShellArg morathustraRecoveryRoot} -o -path ${lib.escapeShellArg minaRecoveryRoot} -o -path ${lib.escapeShellArg nodeAgentQuiescenceRoot} -o -path ${lib.escapeShellArg nodeAgentApplicationResultsRoot} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/topics"} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/projects"} -o -path ${lib.escapeShellArg "${workspaceArchiveRoot}/library"} \) \
          -prune -o ! -type d ! -type l \
          -exec ${pkgs.acl}/bin/setfacl -m u:agents:r-- '{}' +
      }

      protect_workspace_archive_categories() {
        for kind in topics projects library; do
          root=${lib.escapeShellArg workspaceArchiveRoot}/"$kind"
          if [ -L "$root" ]; then
            echo "LOOM workspace archive category must not be a symlink" >&2
            exit 1
          fi
          if [ ! -e "$root" ]; then
            continue
          fi
          if [ ! -d "$root" ] || \
             [ "$(${pkgs.coreutils}/bin/readlink -e -- "$root")" != "$root" ] || \
             [ "$(${pkgs.coreutils}/bin/stat -c %U -- "$root")" != ${lib.escapeShellArg loomServiceUser} ]; then
            echo "LOOM workspace archive category custody is invalid" >&2
            exit 1
          fi
          mode="$(${pkgs.coreutils}/bin/stat -c %a -- "$root")"
          case "$mode" in
            750|2750) ;;
            *) echo "LOOM workspace archive category mode is invalid" >&2; exit 1 ;;
          esac
          # Readers need ordinary file access, not an export or full restore.
          # Preserve payload modes/ACLs, including the original owner's rights.
          ${pkgs.acl}/bin/setfacl --physical --set \
            'u::rwx,u:${loomServiceUser}:r-x,u:agents:r-x,g::---,m::r-x,o::---,d:u::rwx,d:u:${loomServiceUser}:r-x,d:u:agents:r-x,d:g::---,d:m::r-x,d:o::---' \
            -- "$root"
          if [ "$(${pkgs.coreutils}/bin/stat -c %a -- "$root")" != "$mode" ]; then
            echo "LOOM workspace archive category mode changed" >&2
            exit 1
          fi
          # Only immediate service-owned containers; never recurse into payloads.
          for container in "$root"/*; do
            if [ ! -e "$container" ]; then
              continue
            fi
            if [ -L "$container" ] || [ ! -d "$container" ] || \
               [ "$(${pkgs.coreutils}/bin/readlink -e -- "$container")" != "$container" ] || \
               [ "$(${pkgs.coreutils}/bin/stat -c %U -- "$container")" != ${lib.escapeShellArg loomServiceUser} ]; then
              echo "LOOM workspace archive container custody is invalid" >&2
              exit 1
            fi
            mode="$(${pkgs.coreutils}/bin/stat -c %a -- "$container")"
            case "$mode" in
              750|2750) ;;
              *) echo "LOOM workspace archive container mode is invalid" >&2; exit 1 ;;
            esac
            ${pkgs.acl}/bin/setfacl --physical --no-mask \
              -m u:agents:r-x,d:u:agents:r-x -- "$container"
            if [ "$(${pkgs.coreutils}/bin/stat -c %a -- "$container")" != "$mode" ]; then
              echo "LOOM workspace archive container mode changed" >&2
              exit 1
            fi
          done
        done
      }

      protect_selected_recovery_key() {
        root="$1"
        key="$2"
        if [ "$root" != ${lib.escapeShellArg selectedRecoveryRoot} ] || \
           [ "$key" != ${lib.escapeShellArg selectedRecoveryKey} ]; then
          echo "LOOM selected Hermes recovery key path is not canonical" >&2
          exit 1
        fi
        if [ -L "$root" ] || [ ! -d "$root" ]; then
          echo "LOOM selected Hermes recovery key root must be a real directory: $root" >&2
          exit 1
        fi
        if [ "$(${pkgs.coreutils}/bin/readlink -e -- "$root")" != "$root" ]; then
          echo "LOOM selected Hermes recovery key root must resolve to itself: $root" >&2
          exit 1
        fi
        unexpected="$(${pkgs.findutils}/bin/find -P "$root" -xdev -mindepth 1 -maxdepth 1 \
          ! -path "$key" -print -quit)"
        if [ -n "$unexpected" ]; then
          echo "LOOM selected Hermes recovery key root contains an unexpected entry: $unexpected" >&2
          exit 1
        fi
        if [ -L "$key" ] || [ ! -f "$key" ]; then
          echo "LOOM selected Hermes recovery signing key must be a regular file: $key" >&2
          exit 1
        fi
        if [ "$(${pkgs.coreutils}/bin/readlink -e -- "$key")" != "$key" ]; then
          echo "LOOM selected Hermes recovery signing key must resolve to itself: $key" >&2
          exit 1
        fi
        key_identity="$(${pkgs.coreutils}/bin/stat -c '%U:%G:%h:%s' -- "$key")"
        if [ "$key_identity" != "agents:agents:1:64" ]; then
          echo "LOOM selected Hermes recovery signing key identity drifted" >&2
          exit 1
        fi

        ${pkgs.acl}/bin/setfacl -b -- "$root" "$key"
        ${pkgs.coreutils}/bin/chmod 0700 -- "$root"
        ${pkgs.coreutils}/bin/chmod 0600 -- "$key"

        if [ "$(${pkgs.coreutils}/bin/stat -c '%U:%G:%a:%h:%s' -- "$key")" != "agents:agents:600:1:64" ] || \
           [ "$(${pkgs.coreutils}/bin/stat -c '%U:%G:%a' -- "$root")" != "agents:agents:700" ]; then
          echo "LOOM selected Hermes recovery signing key custody drifted" >&2
          exit 1
        fi
        if ${pkgs.acl}/bin/getfacl -cp -- "$root" "$key" | \
          ${pkgs.gnugrep}/bin/grep -q '^user:agents:'; then
          echo "LOOM selected Hermes recovery signing key retains a named agents ACL" >&2
          exit 1
        fi
      }

      grant_loom_service_agent_workspace_read_only() {
        root="$1"
        if [ "$root" != ${lib.escapeShellArg agentWorkspaceRoot} ]; then
          echo "LOOM agent workspace ACL root is not canonical: $root" >&2
          exit 1
        fi
        if [ -L "$root" ] || [ ! -d "$root" ]; then
          echo "LOOM agent workspace must be a real directory: $root" >&2
          exit 1
        fi
        resolved="$(${pkgs.coreutils}/bin/readlink -e -- "$root")"
        if [ "$resolved" != "$root" ]; then
          echo "LOOM agent workspace must resolve to itself: $root -> $resolved" >&2
          exit 1
        fi
        owner_group_mode="$(${pkgs.coreutils}/bin/stat -c '%U:%G:%a' -- "$root")"
        if [ "$owner_group_mode" != "root:agents:770" ]; then
          echo "LOOM agent workspace must remain root:agents mode 0770: $root ($owner_group_mode)" >&2
          exit 1
        fi

        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg morathustraHermesHome} -o -path ${lib.escapeShellArg morathustraWorkspaceRecoveryRoot} -o -path ${lib.escapeShellArg minaHermesHome} -o -path ${lib.escapeShellArg minaWorkspaceRecoveryRoot} \) \
          -prune -o -type d \
          -exec ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x '{}' +
        ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -path ${lib.escapeShellArg morathustraHermesHome} -o -path ${lib.escapeShellArg morathustraWorkspaceRecoveryRoot} -o -path ${lib.escapeShellArg minaHermesHome} -o -path ${lib.escapeShellArg minaWorkspaceRecoveryRoot} \) \
          -prune -o -type f \
          -exec ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-- '{}' +
      }

      grant_loom_service_selected_recovery_read_only() {
        root="$1"
        readme="$root/README.md"
        if [ "$root" != ${lib.escapeShellArg selectedWorkspaceRecoveryRoot} ]; then
          echo "LOOM selected Hermes workspace recovery root is not canonical: $root" >&2
          exit 1
        fi
        if [ -L "$root" ] || [ ! -d "$root" ] || \
           [ "$(${pkgs.coreutils}/bin/readlink -e -- "$root")" != "$root" ]; then
          echo "LOOM selected Hermes workspace recovery root must be a real canonical directory: $root" >&2
          exit 1
        fi
        if [ -L "$readme" ] || [ ! -f "$readme" ] || \
           [ "$(${pkgs.coreutils}/bin/readlink -e -- "$readme")" != "$readme" ]; then
          echo "LOOM selected Hermes recovery README must be a real canonical file: $readme" >&2
          exit 1
        fi
        readme_identity="$(${pkgs.coreutils}/bin/stat -c '%U:%G:%h:%s' -- "$readme")"
        readme_size="$(${pkgs.coreutils}/bin/stat -c '%s' -- "$readme")"
        if [ "$readme_identity" != "agents:agents:1:$readme_size" ] || \
           [ "$readme_size" -gt 1048576 ]; then
          echo "LOOM selected Hermes recovery README identity is unsafe" >&2
          exit 1
        fi

        # Completed packages inherit a read-only loom ACL before the publisher
        # seals their exact 0750/0440 modes. The versioned README is the only
        # existing object normalized here; signed package evidence is untouched.
        ${pkgs.acl}/bin/setfacl -m u:${loomServiceUser}:r-x,d:u:${loomServiceUser}:r-x -- "$root"
        ${pkgs.acl}/bin/setfacl -b -- "$readme"
        ${pkgs.coreutils}/bin/chmod 0644 -- "$readme"
        if [ "$(${pkgs.coreutils}/bin/stat -c '%U:%G:%a:%h:%s' -- "$readme")" != "agents:agents:644:1:$readme_size" ]; then
          echo "LOOM selected Hermes recovery README mode normalization failed" >&2
          exit 1
        fi
      }

      protect_cloud_private_keys() {
        root="$1"
        if [ "$root" != ${lib.escapeShellArg cloudKeysRoot} ]; then
          echo "LOOM cloud key root is not canonical: $root" >&2
          exit 1
        fi
        if [ -L "$root" ] || [ ! -d "$root" ]; then
          echo "LOOM cloud key root must be a real directory: $root" >&2
          exit 1
        fi
        resolved="$(${pkgs.coreutils}/bin/readlink -e -- "$root")"
        if [ "$resolved" != "$root" ]; then
          echo "LOOM cloud key root must resolve to itself: $root -> $resolved" >&2
          exit 1
        fi
        owner_group="$(${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$root")"
        if [ "$owner_group" != "${loomServiceUser}:${loomServiceGroup}" ]; then
          echo "LOOM cloud key root must remain ${loomServiceUser}:${loomServiceGroup}: $root ($owner_group)" >&2
          exit 1
        fi
        unsafe_entry="$(${pkgs.findutils}/bin/find "$root" -xdev -mindepth 1 \
          \( -type l -o ! -type d ! -type f \) -print -quit)"
        if [ -n "$unsafe_entry" ]; then
          echo "LOOM cloud key tree contains a symlink or special file: $unsafe_entry" >&2
          exit 1
        fi

        ${pkgs.findutils}/bin/find "$root" -xdev -type d \
          -exec ${pkgs.coreutils}/bin/chown --no-dereference ${loomServiceUser}:${loomServiceGroup} '{}' + \
          -exec ${pkgs.acl}/bin/setfacl -b '{}' + \
          -exec ${pkgs.coreutils}/bin/chmod 0700 '{}' +
        ${pkgs.findutils}/bin/find "$root" -xdev -type f \
          -exec ${pkgs.acl}/bin/setfacl -b '{}' +
        ${pkgs.findutils}/bin/find "$root" -xdev -type f ! -name 'known_hosts*' \
          -exec ${pkgs.coreutils}/bin/chown --no-dereference ${loomServiceUser}:${loomServiceGroup} '{}' + \
          -exec ${pkgs.coreutils}/bin/chmod 0600 '{}' +

        if ${pkgs.acl}/bin/getfacl -cpR "$root" | \
          ${pkgs.gnugrep}/bin/grep -Eq '^(default:)?user:agents:'; then
          echo "LOOM cloud key tree still exposes an agents ACL: $root" >&2
          exit 1
        fi
        directory_drift="$(${pkgs.findutils}/bin/find "$root" -xdev -type d \
          \( ! -user ${loomServiceUser} -o ! -group ${loomServiceGroup} -o ! -perm 0700 \) \
          -print -quit)"
        if [ -n "$directory_drift" ]; then
          echo "LOOM cloud key directory contract drifted: $directory_drift" >&2
          exit 1
        fi
        identity_drift="$(${pkgs.findutils}/bin/find "$root" -xdev -type f ! -name 'known_hosts*' \
          \( ! -user ${loomServiceUser} -o ! -group ${loomServiceGroup} -o ! -perm 0600 \) \
          -print -quit)"
        if [ -n "$identity_drift" ]; then
          echo "LOOM cloud private identity contract drifted: $identity_drift" >&2
          exit 1
        fi
      }

      protect_backup_evidence() {
        root="$1"
        if [ "$root" != ${lib.escapeShellArg backupEvidenceRoot} ]; then
          echo "LOOM backup evidence root is not canonical" >&2
          exit 1
        fi
        if [ -L "$root" ] || [ ! -d "$root" ]; then
          echo "LOOM backup evidence root must be a real directory" >&2
          exit 1
        fi
        resolved="$(${pkgs.coreutils}/bin/readlink -e -- "$root")"
        if [ "$resolved" != "$root" ]; then
          echo "LOOM backup evidence root must resolve to itself" >&2
          exit 1
        fi
        owner_group="$(${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$root")"
        if [ "$owner_group" != "${loomServiceUser}:${loomServiceGroup}" ]; then
          echo "LOOM backup evidence root owner contract drifted" >&2
          exit 1
        fi
        unsafe_entry="$(${pkgs.findutils}/bin/find -P "$root" -xdev -mindepth 1 \
          ! -type d ! -type f ! -type l -print -quit)"
        if [ -n "$unsafe_entry" ]; then
          echo "LOOM backup evidence tree contains a link or special entry" >&2
          exit 1
        fi

        entry_count="$(${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -type d -o -type f \) -printf . | ${pkgs.coreutils}/bin/wc -c)"
        directory_count="$(${pkgs.findutils}/bin/find -P "$root" -xdev \
          -type d -printf . | ${pkgs.coreutils}/bin/wc -c)"
        if [ "$entry_count" -eq 0 ] || [ "$directory_count" -eq 0 ]; then
          echo "LOOM backup evidence reconciliation traversed no entries" >&2
          exit 1
        fi

        mode_digest_before="$(${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -type d -o -type f -o -type l \) -printf '%p\t%y\t%m\t%l\0' | \
          ${pkgs.coreutils}/bin/sort -z | ${pkgs.coreutils}/bin/sha256sum)"

        ${pkgs.findutils}/bin/find -P "$root" -xdev -type d \
          -exec ${pkgs.acl}/bin/setfacl --no-mask \
            -m u:agents:---,d:u:agents:--- -- '{}' +
        ${pkgs.findutils}/bin/find -P "$root" -xdev -type f \
          -exec ${pkgs.acl}/bin/setfacl --no-mask -m u:agents:--- -- '{}' +

        mode_digest_after="$(${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -type d -o -type f -o -type l \) -printf '%p\t%y\t%m\t%l\0' | \
          ${pkgs.coreutils}/bin/sort -z | ${pkgs.coreutils}/bin/sha256sum)"
        if [ "$mode_digest_after" != "$mode_digest_before" ]; then
          echo "LOOM backup evidence mode inventory changed during ACL reconciliation" >&2
          exit 1
        fi

        if ! ${pkgs.findutils}/bin/find -P "$root" -xdev \
          \( -type d -o -type f \) \
          -exec ${pkgs.acl}/bin/getfacl -cp -- '{}' + | \
          ${pkgs.gawk}/bin/awk -v expected="$entry_count" \
            '$0 == "user:agents:---" { count++ } END { exit count == expected ? 0 : 1 }'; then
          echo "LOOM backup evidence agents access deny verification failed" >&2
          exit 1
        fi
        if ! ${pkgs.findutils}/bin/find -P "$root" -xdev -type d \
          -exec ${pkgs.acl}/bin/getfacl -cp -- '{}' + | \
          ${pkgs.gawk}/bin/awk -v expected="$directory_count" \
            '$0 == "default:user:agents:---" { count++ } END { exit count == expected ? 0 : 1 }'; then
          echo "LOOM backup evidence agents default deny verification failed" >&2
          exit 1
        fi
      }

      grant_agent_read_write /srv/loom/box
      grant_agent_read_only /srv/loom/storage
      grant_agent_read_only /var/lib/loom
      protect_workspace_archive_categories
      protect_cloud_private_keys ${cloudKeysRoot}
      protect_backup_evidence ${backupEvidenceRoot}
      ${lib.optionalString runtime.recoveryEnabled ''
        protect_selected_recovery_key ${selectedRecoveryRoot} ${selectedRecoveryKey}
      ''}
      grant_loom_service_agent_workspace_read_only ${agentWorkspaceRoot}
      ${lib.optionalString runtime.recoveryEnabled ''
        grant_loom_service_selected_recovery_read_only ${selectedWorkspaceRecoveryRoot}
      ''}

      if [ -d /etc/loom ]; then
        ${pkgs.acl}/bin/setfacl -m u:agents:--- /etc/loom
      fi
    '';

    system.activationScripts.loomOrcaShell = lib.stringAfter [ "users" ] ''
      if [ ! -d ${accountHome} ] || [ -L ${accountHome} ]; then
        echo "LOOM agent home must be a real directory: ${accountHome}" >&2
        exit 1
      fi
      for profile in ${accountHome}/.bash_profile ${accountHome}/.bashrc; do
        if [ -L "$profile" ]; then
          echo "LOOM agent Bash profile must not be a symlink: $profile" >&2
          exit 1
        fi
      done
      ${pkgs.coreutils}/bin/install -o agents -g agents -m 0644 \
        ${agentBashProfile} ${accountHome}/.bash_profile
      ${pkgs.coreutils}/bin/install -o agents -g agents -m 0644 \
        ${agentBashrc} ${accountHome}/.bashrc

      for directory in \
        ${accountHome}/.config/proton-pass-cli \
        ${accountHome}/.local/state/proton-pass-cli; do
        if [ -L "$directory" ]; then
          echo "LOOM Proton Pass directory must not be a symlink: $directory" >&2
          exit 1
        fi
        ${pkgs.coreutils}/bin/install -d -o agents -g agents -m 0700 "$directory"
      done

      token_file=${accountHome}/.config/proton-pass-cli/agent.pat
      if [ -e "$token_file" ]; then
        if [ ! -f "$token_file" ] || [ -L "$token_file" ]; then
          echo "LOOM Proton Pass token must be a regular file: $token_file" >&2
          exit 1
        fi
        if [ "$(${pkgs.coreutils}/bin/stat -c '%U:%G:%a' "$token_file")" != "agents:agents:600" ]; then
          echo "LOOM Proton Pass token must be agents:agents mode 0600: $token_file" >&2
          exit 1
        fi
      fi
    '';

    environment.systemPackages = [
      cfg.package
      cfg.protonPassPackage
      agentPassSessionEnsure
      pkgs.git
      pkgs.nodejs_22
      pkgs.ripgrep
      pkgs.xorg.xorgserver
    ];

    networking.firewall.interfaces.${cfg.allowedInterface}.allowedTCPPorts = [ cfg.port ];

    systemd.services."orca-serve" = {
      description = "ORCA runtime server";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [
        "network-online.target"
        "wireguard-wg0.service"
      ];

      unitConfig = {
        StartLimitIntervalSec = 300;
        StartLimitBurst = 5;
      };

      path = [
        config.system.path
        pkgs.bashInteractive
        pkgs.git
        pkgs.nodejs_22
        pkgs.ripgrep
        pkgs.xorg.xorgserver
      ];

      environment = {
        HOME = accountHome;
        XDG_CONFIG_HOME = "${accountHome}/.config";
        LIBGL_ALWAYS_SOFTWARE = "1";
        # ORCA treats APPIMAGE as the stable outer executable when installing
        # its own headless CLI dispatchers. Point it at the FHS wrapper so the
        # generated commands remain executable on NixOS after every restart.
        APPIMAGE = "${cfg.package}/bin/orca";
      };

      serviceConfig = {
        Type = "simple";
        User = "agents";
        Group = "agents";
        WorkingDirectory = accountHome;
        ExecStart = "${cfg.package}/bin/orca ${lib.escapeShellArgs [
          "serve"
          "--port"
          (toString cfg.port)
          "--pairing-address"
          cfg.pairingAddress
          "--json"
        ]}";
        StandardOutput = "journal";
        StandardError = "journal";
        # The Nix AppImage FHS wrapper is the main PID. Signal the whole cgroup
        # so the wrapped Electron runtime and ORCA-owned Xvfb stop with it.
        KillMode = "control-group";
        Restart = "on-failure";
        RestartPreventExitStatus = 3;
        RestartSec = 5;
        NoNewPrivileges = true;
        UMask = "0027";
      };
    };
  };
}
