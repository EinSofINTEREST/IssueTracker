# GitHub Copilot Instructions

## Language
- All responses and code review comments MUST be written in **Korean (한국어)**
- Technical terms (Go, Kafka, gRPC, etc.) may remain in English
- Code itself (identifiers, GoDoc comments) follows the conventions below

---

## Project Overview

**IssueTracker** is a global issue aggregation and clustering system (초기 범위: 미국/한국).

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

**Tech Stack**: Go 1.21+, PostgreSQL 15+, Apache Kafka 3.5+, Redis 7+, Qdrant

---

## Branch Naming Convention

```
{category}/#{issue-number}/{short-kebab-summary}
```

| Category | 용도 |
|----------|------|
| `feature/` | 새로운 기능 구현 및 추가 |
| `fix/` | 버그 수정 |
| `refactor/` | 구조 변경 및 리팩토링 |
| `docs/` | 문서 작업 |

**규칙:**
- 이슈 번호는 `#` 접두사 포함 (예: `#15`)
- 요약은 영문 소문자 + 하이픈(-) + 30자 이내
- 변경 대상 파일 또는 모듈명 중심으로 간결하게 표현

**예시:**
```
feature/#15/cnn-naver-crawler
feature/#16/normalize-validate-enrich-pipeline
feature/#19/redis-rate-limiter
fix/#7/rate-limiter-deadlock
fix/#9/duplicate-article-detection
refactor/#4/validation-logic
refactor/#8/extract-parser-interface
docs/#2/api-documentation
```

---

## Git Commit Convention

### Format

```
[{CATEGORY}]: {변경 내용}
```

### Categories

| Category | 용도 |
|----------|------|
| `FEAT` | 기능 구현 및 추가 |
| `FIX` | 버그 수정 |
| `REFAC` | 구조 변경, 리팩토링 |
| `DOCS` | 문서 작업, 주석, 프롬프트 변경 |
| `CHORE` | 빌드·CI·도구·의존성 등 잡무 (코드 외 부가 작업) |

### 작성 규칙

> **⚠️ 커밋 메시지는 반드시 한국어로 작성**

1. 언어: 한국어 (영어 사용 금지)
2. 형식: 명사형 종결 (예: "구현", "수정", "추가")
3. 내용: 변경 사항의 전체 요약 + 모듈별 변경점 명시

### 예시

```
[FEAT]: Kafka consumer pool을 활용한 크롤러 워커 구현

- KafkaConsumerPool 구조체를 통한 다중 워커 goroutine 관리
- graceful shutdown 지원
- 설정 가능한 워커 개수 구현
```

```
[FIX]: HTTP 클라이언트 timeout 에러 처리 개선

- timeout 발생 시 적절한 에러 핸들링 로직 추가
- exponential backoff 기반 재시도 로직 구현
```

```
[REFAC]: Article 검증 로직 단순화

- 불필요한 검증 단계 제거
- 함수 복잡도 25줄에서 15줄로 감소
```

```
[CHORE]: golangci-lint 버전 v1.64.8 로 업데이트

- ci-quality.yml lint job 의 binary 버전 고정
- 로컬 Makefile 의 lint 타겟에 동일 버전 명시
```

```
[DOCS]: 크롤러 API 문서 및 사용 예제 작성

- GoDoc 주석 추가
- README.md Quick Start 섹션 추가
```

---

## Code Style Guide (Go)

### Formatting

- **Indentation**: 2 spaces (탭 사용 금지)
- **Line Length**: 최대 100자
- **Imports**: 표준 라이브러리 → 외부 패키지 → 내부 패키지 순으로 그룹 분리

```go
import (
  // Standard library
  "context"
  "fmt"
  "time"

  // External packages
  "github.com/rs/zerolog/log"

  // Internal packages
  "issuetracker/internal/crawler"
  "issuetracker/internal/storage"
)
```

### Naming Conventions

| 대상 | 규칙 | 예시 |
|------|------|------|
| 변수 | camelCase | `articleCount`, `httpClient` |
| 상수 | CamelCase (SCREAMING_CASE 금지) | `DefaultTimeout`, `MaxWorkers` |
| Exported 함수 | 대문자 시작 CamelCase | `FetchArticle`, `ParseHTML` |
| Unexported 함수 | 소문자 시작 camelCase | `validateURL`, `extractContent` |
| 인터페이스 | 짧고 명확하게 (Interface 접미사 금지) | `Crawler`, `Repository` |
| 구조체 | 단수 명사 | `Article`, `Parser` |
| 패키지 | 소문자, 단수 | `crawler`, `storage` |

