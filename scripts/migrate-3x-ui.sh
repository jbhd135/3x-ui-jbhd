#!/usr/bin/env bash

set -euo pipefail

SOURCE_HOST=""
SOURCE_PORT="22"
SOURCE_USER="root"
SOURCE_PASS=""

TARGET_HOST=""
TARGET_PORT="22"
TARGET_USER="root"
TARGET_PASS=""

SOURCE_XUI_DIR="/usr/local/x-ui"
SOURCE_XUI_PATH="/usr/local/x-ui/x-ui"
SOURCE_BIN_DIR="/usr/local/x-ui/bin"
SOURCE_DB_PATH="/etc/x-ui/x-ui.db"
SOURCE_ENV_PATH="/etc/default/x-ui"

TARGET_XUI_DIR="/usr/local/x-ui"
TARGET_XUI_PATH="/usr/local/x-ui/x-ui"
TARGET_BIN_DIR="/usr/local/x-ui/bin"
TARGET_DB_DIR="/etc/x-ui"
TARGET_DB_PATH="/etc/x-ui/x-ui.db"
TARGET_ENV_PATH="/etc/default/x-ui"
TARGET_SERVICE_PATH="/etc/systemd/system/x-ui.service"
TARGET_USR_BIN_PATH="/usr/bin/x-ui"
TARGET_LOG_DIR="/var/log/x-ui"

COPY_XRAY="false"
COPY_DB_MODE="auto"
AUTO_ROLLBACK="true"
ASSUME_YES="false"
DRY_RUN="false"

WORK_DIR=""
SOURCE_KNOWN_HOSTS=""
TARGET_KNOWN_HOSTS=""
LOCAL_XUI=""
LOCAL_DB_SNAPSHOT=""
LOCAL_ENV=""
LOCAL_BIN_DIR=""
LOCAL_CERT_DIR=""
LOCAL_GENERATED_SERVICE=""
LOCAL_GENERATED_XUI_MENU=""
REMOTE_SOURCE_DB_SNAPSHOT=""
REMOTE_TARGET_TMP_DIR=""
BACKUP_DIR=""

SOURCE_FACTS=""
TARGET_FACTS=""
SOURCE_ARCH=""
TARGET_ARCH=""
SOURCE_SHA=""
SOURCE_RELEASE=""
SOURCE_DB_EXISTS=""
SOURCE_ENV_EXISTS=""
SOURCE_BIN_EXISTS=""
SOURCE_XUI_EXISTS=""
TARGET_RELEASE=""
TARGET_XUI_EXISTS=""
TARGET_DB_EXISTS=""
TARGET_ENV_EXISTS=""
TARGET_USR_BIN_EXISTS=""
TARGET_HAS_SYSTEMD=""
TARGET_SERVICE_ACTIVE=""
TARGET_BOOTSTRAP_NEEDED="false"
EFFECTIVE_COPY_DB="false"
EFFECTIVE_COPY_BIN="false"
EFFECTIVE_COPY_ENV="false"
EFFECTIVE_COPY_CERTS="false"

declare -a SOURCE_BIN_FILES=()
declare -a SOURCE_CERT_KEYS=()
declare -a SOURCE_CERT_PATHS=()
declare -a LOCAL_CERT_FILES=()

