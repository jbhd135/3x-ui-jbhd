#!/usr/bin/env bash

set -euo pipefail

GITHUB_REPO="${GITHUB_REPO:-jbhd135/3x-ui-jbhd}"
INSTALL_VERSION="${INSTALL_VERSION:-latest}"
ASSUME_YES="false"
SKIP_START="false"
NO_CONFIG_PROMPT="false"
FORCE_CONFIG="false"
DISABLE_IPV6="${XUI_DISABLE_IPV6:-true}"
PANEL_USERNAME="${PANEL_USERNAME:-}"
PANEL_PASSWORD="${PANEL_PASSWORD:-}"
PANEL_PORT="${PANEL_PORT:-}"
PANEL_WEB_BASE_PATH="${PANEL_WEB_BASE_PATH:-${PANEL_WEB_BASEPATH:-}}"
PANEL_PUBLIC_HOST="${PANEL_PUBLIC_HOST:-}"
CONFIGURE_PANEL="false"
FULL_PANEL_CONFIG="false"

XUI_MAIN_FOLDER="${XUI_MAIN_FOLDER:-/usr/local/x-ui}"
XUI_SERVICE="${XUI_SERVICE:-/etc/systemd/system}"
XUI_DB_FOLDER="${XUI_DB_FOLDER:-/etc/x-ui}"
XUI_LOG_FOLDER="${XUI_LOG_FOLDER:-/var/log/x-ui}"
XUI_ENV_FILE="${XUI_ENV_FILE:-/etc/default/x-ui}"
XUI_CLI_BIN="${XUI_CLI_BIN:-/usr/bin/x-ui}"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/install-selfhosted.sh [--version v1.2.3]

Optional:
  --repo            GitHub repository. Default: jbhd135/3x-ui-jbhd
  --version         Release tag to install. Default: latest
  --username        Panel login username. Prompts when omitted on fresh install
  --password        Panel login password. Prompts when omitted on fresh install
  --port            Panel port. Prompts when omitted on fresh install
  --web-base-path   Panel base path, for example /jbhd/. Prompts when omitted on fresh install
  --public-host     Hostname or IP to show in the final browser login URL
  --configure       Configure panel settings even when an existing x-ui.db is present
  --no-config-prompt
                    Do not ask panel setting questions; use provided values or secure defaults
  --yes             Skip installation confirmation prompt
  --skip-start      Install files but do not start x-ui
  --enable-ipv6     Explicitly keep IPv6 enabled. IPv6 is disabled by default
  --help            Show this help text

Environment variables:
  GITHUB_REPO       Same as --repo
  INSTALL_VERSION   Same as --version
  PANEL_USERNAME    Same as --username
  PANEL_PASSWORD    Same as --password
  PANEL_PORT        Same as --port
  PANEL_WEB_BASE_PATH
                    Same as --web-base-path
  PANEL_PUBLIC_HOST Hostname or IP to show in the final browser login URL
  XUI_DISABLE_IPV6  Disable IPv6 when true (default: true)
EOF
}

log() {
  printf '[%s] %s\n' "$(date '+%F %T')" "$*"
}

die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Missing required command: $1"
}

arch() {
  case "$(uname -m)" in
    x86_64|x64|amd64) echo 'amd64' ;;
    i*86|x86) echo '386' ;;
    armv8*|armv8|arm64|aarch64) echo 'arm64' ;;
    armv7*|armv7|arm) echo 'armv7' ;;
    armv6*|armv6) echo 'armv6' ;;
    armv5*|armv5) echo 'armv5' ;;
    s390x) echo 's390x' ;;
    *) die "Unsupported CPU architecture: $(uname -m)" ;;
  esac
}

detect_release() {
  if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    echo "${ID:-unknown}"
    return 0
  fi
  if [[ -f /usr/lib/os-release ]]; then
    . /usr/lib/os-release
    echo "${ID:-unknown}"
    return 0
  fi
  echo "unknown"
}