### Function Design

- 함수당 최대 50줄
- 파라미터 최대 5개 (초과 시 Options struct로 묶기)
- `context.Context`는 항상 첫 번째 파라미터
- Early Return 패턴 사용

```go
// Good
func process() error {
  if err := validate(); err != nil {
    return err
  }
  if err := fetch(); err != nil {
    return err
  }
  return nil
}
```

### Anti-Patterns (금지)

```go
// ❌ Magic numbers
if retries > 3 { ... }
// ✅ Named constants
const MaxRetries = 3
if retries > MaxRetries { ... }
```

```go
// ❌ Deep nesting (4단계 초과)
if a { if b { if c { if d { doSomething() } } } }
// ✅ Early return
if !a { return }
if !b { return }
doSomething()
```

```go
// ❌ else after return
if err != nil { return err } else { process() }
// ✅
if err != nil { return err }
process()
```

```go
// ❌ init() 함수 사용 (암묵적 초기화)
func init() { db = connectDB() }
// ✅ 명시적 초기화
func New() *Service { return &Service{db: connectDB()} }
```

```go
// ❌ Unnecessary variable
func isValid(s string) bool {
  result := len(s) > 0
  return result
}
// ✅
func isValid(s string) bool {
  return len(s) > 0
}
```

### Struct Design

관련 필드를 그룹핑, struct tags는 정렬:

```go
type Article struct {
  // Identity
  ID  string
  URL string

  // Content
  Title string
  Body  string

  // Metadata
  Author      string
  PublishedAt time.Time

  // Internal
  createdAt time.Time
}

// Struct tags — 정렬 필수
type ArticleDB struct {
  ID          string    `json:"id" db:"id"`
  Title       string    `json:"title" db:"title"`
  PublishedAt time.Time `json:"published_at" db:"published_at"`
}
```

### Concurrency

```go
// Channel direction 명시
func producer(out chan<- string) { out <- "data" }
func consumer(in <-chan string)  { data := <-in }

// Context 취소 항상 확인
func process(ctx context.Context) {
  for {
    select {
    case <-ctx.Done():
      return
    default:
      work()
    }
  }
}
```

---

## Comments Convention

**언어 정책:**
- 인라인 주석: **한국어 우선**, 영어 기술 용어 혼용 허용
- GoDoc (exported 심볼): **영어 우선**, 한국어 설명 아래에 추가
- 주석 원칙: **WHY만 설명** (WHAT은 코드로 표현, 당연한 내용 금지)

```go
// FetchArticle retrieves and parses an article from the given URL.
// FetchArticle은 주어진 URL에서 기사를 가져와 파싱합니다.
func FetchArticle(ctx context.Context, url string) (*Article, error) {
  // 서버 과부하 방지를 위해 재시도 전 대기
  time.Sleep(backoff)

  // DLQ로 전송 (3회 재시도 후에도 실패한 경우)
  if retries >= MaxRetries {
    sendToDLQ()
  }
}
```

**TODO 형식:**
```go
// TODO(username): rate limiter를 Redis 기반으로 변경 필요 (distributed 환경 대응)
// TODO: Kafka consumer lag이 1000 초과시 auto-scale 구현
```

---

## Error Handling

- Production 코드에서 `panic` 사용 금지 (`recover()`는 top-level handler에서만)
- 에러는 `fmt.Errorf("context: %w", err)` 로 래핑
- 에러 메시지: 소문자 시작, 마침표 없음

```go
// Good
return fmt.Errorf("failed to fetch article: %w", err)
// Bad
return fmt.Errorf("Failed to fetch article.", err)
```

### Error Categories & Codes

```
NET_001~003 : 네트워크 오류 (Connection refused, timeout, DNS)
HTTP_400~503: HTTP 상태 오류
PARSE_001~004: 파싱 오류
VAL_001~005 : 유효성 검증 오류
DB_001~004  : 데이터베이스 오류
QUEUE_001~003: 큐 오류
EMB_001~003 : 임베딩 오류
```

### Retry 원칙

| 오류 유형 | 최대 재시도 | 초기 대기 | 비고 |
|-----------|-------------|-----------|------|
| Network/Timeout | 3회 | 1초 | exponential backoff + jitter |
| RateLimit | 5회 | 10초 | exponential backoff |
| Permanent (404, 403, parse) | 0회 | — | 즉시 반환 |

