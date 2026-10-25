#!/usr/bin/env bash
# Install or update the EternalShell watcher as a systemd service (Ubuntu).
#
# Run it on the server with sudo. It is safe to run again for updates:
# existing config and secret files are never overwritten.
#
# Expected layout (copy these three things to the server, same folder):
#   deploy/                 this script and eternalshell.service
#   eternalshell            the binary
#   config.example.yaml     used once, if there is no config yet
#
# Build the binary on your machine; static, so it runs on any Ubuntu:
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o eternalshell ./cmd/eternalshell
#
# Usage:  sudo ./deploy/install.sh [path/to/eternalshell]

set -euo pipefail

SERVICE_USER=eternalshell
BIN_DEST=/usr/local/bin/eternalshell
CONF_DIR=/etc/eternalshell
CONF_FILE=$CONF_DIR/config.yaml
SECRET_FILE=$CONF_DIR/secret.env
UNIT_DEST=/etc/systemd/system/eternalshell.service
SECRET_VAR=ETERNALSHELL_SMTP_PASSWORD
PLACEHOLDER_HOST=smtp.example.com

BUILD_CMD='CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o eternalshell ./cmd/eternalshell'

say()  { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- Checks ------------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0"
command -v systemctl >/dev/null 2>&1 || die "systemctl not found: this installer needs systemd"
[ "$(uname -m)" = "x86_64" ] || warn "this machine is $(uname -m); EternalShell targets x86_64 (amd64)"

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(dirname "$SCRIPT_DIR")
BIN_SRC=${1:-$ROOT_DIR/eternalshell}
EXAMPLE_SRC=$ROOT_DIR/config.example.yaml
UNIT_SRC=$SCRIPT_DIR/eternalshell.service

[ -f "$BIN_SRC" ] || die "binary not found: $BIN_SRC
build it on your machine and copy it next to deploy/:
  $BUILD_CMD"
[ -f "$UNIT_SRC" ] || die "unit file not found: $UNIT_SRC"
[ -f "$CONF_FILE" ] || [ -f "$EXAMPLE_SRC" ] || die "config.example.yaml not found: $EXAMPLE_SRC"

# Run the new binary once before anything on the server is touched. An unknown
# command makes it print its usage and exit with code 2. Anything else means
# it cannot run here (wrong architecture, not executable, missing libraries),
# and a working installation must not be replaced by it.
chmod +x "$BIN_SRC" 2>/dev/null || true
set +e
"$BIN_SRC" --installer-smoke-test >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 2 ] || die "$BIN_SRC does not run on this machine (exit code $rc).
Build it static for Linux amd64:
  $BUILD_CMD"

# --- User --------------------------------------------------------------------

if id "$SERVICE_USER" >/dev/null 2>&1; then
  say "user $SERVICE_USER: exists"
else
  useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  say "user $SERVICE_USER: created (system user, no login shell, no home)"
fi

# The watcher reads container names and compose labels from the Docker API, so
# the user needs access to the Docker socket. NOTE: membership in the docker
# group is root-equivalent. EternalShell only makes read-only API calls, but
# anyone who controls this user could take over the machine through Docker.
if getent group docker >/dev/null 2>&1; then
  usermod -aG docker "$SERVICE_USER"
  say "user $SERVICE_USER: in group docker (root-equivalent, read-only use)"
else
  warn "no group 'docker': Docker is not installed here, so container names will be missing"
fi

# --- Binary ------------------------------------------------------------------

# install replaces the file instead of writing into it, so this also works
# while the old version is running.
install -m 0755 -o root -g root "$BIN_SRC" "$BIN_DEST"
say "binary: $BIN_DEST"

# --- Config and secret (never overwritten) -----------------------------------

install -d -m 0750 -o root -g "$SERVICE_USER" "$CONF_DIR"

if [ -e "$CONF_FILE" ]; then
  say "config: $CONF_FILE exists, left unchanged"
else
  install -m 0640 -o root -g "$SERVICE_USER" "$EXAMPLE_SRC" "$CONF_FILE"
  say "config: $CONF_FILE created from config.example.yaml"
fi
# Content is never touched, but owner and mode are enforced on every run.
chown root:"$SERVICE_USER" "$CONF_FILE"
chmod 0640 "$CONF_FILE"

if [ -e "$SECRET_FILE" ]; then
  say "secret: $SECRET_FILE exists, left unchanged"
else
  ( umask 077; printf '%s=\n' "$SECRET_VAR" > "$SECRET_FILE" )
  say "secret: $SECRET_FILE created (empty)"
fi
chown root:root "$SECRET_FILE"
chmod 0600 "$SECRET_FILE"

# --- systemd unit ------------------------------------------------------------

install -m 0644 -o root -g root "$UNIT_SRC" "$UNIT_DEST"
systemctl daemon-reload
systemctl enable eternalshell >/dev/null 2>&1
say "service: unit installed and enabled"

# --- Start (only if it can work) ---------------------------------------------

# The watcher refuses to start without mail settings, so with the placeholders
# still in place it would only fail and restart over and over.
ready=yes
missing=()
if ! grep -Eq "^[[:space:]]*${SECRET_VAR}=[^[:space:]]" "$SECRET_FILE"; then
  ready=no
  missing+=("the SMTP password in $SECRET_FILE")
fi
if grep -Eq "^[[:space:]]*host:[[:space:]]*\"?${PLACEHOLDER_HOST}\"?([[:space:]]|$)" "$CONF_FILE"; then
  ready=no
  missing+=("the smtp section in $CONF_FILE (it still has the example values)")
fi

say
if [ "$ready" = yes ]; then
  systemctl restart eternalshell
  say "service: (re)started"
else
  say "The service is enabled but NOT started yet. Please fill in:"
  for m in "${missing[@]}"; do say "  - $m"; done
  say
  say "  sudoedit $CONF_FILE      # smtp host, port, username, to, server name, alerts"
  say "  sudoedit $SECRET_FILE   # ${SECRET_VAR}=your-password (no quotes needed)"
  say
  say "Then start it by running this script again, or:"
  say "  sudo systemctl restart eternalshell"
  say
  say "To check that mail works first (as yourself, with the password exported):"
  say "  sudo -E env ${SECRET_VAR}='...' $BIN_DEST test-mail --config $CONF_FILE"
fi

say
say "Check the service:"
say "  systemctl status eternalshell"
say "  journalctl -u eternalshell -f"
say
say "To update later: build, copy the binary and run this script again:"
say "  $BUILD_CMD"
