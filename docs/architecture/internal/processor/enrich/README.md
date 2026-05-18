# internal/processor/enrich — 4-Stage Enrichment Pipeline

소스: [`internal/processor/enrich/`](../../../../../internal/processor/enrich/)

\`validated\` 토픽의 article 을 입력으로 받아 entity / claim / fact 추출 → cross-verification → external context → trust score 의 4 단계 enrichment 를 수행하고 \`enriched\` 토픽으로 발행 + \`enriched_contents\` 테이블 영속화.

이슈 #445 ~ #450 으로 도입된 enrich subsystem. claudegen ([`pkg/agent/claude`](../../../pkg/agent/claude.md)) 를 LLM 백엔드로, MCP postgres tool (이슈 #472) 을 read-only DB 액세스로 사용.

<br>

## 패키지 구조

| 디렉토리 | 책임 |
|---|---|
| [`enrich.go`](../../../../../internal/processor/enrich/enrich.go) | 모듈 entry — Worker 생성 (compose) |
| [`core/`](../../../../../internal/processor/enrich/core/) | 도메인 모델 + 4단계 interface + claudegen 구현체 (extract / verify / context / score) — 이슈 #460 (stage 별 agent 사용 OOP 분리) |
| [`worker/`](../../../../../internal/processor/enrich/worker/) | Kafka consumer + 4 단계 직렬 실행 + 영속화 + DLQ |

<br>

## 4-stage Pipeline

\`validated\` 토픽의 단일 article (Content + ContentRef) 가 다음 순서로 처리됩니다:

```
Kafka(issuetracker.validated)
  ↓ Worker.processMessage (claim check 로드)
  ↓ ProcessingLock.Acquire("enrich", url)
  ↓
  ├─ Stage 1: Extractor.Extract(content, html)       — 이슈 #447
  │     → EnrichedFacts (entities[], claims[], facts[], topics[], sentiment)
  │     → 실패 시 forwarding without facts (graceful fallback)
  │
  ├─ Stage 2: Verifier.Verify(content, facts)        — 이슈 #448
  │     → []Verification {claim_idx, verdict, sources, note}
  │     → MCP postgres + WebFetch 로 claim 별 cross-verify
  │     → 실패 시 forwarding without verifications
  │
  ├─ Stage 3: Contextualizer.Contextualize(...)      — 이슈 #449
  │     → PageContext (background[], timeline[], implications)
  │     → MCP postgres + WebFetch 로 entity 별 external background 수집
  │     → 실패 시 forwarding without context
  │
  └─ Stage 4: Scorer.Score(facts, verifications, context) — 이슈 #450
        → TrustScoreResult (trust_score, rationale, factors)
        → factors: claim_support_ratio / source_diversity / context_completeness
        → 실패 시 forwarding without trust_score (graceful)

  ↓ EnrichedContentRepository.Upsert (이슈 #450, rationale/factors 컬럼: #457)
  ↓ Producer.Publish(TopicEnriched, ContentRef)
  ↓ Kafka.CommitMessages
```

**graceful fallback 정책**: 단계별 실패는 비-fatal — 부분 결과만 누락된 채 다음 단계 + DB upsert 가 진행. 운영 시 `forwarding without {facts|verifications|context|trust_score}` WARN 로그로 가시화.

<br>

## core 패키지의 4 interface + 구현체

[`core/`](../../../../../internal/processor/enrich/core/):

```go
// 4 단계 각각의 입력/출력 타입과 인터페이스
type Extractor interface {
    Extract(ctx, in Input) (*EnrichedFacts, error)
}
type Verifier interface {
    Verify(ctx, in VerifyInput) ([]Verification, error)
}
type Contextualizer interface {
    Contextualize(ctx, in ContextInput) (*PageContext, error)
}
type Scorer interface {
    Score(ctx, in ScoreInput) (*TrustScoreResult, error)
}
```

각 인터페이스마다 **두 구현체**:

- **`Noop*`** — stage toggle 비활성 / LLM 비활성 fallback (이슈 #446)
- **`Claudegen*`** — `pkg/agent/claude` 의 [`SessionRunner = agent.Agent`](../../../pkg/agent/claude.md) 를 통해 claude CLI 호출

예: `NewClaudegenExtractor(runner, loader)` — claudegen worker pool 의 `agent.Agent` 와 prompt loader 를 받아 enrich 프롬프트 ([`pkg/llm/prompt/assets/enrich/claude/extract.user.txt`](../../../../../pkg/llm/prompt/assets/enrich/claude/extract.user.txt)) 로 LLM 호출. 결과 JSON 을 `parseEnrichOutput` 으로 unmarshal.

<br>

## 프롬프트 contract (이슈 #486)

`pkg/llm/prompt/assets/enrich/claude/*.user.txt` 4 개 파일:

| 단계 | 프롬프트 파일 | 핵심 schema |
|---|---|---|
| extract | `extract.user.txt` | `{entities, claims, facts, topics, sentiment}` |
| verify | `cross_verify.user.txt` | `{verifications: [{claim_idx, verdict, sources, note}]}` |
| context | `context.user.txt` | `{background, timeline, implications}` |
| score | `score.user.txt` | `{trust_score, rationale, factors}` |

각 프롬프트에 **CRITICAL OUTPUT CONTRACT** 블록이 상단에 명시 (이슈 #486 / PR #487):
- 응답은 raw JSON object 만 (first char `{`, last char `}`)
- 금지 시작 단어 명시 (`"I"`, `"The"`, `"Title"`, `"Based on"`, `"Sure"`, `"Okay"`, `"Here"`, `"Let me"`)
- markdown fence 금지
- 거부 응답 대신 schema-conformant empty defaults 반환
- 위반 시 응답 DISCARD 됨을 LLM 에 통지 (helpfulness 본능 억제)

<br>

## EnrichedContents 영속화 (이슈 #450, #457)

`enriched_contents` 테이블 스키마 (migration 029 + 030):

```sql
id              BIGSERIAL  PRIMARY KEY
content_id      VARCHAR(255) NOT NULL  -- contents.id 참조 (FK 없음)
trust_score     NUMERIC(4,3) NOT NULL  -- 0.0 ~ 1.0
facts           JSONB        NOT NULL  -- entities + claims + facts + topics + sentiment
verifications   JSONB        NOT NULL  -- [{claim_idx, verdict, sources, note}, ...]
context         JSONB        NOT NULL  -- background + timeline + implications
enriched_at     TIMESTAMPTZ  NOT NULL  DEFAULT NOW()
rationale       TEXT         NOT NULL  DEFAULT ''  -- 이슈 #457 — scorer 진단
factors         JSONB        NOT NULL  DEFAULT '{}' -- 이슈 #457 — claim_support_ratio 등
```

Upsert by `content_id` — 동일 article 재enrich 시 갱신. 부분 실패 (facts 만 / verifications 만) 도 partial upsert 로 진행.

<br>

## MCP postgres tool (이슈 #472)

cross-verify / context 단계에서 claudegen 에 read-only DB 접근 권한 부여. `enricher_ro` PostgreSQL role 이 `contents` / `enriched_contents` SELECT 만 허용.

```bash
# .env
ENRICHER_DB_RO_HOST=localhost
ENRICHER_DB_RO_PORT=5432
ENRICHER_DB_RO_DATABASE=main
ENRICHER_DB_RO_USER=enricher_ro
ENRICHER_DB_RO_PASSWORD=enricher_ro_dev_pw
```

`cmd/issuetracker.go` 의 `buildEnricherROMCPConfig` 가 본 env 를 `mcp.json` 으로 변환 → claudegen container 의 `--mcp-config` 인자로 mount. LLM 은 prompt 내에서 `mcp__issuetracker_ro__query` tool 로 SELECT 가능 (statement_timeout 5s).

미설정 시 MCP tool 없이 WebFetch / WebSearch 만으로 verify / context 진행 — graceful degrade.

<br>

## Worker (Kafka consumer)

[`worker/worker.go`](../../../../../internal/processor/enrich/worker/worker.go):

```go
type Worker struct {
    consumer  *kafka.Consumer        // issuetracker.validated
    producer  *kafka.Producer        // issuetracker.enriched
    extractor core.Extractor
    verifier  core.Verifier
    contextu  core.Contextualizer
    scorer    core.Scorer
    rawSvc    service.RawContentService
    contSvc   service.ContentService
    enrichSvc EnrichedContentService
    lock      *locks.ProcessingLock  // "enrich" stage
    log       *logger.Logger
}
```

- input_topic: `issuetracker.validated`
- output_topic: `issuetracker.enriched`
- consumer group: `issuetracker-enricher`
- worker_count: env `ENRICH_WORKER_COUNT` (default 4)
- Stage Gate Semaphore capacity: env `ENRICH_MAX_CONCURRENT_PER_STAGE` (default 2)

<br>

## graceful shutdown

`Worker.Stop(ctx)` 가 in-flight 4-stage 처리를 ctx 까지 대기. claudegen pool 의 컨테이너 정리는 별도 — [`pkg/agent/claude.md`](../../../pkg/agent/claude.md) 참조.

`CLAUDE_CODE_TIMEOUT` 이 각 stage 의 LLM 세션 timeout (default 120s, .env 180s). 4 단계 직렬이라 단일 article 의 enrich 총 walltime 은 최대 `4 × CLAUDE_CODE_TIMEOUT` 가능.

<br>

## 의존

- [`pkg/agent/claude`](../../../pkg/agent/claude.md) — claudegen container pool + SessionRunner
- [`pkg/llm/prompt`](../../../pkg/llm/prompt/) — file → embed prompt loader
- [`internal/storage/service`](../../storage/service.md) — RawContentService, ContentService, EnrichedContentService
- [`internal/storage/repository`](../../storage/README.md) — EnrichedContentRepository (이슈 #450)
- [`internal/locks`](../../locks/README.md) — ProcessingLock (단계 "enrich")
- [`pkg/queue`](../../../pkg/queue.md), [`pkg/logger`](../../../pkg/logger.md)

<br>

## 외부 시스템

- Kafka: `issuetracker.validated` consume, `issuetracker.enriched` produce, `issuetracker.dlq` produce (재시도 한도 초과)
- PostgreSQL: `enriched_contents` upsert (이슈 #450, #457)
- claudegen container (Docker): per-session `docker exec` (4 단계 × N article)
- (optional) MCP postgres tool: `enricher_ro` role 로 read-only DB 액세스 (이슈 #472)

<br>

## Wiring 위치

[`cmd/issuetracker.md`](../../../cmd/issuetracker.md) 의 단계 14 (`EnrichWorker`). `STAGES_ENRICH_ENABLED=true` (default) 일 때만 기동.

<br>

## 관련 이슈

- 이슈 #445 — enrich subsystem 메인 (4 단계 구성)
- 이슈 #446 — enricher 스켈레톤 (Kafka 토픽 + worker passthrough + stage toggle)
- 이슈 #447 — Stage 1 Extractor (entity/claim/fact JSON 구조화)
- 이슈 #448 — Stage 2 Verifier (cross-verify with DB candidates + WebFetch)
- 이슈 #449 — Stage 3 Contextualizer (external background / timeline / implications)
- 이슈 #450 — Stage 4 Scorer + enriched_contents 영속화 (trust_score)
- 이슈 #457 — `rationale` + `factors` 컬럼화 (scorer 진단 정보 영속화)
- 이슈 #458 — claudegen → `pkg/agent/claude` namespace 분리
- 이슈 #460 — parser/core + enrich/core — stage 별 agent 사용 OOP 분리
- 이슈 #470 — claude `--dangerously-skip-permissions` + tool 권한 자동 허가
- 이슈 #472 — enrich agent MCP postgres read-only DB 직접 접근
- 이슈 #474 — claudegen 컨테이너 user non-root (node)
- 이슈 #486 — enrich 프롬프트 JSON-only contract 강화 (CRITICAL OUTPUT CONTRACT 블록)
