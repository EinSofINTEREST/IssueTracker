package rule

import (
	"context"
	"errors"
	"sync"
	"time"

	"issuetracker/internal/processor"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// downgraderStageName 은 processor.Stage 식별자입니다.
const downgraderStageName = "fetcher-downgrader"

// downgraderInitialGrace 는 부팅 직후 첫 실행을 지연시키는 grace 입니다 (gemini 피드백).
//
// 의도:
//
//	부팅 직후 즉시 downgrade 발생을 회피 (운영자 manual inspect 시간 확보) + 7일 같은 긴
//	interval 환경에서 배포 주기보다 길어 cron 이 영영 발동 안 되는 시나리오 차단. 1분이면
//	부팅 wiring 충돌 회피 + 운영자 reaction time 모두 충족.
const downgraderInitialGrace = 1 * time.Minute

// Downgrader 는 자동 upgrade 된 chromedp host 를 주기적으로 goquery 로 reset 하는 cron 입니다.
//
// 동기:
//
//	upgrade-only 정책의 비대칭 보완 — 일시적 트래픽 에러로 잘못 upgrade 된 host 가 영원히 chromedp
//	로 처리되어 시간 누적 시 모든 host 가 chromedp 수렴 → Chrome 자원 압박 회피.
//
// 동작 (주기 default 7일):
//
//  1. FetcherRuleRepository.BulkDowngradeAutoUpgraded — reason='auto_upgrade_validation' AND
//     fetcher='chromedp' row 일괄 goquery 로 UPDATE. manual 룰 / 미래 자동 reason 영향 없음.
//  2. 변경된 host 슬라이스를 받아 Resolver.Invalidate(host) 로 cache 동기화.
//
// 진짜 SPA host 는 단계 2 의 카운터가 임계값 도달 시 단계 3 의 Upgrader 가 24h 내 재upgrade —
// 본 cron 은 "잘못된 upgrade" 만 자연 해소.
//
// processor.Stage 인터페이스 구현 — main.go 의 stages slice 에 다른 단계와 균일하게 등록.
type Downgrader struct {
	repo     storage.FetcherRuleRepository
	resolver Resolver
	interval time.Duration
	log      *logger.Logger

	wg sync.WaitGroup
}

// NewDowngrader 는 Downgrader 를 생성합니다.
//
// repo / resolver 는 nil 허용 안 함.
// interval 은 0 또는 음수 시 error — 호출자가 ENABLED=false 분기로 처리해야 함.
func NewDowngrader(
	repo storage.FetcherRuleRepository,
	resolver Resolver,
	interval time.Duration,
	log *logger.Logger,
) (*Downgrader, error) {
	if repo == nil {
		return nil, errors.New("rule: NewDowngrader requires non-nil FetcherRuleRepository")
	}
	if resolver == nil {
		return nil, errors.New("rule: NewDowngrader requires non-nil Resolver")
	}
	if interval <= 0 {
		return nil, errors.New("rule: NewDowngrader requires positive interval")
	}
	return &Downgrader{
		repo:     repo,
		resolver: resolver,
		interval: interval,
		log:      log,
	}, nil
}

// Name 은 stage 식별자 ("fetcher-downgrader") 를 반환합니다.
func (d *Downgrader) Name() string { return downgraderStageName }

// Run 은 일회성 다운그레이드를 즉시 실행합니다 (테스트용 + Start 의 ticker 진입점).
//
// BulkDowngradeAutoUpgraded → 변경된 host 들 Resolver.Invalidate. 모든 단계 실패는 non-fatal —
// warn 로그만 남기고 다음 주기에 자연 retry.
//
// context.WithoutCancel: parent ctx 가 shutdown 으로 cancel 되어도 본 작업은 끝까지 진행
// (gemini 피드백). 단일 UPDATE 는 1-2초 안에 끝나서 Stop 30s timeout 안에 충분히 wait.
// logger / trace_id 등 ctx value 는 보존되어 운영 가시성 확보.
func (d *Downgrader) Run(ctx context.Context) {
	runCtx := context.WithoutCancel(ctx)
	changed, err := d.repo.BulkDowngradeAutoUpgraded(runCtx)
	if err != nil {
		if d.log != nil {
			d.log.WithError(err).Warn("downgrader bulk update failed (non-fatal)")
		}
		return
	}
	if len(changed) == 0 {
		if d.log != nil {
			d.log.Debug("downgrader: no auto-upgraded rules to reset")
		}
		return
	}
	for _, host := range changed {
		d.resolver.Invalidate(host)
	}
	if d.log != nil {
		d.log.WithFields(map[string]interface{}{
			"changed_count": len(changed),
			"interval":      d.interval.String(),
		}).Info("downgrader reset auto-upgraded chromedp rules to goquery")
	}
}

// Start 는 ctx 가 cancel 될 때까지 interval 주기로 Run 을 실행합니다 (non-blocking).
//
// 첫 사이클은 grace 후 실행 → 그 후 interval 주기 (gemini 피드백).
// grace = min(interval, downgraderInitialGrace) — 운영 환경 (interval >> 1분) 에서는 1분 grace
// 로 운영자 manual inspect 시간 확보, 테스트 환경 (interval < 1분) 에서는 interval 만큼만 대기
// 해 ticker 첫 발동을 차단하지 않음. interval 7일 환경에서 배포 주기가 더 짧아 cron 이 영영
// 발동 안 되는 시나리오도 1분 grace 로 차단 (CodeRabbit 가설 cover).
func (d *Downgrader) Start(ctx context.Context) {
	grace := downgraderInitialGrace
	if d.interval < grace {
		grace = d.interval
	}
	if d.log != nil {
		d.log.WithFields(map[string]interface{}{
			"interval":      d.interval.String(),
			"initial_grace": grace.String(),
		}).Info("downgrader started")
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		// 첫 사이클: grace 후 즉시 실행 (또는 ctx cancel 시 종료).
		select {
		case <-ctx.Done():
			return
		case <-time.After(grace):
			d.Run(ctx)
		}

		// 이후 사이클: interval 주기.
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.Run(ctx)
			}
		}
	}()
}

// Stop 은 Run goroutine 의 종료를 대기합니다 (호출 전 ctx cancel 필수).
//
// shutdownCtx 가 timeout 되면 wait 포기 — 호출자가 외부에서 timeout 관리.
func (d *Downgrader) Stop(shutdownCtx context.Context) error {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if d.log != nil {
			d.log.Info("downgrader stopped")
		}
		return nil
	case <-shutdownCtx.Done():
		if d.log != nil {
			d.log.Warn("downgrader stop timeout")
		}
		return shutdownCtx.Err()
	}
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Downgrader)(nil)
