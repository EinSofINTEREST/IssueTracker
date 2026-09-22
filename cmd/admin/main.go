// Command admin 은 운영자용 파이프라인 조작 도구입니다 (이슈 #542).
//
// # 배경
//
// 진입 마커를 무효화하거나 DLQ 적재 상태를 확인할 수단이 없었습니다. IngestionMarker.Invalidate
// 는 구현되어 있었지만 프로덕션 호출처가 0 이었고, HTTP API 서버도 없습니다 (이슈 #21). 그 결과
// "처리 불가 메시지는 DLQ 로 격리하고 운영자가 확인 후 재처리한다" 는 운영 서술을 실행할 방법이
// 없었습니다. 본 도구가 그 진입점입니다.
//
// # 사용법
//
//	admin invalidate <url> [-stage fetcher|parser|validator|enricher]
//	    진입 마커를 제거해 해당 URL 의 재수집을 허용합니다. -stage 지정 시 그 단계의
//	    ProcessingLock 도 함께 제거합니다 (crash 로 남은 stale 락 정리용).
//
//	admin recrawl <url>
//	    invalidate 후 CrawlJob 을 발행해 즉시 재수집을 시작합니다.
//
//	admin dlq-stats [-max N] [-timeout 10s]
//	    DLQ 토픽을 읽어 origin 토픽 / 에러별로 집계합니다. **읽기 전용** 이며 consumer group 을
//	    쓰지 않아 offset 을 건드리지 않습니다.
//
// # 의도적으로 제공하지 않는 것
//
//   - **DLQ replay**: DLQ 메시지를 원래 토픽으로 되돌리는 기능. 실패한 이유가 남아 있는 채로
//     재주입하면 루프에 빠질 수 있고, 한 번에 다수를 밀어 넣으면 파이프라인에 부하를 줍니다.
//     원인별 분기 정책을 먼저 정해야 하므로 별도 이슈로 다룹니다.
//
//   - **chromedp 강제 (force_fetcher)**: 해당 토큰은 process-local secret 이라
//     (internal/processor/fetcher/rule/force_fetcher_token.go) 별도 프로세스인 본 도구가 유효한
//     토큰을 만들 수 없습니다. 공유하면 "외부 publisher 가 chromedp 를 강제하지 못하게 한다" 는
//     보안 전제가 무너지므로 **제공하지 않는 것으로 확정** 했습니다 (이슈 #560).
//
//     대신 fetcher_rules 로 우회합니다 — 아래 "chromedp 로 다시 수집하기" 참조.
//
// # chromedp 로 다시 수집하기 (force_fetcher 우회)
//
// 특정 URL 을 chromedp 로 재수집해야 할 때 (lazy-load 페이지를 goquery 가 놓친 경우 등) 는
// fetcher_rules 를 일시적으로 바꿉니다.
//
//	-- 1. 현재 규칙 확인 (host_pattern 은 UNIQUE)
//	SELECT id, host_pattern, fetcher, reason FROM fetcher_rules WHERE host_pattern = 'example.com';
//
//	-- 2. chromedp 로 전환 (row 가 없으면 INSERT)
//	UPDATE fetcher_rules SET fetcher = 'chromedp', reason = '일시 전환: <사유> <날짜>', updated_at = NOW()
//	 WHERE host_pattern = 'example.com';
//
//	# 3. 재수집
//	bin/admin recrawl https://example.com/article/123
//
//	-- 4. 원복 (반드시)
//	UPDATE fetcher_rules SET fetcher = 'goquery', reason = '<원래 사유>', updated_at = NOW()
//	 WHERE host_pattern = 'example.com';
//
// **주의**: fetcher_rules 는 host_pattern 단위라 2~4 사이에는 해당 호스트의 **모든** URL 이
// chromedp 로 처리됩니다. chromedp 는 무겁고 worker slot 을 점유하므로, 전환 구간을 짧게
// 유지하고 4 번 원복을 빠뜨리지 마세요. 상시 chromedp 가 맞는 호스트라면 원복 대신
// reason 을 정리해 규칙으로 굳히는 편이 낫습니다.
//
// 이 우회는 force_fetcher 와 **범위가 다릅니다** — force_fetcher 는 job 단위 일회성이지만
// 위 절차는 호스트 단위이며 되돌리기 전까지 지속됩니다. URL 단위 일회성 강제가 필요해지면
// 이슈 #560 의 선택지 C (별도 승인 경로) 를 다시 검토하세요.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	storagecfg "issuetracker/pkg/config/storage"
	"issuetracker/pkg/links"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/redis"
)