usage() {
  cat <<'EOF'
Usage:
  ./scripts/migrate-3x-ui.sh \
    --source-host 159.65.108.187 \
    --source-pass 'SOURCE_PASSWORD' \
    --target-host g.jbhd145.top \
    --target-pass 'TARGET_PASSWORD' \
    --yes

Required:
  --source-host        Source server hostname or IP
  --source-pass        Source server SSH password
  --target-host        Target server hostname or IP
  --target-pass        Target server SSH password

Optional:
  --source-port        Source SSH port, default: 22
  --source-user        Source SSH user, default: root
  --target-port        Target SSH port, default: 22
  --target-user        Target SSH user, default: root
  --copy-xray          Force sync source xray binary and .dat files
  --copy-db            Force sync source x-ui.db to target
  --keep-target-db     Keep target x-ui.db even if source database exists
  --no-rollback        Do not restore old files on failure
  --dry-run            Print the migration plan only
  --yes                Skip the confirmation prompt
  --help               Show this help text

Notes:
  - If the target server has no x-ui installed, this script will bootstrap it
    automatically and migrate the source database.
  - For fresh targets, the script also syncs xray core files, panel CLI helper,
    and systemd service file.
  - Existing targets keep their own database by default unless --copy-db is set.
  - The script is self-contained and can be used through curl | bash.
  - The local machine needs sshpass installed.
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

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

fact_value() {
  local key="$1"
  local data="$2"
  printf '%s\n' "$data" | awk -F= -v k="$key" '$1==k { sub(/^[^=]*=/, "", $0); print; exit }'
}

ssh_common_args() {
  printf '%s\n' \
    "-o" "StrictHostKeyChecking=no" \
    "-o" "UserKnownHostsFile=$1"
}

source_ssh() {
  SSHPASS="$SOURCE_PASS" sshpass -e ssh \
    -p "$SOURCE_PORT" \
    $(ssh_common_args "$SOURCE_KNOWN_HOSTS") \
    "$SOURCE_USER@$SOURCE_HOST" "$@"
}

target_ssh() {
  SSHPASS="$TARGET_PASS" sshpass -e ssh \
    -p "$TARGET_PORT" \
    $(ssh_common_args "$TARGET_KNOWN_HOSTS") \
    "$TARGET_USER@$TARGET_HOST" "$@"
}

source_scp_from() {
  local remote_path="$1"
  local local_path="$2"
  SSHPASS="$SOURCE_PASS" sshpass -e scp \
    -P "$SOURCE_PORT" \
    $(ssh_common_args "$SOURCE_KNOWN_HOSTS") \
    "$SOURCE_USER@$SOURCE_HOST:$remote_path" "$local_path" >/dev/null
}

target_scp_to() {
  local local_path="$1"
  local remote_path="$2"
  SSHPASS="$TARGET_PASS" sshpass -e scp \
    -P "$TARGET_PORT" \
    $(ssh_common_args "$TARGET_KNOWN_HOSTS") \
    "$local_path" "$TARGET_USER@$TARGET_HOST:$remote_path" >/dev/null
}

generate_service_file() {
  cat >"$LOCAL_GENERATED_SERVICE" <<EOF
[Unit]
Description=x-ui Service
After=network.target
Wants=network.target

[Service]
EnvironmentFile=-$TARGET_ENV_PATH
Environment="XRAY_VMESS_AEAD_FORCED=false"
Type=simple
WorkingDirectory=$TARGET_XUI_DIR/
ExecStart=$TARGET_XUI_PATH
ExecReload=kill -USR1 \$MAINPID
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
}

generate_xui_cli_script() {
  cat >"$LOCAL_GENERATED_XUI_MENU" <<EOF
#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="x-ui"
XUI_BIN="$TARGET_XUI_PATH"

cmd="\${1:-}"
case "\$cmd" in
  start)
    exec systemctl start "\$SERVICE_NAME"
    ;;
  stop)
    exec systemctl stop "\$SERVICE_NAME"
    ;;
  restart)
    exec systemctl restart "\$SERVICE_NAME"
    ;;
  status)
    exec systemctl status "\$SERVICE_NAME" --no-pager
    ;;
  enable)
    exec systemctl enable "\$SERVICE_NAME"
    ;;
  disable)
    exec systemctl disable "\$SERVICE_NAME"
    ;;
  log)
    exec journalctl -u "\$SERVICE_NAME" -e --no-pager
    ;;
  settings)
    shift
    exec "\$XUI_BIN" setting "\$@"
    ;;
  setting|cert|migrate)
    shift
    exec "\$XUI_BIN" "\$cmd" "\$@"
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
    exec "\$XUI_BIN" "\$@"
    ;;
esac
EOF
  chmod +x "$LOCAL_GENERATED_XUI_MENU"
}

local_db_setting() {
  local key="$1"
  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$LOCAL_DB_SNAPSHOT" "select value from settings where key='$key' limit 1;"
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$LOCAL_DB_SNAPSHOT" "$key" <<'PY'
import sqlite3
import sys

db_path, key = sys.argv[1], sys.argv[2]
conn = sqlite3.connect(db_path)
cur = conn.cursor()
row = cur.execute("select value from settings where key = ? limit 1", (key,)).fetchone()
print(row[0] if row else "")
conn.close()
PY
    return 0
  fi
  die "Neither sqlite3 nor python3 is available locally to inspect x-ui.db"
}

prepare_source_db_snapshot() {
  log "Creating source database snapshot"
  source_ssh "set -e
    rm -f '$REMOTE_SOURCE_DB_SNAPSHOT'
    if command -v python3 >/dev/null 2>&1; then
      python3 - <<'PY'
import sqlite3

src = '$SOURCE_DB_PATH'
dst = '$REMOTE_SOURCE_DB_SNAPSHOT'

src_conn = sqlite3.connect(f'file:{src}?mode=ro', uri=True)
dst_conn = sqlite3.connect(dst)
with dst_conn:
    src_conn.backup(dst_conn)
src_conn.close()
dst_conn.close()
PY
    elif command -v sqlite3 >/dev/null 2>&1; then
      sqlite3 '$SOURCE_DB_PATH' \".backup $REMOTE_SOURCE_DB_SNAPSHOT\"
    else
      echo 'python3 or sqlite3 is required on the source server to snapshot x-ui.db' >&2
      exit 1
    fi
  "
  source_scp_from "$REMOTE_SOURCE_DB_SNAPSHOT" "$LOCAL_DB_SNAPSHOT"
  source_ssh "rm -f '$REMOTE_SOURCE_DB_SNAPSHOT'"
}

