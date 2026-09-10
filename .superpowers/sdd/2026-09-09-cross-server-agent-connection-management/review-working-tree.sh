#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 BASE_TREE OUTFILE" >&2
  exit 2
fi

base=$1
out=$2
here=$(cd "$(dirname "$0")" && pwd)
after=$("$here/snapshot-tree.sh")

{
  echo "# Review package: working tree ${base}..${after}"
  echo
  echo "## Files changed"
  git diff --stat "${base}" "${after}"
  echo
  echo "## Diff"
  git diff -U10 "${base}" "${after}"
} > "$out"

echo "wrote ${out}: $(wc -c < "$out" | tr -d ' ') bytes"
