-- 018_parsing_rules_page_type DOWN: page_type 컬럼 제거.

ALTER TABLE parsing_rules
  DROP COLUMN IF EXISTS page_type;
