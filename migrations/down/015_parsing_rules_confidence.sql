-- 015_parsing_rules_confidence (down): confidence 컬럼 제거 (이슈 #283 롤백).

ALTER TABLE parsing_rules
  DROP COLUMN IF EXISTS confidence;
