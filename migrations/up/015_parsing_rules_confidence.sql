-- 015_parsing_rules_confidence: parsing_rules 에 confidence 메타데이터 JSONB 컬럼 추가 (이슈 #283)
--
-- 배경:
--   LLM 자동 생성 룰의 selector 가 실제 HTML 에서 추출 가능한지 / published_at 같은 필드가
--   해당 호스트에서 안정적으로 추출되는지를 metadata 로 보존. 하류 validator 가 host 별
--   차별화된 정책 (예: published_at 부재 host 는 reject 안 함) 을 적용 가능.
--
-- 스키마:
--   - confidence: JSONB — 필드별 hit_rate / sample_count 보관
--     예: {"title": {"hit_rate": 1.0, "sample_count": 1},
--          "main_content": {"hit_rate": 1.0, "sample_count": 1},
--          "published_at": {"hit_rate": 0.0, "sample_count": 1}}
--   - 기존 row 는 default '{}' — backward compat (자동 학습 / 추후 갱신)
--   - selectors 와 별도 컬럼 — selectors 는 추출 logic, confidence 는 추출 신뢰도 (관심사 분리)
--
-- 진화 친화적 — 새 metadata 필드 추가 시 application 측 struct 만 변경, migration 불필요.

ALTER TABLE parsing_rules
  ADD COLUMN IF NOT EXISTS confidence JSONB NOT NULL DEFAULT '{}';

COMMENT ON COLUMN parsing_rules.confidence IS
  '필드별 추출 신뢰도 metadata: {"<field>": {"hit_rate": float, "sample_count": int}}. 이슈 #283.';