install_base() {
  local release="$1"
  case "$release" in
    ubuntu|debian|armbian)
      apt-get update
      apt-get install -y -q curl tar ca-certificates openssl
      ;;
    fedora|amzn|virtuozzo|rhel|almalinux|rocky|ol)
      dnf -y update
      dnf install -y -q curl tar ca-certificates openssl
      ;;
    centos)
      if [[ "${VERSION_ID:-}" =~ ^7 ]]; then
        yum -y update
        yum install -y curl tar ca-certificates openssl
      else
        dnf -y update
        dnf install -y -q curl tar ca-certificates openssl
      fi
      ;;
    arch|manjaro|parch)
      pacman -Syu --noconfirm curl tar ca-certificates openssl
      ;;
    opensuse-tumbleweed|opensuse-leap)
      zypper refresh
      zypper -q install -y curl tar ca-certificates openssl
      ;;
    alpine)
      apk update
      apk add curl tar ca-certificates openssl
      ;;
    *)
      apt-get update
      apt-get install -y -q curl tar ca-certificates openssl
      ;;
  esac
}

configure_ipv4_only() {
  local config_file="/etc/sysctl.d/99-xui-ipv4-only.conf"
  local global_ipv6_count="0"

  if [[ "$DISABLE_IPV6" != "true" ]]; then
    log "IPv6 remains enabled by explicit request"
    return 0
  fi

  require_cmd sysctl
  install -d -m 0755 /etc/sysctl.d
  cat >"$config_file" <<'EOF'
# Managed by the jbhd 3x-ui installer. Keep panel and proxy egress on IPv4.
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
EOF
  chmod 0644 "$config_file"
  sysctl -q -p "$config_file"

  [[ "$(sysctl -n net.ipv6.conf.all.disable_ipv6)" == "1" ]] \
    || die "Failed to disable IPv6"
  [[ "$(sysctl -n net.ipv6.conf.default.disable_ipv6)" == "1" ]] \
    || die "Failed to disable IPv6 for new interfaces"

  if command -v ip >/dev/null 2>&1; then
    global_ipv6_count="$(ip -6 -o addr show scope global | wc -l | tr -d '[:space:]')"
    [[ "$global_ipv6_count" == "0" ]] \
      || die "Global IPv6 addresses remain after applying IPv4-only mode"
  fi

  log "IPv4-only mode enabled and verified"
}

resolve_tag() {
  local repo="$1"
  local requested="$2"
  local tag

  if [[ "$requested" != "latest" ]]; then
    printf '%s\n' "$requested"
    return 0
  fi

  tag="$(
    curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=20" \
      | grep -m1 '"tag_name":' \
      | sed -E 's/.*"([^"]+)".*/\1/'
  )"

  [[ -n "$tag" ]] || die "Failed to resolve latest release tag from GitHub for $repo"
  printf '%s\n' "$tag"
}

is_port_in_use() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltn 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {exit 0} END {exit 1}'
    return
  fi
  if command -v netstat >/dev/null 2>&1; then
    netstat -lnt 2>/dev/null | awk -v p=":${port} " '$4 ~ p {exit 0} END {exit 1}'
    return
  fi
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1 && return 0
  fi
  return 1
}

random_string() {
  local length="$1"
  openssl rand -base64 $((length * 2)) | tr -dc 'a-zA-Z0-9' | head -c "$length"
}

pick_random_port() {
  local port
  while :; do
    if command -v shuf >/dev/null 2>&1; then
      port="$(shuf -i 10240-62000 -n 1)"
    else
      port="$((10240 + RANDOM % 51761))"
    fi
    if ! is_port_in_use "$port"; then
      printf '%s\n' "$port"
      return 0
    fi
  done
}

validate_port() {
  local port="$1"
  [[ "$port" =~ ^[0-9]+$ ]] || die "Invalid panel port: $port"
  ((port >= 1 && port <= 65535)) || die "Panel port must be between 1 and 65535"
}

normalize_base_path() {
  local base_path="$1"
  [[ -n "$base_path" ]] || die "Panel base path can not be empty"
  [[ "$base_path" == /* ]] || base_path="/$base_path"
  [[ "$base_path" == */ ]] || base_path="$base_path/"
  printf '%s\n' "$base_path"
}

