# Fixed tools only. No project input or produced bytes are Nix derivation inputs.
{ pkgs }:
let
  tools = [ pkgs.bash pkgs.coreutils pkgs.go pkgs.getent ];
  toolPath = pkgs.lib.makeBinPath tools;
  entry = pkgs.writeText "loom-preparation-entry" ''
    #!${pkgs.bash}/bin/bash
    set -euo pipefail
    export PATH=${toolPath}
    test "$(id -u)" = 1000
    test "$(id -g)" = 1000
    test "$(id -G)" = 1000
    # Fail if nspawn fell back from a private cgroup namespace or mapping.
    test "$(cat /proc/self/cgroup)" = '0::/'
    for map in uid_map gid_map; do
      test "$(wc -l < /proc/self/$map)" -eq 1
      read -r inside outside count < /proc/self/$map
      test "$inside" = 0
      test "$outside" -ge 65536
      test "$((outside % 65536))" = 0
      test "$count" = 65536
    done
    for interface in /sys/class/net/*; do
      test "''${interface##*/}" = lo
    done
    while read -r key value rest; do
      case "$key" in
        CapInh:|CapPrm:|CapEff:|CapBnd:|CapAmb:) test "$value" = 0000000000000000 ;;
        NoNewPrivs:) test "$value" = 1 ;;
      esac
    done < /proc/self/status
    # API filesystems are kernel interfaces, not general writable scratch.
    # Every directory writable by this UID must be an explicit bounded bind.
    device=$(stat -c %d /work)
    for p in /work /home/builder /cache /tmp /var/tmp /run /dev/shm /output; do
      test "$(stat -c %d "$p")" = "$device"
      test -w "$p"
    done
    for p in / /etc /dev /sys /source /producer /nix/store; do
      test ! -w "$p"
    done
    test ! -r /run/host/os-release
    test ! -e /run/current-system
    test ! -e /nix/var/nix/daemon-socket/socket
    # nspawn mounts the private cgroup namespace after outer custom mounts.
    # Only inner root owns its control files; this UID cannot migrate or tune.
    test ! -w /sys/fs/cgroup/cgroup.procs
    test ! -w /sys/fs/cgroup/cgroup.subtree_control
    # mqueue is an API filesystem, not a regular-file scratch mount. Disable
    # queue allocation altogether, including mq_open after producer exec.
    test "$(ulimit -q)" = 0
    test "$(ulimit -Hq)" = 0
    # Readonly roots and inputs must be actual readonly mounts, not chmod claims.
    declare -A readonly_seen=()
    while read -r mid parent dev root mount options rest; do
      case "$mount" in
        /|/source|/producer|/nix/store/*)
          case ",$options," in *,ro,*) readonly_seen["$mount"]=yes ;; *) exit 70 ;; esac ;;
      esac
    done < /proc/self/mountinfo
    test "''${readonly_seen[/]:-}" = yes
    test "''${readonly_seen[/source]:-}" = yes
    test "''${readonly_seen[/producer]:-}" = yes
    # Only stdio (plus this shell's own script FD) exists before exec.
    for fd in /proc/$$/fd/*; do
      case "''${fd##*/}" in 0|1|2|255) ;; *) exit 70 ;; esac
    done
    exec ${pkgs.coreutils}/bin/env -i \
      PATH=${toolPath} LANG=C LC_ALL=C \
      HOME=/home/builder TMPDIR=/tmp XDG_CACHE_HOME=/cache \
      GOROOT=${pkgs.go}/share/go GOCACHE=/cache/go-build GOPATH=/work/go \
      GOPROXY=off GOSUMDB=off GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=0 \
      GOFLAGS=-p=1 GOMAXPROCS=1 \
      LOOM_SOURCE_DIR=/source LOOM_PRODUCER_DIR=/producer LOOM_OUTPUT_DIR=/output \
      "$@"
  '';
  toolClosure = pkgs.closureInfo { rootPaths = tools ++ [ entry ]; };
  root = pkgs.runCommand "loom-preparation-runtime-root" {} ''
    mkdir -p "$out"/{bin,sbin,etc,dev,proc,sys,run,work,source,producer,output,tmp,var/tmp,home/builder,cache,nix/store,usr/bin,usr/sbin,usr/lib,usr/lib64,lib,lib64}
    printf 'ID=loom\nNAME=LOOM\n' > "$out/etc/os-release"
    printf 'root:x:0:0:root:/root:/bin/sh\nbuilder:x:1000:1000:builder:/home/builder:/bin/sh\n' > "$out/etc/passwd"
    printf 'root:x:0:\nbuilder:x:1000:\n' > "$out/etc/group"
    printf 'passwd: files\ngroup: files\nhosts: files\n' > "$out/etc/nsswitch.conf"
    touch "$out/etc/resolv.conf" "$out/etc/machine-id"
    ln -s ${pkgs.bash}/bin/bash "$out/bin/sh"
    # This must be a realized executable, not merely a plausible store path.
    # The pinned glibc bin output deliberately does not contain getent.
    test -f ${pkgs.getent}/bin/getent
    test -x ${pkgs.getent}/bin/getent
    ln -s ${pkgs.getent}/bin/getent "$out/usr/bin/getent"
    test -f "$out/usr/bin/getent"
    test -x "$out/usr/bin/getent"
    cp ${entry} "$out/bin/loom-preparation-entry"
    chmod 0555 "$out/bin/loom-preparation-entry"
    # Bind destinations must already exist in the immutable OS root. Include
    # regular store leaves as files; never expose the host's entire store.
    while IFS= read -r storePath; do
      if test -d "$storePath"; then mkdir -p "$out$storePath"; else touch "$out$storePath"; fi
    done < ${toolClosure}/store-paths
    mkdir -p "$out$out"
  '';
  closure = pkgs.closureInfo { rootPaths = tools ++ [ entry root ]; };
  manifest = pkgs.runCommand "loom-preparation-runtime-manifest" {
    nativeBuildInputs = [ pkgs.python3 ];
    passthru = { inherit root closure entry; };
  } ''
    mkdir -p "$out"
    python3 - ${closure}/store-paths "$out/manifest.json" <<'PY'
    import hashlib, json, sys
    paths = sorted(set(open(sys.argv[1]).read().splitlines()))
    identity = 'sha256:' + hashlib.sha256(('\n'.join(paths)+'\n').encode()).hexdigest()
    with open(sys.argv[2], 'w') as out:
        json.dump(dict(schema='loom.preparation.v1', platform='${pkgs.stdenv.hostPlatform.system}',
            root='${root}', closure=paths, toolchain_identity=identity), out, sort_keys=True)
    PY
  '';
in manifest
