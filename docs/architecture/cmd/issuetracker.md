# cmd/issuetracker — Integrated Pipeline

소스: [`cmd/issuetracker/main.go`](../../../cmd/issuetracker/main.go)
산출물: `bin/issuetracker` (`make build`)

전체 파이프라인 (Fetcher + Parser + Validator + Enricher + Scheduler + Refiner) 을 **단일 프로세스에서 wire** 하는
오케스트레이터. 운영 시 권장 entry point.

<br>

## 역할

- `main()` 은 오직 **wiring 다이어그램** — 비즈니스 로직 0
- 각 단계는 동일 `context.Context` 를 공유 → SIGTERM 시 일괄 cancel
- Redis / LLM 은 fail-soft — 부재 시 noop fallback 으로 graceful degrade
- Stage toggle (이슈 #443) 로 한 바이너리에서 일부 단계만 기동 가능

<br>

## Wiring 흐름 (코드 순서)

```
1. logger / shutdown / metrics endpoint   (pkg/logger, pkg/config/app, pkg/metrics)
2. stage toggle 로드                       (runtimecfg.LoadStages — fetcher/parser/validate/enrich/scheduler, 이슈 #443)
3. Kafka producer + 3-tier consumers       (high/normal/low)
4. PostgreSQL pool                         (TimedPool 데코레이터, 이슈 #427)
5. parser_rules + Resolver                 (RuleLookup interface, 이슈 #463)
6. parser_blacklist Service / Matcher      (BlacklistService + BlacklistMatcher, 이슈 #295/#297/#431)
7. rule.Parser + WithBlacklistAutoDemote   (#477 — blacklistSvc 있을 때만 옵션 wiring)
8. raw_contents Service / Content Service  (Claim Check)
9. Source 등록                              (kr.Register / us.Register → handler.Registry)
10. Redis: ProcessingLock / IngestionLock / DelayedRetryScheduler (이슈 #178, #82)
11. PoolManager (3-tier crawler workers)
12. LLM provider (chain policy, 이슈 #149)
13. ParserWorker (consumer group: parsers)
14. Refiner (path_pattern 정밀화, 이슈 #173 단계 4-2)
15. RawContentCleaner cron                 (Claim Check 잔존물 정리)
16. Scheduler + BacklogThrottler           (이슈 #124)
17. ValidateWorker (consumer group: validators)
18. EnrichWorker (consumer group: enrichers, 이슈 #446 ~ #450)
19. SIGTERM 대기 → 역순 graceful shutdown
```

각 단계는 `runtimecfg.StagesConfig` 의 `*Enabled` 가 false 면 skip — 같은 바이너리를 fetcher-only / parser-only / 등으로 분리 배포 가능 (이슈 #443).

각 단계의 자세한 책임은 해당 패키지 문서 참조:

- [internal/processor/fetcher/worker.md](../internal/processor/fetcher/worker.md) — PoolManager, RetryScheduler
- [internal/locks/README.md](../internal/locks/README.md) — ProcessingLock, IngestionLock
- [internal/processor/parser/rule.md](../internal/processor/parser/rule.md) — rule.Parser, indexonly, auto_demote, llmgen, refiner, blacklist matcher
- [internal/processor/parser/README.md](../internal/processor/parser/README.md) — ParserWorker (Claim Check)
- [internal/processor/validate.md](../internal/processor/validate.md) — Validator
- [internal/processor/enrich/README.md](../internal/processor/enrich/README.md) — EnrichWorker (4-stage pipeline, 이슈 #446 ~ #450)
- [internal/processor/precheck.md](../internal/processor/precheck.md) — URL 처리 가부 게이트 (이슈 #425)
- [internal/scheduler.md](../internal/scheduler.md) — seed job 발행
- [pkg/agent/claude.md](../pkg/agent/claude.md) — claudegen 컨테이너 (parser_llmgen + enrich LLM 백엔드)

<br>

## Build helpers (main.go 내부)

| 함수 | 책임 | 실패 동작 |
|---|---|---|
| `buildLLMProvider()` | `LLM_ENABLED` + API key 검증 → [`pkg/llm`](../../../pkg/llm/) chain provider 구성 | nil 반환 (LLM 비활성) |
| `buildLLMGenerator()` | provider nil 이면 nil — 아니면 [`llmgen.New`](../../../internal/processor/parser/rule/llmgen/) | nil 허용 |
| `buildRefiner()` | `REFINEMENT_ENABLED` + provider 옵션 결합 → [`refiner.New`](../../../internal/processor/parser/rule/refiner/) | nil 반환 (정밀화 비활성) |
| `buildClaudegenPool()` | claudegen worker pool (parser llmgen + enrich 공용 백엔드, 이슈 #458/#460) | nil 반환 (LLM 비활성 시) |
| `buildEnricherROMCPConfig()` | `ENRICHER_DB_RO_*` env → MCP postgres tool 구성 (이슈 #472) | nil 허용 (env 미설정 시 MCP 비활성) |

이슈 #482 (PR #483) 머지 후 부팅 시 `parser_rules` 시드 검증 (`verifyParsingRulesSeeded` / `rule.VerifySeeded`) 은 **제거됨** — DB-driven 메타데이터 관리 신뢰. parser_rules row 부재는 호출별 `ErrNoRule` 진단으로 위임.

<br>

## 환경 변수 의존

config 로딩은 [`pkg/config`](../pkg/config.md) sub-package 분리 (이슈 #440). 주요 키:

| Loader | 패키지 | 결정 항목 | 비활성 시 |
|---|---|---|---|
| `app.LoadLog` | `pkg/config/app` | 로그 레벨 / Pretty 출력 | 기본값 |
| `app.LoadMetrics` | `pkg/config/app` | `METRICS_ADDR` (default `:9090`) | 빈 값이면 endpoint 미기동 |
| `app.LoadShutdown` | `pkg/config/app` | overall + claudegen shutdown timeout | 기본값 |
| `storage.Load` | `pkg/config/storage` | PostgreSQL 접속 | **fail-fast** |
| `storage.LoadRedis` | `pkg/config/storage` | Redis 접속 | warn 후 NoopProcessingLock fallback |
| `llm.LoadLLM` | `pkg/config/llm` | provider, model, API key, timeout | nil provider |
| `llm.LoadPathInfer` | `pkg/config/llm` | refiner interval / min samples | nil refiner |
| `processor.LoadScheduler` | `pkg/config/processor` | seed entry interval / backlog 임계값 | **fail-fast** |
| `processor.LoadValidate` | `pkg/config/processor` | 검증 임계값 | **fail-fast** |
| `processor.LoadBlacklist` | `pkg/config/processor` | parser_blacklist matcher 활성 | warn — Matcher 미주입 (기능 OFF) |
| `processor.LoadStaleRelearn` | `pkg/config/processor` | stale rule 자동 재학습 | warn — 기능 OFF |
| `fetcher.LoadChromedpPool` | `pkg/config/fetcher` | chromedp pool / remote URLs / worker count | **fail-fast** (pool 활성 시 remote URL 부재) |
| `fetcher.LoadAutoUpgrade` | `pkg/config/fetcher` | goquery → chromedp 자동 승격 | warn — auto-upgrade 비활성 |
| `runtime.LoadStages` | `pkg/config/runtime` | **fetcher/parser/validate/enrich/scheduler 단계 toggle** (이슈 #443) | **모두 false 시 fail-fast** |
| `runtime.LoadWorkerCounts` | `pkg/config/runtime` | 단계별 worker pool 크기 | 기본값 |
| `runtime.LoadStageGate` | `pkg/config/runtime` | ProcessingLock + Semaphore 임계값 | 기본값 |

자세한 env 키 매트릭스는 [`pkg/config.md`](../pkg/config.md) 참조. 전체 키 목록은 [`.env.example`](../../../.env.example) 가 단일 소스.

<br>

## Worker 카운트 (runtime.WorkerCountsConfig)

`STAGES_*_WORKER_COUNT` env 로 단계별 조정 (이슈 #443/runtime):

```
FETCHER_HIGH_WORKER_COUNT=3     # high-priority 작업
FETCHER_NORMAL_WORKER_COUNT=6   # 일반 article fetch
FETCHER_LOW_WORKER_COUNT=2      # background backfill
PARSER_WORKER_COUNT=10
VALIDATE_WORKER_COUNT=6
ENRICH_WORKER_COUNT=4
```

- `*_WORKER_COUNT` ≤ 해당 Kafka 토픽 파티션 수 (validate=32, enrich=16 등)
- chromedp pool 은 별도 `FETCHER_CHROMEDP_WORKER_COUNT` (chrome 컨테이너 수와 동기)

<br>

## Stage Toggle (이슈 #443)

같은 바이너리를 stage 별로 분리 배포할 때 사용:

```bash
# fetcher-only 노드
STAGES_FETCHER_ENABLED=true
STAGES_PARSER_ENABLED=false
STAGES_VALIDATE_ENABLED=false
STAGES_ENRICH_ENABLED=false
STAGES_SCHEDULER_ENABLED=false
```

Kafka consumer group 이 stage 간 결합을 담당 (각 stage 가 자기 topic 만 consume). 모든 stage false 는 `LoadStages` 가 에러로 거부.

<br>

## Graceful Shutdown 순서

SIGINT/SIGTERM 수신 시:

1. logger 에 `shutting_down=true` 부착 (이슈 #72)
2. root `cancel()` — 모든 goroutine 의 ctx 무효화
3. shutdownCtx (`SHUTDOWN_TIMEOUT`, default 30s) 로 stages 역순 Stop:
   - `sched.Stop()`
   - `manager.Stop(shutdownCtx)` — drain crawler workers
   - `parserWorker.Stop(shutdownCtx)` — Parser.WaitAutoDemote 도 함께 호출 (이슈 #477)
   - `validateWorker.Stop(shutdownCtx)`
   - `enrichWorker.Stop(shutdownCtx)` — claudegen pool drain
   - `llmGen.Stop(shutdownCtx)` — in-flight LLM call drain (이슈 #149)
   - `pathRefiner.Stop(shutdownCtx)` — refiner cycle drain (PR #191)
   - `cleaner.Stop()` — cleanup cron
4. cleanupCtx (`CLAUDE_CODE_SHUTDOWN_TIMEOUT`, default 10s) 로 claudegen 정리 — shutdownCtx 와 분리하여 stages.Stop 이 timeout 으로 cancel 되더라도 `docker rm -f` 가 반드시 시도되도록 보장:
   - `claudegenPool.Stop(cleanupCtx)` — claudegen 컨테이너 정리 (이슈 #458)
5. `defer` 체인이 Kafka producer/consumer, redis, pgpool 닫음

<br>

## 관련 이슈

- 이슈 #82 — IngestionLock (Publisher 직전)
- 이슈 #124 — Scheduler + BacklogThrottler
- 이슈 #149 — LLM Generator
- 이슈 #178 — ProcessingLock 단계 prefix
- 이슈 #295/#297 — parser_blacklist matcher
- 이슈 #427 — TimedPool decorator
- 이슈 #431 — BlacklistService / ParserRuleService 분리
- 이슈 #439/#440 — pkg/config 검증 helper + sub-package 분리
- 이슈 #443 — stage toggle env flags
- 이슈 #446 ~ #450 — Enrich worker (extract / cross_verify / context / score)
- 이슈 #457 — enriched_contents.rationale + factors 컬럼화
- 이슈 #458 — claudegen → pkg/agent/claude namespace
- 이슈 #463 — RuleLookup interface
- 이슈 #472 — enrich MCP postgres
- 이슈 #474 — claudegen 컨테이너 user non-root
- 이슈 #477 — ParsePage 결과 index-only 자동 강등 wiring
- 이슈 #480 — LLM auto-blacklist mode 분기
- 이슈 #482 — `verifyParsingRulesSeeded` 폐기 (부팅 시 DB seed 검증 제거)
