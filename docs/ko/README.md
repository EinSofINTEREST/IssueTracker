# IssueTracker

한국어 | **[English](../../README.md)**

> 글로벌 이슈 수집 및 분석 시스템

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## 개요

IssueTracker 는 전 세계의 뉴스 / 커뮤니티 / 소셜 소스를 크롤링하고, 콘텐츠를 정규화/검증한 뒤, entity/claim/fact 추출과 cross-source verification 으로 **enrich** 하고, trust score 계산 및 이슈 클러스터링을 수행합니다. 파이프라인은 Kafka 기반이며, **하나의 binary 가 여러 stage** 로 동작합니다 (`make start`).

**초기 타겟 시장**: 미국과 대한민국.

## 주요 기능

- 🌐 **다중 소스 크롤링** — 뉴스 / 커뮤니티 / RSS / SPA (chromedp) handler chain
- 🔄 **Kafka 기반 5-stage 파이프라인** — fetcher / parser / validate / enrich / scheduler
- 🧠 **LLM 기반 셀렉터 자동 학습** — parser rule + path-pattern 정밀화 (claudegen 컨테이너 pool)
- 🔍 **4-stage enrichment** — entity 추출 → cross-verification → 외부 context → trust score
- 🗂️ **DB-driven rule** — `parser_rules` / `parser_blacklist` / `scheduler_entries` — 하드코딩 selector 없음
- 🛡️ **Auto-demote + blacklist** — index-only 휴리스틱 + LLM 기반 mode 분기 (`drop` / `extract_links_only`)
- ⚙️ **Stage toggle** — 같은 binary 를 fetcher-only / parser-only / enrich-only 노드로 분리 배포 (`STAGES_*_ENABLED`)
- 📊 **Prometheus 메트릭** — `/metrics` endpoint + 모듈별 counter
- ✅ **Graceful shutdown** — stage 단계 drain + claudegen 컨테이너 정리

## 상위 아키텍처

```
                          ┌────────────────────────┐
   Scheduler (DB-driven)  │  scheduler_entries     │
        │                 └────────────────────────┘
        ▼                                ▲
  Kafka: crawl.{high,normal,low,chromedp}
        │
        ▼
  Fetcher (PoolManager + handler chain: goquery / chromedp / browser / RSS)
        │
        ├─→ raw_contents (Claim Check)
        ▼
  Kafka: fetched
        │
        ▼
  Parser Worker  ── ParsePage / ParseLinks (rule engine, indexonly heuristic, auto-demote)
        │            ├─ llmgen.Generator (selector 자동 학습 + blacklist mode 분기)
        │            └─ refiner (path_pattern 정밀화)
        ▼
  Kafka: normalized
        │
        ▼
  Validate Worker (news / community)
        │
        ▼
  Kafka: validated
        │
        ▼
  Enrich Worker (4-stage: extract → verify → context → score)
        │  └─ claudegen 컨테이너 pool (pkg/agent/claude)
        │  └─ MCP postgres tool (enricher_ro read-only)
        ▼
  Kafka: enriched + enriched_contents 영속화 (trust_score, rationale, factors)
```

세부 컴포넌트 문서는 [`docs/architecture/`](../architecture/) 에 위치. 권장 읽기 순서:

1. [`cmd/issuetracker.md`](../architecture/cmd/issuetracker.md) — wiring & shutdown
2. [`internal/processor/README.md`](../architecture/internal/processor/README.md) — stage layer
3. [`internal/processor/parser/rule.md`](../architecture/internal/processor/parser/rule.md) — rule engine, indexonly, auto_demote, llmgen, refiner
4. [`internal/processor/enrich/README.md`](../architecture/internal/processor/enrich/README.md) — 4-stage enrichment
5. [`internal/processor/precheck.md`](../architecture/internal/processor/precheck.md) — URL 처리 가부 게이트
6. [`internal/storage/README.md`](../architecture/internal/storage/README.md) — repository / service / decorator layering
7. [`pkg/agent/claude.md`](../architecture/pkg/agent/claude.md) — claudegen 컨테이너 pool

## 빠른 시작

### 요구사항

