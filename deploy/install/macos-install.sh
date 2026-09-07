#!/usr/bin/env bash
set -euo pipefail

ROLE="${1:-agent}"
BINARY="${2:-$(pwd)/tunnelmesh-$ROLE}"
CONFIG="${3:-$HOME/.config/tunnelmesh/$ROLE.yaml}"
case "$ROLE" in server|agent) ;; *) echo "usage: macos-install.sh server|agent [binary] [config]" >&2; exit 2 ;; esac
test -x "$BINARY" || { echo "missing executable: $BINARY" >&2; exit 1; }
mkdir -p "$HOME/.local/bin" "$HOME/.config/tunnelmesh" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
install -m 0755 "$BINARY" "$HOME/.local/bin/tunnelmesh-$ROLE"
label="com.tunnelmesh.$ROLE"
plist="$HOME/Library/LaunchAgents/$label.plist"
cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key><array>
    <string>$HOME/.local/bin/tunnelmesh-$ROLE</string><string>--config</string><string>$CONFIG</string><string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/tunnelmesh-$ROLE.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/tunnelmesh-$ROLE.err.log</string>
</dict></plist>
EOF
plutil -lint "$plist"
launchctl bootout "gui/$(id -u)" "$plist" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$plist"
launchctl kickstart -k "gui/$(id -u)/$label"
echo "installed $label; logs are under $HOME/Library/Logs/"
