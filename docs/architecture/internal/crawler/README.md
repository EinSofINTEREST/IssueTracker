# internal/crawler/ — Web Crawling Subsystem

[`internal/crawler/`](../../../../internal/crawler/) 는 IssueTracker 의 **fetch 단계**를 구성하는
패키지군입니다. 토픽 `issuetracker.crawl.{high,normal,low}` 를 consume 하여 웹페이지 본문을 가져오고,
[Claim Check 패턴](https://learn.microsoft.com/en-us/azure/architecture/patterns/claim-check) 으로
`raw_contents` 테이블에 저장한 뒤 `RawContentRef` 를 `issuetracker.fetched` 에 발행합니다.

<br>

## 서브패키지 트리

```
internal/crawler/
├── core/             ← 인터페이스 + 모델 + 에러 (모든 다른 패키지가 의존)
├── handler/          ← Registry: crawler_name → Handler
├── domain/general/   ← Fetcher Chain of Responsibility + 사이트별 config
│   └── sources/      ← KR (naver/daum/yonhap), US (cnn) 카테고리 URL + RPS
├── implementation/   ← Fetcher 구현체
│   ├── chromedp/     ← 헤드리스 Chrome (CDP)
│   └── goquery/      ← 정적 HTTP + goquery
├── parser/           ← (별도 도메인) DB-driven Parser 인터페이스 + rule engine
│   └── rule/
│       ├── llmgen/   ← LLM 으로 selector 자동 생성 (이슈 #149)
│       ├── pathinfer/← path_pattern 추론
│       └── refiner/  ← path_pattern 정밀화 polling (이슈 #173)
├── rate_limiter/     ← IP 단위 token bucket
└── worker/           ← PoolManager + ProcessingLock + RetryScheduler + CircuitBreaker
```

<br>

## 패키지별 문서

| 패키지                                                              | 문서                                       |
|---------------------------------------------------------------------|-------------------------------------------|
| [`internal/crawler/core/`](../../../../internal/crawler/core/)       | [core.md](core.md)                        |
| [`internal/crawler/handler/`](../../../../internal/crawler/handler/) | [handler.md](handler.md)                  |
| [`internal/crawler/domain/`](../../../../internal/crawler/domain/)   | [domain.md](domain.md)                    |
| [`internal/crawler/implementation/`](../../../../internal/crawler/implementation/) | [implementation.md](implementation.md) |
| [`internal/crawler/parser/`](../../../../internal/crawler/parser/)   | [parser.md](parser.md)                    |
| [`internal/crawler/rate_limiter/`](../../../../internal/crawler/rate_limiter/) | [rate_limiter.md](rate_limiter.md) |
| [`internal/crawler/worker/`](../../../../internal/crawler/worker/)   | [worker.md](worker.md)                    |

<br>

## 의존 그래프 (서브패키지간)

```
worker ──→ handler ──→ (domain/general → fetcher) ──→ implementation/{goquery,chromedp}
   │           │              │
   │           │              ├──→ rate_limiter
   │           │              └──→ pkg/links / pkg/urlguard
   │           │
   │           └──→ noop fallback
   │
   └──→ core (interfaces, models, errors) ──── 모든 패키지가 의존

parser/rule ──→ storage (parsing_rules / sample_urls 조회)
parser/rule/llmgen ──→ pkg/llm (provider abstract)
parser/rule/refiner ──→ pkg/llm + storage + pathinfer
```

`core` 가 sink 노드 (다른 어떤 서브패키지도 의존하지 않음 — 가장 안정적인 인터페이스 계층).

<br>

## 호출 흐름 (한 page 의 일생)

```
1. CrawlJob 메시지가 Kafka 로 도착
       │
       ▼ worker/pool.go
2. PoolManager 의 priority 별 KafkaConsumerPool 이 메시지를 fetch
       │
       ▼ worker/processing_lock.go (Acquire)
3. ProcessingLock 획득 (같은 URL 의 다른 인스턴스 차단)
       │
       ▼ handler/handler.go (Registry.Handle)
4. crawler_name 으로 Handler dispatch → ChainHandler
       │
       ▼ domain/general/chain_handler.go
5. ChainHandler 가 fetcher chain 순회 (goquery 우선 → 실패 시 chromedp)
       │
       ▼ implementation/{goquery,chromedp}/fetch.go
6. RawContent (HTML) 획득
       │
       ▼ rate_limiter (다음 요청을 위한 토큰 소비)
7. ChainHandler 가 raw_contents 에 저장 (Claim Check)
       │
       ▼ pkg/queue.Producer
8. RawContentRef 를 issuetracker.fetched 토픽에 발행
       │
       ▼
   (parser stage 로 핸드오프 — internal/parser/README.md)
```

각 단계의 자세한 책임은 위 패키지별 문서 참조.

<br>

## 외부 의존

- **Kafka**: `issuetracker.crawl.{high,normal,low}` consume / `issuetracker.fetched` produce
- **PostgreSQL**: `raw_contents` (Claim Check) / `parsing_rules` (rule engine, parser/) / `sample_urls` (refiner)
- **Redis**: ProcessingLock (SETNX) / IngestionLock / RetryQueue (ZSET)
- **Chrome (CDP)**: `implementation/chromedp` 가 `ws://localhost:9222` 또는 컨테이너 내장 chrome 사용
- **LLM**: `parser/rule/llmgen` 과 `parser/rule/refiner` 가 [`pkg/llm`](../../../../pkg/llm/) 통해 호출

<br>

## 관련 이슈

- 이슈 #100 — DB-driven parsing rules (사이트별 hardcode 파서 폐기)
- 이슈 #134 — fetcher 와 parser 분리 (Claim Check 도입)
- 이슈 #82 — Redis delayed retry queue (worker slot 점유 회피)
- 이슈 #178 — IngestionLock + ProcessingLock 단일 책임화
- 이슈 #149 — LLM 기반 selector 자동 생성
- 이슈 #173 — path_pattern 정밀화 (refiner)
