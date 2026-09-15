#!@bash@/bin/bash -p
# Fixed native dispatch. No credential contents, provisioning or actor rebinding.
# -p ignores BASH_ENV, imported functions and inherited shell tracing options.
set -euo pipefail
umask 077
if [[ '@retired@' == true ]]; then
  printf '%s\n' 'Morathustra entry point is retired; use the reviewed loom-mina tool and operator-migrated MINA profile.' >&2
  exit 64
fi
refuse() {
  printf '%s\n' "@identity@ account tool refused: ${1:-explicit_override_refused}; use the operator handoff." >&2
  exit 64
}

# Discard launcher/ambient overrides before even the fixed custody helpers.
# The native CLI receives only the explicit env -i vector below.
while IFS= read -r key; do
  case "$key" in
    GH_*|GITHUB_*|BASECAMP_*|GIT_*|SSH_*|HTTP_PROXY|HTTPS_PROXY|ALL_PROXY|NO_PROXY|http_proxy|https_proxy|all_proxy|no_proxy|LD_*|DYLD_*|PAGER) unset "$key" ;;
  esac
done < <(compgen -e)

readonly auth_root='@authRoot@'
readonly native='@native@'
readonly tool='@tool@'
readonly owner='@owner@'
uid="$('@coreutils@/bin/id' -u "$owner" 2>/dev/null)" || refuse identity_mismatch
gid="$('@coreutils@/bin/id' -g "$owner" 2>/dev/null)" || refuse identity_mismatch
[[ "$EUID" == "$uid" ]] || refuse identity_mismatch

check_path() {
  local path="$1" kind="$2" meta expected
  [[ ! -L "$path" ]] || refuse custody_invalid
  [[ -e "$path" ]] || refuse auth_missing
  [[ "$('@coreutils@/bin/readlink' -e -- "$path" 2>/dev/null)" == "$path" ]] || refuse custody_invalid
  meta="$('@coreutils@/bin/stat' -c '%u:%g:%a:%h' -- "$path" 2>/dev/null)" || refuse custody_invalid
  if [[ "$kind" == directory ]]; then
    [[ -d "$path" && "${meta%:*}" == "$uid:$gid:700" ]] || refuse custody_invalid
  else
    expected="$uid:$gid:600:1"
    [[ -f "$path" && "$meta" == "$expected" ]] || refuse custody_invalid
  fi
}
check_path "$auth_root" directory
for child in home config cache; do check_path "$auth_root/$child" directory; done
if [[ "$tool" == gh ]]; then
  check_path "$auth_root/gh" directory
  check_path "$auth_root/gh/config.yml" file
  check_path "$auth_root/gh/hosts.yml" file
else
  check_path "$auth_root/config/basecamp" directory
  check_path "$auth_root/config/basecamp/config.json" file
  check_path "$auth_root/config/basecamp/credentials.json" file
  if [[ -e "$auth_root/config/basecamp/.last-run-version" || -L "$auth_root/config/basecamp/.last-run-version" ]]; then
    check_path "$auth_root/config/basecamp/.last-run-version" file
  fi
fi
# Do not recurse into cache: native cache/socket objects are not credentials.

