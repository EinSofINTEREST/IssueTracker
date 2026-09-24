package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

// HostSignalAggregate 는 한 host 의 집계 원시값입니다 (이슈 #382).
//
// 정규화 전 값이다 — [0,1] 변환은 scoring 패키지가 담당한다. 여기서 정규화까지 하면
// weight 를 조정할 때 SQL 을 고쳐야 하고, 원시값을 로그/디버깅에 쓸 수 없다.
type HostSignalAggregate struct {
	Host string

	// AvgDetectLagSeconds 는 published_at → created_at 평균 지연(초)입니다.
	// published_at 이 NULL 인 row 는 제외됩니다 (migration 028 이후 nullable).
	AvgDetectLagSeconds float64

	// PassedCount / RejectedCount 는 validation 결과 분포입니다.
	PassedCount   int
	RejectedCount int

	// TopCategory 는 구간 내 가장 많은 카테고리입니다 — impact 가중치의 입력.
	TopCategory string

	// SampleCount 는 집계에 쓰인 전체 문서 수입니다 (cold-start 판정 근거).
	SampleCount int
}

// pgHostSignalAggregator 는 contents 테이블에서 host 단위 signal 을 집계합니다.
type HostSignalAggregator struct {
	pool *pgxpool.Pool
}

// NewHostSignalAggregator 는 집계기를 생성합니다.
func NewHostSignalAggregator(pool *pgxpool.Pool, log *logger.Logger) *HostSignalAggregator {
	_ = log
	return &HostSignalAggregator{pool: pool}
}

// sqlAggregateHostSignals 는 구간 내 contents 를 host 로 묶어 집계합니다.
//
// host 추출: url 에서 scheme 과 경로를 떼고 포트를 제거한다. 저장 시점에 host 컬럼을
// 따로 두지 않았으므로 질의에서 파생한다 — 컬럼 추가는 전량 backfill 이 필요해
// 본 기능의 범위를 넘는다.
//
// scheme 매칭에 'i' 플래그가 필요하다. 없으면 `HTTPS://...` 가 그대로 남아 host 가
// "https" 로 잡히는데, 조회하는 Go 쪽은 url.Parse 라 올바른 host 를 쓴다 — 저장과 조회가
// 어긋나 점수가 영영 매칭되지 않는다. 조용히 빗나가는 종류라 에러로 드러나지도 않는다.
//
// created_at 기준으로 구간을 자른다 (published_at 이 아니라) — 집계 대상은 "언제 수집했나"
// 이고, published_at 은 과거 기사가 섞여 구간이 불안정해진다.
const sqlAggregateHostSignals = `
WITH windowed AS (
  SELECT
    lower(split_part(split_part(regexp_replace(url, '^https?://', '', 'i'), '/', 1), ':', 1)) AS host,
    published_at,
    created_at,
    validation_status,
    category
  FROM contents
  WHERE created_at >= NOW() - ($1::INT * INTERVAL '1 minute')
),
agg AS (
  SELECT
    host,
    COUNT(*) AS sample_count,
    COALESCE(AVG(
      CASE WHEN published_at IS NOT NULL AND created_at >= published_at
           THEN EXTRACT(EPOCH FROM (created_at - published_at))
      END
    ), 0) AS avg_lag_seconds,
    COUNT(*) FILTER (WHERE validation_status = 'passed') AS passed_count,
    COUNT(*) FILTER (WHERE validation_status = 'rejected') AS rejected_count
  FROM windowed
  WHERE host <> ''
  GROUP BY host
),
top_cat AS (
  SELECT DISTINCT ON (host) host, category
  FROM windowed
  WHERE host <> '' AND category <> ''
  GROUP BY host, category
  ORDER BY host, COUNT(*) DESC, category
)
SELECT a.host, a.avg_lag_seconds, a.passed_count, a.rejected_count,
       COALESCE(t.category, ''), a.sample_count
FROM agg a
LEFT JOIN top_cat t ON t.host = a.host
`

// Aggregate 는 최근 windowMinutes 구간의 host 별 signal 을 집계합니다.
func (a *HostSignalAggregator) Aggregate(ctx context.Context, windowMinutes int) ([]HostSignalAggregate, error) {
	if windowMinutes <= 0 {
		return nil, fmt.Errorf("aggregate host signals: window must be positive, got %d", windowMinutes)
	}
	rows, err := a.pool.Query(ctx, sqlAggregateHostSignals, windowMinutes)
	if err != nil {
		return nil, fmt.Errorf("aggregate host signals: %w", err)
	}
	defer rows.Close()

	var out []HostSignalAggregate
	for rows.Next() {
		var h HostSignalAggregate
		if err := rows.Scan(&h.Host, &h.AvgDetectLagSeconds, &h.PassedCount,
			&h.RejectedCount, &h.TopCategory, &h.SampleCount); err != nil {
			return nil, fmt.Errorf("scan host signal aggregate: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate host signal aggregates: %w", err)
	}
	return out, nil
}

// freshnessHorizon 은 freshness 정규화의 상한입니다.
//
// 이 시간 이상 지연되면 0 점. 6시간으로 둔 근거: 뉴스 수집에서 그 이상 지난 기사는
// 시의성 관점에서 구별할 실익이 없다. 더 길게 잡으면 대부분의 host 가 1 에 몰려
// 변별력이 사라진다.
const freshnessHorizon = 6 * time.Hour

// ToSignals 는 집계 원시값을 [0,1] 정규화 signal 로 변환합니다.
//
// impact 는 카테고리 가중치 표를 따른다 (categoryImpact).
func (h HostSignalAggregate) ToSignals() model.HostSignals {
	return model.HostSignals{
		Freshness:   normalizeFreshness(h.AvgDetectLagSeconds),
		Impact:      categoryImpact(h.TopCategory),
		HostTrust:   normalizeTrust(h.PassedCount, h.RejectedCount),
		SampleCount: h.SampleCount,
	}
}

// normalizeFreshness 는 지연(초) 을 [0,1] 로 뒤집습니다 — 짧을수록 1.
func normalizeFreshness(lagSeconds float64) float64 {
	if lagSeconds <= 0 {
		// 지연 정보가 없는 경우 (published_at 부재) 도 여기로 온다. 중립값 0.5 를 준다 —
		// 1 을 주면 published_at 을 노출하지 않는 host 가 부당하게 우대된다.
		return 0.5
	}
	horizon := freshnessHorizon.Seconds()
	if lagSeconds >= horizon {
		return 0
	}
	return 1 - (lagSeconds / horizon)
}

// normalizeTrust 는 validation 통과율을 반환합니다.
//
// 판정이 끝난 건이 없으면 중립값 0.5 — pending 만 있는 host 를 신뢰도 0 으로 보면
// 신규 host 가 영구히 불리해진다.
func normalizeTrust(passed, rejected int) float64 {
	total := passed + rejected
	if total == 0 {
		return 0.5
	}
	return float64(passed) / float64(total)
}

// categoryImpact 는 카테고리별 가중치입니다 (이슈 #382 의 impact signal).
//
// 표에 없는 카테고리는 중립값 0.5 — 알 수 없는 것을 낮게 보면 분류가 안 된 host 가
// 일괄 불리해지고, 높게 보면 그 반대가 된다.
func categoryImpact(category string) float64 {
	switch category {
	case "politics", "economy", "social", "current_affairs", "breaking_news":
		return 1.0
	case "international", "tech", "science":
		return 0.7
	case "sports", "culture", "entertainment", "lifestyle":
		return 0.4
	default:
		return 0.5
	}
}
