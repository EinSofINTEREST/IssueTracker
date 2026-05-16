# claudegen 자체 Claude Code 이미지 (이슈 #269)
#
# Anthropic 공식 ghcr.io/anthropics/claude-code 이미지가 비공개 상태이므로 자체 빌드합니다.
# 호스트의 ~/.claude/ 디렉토리 + ~/.claude.json 파일을 마운트하여 구독 quota 안에서
# sonnet 호출 (이슈 #266).
#
# 기본 user 는 root — Claude CLI 의 OAuth 인증은 HOME 기반 ($HOME/.claude{.json,/}) 로 동작.
# 호스트 인증 디렉토리가 root 소유인 경우가 일반적이라 root user 가 권한 일관성 면에서 단순.
#
# 빌드:    make claudegen-build
# 사용:    docker run -d --rm \
#            -v $HOME/.claude:/root/.claude \
#            -v $HOME/.claude.json:/root/.claude.json \
#            -v /tmp/workspace:/workspace \
#            issuetracker-claudegen:local tail -f /dev/null

FROM node:20-slim

# 시스템 의존성: ca-certificates (HTTPS), curl (디버그 용)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Claude Code CLI 전역 설치 (이슈 #269)
# - 버전 고정 (재현성 확보 — PR #270 리뷰 피드백)
# - npm root -g 로 실제 global 경로 동적 결정 (node:20-slim 은 /usr/local/lib/node_modules)
# - postinstall 스크립트가 native binary 를 다운로드하지 못하는 경우가 있어 install.cjs 명시 실행
# - 마지막에 claude --version 으로 smoke check — 실패 시 빌드 실패 (silent breakage 회피)
ARG CLAUDE_CODE_VERSION=2.1.128
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
    && INSTALL_CJS="$(npm root -g)/@anthropic-ai/claude-code/install.cjs" \
    && if [ -f "$INSTALL_CJS" ]; then node "$INSTALL_CJS"; \
       else echo "install.cjs not found at $INSTALL_CJS — native binary may already be present"; fi \
    && claude --version

# MCP postgres 서버 사전 설치 (이슈 #472)
# - enrich agent 가 read-only DB 직접 조회를 위해 사용
# - 매 세션 npx -y 로 호출하지만, 캐시 누락 시 콜드 스타트가 길어지므로 미리 설치하여 npx 가
#   캐시된 패키지를 즉시 실행하도록 준비. 버전 고정으로 재현성 확보.
ARG MCP_POSTGRES_VERSION=0.6.2
RUN npm install -g @modelcontextprotocol/server-postgres@${MCP_POSTGRES_VERSION} \
    && command -v mcp-server-postgres || echo "mcp-server-postgres binary not on PATH; will be resolved via npx"

# 인증 디렉토리 마운트 지점 (이슈 #266) — 호스트 ~/.claude 가 read-write 로 마운트됨
RUN mkdir -p /root/.claude

# 작업 디렉토리 — 호스트의 임시 workspace 가 마운트됨
WORKDIR /workspace

# 컨테이너는 docker exec 세션이 올 때까지 대기
ENTRYPOINT []
CMD ["tail", "-f", "/dev/null"]
