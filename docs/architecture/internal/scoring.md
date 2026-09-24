# internal/scoring — Host 단위 Priority Score

소스: [`internal/scoring/`](../../../internal/scoring/)

관찰된 signal 로 host 별 점수를 계산해 **High ↔ Normal 우선순위를 자동 분기** 합니다
(이슈 #382, 메타 #380 Sub B). 기본 비활성 — `HOST_SCORING_ENABLED=true` 여야 동작합니다.

<br>

## 범위 — Low 는 만들지 않는다

점수가 낮아도 **Normal 까지만** 내려갑니다. Low 강등은 운영자의 명시적 결정이어야 합니다.

자동 강등하면 일시적 품질 저하가 host 를 저우선으로 묶고, **그 상태에서 수집이 줄어
신호가 갱신되지 않는 되먹임** 이 생깁니다. Low 는 DB `crawl_priority` (이슈 #521) 또는
`priority_config.base_priority` (이슈 #383) 로만 지정합니다.

<br>

## 3 signal 로 시작한 이유

원 설계 (이슈 #382) 는 5개 signal 을 들었으나 `cost` / `reliability` 를 제외했습니다.
두 신호의 출처인 Prometheus metrics 가

- 앱에 **읽기 경로가 없고** (`Gather()` 사용처 0),
- 필요한 것이 현재 값이 아니라 **구간 rate** 이며 (누적 counter 를 차분해야 함),
- **인스턴스 로컬** 이라 여러 인스턴스에서 host 점수가 갈립니다 (이슈 #289 와 얽힘).

골격을 먼저 세우고 #289 결정 후 얹는 편이 되돌리기 쉽다는 판단입니다.

| Signal | 의미 | 출처 |
|---|---|---|
| `freshness` | publish → detect 지연이 짧을수록 높음 | `contents.published_at` vs `created_at` |
| `impact` | 카테고리 가중치 (정치/경제 高) | `contents.category` 최빈값 |
| `host_trust` | validation 통과율 | `contents.validation_status` |

<br>

## 패키지 구성

| 파일 | 역할 |
|---|---|
| [`score.go`](../../../internal/scoring/score.go) | `Score` — 가중 합산 + `[0,1]` clamp. `MergeWeights` — host weight 를 cluster 값 위에 덮음 |
| [`scorer.go`](../../../internal/scoring/scorer.go) | 주기 goroutine — 집계 → 점수 → 저장 → 스냅샷 공급 |
| [`adapter.go`](../../../internal/scoring/adapter.go) | postgres 집계기를 `Aggregator` 인터페이스로 연결 |

집계 SQL 은 [`internal/storage/postgres/host_signals.go`](../../../internal/storage/postgres/host_signals.go),
상태 저장은 [`host_scoring.go`](../../../internal/storage/postgres/host_scoring.go) 입니다.

<br>

## 핵심 정책

### 표본 부족은 점수를 내지 않는다

`MinSampleCount = 20` 미만은 cold-start 로 보고 스냅샷에서 제외 → resolver 의
`CanResolve` 가 false → chain 이 기존 경로에 위임합니다.

기사 2건으로 계산된 `host_trust = 1.0` 이 High 로 올라가면 **노이즈가 곧 우선순위** 가
됩니다.

### 실패 시 이전 스냅샷 유지

| 실패 | 처리 | 이유 |
|---|---|---|
| 집계 실패 | 이전 스냅샷 유지 (`SetScores` 미호출) | 비우면 DB 일시 장애가 전체 host 의 우선순위 변동으로 번진다 |
| 저장 실패 | 그 host 만 WARN, 스냅샷에는 유지 | 점수는 메모리에서 유효하고 다음 주기에 재시도. 쓰기 장애가 기능 전체를 멈추지 않도록 |
| 가중치 전부 0 | **기동 실패** | 조용히 0 을 돌려주면 "켰는데 효과가 없다" 는 진단 불가 상태가 된다 |

### 중립값은 0.5

`published_at` 부재 / 판정 완료 건 없음 / 미분류 카테고리는 모두 **0.5** 입니다.
1 을 주면 정보를 노출하지 않는 host 가 우대되고, 0 을 주면 신규 host 가 영구히 불리해집니다.

그래서 threshold 기본값이 0.5 가 아니라 **0.7** 입니다 — 중립값으로 채워진 host 가
경계에서 흔들리지 않도록 명확히 좋은 host 만 승급시킵니다.

<br>

## chain 위치

```
Explicit → Override(#383) → Source → RuleBased(DB crawl_priority) → Scoring → default Normal
```

`RuleBased` **뒤** 입니다. 운영자가 DB 에 명시한 host 는 그쪽에서 확정되고, scoring 은
명시가 없어 Normal 로 흐르던 host 만 다룹니다. 앞에 두면 관찰 신호가 운영자의 명시를
뒤집습니다.

resolver 는 [`internal/bus/score_resolver.go`](../../../internal/bus/score_resolver.go) 의
`DynamicScorePriorityResolver` 이며, publish hot path 라 **in-memory 스냅샷 + atomic 교체**
를 씁니다 — host 마다 DB 를 왕복할 수 없습니다.

<br>

## 환경 변수

`HOST_SCORING_ENABLED` / `_INTERVAL` / `_WINDOW_MINUTES` / `_THRESHOLD` /
`_W_{FRESHNESS,IMPACT,TRUST}`. 상세는 [`.env.example`](../../../.env.example) 참조.

host 별 weight / threshold override 는 `fetcher_rules.priority_config` (이슈 #383).

<br>

## 관련 이슈

- **이슈 #382 — 본 패키지 도입** (메타 #380 Sub B)
- 이슈 #383 — per-host weight / threshold override
- 이슈 #521 — DB `crawl_priority` (Low 결정 주체)
- 이슈 #289 — 멀티 인스턴스 (cost / reliability signal 의 선행 조건)
- 이슈 #612 / #614 — 집계 SQL · repository 통합 테스트
