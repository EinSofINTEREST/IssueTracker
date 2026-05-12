-- 026_rename_parsing_to_parser 롤백 — parser_* → parsing_* 역방향
--
-- 모든 RENAME 을 026 up 의 역순으로 수행하여 schema 를 025 시점 상태로 복원한다.
-- 멱등성 — 이미 원상복귀된 상태에서 재실행해도 무처리.

-- ─── Function + Trigger (역순) ───────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_proc WHERE proname = 'parser_rules_touch_updated_at'
  ) THEN
    ALTER FUNCTION parser_rules_touch_updated_at() RENAME TO parsing_rules_touch_updated_at;
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_trigger tr
    JOIN pg_class t ON tr.tgrelid = t.oid
    WHERE t.relname = 'parser_rules' AND tr.tgname = 'parser_rules_touch_updated_at'
  ) THEN
    ALTER TRIGGER parser_rules_touch_updated_at ON parser_rules
      RENAME TO parsing_rules_touch_updated_at;
  END IF;
END $$;

-- ─── Constraints ─────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_blacklist' AND c.conname = 'parser_blacklist_mode_check'
  ) THEN
    ALTER TABLE parser_blacklist
      RENAME CONSTRAINT parser_blacklist_mode_check TO parsing_blacklist_mode_check;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_blacklist' AND c.conname = 'parser_blacklist_source_check'
  ) THEN
    ALTER TABLE parser_blacklist
      RENAME CONSTRAINT parser_blacklist_source_check TO parsing_blacklist_source_check;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rule_sample_urls' AND c.conname = 'parser_rule_sample_urls_unique'
  ) THEN
    ALTER TABLE parser_rule_sample_urls
      RENAME CONSTRAINT parser_rule_sample_urls_unique TO parsing_rule_sample_urls_unique;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parser_rules_lookup_key_unique'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parser_rules_lookup_key_unique TO parsing_rules_lookup_key_unique;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parser_rules_version_positive'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parser_rules_version_positive TO parsing_rules_version_positive;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint c
    JOIN pg_class t ON c.conrelid = t.oid
    WHERE t.relname = 'parser_rules' AND c.conname = 'parser_rules_target_type_check'
  ) THEN
    ALTER TABLE parser_rules
      RENAME CONSTRAINT parser_rules_target_type_check TO parsing_rules_target_type_check;
  END IF;
END $$;

-- ─── Indexes ─────────────────────────────────────────────────────────────────
ALTER INDEX IF EXISTS idx_parser_rule_sample_urls_lookup
  RENAME TO idx_parsing_rule_sample_urls_lookup;

ALTER INDEX IF EXISTS idx_parser_blacklist_host_enabled
  RENAME TO idx_parsing_blacklist_host_enabled;

ALTER INDEX IF EXISTS idx_parser_rules_source_enabled
  RENAME TO idx_parsing_rules_source_enabled;

ALTER INDEX IF EXISTS idx_parser_rules_lookup
  RENAME TO idx_parsing_rules_lookup;

-- ─── Tables ──────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'parser_rule_sample_urls') THEN
    ALTER TABLE parser_rule_sample_urls RENAME TO parsing_rule_sample_urls;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'parser_blacklist') THEN
    ALTER TABLE parser_blacklist RENAME TO parsing_blacklist;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'parser_rules') THEN
    ALTER TABLE parser_rules RENAME TO parsing_rules;
  END IF;
END $$;
