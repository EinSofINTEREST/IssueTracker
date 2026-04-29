-- 006_create_parsing_rules 롤백 — trigger / function / 인덱스 / 테이블 역순 제거

DROP TRIGGER IF EXISTS parsing_rules_touch_updated_at ON parsing_rules;
DROP FUNCTION IF EXISTS parsing_rules_touch_updated_at();

DROP INDEX IF EXISTS idx_parsing_rules_source_enabled;
DROP INDEX IF EXISTS idx_parsing_rules_lookup;

DROP TABLE IF EXISTS parsing_rules;
