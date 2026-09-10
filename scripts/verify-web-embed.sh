#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
diff -qr "$root/web/dist" "$root/internal/server/web_dist"
echo "web/dist and internal/server/web_dist match"
