# IssueTracker

**[한국어](docs/ko/README.md)** | English

> Global Issue Collection and Analysis System

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Overview

IssueTracker crawls news / community / social sources worldwide, normalizes and validates the content, **enriches** it with extracted entities/claims/facts and cross-source verification, then scores trust and clusters issues. The pipeline is fully Kafka-driven and operates as **one binary, multiple stages** (`make start`).

**Initial Target Markets**: United States and South Korea.

## Features

- 🌐 **Multi-source crawling** — news / community / RSS / SPA (chromedp) handler chain
- 🔄 **Kafka-driven 5-stage pipeline** — fetcher / parser / validate / enrich / scheduler
- 🧠 **LLM-backed selector auto-learning** — parser rules + path-pattern refinement (claudegen container pool)
- 🔍 **4-stage enrichment** — entity extraction → cross-verification → external context → trust score
- 🗂️ **DB-driven rules** — `parser_rules` / `parser_blacklist` / `scheduler_entries` — no hardcoded selectors
- 🛡️ **Auto-demote & blacklist** — index-only heuristic + LLM-based mode-aware blacklist (`drop` / `extract_links_only`)
- ⚙️ **Stage toggle** — run the same binary as fetcher-only / parser-only / enrich-only nodes (`STAGES_*_ENABLED`)
- 📊 **Prometheus metrics** — `/metrics` endpoint + per-module counters
- ✅ **Graceful shutdown** — staged drain with claudegen container cleanup

## High-level Architecture

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
        │            ├─ llmgen.Generator (selector auto-learning + blacklist mode)
        │            └─ refiner (path_pattern refinement)
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
        │  └─ claudegen container pool (pkg/agent/claude)
        │  └─ MCP postgres tool (enricher_ro read-only)
        ▼
  Kafka: enriched + enriched_contents persistence (trust_score, rationale, factors)
```

Detailed component docs live under [`docs/architecture/`](docs/architecture/). Suggested reading order:

1. [`cmd/issuetracker.md`](docs/architecture/cmd/issuetracker.md) — wiring & shutdown
2. [`internal/processor/README.md`](docs/architecture/internal/processor/README.md) — stage layer
3. [`internal/processor/parser/rule.md`](docs/architecture/internal/processor/parser/rule.md) — rule engine, indexonly, auto_demote, llmgen, refiner
4. [`internal/processor/enrich/README.md`](docs/architecture/internal/processor/enrich/README.md) — 4-stage enrichment
5. [`internal/processor/precheck.md`](docs/architecture/internal/processor/precheck.md) — URL processing gate
6. [`internal/storage/README.md`](docs/architecture/internal/storage/README.md) — repository / service / decorator layering
7. [`pkg/agent/claude.md`](docs/architecture/pkg/agent/claude.md) — claudegen container pool

## Quick Start

### Prerequisites

- Go 1.22+
- PostgreSQL 15+
- Apache Kafka 4.x (KRaft mode, no Zookeeper)
- Redis 7+
- Docker (for chromedp pool + claudegen container)

### Installation

```bash
git clone https://github.com/EinSofINTEREST/IssueTracker
cd IssueTracker
cp .env.example .env  # then fill in DB / API keys / stage toggles
make build            # builds all binaries to bin/
make claudegen-build  # builds the claudegen container image (issuetracker-claudegen:local)
```

### Binaries

| Binary | Entry Point | Description |
|---|---|---|
| `bin/issuetracker` | `cmd/issuetracker/` | **Integrated pipeline** — fetcher + parser + validate + enrich + scheduler (운영 entry) |
| `bin/processor` | `cmd/processor/` | Validate processor standalone |
| `bin/migrate` | `cmd/migrate/` | Run DB migrations (up) |
| `bin/migrate-down` | `cmd/migrate-down/` | Rollback DB migrations |
| `bin/rule-validator` | `cmd/rule-validator/` | Parser rule selector validator (dry-run tool) |

### Bringing the pipeline up

```bash
# 1. Bring up infra (idempotent — safe to re-run)
make pg-start          # PostgreSQL container
make pg-migrate        # apply migrations (currently 031)
make kafka-start       # Kafka broker + UI (http://localhost:8080) + topic init
make chrome-start      # chromedp containers (worker_count replicas)

# 2. Run the integrated pipeline
make start             # = chrome-ensure → kafka-ensure → run-issuetracker
                       # auto-discovers chromedp remote URLs and injects them