if [[ "$tool" == basecamp ]]; then
  # Keep the literal visible profile contract. No alias, short flag or default.
  [[ $# -ge 3 && "$1" == --profile && "$2" == @identity@ ]] || refuse
  shift 2
fi
args=("$@")
# Command first makes native global-option values unambiguous. The literal
# Basecamp profile prefix was already consumed. Put all other flags after it.
[[ ${#args[@]} -gt 0 ]] || refuse
command_name="${args[0]}"
if [[ "$command_name" == -* ]]; then
  [[ ${#args[@]} == 1 && ( "$command_name" == --help || "$command_name" == --version ) ]] || refuse
fi
api_endpoint_seen=false
for ((i=1; i<${#args[@]}; i++)); do
  arg="${args[i]}"
  case "$arg" in
    --profile|--profile=*|--account|--account=*|--host|--host=*|--config-dir|--config-dir=*|--cache-dir|--cache-dir=*|--token|--token=*|--with-token|--show-token|--base-url|--base-url=*) refuse ;;
    --hostname)
      ((i+=1)); [[ "$tool" == gh && "${args[i]-}" == github.com ]] || refuse ;;
    --hostname=*) [[ "$tool" == gh && "$arg" == --hostname=github.com ]] || refuse ;;
    --repo|-R|--repo=*|-R?*)
      [[ "$tool" == gh ]] || refuse
      case "$arg" in
        --repo|-R) ((i+=1)); repo="${args[i]-}" ;;
        --repo=*) repo="${arg#--repo=}" ;;
        -R*) repo="${arg#-R}" ;;
      esac
      # All owners/organisations on github.com remain available, not just example-operator.
      [[ "$repo" =~ ^(github\.com/)?[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || refuse ;;
    -H|--header|--header=*|-H?*)
      if [[ "$tool" == gh && "$command_name" == api ]]; then
        case "$arg" in
          -H|--header) ((i+=1)); header="${args[i]-}" ;;
          --header=*) header="${arg#--header=}" ;;
          -H*) header="${arg#-H}"; header="${header#=}" ;;
        esac
        # Native gh honors caller Authorization before its stored identity.
        [[ "$header" =~ ^[A-Za-z0-9-]+: && "$header" != *$'\n'* && "$header" != *$'\r'* ]] || refuse
        header_name="${header%%:*}"
        case "${header_name,,}" in authorization|proxy-authorization|host|cookie) refuse ;; esac
      fi ;;
    -X|--method|-F|--field|-f|--raw-field|--input|--jq|-q|--template|-t|--cache|-p|--preview)
      if [[ "$tool" == gh && "$command_name" == api ]]; then ((i+=1)); fi ;;
    -?*)
      # Basecamp's account/profile short flags also work inside pflag clusters.
      if [[ "$tool" == basecamp && "$arg" != --* && "$arg" =~ [aP] ]]; then refuse; fi
      if [[ "$tool" == gh && "$command_name" == api && "$arg" != --* ]]; then
        # Reject ambiguous short clusters; explicit flags or attached values
        # for these pinned native value flags keep endpoint parsing exact.
        case "$arg" in -i|-h|-X?*|-F?*|-f?*|-q?*|-t?*|-p?*) ;; *) refuse ;; esac
      fi ;;
    *)
      if [[ "$tool" == gh && "$command_name" == api && "$api_endpoint_seen" == false ]]; then
        # Native gh accepts absolute endpoints independently of GH_HOST.
        case "$arg" in *://*|//*) refuse ;; esac
        api_endpoint_seen=true
      fi ;;
  esac
done
# Authentication lifecycle and persistent CLI configuration stay operator-owned.
case "$command_name" in
  skill)
    # Pinned Basecamp prints its embedded official skill in machine mode.
    [[ "$tool" == basecamp && ${#args[@]} == 2 && "${args[1]}" == --agent ]] || refuse ;;
  auth|login|logout|config|profile|setup|setup-git|alias|extension|extensions|upgrade|update|install|agent|migrate|tui) refuse ;;
esac

child=(
  'HOME=@authRoot@/home'
  'XDG_CONFIG_HOME=@authRoot@/config'
  'XDG_CACHE_HOME=@authRoot@/cache'
  'PATH=@childPath@'
  'LANG=C.UTF-8'
  # Replace launcher/user CA metadata with Nix's immutable trusted bundle.
  'SSL_CERT_FILE=@cacert@/etc/ssl/certs/ca-bundle.crt'
  'USER=@owner@' 'LOGNAME=@owner@'
  # Native file grants only; do not discover the shared user's session keyring.
  'DBUS_SESSION_BUS_ADDRESS=unix:path=/dev/null'
)
if [[ "$tool" == gh ]]; then
  exec '@coreutils@/bin/env' -i "${child[@]}" \
    'GH_CONFIG_DIR=@authRoot@/gh' 'GH_HOST=github.com' \
    'GH_PROMPT_DISABLED=1' 'GH_NO_UPDATE_NOTIFIER=1' \
    'GH_NO_EXTENSION_UPDATE_NOTIFIER=1' "$native" "${args[@]}"
else
  exec '@coreutils@/bin/env' -i "${child[@]}" \
  'BASECAMP_BASE_URL=https://3.basecampapi.com' 'BASECAMP_ACCOUNT_ID=@basecampAccountID@' \
    'BASECAMP_NO_KEYRING=1' 'BASECAMP_NO_UPDATE_CHECK=1' \
    'BASECAMP_CACHE_DIR=@authRoot@/cache' \
    "$native" --profile @identity@ "${args[@]}"
fi
