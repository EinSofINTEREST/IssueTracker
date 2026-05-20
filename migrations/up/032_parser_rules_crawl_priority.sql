-- 032_parser_rules_crawl_priority: parser_rules 에 crawl_priority 컬럼 추가 (이슈 #521, 메타 #515 Sub 1)
--
-- 배경:
--   현 운영에서 95%+ 트래픽이 Normal 토픽 단일 경로로 흐름 (이슈 #515 분석).
--   bus.RuleBasedPriorityResolver 가 wiring 만 됐고 룰 0건이라 dead wiring 상태.
--   host_pattern + path_pattern 이 이미 parser_rules 에 존재하므로 같은 row 에
--   crawl_priority 를 함께 두면 RuleBasedPriorityResolver 가 자연스럽게 host/path 기반
--   priority 분기를 수행 가능.
--
-- 스키마:
--   - crawl_priority SMALLINT NOT NULL DEFAULT 2
--   - 매핑: 1=high / 2=normal / 3=low — core.Priority 와 동일 (scheduler_entries.priority 와 정렬)
--   - CHECK 제약: 1~3 만 허용 — 잘못된 값 진입 차단
--
-- 호환성:
--   - DEFAULT 2 (normal) — 기존 모든 룰의 라우팅 동작 100% 보존
--   - 운영자가 명시적으로 1 (high) / 3 (low) 로 지정한 룰만 분기 효과
--
-- 적용 흐름:
--   1. 본 migration 으로 컬럼 추가 (모든 row 가 2 로 초기화)
--   2. 운영자가 breaking-news / archive 등의 host/path 룰에 1 또는 3 명시
--   3. RuleBasedPriorityResolver 가 parser_rules 를 lookup 하여 host/path → priority 결정
--   4. 매칭 룰 없으면 DefaultPriorityResolver fallback (Normal)

ALTER TABLE parser_rules
  ADD COLUMN IF NOT EXISTS crawl_priority SMALLINT NOT NULL DEFAULT 2;

ALTER TABLE parser_rules
  DROP CONSTRAINT IF EXISTS parser_rules_crawl_priority_range;

ALTER TABLE parser_rules
  ADD CONSTRAINT parser_rules_crawl_priority_range
    CHECK (crawl_priority BETWEEN 1 AND 3);

COMMENT ON COLUMN parser_rules.crawl_priority IS
  'host/path 매칭된 URL 의 crawl 우선순위. 1=high / 2=normal / 3=low (core.Priority 매핑). RuleBasedPriorityResolver 가 lookup 하여 chained / upgrade URL 발행 시 토픽 라우팅 결정. 이슈 #521 / 메타 #515.';
