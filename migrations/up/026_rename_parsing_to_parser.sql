-- 026_rename_parsing_to_parser: DB 명칭을 다른 stage 의 명명 규칙과 정합 (이슈 #372)
--
-- 배경:
--   fetcher / scheduler 등 다른 stage 는 stage 명을 그대로 prefix 로 사용 (fetcher_rules,
--   scheduler_entries). 반면 parser stage 만 gerund 형 `parsing_*` 를 사용하여 일관성 결여.
--   본 마이그레이션은 schema 레벨 명칭을 `parser_*` 로 통일.
--
-- Phase 1 (본 migration) — schema 명칭 + Go SQL 문자열 동시 갱신
-- Phase 2 (후속 이슈) — Go 식별자 (ParsingRule → ParserRule 등) 정리
--
-- 변경 항목:
--   Tables (3):
--     parsing_rules → parser_rules
--     parsing_blacklist → parser_blacklist
--     parsing_rule_sample_urls → parser_rule_sample_urls
--   Indexes (4):
--     idx_parsing_rules_lookup, idx_parsing_rules_source_enabled,
--     idx_parsing_blacklist_host_enabled, idx_parsing_rule_sample_urls_lookup
--   Named constraints (4):
--     parsing_rules_target_type_check, parsing_rules_version_positive,
--     parsing_rules_lookup_key_unique, parsing_rule_sample_urls_unique
--   Anonymous CHECK constraints (PG auto-named `<table>_<col>_check`, 2):
--     parsing_blacklist_source_check, parsing_blacklist_mode_check
--   Trigger + Function:
--     parsing_rules_touch_updated_at
--
-- 멱등성:
--   - ALTER TABLE/INDEX/TRIGGER/FUNCTION 은 IF EXISTS 미지원 (PG 16 기준 인덱스만 지원)
--     → DO $$ BEGIN ... END $$ 블록으로 존재 여부 체크 후 RENAME 수행
--   - 이미 RENAME 된 상태에서 재실행해도 무처리 (catalog 조회로 분기)
--
-- 운영 영향:
--   - ALTER TABLE RENAME 은 ACCESS EXCLUSIVE lock — in-flight 쿼리 ERROR
--   - 배포 순서 강제: migration up 적용 → app 재기동 (구 코드가 `parsing_*` 참조 시 실패)
--   - 롤백은 026_rename_parsing_to_parser down 으로 복귀

-- ─── Tables ──────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'parsing_rules') THEN
    ALTER TABLE parsing_rules RENAME TO parser_rules;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'parsing_blacklist') THEN
    ALTER TABLE parsing_blacklist RENAME TO parser_blacklist;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'parsing_rule_sample_urls') THEN
    ALTER TABLE parsing_rule_sample_urls RENAME TO parser_rule_sample_urls;
  END IF;
END $$;

-- ─── Indexes ─────────────────────────────────────────────────────────────────
ALTER INDEX IF EXISTS idx_parsing_rules_lookup
  RENAME TO idx_parser_rules_lookup;

ALTER INDEX IF EXISTS idx_parsing_rules_source_enabled
  RENAME TO idx_parser_rules_source_enabled;

ALTER INDEX IF EXISTS idx_parsing_blacklist_host_enabled
  RENAME TO idx_parser_blacklist_host_enabled;

ALTER INDEX IF EXISTS idx_parsing_rule_sample_urls_lookup
  RENAME TO idx_parser_rule_sample_urls_lookup;