- Go 1.24+
- PostgreSQL 15+
- Apache Kafka 4.x (KRaft 모드, Zookeeper 불필요)
- Redis 7+
- Docker (chromedp pool + claudegen 컨테이너용)

### 설치

```bash
git clone https://github.com/EinSofINTEREST/IssueTracker
cd IssueTracker
cp .env.example .env  # DB / API 키 / stage toggle 입력
make build            # 모든 binary 를 bin/ 에 빌드
make claudegen-build  # claudegen 컨테이너 이미지 (issuetracker-claudegen:local)
```

### 바이너리

| 바이너리 | Entry Point | 설명 |
|---|---|---|
| `bin/issuetracker` | `cmd/issuetracker/` | **통합 파이프라인** — fetcher + parser + validate + enrich + scheduler (운영 entry) |
| `bin/processor` | `cmd/processor/` | Validate processor 단독 |
| `bin/migrate` | `cmd/migrate/` | DB 마이그레이션 실행 (up) |
| `bin/migrate-down` | `cmd/migrate-down/` | DB 마이그레이션 롤백 |
| `bin/rule-validator` | `cmd/rule-validator/` | Parser rule selector 검증 도구 (dry-run) |

### 파이프라인 기동

```bash
# 1. 인프라 기동 (멱등 — 재실행 안전)
make pg-start          # PostgreSQL 컨테이너
make pg-migrate        # 마이그레이션 적용 (현재 031)
make kafka-start       # Kafka broker + UI (http://localhost:8080) + 토픽 초기화
make chrome-start      # chromedp 컨테이너 (worker_count 만큼)

# 2. 통합 파이프라인 실행
make start             # = chrome-ensure → kafka-ensure → run-issuetracker
                       # chromedp 동적 포트를 자동 발견하여 env 주입
```

`SIGINT`/`SIGTERM` 으로 종료 — graceful shutdown 이 모든 stage 를 drain 한 뒤 claudegen 컨테이너 pool 정리.

### Stage Toggle

같은 binary 를 부분 파이프라인 노드로 분리 실행:

```bash
# fetcher-only 노드
STAGES_FETCHER_ENABLED=true
STAGES_PARSER_ENABLED=false
STAGES_VALIDATE_ENABLED=false
STAGES_ENRICH_ENABLED=false
STAGES_SCHEDULER_ENABLED=false
```

Kafka consumer group 이 stage 간 라우팅을 담당 — 각 stage 는 자기 topic 만 consume.

## 프로젝트 구조

표준 Go 프로젝트 레이아웃:

