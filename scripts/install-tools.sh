#!/usr/bin/env bash
# Builds CI tools pinned in tools/<name>/go.mod (checksums in its go.sum) into the directory given as $1;
# the names after it select which, all of them by default.
# One module per tool: their dependency graphs conflict when merged into a single go.mod.
set -euo pipefail

dest="${1:?usage: install-tools.sh <dir> [tool...]}"
shift
mkdir -p "${dest}"
dest="$(cd "${dest}" && pwd)"

cd "$(dirname "${BASH_SOURCE[0]}")/../tools"
if [ "$#" -eq 0 ]; then
  set -- *
fi
for tool in "$@"; do
  (cd "${tool}" && GOBIN="${dest}" CGO_ENABLED=0 go install tool)
done
