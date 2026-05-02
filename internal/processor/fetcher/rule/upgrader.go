package rule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// MetadataKeyForceFetcher 는 단계 3 의 republish CrawlJob 이 ChainHandler 에 chromedp 강제
// 사용을 지시할 때 Target.Metadata 에 사용하는 키 (이슈 #221).
//
// 같은 키가 internal/processor/fetcher/domain/general/chain_handler.go 에도 정의되어 있으나
// import cycle 회피를 위해 본 패키지에 별도 const 유지. 두 값이 동일 문자열임을 두 패키지의
// 경계에서 보장.
const MetadataKeyForceFetcher = "force_fetcher"

const (
	// upgraderInflightTTL: 같은 host 의 동시 trigger 가 1회만 실행되도록 잡는 SETNX lock 의 TTL.
	// 너무 짧으면 trigger 중간에 lock 만료 → 중복 republish. 너무 길면 다음 정당한 trigger 가 차단됨.
	// 단계 3 의 trigger 작업량 (fetcher_rules UPSERT + 최대 20건 republish) 에 충분한 5분.
	upgraderInflightTTL = 5 * time.Minute

	// upgraderMaxRepublishPerCycle: 단일 trigger 사이클의 max republish 수.
	// 한 host 에 잔존 raw 100건이 있어도 일괄 republish 시 fetcher pool 폭주 회피.
	// 나머지는 다음 trigger / 재카운팅 사이클이 자연스럽게 처리.
	upgraderMaxRepublishPerCycle = 20

	// republishedJobTimeout: republish 한 CrawlJob 의 fetch timeout. fetcher chromedp 의 일반값 (#146 설정).
	republishedJobTimeout = 30 * time.Second
)

// Upgrader 는 임계값 도달 host 를 chromedp 로 자동 전환하고 실패 raw 를 republish 합니다 (이슈 #175 단계 3).
//
// 호출 흐름 (parser_worker 가 thresholdReached=true 받으면 Trigger 호출):
//
//  1. Redis SETNX 로 host 단위 in-flight lock — 동시 trigger 의 race / flooding 차단.
//  2. fetcher_rules.GetByHost: 이미 chromedp 면 audit warn 후 skip (chromedp 도 실패하는 host 신호 — 운영자 개입 영역).
//  3. fetcher_rules.Upsert(host, chromedp, "auto_upgrade_validation").
//  4. Resolver.Invalidate(host) — cache 동기화.
//  5. RawIDTracker.PopByHost(host, max=20) — 실패 raw 수집.
//  6. 각 raw 에 대해: RawContentService.GetByID 로 URL 조회 → CrawlJob 생성 (Target.Metadata force_fetcher=chromedp, Headers={retry_reason, original_raw_id}) → Producer.PublishBatch.
//  7. lock 자동 만료 (TTL).
//
// 안전망:
//   - 이미 chromedp 인 host: UPSERT/republish 모두 skip + audit
//   - 동시 trigger: SETNX dedup
//   - max 20 cap: 단일 사이클 republish 폭주 회피
//   - 모든 단계 실패는 non-fatal — best-effort. 다음 카운팅 사이클이 자연스러운 retry.
type Upgrader struct {
	repo     storage.FetcherRuleRepository
	resolver Resolver
	tracker  RawIDTracker
	rawSvc   service.RawContentService
	producer queue.Producer
	redis    *goredis.Client // SETNX in-flight lock 용. nil 이면 lock 비활성 (단일 인스턴스 환경).
	log      *logger.Logger
}

// NewUpgrader 는 Upgrader 를 생성합니다.
//
// 모든 인자는 nil 허용 안 함 (redis 만 nil 허용 — 단일 인스턴스 환경에서 lock 비활성).
// nil 인자 발견 시 error (이슈 #208 정책).
func NewUpgrader(
	repo storage.FetcherRuleRepository,
	resolver Resolver,
	tracker RawIDTracker,
	rawSvc service.RawContentService,
	producer queue.Producer,
	redisClient *goredis.Client,
	log *logger.Logger,
) (*Upgrader, error) {
	if repo == nil {
		return nil, errors.New("rule: NewUpgrader requires non-nil FetcherRuleRepository")
	}
	if resolver == nil {
		return nil, errors.New("rule: NewUpgrader requires non-nil Resolver")
	}
	if tracker == nil {
		return nil, errors.New("rule: NewUpgrader requires non-nil RawIDTracker")
	}
	if rawSvc == nil {
		return nil, errors.New("rule: NewUpgrader requires non-nil RawContentService")
	}
	if producer == nil {
		return nil, errors.New("rule: NewUpgrader requires non-nil Producer")
	}
	return &Upgrader{
		repo:     repo,
		resolver: resolver,
		tracker:  tracker,
		rawSvc:   rawSvc,
		producer: producer,
		redis:    redisClient,
		log:      log,
	}, nil
}

