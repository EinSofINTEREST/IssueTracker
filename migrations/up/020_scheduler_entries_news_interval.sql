-- 020_scheduler_entries_news_interval: news 카테고리 interval 단축 (이슈 #329)
--
-- 배경:
--   migration 019 에서 4 source / 29 URL 을 scheduler_entries 로 seed 하면서 기존 동작
--   보존을 위해 interval_seconds=7200 (2h) 로 둠. 본 마이그레이션은 sub A 의 의도대로
--   30m 으로 단축 — "빠른 정보 수신을 위한 잦은 스케줄링" (이슈 #328 의 1번 요구사항).
--
-- 정책:
--   - news 카테고리 전체 일괄 1800s (30m) 적용
--   - source 별 차별화는 운영자 manual UPDATE 로 후속 조정 가능
--   - 기존 운영자 manual override (interval ≠ 7200) 는 보존 — WHERE interval_seconds = 7200
--     으로 seed 기본값만 갱신
--
-- 영향:
--   - fetch 빈도 4배 ↑ — fetcher_rules.requests_per_hour 와 IPRateLimiterRegistry (#323) 가
--     IP 단위 token bucket 으로 throttle 하므로 IP 차단 위험은 RPH 정책 안에서 cap
--   - pipeline guard (Article 24h / Category 1m TTL) 가 중복 fetch 차단
--   - sub B (community) / sub C (search) 는 별도 카테고리라 영향 없음

UPDATE scheduler_entries
SET interval_seconds = 1800,
    updated_at = NOW()
WHERE category = 'news'
  AND interval_seconds = 7200;
