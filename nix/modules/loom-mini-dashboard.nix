{ config, lib, pkgs, self, ... }:

let
  cfg = config.loom.miniDashboard;
  system = pkgs.stdenv.hostPlatform.system;
  stateDirectory = "/var/lib/loom/mini-dashboard";
  loopbackListen =
    lib.hasPrefix "127.0.0.1:" cfg.listenAddress
    || lib.hasPrefix "[::1]:" cfg.listenAddress;
  dashboardURL = "http://${cfg.listenAddress}/";
  kioskWrapper = pkgs.writeShellApplication {
    name = "loom-mini-dashboard-kiosk";
    runtimeInputs = [
      pkgs.chromium
      pkgs.coreutils
      pkgs.curl
      pkgs.findutils
      pkgs.gawk
      pkgs.gnome-session
      pkgs.gnused
      pkgs.xorg.xrandr
    ];
    text = ''
      # BEGIN LOOM MINI DASHBOARD KIOSK WRAPPER
      set -euo pipefail

      parse_lcd_geometry() {
        layout=$(cat)
        connector_lines=$(printf '%s\n' "$layout" | awk '$1 == "HDMI-1" && $2 == "connected" { count++ } END { print count + 0 }')
        [ "$connector_lines" -eq 1 ] || return 1

        geometry=$(printf '%s\n' "$layout" | sed -nE 's/^HDMI-1 connected 1024x600\+([0-9]+)\+([0-9]+)([[:space:]].*)?$/\1 \2/p')
        [ -n "$geometry" ] || return 1
        [ "$(printf '%s\n' "$geometry" | wc -l | tr -d ' ')" -eq 1 ] || return 1
        printf '%s\n' "$geometry"
      }

      if [ "$#" -eq 1 ] && [ "$1" = "--parse-xrandr" ]; then
        parse_lcd_geometry
        exit $?
      fi

      test_launch=false
      if [ "$#" -eq 1 ] && [ "$1" = "--test-launch" ]; then
        test_launch=true
      elif [ "$#" -ne 0 ]; then
        printf 'loom-mini-dashboard-kiosk: unsupported arguments\n' >&2
        exit 2
      fi

      log() {
        printf 'loom-mini-dashboard-kiosk: %s\n' "$*" >&2
      }

      discover_xauthority() {
        set -- "$runtime_dir"/.mutter-Xwaylandauth.*
        [ "$#" -eq 1 ] || return 1
        [ "$1" != "$runtime_dir/.mutter-Xwaylandauth.*" ] || return 1
        [ -f "$1" ] && [ -r "$1" ] || return 1
        printf '%s\n' "$1"
      }

      query_lcd_geometry() {
        xrandr --query | parse_lcd_geometry
      }

      wait_for_display_and_companion() {
        candidate_xauthority=
        candidate_geometry=
        attempt=1
        while [ "$attempt" -le "$max_attempts" ]; do
          if candidate_xauthority=$(discover_xauthority); then
            export XAUTHORITY="$candidate_xauthority"
            if candidate_geometry=$(query_lcd_geometry) \
              && curl --fail --silent --show-error --max-time 2 "$dashboard_url"healthz >/dev/null; then
              xauthority="$candidate_xauthority"
              geometry="$candidate_geometry"
              return 0
            fi
          fi
          if [ "$retry_delay" -gt 0 ]; then
            sleep "$retry_delay"
          fi
          attempt=$((attempt + 1))
        done
        return 1
      }

      display=$(printenv DISPLAY || true)
      state_directory=$(printenv STATE_DIRECTORY || true)
      dashboard_url=$(printenv LOOM_MINI_DASHBOARD_URL || true)

      if [ "$test_launch" = true ]; then
        runtime_dir=$(printenv LOOM_MINI_DASHBOARD_TEST_RUNTIME_DIR || true)
        max_attempts=1
        retry_delay=0
        if [ -z "$runtime_dir" ]; then
          log "test runtime directory is unavailable"
          exit 1
        fi
      else
        runtime_dir="/run/user/$(id -u)"
        max_attempts=30
        retry_delay=2
      fi

      if [ "$(id -un)" != "loomadmin" ]; then
        log "refusing to run outside the loomadmin graphical session"
        exit 1
      fi
      if [ -z "$display" ]; then
        log "DISPLAY is unavailable"
        exit 1
      fi
      if [ -z "$state_directory" ]; then
        log "STATE_DIRECTORY is unavailable"
        exit 1
      fi
      if [ -z "$dashboard_url" ]; then
        log "dashboard URL is unavailable"
        exit 1
      fi
      case "$dashboard_url" in
        http://127.0.0.1:*) resolver_exclusion="127.0.0.1" ;;
        http://\[::1\]:*) resolver_exclusion="::1" ;;
        *)
          log "dashboard URL is not an explicit loopback address"
          exit 1
          ;;
      esac
      export DISPLAY="$display"

      xauthority=
      geometry=
      if ! wait_for_display_and_companion; then
        log "HDMI-1, Xwayland authentication, or companion health did not become uniquely available"
        exit 1
      fi
      export XAUTHORITY="$xauthority"
      read -r lcd_x lcd_y <<EOF
      $geometry
      EOF

      profile_dir="$state_directory/chromium"
      config_home="$state_directory/config"
      cache_home="$state_directory/cache"
      data_home="$state_directory/data"
      mkdir -p "$profile_dir" "$config_home" "$cache_home" "$data_home"
      export XDG_CONFIG_HOME="$config_home"
      export XDG_CACHE_HOME="$cache_home"
      export XDG_DATA_HOME="$data_home"

      launch_pid=
      cleanup() {
        if [ -n "$launch_pid" ] && kill -0 "$launch_pid" 2>/dev/null; then
          kill "$launch_pid" 2>/dev/null || true
          wait "$launch_pid" 2>/dev/null || true
        fi
      }
      trap cleanup EXIT INT TERM

      gnome-session-inhibit \
        --app-id=loom-mini-dashboard-kiosk \
        --reason="Keep the physical LOOM status panel visible" \
        --inhibit=idle:suspend \
        chromium \
          --kiosk \
          --ozone-platform=x11 \
          --window-position="$lcd_x,$lcd_y" \
          --window-size=1024,600 \
          --user-data-dir="$profile_dir" \
          --no-first-run \
          --no-default-browser-check \
          --disable-background-networking \
          --disable-component-update \
          --disable-default-apps \
          --disable-extensions \
          --disable-features=AutofillServerCommunication,CertificateTransparencyComponentUpdater,InterestFeedContentSuggestions,MediaRouter,OptimizationHints,Translate \
          --disable-session-crashed-bubble \
          --disable-sync \
          --hide-crash-restore-bubble \
          --host-resolver-rules="MAP * ~NOTFOUND, EXCLUDE $resolver_exclusion" \
          "$dashboard_url" &
      launch_pid=$!

      while kill -0 "$launch_pid" 2>/dev/null; do
        sleep 5
        if ! current_geometry=$(query_lcd_geometry); then
          log "HDMI-1 disappeared or became ambiguous; stopping Chromium for a bounded restart"
          exit 1
        fi
        if [ "$current_geometry" != "$geometry" ]; then
          log "HDMI-1 moved; stopping Chromium so it can restart at the new coordinates"
          exit 1
        fi
      done

      wait "$launch_pid"
      # END LOOM MINI DASHBOARD KIOSK WRAPPER
    '';
  };
