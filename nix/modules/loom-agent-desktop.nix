{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.agentDesktop;
  preferencesPython = pkgs.python3.withPackages (ps: [ ps.pygobject3 ]);
  driver = pkgs.callPackage ../packages/cua-driver-linux.nix { };
  nativeHermes = self.packages.${pkgs.stdenv.hostPlatform.system}.hermes-native-agent;
  botRuntime = pkgs.writeShellScriptBin "loom-hermes-desktop" ''
    export PYTHONDONTWRITEBYTECODE=1
    export LOOM_DESKTOP_MODE=bot
    export LOOM_CUA_BINARY=${driver}/bin/cua-driver
    export LOOM_CUA_LAUNCHER="$(${pkgs.coreutils}/bin/readlink -f "$0")"
    exec ${nativeHermes.hermesVenv}/bin/python3 -B ${../files/agent-desktop/session_launcher.py} "$@"
  '';
  desktopTheme = pkgs.fluent-gtk-theme.override {
    themeVariants = [ "teal" ];
    colorVariants = [ "dark" ];
    sizeVariants = [ "standard" ];
  };
  darkTheme = pkgs.colloid-gtk-theme.override {
    themeVariants = [ "teal" ];
    colorVariants = [ "dark" ];
    sizeVariants = [ "standard" ];
    tweaks = [ "black" ];
  };
  compositor = pkgs.writeShellScriptBin "loom-agent-compositor" ''
    export PATH=${lib.makeBinPath [ pkgs.systemd pkgs.xfconf ]}:"$PATH"
    export PYTHONPATH=${../files/agent-desktop}
    export LOOM_PICOM_BINARY=${pkgs.picom}/bin/picom
    export LOOM_PICOM_CONFIG=${../files/agent-desktop/picom.conf}
    exec ${pkgs.python3}/bin/python3 -B ${../files/agent-desktop/compositor.py} "$@"
  '';
  darkPalette = pkgs.writeShellScriptBin "loom-agent-dark-palette" ''
    export GI_TYPELIB_PATH=${lib.makeSearchPath "lib/girepository-1.0" (map lib.getLib [ pkgs.glib pkgs.gobject-introspection ])}
    export PYTHONPATH=${../files/agent-desktop}
    export LOOM_DARK_THEME=Colloid-Teal-Dark
    export LOOM_DARK_THEME_ROOT=${darkTheme}/share/themes
    export LOOM_DARK_PALETTE_CSS=${../files/agent-desktop/near-black.css}
    exec ${preferencesPython}/bin/python3 -B ${../files/agent-desktop/dark_palette.py} "$@"
  '';
  runtime = pkgs.writeShellScriptBin "loom-main-desktop" ''
    export PYTHONDONTWRITEBYTECODE=1
    export PATH=${lib.makeBinPath [ pkgs.systemd ]}:"$PATH"
    export LOOM_CUA_BINARY=${driver}/bin/cua-driver
    export LOOM_CUA_LAUNCHER="$(${pkgs.coreutils}/bin/readlink -f "$0")"
    exec ${pkgs.python3}/bin/python3 -B ${../files/agent-desktop/session_launcher.py} "$@"
  '';
  preferences = pkgs.writeShellScript "loom-agent-gnome-preferences" ''
    export GI_TYPELIB_PATH=${lib.makeSearchPath "lib/girepository-1.0" (map lib.getLib [ pkgs.glib pkgs.gobject-introspection ])}
    exec ${preferencesPython}/bin/python3 -B ${../files/agent-desktop/preferences.py}
  '';
  xfcePreferences = pkgs.writeShellScriptBin "loom-agent-xfce-appearance" ''
    export GI_TYPELIB_PATH=${lib.makeSearchPath "lib/girepository-1.0" (map lib.getLib [ pkgs.glib pkgs.gobject-introspection ])}
    export PYTHONPATH=${../files/agent-desktop}
    export LOOM_XFCE_THEME_ROOT=${desktopTheme}/share/themes
    exec ${preferencesPython}/bin/python3 -B ${../files/agent-desktop/xfce_preferences.py} "$@"
  '';
in {
  options.loom.agentDesktop.enable = lib.mkEnableOption "upstream Cua access to the agents Xfce/X11 login alongside GNOME";
  config = lib.mkIf cfg.enable {
    # Xfwm 4.20.0 can look up a destroyed window table when Picom owns compositing.
    # Keep the upstream backport local to hosts with the agents desktop enabled.
    nixpkgs.overlays = [
      (_final: prev: {
        xfwm4 = prev.xfwm4.overrideAttrs (old: {
          patches = (old.patches or [ ]) ++ [ ../patches/xfwm4-external-compositor-hash.patch ];
        });
      })
    ];
    assertions = [
      { assertion = config.users.mutableUsers;
        message = "Agents login uses mutable shadow-password ownership; later password changes must survive activation."; }
    ];
    users.users.agents.description = lib.mkForce "LOOM Agents";
    services.xserver.enable = true;
    services.xserver.desktopManager.xfce.enable = true;
    # Preserve GNOME and its default login; the human selects Xfce once.
    services.xserver.desktopManager.xfce.enableWaylandSession = false;
    environment.systemPackages = [
      runtime botRuntime driver pkgs.tigervnc pkgs.xdpyinfo
      xfcePreferences desktopTheme darkTheme compositor darkPalette pkgs.picom pkgs.papirus-icon-theme
      pkgs.xfce4-whiskermenu-plugin pkgs.gnomeExtensions.tiling-shell
    ];
    fonts.packages = [ pkgs.inter pkgs.jetbrains-mono ];
    systemd.user.services.loom-agent-compositor = {
      description = "Agents Xfce compositor with Xfwm recovery";
      unitConfig.ConditionUser = "agents";
      # Only the actual Xfce login starts it; user-manager linger is not a desktop.
      restartIfChanged = false;
      serviceConfig = {
        Type = "exec";
        ExecStart = "${compositor}/bin/loom-agent-compositor run";
        ExecStopPost = "${compositor}/bin/loom-agent-compositor recover";
        # The lingering user manager can retain a previous GNOME display.
        # This owner selects only the validated session binding, never that ambient display.
        UnsetEnvironment = [ "DISPLAY" "WAYLAND_DISPLAY" "XAUTHORITY" "XDG_CURRENT_DESKTOP" "XDG_SESSION_TYPE" "XDG_SESSION_ID" ];
        Restart = "no";
        KillMode = "mixed";
        TimeoutStopSec = 15;
        UMask = "0077";
      };
    };
    environment.etc."xdg/autostart/loom-agent-compositor.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=LOOM Rounded Window Compositor
      Exec=${compositor}/bin/loom-agent-compositor autostart
      OnlyShowIn=XFCE;
      NoDisplay=true
    '';
    environment.etc."xdg/autostart/loom-agent-desktop.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=LOOM Agent Desktop Binding
      Exec=${runtime}/bin/loom-main-desktop session
      OnlyShowIn=XFCE;
      NoDisplay=true
    '';
    environment.etc."xdg/autostart/loom-agent-gnome-preferences.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=LOOM GNOME Preferences
      Exec=${preferences}
      OnlyShowIn=GNOME;
      NoDisplay=true
      X-GNOME-Autostart-enabled=true
    '';
    environment.etc."xdg/autostart/loom-agent-xfce-appearance.desktop".text = ''
      [Desktop Entry]
      Type=Application
      Name=LOOM Xfce Appearance Defaults
      Exec=${xfcePreferences}/bin/loom-agent-xfce-appearance
      OnlyShowIn=XFCE;
      NoDisplay=true
    '';
    systemd.tmpfiles.rules = [ "d /var/lib/loom-agent-desktop 0755 root root - -" ];
  };
}