prompt_value() {
  local label="$1"
  local default_value="$2"
  local value
  printf '%s [%s]: ' "$label" "$default_value" >&2
  read -r value
  printf '%s\n' "${value:-$default_value}"
}

prompt_password() {
  local label="$1"
  local generated_value="$2"
  local value
  printf '%s [press Enter to auto-generate]: ' "$label" >&2
  read -rs value
  printf '\n' >&2
  printf '%s\n' "${value:-$generated_value}"
}

prompt_yes_no() {
  local label="$1"
  local default_answer="$2"
  local suffix answer

  if [[ "$default_answer" == "yes" ]]; then
    suffix="[Y/n]"
  else
    suffix="[y/N]"
  fi

  printf '%s %s: ' "$label" "$suffix" >&2
  read -r answer
  answer="${answer:-$default_answer}"
  [[ "$answer" =~ ^[Yy] ]]
}

prepare_panel_config() {
  local db_existed="$1"
  local default_user default_pass default_port default_path

  if [[ "$db_existed" == "true" && "$FORCE_CONFIG" != "true" ]]; then
    if [[ -z "$PANEL_USERNAME" && -z "$PANEL_PASSWORD" && -z "$PANEL_PORT" && -z "$PANEL_WEB_BASE_PATH" ]]; then
      if [[ "$NO_CONFIG_PROMPT" != "true" && -t 0 ]]; then
        log "Existing x-ui.db detected."
        if prompt_yes_no "Reconfigure panel username, password, port and base path?" "no"; then
          FORCE_CONFIG="true"
        else
          CONFIGURE_PANEL="false"
          return 0
        fi
      else
        CONFIGURE_PANEL="false"
        return 0
      fi
    fi
  fi

  if [[ "$db_existed" == "true" && "$FORCE_CONFIG" != "true" ]]; then
    if [[ -z "$PANEL_USERNAME" && -z "$PANEL_PASSWORD" && -z "$PANEL_PORT" && -z "$PANEL_WEB_BASE_PATH" ]]; then
      CONFIGURE_PANEL="false"
      return 0
    fi
    CONFIGURE_PANEL="true"
    FULL_PANEL_CONFIG="false"
    if [[ -n "$PANEL_USERNAME" || -n "$PANEL_PASSWORD" ]]; then
      [[ -n "$PANEL_USERNAME" && -n "$PANEL_PASSWORD" ]] || die "--username and --password must be used together for an existing x-ui.db"
    fi
    [[ -z "$PANEL_PORT" ]] || validate_port "$PANEL_PORT"
    [[ -z "$PANEL_WEB_BASE_PATH" ]] || PANEL_WEB_BASE_PATH="$(normalize_base_path "$PANEL_WEB_BASE_PATH")"
    return 0
  fi

  CONFIGURE_PANEL="true"
  FULL_PANEL_CONFIG="true"
  default_user="$(random_string 10)"
  default_pass="$(random_string 14)"
  default_port="$(pick_random_port)"
  default_path="/jbhd/"

  if [[ "$NO_CONFIG_PROMPT" != "true" && -t 0 ]]; then
    log "Panel configuration"
    PANEL_USERNAME="${PANEL_USERNAME:-$(prompt_value 'Panel username' "$default_user")}"
    PANEL_PASSWORD="${PANEL_PASSWORD:-$(prompt_password 'Panel password' "$default_pass")}"
    PANEL_PORT="${PANEL_PORT:-$(prompt_value 'Panel port' "$default_port")}"
    PANEL_WEB_BASE_PATH="${PANEL_WEB_BASE_PATH:-$(prompt_value 'Panel base path' "$default_path")}"
  else
    PANEL_USERNAME="${PANEL_USERNAME:-$default_user}"
    PANEL_PASSWORD="${PANEL_PASSWORD:-$default_pass}"
    PANEL_PORT="${PANEL_PORT:-$default_port}"
    PANEL_WEB_BASE_PATH="${PANEL_WEB_BASE_PATH:-$default_path}"
  fi

  validate_port "$PANEL_PORT"
  PANEL_WEB_BASE_PATH="$(normalize_base_path "$PANEL_WEB_BASE_PATH")"
}