```
issuetracker/
├── cmd/                            # 애플리케이션 entry → bin/
│   ├── issuetracker/               # 통합 파이프라인 (메인 entry)
│   ├── processor/                  # validate 전용
│   ├── migrate/                    # DB 스키마 마이그레이션 (up)
│   ├── migrate-down/               # DB 스키마 마이그레이션 (down)
│   └── rule-validator/             # parser rule dry-run 도구
│
├── internal/
│   ├── processor/
│   │   ├── processor.go            # Stage lifecycle interface
│   │   ├── fetcher/                # crawler pool + handler chain (goquery / chromedp / browser / RSS)
│   │   │   ├── core/
│   │   │   ├── handler/
│   │   │   ├── worker/
│   │   │   ├── domain/
│   │   │   ├── implementation/
│   │   │   └── rate_limiter/
│   │   ├── parser/
│   │   │   ├── types/              # Page / LinkItem / ContentParser interface
│   │   │   ├── rule/               # DB-driven rule engine
│   │   │   │   ├── indexonly/      # index-only 휴리스틱
│   │   │   │   ├── llmgen/         # LLM selector 자동 학습
│   │   │   │   ├── pathinfer/      # path_pattern 추론
│   │   │   │   └── refiner/        # path_pattern 정밀화
│   │   │   └── worker/             # Kafka consumer + Claim Check
│   │   ├── validate/               # news / community validator + worker
│   │   ├── enrich/                 # 4-stage: extract / verify / context / score
│   │   │   ├── core/               # interface + Noop*/Claudegen* 구현
│   │   │   └── worker/             # Kafka consumer
│   │   └── precheck/               # URL 처리 가부 게이트 (BlacklistSource 등)
│   ├── storage/                    # 7-layer 분리 (Phase 1/2)
│   │   ├── model/                  # 도메인 타입
│   │   ├── repository/             # CRUD interface
│   │   ├── primitive/              # InflightLocker 등
│   │   ├── decorator/              # timeout / cache invalidator
│   │   ├── service/                # 비즈니스 로직 (ContentService, BlacklistService, ParserRuleService, …)
│   │   ├── postgres/               # PostgreSQL 구현
│   │   └── redis/                  # Redis 구현 (lock, sliding window)
│   ├── locks/                      # ProcessingLock / IngestionLock (Redis)
│   ├── bus/                        # Kafka producer/consumer + retry scheduler
│   ├── scheduler/                  # DB-driven seed job emitter
│   ├── workerpool/                 # generic worker pool primitives
│   └── classifier/                 # external classifier gRPC/HTTP client
│
├── pkg/                            # 도메인 중립 공개 utility
│   ├── agent/                      # LLM agent adapter
│   │   └── claude/                 # claudegen 컨테이너 pool (parser llmgen + enrich 백엔드)
│   ├── config/                     # 6 sub-package (app / storage / fetcher / processor / llm / runtime)
│   ├── llm/                        # 다중 provider LLM 추상 + prompt loader
│   ├── logger/                     # zerolog 기반 구조화 logger
│   ├── metrics/                    # Prometheus registry + /metrics endpoint
│   ├── queue/                      # Kafka 추상 (producer / consumer / topic)
│   ├── redis/                      # Redis 클라이언트 wrapper
│   ├── links/                      # URL 정규화 + 추출
│   └── urlguard/                   # URL 허용/차단 술어
│
├── deployments/
│   └── docker/
│       ├── docker-compose.yml      # Kafka (KRaft) + chrome pool
│       └── claudegen/Dockerfile    # node:20-slim + claude-code + mcp-postgres (non-root)
│
├── migrations/                     # 031 SQL 마이그레이션 (up + down)
├── test/                           # 소스 트리 미러링
└── docs/
    ├── architecture/               # canonical architecture 문서
    ├── en/  └── ko/                # README 번역
```

### 주요 설계 결정

