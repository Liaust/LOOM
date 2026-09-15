{ config, lib, pkgs, self, ... }:

let
  minaSelected = config.loom.mina.selected || config.loom.mina.enable || config.loom.mina.recoveryEnabled;
  identity = if minaSelected then "mina" else "morathustra";
  label = if minaSelected then "MINA" else "Morathustra";
  cfg = config.loom.${identity};
  workspaceRoot = "/srv/loom/agents/${identity}";
  boxRoot = if config.loom.boxPath != null then config.loom.boxPath else "/srv/loom/box";
  environmentPrefix = if minaSelected then "LOOM_MINA" else "LOOM_MORATHUSTRA";
  hermesHome = "${workspaceRoot}/.hermes";
  # Executables only. Named profiles and isolated GitHub authentication are
  # provisioned by the separate operator gate, never by package installation.
  externalTools = [ self.packages.${pkgs.stdenv.hostPlatform.system}.basecamp-cli pkgs.gh ];
  accountTools = pkgs.callPackage ../packages/morathustra-external-tools.nix {
    agentIdentity = identity;
    basecampAccountID = if cfg.externalAccountBinding == null then 0 else cfg.externalAccountBinding.basecampAccountID;
    basecamp-cli = self.packages.${pkgs.stdenv.hostPlatform.system}.basecamp-cli;
  };
  enabledAccountTools = lib.optionals cfg.externalAccountsEnabled accountTools;
  browser = self.packages.${pkgs.stdenv.hostPlatform.system}.agent-browser;
  browserTools = lib.optionals cfg.browserEnabled [ browser browser.chromium ];
  selectedMacBridge = profile: self.packages.${pkgs.stdenv.hostPlatform.system}.mac-computer-use-bridge.overrideAttrs (_: {
    pinnedBindingSHA256 = if profile.macComputerUseBindingSHA256 == null then "" else profile.macComputerUseBindingSHA256;
  });
  macBridge = selectedMacBridge cfg;
  macTools = lib.optional cfg.macComputerUseEnabled macBridge;
  accountAuthRoot = "/var/lib/loom-${identity}-auth";
  recoveryStateRoot = "/var/lib/loom/${identity}-recovery";
  recoverySigningKeyFile = "${recoveryStateRoot}/signing.key";
  profileCustody = pkgs.writeShellScript "loom-${identity}-profile-custody" ''
    set -eu

    profile=${lib.escapeShellArg hermesHome}
    if [ "$profile" != ${lib.escapeShellArg "${workspaceRoot}/.hermes"} ] || \
       [ -L "$profile" ] || [ ! -d "$profile" ]; then
      echo "LOOM ${label} profile must be the canonical real directory" >&2
      exit 1
    fi
    if [ "$(${pkgs.coreutils}/bin/readlink -e -- "$profile")" != "$profile" ] || \
       [ "$(${pkgs.coreutils}/bin/stat -c '%U:%G' -- "$profile")" != "agents:agents" ]; then
      echo "LOOM ${label} profile identity drifted" >&2
      exit 1
    fi
    unsafe="$(${pkgs.findutils}/bin/find -P "$profile" -xdev -mindepth 1 \
      ! -type d ! -type f ! -type s -print -quit)"
    if [ -n "$unsafe" ]; then
      echo "LOOM ${label} profile contains a link or unsupported special entry: $unsafe" >&2
      exit 1
    fi
    owner_drift="$(${pkgs.findutils}/bin/find -P "$profile" -xdev \
      \( -type d -o -type f -o -type s \) \
      \( ! -user agents -o ! -group agents \) -print -quit)"
    if [ -n "$owner_drift" ]; then
      echo "LOOM ${label} profile ownership drifted: $owner_drift" >&2
      exit 1
    fi
    linked="$(${pkgs.findutils}/bin/find -P "$profile" -xdev -type f -links +1 -print -quit)"
    if [ -n "$linked" ]; then
      echo "LOOM ${label} profile contains a multiply linked file: $linked" >&2
      exit 1
    fi

    # The live profile belongs only to Hermes. LOOM consumes its sealed
    # recovery package outside this tree, so inherited service ACLs are both
    # unnecessary and capable of changing native modes during online backup.
    ${pkgs.findutils}/bin/find -P "$profile" -xdev -type d \
      -exec ${pkgs.acl}/bin/setfacl -b -k -- '{}' + \
      -exec ${pkgs.coreutils}/bin/chmod go= -- '{}' +
    ${pkgs.findutils}/bin/find -P "$profile" -xdev -type f \
      -exec ${pkgs.acl}/bin/setfacl -b -- '{}' + \
      -exec ${pkgs.coreutils}/bin/chmod go= -- '{}' +

    ${lib.optionalString minaSelected ''
      # Native copies may retain Nix's read-only modes. Hermes owns this tree;
      # managed external skills live outside it and remain read-only.
      if [ -d "$profile/skills" ]; then
        ${pkgs.findutils}/bin/find -P "$profile/skills" -xdev -type d \
          -exec ${pkgs.coreutils}/bin/chmod u+rwx,go= -- '{}' +
        ${pkgs.findutils}/bin/find -P "$profile/skills" -xdev -type f \
          -exec ${pkgs.coreutils}/bin/chmod u+rw,go= -- '{}' +
      fi
    ''}

    exposed="$(${pkgs.findutils}/bin/find -P "$profile" -xdev \
      \( -type d -o -type f \) -perm /0077 -print -quit)"
    if [ -n "$exposed" ]; then
      echo "LOOM ${label} profile retains group or other access: $exposed" >&2
      exit 1
    fi
    if ${pkgs.acl}/bin/getfacl -cpR "$profile" | \
      ${pkgs.gnugrep}/bin/grep -Eq '^(default:|user:loom:)'; then
      echo "LOOM ${label} profile retains an inherited service ACL" >&2
      exit 1
    fi
  '';
  recoveryPublish = pkgs.writeShellScript "loom-${identity}-recovery-publish" ''
    set -euo pipefail
    epoch="$(${pkgs.coreutils}/bin/date --utc +%s)"
    id="$(${pkgs.coreutils}/bin/date --utc --date="@''${epoch}" +${identity}-%Y%m%d-%H%M%S)"
    created_at="$(${pkgs.coreutils}/bin/date --utc --date="@''${epoch}" +%Y-%m-%dT%H:%M:%SZ)"
    exec ${cfg.recoveryPackage}/bin/loom-hermes-recovery \
      --mode publish \
      --identity ${identity} \
      --workspace ${lib.escapeShellArg workspaceRoot} \
      --hermes ${cfg.package}/bin/hermes \
      --id "''${id}" \
      --created-at "''${created_at}" \
      --signing-key-file ${lib.escapeShellArg recoverySigningKeyFile}
  '';
  # Built-in and bundled plugin platforms in the pinned v0.21.0 release.
  # Explicit false also defeats credential-based platform auto-detection.
  platforms = [
    "local" "telegram" "discord" "whatsapp" "whatsapp_cloud" "slack"
    "signal" "mattermost" "matrix" "homeassistant" "email" "sms"
    "dingtalk" "api_server" "webhook" "msgraph_webhook" "feishu"
    "wecom" "wecom_callback" "weixin" "bluebubbles" "qqbot" "yuanbao"
    "relay" "simplex" "ntfy" "a2a" "teams" "irc" "google_chat"
    "buzz" "raft" "line" "photon"
  ];
  runtimeOptions = name:
    let
      profileCfg = config.loom.${name};
      workspaceRoot = "/srv/loom/agents/${name}";
      hermesHome = "${workspaceRoot}/.hermes";
      recoverySigningKeyFile = "/var/lib/loom/${name}-recovery/signing.key";
    in {
      browserEnabled = lib.mkEnableOption "Nix-managed local Chromium and native agent-browser for agents and Hermes";
      externalAccountsEnabled = lib.mkEnableOption "verified ${name} native account entry points (existing operator auth required)";
      externalAccountBinding = lib.mkOption {
        default = null;
        type = lib.types.nullOr (lib.types.submodule {
          options = {
            githubLogin = lib.mkOption { type = lib.types.strMatching "[A-Za-z0-9][A-Za-z0-9-]*"; };
            githubID = lib.mkOption { type = lib.types.ints.positive; };
            basecampIdentityID = lib.mkOption { type = lib.types.ints.positive; };
            basecampAccountID = lib.mkOption { type = lib.types.ints.positive; };
            basecampPersonID = lib.mkOption { type = lib.types.ints.positive; };
            basecampEmail = lib.mkOption { type = lib.types.strMatching "[^[:space:]@]+@[^[:space:]@]+"; };
          };
        });
        description = "Operator-verified public account identifiers, not tokens. Required when enabling named external account tools; the Basecamp profile remains bound to this runtime's identity.";
      };
      enable = lib.mkEnableOption "the native ${name} Hermes gateway";
      macComputerUseEnabled = lib.mkEnableOption "the selected runtime's operator-bound native Mac computer use";

      macComputerUseBindingSHA256 = lib.mkOption {
        type = lib.types.nullOr (lib.types.strMatching "[0-9a-f]{64}");
        default = null;
        description = "Operator-verified public binding hash embedded in the immutable bridge; supports rootless FHS ownership translation without trusting unmapped UIDs.";
      };

      macComputerUseEnvironment = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        readOnly = true;
        default = lib.optionalAttrs profileCfg.macComputerUseEnabled {
          HERMES_CUA_DRIVER_CMD = "${selectedMacBridge profileCfg}/bin/loom-mac-computer-use-bridge";
          HERMES_MAC_BINDING = "/etc/loom-mac-computer-use/binding.json";
        };
        description = "Public setup binding and pinned launcher; no runtime or key bytes are published by Nix.";
      };

      interactivePackage = lib.mkOption {
        type = lib.types.package;
        readOnly = true;
        default = if !profileCfg.macComputerUseEnabled then profileCfg.package else
          let
            # Select at invocation, not shell initialization: an explicit other
            # HERMES_HOME must not inherit the route from an already-open TUI.
            selector = ''
              if [ "''${HERMES_HOME-}" = ${lib.escapeShellArg hermesHome} ]; then
                ${lib.toShellVars profileCfg.macComputerUseEnvironment}
                export HERMES_CUA_DRIVER_CMD HERMES_MAC_BINDING
              else
                unset HERMES_CUA_DRIVER_CMD HERMES_MAC_BINDING
              fi
            '';
          in pkgs.symlinkJoin {
            name = "${name}-hermes-mac-selected";
            paths = [ profileCfg.package ];
            nativeBuildInputs = [ pkgs.makeWrapper ];
            postBuild = ''
              for executable in hermes hermes-agent hermes-acp; do
                wrapProgram "$out/bin/$executable" --run ${lib.escapeShellArg selector}
              done
            '';
            passthru.macSelector = selector;
          };
        description = "Selected-profile launch environment only; the underlying pinned native package is unchanged.";
      };

      recoveryEnabled = lib.mkEnableOption "authenticated ${name} recovery coverage and the explicit recovery producer";

      model = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "Non-secret default Hermes inference model. An empty value keeps model execution disabled.";
      };

      modelProvider = lib.mkOption {
        type = lib.types.str;
        default = "auto";
        description = "Hermes provider used with the configured default model.";
      };

      package = lib.mkOption {
        type = lib.types.package;
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.hermes-agent;
        description = "Pinned native Hermes CLI, TUI and messaging gateway.";
      };

      workspaceRoot = lib.mkOption {
        type = lib.types.str;
        readOnly = true;
        default = workspaceRoot;
        description = "The existing ${name} folder project in ORCA.";
      };

      hermesHome = lib.mkOption {
        type = lib.types.str;
        readOnly = true;
        default = hermesHome;
        description = "The single shared gateway and interactive Hermes profile.";
      };

      recoveryPublicKey = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "Independently provisioned Ed25519 recovery verification key (hex, public only). Required only when recoveryEnabled is true.";
      };

      recoveryPackage = lib.mkOption {
        type = lib.types.package;
        readOnly = true;
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.loom.overrideAttrs (_: {
          pname = "loom-hermes-recovery";
          subPackages = [ "internal/hermesprofile/cmd/loom-hermes-recovery" ];
          postInstall = "";
        });
        description = "Recovery adapter used by the gated pre-backup publisher; requires agents and an external signing key.";
      };

      recoverySigningKeyFile = lib.mkOption {
        type = lib.types.str;
        readOnly = true;
        default = recoverySigningKeyFile;
        description = "Fixed external owner-only Ed25519 signing key used by the recovery publisher.";
      };

      baselineSettings = lib.mkOption {
        type = lib.types.attrs;
        readOnly = true;
        default = {
          model = { default = profileCfg.model; provider = profileCfg.modelProvider; };
          terminal.cwd = workspaceRoot;
          platforms = lib.genAttrs platforms (_: { enabled = false; });
          curator = {
            enabled = true;
            prune_builtins = false;
            consolidate = false;
            interval_hours = 168;
            min_idle_hours = 2;
            stale_after_days = 30;
            archive_after_days = 90;
            archive_ttl_days = 0;
            backup = { enabled = true; keep = 5; };
          };
          memory.write_approval = name != "mina";
          skills = {
            external_dirs = [ "${workspaceRoot}/skills/installed" ];
            write_approval = name != "mina";
            guard_agent_created = name != "mina";
            # Disable selection only; keep bundled files and their ownership.
            disabled = lib.filter (skill: !profileCfg.macComputerUseEnabled || skill != "computer-use") [
              "airtable" "arxiv" "ascii-video" "baoyu-infographic" "box"
              "claude-code" "claude-design" "codebase-inspection" "codex"
              "competitor-news-monitor" "computer-use" "design-md" "dogfood"
              "email-inbox-triage" "gif-search" "google-workspace"
              "hermes-agent-skill-authoring" "himalaya" "humanizer"
              "inspecting-hermes-desktop-dom" "llm-wiki" "manim-video" "maps"
              "node-inspect-debugger" "notion" "obsidian" "opencode" "p5js"
              "popular-web-designs" "product-price-monitor" "python-debugpy"
              "requesting-code-review" "sdlc-review" "simplify-code" "songsee"
              "songwriting-and-ai-music" "spike" "systematic-debugging"
              "teams-meeting-pipeline" "test-driven-development" "xurl"
              "youtube-content"
            ];
          };
          sessions = {
            retention_days = 90;
            auto_prune = false;
            auto_archive = false;
          };
          security.allow_lazy_installs = false;
        } // lib.optionalAttrs profileCfg.macComputerUseEnabled {
          # Pinned Hermes uses cli for both interactive and LOCAL gateway runs.
          # Retain the native composite; this template never rewrites live config.
          platform_toolsets.cli = [ "hermes-cli" "computer_use" ];
          computer_use.backend = "cua";
        } // lib.optionalAttrs (name == "mina") { display.skin = "mina-matrix-teal"; };
        description = "Non-secret pilot baseline; account and adapter activation are separate reviewed work.";
      };

      baselineConfig = lib.mkOption {
        type = lib.types.package;
        readOnly = true;
        # JSON is a YAML subset. No interpreter or mutable installer is needed
        # to render this template, and no secret-bearing settings option exists.
        default = pkgs.writeText "${name}-hermes-config.yaml" (builtins.toJSON profileCfg.baselineSettings);
        description = "Baseline config template for later non-overwriting profile provisioning; never installed by this module.";
      };
    };

