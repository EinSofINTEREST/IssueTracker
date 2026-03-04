# IssueTracker

한국어 | **[English](../../README.md)**

> 글로벌 이슈 수집 및 분석 시스템

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Test Coverage](https://img.shields.io/badge/coverage-92.1%25-brightgreen)](./coverage.out)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## 개요

IssueTracker는 전 세계의 뉴스, 소셜 미디어, 커뮤니티 소스를 크롤링하고, 다국어 콘텐츠를 처리 및 정규화하며, 임베딩과 클러스터링을 통해 주요 이슈를 식별하도록 설계된 확장 가능하고 유연한 시스템입니다.

**초기 타겟 시장**: 미국과 대한민국

## 주요 기능

- 🌐 **다중 소스 크롤링**: 뉴스 사이트, RSS 피드, API, 커뮤니티 플랫폼 지원
- 🔄 **Kafka 기반 파이프라인**: 장애 허용성을 갖춘 분산 비동기 처리
- 🧠 **ML 기반 분석**: 이슈 탐지를 위한 임베딩 생성 및 클러스터링
- 🌍 **다국어 지원**: 여러 언어(영어, 한국어)의 콘텐츠 처리
- 📊 **실시간 모니터링**: Prometheus 메트릭 및 헬스 체크
- ✅ **프로덕션 준비 완료**: 포괄적인 에러 핸들링, 재시도 로직, 속도 제한
- 🏗️ **표준 Go 레이아웃**: golang-standards/project-layout 준수

## 아키텍처

```
┌─────────────────────────────────────────┐
│     API / Job Scheduler Layer           │
├─────────────────────────────────────────┤
│     Crawler Orchestration Layer         │
├─────────────────────────────────────────┤
│     Source-Specific Crawlers            │
│  (News, Community, Social Media)        │
├─────────────────────────────────────────┤
│     Data Processing Pipeline            │
│  (Normalize, Validate, Enrich)          │
├─────────────────────────────────────────┤
│     Embedding & ML Layer                │
│  (Vectorize, Cluster, Classify)         │
├─────────────────────────────────────────┤
│     Storage Layer                       │
│  (Raw, Processed, Embeddings)           │
└─────────────────────────────────────────┘
```

## 빠른 시작

### 사전 요구사항

- Go 1.21+
- PostgreSQL 15+
- Apache Kafka 3.5+
- Redis 7+

### 설치

```bash
git clone https://github.com/yourusername/issuetracker
cd issuetracker
go mod tidy
```

### 빌드

```bash
# 모든 바이너리 빌드
make build

# 특정 바이너리 빌드
go build -o bin/crawler ./cmd/crawler
```

### Kafka 설정

```bash
# Kafka 브로커 + UI 시작 (localhost:9092, UI: http://localhost:8080)
make kafka-start

# 중지 (데이터 보존)
make kafka-stop

# 중지 + 모든 데이터 삭제
make kafka-clean
```

**파티션 설정** — 시작하기 전에 `.env.example`을 복사하고 조정:

```bash
cp deployments/docker/.env.example deployments/docker/.env
# 편집: KAFKA_PARTITIONS_HIGH, KAFKA_PARTITIONS_NORMAL, KAFKA_PARTITIONS_LOW
make kafka-start
```

### 실행

```bash
# 먼저 Kafka 시작
make kafka-start

# 크롤러 실행 (localhost:9092에 연결, crawl.normal 구독)
make run-crawler

# Kafka 파이프라인 예제 실행 (인메모리 모의 구현, Kafka 불필요)
make run-kafka-pipeline

# 기본 예제 실행
make run-example

# 바이너리 직접 실행
./bin/crawler
```

### 데이터베이스 설정

이 프로젝트는 데이터 지속성을 위해 pgx/v5 드라이버와 함께 PostgreSQL을 사용합니다.

```bash
# PostgreSQL 컨테이너 시작
make pg-start

# PostgreSQL 상태 확인
make pg-status

# 마이그레이션 실행 (멱등성, 여러 번 실행해도 안전)
make pg-migrate

# 마이그레이션 롤백 (프로덕션에서는 주의해서 사용)
make pg-migrate-down

# PostgreSQL 셸 연결
make pg-psql
```

**마이그레이션**:
- `001_create_raw_contents.up.sql` — RawContent 저장 (원본 HTML)
- `002_create_contents.up.sql` — 정규화된 Content 저장
- `003_create_news_articles.up.sql` — NewsArticle 저장 (title, body, author, category, tags, image_urls, published_at)

### 테스트

```bash
# 모든 테스트 실행
make test

# 상세 출력과 함께 실행
make test-verbose

# 커버리지 확인
make coverage

# HTML 커버리지 보고서 생성
make coverage-html
```

### 개발

```bash
# 코드 포맷
make fmt

# 린터 실행
make lint

# 빌드 아티팩트 정리
make clean

# 의존성 업데이트
make deps
```

## 현재 상태

✅ **핵심 크롤러 인프라** (v0.2.0)
- [x] 핵심 인터페이스 및 데이터 모델
- [x] 커넥션 풀링을 갖춘 HTTP 클라이언트
- [x] 토큰 버킷 속도 제한기
- [x] 지수 백오프를 사용한 재시도 로직
- [x] 포괄적인 에러 핸들링
- [x] zerolog를 사용한 구조화된 로깅
- [x] 컨텍스트 인식 로깅
- [x] 92.1% 테스트 커버리지
- [x] 표준 Go 프로젝트 레이아웃
- [x] 빌드 자동화를 위한 Makefile
- [x] cmd/ 진입점

✅ **Kafka 통합** (v0.3.0)
- [x] Producer / Consumer 인터페이스 추상화 (`pkg/queue`)
- [x] KafkaConsumerPool — 우선순위 기반 다중 워커 고루틴 풀
- [x] Handler Registry — 크롤러 이름 → 핸들러 디스패치
- [x] 재시도 횟수 기반 라우팅을 갖춘 DLQ (Dead Letter Queue)
- [x] Docker Compose Kafka 스택 (KRaft 모드, Zookeeper 불필요)
- [x] `.env`를 통한 설정 가능한 파티션으로 Kafka 토픽 초기화
- [x] Kafka UI (`http://localhost:8080`)
- [x] 우선순위 기반 다중 풀 매니저 (`PoolManager`)

✅ **뉴스 도메인 크롤러** (v0.4.0)
- [x] 뉴스 도메인 DIP 인터페이스 (`internal/crawler/domain/news/news.go`)
- [x] Chain of Responsibility 핸들러 (`handler.go`) — RSS → GoQuery → Browser 폴백 체인
- [x] RSS/GoQuery/Browser 어댑터 (`fetcher/`)
- [x] **한국 소스**:
  - [x] Naver 크롤러 (GoQuery → Browser 폴백) + Parser
  - [x] Yonhap 크롤러 (RSS → GoQuery 폴백) + Parser
  - [x] Daum 크롤러 (GoQuery) + Parser
  - [x] KR 레지스트리 조립 진입점 (`kr/registry.go`)
- [x] **미국 소스**:
  - [x] CNN 크롤러 (GoQuery) + Parser
  - [x] US 레지스트리 조립 진입점 (`us/registry.go`)
- [x] PostgreSQL 스토리지 레이어 (`internal/storage/news_article.go`, `postgres/news_article.go`)
- [x] 마이그레이션 — `news_articles` 테이블 생성 (`003_create_news_articles.up.sql`)
- [x] 테스트 — KR 파서/크롤러 780+ 케이스, US 파서/크롤러 519+ 케이스

✅ **Kafka Blob 오프로딩** (v0.5.0)
- [x] `RawContentRef` — 경량 Kafka 메시지 구조체 (ID + 메타데이터, HTML 본문 제외)
- [x] `RawContentService`를 `KafkaConsumerPool`에 주입하여 Postgres 우선 저장
- [x] 워커가 전체 `RawContent`(HTML 포함)를 Postgres에 저장 후 `RawContentRef`만 Kafka에 발행
- [x] 중복 URL 처리 — 에러 없이 기존 레코드 ID 반환
- [x] `PoolManager` (`manager.go`) — 우선순위 기반 다중 풀 오케스트레이션 (High / Normal / Low)
- [x] 풀 처리 로직 단위 테스트 (`test/internal/worker/`)

📋 **계획됨**
- [ ] 처리 파이프라인 (normalize → validate → enrich)
- [ ] 임베딩 생성
- [ ] 클러스터링 알고리즘
- [ ] API 엔드포인트
- [ ] 웹 대시보드

## 프로젝트 구조

[Standard Go Project Layout](https://github.com/golang-standards/project-layout)을 따릅니다:

```
issuetracker/
├── cmd/
│   └── crawler/               # 크롤러 진입점
│       └── main.go
│
├── internal/
│   ├── crawler/
│   │   ├── core/              # 크롤러 인터페이스, 모델, 에러, 재시도
│   │   ├── handler/           # Handler 인터페이스 + Registry (크롤러 이름 디스패치)
│   │   │   ├── handler.go     # Handler 인터페이스, Registry
│   │   │   └── noop.go        # 폴백 noop 핸들러
│   │   ├── worker/            # Kafka consumer 풀
│   │   │   ├── pool.go        # KafkaConsumerPool (고루틴 워커 풀 + DLQ + Postgres 오프로딩)
│   │   │   └── manager.go     # PoolManager (High/Normal/Low 우선순위 풀 오케스트레이션)
│   └── storage/               # 데이터 액세스 레이어
│       ├── news_article.go    # NewsArticle 리포지토리 인터페이스
│       └── postgres/          # PostgreSQL 구현
│           └── news_article.go # pgx/v5 기반 NewsArticle CRUD
│       └── domain/
│           └── news/          # 뉴스 도메인 크롤러 (DIP + Chain of Responsibility)
│               ├── news.go    # 도메인 인터페이스 (NewsFetcher, NewsRSSFetcher, ...)
│               ├── handler.go # Chain: RSS → GoQuery → Browser
│               ├── fetcher/   # 어댑터 (rss, goquery, browser)
│               ├── kr/        # 한국 소스
│               │   ├── naver/ # 네이버 (config, crawler, parser)
│               │   ├── yonhap/ # 연합뉴스 (config, crawler, parser)
│               │   ├── daum/  # 다음 (config, crawler, parser)
│               │   └── registry.go # 조립 & 등록 진입점
│               └── us/        # 미국 소스
│                   ├── cnn/   # CNN (config, crawler, parser)
│                   └── registry.go # 조립 & 등록 진입점
│
├── pkg/
│   ├── logger/                # 구조화된 로거 (zerolog)
│   └── queue/                 # Kafka producer/consumer 추상화
│       ├── queue.go           # Producer, Consumer 인터페이스
│       ├── config.go          # 토픽/그룹 상수, Config
│       ├── producer.go        # KafkaProducer (kafka-go)
│       └── consumer.go        # KafkaConsumer (kafka-go, 수동 커밋)
│
├── deployments/
│   └── docker/
│       ├── docker-compose.yml # Kafka 브로커 (KRaft) + kafka-ui
│       └── .env.example       # 파티션 설정 템플릿
│
├── examples/
│   ├── basic_usage.go
│   └── kafka_pipeline/        # 인메모리 모의 파이프라인 예제
│
├── migrations/                # 데이터베이스 마이그레이션 (PostgreSQL)
│   ├── 001_create_raw_contents.up.sql     # raw_contents 테이블
│   ├── 002_create_contents.up.sql         # contents 테이블
│   ├── 003_create_news_articles.up.sql    # news_articles 테이블 + 인덱스
│   └── *.down.sql             # 롤백 마이그레이션
│
├── test/                      # 패키지 레벨 테스트
├── docs/
│   ├── en/
│   └── ko/
├── Makefile
├── go.mod
└── go.sum
```

### 설계 근거

**표준 Go 프로젝트 레이아웃:**
- **`cmd/`**: 애플리케이션 진입점 (main 패키지)
- **`internal/`**: 비공개 코드 (외부 프로젝트에서 임포트 불가)
- **`pkg/`**: 공개 라이브러리 코드 (외부 프로젝트에서 임포트 가능)
- 장점:
  - 업계 표준 구조
  - 명확한 관심사 분리
  - Go 개발자에게 친숙한 탐색
  - 향상된 의존성 관리

## 핵심 컴포넌트

### Crawler 인터페이스

```go
type Crawler interface {
  Name() string
  Source() SourceInfo
  Initialize(ctx context.Context, config Config) error
  Start(ctx context.Context) error
  Stop(ctx context.Context) error
  Fetch(ctx context.Context, target Target) (*RawContent, error)
  HealthCheck(ctx context.Context) error
}
```

### HTTP Client

- 커넥션 풀링 (최대 100개의 유휴 연결)
- 설정 가능한 타임아웃
- 응답 크기 제한 (10MB)
- 네트워크 에러 시 자동 재시도
- 통합 로깅

### Rate Limiter

- 토큰 버킷 알고리즘
- 시간당 요청 수 설정 가능
- 버스트 지원
- 컨텍스트 인식 대기
- 성능 최적화

### 에러 핸들링

- 카테고리가 있는 타입 에러
- 재시도 가능 vs 불가능 에러
- 추적을 위한 에러 코드
- 완전한 에러 컨텍스트 보존

### 구조화된 Logger

- 고성능을 위해 `zerolog` 기반
- 컨텍스트 인식 로깅
- 다양한 로그 레벨 (Debug, Info, Warn, Error, Fatal)
- JSON 및 pretty-print 형식
- 요청 ID 추적
- 크롤러별 필드

## 크롤러 구현

IssueTracker는 정적 및 동적 페이지를 위한 두 가지 크롤러를 제공합니다:

### Goquery - 정적 크롤링 (`implementation/goquery`)

정적 HTML 페이지를 위한 경량 크롤러입니다.

```go
crawler := goquery.NewGoqueryCrawler("my-crawler", sourceInfo, config)
raw, err := crawler.Fetch(ctx, target)
article, err := crawler.FetchAndParse(ctx, target, selectors)
```

### Chromedp - 동적 크롤링 (`implementation/chromedp`)

JavaScript 렌더링이 필요한 동적 페이지를 위한 헤드리스 브라우저 크롤러입니다.

```go
crawler := chromedp.NewChromedpCrawler("my-crawler", sourceInfo, config)
crawler.Initialize(ctx, config)
defer crawler.Stop(ctx)

raw, err := crawler.Fetch(ctx, target)                       // 렌더링된 HTML
article, err := crawler.FetchAndParse(ctx, target, selectors) // 렌더링 + 파싱
result, err := crawler.EvaluateJS(ctx, url, "document.title") // JS 실행
```

### 비교

| | Goquery | Chromedp |
|---|---|---|
| **용도** | 정적 HTML | JavaScript SPA |
| **속도** | 빠름 (~1초) | 느림 (~3-5초) |
| **메모리** | 낮음 (~10MB) | 높음 (~100MB) |
| **JS 지원** | ✗ | ✓ |
| **사용 사례** | 뉴스, RSS | 커뮤니티, SPA |

### 예제 실행

```bash
make run-comparison
```

## 뉴스 도메인 크롤러

### 한국 소스

#### Naver (네이버)
- **Fetcher**: GoQuery → Browser 폴백
- **기능**: 카테고리 추출, 이미지 URL 수집, KST → UTC 변환
- **날짜 형식**: `"2026-03-02 14:54:16"` (KST)

#### Yonhap (연합뉴스)
- **Fetcher**: RSS → GoQuery 폴백
- **기능**: 복수 기자 추출, 태그/키워드 수집, 사진 갤러리 지원
- **날짜 형식**: `"2024-01-15 14:30"` (KST)

#### Daum (다음)
- **Fetcher**: GoQuery
- **기능**: 카테고리, 이미지, 메타데이터 추출
- **날짜 형식**: ISO 8601

### 미국 소스

#### CNN
- **Fetcher**: GoQuery
- **기능**: Section/subsection 추출, byline 파싱, 메타데이터 지원
- **날짜 형식**: ISO 8601

**모든 파서**:
- 추출: Title, Body, Author, Category, Tags, ImageURLs, PublishedAt
- 누락된 필드를 gracefully 처리 (기본값으로 폴백)
- 모든 타임스탬프를 UTC로 변환
- 780+ 테스트 케이스 (한국), 519+ 테스트 케이스 (미국)

## 개발

### 코드 스타일

- **인덴트**: 2 스페이스 (탭 아님)
- **주석**: 한국어 + 영어 혼용
- **테스트**: 최소 70% 커버리지 (현재 92.1%)
- **네이밍**: 명확하고 자체 문서화된 이름

### 린터 실행

```bash
# Makefile 사용
make lint

# 직접 실행
golangci-lint run
```

### 문서

개발 규칙은 [`.claude/rules/`](../../.claude/rules/)에 있습니다:

1. **[01-architecture.md](../../.claude/rules/01-architecture.md)** - 시스템 아키텍처
2. **[02-crawler-implementation.md](../../.claude/rules/02-crawler-implementation.md)** - 크롤러 표준
3. **[03-data-processing.md](../../.claude/rules/03-data-processing.md)** - 처리 파이프라인
4. **[04-error-handling.md](../../.claude/rules/04-error-handling.md)** - 에러 핸들링 & 모니터링
5. **[05-testing.md](../../.claude/rules/05-testing.md)** - 테스트 전략
6. **[06-code-style.md](../../.claude/rules/06-code-style.md)** - 코드 규칙

## 사용 예제

전체 예제는 [examples/basic_usage.go](../../examples/basic_usage.go)를 참조하세요:

```go
import (
  "issuetracker/pkg/logger"
  "issuetracker/internal/crawler/core"
)

// 로거 설정 (개발 모드, pretty printing)
logConfig := logger.DefaultConfig()
logConfig.Level = logger.LevelDebug
logConfig.Pretty = true
log := logger.New(logConfig)

// 속도 제한이 있는 HTTP 클라이언트 생성
config := core.DefaultConfig()
config.RequestsPerHour = 60
httpClient := core.NewHTTPClient(config)
rateLimiter := core.NewRateLimiter(config.RequestsPerHour, config.BurstSize)

// 컨텍스트에 로거 추가
ctx := context.Background()
ctx = log.ToContext(ctx)

// 추적을 위한 요청 ID 추가
requestLog := log.WithRequestID("req-123")
requestCtx := requestLog.ToContext(ctx)

// 재시도와 함께 가져오기 (로깅은 자동으로 발생)
var resp *core.HTTPResponse
err := core.WithRetry(requestCtx, core.DefaultRetryPolicy, func() error {
  var fetchErr error
  resp, fetchErr = httpClient.Get(requestCtx, url)
  return fetchErr
})
```

## Makefile 명령어

```bash
# 빌드 & 실행
make build              # 크롤러 바이너리 빌드 → bin/crawler
make run-crawler        # 빌드 후 크롤러 실행
make run-example        # 기본 사용 예제 실행
make run-kafka-pipeline # 인메모리 Kafka 파이프라인 예제 실행

# 테스트 & 품질
make test               # 모든 테스트 실행
make coverage           # 커버리지 보고서와 함께 테스트 실행
make lint               # 린터 실행
make fmt                # 코드 포맷
make clean              # 빌드 아티팩트 제거

# Kafka
make kafka-start        # Kafka 브로커 + UI 시작 (토픽 생성)
make kafka-stop         # 중지 (KAFKA_DATA_DIR에 데이터 보존)
make kafka-clean        # 중지 + 모든 데이터 삭제
make kafka-status       # 컨테이너 상태 표시
make kafka-topics       # 모든 토픽 나열
make kafka-describe     # 토픽별 파티션/리더 세부 정보 표시
make kafka-scale-partitions TOPIC=<topic> PARTITIONS=<n>  # 파티션 증가

# PostgreSQL
make pg-start           # PostgreSQL 컨테이너 시작
make pg-stop            # PostgreSQL 중지 (데이터 보존)
make pg-clean           # 중지 + 모든 데이터 삭제
make pg-status          # PostgreSQL 컨테이너 상태 표시
make pg-migrate         # 데이터베이스 마이그레이션 실행 (멱등성)
make pg-migrate-down    # 마이그레이션 롤백 (주의해서 사용)
make pg-psql            # PostgreSQL 셸 연결

make help               # 설명과 함께 모든 명령어 표시
```

## 기여하기

기여하기 전에 `.claude/rules/`의 개발 규칙을 읽어주세요.

## 라이선스

MIT

## 문의

질문이나 피드백이 있으시면 GitHub에서 이슈를 열어주세요.
