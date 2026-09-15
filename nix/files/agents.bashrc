# LOOM ORCA agent shell. Managed by nix/modules/loom-orca.nix.

# The Codex installer uses this account-local directory.
export PATH="$HOME/.local/bin:$PATH"

# Proton Pass sessions use the account-owned filesystem key provider because
# the headless agents account has no persistent desktop keyring. Secret values
# and agent tokens remain outside this managed file.
export PROTON_PASS_KEY_PROVIDER=fs
export PASS_LOG_LEVEL=warn

case $- in
  *i*) ;;
  *) return ;;
esac

shopt -s checkwinsize cmdhist histappend
HISTCONTROL=ignoreboth:erasedups
HISTSIZE=10000
HISTFILESIZE=20000
PROMPT_DIRTRIM=4

# VIRA Carbon terminal palette.
if [[ -t 1 && ${TERM:-dumb} != dumb && -z ${NO_COLOR:-} ]]; then
  VIRA_RESET=$'\e[0m'
  VIRA_TEXT=$'\e[38;2;217;217;217m'
  VIRA_MUTED=$'\e[38;2;86;87;93m'
  VIRA_DIM=$'\e[38;2;69;69;74m'
  VIRA_TEAL=$'\e[38;2;128;203;196m'
  VIRA_CYAN=$'\e[38;2;110;186;215m'
  VIRA_BLUE=$'\e[38;2;106;144;208m'
  VIRA_GREEN=$'\e[38;2;163;198;121m'
  VIRA_YELLOW=$'\e[38;2;213;176;95m'
  VIRA_RED=$'\e[38;2;200;94;96m'
  VIRA_MAGENTA=$'\e[38;2;161;120;196m'
else
  VIRA_RESET=
  VIRA_TEXT=
  VIRA_MUTED=
  VIRA_DIM=
  VIRA_TEAL=
  VIRA_CYAN=
  VIRA_BLUE=
  VIRA_GREEN=
  VIRA_YELLOW=
  VIRA_RED=
  VIRA_MAGENTA=
fi

export CLICOLOR=1
export LS_COLORS='di=38;2;106;144;208:ln=38;2;110;186;215:so=38;2;161;120;196:pi=38;2;213;176;95:ex=38;2;163;198;121:bd=38;2;213;176;95:cd=38;2;213;176;95:su=38;2;200;94;96:sg=38;2;200;94;96:tw=38;2;86;87;93:ow=38;2;86;87;93:st=38;2;86;87;93:*.tar=38;2;200;94;96:*.tgz=38;2;200;94;96:*.gz=38;2;200;94;96:*.zip=38;2;200;94;96:*.json=38;2;213;176;95:*.yaml=38;2;213;176;95:*.yml=38;2;213;176;95:*.md=38;2;110;186;215:*.go=38;2;128;203;196:*.js=38;2;213;176;95:*.ts=38;2;106;144;208:*.tsx=38;2;106;144;208:*.sh=38;2;163;198;121'

export LESS='-FRX'
export LESS_TERMCAP_mb=$'\e[38;2;200;94;96m'
export LESS_TERMCAP_md=$'\e[38;2;128;203;196m'
export LESS_TERMCAP_me=$'\e[0m'
export LESS_TERMCAP_se=$'\e[0m'
export LESS_TERMCAP_so=$'\e[48;2;47;50;55m\e[38;2;217;217;217m'
export LESS_TERMCAP_ue=$'\e[0m'
export LESS_TERMCAP_us=$'\e[38;2;110;186;215m'

alias ls='ls --color=auto --group-directories-first'
alias l='ls -CF'
alias la='ls -A'
alias ll='ls -lah'
alias grep='grep --color=auto'
alias egrep='grep -E --color=auto'
alias fgrep='grep -F --color=auto'
alias diff='diff --color=auto'
alias ip='ip -color=auto'

alias gs='git status --short --branch'
alias gd='git diff'
alias gl='git log --graph --decorate --oneline -20'
alias gll='git log --graph --decorate --oneline --all -30'

