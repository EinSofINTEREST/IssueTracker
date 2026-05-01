-- 012_parsing_rule_sample_urls: 점진적 정밀화 워크플로의 sample URL 누적 (이슈 #173 단계 4-1)
--
-- 배경:
--   단계 1-3 으로 LLM 자동 rule 생성 + path_pattern 매칭 + LLM 추론 인프라 구축됨.
--   단계 4 의 점진적 정밀화는 \"같은 host 의 article URL 을 누적 → 5개 도달 시 알고리즘
--   /LLM 으로 path_pattern 추론 → DB UPDATE\" 흐름.
--   본 migration 은 그 \"sample 누적\" 부분의 인프라.
--
-- 정책:
--   - parser_worker 가 정상 파싱 후 sample 누적
--   - 누적 대상: rule.path_pattern='' (catch-all) AND rule.source_name='llm-auto' 인 rule
--     (운영자 hand-tuned rule 은 정밀화 대상 아니므로 누적 X)
--   - UNIQUE (rule_id, url): 같은 URL 중복 누적 방지 — INSERT 중복은 ErrDuplicate 로 무시
--   - ON DELETE CASCADE: 부모 rule 삭제 시 자동 정리
--
-- 운영 cap (application 측):
--   - 같은 rule_id 의 sample 100 도달 시 application 이 INSERT skip — DB 폭증 방어
--     (단계 4-2 의 trigger 가 5개 시점에 정밀화 + purge 하면 100 cap 도달 전 정리됨)

CREATE TABLE IF NOT EXISTS parsing_rule_sample_urls (
  id          BIGSERIAL    PRIMARY KEY,

  rule_id     BIGINT       NOT NULL
    REFERENCES parsing_rules(id) ON DELETE CASCADE,

  url         TEXT         NOT NULL,    -- 정규화된 URL (path 추출 시 path_pattern 추론 입력)
  observed_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

  CONSTRAINT parsing_rule_sample_urls_unique
    UNIQUE (rule_id, url)
);

-- list / count 핫패스 — rule_id 별 누적 sample 조회 + 정밀화 트리거 시점 카운트
CREATE INDEX IF NOT EXISTS idx_parsing_rule_sample_urls_lookup
  ON parsing_rule_sample_urls (rule_id, observed_at DESC);
