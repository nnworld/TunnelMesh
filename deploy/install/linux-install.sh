#!/usr/bin/env bash
set -euo pipefail

ROLE="both"
BINARY_DIR="$(pwd)"
ENABLE=0
START=0

usage() {
  cat >&2 <<'EOF'
Usage: linux-install.sh [--role server|agent|both] [--binary-dir DIR] [--enable] [--start]

The script installs binaries and systemd units. It never creates or overwrites
configuration files; create /etc/tunnelmesh/server.yaml or agent.yaml first.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role) ROLE="${2:?missing role}"; shift 2 ;;
    --binary-dir) BINARY_DIR="${2:?missing binary directory}"; shift 2 ;;
    --enable) ENABLE=1; shift ;;
    --start) START=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
case "$ROLE" in server|agent|both) ;; *) echo "invalid role: $ROLE" >&2; exit 2 ;; esac

install -d -m 0750 /etc/tunnelmesh /var/lib/tunnelmesh /var/lib/tunnelmesh-agent
if ! getent group tunnelmesh >/dev/null 2>&1; then groupadd --system tunnelmesh; fi
if ! id tunnelmesh >/dev/null 2>&1; then useradd --system --gid tunnelmesh --home-dir /var/lib/tunnelmesh --shell /usr/sbin/nologin tunnelmesh; fi
chown tunnelmesh:tunnelmesh /var/lib/tunnelmesh /var/lib/tunnelmesh-agent

install_binary() {
  local name="$1"
  test -x "$BINARY_DIR/$name" || { echo "missing executable: $BINARY_DIR/$name" >&2; exit 1; }
  install -m 0755 "$BINARY_DIR/$name" "/usr/local/bin/$name"
}

services=()
if [[ "$ROLE" == server || "$ROLE" == both ]]; then
  install_binary tunnelmesh-server
  install -m 0644 "$(dirname "$0")/../systemd/tunnelmesh-server.service" /etc/systemd/system/tunnelmesh-server.service
  services+=(tunnelmesh-server.service)
fi
if [[ "$ROLE" == agent || "$ROLE" == both ]]; then
  install_binary tunnelmesh-agent
  install -m 0644 "$(dirname "$0")/../systemd/tunnelmesh-agent.service" /etc/systemd/system/tunnelmesh-agent.service
  services+=(tunnelmesh-agent.service)
fi

systemctl daemon-reload
for service in "${services[@]}"; do
  if [[ "$ENABLE" -eq 1 ]]; then systemctl enable "$service"; fi
  if [[ "$START" -eq 1 ]]; then systemctl restart "$service"; fi
done
echo "installed TunnelMesh $ROLE; configure /etc/tunnelmesh/*.yaml before starting"