const defaultOpTimeout = 30 * time.Second

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "invalidate":
		err = runInvalidate(os.Args[2:])
	case "recrawl":
		err = runRecrawl(os.Args[2:])
	case "dlq-stats":
		err = runDLQStats(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `admin — IssueTracker 운영 도구

  admin invalidate <url> [-stage <stage>]   진입 마커 제거 (+ 선택적으로 stage 락)
  admin recrawl <url>                       마커 제거 후 CrawlJob 발행
  admin dlq-stats [-max N] [-timeout 10s]   DLQ 적재 현황 집계 (읽기 전용)

stage: fetcher | parser | validator | enricher
`)
}

// newRedis 는 환경변수 설정으로 Redis client 를 엽니다.
func newRedis(ctx context.Context) (*redis.Client, storagecfg.RedisConfig, error) {
	cfg, err := storagecfg.LoadRedis()
	if err != nil {
		return nil, storagecfg.RedisConfig{}, fmt.Errorf("load redis config: %w", err)
	}
	client, err := redis.New(ctx, cfg)
	if err != nil {
		return nil, storagecfg.RedisConfig{}, fmt.Errorf("connect redis: %w", err)
	}
	return client, cfg, nil
}

// normalizeURL 은 파이프라인과 동일한 정규화를 적용합니다.
//
// 키가 sha256(정규형 URL) 이라 정규화를 건너뛰면 **다른 키를 지우게 되어** 조용히 아무 효과가
// 없습니다. 운영자가 브라우저에서 복사한 URL 을 그대로 넣어도 맞도록 여기서 맞춥니다.
func normalizeURL(raw string) string {
	n := links.NewNormalizer()
	if normalized, err := n.Normalize(raw); err == nil {
		return normalized
	}
	return raw
}

func runInvalidate(args []string) error {
	fs := flag.NewFlagSet("invalidate", flag.ExitOnError)
	stage := fs.String("stage", "", "함께 제거할 ProcessingLock 의 stage (fetcher|parser|validator|enricher)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: admin invalidate <url> [-stage <stage>]")
	}
	if *stage != "" && !validStage(*stage) {
		return fmt.Errorf("invalid stage %q (fetcher|parser|validator|enricher)", *stage)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultOpTimeout)
	defer cancel()

	client, cfg, err := newRedis(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	url := normalizeURL(fs.Arg(0))
	marker := locks.NewRedisIngestionMarker(client, cfg.IngestionMarkTTL)
	if err := marker.Invalidate(ctx, url); err != nil {
		return fmt.Errorf("invalidate ingestion marker: %w", err)
	}
	fmt.Printf("ingestion marker removed: %s\n", url)

	if *stage != "" {
		// ProcessingLock 은 소유권 토큰으로 보호되지만 (이슈 #63), 운영자의 명시적 정리는
		// "누가 잡았든 지운다" 가 의도이므로 키를 직접 제거합니다.
		key := locks.ProcessingKey(*stage, url)
		if err := client.ReleaseLock(ctx, key); err != nil {
			return fmt.Errorf("remove processing lock: %w", err)
		}
		fmt.Printf("processing lock removed: stage=%s\n", *stage)
	}
	return nil
}

func validStage(s string) bool {
	switch s {
	case locks.StageFetcher, locks.StageParser, locks.StageValidator, locks.StageEnricher:
		return true
	}
	return false
}

