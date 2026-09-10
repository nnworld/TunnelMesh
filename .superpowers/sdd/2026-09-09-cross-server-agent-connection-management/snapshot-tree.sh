#!/usr/bin/env bash
set -euo pipefail

index=$(mktemp)
trap 'rm -f "$index"' EXIT
GIT_INDEX_FILE="$index" git read-tree HEAD
GIT_INDEX_FILE="$index" git add -A
GIT_INDEX_FILE="$index" git write-tree
