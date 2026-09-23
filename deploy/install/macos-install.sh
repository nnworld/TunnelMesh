#!/usr/bin/env bash
set -euo pipefail

ROLE="${1:-agent}"
BINARY="${2:-$(pwd)/tunnelmesh-$ROLE}"
CONFIG="${3:-$HOME/.config/tunnelmesh/$ROLE.yaml}"
case "$ROLE" in server|agent|client) ;; *) echo "usage: macos-install.sh server|agent|client [binary] [config]" >&2; exit 2 ;; esac
test -x "$BINARY" || { echo "missing executable: $BINARY" >&2; exit 1; }
mkdir -p "$HOME/.local/bin" "$HOME/.config/tunnelmesh" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
install -m 0755 "$BINARY" "$HOME/.local/bin/tunnelmesh-$ROLE"
label="com.tunnelmesh.$ROLE"
plist="$HOME/Library/LaunchAgents/$label.plist"
installed="$HOME/.local/bin/tunnelmesh-$ROLE"
# 三个角色共用 deploy/macos/tunnelmesh.plist，只替换占位符。此前 client 走模板、
# server/agent 走内联 heredoc，两份来源并存时 client 会原样拷贝模板并忽略 $CONFIG。
sed -e "s|__ROLE__|$ROLE|g" \
  -e "s|__HOME__|$HOME|g" \
  -e "s|__BINARY__|$installed|g" \
  -e "s|__CONFIG__|$CONFIG|g" \
  -e "s|__ENVIRONMENT__||g" \
  "$(dirname "$0")/../macos/tunnelmesh.plist" >"$plist"
plutil -lint "$plist"
launchctl bootout "gui/$(id -u)" "$plist" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$plist"
launchctl kickstart -k "gui/$(id -u)/$label"
echo "installed $label; logs are under $HOME/Library/Logs/"