func runRecrawl(args []string) error {
	fs := flag.NewFlagSet("recrawl", flag.ExitOnError)
	crawler := fs.String("crawler", "admin-recrawl", "CrawlJob 의 crawler 이름")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: admin recrawl <url> [-crawler <name>]")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultOpTimeout)
	defer cancel()

	client, cfg, err := newRedis(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	url := normalizeURL(fs.Arg(0))

	// 마커를 먼저 지우지 않으면 publish 가 진입 게이트에서 걸러집니다.
	marker := locks.NewRedisIngestionMarker(client, cfg.IngestionMarkTTL)
	if err := marker.Invalidate(ctx, url); err != nil {
		return fmt.Errorf("invalidate ingestion marker: %w", err)
	}

	kafkaCfg := queue.DefaultConfig()
	producer := queue.NewProducer(kafkaCfg)
	defer producer.Close()

	log := logger.New(logger.DefaultConfig())
	job := buildRecrawlJob(url, *crawler)
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal crawl job: %w", err)
	}

	msg := queue.Message{
		Topic: queue.TopicCrawlNormal,
		Key:   []byte(job.ID),
		Value: payload,
		Headers: map[string]string{
			"crawler":  *crawler,
			"priority": "2",
			"source":   "admin-recrawl",
		},
	}
	if err := producer.Publish(ctx, msg); err != nil {
		return fmt.Errorf("publish crawl job: %w", err)
	}
	log.WithFields(map[string]interface{}{
		"job_id":  job.ID,
		"url":     url,
		"crawler": *crawler,
		"topic":   queue.TopicCrawlNormal,
	}).Info("recrawl job published")
	fmt.Printf("recrawl published: %s (job_id=%s)\n", url, job.ID)
	return nil
}

func runDLQStats(args []string) error {
	fs := flag.NewFlagSet("dlq-stats", flag.ExitOnError)
	maxMsgs := fs.Int("max", 500, "읽을 최대 메시지 수")
	timeout := fs.Duration("timeout", 10*time.Second, "더 읽을 메시지가 없을 때까지의 대기 시간")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	kafkaCfg := queue.DefaultConfig()
	// GroupID 를 비워 consumer group 에 참여하지 않습니다 — offset 을 건드리지 않는 읽기 전용.
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   kafkaCfg.Brokers,
		Topic:     queue.TopicDLQ,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
	})
	defer reader.Close()
	if err := reader.SetOffset(kafkago.FirstOffset); err != nil {
		return fmt.Errorf("seek dlq: %w", err)
	}

	byOrigin := map[string]int{}
	byError := map[string]int{}
	total := 0

	for total < *maxMsgs {
		m, err := reader.ReadMessage(ctx)
		if err != nil {
			break // timeout 또는 더 읽을 메시지 없음 — 집계한 만큼 보고
		}
		total++
		headers := map[string]string{}
		for _, h := range m.Headers {
			headers[h.Key] = string(h.Value)
		}
		origin := headers["original-topic"]
		if origin == "" {
			origin = "(unknown)"
		}
		byOrigin[origin]++

		reason := headers["error"]
		if reason == "" {
			reason = "(none)"
		}
		byError[truncate(reason, 80)]++
	}

	fmt.Printf("DLQ 메시지 %d건 (partition 0, 최대 %d건 표본)\n\n", total, *maxMsgs)
	if total == 0 {
		fmt.Println("적재된 메시지가 없거나 timeout 내 읽지 못했습니다.")
		return nil
	}
	printCounts("origin 토픽별", byOrigin)
	fmt.Println()
	printCounts("에러별", byError)
	return nil
}

func printCounts(title string, counts map[string]int) {
	fmt.Printf("%s:\n", title)
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Printf("  %6d  %s\n", counts[k], k)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// buildRecrawlJob 은 운영자 재수집용 CrawlJob 을 만듭니다.
//
// Priority 는 Normal — 운영자 조작이라도 진행 중인 고우선 작업을 밀어내지 않도록 합니다.
// force_fetcher 는 붙이지 않습니다 (파일 상단 "의도적으로 제공하지 않는 것" 참조).
func buildRecrawlJob(url, crawler string) core.CrawlJob {
	return core.CrawlJob{
		ID:          fmt.Sprintf("admin-%d", time.Now().UnixNano()),
		CrawlerName: crawler,
		Target: core.Target{
			URL:  url,
			Type: core.TargetTypeArticle,
			Metadata: map[string]interface{}{
				"retry_reason": "admin_recrawl",
			},
		},
		Priority:    core.PriorityNormal,
		ScheduledAt: time.Now(),
		Timeout:     30 * time.Second,
		MaxRetries:  3,
	}
}
