# internal/parser — Rule Engine 상세

상위 [README.md](README.md) 의 layer 1 (rule engine) 상세 문서. Kafka worker 부분은 README 참조.

소스: [`internal/parser/parser.go`](../../../../internal/parser/parser.go) (도메인 중립 인터페이스) + [`internal/parser/rule/`](../../../../internal/parser/rule/) (`parsing_rules` 테이블 기반 단일 engine, 이슈 #100)

사이트별 hardcode 파서 (NaverParser/CNNParser/…) 를 폐기하고 DB 기반 rule 한 개의 parser engine 으로 모든 사이트를 처리.

<br>

## 1. 도메인 중립 인터페이스 ([parser.go](../../../../internal/parser/parser.go))

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
}
```

호출자는 도메인 (news / blog / community) 별로 `Page → 자기 모델` 변환 책임을 가집니다 (예:
[`domain/general/convert.go`](../../../../internal/processor/fetcher/domain/general/convert.go) 가 `Page → Content`).

<br>

## 2. Rule Engine ([rule/](../../../../internal/parser/rule/))

| 파일                                                                                     | 역할                                                            |
|----------------------------------------------------------------------------------------|-----------------------------------------------------------------|
| [parser.go](../../../../internal/parser/rule/parser.go)                         | `Parser` 구조체 — `ParsePage` / `ParseLinks` 를 SelectorMap 으로 실행 |
| [resolver.go](../../../../internal/parser/rule/resolver.go)                     | `Resolver` — host → `[]*ParsingRuleRecord` 캐시 (TTL 5min/30s, regex compile cache) |
| [discovery.go](../../../../internal/parser/rule/discovery.go)                   | `PageLinkDiscovery` — 전체 페이지 `<a>` 스캔 모드 (이슈 #139)    |
| [errors.go](../../../../internal/parser/rule/errors.go)                         | `Error` + `ErrCode` (`ErrNoRule`, `ErrEmptySelector`, `ErrParseFailure`) |

**처리 흐름**:
```
ParsePage(ctx, raw)
   ├ Resolver.ResolveByURL(raw.URL) → 매칭 ParsingRuleRecord
   │   ├ host_pattern = exact / wildcard
   │   └ path_pattern = regex (애플리케이션 레벨 매칭, 다중 rule 지원)
   ├ 없음 → ErrNoRule (호출자가 llmgen 으로 fallback)
   └ 있음 → SelectorMap 으로 goquery 추출 → Page 반환
