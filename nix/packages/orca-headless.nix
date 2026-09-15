{
  appimageTools,
  buildFHSEnv,
  fetchurl,
  lib,
  patch,
  runCommand,
  writeShellScript,
  agentIdentity ? "morathustra",
  macComputerUseEnabled ? false,
}:

assert builtins.elem agentIdentity [ "morathustra" "mina" ];
let
  identity = agentIdentity;
  pname = "orca";
  version = "1.4.191";

  src = fetchurl {
    url = "https://github.com/stablyai/orca/releases/download/v${version}/orca-linux.AppImage";
    hash = "sha256-GWoQ+dr2eH+iWr63caGoKc39MpWSoadlXKX5dgyZ5kk=";
  };

  unpatchedAppDir = appimageTools.extractType2 {
    inherit pname version src;
  };

  # Backport only the public CLI's folder-kind exposure. Never modify the
  # fetched AppImage, its extraction, native modules, ASAR, or an installation.
  appDir = runCommand "${pname}-${version}-folder-bootstrap" {
    nativeBuildInputs = [ patch ];
  } ''
    cp -a ${unpatchedAppDir}/. "$out/"
    cd "$out"
    cli=resources/app.asar.unpacked/out/cli
    test ! -e "$cli/repo-kind-flag.js"
    sha256sum --check <<'PREIMAGES'
    c88b32651e0026ea083bf50ec2a7825a56cff004f7e5fbe9f5ec345ee20cafe7  resources/app.asar.unpacked/out/cli/handlers/repo.js
    a5ad22e21cedb53f0ac3e05b8465eb6184c3a596a608ec1b251377d1f2c51360  resources/app.asar.unpacked/out/cli/handlers/project.js
    b0857506ba61e8e01ff7ddff5228d8376452246d80b6afddd1aa0961f3516f6e  resources/app.asar.unpacked/out/cli/specs/core.js
    eaa80fba626b92381166032e51c205d89d0ace1323e74ed36d7ca3e88a53ba6c  resources/app.asar.unpacked/out/cli/help.js
    0615cb31f7158bfbe64d17c24247f0df6b8a4c040815deaaffccc9316ae1ef6b  resources/app.asar.unpacked/out/cli/bundled-skill-guides.js
    PREIMAGES
    chmod u+w "$cli" "$cli/handlers" "$cli/specs" \
      "$cli/handlers/repo.js" "$cli/handlers/project.js" "$cli/specs/core.js" \
      "$cli/help.js" "$cli/bundled-skill-guides.js"
    patch --batch --forward --fuzz=0 -p1 --dry-run \
      < ${../patches/orca-folder-bootstrap.patch} > "$TMPDIR/patch-dry-run.log"
    test "$(grep -c '^checking file ' "$TMPDIR/patch-dry-run.log")" -eq 6
    if grep -Ei 'offset|fuzz|FAILED' "$TMPDIR/patch-dry-run.log"; then
      echo "ORCA folder patch dry-run context drift" >&2
      exit 1
    fi
    patch --batch --forward --fuzz=0 -p1 \
      < ${../patches/orca-folder-bootstrap.patch} > "$TMPDIR/patch-apply.log"
    test "$(grep -c '^patching file ' "$TMPDIR/patch-apply.log")" -eq 6
    if grep -Ei 'offset|fuzz|FAILED' "$TMPDIR/patch-apply.log"; then
      echo "ORCA folder patch apply context drift" >&2
      exit 1
    fi
    cmp ${unpatchedAppDir}/AppRun "$out/AppRun"
    cmp ${unpatchedAppDir}/orca-ide "$out/orca-ide"
    cmp ${unpatchedAppDir}/resources/app.asar "$out/resources/app.asar"
  '';

  # Check the wrapper's bootstrap assumptions separately so this correction
  # reuses the accepted appDir rather than copying the full payload again.
  bootstrapContract = runCommand "${pname}-${version}-cli-bootstrap-checked" {} ''
    cd ${appDir}
    sha256sum --check <<'PREIMAGES'
    f48e4f9b653213fcdf77c91176503f090a06a9d57f8350a440106ba79e989f91  AppRun
    8744abd46f19819fc7a91302ee9a122a5a94af7ffff0c605df18a688f0347f83  resources/app.asar.unpacked/out/cli/index.js
    24ce7fa0f72c60ed792bacb009699a8a4a392ad869cd4ed16edcd413d9a95a8e  resources/app.asar.unpacked/out/cli/args.js
    c79a9eb802d7940a0f93214fdd73127180ee4cc8a99a0edb0804eeb1564f0b7a  resources/app.asar.unpacked/out/cli/runtime/launch.js
    PREIMAGES
    touch "$out"
  '';

  # The upstream AppImage CLI dispatcher expects APPDIR in Electron's
  # environment. The extracted AppRun discovers it only as a shell variable,
  # so export the immutable extraction root before entering AppRun.
  appRun = writeShellScript "orca-apprun" ''
    # Pinned bootstrap verified by ${bootstrapContract}.
    export APPDIR=${appDir}

    # 1.4.191 omits these two implemented families from its AppImage command
    # list. Select only after its known global prefix; never scan command or
    # desktop option values for a family name. The bundled CLI still owns all
    # parsing, validation, host selection and RPC.
    orca_skip_value=0
    for orca_arg in "$@"; do
      if [ "$orca_skip_value" -eq 1 ]; then
        orca_skip_value=0
        # parseArgs treats another --flag as a missing value, not as the
        # previous flag's value. Do not skip a desktop flag and misread its
        # following project/skills value as a command.
        case "$orca_arg" in --*) ;; *) continue ;; esac
      fi
      case "$orca_arg" in
        --environment|--pairing-code) orca_skip_value=1 ;;
        --environment=*|--pairing-code=*|--json|--json=*|--help|--help=*) ;;
        project|skills)
          # Match pinned AppRun's library/search environment and Electron's
          # buildElectronRunAsNodeEnv. Confine it to this exec branch so serve
          # and desktop launches retain their original environment. Native
          # runtime launches use the bundled stripElectronRunAsNode helper.
          export PATH="$APPDIR:$APPDIR/usr/sbin''${PATH:+:$PATH}"
          export XDG_DATA_DIRS="$APPDIR/usr/share/''${XDG_DATA_DIRS:+:$XDG_DATA_DIRS}:/usr/share/gnome:/usr/local/share/:/usr/share/"
          export LD_LIBRARY_PATH="$APPDIR/usr/lib''${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
          export GSETTINGS_SCHEMA_DIR="$APPDIR/usr/share/glib-2.0/schemas''${GSETTINGS_SCHEMA_DIR:+:$GSETTINGS_SCHEMA_DIR}"
          export ORCA_NODE_OPTIONS="''${NODE_OPTIONS-}"
          export ORCA_NODE_REPL_EXTERNAL_MODULE="''${NODE_REPL_EXTERNAL_MODULE-}"
          unset NODE_OPTIONS NODE_REPL_EXTERNAL_MODULE
          export ELECTRON_RUN_AS_NODE=1
          exec ${appDir}/orca-ide ${appDir}/resources/app.asar.unpacked/out/cli/index.js "$@"
          ;;
        *) break ;;
      esac
    done
    exec ${appDir}/AppRun "$@"
  '';
