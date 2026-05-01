# pkg/logger — Structured Logging Wrapper

소스: [`pkg/logger/logger.go`](../../../pkg/logger/logger.go)

`zerolog` 위에 얇게 wrap 한 구조화 로거. **로그 메시지 문자열은 영어**, 구조화 필드 키는 snake_case 가
규약 ([04-error-handling.md](../../../.claude/rules/04-error-handling.md) 참조).

<br>

## API

```go
log := logger.New(logger.DefaultConfig())

log.Info("crawl job started")
log.WithError(err).Error("failed to fetch")
log.WithField("url", u).Debug("fetching")
log.WithFields(map[string]interface{}{
    "job_id":  id,
    "crawler": "naver",
}).Info("dispatching job")

// Context 전파
ctx = log.ToContext(ctx)
sublog := logger.FromContext(ctx)  // ctx 안의 logger 또는 default 반환
```

<br>

## Level

| Level    | 용도                                                     |
|----------|---------------------------------------------------------|
| `debug`  | 개발 / 트러블슈팅 (운영에서 필터링)                      |
| `info`   | 정상 동작 마일스톤                                       |
| `warn`   | 예상 외 상황이지만 처리됨 (재시도 / fallback / shutdown timeout) |
| `error`  | 작업 실패 — 다른 요청은 계속 처리 가능                   |
| `fatal`  | 복구 불가 — `os.Exit(1)`                                  |

자세한 선택 기준은 [04-error-handling.md](../../../.claude/rules/04-error-handling.md) 의 표 참조.

<br>

## Context Propagation

```go
// 컴포넌트 초기화 시 scoped logger
ctx = log.WithFields(map[string]interface{}{
    "request_id": rid,
    "crawler":    name,
}).ToContext(ctx)

// 하위 함수에서:
sublog := logger.FromContext(ctx)
sublog.Info("started processing")  // 위의 fields 자동 상속
```

`shutting_down=true` 등 graceful shutdown 마커도 본 메커니즘으로 전파 (이슈 #72 / #161).

<br>

## 의존

- 외부: `github.com/rs/zerolog`
- [`pkg/config`](config.md) — `LogConfig` (level / pretty)

<br>

## 호출 측

거의 모든 패키지. nil 허용 옵션 의존인 경우도 다수.

<br>

## 관련 이슈

- 이슈 #72 — graceful shutdown shutting_down 필드
- 이슈 #161 — shutdown 로그 정합성
