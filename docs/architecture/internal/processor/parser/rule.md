# internal/processor/parser — Rule Engine 상세

상위 [README.md](README.md) 의 layer 1 (rule engine) 상세 문서. Kafka worker 부분은 README 참조.

소스: [`internal/processor/parser/types/types.go`](../../../../../internal/processor/parser/types/types.go) (도메인 중립 인터페이스 + `Page` / `LinkItem`) + [`internal/processor/parser/rule/`](../../../../../internal/processor/parser/rule/) (`parser_rules` 테이블 기반 단일 engine, 이슈 #100)

사이트별 hardcode 파서 (NaverParser/CNNParser/…) 를 폐기하고 DB 기반 rule 한 개의 parser engine 으로 모든 사이트를 처리.

<br>

## 1. 도메인 중립 인터페이스 ([types/types.go](../../../../../internal/processor/parser/types/types.go))

```go
type ContentParser interface {
    ParsePage(ctx context.Context, raw *core.RawContent) (*Page, error)
}

type LinkListParser interface {
    ParseLinks(ctx context.Context, raw *core.RawContent) ([]LinkItem, error)
}

type Page struct {
    URL, Title, MainContent, Summary, Author, Language, Category string
    PublishedAt time.Time
    Tags        []string
    Images      []string
    Metadata    map[string]string

    // 이슈 #423 — 적용된 parser_rule 의 article 플래그. 다운스트림 validator 가
    // PublishedAt 강제 여부를 결정 (article=true 일 때만 필수).
    Article bool
}
```

호출자는 도메인 (news / blog / community) 별로 `Page → 자기 모델` 변환 책임을 가집니다 (예:
[`domain/general/convert.go`](../../../../../internal/processor/fetcher/domain/general/convert.go) 가 `Page → Content`).

<br>

## 2. Rule Engine ([rule/](../../../../../internal/processor/parser/rule/))

| 파일 | 역할 |
|------|------|
| [parser.go](../../../../../internal/processor/parser/rule/parser.go) | `Parser` 구조체 — `ParsePage` / `ParseLinks` 를 SelectorMap 으로 실행. `ParserOption` functional option + `WithBlacklistAutoDemote` (이슈 #477) |
| [resolver.go](../../../../../internal/processor/parser/rule/resolver.go) | `Resolver` — host → `[]*ParserRuleRecord` 캐시 (TTL 5min/30s, regex compile cache). `RuleLookup` interface 구현체 (이슈 #463) |
| [extract.go](../../../../../internal/processor/parser/rule/extract.go) | `validateRaw` / `extractField` / `extractFieldMulti` / `extractDate` 등 helper 모음 (이슈 #463 분리) |
| [discovery.go](../../../../../internal/processor/parser/rule/discovery.go) | `PageLinkDiscovery` — 전체 페이지 `<a>` 스캔 모드 (이슈 #139) |
| [errors.go](../../../../../internal/processor/parser/rule/errors.go) | `Error` + `ErrCode` (`ErrNoRule`, `ErrEmptySelector`, `ErrParseFailure`) |
| [blacklist_matcher.go](../../../../../internal/processor/parser/rule/blacklist_matcher.go) | `BlacklistMatcher` — host 단위 lookup + path regex 매칭. `Classify(urls) → Drop / ExtractLinksOnly / Pass` 분기 (이슈 #295, #297) |
| [auto_demote.go](../../../../../internal/processor/parser/rule/auto_demote.go) | `autoDemoter` — index-only 판정 페이지를 `parser_blacklist` 에 `mode='extract_links_only'` 로 비동기 자동 등록 (이슈 #477). `AutoDemoteRegisterer` narrow interface — `service.BlacklistService` 가 만족 |
| [auto_demote_metrics.go](../../../../../internal/processor/parser/rule/auto_demote_metrics.go) | `AutoDemoteMetrics` — `parser_index_page_auto_demoted_total{host}` Counter (nil-safe, idempotent register) |

**RuleLookup interface** (이슈 #463) — `*Resolver` 가 본 interface 를 만족. `Parser` 는 concrete 대신 interface 의존으로 단위 테스트 mock 주입 가능:

```go
type RuleLookup interface {
    ResolveByURL(ctx context.Context, rawURL string, targetType model.TargetType) (*model.ParserRuleRecord, error)
}
```

**ParserOption 패턴** (이슈 #477):

```go
type ParserOption func(*Parser)

// repo / log 가 nil 이면 옵션 noop (기존 동작 100% 유지)
func WithBlacklistAutoDemote(repo AutoDemoteRegisterer, metrics *AutoDemoteMetrics, log *logger.Logger) ParserOption
```

**처리 흐름**:
```
ParsePage(ctx, raw)
   ├ validateRaw → raw.HTML 비어있으면 ErrParseFailure
   ├ Resolver.ResolveByURL(raw.URL, page) → 매칭 ParserRuleRecord
   │   ├ host_pattern = exact / wildcard
   │   └ path_pattern = regex (애플리케이션 레벨 매칭, 다중 rule 지원)
   ├ rule 없음 → ErrNoRule (호출자가 llmgen 으로 fallback)
   ├ Title/MainContent selector 부재 → ErrEmptySelector
   ├ Selectors 로 goquery 추출 → Page
   ├ Title/MainContent 매칭 0건 → ErrParseFailure (stale rule 진단)
   └ 이슈 #477 분기: autoDemoter != nil 이면
       indexonly.IsIndexOnly(page, raw.HTML) 평가 →
       true 시 demoteAsync (별도 goroutine + context.WithoutCancel) →
       page 자체는 정상 반환 (다음 호출부터 matcher 가 extract_links_only 로 라우팅)
```

`Parser.WaitAutoDemote()` 는 in-flight async demote goroutine 완료 대기 — graceful shutdown / 테스트 race 회피용.

<br>

## 3. Index-only Heuristic ([rule/indexonly/](../../../../../internal/processor/parser/rule/indexonly/))

이슈 #476. ParsePage 결과가 카테고리/링크 hub 페이지로 보이는지 판정하는 pure-logic 모듈.

| 파일 | 역할 |
|------|------|
| [indexonly.go](../../../../../internal/processor/parser/rule/indexonly/indexonly.go) | `IsIndexOnly(page, rawHTML, cfg) (bool, Score)` + `Config` + `Score` |

**판정 기준** (AND, false-positive 회피):

- 본문이 짧음 — `Page.MainContent` rune 길이 < `MinBodyRunes` (default 200)
- `PublishedAt` zero-value (카테고리/목록 페이지)
- 링크 hub 신호 (둘 중 하나):
  - same-host anchor 텍스트 비율 ≥ `MinLinkRatio` (default 0.8)
  - article-like link ≥ `MinArticleLinks` (default 5)

**Same-host 필터**: LinkRatio / ArticleLinks 모두 same-host anchor 만 카운트 — 외부 광고 / 관련 사이트 영역의 false-positive 차단.

호출 측 ([rule/parser.go](../../../../../internal/processor/parser/rule/parser.go) ParsePage 후반부) 이 결과를 받아 `autoDemoter.demoteAsync` 로 위임.

<br>

## 4. LLM Selector Generator ([rule/llmgen/](../../../../../internal/processor/parser/rule/llmgen/))

이슈 #149. `ErrNoRule` / `ErrParseFailure` 등 발생 시 **비동기**로 LLM 에게 selector 를 생성시키고 `enabled=false` 로 DB 에 INSERT (운영자 검토 후 활성). 또한 LLM 이 페이지를 "파싱 부적합" 으로 판정하면 `parser_blacklist` 에 `source='auto'` 로 자동 등록 (이슈 #326, #480).

| 파일 | 역할 |
|------|------|
| [generator.go](../../../../../internal/processor/parser/rule/llmgen/generator.go) | `Generator` — Enqueue / 처리 goroutine / Stop / `BlacklistAutoRegister` interface |
| [extractor.go](../../../../../internal/processor/parser/rule/llmgen/extractor.go) | `BlacklistDecision` (Reason + **Mode**, 이슈 #480) / `ExtractResult` / `EnrichedExtractor` interface |
| [confidence.go](../../../../../internal/processor/parser/rule/llmgen/confidence.go) | LLM 신뢰도 → INSERT 결정 |
| [breaker.go](../../../../../internal/processor/parser/rule/llmgen/breaker.go) | host 단위 circuit breaker (3-strike / 10min cooldown) |
| [pending.go](../../../../../internal/processor/parser/rule/llmgen/pending.go) | Redis 기반 pending URL 큐 |

**BlacklistDecision.Mode** (이슈 #480):

```go
type BlacklistDecision struct {
    Reason string
    Mode   model.BlacklistMode  // "drop" | "extract_links_only" (빈 문자열은 service 가 drop fallback)
}
```

LLM 프롬프트 ([`pkg/llm/prompt/assets/parser/claude/{page,list}.user.txt`](../../../../../pkg/llm/prompt/assets/parser/claude/)) 가 `blacklist_mode` 필드를 emit. claudegen worker ([`pkg/agent/claude/worker.go`](../../../../../pkg/agent/claude/worker.go)) 가 `enrichedOutput.BlacklistMode` 파싱 → `BlacklistDecision.Mode` 에 전파. `service.BlacklistService.HandleLLMDecision(ctx, host, sampleURL, targetType, reason, mode)` 호출.

**article 플래그** (이슈 #421/#423): `ExtractResult.Article` + `ArticleConfidence` 가 LLM 자체 분류. parser_rules.article 컬럼에 영속화 → 다운스트림 validator 가 `Article=true` 에만 PublishedAt 필수 강제.

**동작**: ParserWorker 가 `ErrNoRule` / `ErrParseFailure` 받으면 `llmGen.Enqueue(ctx, host, targetType, raw)` 호출 — `targetType` (`page`/`list`) 별로 별도 in-flight set 으로 dedup. Generator 는 별도 goroutine 에서 EnrichedExtractor (claudegen) 호출 → 결과 검증 → INSERT (enabled=false) → [`Resolver.Invalidate(host, targetType)`](../../../../../internal/processor/parser/rule/resolver.go).

<br>

## 5. Path Pattern Inference ([rule/pathinfer/](../../../../../internal/processor/parser/rule/pathinfer/))

| 파일 | 역할 |
|------|------|
| [pathinfer.go](../../../../../internal/processor/parser/rule/pathinfer/pathinfer.go) | `InferHeuristic` — 알고리즘 only (공통 prefix / 숫자 ID 패턴) |
| [llm.go](../../../../../internal/processor/parser/rule/pathinfer/llm.go) | `InferLLM` — heuristic 실패 시 LLM 에 위임 |

[refiner](../../../../../internal/processor/parser/rule/refiner/) 가 본 함수들을 호출.

<br>

## 6. Refiner ([rule/refiner/](../../../../../internal/processor/parser/rule/refiner/))

이슈 #173 단계 4-2. `llm-auto` source 의 **catch-all** rule 의 `path_pattern` 을 `sample_urls` 누적 데이터로 정밀화.

| 파일 | 역할 |
|------|------|
| [refiner.go](../../../../../internal/processor/parser/rule/refiner/refiner.go) | `Refiner` — Start / Stop / RunOnce — interval polling goroutine |
| [llm_adapter.go](../../../../../internal/processor/parser/rule/refiner/llm_adapter.go) | `pkg/llm.Provider` → `LLMClient` 인터페이스 어댑터 |
| [metrics.go](../../../../../internal/processor/parser/rule/refiner/metrics.go) | `refinement_attempts` Prometheus counter (PR #191) |

**동작**:
```
1. polling goroutine 가 interval 마다 RunOnce 호출
2. parser_rules 에서 source_name='llm-auto' AND path_pattern='' (catch-all) 조회
3. 각 rule 에 대해 sample_urls 에서 누적 URL 로드 (MinSamples 미만이면 skip)
4. pathinfer.InferHeuristic → 실패 + LLM 있으면 InferLLM
5. 결과 path_pattern 으로 parser_rules.UpdatePathPattern (optimistic guard)
6. Resolver.Invalidate(host) + SampleURLRepository.Purge(rule_id)
```

<br>

## 의존 그래프

```
types/types.go (interface)
    │
    ▼
rule/parser.go ──→ rule/resolver.go (RuleLookup) ──→ internal/storage (ParserRuleRepository)
       │                  │
       │                  └──→ regex cache + TTL cache
       │
       ├──→ rule/indexonly.IsIndexOnly (#476)
       └──→ rule/auto_demote.autoDemoter ──→ service.BlacklistService (#477)
                                                 │
                                                 └──→ AutoDemoteMetrics → prometheus

rule/llmgen/ ──→ pkg/agent/claude (EnrichedExtractor) + storage (parser_rules INSERT)
                  │
                  └──→ service.BlacklistService.HandleLLMDecision (mode 인자, #480)
rule/refiner/ ──→ pathinfer + pkg/llm + storage (parser_rules UPDATE / sample_urls)
rule/discovery/ ──→ pkg/links (link extract)
rule/blacklist_matcher.go ──→ storage.BlacklistRepository (host 단위 lookup)
```

<br>

## 호출 측

- [`internal/processor/parser/worker.Worker`](README.md) — `rule.Parser` 와 `Resolver` 를 인스턴스로 보유, 각 메시지마다 `ParsePage` / `ParseLinks` 호출
- [`internal/processor/parser/worker.Worker`](README.md) — `ErrNoRule` / `ErrParseFailure` 발생 시 `llmGen.Enqueue` 호출
- [`internal/processor/precheck.Precheck`](../precheck.md) — 발행 직전 `BlacklistMatcher.Classify` 호출 (이슈 #425)
- [`cmd/issuetracker`](../../../cmd/issuetracker.md) — `rule.NewParser(resolver, rule.WithBlacklistAutoDemote(blacklistSvc, metrics, log))` wiring + Refiner 인스턴스 wire

<br>

## 관련 이슈

- 이슈 #100 — DB-driven parser rules
- 이슈 #139 — full-page link discovery (LinkDiscoveryConfig)
- 이슈 #149 — LLM selector 자동 생성
- 이슈 #173 — path_pattern 정밀화 (refiner)
- 이슈 #190 — refinement trigger refinement
- PR #191 — refiner Prometheus metric (refinement_attempts)
- 이슈 #295 — page-parse 블랙리스트 인프라 (drop)
- 이슈 #297 — `extract_links_only` mode 도입
- 이슈 #326 — LLM 자동 blacklist 등록
- 이슈 #421/#423 — `parser_rules.article` 플래그 + 다운스트림 wiring
- 이슈 #463 — RuleLookup interface + extract.go 분리
- 이슈 #476 — index-only 휴리스틱 모듈 (`indexonly/`)
- 이슈 #477 — ParsePage 자동 강등 wiring + `parser_index_page_auto_demoted_total` metric
- 이슈 #480 — LLM auto-blacklist mode 분기 (BlacklistDecision.Mode)
- 이슈 #482 — `seededHostTargets` / `VerifySeeded` 폐기 (DB-driven readiness 신뢰)