---

## Logging (zerolog)

**Log Levels:**

| Level | 용도 |
|-------|------|
| `DEBUG` | 개발/진단용 상세 정보 (요청/응답, 파싱 단계) |
| `INFO` | 정상 운영 (기사 수집 성공, 시작/종료, 작업 완료) |
| `WARN` | 처리는 됐지만 비정상 (재시도, degraded) |
| `ERROR` | 작업 실패 |
| `FATAL` | 복구 불가능한 오류 (설정 오류, DB 치명 오류) |

**Structured logging 사용 (string interpolation 금지):**

```go
// Good
log.Info().
  Str("crawler", "cnn").
  Str("url", url).
  Int("status", resp.StatusCode).
  Dur("duration", elapsed).
  Msg("article fetched")

// Bad
log.Info().Msgf("Fetched %s from %s with status %d", url, "cnn", resp.StatusCode)
```

**항상 관련 컨텍스트 포함:**
```go
log.Error().
  Err(err).
  Str("article_id", article.ID).
  Str("source", article.Source).
  Str("error_code", "PARSE_001").
  Msg("parse failed")
```

---

## Testing

### Naming Pattern

```go
// Test{Function}_{Scenario}_{Expected}
func TestFetchArticle_ValidURL_ReturnsContent(t *testing.T) {}
func TestFetchArticle_InvalidURL_ReturnsError(t *testing.T) {}
func TestFetchArticle_Timeout_ReturnsTimeoutError(t *testing.T) {}
```

### Test Structure (AAA)

```go
func TestFetch_Success(t *testing.T) {
  // Arrange
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte("<html><body>Test</body></html>"))
  }))
  defer server.Close()
  crawler := NewCrawler()

  // Act
  content, err := crawler.Fetch(context.Background(), server.URL)

  // Assert
  assert.NoError(t, err)
  assert.NotNil(t, content)
}
```

### Coverage 기준

| 대상 | 최소 커버리지 |
|------|---------------|
| 핵심 패키지 | 70% |
| 크롤러/처리 로직 | 90% |
| 에러 핸들링 경로 | 100% |

### Test 파일 위치

소스 파일과 **같은 디렉토리에 두지 않음** — 별도 `test/` 디렉토리 사용:

```
test/
├── internal/
│   ├── crawler_core/
│   │   ├── errors_test.go
│   │   └── http_client_test.go
│   └── storage/
│       └── content_service_test.go
└── pkg/
    └── config/
        └── config_test.go
```

패키지 선언: `package <name>_test` (외부 테스트 패키지)

---

## Database Conventions

### SQL Style

- Keywords: UPPERCASE
- Table/Column Names: snake_case
- Indentation: 2 spaces

```sql
SELECT
  id,
  title,
  published_at
FROM articles
WHERE country = 'US'
  AND published_at > NOW() - INTERVAL '24 hours'
ORDER BY published_at DESC
LIMIT 100;
```

---

## Configuration Files (YAML)

- Indentation: 2 spaces
- Keys: snake_case
- 비자명한 값에는 단위/의미 주석 추가

```yaml
kafka:
  brokers:
    - kafka-1.example.com:9092
  topic_configs:
    default_partitions: 16
    retention_ms: 86400000  # 24 hours

sources:
  us:
    news:
      - name: cnn
        enabled: true
        rate_limit: 100  # requests per hour
```

---

## File Organization

### File Naming

- 소문자, 언더스코어 구분: `http_client.go`
- 테스트 파일: `http_client_test.go`
- 구현 종류별: `crawler_rss.go`, `crawler_html.go`

### File Structure (내부 순서)

```go
// 1. Package declaration
// 2. Imports (grouped)
// 3. Constants
// 4. Types (interfaces → structs)
// 5. Constructors (New...)
// 6. Public methods
// 7. Private methods
// 8. Helper functions
```

### Package Organization

```
internal/
└── crawler/
    ├── crawler.go       # Main interface
    ├── http.go          # HTTP implementation
    ├── rss.go           # RSS implementation
    └── crawler_test.go
```

---

## Documentation

- `README.md` (root): **영어**로 작성, 상단에 한국어 링크 포함
- 한국어 번역: `docs/ko/README.md`
- GoDoc: 영어 우선, 한국어 설명 병기
- 문서 업데이트 시 영어 먼저, 한국어 번역 후 동기화

문서 관련 커밋 메시지:
```
[DOCS]: [기능명] 문서 업데이트 (en+ko)
```

