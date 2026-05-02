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

// Downgrader 는 자동 upgrade 된 chromedp host 를 주기적으로 goquery 로 reset 하는 cron 입니다 (이슈 #175 후속, sub-issue #224).
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
// repo / resolver 는 nil 허용 안 함 (이슈 #208 정책).
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

// Run 은 일회성 다운그레이드를 즉시 실행합니다 (테스트용 + Start 의 첫 사이클 진입점).
//
// BulkDowngradeAutoUpgraded → 변경된 host 들 Resolver.Invalidate. 모든 단계 실패는 non-fatal —
// warn 로그만 남기고 다음 주기에 자연 retry.
func (d *Downgrader) Run(ctx context.Context) {
	changed, err := d.repo.BulkDowngradeAutoUpgraded(ctx)
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
// 첫 사이클은 interval 만큼 대기 후 실행 — 부팅 직후 즉시 reset 발생 회피 (운영자가 manual
// inspect 할 시간 확보). 즉시 실행이 필요하면 호출자가 별도로 Run 호출.
func (d *Downgrader) Start(ctx context.Context) {
	if d.log != nil {
		d.log.WithField("interval", d.interval.String()).Info("downgrader started")
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
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