apply_panel_config() {
  local args=()

  if [[ "$FULL_PANEL_CONFIG" == "true" || -n "$PANEL_USERNAME" || -n "$PANEL_PASSWORD" ]]; then
    args+=(-username "$PANEL_USERNAME" -password "$PANEL_PASSWORD")
  fi
  if [[ "$FULL_PANEL_CONFIG" == "true" || -n "$PANEL_PORT" ]]; then
    args+=(-port "$PANEL_PORT")
  fi
  if [[ "$FULL_PANEL_CONFIG" == "true" || -n "$PANEL_WEB_BASE_PATH" ]]; then
    args+=(-webBasePath "$PANEL_WEB_BASE_PATH")
  fi

  ((${#args[@]} > 0)) || return 0
  log "Applying panel settings"
  "$XUI_MAIN_FOLDER/x-ui" setting "${args[@]}" >/dev/null
}

detect_public_host() {
  local host

  if [[ -n "$PANEL_PUBLIC_HOST" ]]; then
    printf '%s\n' "$PANEL_PUBLIC_HOST"
    return 0
  fi

  host="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  if [[ -n "$host" ]]; then
    printf '%s\n' "$host"
    return 0
  fi

  if command -v hostname >/dev/null 2>&1; then
    host="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
    if [[ -n "$host" ]]; then
      printf '%s\n' "$host"
      return 0
    fi
  fi

  printf '%s\n' "YOUR_SERVER_IP"
}

extract_setting_value() {
  local output="$1"
  local key="$2"

  printf '%s\n' "$output" \
    | sed -nE "s/^[[:space:]]*${key}:[[:space:]]*(.*)$/\\1/p" \
    | tail -n 1
}

print_install_summary() {
  local settings_output protocol public_host summary_port summary_path summary_username summary_password login_url

  settings_output="$("$XUI_MAIN_FOLDER/x-ui" setting -show 2>/dev/null || true)"
  protocol="http"
  if printf '%s\n' "$settings_output" | grep -q "Panel is secure with SSL"; then
    protocol="https"
  fi

  public_host="$(detect_public_host)"
  summary_port="${PANEL_PORT:-$(extract_setting_value "$settings_output" "port")}"
  summary_path="${PANEL_WEB_BASE_PATH:-$(extract_setting_value "$settings_output" "webBasePath")}"
  summary_path="${summary_path:-/}"
  summary_path="$(normalize_base_path "$summary_path")"

  summary_username="${PANEL_USERNAME:-Preserved in existing x-ui.db}"
  summary_password="${PANEL_PASSWORD:-Preserved in existing x-ui.db; plaintext password is not recoverable}"

  if [[ -n "$summary_port" ]]; then
    login_url="${protocol}://${public_host}:${summary_port}${summary_path}"
  else
    login_url="${protocol}://${public_host}${summary_path}"
  fi

  printf '\n'
  log "Login summary:"
  log "  Browser URL: $login_url"
  log "  Username: $summary_username"
  log "  Password: $summary_password"
  printf '\n'
}

generate_service_file() {
  cat >"$1" <<EOF
[Unit]
Description=x-ui Service
After=network.target
Wants=network.target

[Service]
EnvironmentFile=-$XUI_ENV_FILE
Environment="XRAY_VMESS_AEAD_FORCED=false"
Type=simple
WorkingDirectory=$XUI_MAIN_FOLDER/
ExecStartPre=/bin/sh -c 'test ! -r /etc/sysctl.d/99-xui-ipv4-only.conf || sysctl -q -p /etc/sysctl.d/99-xui-ipv4-only.conf'
ExecStart=$XUI_MAIN_FOLDER/x-ui
ExecReload=kill -USR1 \$MAINPID
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
}

generate_cli_script() {
  cat >"$1" <<EOF
#!/usr/bin/env bash
set -euo pipefail

cmd="\${1:-}"
case "\$cmd" in
  start)
    exec systemctl start x-ui
    ;;
  stop)
    exec systemctl stop x-ui
    ;;
  restart)
    exec systemctl restart x-ui
    ;;
  status)
    exec systemctl status x-ui --no-pager
    ;;
  enable)
    exec systemctl enable x-ui
    ;;
  disable)
    exec systemctl disable x-ui
    ;;
  log)
    exec journalctl -u x-ui -e --no-pager
    ;;
  setting|cert|migrate)
    shift
    exec "$XUI_MAIN_FOLDER/x-ui" "\$cmd" "\$@"
    ;;
  settings)
    shift
    exec "$XUI_MAIN_FOLDER/x-ui" setting "\$@"
    ;;
  "")
    cat <<'USAGE'
