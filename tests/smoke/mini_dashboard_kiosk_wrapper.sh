#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
module="$repo_root/nix/modules/loom-mini-dashboard.nix"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
wrapper="$scratch/loom-mini-dashboard-kiosk"

awk '
  /# BEGIN LOOM MINI DASHBOARD KIOSK WRAPPER/ { capture = 1 }
  capture { sub(/^      /, ""); print }
  /# END LOOM MINI DASHBOARD KIOSK WRAPPER/ { exit }
' "$module" >"$wrapper"

bash -n "$wrapper"

if production_error=$(env -u DISPLAY -u STATE_DIRECTORY -u LOOM_MINI_DASHBOARD_URL bash "$wrapper" 2>&1); then
  printf 'wrapper unexpectedly started without its graphical-session environment\n' >&2
  exit 1
fi
if [[ "$production_error" == *"unbound variable"* ]]; then
  printf 'wrapper accessed an unset graphical-session variable: %s\n' "$production_error" >&2
  exit 1
fi

expect_geometry() {
  name=$1
  want=$2
  fixture=$3
  got=$(printf '%s\n' "$fixture" | bash "$wrapper" --parse-xrandr)
  if [[ "$got" != "$want" ]]; then
    printf '%s: got %q, want %q\n' "$name" "$got" "$want" >&2
    exit 1
  fi
}

expect_rejected() {
  name=$1
  fixture=$2
  if printf '%s\n' "$fixture" | bash "$wrapper" --parse-xrandr >/dev/null 2>&1; then
    printf '%s: unsafe layout was accepted\n' "$name" >&2
    exit 1
  fi
}

present='DP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis)
HDMI-1 connected 1024x600+2560+0 (normal left inverted right x axis y axis)'
reordered='HDMI-1 connected 1024x600+2560+0 (normal left inverted right x axis y axis)
DP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis)'
relocated='DP-1 connected primary 1920x1080+0+0 (normal left inverted right x axis y axis)
HDMI-1 connected 1024x600+1920+120 (normal left inverted right x axis y axis)'
absent='DP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis)
HDMI-1 disconnected (normal left inverted right x axis y axis)'
ambiguous='HDMI-1 connected 1024x600+2560+0 (normal left inverted right x axis y axis)
HDMI-1 connected 1024x600+0+0 (normal left inverted right x axis y axis)'
malformed='DP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis)
HDMI-1 connected primary 1024x600+2560+0 (normal left inverted right x axis y axis)'
wrong_mode='DP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis)
HDMI-1 connected 800x600+2560+0 (normal left inverted right x axis y axis)'

expect_geometry present "2560 0" "$present"
expect_geometry reordered "2560 0" "$reordered"
expect_geometry relocated "1920 120" "$relocated"
expect_rejected absent "$absent"
expect_rejected ambiguous "$ambiguous"
expect_rejected malformed "$malformed"
expect_rejected wrong-mode "$wrong_mode"

fake_bin="$scratch/fake-bin"
runtime_dir="$scratch/runtime"
state_dir="$scratch/state"
capture_dir="$scratch/capture"
mkdir -p "$fake_bin" "$runtime_dir" "$state_dir" "$capture_dir"
touch "$runtime_dir/.mutter-Xwaylandauth.fixture"

cat >"$fake_bin/id" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -u) printf '4242\n' ;;
  -un) printf 'loomadmin\n' ;;
  *) exit 2 ;;
esac
EOF

cat >"$fake_bin/xrandr" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' \
  'DP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis)' \
  'HDMI-1 connected 1024x600+2560+0 (normal left inverted right x axis y axis)'
EOF

cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat >"$fake_bin/gnome-session-inhibit" <<'EOF'
#!/usr/bin/env bash
printf 'XAUTHORITY=%s\n' "${XAUTHORITY:-}" >"$LOOM_KIOSK_TEST_CAPTURE/inhibitor.env"
printf '%s\n' "$@" >"$LOOM_KIOSK_TEST_CAPTURE/inhibitor.args"
while [ "$#" -gt 0 ] && [ "$1" != "chromium" ]; do
  shift
done
[ "$#" -gt 0 ] || exit 2
exec "$@"
EOF

cat >"$fake_bin/chromium" <<'EOF'
#!/usr/bin/env bash
{
  printf 'XAUTHORITY=%s\n' "${XAUTHORITY:-}"
  printf 'XDG_CONFIG_HOME=%s\n' "${XDG_CONFIG_HOME:-}"
  printf 'XDG_CACHE_HOME=%s\n' "${XDG_CACHE_HOME:-}"
  printf 'XDG_DATA_HOME=%s\n' "${XDG_DATA_HOME:-}"
} >"$LOOM_KIOSK_TEST_CAPTURE/browser.env"
printf '%s\n' "$@" >"$LOOM_KIOSK_TEST_CAPTURE/browser.args"
EOF
chmod +x "$fake_bin"/*

PATH="$fake_bin:$PATH" \
DISPLAY=:0 \
STATE_DIRECTORY="$state_dir" \
LOOM_MINI_DASHBOARD_URL=http://127.0.0.1:8090/ \
LOOM_MINI_DASHBOARD_TEST_RUNTIME_DIR="$runtime_dir" \
LOOM_KIOSK_TEST_CAPTURE="$capture_dir" \
  bash "$wrapper" --test-launch

expected_xauthority="XAUTHORITY=$runtime_dir/.mutter-Xwaylandauth.fixture"
grep -Fx -- "$expected_xauthority" "$capture_dir/inhibitor.env" >/dev/null
grep -Fx -- "$expected_xauthority" "$capture_dir/browser.env" >/dev/null
grep -Fx -- "XDG_CONFIG_HOME=$state_dir/config" "$capture_dir/browser.env" >/dev/null
grep -Fx -- "XDG_CACHE_HOME=$state_dir/cache" "$capture_dir/browser.env" >/dev/null
grep -Fx -- "XDG_DATA_HOME=$state_dir/data" "$capture_dir/browser.env" >/dev/null
grep -Fx -- '--inhibit=idle:suspend' "$capture_dir/inhibitor.args" >/dev/null
grep -Fx -- '--ozone-platform=x11' "$capture_dir/browser.args" >/dev/null
grep -Fx -- '--window-position=2560,0' "$capture_dir/browser.args" >/dev/null
grep -Fx -- '--window-size=1024,600' "$capture_dir/browser.args" >/dev/null
grep -Fx -- '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1' "$capture_dir/browser.args" >/dev/null

touch "$runtime_dir/.mutter-Xwaylandauth.ambiguous"
if PATH="$fake_bin:$PATH" \
  DISPLAY=:0 \
  STATE_DIRECTORY="$state_dir" \
  LOOM_MINI_DASHBOARD_URL=http://127.0.0.1:8090/ \
  LOOM_MINI_DASHBOARD_TEST_RUNTIME_DIR="$runtime_dir" \
  LOOM_KIOSK_TEST_CAPTURE="$capture_dir" \
    bash "$wrapper" --test-launch >/dev/null 2>&1; then
  printf 'wrapper launched with ambiguous Xauthority evidence\n' >&2
  exit 1
fi

printf 'mini-dashboard kiosk wrapper fixtures and launch environment passed\n'
