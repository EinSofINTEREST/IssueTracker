# Git Conventions

## Commit Message Format

```
[{카테고리}]: {변경 내용}
```

### 카테고리 (Categories)

- **FEAT**: feature, 기능 구현 및 추가
- **FIX**: fix, 버그 수정
- **REFAC**: refactor, 구조 변경, 메소드 구조 변경 및 리팩토링
- **DOCS**: documentation, 문서 작업 및 프롬프트 변경, 주석 등 설명 요소 작성

### 변경 내용 작성 규칙

**⚠️ 중요: 모든 커밋 메시지는 한국어로 작성해야 합니다.**

1. **언어**: 반드시 한국어로 작성 (영어 사용 금지)
2. **형식**: 명사형 종결 (예: "구현", "수정", "추가")
3. **내용**: 변경 내용의 전체적인 요약, 각 모듈 단위의 변경점을 명확히 기술

### Commit Message Examples

#### 기능 구현 및 추가 (FEAT)
```bash
[FEAT]: Kafka consumer pool을 활용한 크롤러 워커 구현

- KafkaConsumerPool 구조체를 통한 다중 워커 goroutine 관리
- graceful shutdown 지원
- 설정 가능한 워커 개수 구현
```

```bash
[FEAT]: Reddit 크롤러를 활용한 미국 커뮤니티 데이터 수집

- RSS feed 기반 Reddit 크롤러 구현
- r/news, r/worldnews, r/politics 서브레딧 지원
```

```bash
[FEAT]: CNN 크롤러 구현

- HTML 파싱 기반 CNN 뉴스 크롤러 구현
- 기사 title, body, publishedAt 추출
- rate limiting 적용
```

#### 버그 수정 (FIX)
```bash
[FIX]: HTTP 클라이언트 timeout 에러 처리 개선

- timeout 발생 시 적절한 에러 핸들링 로직 추가
- exponential backoff 기반 재시도 로직 구현
- 느린 소스에서 크롤러 hang 방지
```

```bash
[FIX]: Rate limiter deadlock 문제 해결

- mutex lock 순서 변경
- context cancellation 처리 개선
```

#### 구조 변경 및 리팩토링 (REFAC)
```bash
[REFAC]: Article 검증 로직 단순화

- 불필요한 검증 단계 제거
- 관련 검증 로직 통합
- 함수 복잡도 25줄에서 15줄로 감소
```

```bash
[REFAC]: Parser 인터페이스 추출

- 공통 파싱 로직을 인터페이스로 분리
- CNN, Naver 파서가 동일 인터페이스 구현
```

#### 문서 작업 (DOCS)
```bash
[DOCS]: 크롤러 API 문서 및 사용 예제 작성

- 크롤러 인터페이스 GoDoc 주석 추가
- examples/basic_usage.go 작성
- README.md에 Quick Start 섹션 추가
```

```bash
[DOCS]: Git conventions 작성 규칙 추가

- .cursor/ 디렉토리에 git-conventions.md 추가
- commit 및 branch naming 규칙 정리
```

## Branch Naming Convention

```
{카테고리}/{간단한-설명}
```

### 브랜치 카테고리

- **feat/**: 새로운 기능 구현 및 추가
- **fix/**: 버그 수정
- **refac/**: 구조 변경 및 리팩토링
- **docs/**: 문서 작업

### 브랜치명 작성 규칙

1. **형식**: 영문 소문자, 단어 구분은 하이픈(-)
2. **길이**: 간결하게, 50자 이내
3. **내용**: 작업 내용을 명확히 표현

### Branch Name Examples

#### 기능 구현 (feat/)
```bash
feat/kafka-consumer-pool
feat/reddit-crawler
feat/cnn-crawler-implementation
feat/naver-news-crawler
feat/embedding-generation
feat/clustering-algorithm
```

#### 버그 수정 (fix/)
```bash
fix/http-timeout-handling
fix/rate-limiter-deadlock
fix/duplicate-article-detection
fix/parsing-encoding-error
fix/memory-leak-worker-pool
```

#### 리팩토링 (refac/)
```bash
refac/validation-logic
refac/simplify-error-handling
refac/extract-parser-interface
refac/improve-test-coverage
refac/migrate-standard-layout
```

#### 문서 작업 (docs/)
```bash
docs/api-documentation
docs/update-readme
docs/add-crawler-guide
docs/git-conventions
docs/architecture-diagram
```

## Workflow

### 1. Branch 생성
```bash
# feature 개발
git checkout -b feat/feature-name

# bug 수정
git checkout -b fix/bug-name

# refactoring
git checkout -b refac/refactor-name

# documentation
git checkout -b docs/doc-name
```

### 2. 작업 및 Commit
```bash
# 작업 완료 후 staging
git add .

# commit message 작성
git commit -m "[FEAT]: 기능 설명

- 세부 변경사항 1
- 세부 변경사항 2
- 세부 변경사항 3"
```

### 3. Push 및 PR 생성
```bash
# remote에 push
git push origin feat/feature-name

# GitHub에서 Pull Request 생성
# PR 제목: [FEAT]: 기능 설명
```

## Best Practices

### Commit 작성 시
- 한 commit은 하나의 논리적 변경사항만 포함
- commit message는 명확하고 구체적으로
- 변경된 모듈/파일별로 세부사항 기술

### Branch 작성 시
- branch는 작업 단위별로 생성
- main/master에서 직접 작업 금지
- 작업 완료 후 branch 삭제

### Code Review
- PR 생성 시 충분한 설명 포함
- reviewer의 피드백 반영
- approve 후 merge