collect_source_cert_paths() {
  local -A seen_paths=()
  local key
  local value

  require_cmd python3
  while IFS=$'\t' read -r key value; do
    value="${value//$'\r'/}"
    [[ -n "$value" ]] || continue
    [[ "$value" == /* ]] || die "Certificate path must be absolute: $key=$value"
    if [[ -z "${seen_paths[$value]:-}" ]]; then
      SOURCE_CERT_KEYS+=("$key")
      SOURCE_CERT_PATHS+=("$value")
      seen_paths["$value"]="true"
    fi
  done < <(python3 - "$LOCAL_DB_SNAPSHOT" <<'PY'
import json
import sqlite3
import sys

db_path = sys.argv[1]
conn = sqlite3.connect(db_path)
cur = conn.cursor()

for key in ("webCertFile", "webKeyFile", "subCertFile", "subKeyFile"):
    row = cur.execute(
        "select value from settings where key = ? limit 1", (key,)
    ).fetchone()
    if row and row[0]:
        print(f"{key}\t{str(row[0]).strip()}")

def walk(value, prefix):
    if isinstance(value, dict):
        for key, child in value.items():
            child_prefix = f"{prefix}.{key}" if prefix else key
            if key in ("certificateFile", "keyFile") and isinstance(child, str):
                path = child.strip()
                if path:
                    print(f"{child_prefix}\t{path}")
            walk(child, child_prefix)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            walk(child, f"{prefix}[{index}]")

try:
    rows = cur.execute("select stream_settings from inbounds").fetchall()
except sqlite3.OperationalError:
    rows = []

for index, (raw_settings,) in enumerate(rows):
    if not raw_settings:
        continue
    try:
        settings = json.loads(raw_settings)
    except (TypeError, json.JSONDecodeError):
        continue
    walk(settings, f"inbounds[{index}].stream_settings")

conn.close()
PY
  )

  if [[ ${#SOURCE_CERT_PATHS[@]} -gt 0 ]]; then
    EFFECTIVE_COPY_CERTS="true"
  fi
}

download_source_certs() {
  local i
  local remote_path
  local local_path
  mkdir -p "$LOCAL_CERT_DIR"
  for ((i = 0; i < ${#SOURCE_CERT_PATHS[@]}; i++)); do
    remote_path="${SOURCE_CERT_PATHS[$i]}"
    local_path="$LOCAL_CERT_DIR/cert-$i"
    if source_ssh "[ -f '$remote_path' ]"; then
      log "Downloading source file: $remote_path"
      source_scp_from "$remote_path" "$local_path"
      LOCAL_CERT_FILES+=("$local_path")
    else
      die "Configured certificate file is missing on source: $remote_path"
    fi
  done
}

stop_target_service_if_needed() {
  if [[ "$TARGET_HAS_SYSTEMD" == "yes" ]]; then
    target_ssh "systemctl stop x-ui >/dev/null 2>&1 || true"
  fi
}

create_target_backup() {
  log "Creating target backup"
  BACKUP_DIR="$(target_ssh "set -e
    ts=\$(date +%F-%H%M%S)
    backup_dir=/root/xui-migration-backup-\$ts
    mkdir -p \"\$backup_dir\"
    if [ -f '$TARGET_XUI_PATH' ]; then
      cp -a '$TARGET_XUI_PATH' \"\$backup_dir/x-ui.old\"
    fi
    if [ -d '$TARGET_BIN_DIR' ]; then
      tar -C '$TARGET_XUI_DIR' -czf \"\$backup_dir/bin.old.tar.gz\" bin
    fi
    if [ -f '$TARGET_DB_PATH' ]; then
      cp -a '$TARGET_DB_PATH' \"\$backup_dir/x-ui.db\"
    fi
    if [ -d /root/cert ]; then
      tar -C /root -czf \"\$backup_dir/root-cert.old.tar.gz\" cert
    fi
    if [ -d /etc/letsencrypt ]; then
      tar -C /etc -czf \"\$backup_dir/letsencrypt.old.tar.gz\" letsencrypt
    fi
    if [ -f '$TARGET_ENV_PATH' ]; then
      cp -a '$TARGET_ENV_PATH' \"\$backup_dir/default-x-ui.env\"
    fi
    if [ -f '$TARGET_SERVICE_PATH' ]; then
      cp -a '$TARGET_SERVICE_PATH' \"\$backup_dir/x-ui.service.old\"
    fi
    if [ -f '$TARGET_USR_BIN_PATH' ]; then
      cp -a '$TARGET_USR_BIN_PATH' \"\$backup_dir/usr-bin-x-ui.old\"
    fi
    printf '%s' \"\$backup_dir\"
  ")"
  log "Target backup saved to $BACKUP_DIR"
}

rollback_target() {
  if [[ -z "$BACKUP_DIR" ]]; then
    return 1
  fi

  log "Rolling back target server from backup: $BACKUP_DIR"
  target_ssh "set -e
    if command -v systemctl >/dev/null 2>&1; then
      systemctl stop x-ui >/dev/null 2>&1 || true
    fi

    if [ -f '$BACKUP_DIR/x-ui.old' ]; then
      mkdir -p '$TARGET_XUI_DIR'
      install -m 0755 '$BACKUP_DIR/x-ui.old' '$TARGET_XUI_PATH'
    else
      rm -f '$TARGET_XUI_PATH'
    fi

    if [ -f '$BACKUP_DIR/bin.old.tar.gz' ]; then
      rm -rf '$TARGET_BIN_DIR'
      mkdir -p '$TARGET_XUI_DIR'
      tar -C '$TARGET_XUI_DIR' -xzf '$BACKUP_DIR/bin.old.tar.gz'
    else
      rm -rf '$TARGET_BIN_DIR'
    fi

    if [ -f '$BACKUP_DIR/x-ui.db' ]; then
      mkdir -p '$TARGET_DB_DIR'
      install -m 0644 '$BACKUP_DIR/x-ui.db' '$TARGET_DB_PATH'
    else
      rm -f '$TARGET_DB_PATH'
    fi

    rm -rf /root/cert
    if [ -f '$BACKUP_DIR/root-cert.old.tar.gz' ]; then
      tar -C /root -xzf '$BACKUP_DIR/root-cert.old.tar.gz'
    fi

    rm -rf /etc/letsencrypt
    if [ -f '$BACKUP_DIR/letsencrypt.old.tar.gz' ]; then
      tar -C /etc -xzf '$BACKUP_DIR/letsencrypt.old.tar.gz'
    fi

    if [ -f '$BACKUP_DIR/default-x-ui.env' ]; then
      install -m 0644 '$BACKUP_DIR/default-x-ui.env' '$TARGET_ENV_PATH'
    else
      rm -f '$TARGET_ENV_PATH'
    fi

    if [ -f '$BACKUP_DIR/x-ui.service.old' ]; then
      install -m 0644 '$BACKUP_DIR/x-ui.service.old' '$TARGET_SERVICE_PATH'
    else
      rm -f '$TARGET_SERVICE_PATH'
    fi

    if [ -f '$BACKUP_DIR/usr-bin-x-ui.old' ]; then
      install -m 0755 '$BACKUP_DIR/usr-bin-x-ui.old' '$TARGET_USR_BIN_PATH'
    else
      rm -f '$TARGET_USR_BIN_PATH'
    fi

    rm -rf '$REMOTE_TARGET_TMP_DIR'

    if command -v systemctl >/dev/null 2>&1; then
      systemctl daemon-reload >/dev/null 2>&1 || true
      if [ '$TARGET_SERVICE_ACTIVE' = 'active' ] && [ -f '$TARGET_SERVICE_PATH' ]; then
        systemctl start x-ui >/dev/null 2>&1 || true
      fi
    fi
  "
}

verify_target_db() {
  target_ssh "set -e
    '$TARGET_XUI_PATH' setting -show true >/dev/null
    if command -v python3 >/dev/null 2>&1; then
      python3 - <<'PY'
import json
import os
import sqlite3

conn = sqlite3.connect('$TARGET_DB_PATH')
cur = conn.cursor()
cols = [row[1] for row in cur.execute('pragma table_info(inbounds)')]
need = ['traffic_reset', 'last_traffic_reset_time', 'socks_proxy_enabled']
missing = [name for name in need if name not in cols]
if missing:
    raise SystemExit('missing migrated columns: ' + ', '.join(missing))

cert_paths = []
for key in ('webCertFile', 'webKeyFile', 'subCertFile', 'subKeyFile'):
    row = cur.execute(
        'select value from settings where key = ? limit 1', (key,)
    ).fetchone()
    if row and row[0]:
        cert_paths.append((key, str(row[0]).strip()))

def walk(value, prefix):
    if isinstance(value, dict):
        for key, child in value.items():
            child_prefix = f'{prefix}.{key}' if prefix else key
            if key in ('certificateFile', 'keyFile') and isinstance(child, str):
                path = child.strip()
                if path:
                    cert_paths.append((child_prefix, path))
            walk(child, child_prefix)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            walk(child, f'{prefix}[{index}]')

for index, (raw_settings,) in enumerate(
    cur.execute('select stream_settings from inbounds').fetchall()
):
    if not raw_settings:
        continue
    try:
        settings = json.loads(raw_settings)
    except (TypeError, json.JSONDecodeError):
        continue
    walk(settings, f'inbounds[{index}].stream_settings')

missing_cert_paths = [
    f'{key}={path}' for key, path in cert_paths if not os.path.isfile(path)
]
if missing_cert_paths:
    raise SystemExit(
        'missing configured certificate files: ' + ', '.join(missing_cert_paths)
    )

print('inbound_columns_ok')
print('certificate_paths_ok')
conn.close()
PY
    fi
  "
}

verify_target_runtime() {
  target_ssh "set -e
    for attempt in \$(seq 1 10); do
      if pgrep -f '[x]ray-linux-.*bin/config.json' >/dev/null 2>&1; then
        exit 0
      fi
      sleep 1
    done
    echo 'xray process did not start after migration' >&2
    journalctl -u x-ui -n 80 --no-pager >&2 || true
    exit 1
  "
}

install_target_files() {
  local i
  local basename
  local remote_path
  local remote_tmp_path
  local perm

  log "Preparing target directories"
  target_ssh "set -e
    mkdir -p '$TARGET_XUI_DIR' '$TARGET_BIN_DIR' '$TARGET_DB_DIR' '$TARGET_LOG_DIR' '$REMOTE_TARGET_TMP_DIR'
  "

  log "Uploading x-ui binary"
  target_scp_to "$LOCAL_XUI" "$REMOTE_TARGET_TMP_DIR/x-ui"

  if [[ "$TARGET_BOOTSTRAP_NEEDED" == "true" ]]; then
    log "Uploading bootstrap files"
    target_scp_to "$LOCAL_GENERATED_SERVICE" "$REMOTE_TARGET_TMP_DIR/x-ui.service"
    target_scp_to "$LOCAL_GENERATED_XUI_MENU" "$REMOTE_TARGET_TMP_DIR/x-ui.sh"
  fi

  if [[ "$EFFECTIVE_COPY_ENV" == "true" && -f "$LOCAL_ENV" ]]; then
    log "Uploading source env file"
    target_scp_to "$LOCAL_ENV" "$REMOTE_TARGET_TMP_DIR/default-x-ui.env"
  fi

  if [[ "$EFFECTIVE_COPY_BIN" == "true" ]]; then
    for basename in "${SOURCE_BIN_FILES[@]}"; do
      log "Uploading source bin file: $basename"
      target_scp_to "$LOCAL_BIN_DIR/$basename" "$REMOTE_TARGET_TMP_DIR/$basename"
    done
  fi

  if [[ "$EFFECTIVE_COPY_DB" == "true" ]]; then
    log "Uploading source database snapshot"
    target_scp_to "$LOCAL_DB_SNAPSHOT" "$REMOTE_TARGET_TMP_DIR/x-ui.db"
  fi

  if [[ "$EFFECTIVE_COPY_CERTS" == "true" ]]; then
    for ((i = 0; i < ${#SOURCE_CERT_PATHS[@]}; i++)); do
      if [[ -n "${LOCAL_CERT_FILES[$i]}" ]]; then
        target_scp_to "${LOCAL_CERT_FILES[$i]}" "$REMOTE_TARGET_TMP_DIR/cert-$i"
      fi
    done
  fi

  log "Installing files on target"
  target_ssh "set -e
    install -m 0755 '$REMOTE_TARGET_TMP_DIR/x-ui' '$TARGET_XUI_PATH'
  "

  if [[ "$TARGET_BOOTSTRAP_NEEDED" == "true" ]]; then
    target_ssh "set -e
      install -m 0755 '$REMOTE_TARGET_TMP_DIR/x-ui.sh' '$TARGET_XUI_DIR/x-ui.sh'
      install -m 0755 '$REMOTE_TARGET_TMP_DIR/x-ui.sh' '$TARGET_USR_BIN_PATH'
      install -m 0644 '$REMOTE_TARGET_TMP_DIR/x-ui.service' '$TARGET_SERVICE_PATH'
      systemctl daemon-reload
      systemctl enable x-ui >/dev/null 2>&1 || true
    "
  fi

  if [[ "$EFFECTIVE_COPY_ENV" == "true" ]]; then
    target_ssh "set -e
      if [ -f '$REMOTE_TARGET_TMP_DIR/default-x-ui.env' ]; then
        install -m 0644 '$REMOTE_TARGET_TMP_DIR/default-x-ui.env' '$TARGET_ENV_PATH'
      fi
    "
  fi

  if [[ "$EFFECTIVE_COPY_BIN" == "true" ]]; then
    for basename in "${SOURCE_BIN_FILES[@]}"; do
      if [[ "$basename" == xray-linux-* ]]; then
        perm="0755"
      else
        perm="0644"
      fi
      target_ssh "install -m $perm '$REMOTE_TARGET_TMP_DIR/$basename' '$TARGET_BIN_DIR/$basename'"
    done
  fi

  if [[ "$EFFECTIVE_COPY_DB" == "true" ]]; then
    target_ssh "set -e
      rm -f '$TARGET_DB_PATH-wal' '$TARGET_DB_PATH-shm'
      install -m 0644 '$REMOTE_TARGET_TMP_DIR/x-ui.db' '$TARGET_DB_PATH'
    "
  fi

  if [[ "$EFFECTIVE_COPY_CERTS" == "true" ]]; then
    for ((i = 0; i < ${#SOURCE_CERT_PATHS[@]}; i++)); do
      if [[ -z "${LOCAL_CERT_FILES[$i]}" ]]; then
        continue
      fi
      remote_path="${SOURCE_CERT_PATHS[$i]}"
      remote_tmp_path="$REMOTE_TARGET_TMP_DIR/cert-$i"
      case "${SOURCE_CERT_KEYS[$i],,}" in
        *keyfile*)
          perm="0600"
          ;;
        *)
          perm="0644"
          ;;
      esac
      target_ssh "install -D -m $perm '$remote_tmp_path' '$remote_path'"
    done
  fi

  target_ssh "rm -rf '$REMOTE_TARGET_TMP_DIR'"
}

on_error() {
  local line="$1"
  log "Migration failed near line $line"
  if [[ "$AUTO_ROLLBACK" == "true" ]]; then
    rollback_target || true
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-host)
      SOURCE_HOST="${2:-}"; shift 2 ;;
    --source-port)
      SOURCE_PORT="${2:-}"; shift 2 ;;
    --source-user)
      SOURCE_USER="${2:-}"; shift 2 ;;
    --source-pass)
      SOURCE_PASS="${2:-}"; shift 2 ;;
    --target-host)
      TARGET_HOST="${2:-}"; shift 2 ;;
    --target-port)
      TARGET_PORT="${2:-}"; shift 2 ;;
    --target-user)
      TARGET_USER="${2:-}"; shift 2 ;;
    --target-pass)
      TARGET_PASS="${2:-}"; shift 2 ;;
    --copy-xray)
      COPY_XRAY="true"; shift ;;
    --copy-db)
      COPY_DB_MODE="true"; shift ;;
    --keep-target-db)
      COPY_DB_MODE="false"; shift ;;
    --no-rollback)
      AUTO_ROLLBACK="false"; shift ;;
    --dry-run)
      DRY_RUN="true"; shift ;;
    --yes)
      ASSUME_YES="true"; shift ;;
    --help|-h)
      usage; exit 0 ;;
    *)
      die "Unknown argument: $1" ;;
  esac
done

[[ -n "$SOURCE_HOST" ]] || die "--source-host is required"
[[ -n "$SOURCE_PASS" ]] || die "--source-pass is required"
[[ -n "$TARGET_HOST" ]] || die "--target-host is required"
[[ -n "$TARGET_PASS" ]] || die "--target-pass is required"

require_cmd ssh
require_cmd scp
require_cmd sshpass
require_cmd mktemp

WORK_DIR="$(mktemp -d)"
SOURCE_KNOWN_HOSTS="$WORK_DIR/source_known_hosts"
TARGET_KNOWN_HOSTS="$WORK_DIR/target_known_hosts"
LOCAL_XUI="$WORK_DIR/x-ui"
LOCAL_DB_SNAPSHOT="$WORK_DIR/x-ui.db"
LOCAL_ENV="$WORK_DIR/default-x-ui.env"
LOCAL_BIN_DIR="$WORK_DIR/bin"
LOCAL_CERT_DIR="$WORK_DIR/certs"
LOCAL_GENERATED_SERVICE="$WORK_DIR/x-ui.service"
LOCAL_GENERATED_XUI_MENU="$WORK_DIR/x-ui.sh"
REMOTE_SOURCE_DB_SNAPSHOT="/tmp/x-ui.db.snapshot.$$"
REMOTE_TARGET_TMP_DIR="/tmp/3x-ui-migrate.$$"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT
trap 'on_error $LINENO' ERR

log "Collecting source server facts"
SOURCE_FACTS="$(source_ssh "set -e
  release=unknown
  if [ -f /etc/os-release ]; then
    . /etc/os-release
    release=\${ID:-unknown}
  fi
  printf 'arch=%s\n' \"\$(uname -m)\"
  printf 'release=%s\n' \"\$release\"
  printf 'xui_exists=%s\n' \"\$( [ -f '$SOURCE_XUI_PATH' ] && echo yes || echo no )\"
  if [ -f '$SOURCE_XUI_PATH' ]; then
    printf 'xui_sha=%s\n' \"\$(sha256sum '$SOURCE_XUI_PATH' | awk '{print \$1}')\"
    printf 'xui_size=%s\n' \"\$(wc -c < '$SOURCE_XUI_PATH' | tr -d ' ')\" 
  fi
  printf 'db_exists=%s\n' \"\$( [ -f '$SOURCE_DB_PATH' ] && echo yes || echo no )\"
  printf 'env_exists=%s\n' \"\$( [ -f '$SOURCE_ENV_PATH' ] && echo yes || echo no )\"
  printf 'bin_exists=%s\n' \"\$( [ -d '$SOURCE_BIN_DIR' ] && echo yes || echo no )\"
")"
printf '%s\n' "$SOURCE_FACTS"

log "Collecting target server facts"
TARGET_FACTS="$(target_ssh "set -e
  release=unknown
  if [ -f /etc/os-release ]; then
    . /etc/os-release
    release=\${ID:-unknown}
  fi
  printf 'arch=%s\n' \"\$(uname -m)\"
  printf 'release=%s\n' \"\$release\"
  printf 'has_systemd=%s\n' \"\$(command -v systemctl >/dev/null 2>&1 && echo yes || echo no)\"
  printf 'service=%s\n' \"\$(systemctl is-active x-ui 2>/dev/null || true)\"
  printf 'xui_exists=%s\n' \"\$( [ -f '$TARGET_XUI_PATH' ] && echo yes || echo no )\"
  if [ -f '$TARGET_XUI_PATH' ]; then
    printf 'xui_sha=%s\n' \"\$(sha256sum '$TARGET_XUI_PATH' | awk '{print \$1}')\"
  fi
  printf 'db_exists=%s\n' \"\$( [ -f '$TARGET_DB_PATH' ] && echo yes || echo no )\"
  printf 'env_exists=%s\n' \"\$( [ -f '$TARGET_ENV_PATH' ] && echo yes || echo no )\"
  printf 'usr_bin_exists=%s\n' \"\$( [ -f '$TARGET_USR_BIN_PATH' ] && echo yes || echo no )\"
")"
printf '%s\n' "$TARGET_FACTS"

SOURCE_ARCH="$(fact_value arch "$SOURCE_FACTS")"
TARGET_ARCH="$(fact_value arch "$TARGET_FACTS")"
SOURCE_RELEASE="$(fact_value release "$SOURCE_FACTS")"
TARGET_RELEASE="$(fact_value release "$TARGET_FACTS")"
SOURCE_XUI_EXISTS="$(fact_value xui_exists "$SOURCE_FACTS")"
SOURCE_SHA="$(fact_value xui_sha "$SOURCE_FACTS")"
SOURCE_DB_EXISTS="$(fact_value db_exists "$SOURCE_FACTS")"
SOURCE_ENV_EXISTS="$(fact_value env_exists "$SOURCE_FACTS")"
SOURCE_BIN_EXISTS="$(fact_value bin_exists "$SOURCE_FACTS")"
TARGET_XUI_EXISTS="$(fact_value xui_exists "$TARGET_FACTS")"
TARGET_DB_EXISTS="$(fact_value db_exists "$TARGET_FACTS")"
TARGET_ENV_EXISTS="$(fact_value env_exists "$TARGET_FACTS")"
TARGET_USR_BIN_EXISTS="$(fact_value usr_bin_exists "$TARGET_FACTS")"
TARGET_HAS_SYSTEMD="$(fact_value has_systemd "$TARGET_FACTS")"
TARGET_SERVICE_ACTIVE="$(fact_value service "$TARGET_FACTS")"

[[ "$SOURCE_XUI_EXISTS" == "yes" ]] || die "Source x-ui binary not found at $SOURCE_XUI_PATH"
[[ -n "$SOURCE_SHA" ]] || die "Failed to read source x-ui checksum"
[[ "$SOURCE_ARCH" == "$TARGET_ARCH" ]] || die "Architecture mismatch: source=$SOURCE_ARCH target=$TARGET_ARCH"
[[ "$TARGET_HAS_SYSTEMD" == "yes" ]] || die "Target server does not provide systemd; this script currently supports systemd-based Linux only"

if [[ "$TARGET_XUI_EXISTS" != "yes" ]]; then
  TARGET_BOOTSTRAP_NEEDED="true"
fi

case "$COPY_DB_MODE" in
  true)
    EFFECTIVE_COPY_DB="true"
    ;;
  false)
    EFFECTIVE_COPY_DB="false"
    ;;
  auto)
    if [[ "$TARGET_DB_EXISTS" != "yes" ]]; then
      EFFECTIVE_COPY_DB="true"
    fi
    ;;
  *)
    die "Unexpected copy-db mode: $COPY_DB_MODE"
    ;;
esac

if [[ "$TARGET_BOOTSTRAP_NEEDED" == "true" ]]; then
  EFFECTIVE_COPY_DB="true"
  EFFECTIVE_COPY_BIN="true"
  EFFECTIVE_COPY_ENV="true"
fi

if [[ "$COPY_XRAY" == "true" ]]; then
  EFFECTIVE_COPY_BIN="true"
fi

if [[ "$COPY_DB_MODE" == "true" ]]; then
  EFFECTIVE_COPY_ENV="true"
fi

if [[ "$EFFECTIVE_COPY_DB" == "true" && "$SOURCE_DB_EXISTS" != "yes" ]]; then
  die "Source database not found at $SOURCE_DB_PATH"
fi

if [[ "$EFFECTIVE_COPY_BIN" == "true" && "$SOURCE_BIN_EXISTS" != "yes" ]]; then
  die "Source bin directory not found at $SOURCE_BIN_DIR"
fi

generate_service_file
generate_xui_cli_script

log "Migration plan"
log "  source release: $SOURCE_RELEASE"
log "  target release: $TARGET_RELEASE"
log "  bootstrap target: $TARGET_BOOTSTRAP_NEEDED"
log "  copy source database: $EFFECTIVE_COPY_DB"
log "  copy source bin assets: $EFFECTIVE_COPY_BIN"
log "  copy source env file: $EFFECTIVE_COPY_ENV"

if [[ "$DRY_RUN" == "true" ]]; then
  log "Dry-run complete. No changes were made."
  exit 0
fi

if [[ "$ASSUME_YES" != "true" ]]; then
  printf 'Proceed with migration from %s to %s? [y/N]: ' "$SOURCE_HOST" "$TARGET_HOST"
  read -r answer
  [[ "$answer" =~ ^[Yy]$ ]] || die "Aborted by user"
fi

log "Downloading source x-ui binary"
source_scp_from "$SOURCE_XUI_PATH" "$LOCAL_XUI"
[[ "$(sha256_file "$LOCAL_XUI")" == "$SOURCE_SHA" ]] || die "Downloaded x-ui checksum mismatch"

if [[ "$EFFECTIVE_COPY_BIN" == "true" ]]; then
  mkdir -p "$LOCAL_BIN_DIR"
  while IFS= read -r basename; do
    [[ -n "$basename" ]] || continue
    SOURCE_BIN_FILES+=("$basename")
    log "Downloading source bin file: $basename"
    source_scp_from "$SOURCE_BIN_DIR/$basename" "$LOCAL_BIN_DIR/$basename"
  done < <(source_ssh "find '$SOURCE_BIN_DIR' -maxdepth 1 -type f \\( -name 'xray-linux-*' -o -name '*.dat' \\) -exec basename {} \\; | sort")
  [[ ${#SOURCE_BIN_FILES[@]} -gt 0 ]] || die "No xray binary or .dat files found under $SOURCE_BIN_DIR"
fi

if [[ "$EFFECTIVE_COPY_DB" == "true" ]]; then
  prepare_source_db_snapshot
fi

if [[ "$EFFECTIVE_COPY_ENV" == "true" && "$SOURCE_ENV_EXISTS" == "yes" ]]; then
  log "Downloading source env file"
  source_scp_from "$SOURCE_ENV_PATH" "$LOCAL_ENV"
fi

if [[ "$EFFECTIVE_COPY_DB" == "true" ]]; then
  collect_source_cert_paths
  if [[ "$EFFECTIVE_COPY_CERTS" == "true" ]]; then
    log "Detected certificate paths in source database"
    download_source_certs
  fi
fi

stop_target_service_if_needed
create_target_backup
install_target_files

log "Starting target x-ui service"
target_ssh "set -e
  systemctl start x-ui
  sleep 3
  systemctl is-active x-ui >/dev/null
"

log "Verifying migration result"
verify_target_db
verify_target_runtime

TARGET_SHA="$(target_ssh "sha256sum '$TARGET_XUI_PATH' | awk '{print \$1}'")"
[[ "$TARGET_SHA" == "$SOURCE_SHA" ]] || die "Target x-ui checksum mismatch after install"

log "Migration completed successfully"
log "Source x-ui sha256: $SOURCE_SHA"
log "Target x-ui sha256: $TARGET_SHA"
log "Backup directory: $BACKUP_DIR"