- **DB-driven rule** — Parser selector / blacklist / scheduler entry 모두 PostgreSQL (`parser_rules`, `parser_blacklist`, `scheduler_entries`) 에 영속. 사이트 추가는 SQL 변경, 코드 변경 X (이슈 #100, #482).
- **Claim Check 패턴** — Raw HTML 은 `raw_contents` 에 저장하고 Kafka 메시지에는 **row ID 만**. Parser worker 가 1 회 로드 후 즉시 Delete (이슈 #134).
- **Storage layering (Phase 1/2)** — `model/` (타입) → `repository/` (CRUD interface) → `decorator/` (timeout / cache invalidator) → `service/` (비즈니스 로직). Decorator chain 은 `service.New*` 가 자동 합성 (이슈 #430, #431).
- **Stage Gate Semaphore** — stage 별 `ProcessingLock` + `Semaphore` capacity 가 동일 URL 동시 처리 방지 + worker count 미만 동시성 보장.
- **Auto-blacklist** — `parser_blacklist` 자동 등록 경로 2종:
  - **휴리스틱 auto-demote** ([`rule/indexonly/`](../../internal/processor/parser/rule/indexonly/)) — ParsePage 성공했으나 index-only 일 때 → `mode='extract_links_only'`
  - **LLM 기반** ([`llmgen.BlacklistDecision`](../../internal/processor/parser/rule/llmgen/)) — LLM 이 페이지를 분류하면 → `mode='drop'` 또는 `'extract_links_only'`

## 운영 레퍼런스

### 빌드 & 테스트

```bash
make build               # 5 binary 전체
make claudegen-build     # claudegen 컨테이너 이미지

make test                # 모든 단위 테스트
make coverage            # 커버리지 리포트
make lint                # golangci-lint
make fmt                 # gofmt

make clean               # 빌드 산출물 제거
```

### 인프라

```bash
# PostgreSQL
make pg-start | pg-stop | pg-clean | pg-status | pg-psql
make pg-migrate          # 멱등 up
make pg-migrate-down     # 롤백 (주의)

# Kafka (KRaft)
make kafka-start         # broker + UI (http://localhost:8080) + topic init
make kafka-stop | kafka-clean
make kafka-status | kafka-topics | kafka-describe
make kafka-scale-partitions TOPIC=<t> PARTITIONS=<n>

# chromedp pool
make chrome-start | chrome-stop | chrome-status
make chrome-remote-urls  # 동적 ws:// URL 발견
```

### 실행

```bash
make start               # 통합 파이프라인 (운영 entry — 권장)
make run-issuetracker    # 동일, chrome/kafka 사전 체크 없음
make run-processor       # validate worker 단독

# 예제
make run-example
make run-kafka-pipeline  # in-memory mock, Kafka 불필요
```

### 설정

모든 env 변수는 `.env` 단일 소스. [`pkg/config`](../architecture/pkg/config.md) sub-package 별 로드:

| Prefix | Sub-package |
|---|---|
| `POSTGRES_*`, `REDIS_*` | `storage` |
| `KAFKA_*` | (via `pkg/queue`) |
| `LLM_*`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY` | `llm` |
| `CLAUDE_CODE_*` | `pkg/agent/claude` |
| `FETCHER_*`, `STAGES_*_ENABLED`, `STAGES_*_WORKER_COUNT` | `fetcher` / `runtime` |
| `BLACKLIST_*`, `VALIDATE_*`, `SCHEDULER_*` | `processor` |
| `ENRICHER_DB_RO_*` | enrich MCP postgres (이슈 #472) |
| `METRICS_ADDR`, `LOG_*`, `SHUTDOWN_TIMEOUT` | `app` |

전체 키 목록은 [`.env.example`](../../.env.example) 참조.

## 개발

### Workflow

개발 규약은 [`.claude/rules/`](../../.claude/rules/) 에 정리:

1. [01-architecture.md](../../.claude/rules/01-architecture.md) — 시스템 아키텍처
2. [02-crawler-implementation.md](../../.claude/rules/02-crawler-implementation.md) — fetcher 표준
3. [03-data-processing.md](../../.claude/rules/03-data-processing.md) — 처리 파이프라인
4. [04-error-handling.md](../../.claude/rules/04-error-handling.md) — 에러 처리 & 모니터링
5. [05-testing.md](../../.claude/rules/05-testing.md) — 테스트 전략
6. [06-code-style.md](../../.claude/rules/06-code-style.md) — 코드 컨벤션
7. [07-workflow.md](../../.claude/rules/07-workflow.md) — issue-first / commit-per-TODO / 자율 진행

### 코드 스타일

- Go formatting: `gofmt -w .` (tab)
- 주석은 한국어 / 영어 혼용 (한국어 우선)
- 한국어 커밋 메시지 + 카테고리 prefix: `[FEAT]`, `[FIX]`, `[REFAC]`, `[DOCS]`, `[CHORE]`
- PR 제목: `[카테고리#이슈번호] 설명` (CI 강제)

### CI / 머지 게이트

Required status checks ([`docs/ci/`](../ci/) 참조):
- Commit Lint / PR Title Lint / Linked Issue Check
- Format Check / Build / Test / Lint

## 문서

- **Canonical**: [`docs/architecture/`](../architecture/) — 패키지별 설계 문서
- **운영**: [`docs/ci/`](../ci/) — CI 컨벤션 + required status check
- **Workflow**: [`.claude/rules/07-workflow.md`](../../.claude/rules/07-workflow.md) — AI / 사람 협업 규약
- **번역**: [`docs/ko/`](.) — 한국어 (영어판과 동기화)

## 기여

[`.claude/rules/07-workflow.md`](../../.claude/rules/07-workflow.md) 의 issue-first workflow 및 labeling 정책을 먼저 읽어주세요.

## 라이선스

MIT

## 연락처

문의 / 피드백은 GitHub issue 로 부탁드립니다.