x-ui helper
Usage:
  x-ui start|stop|restart|status|enable|disable|log
  x-ui setting [flags]
  x-ui cert [flags]
  x-ui migrate
USAGE
    ;;
  *)
    exec "$XUI_MAIN_FOLDER/x-ui" "\$@"
    ;;
esac
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)
      GITHUB_REPO="${2:-}"; shift 2 ;;
    --version)
      INSTALL_VERSION="${2:-}"; shift 2 ;;
    --username)
      PANEL_USERNAME="${2:-}"; shift 2 ;;
    --password)
      PANEL_PASSWORD="${2:-}"; shift 2 ;;
    --port)
      PANEL_PORT="${2:-}"; shift 2 ;;
    --web-base-path|--webBasePath)
      PANEL_WEB_BASE_PATH="${2:-}"; shift 2 ;;
    --public-host)
      PANEL_PUBLIC_HOST="${2:-}"; shift 2 ;;
    --configure)
      FORCE_CONFIG="true"; shift ;;
    --no-config-prompt)
      NO_CONFIG_PROMPT="true"; shift ;;
    --yes)
      ASSUME_YES="true"; shift ;;
    --skip-start)
      SKIP_START="true"; shift ;;
    --enable-ipv6)
      DISABLE_IPV6="false"; shift ;;
    --help|-h)
      usage; exit 0 ;;
    *)
      die "Unknown argument: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "Please run this script as root"
[[ "$GITHUB_REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "Invalid GitHub repository: $GITHUB_REPO"

case "${DISABLE_IPV6,,}" in
  true|1|yes) DISABLE_IPV6="true" ;;
  false|0|no) DISABLE_IPV6="false" ;;
  *) die "XUI_DISABLE_IPV6 must be true or false" ;;
esac

require_cmd curl
require_cmd tar
require_cmd mktemp
require_cmd systemctl

RELEASE="$(detect_release)"
ARCH="$(arch)"
TAG="$(resolve_tag "$GITHUB_REPO" "$INSTALL_VERSION")"
DOWNLOAD_URL="https://github.com/$GITHUB_REPO/releases/download/$TAG/x-ui-linux-$ARCH.tar.gz"

log "Repository: $GITHUB_REPO"
log "Release tag: $TAG"
log "Architecture: $ARCH"
log "Download URL: $DOWNLOAD_URL"

if [[ "$ASSUME_YES" != "true" ]]; then
  printf 'Proceed with installation? [y/N]: '
  read -r answer
  [[ "$answer" =~ ^[Yy]$ ]] || die "Aborted by user"
fi

install_base "$RELEASE"

WORK_DIR="$(mktemp -d)"
ARCHIVE_PATH="$WORK_DIR/x-ui-linux-$ARCH.tar.gz"
SERVICE_FILE="$WORK_DIR/x-ui.service"
CLI_FILE="$WORK_DIR/x-ui.sh"
BACKUP_DIR="/root/xui-install-backup-$(date +%F-%H%M%S)"
DB_EXISTED="false"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

if [[ -f "$XUI_DB_FOLDER/x-ui.db" ]]; then
  DB_EXISTED="true"
fi

prepare_panel_config "$DB_EXISTED"

log "Downloading release archive"
curl -fL "$DOWNLOAD_URL" -o "$ARCHIVE_PATH"