```

Stop the pipeline with `SIGINT`/`SIGTERM` — graceful shutdown drains all stages then cleans up the claudegen container pool.

### Stage Toggle

Run the same binary as a partial-pipeline node:

```bash
# fetcher-only node
STAGES_FETCHER_ENABLED=true
STAGES_PARSER_ENABLED=false
STAGES_VALIDATE_ENABLED=false
STAGES_ENRICH_ENABLED=false
STAGES_SCHEDULER_ENABLED=false
```

Kafka consumer groups handle the inter-stage routing; each stage just consumes its own topic.

## Project Structure

Standard Go project layout:

```
issuetracker/
├── cmd/                            # Application entry points → bin/
│   ├── issuetracker/               # integrated pipeline (main entry)
│   ├── processor/                  # validate-only
│   ├── migrate/  ├── migrate-down/ # DB schema migrations
│   └── rule-validator/             # parser rule dry-run tool
│
├── internal/
│   ├── processor/
│   │   ├── processor.go            # Stage lifecycle interface
│   │   ├── fetcher/                # crawler pool + handler chain (goquery / chromedp / browser / RSS)
│   │   │   ├── core/   handler/   worker/   domain/   implementation/
│   │   │   └── rate_limiter/
│   │   ├── parser/
│   │   │   ├── types/              # Page / LinkItem / ContentParser interfaces
│   │   │   ├── rule/               # DB-driven rule engine
│   │   │   │   ├── indexonly/      # index-only heuristic
│   │   │   │   ├── llmgen/         # LLM selector auto-learning
│   │   │   │   ├── pathinfer/      # path_pattern inference
│   │   │   │   └── refiner/        # path_pattern refinement
│   │   │   └── worker/             # Kafka consumer + Claim Check
│   │   ├── validate/               # news / community validators + worker
│   │   ├── enrich/                 # 4-stage: extract / verify / context / score
│   │   │   ├── core/               # interfaces + Noop*/Claudegen* implementations
│   │   │   └── worker/             # Kafka consumer
│   │   └── precheck/               # URL processing gate (BlacklistSource etc.)
│   ├── storage/                    # 7-layer separation (Phase 1/2)
│   │   ├── model/                  # domain types
│   │   ├── repository/             # CRUD interfaces
│   │   ├── primitive/              # InflightLocker etc.
│   │   ├── decorator/              # timeout / cache invalidator
│   │   ├── service/                # business logic (ContentService, BlacklistService, ParserRuleService, ...)
│   │   ├── postgres/               # PostgreSQL implementations
│   │   └── redis/                  # Redis implementations (locks, sliding window)
│   ├── locks/                      # ProcessingLock / IngestionLock (Redis)
│   ├── bus/                        # Kafka producer/consumer with retry scheduler
│   ├── publisher/                  # Crawl job publisher (precheck gate consumer)
│   ├── scheduler/                  # DB-driven seed job emitter
│   ├── workerpool/                 # generic worker pool primitives
│   └── classifier/                 # external classifier gRPC/HTTP client
│
├── pkg/                            # Public, domain-neutral utilities
│   ├── agent/                      # LLM agent adapters
│   │   └── claude/                 # claudegen container pool (parser llmgen + enrich backend)
│   ├── config/                     # 6 sub-packages (app / storage / fetcher / processor / llm / runtime)
│   ├── llm/                        # multi-provider LLM abstraction + prompt loader
│   ├── logger/                     # zerolog-based structured logger
│   ├── metrics/                    # Prometheus registry + /metrics endpoint
│   ├── queue/                      # Kafka abstraction (producer / consumer / topics)
│   ├── redis/                      # Redis client wrapper
│   ├── links/                      # URL normalization + extraction
│   └── urlguard/                   # URL allow/deny predicates
│
├── deployments/
│   └── docker/
│       ├── docker-compose.yml      # Kafka (KRaft) + chrome pool
│       └── claudegen/Dockerfile    # node:20-slim + claude-code + mcp-postgres (non-root)
│
├── migrations/                     # 031 SQL migrations (up + down)
├── test/                           # mirrors source tree
└── docs/
    ├── architecture/               # canonical architecture docs
    ├── en/  └── ko/                # README translations
