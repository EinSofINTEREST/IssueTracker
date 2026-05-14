# pkg/config

IssueTracker 의 모든 환경 설정을 **도메인별 sub-package** 로 그룹화하여 관리합니다 (이슈 #440).

각 sub-package 는 자신의 도메인 Config 들 (`XxxConfig` 타입 + `DefaultXxxConfig` + `LoadXxx`) 만 노출하며, 외부 호출자는 필요한 도메인만 import 합니다.

## 디렉토리 구조

```
pkg/config/
├── app/         (package appcfg)        → log / metrics / shutdown
├── storage/     (package storagecfg)    → db / redis
├── runtime/     (package runtimecfg)    → worker_counts / stage_gate / retry_scheduler
├── fetcher/     (package fetchercfg)    → chromedp_pool / auto_downgrade / auto_upgrade
├── processor/   (package processorcfg)  → validate / scheduler / blacklist / stale_relearn
└── llm/         (package llmcfg)        → llm / prompt / classifier / path_infer / google_cse / refinement
```

## 패키지 매트릭스

| Sub-package | Configs | 책임 |
|---|---|---|
| `appcfg` | LogConfig, MetricsConfig, ShutdownConfig | 앱 라이프사이클 / 관측성 |
| `storagecfg` | DBConfig (+ `Load` alias + `DSN()` method), RedisConfig | DB / Redis 연결 |
| `runtimecfg` | WorkerCountsConfig, StageGateConfig (+ `CapPerStage`), RetrySchedulerConfig | 워커 동시성 / lock / 재시도 정책 |
| `fetchercfg` | FetcherChromedpPoolConfig, FetcherAutoDowngradeConfig, FetcherAutoUpgradeConfig | fetcher 워커 풀 / 자동 전환 |
| `processorcfg` | ValidateConfig, SchedulerConfig, BlacklistConfig, StaleRelearnConfig | parser / validator / scheduler / 블랙리스트 / stale 재학습 |
| `llmcfg` | LLMConfig (+ `lookupLLMAPIKey`), PromptConfig, ClassifierConfig, PathInferConfig, GoogleCSEConfig (+ `IsConfigured`), RefinementConfig | LLM 호출 / 프롬프트 / Google CSE / refinement |

## 패키지 네이밍 (Xxxcfg)

`internal/` 의 동명 패키지 (storage / processor / fetcher / llm) 와 충돌 회피 + Go stdlib `runtime` / `pkg/llm` 과의 collision 방지를 위해 `Xxxcfg` 접미사 채택.

## 사용 예시

```go
import (
    appcfg "issuetracker/pkg/config/app"
    storagecfg "issuetracker/pkg/config/storage"
)

logCfg, err := appcfg.LoadLog()
if err != nil { ... }

dbCfg, err := storagecfg.Load()  // DBConfig.Load alias
pool, err := pgstore.NewPool(ctx, dbCfg, log)
```

## 추가 / 변경 시 규칙

1. **새 Config 추가** — 도메인을 결정 → 해당 sub-package 의 새 파일 생성. 위 매트릭스 갱신.
2. **새 도메인 추가** — 새 sub-package 생성. 패키지명은 `Xxxcfg` 컨벤션 준수. README 매트릭스 갱신.
3. **Config 시그니처** — `type XxxConfig struct`, `func DefaultXxxConfig()`, `func LoadXxx(envFiles ...string)` 3종 세트 유지. `.env` 로드는 godotenv 패턴 일관.
4. **Cross-domain 의존성 회피** — 한 Config 가 다른 도메인 Config 를 참조하지 않도록. 필요 시 application layer (cmd / wiring) 에서 합성.

## 관련 이슈

- 이슈 #440 — 디렉토리 분리 (본 README 의 구조)
- 이슈 #439 — env 값 검증 강화 (별도 후속)