log "Extracting archive"
mkdir -p "$WORK_DIR/extract"
tar -xzf "$ARCHIVE_PATH" -C "$WORK_DIR/extract"
[[ -d "$WORK_DIR/extract/x-ui" ]] || die "Invalid release archive: x-ui directory missing"
[[ -f "$WORK_DIR/extract/x-ui/x-ui" ]] || die "Invalid release archive: x-ui binary missing"

log "Backing up current installation to $BACKUP_DIR"
mkdir -p "$BACKUP_DIR"
if [[ -d "$XUI_MAIN_FOLDER" ]]; then
  cp -a "$XUI_MAIN_FOLDER" "$BACKUP_DIR/" >/dev/null 2>&1 || true
fi
if [[ -f "$XUI_DB_FOLDER/x-ui.db" ]]; then
  cp -a "$XUI_DB_FOLDER/x-ui.db" "$BACKUP_DIR/" >/dev/null 2>&1 || true
fi
if [[ -f "$XUI_SERVICE/x-ui.service" ]]; then
  cp -a "$XUI_SERVICE/x-ui.service" "$BACKUP_DIR/" >/dev/null 2>&1 || true
fi
if [[ -f "$XUI_ENV_FILE" ]]; then
  cp -a "$XUI_ENV_FILE" "$BACKUP_DIR/" >/dev/null 2>&1 || true
fi
if [[ -f /etc/sysctl.d/99-xui-ipv4-only.conf ]]; then
  cp -a /etc/sysctl.d/99-xui-ipv4-only.conf "$BACKUP_DIR/" >/dev/null 2>&1 || true
fi

log "Stopping old x-ui service"
systemctl stop x-ui >/dev/null 2>&1 || true

configure_ipv4_only

log "Installing files"
rm -rf "$XUI_MAIN_FOLDER"
mkdir -p "$(dirname "$XUI_MAIN_FOLDER")"
cp -a "$WORK_DIR/extract/x-ui" "$XUI_MAIN_FOLDER"
chmod +x "$XUI_MAIN_FOLDER/x-ui"
mkdir -p "$XUI_DB_FOLDER" "$XUI_LOG_FOLDER"

generate_service_file "$SERVICE_FILE"
generate_cli_script "$CLI_FILE"
install -m 0644 "$SERVICE_FILE" "$XUI_SERVICE/x-ui.service"
install -m 0755 "$CLI_FILE" "$XUI_CLI_BIN"
systemctl daemon-reload
systemctl enable x-ui >/dev/null 2>&1 || true

if [[ "$SKIP_START" == "true" ]]; then
  log "Installation finished. x-ui service was not started because --skip-start was used."
  if [[ "$CONFIGURE_PANEL" == "true" ]]; then
    log "Panel settings were collected but not applied because --skip-start was used."
  fi
  exit 0
fi

log "Starting x-ui"
systemctl start x-ui
sleep 3
systemctl is-active x-ui >/dev/null

if [[ "$CONFIGURE_PANEL" == "true" ]]; then
  apply_panel_config
  systemctl restart x-ui
  sleep 2
  if [[ "$FULL_PANEL_CONFIG" == "true" ]]; then
    log "Panel settings:"
    log "  username: $PANEL_USERNAME"
    log "  password: $PANEL_PASSWORD"
    log "  port: $PANEL_PORT"
    log "  webBasePath: $PANEL_WEB_BASE_PATH"
    log "These settings will be replaced if you later upload your own x-ui.db."
  else
    log "Updated panel settings:"
    [[ -z "$PANEL_USERNAME" ]] || log "  username: $PANEL_USERNAME"
    [[ -z "$PANEL_PASSWORD" ]] || log "  password: $PANEL_PASSWORD"
    [[ -z "$PANEL_PORT" ]] || log "  port: $PANEL_PORT"
    [[ -z "$PANEL_WEB_BASE_PATH" ]] || log "  webBasePath: $PANEL_WEB_BASE_PATH"
  fi
else
  log "Existing x-ui.db detected, panel settings were preserved."
fi

log "Installation completed successfully"
print_install_summary
log "Backup directory: $BACKUP_DIR"
