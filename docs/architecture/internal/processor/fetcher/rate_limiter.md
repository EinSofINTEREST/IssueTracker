# internal/processor/fetcher/rate_limiter — IP-Based Token Bucket

소스: [`internal/processor/fetcher/rate_limiter/`](../../../../internal/processor/fetcher/rate_limiter/)

URL → IP 해석 후 **per-IP token bucket** 으로 fetch rate 를 제한합니다. 동일 호스팅 사이트 (CDN
공유 등) 가 IP 단위로 묶이므로 host 단위보다 안정적입니다.

<br>

## 구성

| 파일                                                                          | 역할                                                  |
|------------------------------------------------------------------------------|-------------------------------------------------------|
| [token_bucket.go](../../../../internal/processor/fetcher/rate_limiter/token_bucket.go)  | `TokenBucketRateLimiter` (구현) — `core.RateLimiter` 만족 |
| [dns_resolver.go](../../../../internal/processor/fetcher/rate_limiter/dns_resolver.go)  | host → IP 해석 + 캐시                                  |
| [ip_registry.go](../../../../internal/processor/fetcher/rate_limiter/ip_registry.go)    | `IPRegistry` — IP → bucket 매핑 (lazy 생성)            |

<br>

## 사용

```go
limiter := rate_limiter.NewRateLimiter(requestsPerHour, burst)  // → core.RateLimiter
// fetcher 가 매 요청 전에:
limiter.Wait(ctx)
```

`burst` 는 단기 spike 허용량. requestsPerHour 는 사이트별 [`config.go`](../../../../internal/processor/fetcher/domain/general/sources/) 에서
지정 (예: Naver 200/h, CNN 100/h).

<br>

## 의존

- [`internal/processor/fetcher/core`](core.md) — `RateLimiter` 인터페이스
- 외부: standard `net` (DNS), `time`

<br>

## 한계 / TODO

- 분산 환경에서는 인스턴스마다 별도 bucket 보유 → 실제 RPS = (인스턴스 수) × (bucket 한도)
- Redis 기반 분산 토큰 버킷은 백로그 (장기적으로 IP shared 라면 필요)
