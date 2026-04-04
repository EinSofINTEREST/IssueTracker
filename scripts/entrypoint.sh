#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ISSUETRACKER_BINARY="${PROJECT_ROOT}/bin/issuetracker"

if [[ ! -x "${ISSUETRACKER_BINARY}" ]]; then
  echo "issuetracker binary not found or not executable: ${ISSUETRACKER_BINARY}" >&2
  echo "Run 'make build' first." >&2
  exit 1
fi

exec "${ISSUETRACKER_BINARY}" "$@"