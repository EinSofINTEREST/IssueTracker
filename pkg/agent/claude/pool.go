package claude

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/agent"
	agentdb "issuetracker/pkg/agent/dependency/db"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// 이슈 #352 — claude Worker pool.
//
// 라이브 분석 (2026-05-11 22:39~) 6시간 누적 검증 실패율 88% 중 핵심 사유는 published_at
// selector 부재 (1174건 / 83%). LLM rule generator throughput 을 높여 stale/missing selector
// 의 자동 학습 + 정밀화 속도를 끌어올리는 것이 본 이슈의 목적.
//
// 본 파일은 Worker 를 N replica pool 로 확장 — concurrent Extract 호출을 round-robin
// 분배하여 단일 컨테이너 직렬화 병목 해소.

const (
	// envWorkerCount: pool size 환경변수. 0 / 미설정 → defaultWorkerCount.
	envWorkerCount     = "CLAUDE_CODE_WORKER_COUNT"
	defaultWorkerCount = 2
	// maxWorkerCount: docker 컨테이너 누수 / claude API quota 폭발 방지용 상한.
	// 운영자가 강제로 더 큰 값을 지정하면 maxWorkerCount 로 clamp.
	maxWorkerCount = 16
)

// PoolConfig 는 agent.PoolConfig type alias 입니다 (이슈 #530 재설계).
//
// claude 전용 옵션이 추가 필요한 경우 본 alias 를 별도 struct 로 분리하고 agent.PoolConfig 를
// embed 하면 됩니다. 현재는 Name 만 사용 — agent 공용 struct 를 그대로 노출.
//
// 우선순위 (예 Name="parser"):
//  1. PARSER_CLAUDE_CODE_WORKER_COUNT — stage 별 명시
//  2. CLAUDE_CODE_WORKER_COUNT — fallback
//  3. defaultWorkerCount
//
// 같은 정책이 CLAUDE_CODE_TIMEOUT / IMAGE / MODEL 등에도 적용 — agent.StageEnv 참조.
type PoolConfig = agent.PoolConfig

// Pool 은 Worker N replica 를 round-robin 분배로 사용하는 풀입니다.
//
// 모든 worker 가 동일한 환경 (image / model / authDir / timeout) 을 공유하므로 외부에서
// 본 풀은 단일 SelectorExtractor / EnrichedExtractor 처럼 보입니다 — interface 호환을 통해
// llmgen.Generator.SetExtractor 가 그대로 사용 가능.
//
// 분배 정책: round-robin (atomic.Uint64 의 modulo). 단순성 우선 — idle-first 분배는 운영
// 검증 후 별도 이슈에서 도입.
//
// goroutine-safe: nextIdx 가 atomic, 각 worker 가 자체 mu/wg 로 동시 호출 safe.
type Pool struct {
	name    string // stage 식별자 — 로깅용 (이슈 #530). 빈 문자열이면 미부착.
	workers []*Worker
	nextIdx atomic.Uint64
	log     *logger.Logger
}

// Name 은 pool 의 stage 식별자를 반환합니다 — 진단/메트릭 용. 빈 문자열이면 단일 풀.
func (p *Pool) Name() string { return p.name }

// NewPool 은 주어진 worker slice 로 Pool 을 생성합니다 (DI 용).
//
// workers 빈 slice 시 error. log nil 시 error.
// 모든 worker 가 동일한 model 사용을 가정 — ModelName() 은 workers[0].ModelName() 반환.
func NewPool(workers []*Worker, log *logger.Logger) (*Pool, error) {
	if log == nil {
		return nil, errors.New("claude: NewPool requires non-nil logger")
	}
	if len(workers) == 0 {
		return nil, errors.New("claude: NewPool requires at least 1 worker")
	}
	for i, w := range workers {
		if w == nil {
			return nil, fmt.Errorf("claude: NewPool workers[%d] is nil", i)
		}
	}
	return &Pool{workers: workers, log: log}, nil
}

// NewPoolFromEnv 는 단일 풀 호환 진입점 — 기존 호출처 보존용 (이슈 #530 이전 시그니처).
//
// 새 코드는 NewPoolFromConfig(PoolConfig{Name: "<stage>"}, ...) 사용 권장.
// 본 함수는 PoolConfig{Name: ""} 로 위임 — CLAUDE_CODE_* env 만 lookup.
func NewPoolFromEnv(loader prompt.Loader, log *logger.Logger) (*Pool, error) {
	return NewPoolFromConfig(PoolConfig{}, loader, log)
}

