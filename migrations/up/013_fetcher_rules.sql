-- 013_fetcher_rules: 호스트 단위 fetcher 선택 정책 테이블 (이슈 #175 단계 1, sub-issue #219)
--
-- 배경:
--   기존 fetcher 파이프라인은 모든 host 에 동일 chain (goquery 우선 + lazy-load 시 chromedp fallback)
--   적용. SPA / dynamic content 사이트는 \"형식적으로는 받아왔으나 본문이 비어있는\" 상태로 통과해
--   parsing 까지 가서야 빈 본문 발견. 본 테이블은 host 단위로 fetcher 선택을 강제할 수 있는 base 를
--   마련한다.
--
-- 자동 전환은 별도 sub-issue (#220 카운팅 + #221 자동 UPSERT) 의 책임. 본 단계에서는 운영자
-- manual UPSERT + Resolver 의 조회만 동작.
--
-- 호환성:
--   - fetcher_rules 부재 host 는 default chain (현재 동작 100% 보존)
--   - parsing_rules 와 별도 테이블 — lifecycle / 정책 다름

CREATE TABLE IF NOT EXISTS fetcher_rules (
  id           BIGSERIAL PRIMARY KEY,
  host_pattern TEXT      NOT NULL UNIQUE
              CHECK (host_pattern = LOWER(BTRIM(host_pattern)))
              CHECK (BTRIM(host_pattern) <> ''),
  fetcher      TEXT      NOT NULL CHECK (fetcher IN ('goquery', 'chromedp')),
  reason       TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_fetcher_rules_host ON fetcher_rules(host_pattern);

COMMENT ON TABLE  fetcher_rules               IS 'host 단위 fetcher 선택 정책 (이슈 #175 단계 1).';
COMMENT ON COLUMN fetcher_rules.host_pattern  IS 'exact host match (예: ''edition.cnn.com''). path 단위 매칭은 본 단계 scope 외.';
COMMENT ON COLUMN fetcher_rules.fetcher       IS 'goquery | chromedp — Resolver 가 host 매칭 시 반환할 fetcher 식별자.';
COMMENT ON COLUMN fetcher_rules.reason        IS 'manual | auto_upgrade_validation | ... — UPSERT 사유 audit.';
