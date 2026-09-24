# codex 자체 Codex CLI 이미지 (이슈 #533 — 메타 #462 Sub 2)
#
# 공식 openai/codex-universal 이나 커뮤니티 이미지 대신 직접 빌드합니다 — 버전을 저장소가
# 통제해야 CLI 업데이트로 인한 무성한 동작 변화를 막을 수 있습니다.
# deployments/docker/claudegen/Dockerfile 의 패턴을 미러링합니다.
#
# 호스트의 ~/.codex/ 디렉토리를 마운트하여 사전 로그인한 자격으로 호출합니다.
# 기본 user 는 node (uid=1000, gid=1000) — node:22-bookworm-slim 이 제공하는 기본 user.
# 호스트 ~/.codex (uid 1000 소유) 와 컨테이너 node uid 1000 이 일치하므로 mount RW 권한 일관.
#
# 빌드:    make codex-build
# 사용:    docker run -d --rm \
#            -v $HOME/.codex:/home/node/.codex \
#            -v /tmp/workspace:/workspace \
#            issuetracker-codex:local tail -f /dev/null

FROM node:22-bookworm-slim

# 시스템 의존성: ca-certificates (HTTPS), curl (디버그 용)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Codex CLI 전역 설치
# - 버전 고정 (재현성 — claudegen 의 CLAUDE_CODE_VERSION 과 동일 정책)
# - 마지막에 codex --version 으로 smoke check — 실패 시 빌드 실패 (silent breakage 회피)
ARG CODEX_VERSION=0.156.1
RUN npm install -g @openai/codex@${CODEX_VERSION} \
    && codex --version

# 인증 디렉토리 마운트 지점 — 호스트 ~/.codex 가 read-write 로 마운트됨
# - node user (uid=1000) 가 마운트 후 RW 가능하도록 미리 ownership 부여
#   (Codex CLI 가 세션 상태를 이 디렉토리에 기록)
RUN mkdir -p /home/node/.codex && chown -R node:node /home/node/.codex

# 작업 디렉토리 — 호스트의 임시 workspace 가 마운트됨
RUN mkdir -p /workspace && chown -R node:node /workspace
WORKDIR /workspace

USER node

# 컨테이너는 docker exec 세션이 올 때까지 대기.
# ENTRYPOINT 를 비우는 이유: node 이미지의 docker-entrypoint.sh 가 CMD 를 가로채
# tail 이 그대로 실행되지 않는 것을 막기 위함 (claudegen 과 동일).
ENTRYPOINT []
CMD ["tail", "-f", "/dev/null"]