// NewPoolFromConfig 는 PoolConfig 기반으로 stage 별 Pool 을 구성합니다 (이슈 #530).
//
// cfg.Name 이 비어 있으면 NewPoolFromEnv 와 동일 동작 (단일 풀). 명시 시 stage prefix 환경
// 변수가 우선 적용됩니다 — `<NAME>_CLAUDE_CODE_*` 이 있으면 사용, 없으면 `CLAUDE_CODE_*` fallback.
//
// 각 worker 도 동일 stage prefix 환경 변수로 구성 (image / model / authDir / timeout 등).
// MCPConfig 는 별도 — main wiring 에서 stage 의도에 맞게 WithMCPConfig 호출.
func NewPoolFromConfig(cfg PoolConfig, loader prompt.Loader, log *logger.Logger) (*Pool, error) {
	if log == nil {
		return nil, errors.New("claude: NewPoolFromConfig requires non-nil logger")
	}
	if loader == nil {
		return nil, errors.New("claude: NewPoolFromConfig requires non-nil prompt loader")
	}
	envR := agent.NewStageEnv(cfg.Name)
	poolLog := log
	if cfg.Name != "" {
		poolLog = log.WithField("agent_pool", cfg.Name)
	}
	count := resolveWorkerCount(envR, poolLog)
	workers := make([]*Worker, 0, count)
	for i := 0; i < count; i++ {
		w, err := newWorkerFromStageEnv(envR, loader, poolLog)
		if err != nil {
			return nil, fmt.Errorf("claude: NewPoolFromConfig worker[%d]: %w", i, err)
		}
		workers = append(workers, w)
	}
	pool, err := NewPool(workers, poolLog)
	if err != nil {
		return nil, err
	}
	pool.name = cfg.Name
	poolLog.WithFields(map[string]interface{}{
		"worker_count": count,
	}).Info("claude worker pool constructed (containers not started yet)")
	return pool, nil
}

// resolveWorkerCount 는 stage prefix 우선 + base fallback 으로 WorkerCount 를 파싱합니다.
// [1, maxWorkerCount] 로 clamp. parse 실패 / 음수 / 0 시 default 적용 + WARN.
func resolveWorkerCount(envR agent.StageEnv, log *logger.Logger) int {
	raw, key := envR.Get(envWorkerCount)
	if raw == "" {
		return defaultWorkerCount
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"env":   key,
			"value": raw,
		}).WithError(err).Warn("worker count parse failed, using default")
		return defaultWorkerCount
	}
	if n <= 0 {
		log.WithFields(map[string]interface{}{
			"env":   key,
			"value": n,
		}).Warn("worker count must be positive, using default")
		return defaultWorkerCount
	}
	if n > maxWorkerCount {
		log.WithFields(map[string]interface{}{
			"env":   key,
			"value": n,
			"cap":   maxWorkerCount,
		}).Warn("worker count exceeds upper bound, clamping")
		return maxWorkerCount
	}
	return n
}

// WithMCPConfig 는 풀의 모든 worker 에 동일한 MCPConfig 를 등록합니다 (이슈 #472).
//
// Start() 전에 1회 호출. nil 전달 시 비활성. 본 메소드는 thread-unsafe — 풀 기동 후 호출 금지.
func (p *Pool) WithMCPConfig(c *agentdb.MCPConfig) *Pool {
	for _, w := range p.workers {
		w.WithMCPConfig(c)
	}
	return p
}

// ModelName 은 pool 의 첫 worker 모델 ID 를 반환합니다 — 모든 worker 가 동일 환경 가정.
func (p *Pool) ModelName() string {
	return p.workers[0].ModelName()
}

// WorkerCount 는 pool 크기를 반환합니다 — 운영 진단용.
func (p *Pool) WorkerCount() int {
	return len(p.workers)
}