// Trigger 는 host 의 chromedp 자동 전환 + 실패 raw republish 를 1회 실행합니다.
//
// host 가 빈 문자열이면 noop. 모든 단계 실패는 non-fatal — best-effort.
//
// 본 메소드는 동기 — 호출자가 별도 goroutine 에서 실행하는 것을 권장 (parser_worker 의 처리 흐름을 차단하지 않도록).
func (u *Upgrader) Trigger(ctx context.Context, host string) {
	if host == "" || u == nil {
		return
	}

	// in-flight dedup — SetArgs(NX, TTL) 으로 atomic SET … NX PX. SetNX 는 go-redis v9 에서 deprecated.
	// (goredis.Nil 은 NX 조건 불충족 = 이미 lock 보유 중을 의미하는 정상 신호.)
	if u.redis != nil {
		key := "fetcher:upgrader:lock:" + host
		err := u.redis.SetArgs(ctx, key, 1, goredis.SetArgs{Mode: "NX", TTL: upgraderInflightTTL}).Err()
		if errors.Is(err, goredis.Nil) {
			u.logDebug("upgrader skipped — another trigger in flight for host", host)
			return
		}
		if err != nil {
			u.logWarn("upgrader inflight lock failed, proceeding without dedup", host, err)
		}
	}

	// 이미 chromedp 인 host 차단
	if existing, err := u.repo.GetByHost(ctx, host); err == nil {
		if existing.Fetcher == storage.FetcherChromedp {
			u.logFields("upgrader skipped — host already on chromedp (audit: chromedp itself failing for this host)", map[string]interface{}{
				"host":   host,
				"reason": existing.Reason,
			})
			return
		}
	} else if !errors.Is(err, storage.ErrNotFound) {
		u.logWarn("upgrader fetcher_rules GetByHost failed, aborting trigger", host, err)
		return
	}

	// UPSERT chromedp + cache invalidate
	if err := u.repo.Upsert(ctx, host, storage.FetcherChromedp, "auto_upgrade_validation"); err != nil {
		u.logWarn("upgrader fetcher_rules Upsert failed", host, err)
		return
	}
	u.resolver.Invalidate(host)
	u.logFields("fetcher rule auto-upgraded to chromedp", map[string]interface{}{
		"host":   host,
		"reason": "auto_upgrade_validation",
	})

	// 실패 raw_id 수집 + republish
	rawIDs, err := u.tracker.PopByHost(ctx, host, upgraderMaxRepublishPerCycle)
	if err != nil {
		u.logWarn("upgrader tracker PopByHost failed, host upgraded but no raw republished", host, err)
		return
	}
	if len(rawIDs) == 0 {
		u.logDebug("upgrader: no failed raw to republish (already popped or expired)", host)
		return
	}

	u.republishRaws(ctx, host, rawIDs)
}

// republishRaws 는 PopByHost 결과의 raw_id 들을 새 CrawlJob 으로 발행합니다.
//
// 각 raw 에 대해:
//   - rawSvc.GetByID 로 URL / Crawler 이름 추출 (실패 시 skip)
//   - 새 CrawlJob 생성: Target.URL = raw.URL, Target.Metadata["force_fetcher"]="chromedp",
//     Headers = {retry_reason: validation_upgrade, original_raw_id: ...}
//   - 단일 PublishBatch 로 Kafka 발행 (priority normal)
func (u *Upgrader) republishRaws(ctx context.Context, host string, rawIDs []string) {
	msgs := make([]queue.Message, 0, len(rawIDs))

	for _, rawID := range rawIDs {
		raw, err := u.rawSvc.GetByID(ctx, rawID)
		if err != nil {
			u.logWarn("upgrader raw GetByID failed (skipping)", host, err)
			continue
		}
		if raw == nil || raw.URL == "" {
			continue
		}

		job := &core.CrawlJob{
			ID:          newRepublishJobID(),
			CrawlerName: raw.SourceInfo.Name,
			Target: core.Target{
				URL:  raw.URL,
				Type: core.TargetTypeArticle,
				Metadata: map[string]interface{}{
					MetadataKeyForceFetcher: string(storage.FetcherChromedp),
					"retry_reason":          "validation_upgrade",
					"original_raw_id":       rawID,
				},
			},
			Priority:    core.PriorityNormal,
			ScheduledAt: time.Now(),
			Timeout:     republishedJobTimeout,
			MaxRetries:  3,
		}
		data, err := job.Marshal()
		if err != nil {
			u.logWarn("upgrader job Marshal failed (skipping)", host, err)
			continue
		}
		msgs = append(msgs, queue.Message{
			Topic: queue.TopicCrawlNormal,
			Key:   []byte(job.ID),
			Value: data,
			Headers: map[string]string{
				"crawler":         job.CrawlerName,
				"priority":        fmt.Sprintf("%d", int(job.Priority)),
				"retry_reason":    "validation_upgrade",
				"original_raw_id": rawID,
			},
		})
	}

	if len(msgs) == 0 {
		u.logDebug("upgrader: all republish candidates skipped (no valid raw)", host)
		return
	}

	if err := u.producer.PublishBatch(ctx, msgs); err != nil {
		u.logWarn("upgrader republish PublishBatch failed", host, err)
		return
	}

	u.logFields("upgrader republish completed", map[string]interface{}{
		"host":            host,
		"republish_count": len(msgs),
	})
}

func (u *Upgrader) logWarn(msg, host string, err error) {
	if u.log == nil {
		return
	}
	u.log.WithField("host", host).WithError(err).Warn(msg)
}

func (u *Upgrader) logDebug(msg, host string) {
	if u.log == nil {
		return
	}
	u.log.WithField("host", host).Debug(msg)
}

func (u *Upgrader) logFields(msg string, fields map[string]interface{}) {
	if u.log == nil {
		return
	}
	u.log.WithFields(fields).Info(msg)
}

// newRepublishJobID 는 republish CrawlJob 의 고유 ID 를 생성합니다 (publisher.newJobID 와 동일 패턴 — 32자 hex).
func newRepublishJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 실패는 매우 드물지만 발생 시 timestamp 기반 fallback.
		return fmt.Sprintf("republish-%d", time.Now().UnixNano())
	}
	return "republish-" + hex.EncodeToString(b)
}
