-- 032_parser_rules_crawl_priority (down): crawl_priority 컬럼 + CHECK 제약 제거

ALTER TABLE parser_rules
  DROP CONSTRAINT IF EXISTS parser_rules_crawl_priority_range;

ALTER TABLE parser_rules
  DROP COLUMN IF EXISTS crawl_priority;