__loom_scope_prompt() {
  LOOM_SCOPE_TEXT=
  LOOM_SCOPE_COLOR=$VIRA_MUTED
  case $PWD in
    /srv/loom/box|/srv/loom/box/*)
      LOOM_SCOPE_TEXT='box:rw'
      LOOM_SCOPE_COLOR=$VIRA_GREEN
      ;;
    /srv/loom/agents|/srv/loom/agents/*)
      LOOM_SCOPE_TEXT='agents:rw'
      LOOM_SCOPE_COLOR=$VIRA_TEAL
      ;;
    /srv/loom/storage|/srv/loom/storage/*)
      LOOM_SCOPE_TEXT='storage:ro'
      LOOM_SCOPE_COLOR=$VIRA_YELLOW
      ;;
    /var/lib/loom|/var/lib/loom/*)
      LOOM_SCOPE_TEXT='runtime:ro'
      LOOM_SCOPE_COLOR=$VIRA_YELLOW
      ;;
  esac
}

__loom_git_prompt() {
  LOOM_GIT_TEXT=
  LOOM_GIT_COLOR=$VIRA_GREEN

  local line branch oid ahead behind dirty
  branch=
  oid=
  ahead=0
  behind=0
  dirty=

  while IFS= read -r line; do
    case $line in
      '# branch.head '*) branch=${line#\# branch.head } ;;
      '# branch.oid '*) oid=${line#\# branch.oid } ;;
      '# branch.ab '*)
        read -r _ _ ahead behind <<<"$line"
        ahead=${ahead#+}
        behind=${behind#-}
        ;;
      '1 '*|'2 '*|'u '*) dirty='*' ;;
    esac
  done < <(command git status --porcelain=v2 --branch --untracked-files=no 2>/dev/null)

  [[ -n $branch ]] || return
  if [[ $branch == '(detached)' ]]; then
    branch="@${oid:0:8}"
  fi

  LOOM_GIT_TEXT="git:${branch}"
  (( ahead > 0 )) && LOOM_GIT_TEXT+=" +${ahead}"
  (( behind > 0 )) && LOOM_GIT_TEXT+=" -${behind}"
  if [[ -n $dirty ]]; then
    LOOM_GIT_TEXT+=$dirty
    LOOM_GIT_COLOR=$VIRA_YELLOW
  fi
}

__loom_prompt_command() {
  local exit_status=$?
  local title_path=${PWD/#$HOME/~}
  title_path=${title_path//$'\e'/}
  title_path=${title_path//$'\a'/}

  __loom_scope_prompt
  __loom_git_prompt

  PS1="\[${VIRA_TEAL}\]\u\[${VIRA_MUTED}\]@\[${VIRA_BLUE}\]\h\[${VIRA_RESET}\]"
  if [[ -n $LOOM_SCOPE_TEXT ]]; then
    PS1+="  \[${LOOM_SCOPE_COLOR}\][${LOOM_SCOPE_TEXT}]\[${VIRA_RESET}\]"
  fi
  PS1+="  \[${VIRA_CYAN}\]\w\[${VIRA_RESET}\]"
  if [[ -n $LOOM_GIT_TEXT ]]; then
    PS1+="  \[${LOOM_GIT_COLOR}\]${LOOM_GIT_TEXT}\[${VIRA_RESET}\]"
  fi
  if (( exit_status != 0 )); then
    PS1+="  \[${VIRA_RED}\]exit:${exit_status}\[${VIRA_RESET}\]"
  fi
  PS1+=$'\n'
  PS1+="\[${VIRA_DIM}\]└─\[${VIRA_TEAL}\]❯\[${VIRA_RESET}\] "

  printf '\033]0;%s@%s:%s\007' "$USER" "${HOSTNAME%%.*}" "$title_path"
  return "$exit_status"
}

__loom_install_prompt_command() {
  local declaration part
  declaration=$(declare -p PROMPT_COMMAND 2>/dev/null || true)
  if [[ $declaration == 'declare -a '* ]]; then
    for part in "${PROMPT_COMMAND[@]}"; do
      [[ $part == __loom_prompt_command ]] && return
    done
    PROMPT_COMMAND=(__loom_prompt_command "${PROMPT_COMMAND[@]}")
    return
  fi

  case ";${PROMPT_COMMAND:-};" in
    *';__loom_prompt_command;'*) return ;;
  esac
  PROMPT_COMMAND="__loom_prompt_command${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
}
__loom_install_prompt_command
unset -f __loom_install_prompt_command

if [[ -z ${LOOM_SHELL_WELCOMED:-} ]]; then
  export LOOM_SHELL_WELCOMED=1
  printf '%sLOOM MAIN%s  %sORCA agent workspace%s\n' \
    "$VIRA_TEAL" "$VIRA_RESET" "$VIRA_MUTED" "$VIRA_RESET"
fi

# Optional account-local additions survive without changing the managed theme.
if [[ -f $HOME/.bashrc.local ]]; then
  source "$HOME/.bashrc.local"
fi