---

## Issue Convention

### 이슈 타이틀 형식

```
[{CATEGORY}] 이슈 타이틀
```

| Category | 용도 | 라벨 | 템플릿 |
|----------|------|------|--------|
| `FEATURE` | 새로운 기능 요청 및 구현 | `feature` | `feature.md` |
| `BUG` | 버그 리포트 및 수정 | `bug` | `bug.md` |
| `REFACTOR` | 코드 구조 개선, 리팩토링 | `refactor` | `refactor.md` |
| `CHORE` | 문서 작업, 의존성 업데이트 등 코드 외 부가 작업 | `chore` | `chore.md` |

**예시:**
```
[FEATURE] CNN/Naver 크롤러 구현
[BUG] rate limiter deadlock 발생
[REFACTOR] Article 검증 로직 단순화
[CHORE] go.mod 의존성 업데이트
[CHORE] 크롤러 API 문서 작성
```

### 이슈 템플릿별 작성 규칙

**FEATURE** (`.github/ISSUE_TEMPLATE/feature.md`):

| 섹션 | 설명 |
|------|------|
| 어떤 기능인가요? | 구현할 기능의 목적과 배경을 간략히 서술 |
| 무엇을 하나요? | 구현 단위를 task 체크리스트로 나열 |
| 참고 자료 | 관련 문서, 링크, 설계 자료 |

**BUG** (`.github/ISSUE_TEMPLATE/bug.md`):

| 섹션 | 설명 |
|------|------|
| 무엇을 수정하나요? | 버그 현상과 예상 동작을 서술 |
| 참고 자료 | 로그, 스크린샷, 재현 방법 |

**REFACTOR** (`.github/ISSUE_TEMPLATE/refactor.md`):

| 섹션 | 설명 |
|------|------|
| 무엇을 개선하나요? | 현재 문제점과 개선 목표를 서술 |
| 무엇을 하나요? | 개선 작업을 task 체크리스트로 나열 |
| 참고 자료 | 관련 문서, 링크 |

**CHORE** (`.github/ISSUE_TEMPLATE/chore.md`):

| 섹션 | 설명 |
|------|------|
| 어떤 작업인가요? | 작업의 목적과 배경을 간략히 서술 |
| 무엇을 하나요? | 작업 항목을 task 체크리스트로 나열 |
| 참고 자료 | 관련 문서, 링크 |

### 이슈 ↔ 브랜치 ↔ PR ↔ 커밋 연결

```
이슈: [FEATURE] CNN/Naver 크롤러 구현  (이슈 #15)
  ↓
브랜치: feature/#15/cnn-naver-crawler
  ↓
커밋: [FEAT]: CNN HTML 크롤러 구현
  ↓
PR 타이틀: [FEAT#15] CNN/Naver 크롤러 구현
PR 라벨: feature
```

> 이슈 카테고리(FEATURE)와 커밋/브랜치/PR 카테고리(FEAT)는 약어가 다를 수 있다. 위 매핑을 기준으로 맞춘다.

| 이슈 | 브랜치 prefix | 커밋/PR |
|------|---------------|---------|
| `FEATURE` | `feature/` | `FEAT` |
| `BUG` | `fix/` | `FIX` |
| `REFACTOR` | `refactor/` | `REFAC` |
| `CHORE` | `chore/` 또는 `docs/` | `CHORE` (코드 외 잡무) 또는 `DOCS` (순수 문서 작업) |

---

## Pull Request Convention

### PR 타이틀 형식