```

### Key Design Choices

- **DB-driven rules** — Parser selectors / blacklist / scheduler entries all live in PostgreSQL (`parser_rules`, `parser_blacklist`, `scheduler_entries`). Site additions are SQL changes, not code (이슈 #100, #482).
- **Claim Check pattern** — Raw HTML is stored in `raw_contents` and **only the row ID** travels through Kafka. Parser worker loads it once, deletes after success (이슈 #134).
- **Storage layering (Phase 1/2)** — `model/` (types) → `repository/` (CRUD interfaces) → `decorator/` (timeout / cache invalidator) → `service/` (business logic). Decorator chain is auto-composed by `service.New*` (이슈 #430, #431).
- **Stage Gate Semaphore** — Per-stage `ProcessingLock` + `Semaphore` capacity prevents same-URL concurrent work and bounds concurrency below worker count.
- **Auto-blacklist** — Two paths register to `parser_blacklist`:
  - **Heuristic auto-demote** ([`rule/indexonly/`](internal/processor/parser/rule/indexonly/)) — when ParsePage succeeds but content is index-only → `mode='extract_links_only'`
  - **LLM-based** ([`llmgen.BlacklistDecision`](internal/processor/parser/rule/llmgen/)) — when LLM classifies the page → `mode='drop'` or `'extract_links_only'`

## Operations Reference

### Build & Test

```bash
make build               # all 5 binaries
make claudegen-build     # claudegen container image

make test                # all unit tests
make coverage            # coverage report
make lint                # golangci-lint
make fmt                 # gofmt

make clean               # remove build artifacts
```

### Infrastructure

```bash
# PostgreSQL
make pg-start | pg-stop | pg-clean | pg-status | pg-psql
make pg-migrate          # idempotent up
make pg-migrate-down     # rollback (use carefully)

# Kafka (KRaft)
make kafka-start         # broker + UI (http://localhost:8080) + topic init
make kafka-stop | kafka-clean
make kafka-status | kafka-topics | kafka-describe
make kafka-scale-partitions TOPIC=<t> PARTITIONS=<n>

# chromedp pool
make chrome-start | chrome-stop | chrome-status
make chrome-remote-urls  # discover dynamic ws:// URLs
```

### Running

```bash
make start               # integrated pipeline (운영 entry — preferred)
make run-issuetracker    # same, no chrome/kafka pre-check
make run-processor       # validate worker only

# examples
make run-example
make run-kafka-pipeline  # in-memory mock, no Kafka required
```

### Configuration

All env vars live in `.env` (single source). Loaded via [`pkg/config`](docs/architecture/pkg/config.md) sub-packages:

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

See [`.env.example`](.env.example) for the canonical list.

## Development

### Workflow

Development conventions are codified in [`.claude/rules/`](.claude/rules/):

1. [01-architecture.md](.claude/rules/01-architecture.md) — system architecture
2. [02-crawler-implementation.md](.claude/rules/02-crawler-implementation.md) — fetcher standards
3. [03-data-processing.md](.claude/rules/03-data-processing.md) — processing pipeline
4. [04-error-handling.md](.claude/rules/04-error-handling.md) — error handling & monitoring
5. [05-testing.md](.claude/rules/05-testing.md) — testing strategy
6. [06-code-style.md](.claude/rules/06-code-style.md) — code conventions
7. [07-workflow.md](.claude/rules/07-workflow.md) — issue-first / commit-per-TODO / autonomous progression

### Code Style

- Go formatting: `gofmt -w .` (tabs)
- Korean / English mix in comments (Korean primary)
- Korean commit messages with category prefix: `[FEAT]`, `[FIX]`, `[REFAC]`, `[DOCS]`, `[CHORE]`
- PR titles: `[CATEGORY#ISSUE] description` (CI-enforced)

### CI / Merge Gates

Required status checks (see [`docs/ci/`](docs/ci/)):
- Commit Lint / PR Title Lint / Linked Issue Check
- Format Check / Build / Test / Lint

## Documentation

- **Canonical** : [`docs/architecture/`](docs/architecture/) — per-package design docs
- **Operations** : [`docs/ci/`](docs/ci/) — CI conventions + required status checks
- **Workflow** : [`.claude/rules/07-workflow.md`](.claude/rules/07-workflow.md) — AI / human collaboration conventions
- **Translations** : [`docs/ko/`](docs/ko/) — Korean (synced with English)

## Contributing

Please read [`.claude/rules/07-workflow.md`](.claude/rules/07-workflow.md) for the issue-first workflow and labeling policy before contributing.

## License

MIT

## Contact

For questions or feedback, please open an issue on GitHub.
