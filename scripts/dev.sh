#!/usr/bin/env bash
set -euo pipefail

project_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
local_go="$project_root/.tools/go/bin/go"

if [[ -x "$local_go" ]]; then
    export GOPATH="${GOPATH:-$project_root/.tools/gopath}"
    export GOCACHE="${GOCACHE:-$project_root/.tools/gocache}"
    task_go=$local_go
elif command -v go >/dev/null 2>&1; then
    task_go=$(command -v go)
else
    printf 'Go 1.24+ is required on PATH or at %s\n' "$local_go" >&2
    exit 127
fi

# Resolve module and command paths consistently when called from another directory.
cd -- "$project_root"
exec "$task_go" "$@"