// Start 는 모든 worker 컨테이너를 병렬로 기동합니다.
//
// 일부 worker 실패 시: 이미 성공한 worker 들을 best-effort 로 Stop 후 첫 에러 반환.
// 운영자가 부분 기동 상태를 보지 않도록 — 전부 가동 또는 전부 종료의 두 상태만 노출.
func (p *Pool) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, len(p.workers))
	for i, w := range p.workers {
		wg.Add(1)
		go func(idx int, worker *Worker) {
			defer wg.Done()
			errs[idx] = worker.Start(ctx)
		}(i, w)
	}
	wg.Wait()

	// 부분 실패 시 cleanup
	var firstErr error
	failedAny := false
	for i, err := range errs {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("claude: pool worker[%d] start failed: %w", i, err)
		}
		if err != nil {
			failedAny = true
		}
	}
	if failedAny {
		// 이미 성공한 워커들을 병렬 best-effort 로 Stop — 직렬 cleanup 시 앞 worker 의 timeout 이
		// 단일 cleanupCtx 를 소진하여 뒤 worker cleanup 이 ctx.Done 으로 실패하는 누수 회피 (Copilot 반영).
		// worker 별로 독립 timeout 부여.
		var cwg sync.WaitGroup
		for i, w := range p.workers {
			if errs[i] == nil {
				cwg.Add(1)
				go func(idx int, worker *Worker) {
					defer cwg.Done()
					cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), defaultSessionTimeout)
					defer cleanupCancel()
					if stopErr := worker.Stop(cleanupCtx); stopErr != nil {
						p.log.WithFields(map[string]interface{}{
							"worker_idx": idx,
						}).WithError(stopErr).Warn("partial-start cleanup: worker stop failed")
					}
				}(i, w)
			}
		}
		cwg.Wait()
		return firstErr
	}
	p.log.WithFields(map[string]interface{}{
		"worker_count": len(p.workers),
	}).Info("claude worker pool started (warm containers)")
	return nil
}

// Stop 은 모든 worker 컨테이너를 병렬로 정리합니다.
//
// 각 worker.Stop 이 자체 wg.Wait 으로 in-flight Extract 완료를 보장. 일부 실패는 첫 에러
// 반환 + 나머지는 best-effort 시도.
func (p *Pool) Stop(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, len(p.workers))
	for i, w := range p.workers {
		wg.Add(1)
		go func(idx int, worker *Worker) {
			defer wg.Done()
			errs[idx] = worker.Stop(ctx)
		}(i, w)
	}
	wg.Wait()

	var firstErr error
	for i, err := range errs {
		if err != nil {
			p.log.WithFields(map[string]interface{}{
				"worker_idx": i,
			}).WithError(err).Warn("claude pool worker stop failed")
			if firstErr == nil {
				firstErr = fmt.Errorf("claude: pool worker[%d] stop failed: %w", i, err)
			}
		}
	}
	if firstErr == nil {
		p.log.Info("claude worker pool stopped")
	}
	return firstErr
}

// Extract 는 round-robin 으로 worker 를 선택해 Extract 위임합니다.
//
// SelectorExtractor 인터페이스 호환 — llmgen.Generator.SetExtractor 가 본 메소드를 호출.
func (p *Pool) Extract(ctx context.Context, host string, targetType model.TargetType, html string) (model.SelectorMap, error) {
	return p.pick().Extract(ctx, host, targetType, html)
}

// ExtractEnriched 는 round-robin 으로 worker 를 선택해 ExtractEnriched 위임합니다.
//
// EnrichedExtractor 인터페이스 호환 — llmgen.Generator 가 type assertion 으로 자동 분기.
func (p *Pool) ExtractEnriched(ctx context.Context, host string, targetType model.TargetType, html string) (*llmgen.ExtractResult, error) {
	return p.pick().ExtractEnriched(ctx, host, targetType, html)
}

// RunSession 은 round-robin worker 선택 후 RunSession 위임 (이슈 #447).
//
// enrich.Extractor 구현체 (extractor/claudegen.go) 가 호출하는 진입점.
func (p *Pool) RunSession(
	ctx context.Context,
	sessionLabel string,
	files map[string][]byte,
	promptText string,
) (string, error) {
	return p.pick().RunSession(ctx, sessionLabel, files, promptText)
}

// pick 는 round-robin 분배로 다음 worker 를 선택합니다.
//
// atomic.Uint64 의 ADD-AND-MODULO 패턴 — lock-free + counter wrap-around 시 modulo 정상 동작.
// 분배 균등성은 worker 수가 2^N 일 때 가장 좋고 그 외에도 bias 가 최소.
func (p *Pool) pick() *Worker {
	idx := p.nextIdx.Add(1) - 1 // Add 가 갱신 후 값 반환 — 0-base index 로 -1
	return p.workers[idx%uint64(len(p.workers))]
}

// 컴파일 타임 contract — pool 이 두 stage 인터페이스 + pkg/agent.Agent 를 구현하는지 검증 (이슈 #460).
var (
	_ llmgen.SelectorExtractor = (*Pool)(nil)
	_ llmgen.EnrichedExtractor = (*Pool)(nil)
	_ agent.Agent              = (*Pool)(nil)
)