```

<br>

## 3. LLM Selector Generator ([rule/llmgen/](../../../../internal/parser/rule/llmgen/))

이슈 #149. `ErrNoRule` 발생 시 **비동기**로 LLM 에게 selector 를 생성시키고 `enabled=false` 로
DB 에 INSERT (운영자 검토 후 활성).

| 파일                                                                                              | 역할                                                  |
|--------------------------------------------------------------------------------------------------|-------------------------------------------------------|
| [generator.go](../../../../internal/parser/rule/llmgen/generator.go)                      | `Generator` — Enqueue / 처리 goroutine / Stop          |
| [prompt.go](../../../../internal/parser/rule/llmgen/prompt.go)                            | LLM 프롬프트 템플릿 (HTML 샘플 + 출력 스키마)          |
| [dedup.go](../../../../internal/parser/rule/llmgen/dedup.go)                              | in-flight set — 동일 host 중복 enqueue 방지            |

**동작**: ParserWorker 가 `ErrNoRule` 받으면 `llmGen.Enqueue(ctx, host, targetType, raw)` 호출 —
`targetType` (`page`/`list`) 별로 별도 in-flight set 으로 dedup 됩니다. Generator 는 별도 goroutine 에서
LLM 호출 → 결과 검증 (selector 가 실제 매칭되는지 확인) → INSERT (enabled=false) →
[`Resolver.Invalidate(host, targetType)`](../../../../internal/parser/rule/resolver.go) 로
해당 (host, targetType) 캐시 항목 무효화 (전체 invalidation 은 `Resolver.InvalidateAll()`).

<br>

## 4. Path Pattern Inference ([rule/pathinfer/](../../../../internal/parser/rule/pathinfer/))

| 파일                                                                                              | 역할                                                  |
|--------------------------------------------------------------------------------------------------|-------------------------------------------------------|
| [pathinfer.go](../../../../internal/parser/rule/pathinfer/pathinfer.go)                   | `InferHeuristic` — 알고리즘 only (공통 prefix / 숫자 ID 패턴) |
| [llm.go](../../../../internal/parser/rule/pathinfer/llm.go)                               | `InferLLM` — heuristic 실패 시 LLM 에 위임            |

[refiner](../../../../internal/parser/rule/refiner/) 가 본 함수들을 호출.

<br>

## 5. Refiner ([rule/refiner/](../../../../internal/parser/rule/refiner/))

이슈 #173 단계 4-2. `llm-auto` source 의 **catch-all** rule 의 `path_pattern` 을 `sample_urls`
누적 데이터로 정밀화.

| 파일                                                                                                 | 역할                                                 |
|-----------------------------------------------------------------------------------------------------|------------------------------------------------------|
| [refiner.go](../../../../internal/parser/rule/refiner/refiner.go)                            | `Refiner` — Start / Stop / RunOnce — interval polling goroutine |
| [llm_adapter.go](../../../../internal/parser/rule/refiner/llm_adapter.go)                    | `pkg/llm.Provider` → `LLMClient` 인터페이스 어댑터    |
| [metrics.go](../../../../internal/parser/rule/refiner/metrics.go)                            | `refiner_attempts` Prometheus counter (PR #191)      |

**동작**:
```
1. polling goroutine 가 interval 마다 RunOnce 호출
2. parsing_rules 에서 source_name='llm-auto' AND path_pattern='' (catch-all) 조회
3. 각 rule 에 대해 sample_urls 에서 누적 URL 로드 (MinSamples 미만이면 skip)
4. pathinfer.InferHeuristic → 실패 + LLM 있으면 InferLLM
5. 결과 path_pattern 으로 parsing_rules.UpdatePathPattern (optimistic guard)
6. Resolver.Invalidate(host) + SampleURLRepository.Purge(rule_id)
```

<br>

## 의존 그래프

```
parser.go (interface)
    │
    ▼
rule/parser.go ──→ rule/resolver.go ──→ internal/storage (ParsingRuleRepository)
                          │
                          └──→ regex cache + TTL cache

rule/llmgen/ ──→ pkg/llm + storage (parsing_rules INSERT)
rule/refiner/ ──→ pathinfer + pkg/llm + storage (parsing_rules UPDATE / sample_urls)
rule/discovery/ ──→ pkg/links (link extract)
```

<br>

## 호출 측

- [`internal/parser/worker.ParserWorker`](README.md) — `rule.Parser` 와 `Resolver` 를 인스턴스로 보유, 각 메시지마다 `ParsePage` / `ParseLinks` 호출
- [`internal/parser/worker.ParserWorker`](README.md) — `ErrNoRule` 발생 시 `llmGen.Enqueue` 호출
- [`cmd/issuetracker.buildRefiner`](../../cmd/issuetracker.md) — Refiner 인스턴스 wire

<br>

## 관련 이슈

- 이슈 #100 — DB-driven parsing rules
- 이슈 #139 — full-page link discovery (LinkDiscoveryConfig)
- 이슈 #149 — LLM selector 자동 생성
- 이슈 #173 — path_pattern 정밀화 (refiner)
- 이슈 #190 — refinement trigger refinement
- 이슈 #192 — refiner LLM 추론 검증 강화 (백로그)
- PR #191 — refiner Prometheus metric (refinement_attempts)
