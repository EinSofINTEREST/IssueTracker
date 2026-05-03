#!/usr/bin/env bash
# lint.sh — 프로젝트 루트 기준으로 golangci-lint 를 실행합니다 (이슈 #238).
#
# 사용법:
#   scripts/lint.sh           # 전체 lint
#   scripts/lint.sh ./...     # 명시적 경로 지정 (기본값과 동일)
#   scripts/lint.sh --fix     # 자동 수정 가능한 항목 수정
#
# 호출 경로에 관계없이 항상 프로젝트 루트(go.mod 위치)에서 실행됩니다.
# Makefile 의 `lint` 타겟이 이 스크립트를 호출합니다.

set -euo pipefail

# go.mod 가 있는 프로젝트 루트로 이동
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# CI 고정 버전과 로컬 버전을 일치시켜 lint drift 방지 (CodeRabbit 피드백 반영 — PR #239)
LINT_VERSION="v1.64.8"

if ! command -v golangci-lint &>/dev/null; then
  echo "golangci-lint not found. Install with:" >&2
  echo "  go install github.com/golangci/golangci-lint/cmd/golangci-lint@${LINT_VERSION}" >&2
  exit 1
fi

ARGS=("${@:-./...}")
echo "Running golangci-lint in $PROJECT_ROOT ..."
exec golangci-lint run "${ARGS[@]}"