CI (`PR Title Lint`, 이슈 #121) 가 정규식으로 엄격 강제합니다:
`^\[(FEAT|FIX|REFAC|DOCS|CHORE)#[0-9]+\]:? .+`

```
[{CATEGORY}#{이슈번호}] PR 타이틀
[{CATEGORY}#{이슈번호}]: PR 타이틀   (콜론 형태도 허용)
```

| Category | 용도 |
|----------|------|
| `FEAT` | 기능 구현 및 추가 |
| `FIX` | 버그 수정 |
| `REFAC` | 구조 변경, 리팩토링 |
| `DOCS` | 문서 작업 |
| `CHORE` | 빌드·CI·도구 등 잡무 |

**통과 예시:**
```
[FEAT#15] CNN/Naver 크롤러 구현
[FEAT#15]: CNN/Naver 크롤러 구현
[FIX#7] rate limiter deadlock 수정
[REFAC#4] Article 검증 로직 단순화
[DOCS#2] 크롤러 API 문서 작성
[CHORE#117] CI golangci-lint 버전 업데이트
```

**거부 예시 (CI 실패):**
- `[FEAT]: 이슈번호 누락` — `#이슈번호` 필수
- `[FEAT 119]: # 대신 공백` — 반드시 `#` 사용
- `[FEAT#abc]: 숫자 아닌 이슈번호` — 숫자만 허용
- `[FEATXX#1]: 잘못된 카테고리` — 위 5개만 허용
- `[FEAT#1]설명` — `]` 또는 `:` 뒤 공백 필수
- `feat#1: 소문자` — 카테고리는 대문자만 허용

### PR 본문 — 템플릿 작성 규칙

PR 본문은 `.github/PULL_REQUEST_TEMPLATE.md` 폼을 그대로 사용한다.

```markdown
## 연관 이슈
- #{이슈번호}

## 구현 내용
- {변경사항 1}
- {변경사항 2}
- ...

## TODO
-

## 논의 사항
-
```

**섹션별 작성 원칙:**

| 섹션 | 규칙 |
|------|------|
| **연관 이슈** | 반드시 연관 이슈 번호 링크 (`- #15`) |
| **구현 내용** | 변경사항과 관련 핵심 함수/구조체를 명시. **반드시 작성** |
| **TODO** | 보완이 필요한 항목이 없으면 그대로 둠 (`- `) |
| **논의 사항** | 논의가 필요한 항목이 없으면 그대로 둠 (`- `) |

> **⚠️ 작업 내용(구현 내용)만 채우고, TODO·논의 사항에 기재할 내용이 없으면 빈 항목(`- `)을 지우지 않고 그대로 남긴다.**

### 라벨

PR 카테고리에 맞는 라벨을 지정:

| 라벨 | 적용 조건 |
|------|-----------|
| `feature` | FEAT 카테고리 PR |
| `bug` | FIX 카테고리 PR |
| `refactor` | REFAC 카테고리 PR |
| `documentation` | DOCS 카테고리 PR |
| `breaking change` | 하위 호환성을 깨는 변경 포함 시 추가 |
| `wip` | 작업이 완료되지 않은 draft PR |

### Tasks (체크리스트)

PR 생성 시 구현 내용에 따라 관련 task를 자동으로 추가:

**FEAT PR:**
- [ ] 인터페이스/구조체 정의 완료
- [ ] 핵심 로직 구현 완료
- [ ] 단위 테스트 작성 (커버리지 기준 충족)
- [ ] GoDoc 주석 작성 (exported 심볼)
- [ ] 통합 테스트 작성 (해당하는 경우)

**FIX PR:**
- [ ] 버그 재현 테스트 작성
- [ ] 수정 사항 구현
- [ ] 기존 테스트 통과 확인
- [ ] 회귀 테스트 추가

**REFAC PR:**
- [ ] 기존 동작 변경 없음 확인
- [ ] 기존 테스트 통과 확인
- [ ] 불필요한 코드 제거

**DOCS PR:**
- [ ] 영어 문서 작성/수정
- [ ] 한국어 번역 동기화

### PR 예시

```markdown
## 연관 이슈
- #15

## 구현 내용
- `internal/crawler/` 패키지에 CNN HTML 크롤러 구현
- `internal/crawler/` 패키지에 Naver RSS 크롤러 구현
- `Crawler` 인터페이스 기반 공통 추상화 적용
- rate limiter 연동 (token bucket, 100 req/hr)

## TODO
-

## 논의 사항
-
```

---

## Code Review Checklist

PR 제출 전 확인:

- [ ] 커밋 메시지가 한국어 + `[CATEGORY]:` 형식을 따름
- [ ] 브랜치명이 `{category}/#{issue}/{kebab-summary}` 형식을 따름
- [ ] 주석 처리된 코드 없음
- [ ] 불필요한 변수 없음
- [ ] Magic number 없음 (상수 사용)
- [ ] 에러 핸들링 완결 (`%w` 래핑 포함)
- [ ] 함수 크기 50줄 이하
- [ ] 중첩 깊이 4단계 이하
- [ ] `context.Context` 첫 번째 파라미터
- [ ] 테스트 포함 (커버리지 기준 충족)
- [ ] 로그에 관련 컨텍스트 포함
- [ ] `init()` 함수 미사용
- [ ] 코드가 자기 설명적 (WHAT 주석 금지)