in
buildFHSEnv {
  inherit pname version;

  runScript = appRun;

  # buildFHSEnv already exposes host /etc read-only at /.host-etc, but its
  # selected /etc links omit this non-secret binding used by agent protocols.
  extraBwrapArgs = [
    "--symlink /.host-etc/loom-${identity}-accounts.json /etc/loom-${identity}-accounts.json"
  ] ++ lib.optional macComputerUseEnabled
    "--symlink /.host-etc/loom-mac-computer-use /etc/loom-mac-computer-use";

  # The AppImage bundles ORCA and Electron. Supply only its direct ELF
  # dependencies plus the headless startup tools used by this release.
  targetPkgs = pkgs: with pkgs; [
    alsa-lib
    at-spi2-atk
    at-spi2-core
    atk
    cairo
    cups
    dbus
    expat
    glib
    gtk3
    libgbm
    libxkbcommon
    nspr
    nss
    pango
    udev
    util-linux
    which
    zlib
    xorg.libX11
    xorg.libXcomposite
    xorg.libXdamage
    xorg.libXext
    xorg.libXfixes
    xorg.libXrandr
    xorg.libxcb
  ];

  passthru = {
    inherit appDir unpatchedAppDir src appRun bootstrapContract;
  };

  meta = {
    description = "ORCA remote agent runtime";
    homepage = "https://github.com/stablyai/orca";
    license = lib.licenses.mit;
    mainProgram = "orca";
    platforms = [ "x86_64-linux" ];
    sourceProvenance = with lib.sourceTypes; [ binaryNativeCode ];
  };
}