in
{
  options.loom.morathustra = runtimeOptions "morathustra";
  options.loom.mina = runtimeOptions "mina" // {
    selected = lib.mkEnableOption "MINA identity even while its gateway and recovery producer are disabled";
    retainedMorathustra = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = "Exact retained old signed package IDs at the selected MINA recovery root. No discovery or implicit admission.";
    };
    skin = lib.mkOption {
      type = lib.types.package;
      readOnly = true;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.mina-matrix-teal;
      description = "Immutable supplied native skin for later non-overwriting operator publication; never installed into a profile here.";
    };
  };

  config = lib.mkMerge [
    {
      assertions = [
        {
          assertion = !cfg.externalAccountsEnabled || cfg.externalAccountBinding != null;
          message = "Named external account tools require an explicit externalAccountBinding verified by the operator.";
        }
        {
          assertion = !config.loom.morathustra.macComputerUseEnabled || (!minaSelected && config.loom.morathustra.enable);
          message = "Morathustra Mac computer use requires its sole selected enabled runtime.";
        }
        {
          assertion = !config.loom.mina.macComputerUseEnabled || (minaSelected && config.loom.mina.enable);
          message = "MINA Mac computer use requires its sole selected enabled runtime.";
        }
        {
          assertion = !minaSelected || !(config.loom.morathustra.enable || config.loom.morathustra.recoveryEnabled || config.loom.morathustra.recoveryPublicKey != "");
          message = "Conflicting Morathustra and MINA runtime/recovery selections.";
        }
        {
          assertion = minaSelected || (config.loom.mina.recoveryPublicKey == "" && config.loom.mina.retainedMorathustra == []);
          message = "MINA recovery bindings require explicit MINA selection.";
        }
        {
          assertion = builtins.length config.loom.mina.retainedMorathustra <= 8192 && builtins.all (id: builtins.match "[a-z0-9][a-z0-9-]{0,79}" id != null) config.loom.mina.retainedMorathustra
            && builtins.length config.loom.mina.retainedMorathustra == builtins.length (lib.unique config.loom.mina.retainedMorathustra);
          message = "Invalid or duplicate retained Morathustra recovery binding.";
        }
      ];
    }
    (lib.mkIf minaSelected {
      assertions = [{
        assertion = cfg.recoveryPublicKey == "" || builtins.match "[0-9a-fA-F]{64}" cfg.recoveryPublicKey != null;
        message = "MINA recovery public key must be 64 hexadecimal characters.";
      }];
      systemd.services.loomd.environment = {
        LOOM_MINA_SELECTED = "true";
        LOOM_MINA_RECOVERY_PUBLIC_KEY = cfg.recoveryPublicKey;
        LOOM_MINA_ENABLED = lib.boolToString cfg.enable;
        LOOM_MINA_RECOVERY_ENABLED = lib.boolToString cfg.recoveryEnabled;
        LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS = builtins.toJSON cfg.retainedMorathustra;
      };
    })
    (lib.mkIf (cfg.enable || cfg.recoveryEnabled) {
      assertions = [
        {
          assertion = config.loom.orca.enable;
          message = "${label} requires the existing LOOM ORCA agents account.";
        }
        {
          assertion = config.users.users.agents.group == "agents";
          message = "${label} must retain the agents:agents service identity.";
        }
        {
          assertion = (cfg.model == "" && cfg.modelProvider == "auto")
            || (cfg.model != "" && cfg.modelProvider != "" && cfg.modelProvider != "auto");
          message = "${label} model activation requires an explicit model and non-auto provider, or the disabled empty-model/auto-provider pair.";
        }
      ];

      # Public trust/configuration only. Profile initialization and provider
      # credentials remain outside this module.
      systemd.services.loomd.environment = {
        "${environmentPrefix}_ENABLED" = lib.boolToString cfg.enable;
        "${environmentPrefix}_RECOVERY_ENABLED" = lib.boolToString cfg.recoveryEnabled;
      };
    })

    (lib.mkIf cfg.recoveryEnabled {
      assertions = [{
        assertion = builtins.match "[0-9a-fA-F]{64}" cfg.recoveryPublicKey != null;
        message = "${label} recovery requires a 64-character hexadecimal Ed25519 public verification key.";
      }];
      systemd.services.loomd.environment."${environmentPrefix}_RECOVERY_PUBLIC_KEY" = cfg.recoveryPublicKey;
      users.users.agents.packages = [ cfg.recoveryPackage ];
      systemd.tmpfiles.rules = [ "d ${recoveryStateRoot} 0700 agents agents - -" ];

      systemd.services."loom-${identity}-recovery-publish" = {
        description = "Publish authenticated ${label} recovery evidence";
        requires = [ "loom-${identity}.service" ];
        after = [ "loom-${identity}.service" ];
        unitConfig = {
          RequiresMountsFor = [ workspaceRoot recoveryStateRoot ];
          AssertPathExists = [ recoverySigningKeyFile ];
          AssertPathIsSymbolicLink = map (path: "!${path}") [
            workspaceRoot hermesHome recoveryStateRoot recoverySigningKeyFile
          ];
        };
        serviceConfig = {
          Type = "oneshot";
          User = "agents";
          Group = "agents";
          WorkingDirectory = workspaceRoot;
          ExecStart = recoveryPublish;
          NoNewPrivileges = true;
          UMask = "0077";
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = true;
          PrivateDevices = true;
          PrivateNetwork = true;
          ReadWritePaths = [ workspaceRoot ];
          ReadOnlyPaths = [ recoverySigningKeyFile "-${workspaceRoot}/skills/installed" ];
          TimeoutStartSec = 480;
          StandardOutput = "journal";
          StandardError = "journal";
        };
      };

      systemd.timers."loom-${identity}-recovery-publish" = {
        description = "Publish ${label} recovery evidence before daily backup";
        wantedBy = [ "timers.target" ];
        timerConfig = {
          OnCalendar = "*-*-* 02:45:00 Europe/Amsterdam";
          AccuracySec = "1s";
          RandomizedDelaySec = "0";
          Persistent = true;
        };
      };
    })

    (lib.mkIf cfg.enable {

      users.users.agents.packages = externalTools ++ enabledAccountTools ++ browserTools ++ macTools;

      environment.etc = lib.optionalAttrs (cfg.externalAccountsEnabled && cfg.externalAccountBinding != null) {
        "loom-${identity}-accounts.json" = {
          mode = "symlink";
          text = builtins.toJSON {
            schema_version = 1;
            github = { host = "github.com"; login = cfg.externalAccountBinding.githubLogin; id = cfg.externalAccountBinding.githubID; };
            basecamp = {
              profile = identity;
              identity_id = cfg.externalAccountBinding.basecampIdentityID;
              account_id = cfg.externalAccountBinding.basecampAccountID;
              person_id = cfg.externalAccountBinding.basecampPersonID;
              email = cfg.externalAccountBinding.basecampEmail;
            };
          };
        };
      };

      # Profile creation/migration belongs to the later reviewed deployment.
      # Enabling a package must not overwrite existing workspace or auth state.
      systemd.services."loom-${identity}" = {
        description = "${label} Hermes gateway";
        wantedBy = [ "multi-user.target" ];
        wants = [ "network-online.target" ];
        after = [ "network-online.target" ];
        unitConfig = {
          RequiresMountsFor = [ workspaceRoot ] ++ lib.optional minaSelected boxRoot;
          # Managed Hermes checks these directories rather than provisioning
          # them. Reject an absent SOUL; later provisioning must also validate
          # its content because upstream replaces empty legacy templates.
          AssertPathIsDirectory = [ workspaceRoot hermesHome ]
            ++ lib.optional minaSelected boxRoot
            ++ map (name: "${hermesHome}/${name}") [ "cron" "sessions" "logs" "memories" ];
          AssertPathExists = [ "${hermesHome}/config.yaml" "${hermesHome}/SOUL.md" ];
          AssertPathIsSymbolicLink = map (path: "!${path}") [
            workspaceRoot hermesHome "${hermesHome}/skills"
            "${hermesHome}/config.yaml" "${hermesHome}/SOUL.md"
          ];
          StartLimitIntervalSec = 300;
          StartLimitBurst = 5;
        };
        environment = {
          HOME = "/home/agents";
          HERMES_HOME = hermesHome;
        } // cfg.macComputerUseEnvironment // lib.optionalAttrs cfg.browserEnabled {
          LOOM_BROWSER_RUNTIME_DIR = "/run/loom-${identity}-browser";
        } // lib.optionalAttrs (cfg.model != "") {
          HERMES_INFERENCE_MODEL = cfg.model;
          HERMES_TUI_PROVIDER = cfg.modelProvider;
        };
        path = [ cfg.interactivePackage pkgs.bash pkgs.coreutils ] ++ externalTools ++ enabledAccountTools ++ browserTools ++ macTools;
        serviceConfig = {
          Type = "simple";
          User = "agents";
          Group = "agents";
          WorkingDirectory = workspaceRoot;
          ExecStartPre = profileCustody;
          ExecStart = "${cfg.package}/bin/hermes gateway";
          NoNewPrivileges = true;
          UMask = "0077";
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = true;
          ReadWritePaths = [ workspaceRoot ] ++ lib.optional minaSelected boxRoot
            ++ lib.optional cfg.externalAccountsEnabled accountAuthRoot;
          # Optional paths keep chat/startup independent of unprovisioned or
          # offline Mac access. Operator setup must bind this fixed agents-owned
          # 0400/0600 key outside ProtectHome; Nix never provisions it or its parent.
          ReadOnlyPaths = [ "-${workspaceRoot}/skills/installed" ] ++ lib.optionals cfg.macComputerUseEnabled [
            "-/etc/loom-mac-computer-use"
            "-/var/lib/loom-mac-computer-use/id_ed25519"
          ];
          KillMode = "control-group";
          Restart = "on-failure";
          RestartSec = 5;
          StandardOutput = "journal";
          StandardError = "journal";
        } // lib.optionalAttrs cfg.browserEnabled {
          RuntimeDirectory = "loom-${identity}-browser";
          RuntimeDirectoryMode = "0700";
        };
      };
    })
  ];
}