in
{
  options.loom.miniDashboard = {
    enable = lib.mkEnableOption "the loopback-only LOOM mini-dashboard companion";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.loom-mini-dashboard;
      description = "Package providing the loom-mini-dashboard companion.";
    };

    listenAddress = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:8090";
      description = "Loopback address used by the mini-dashboard HTTP server.";
    };

    socketPath = lib.mkOption {
      type = lib.types.str;
      default = config.loom.socketPath;
      description = "Local loomd Unix socket read by the companion.";
    };

    statePath = lib.mkOption {
      type = lib.types.str;
      default = "${stateDirectory}/last-good.json";
      description = "Last-good dashboard cache within the dedicated state directory.";
    };

    dataRoot = lib.mkOption {
      type = lib.types.str;
      default = config.loom.storageRoot;
      description = "Read-only LOOM filesystem root sampled for capacity telemetry.";
    };

    kiosk.enable = lib.mkEnableOption "the loomadmin GNOME LCD kiosk";
  };

  config = lib.mkIf (cfg.enable || cfg.kiosk.enable) {
    assertions = [
      {
        assertion = !cfg.kiosk.enable || cfg.enable;
        message = "loom.miniDashboard.kiosk.enable requires loom.miniDashboard.enable.";
      }
      {
        assertion = loopbackListen;
        message = "loom.miniDashboard.listenAddress must use an explicit loopback IP.";
      }
      {
        assertion = builtins.dirOf cfg.statePath == stateDirectory;
        message = "loom.miniDashboard.statePath must be a file directly within ${stateDirectory}.";
      }
      {
        assertion = lib.hasPrefix "/" cfg.socketPath;
        message = "loom.miniDashboard.socketPath must be absolute.";
      }
      {
        assertion = lib.hasPrefix "/" cfg.dataRoot;
        message = "loom.miniDashboard.dataRoot must be absolute.";
      }
    ];

    systemd.services."loom-mini-dashboard" = lib.mkIf cfg.enable {
      description = "LOOM mini-dashboard companion";
      wantedBy = [ "multi-user.target" ];
      after = [ "loomd.service" ];

      serviceConfig = {
        Type = "simple";
        User = config.loom.user;
        Group = config.loom.group;
        WorkingDirectory = stateDirectory;
        ExecStart = "${cfg.package}/bin/loom-mini-dashboard ${lib.escapeShellArgs [
          "--listen" cfg.listenAddress
          "--state" cfg.statePath
          "--loomd-socket" cfg.socketPath
          "--data-dir" cfg.dataRoot
        ]}";
        Restart = "on-failure";
        RestartSec = "2s";
        StateDirectory = "loom/mini-dashboard";
        StateDirectoryMode = "0750";
        UMask = "0027";

        NoNewPrivileges = true;
        PrivateDevices = true;
        PrivateTmp = true;
        ProtectClock = true;
        ProtectControlGroups = true;
        ProtectHome = true;
        ProtectHostname = true;
        ProtectKernelLogs = true;
        ProtectKernelModules = true;
        ProtectKernelTunables = true;
        ProtectSystem = "strict";
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        SystemCallArchitectures = "native";
        CapabilityBoundingSet = "";
        AmbientCapabilities = "";
        IPAddressDeny = "any";
        IPAddressAllow = [ "localhost" ];
        ReadOnlyPaths = [ "/run" cfg.dataRoot ];
        ReadWritePaths = [ stateDirectory ];
      };
    };

    systemd.user.services."loom-mini-dashboard-kiosk" = lib.mkIf cfg.kiosk.enable {
      description = "LOOM mini-dashboard kiosk on the dedicated HDMI LCD";
      wantedBy = [ "graphical-session.target" ];
      partOf = [ "graphical-session.target" ];
      unitConfig.ConditionUser = "loomadmin";

      serviceConfig = {
        Type = "simple";
        ExecStart = "${kioskWrapper}/bin/loom-mini-dashboard-kiosk";
        KillMode = "control-group";
        Restart = "always";
        RestartSec = "10s";
        StateDirectory = "loom-mini-dashboard";
        StateDirectoryMode = "0700";
        UMask = "0077";
        Environment = [ "LOOM_MINI_DASHBOARD_URL=${dashboardURL}" ];
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        RestrictSUIDSGID = true;
        LockPersonality = true;
        IPAddressDeny = "any";
        IPAddressAllow = [ "localhost" ];
      };
    };

    system.build.loomMiniDashboardKioskWrapper = lib.mkIf cfg.kiosk.enable kioskWrapper;
  };
}