-- ─── Named constraints ───────────────────────────────────────────────────────
DO $$
BEGIN
  -- parser_rules constraints
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parsing_rules_target_type_check'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parsing_rules_target_type_check TO parser_rules_target_type_check;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parsing_rules_version_positive'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parsing_rules_version_positive TO parser_rules_version_positive;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parsing_rules_lookup_key_unique'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parsing_rules_lookup_key_unique TO parser_rules_lookup_key_unique;
  END IF;

  -- parser_rule_sample_urls constraints
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rule_sample_urls' AND c.conname = 'parsing_rule_sample_urls_unique'
  ) THEN
    ALTER TABLE parser_rule_sample_urls
      RENAME CONSTRAINT parsing_rule_sample_urls_unique TO parser_rule_sample_urls_unique;
  END IF;

  -- parser_blacklist anonymous CHECK constraints (PG auto-named)
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_blacklist' AND c.conname = 'parsing_blacklist_source_check'
  ) THEN
    ALTER TABLE parser_blacklist
      RENAME CONSTRAINT parsing_blacklist_source_check TO parser_blacklist_source_check;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_blacklist' AND c.conname = 'parsing_blacklist_mode_check'
  ) THEN
    ALTER TABLE parser_blacklist
      RENAME CONSTRAINT parsing_blacklist_mode_check TO parser_blacklist_mode_check;
  END IF;

  -- PG auto-generated constraint names: <orig_table>_pkey / <orig_table>_<cols>_key /
  -- <orig_table>_<col>_fkey. RENAME TABLE 은 이 이름을 자동 갱신하지 않음 → 명시 처리.
  -- parser_rules
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parsing_rules_pkey'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parsing_rules_pkey TO parser_rules_pkey;
  END IF;

  -- parser_blacklist auto-generated PK + UNIQUE
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_blacklist' AND c.conname = 'parsing_blacklist_pkey'
  ) THEN
    ALTER TABLE parser_blacklist
      RENAME CONSTRAINT parsing_blacklist_pkey TO parser_blacklist_pkey;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_blacklist' AND c.conname = 'parsing_blacklist_host_pattern_path_pattern_key'
  ) THEN
    ALTER TABLE parser_blacklist
      RENAME CONSTRAINT parsing_blacklist_host_pattern_path_pattern_key
                     TO parser_blacklist_host_pattern_path_pattern_key;
  END IF;

  -- parser_rule_sample_urls auto-generated PK + FK
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rule_sample_urls' AND c.conname = 'parsing_rule_sample_urls_pkey'
  ) THEN
    ALTER TABLE parser_rule_sample_urls
      RENAME CONSTRAINT parsing_rule_sample_urls_pkey TO parser_rule_sample_urls_pkey;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rule_sample_urls' AND c.conname = 'parsing_rule_sample_urls_rule_id_fkey'
  ) THEN
    ALTER TABLE parser_rule_sample_urls
      RENAME CONSTRAINT parsing_rule_sample_urls_rule_id_fkey TO parser_rule_sample_urls_rule_id_fkey;
  END IF;
END $$;

-- 명명된 인덱스가 PG 카탈로그에 자동으로 같이 따라오므로 UNIQUE / PK 의 backing 인덱스 명도
-- constraint 명과 동기화 — PG 는 constraint rename 시 backing 인덱스도 함께 rename 한다
-- (catalog 시맨틱), 따라서 별도 ALTER INDEX 불필요.

-- ─── Trigger + Function ──────────────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_trigger tr
    JOIN pg_class t ON tr.tgrelid = t.oid
    WHERE t.relname = 'parser_rules' AND tr.tgname = 'parsing_rules_touch_updated_at'
  ) THEN
    ALTER TRIGGER parsing_rules_touch_updated_at ON parser_rules
      RENAME TO parser_rules_touch_updated_at;
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_proc WHERE proname = 'parsing_rules_touch_updated_at'
  ) THEN
    ALTER FUNCTION parsing_rules_touch_updated_at() RENAME TO parser_rules_touch_updated_at;
  END IF;
END $$;

-- ─── Comments — 본문에 참조된 테이블 명 갱신 ──────────────────────────────────
-- (016 에서 설정한 parsing_blacklist 관련 COMMENT 는 catalog 상 자동 follow — 별도 처리 불필요)
COMMENT ON TABLE parser_blacklist IS
  'page-parse 블랙리스트 (이슈 #295) — 매칭 URL 은 article job 발행 단계에서 drop.';
