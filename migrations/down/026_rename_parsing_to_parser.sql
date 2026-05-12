-- 026_rename_parsing_to_parser 롤백 — parser_* → parsing_* 역방향
--
-- up 의 모든 변경을 역순으로 수행하여 schema 를 025 시점 상태로 복원.
-- 멱등성 — 이미 원상복귀된 상태에서 재실행해도 무처리.
-- 모든 존재 확인은 schema-qualified (`to_regclass` / `to_regprocedure`) — up 과 동일 패턴.

-- ─── Function + Trigger (역순) ───────────────────────────────────────────────
DO $$
BEGIN
  IF to_regprocedure('public.parser_rules_touch_updated_at()') IS NOT NULL THEN
    ALTER FUNCTION public.parser_rules_touch_updated_at()
      RENAME TO parsing_rules_touch_updated_at;
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_trigger tr
    JOIN pg_class t ON tr.tgrelid = t.oid
    JOIN pg_namespace n ON t.relnamespace = n.oid
    WHERE n.nspname = 'public'
      AND t.relname = 'parser_rules'
      AND tr.tgname = 'parser_rules_touch_updated_at'
  ) THEN
    ALTER TRIGGER parser_rules_touch_updated_at ON public.parser_rules
      RENAME TO parsing_rules_touch_updated_at;
  END IF;
END $$;

-- ─── Constraints (역순) ──────────────────────────────────────────────────────
DO $$
DECLARE
  rec record;
BEGIN
  FOR rec IN
    SELECT * FROM (VALUES
      -- up 의 역순 (FK → PK → UNIQUE → anonymous CHECK → named)
      ('parser_rule_sample_urls', 'parser_rule_sample_urls_rule_id_fkey',            'parsing_rule_sample_urls_rule_id_fkey'),
      ('parser_rule_sample_urls', 'parser_rule_sample_urls_pkey',                    'parsing_rule_sample_urls_pkey'),
      ('parser_rule_sample_urls', 'parser_rule_sample_urls_unique',                  'parsing_rule_sample_urls_unique'),
      ('parser_blacklist',        'parser_blacklist_host_pattern_path_pattern_key',  'parsing_blacklist_host_pattern_path_pattern_key'),
      ('parser_blacklist',        'parser_blacklist_pkey',                           'parsing_blacklist_pkey'),
      ('parser_blacklist',        'parser_blacklist_mode_check',                     'parsing_blacklist_mode_check'),
      ('parser_blacklist',        'parser_blacklist_source_check',                   'parsing_blacklist_source_check'),
      ('parser_rules',            'parser_rules_pkey',                               'parsing_rules_pkey'),
      ('parser_rules',            'parser_rules_lookup_key_unique',                  'parsing_rules_lookup_key_unique'),
      ('parser_rules',            'parser_rules_version_positive',                   'parsing_rules_version_positive'),
      ('parser_rules',            'parser_rules_target_type_check',                  'parsing_rules_target_type_check')
    ) AS v(tbl, old_name, new_name)
  LOOP
    IF EXISTS (
      SELECT 1 FROM pg_constraint c
      JOIN pg_class t ON c.conrelid = t.oid
      JOIN pg_namespace n ON t.relnamespace = n.oid
      WHERE n.nspname = 'public' AND t.relname = rec.tbl AND c.conname = rec.old_name
    ) THEN
      EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I',
                     rec.tbl, rec.old_name, rec.new_name);
    END IF;
  END LOOP;
END $$;

-- ─── Sequences (역순) ────────────────────────────────────────────────────────
ALTER SEQUENCE IF EXISTS public.parser_rule_sample_urls_id_seq
  RENAME TO parsing_rule_sample_urls_id_seq;

ALTER SEQUENCE IF EXISTS public.parser_blacklist_id_seq
  RENAME TO parsing_blacklist_id_seq;

ALTER SEQUENCE IF EXISTS public.parser_rules_id_seq
  RENAME TO parsing_rules_id_seq;

-- ─── Indexes (역순) ──────────────────────────────────────────────────────────
ALTER INDEX IF EXISTS public.idx_parser_rule_sample_urls_lookup
  RENAME TO idx_parsing_rule_sample_urls_lookup;

ALTER INDEX IF EXISTS public.idx_parser_blacklist_host_enabled
  RENAME TO idx_parsing_blacklist_host_enabled;

ALTER INDEX IF EXISTS public.idx_parser_rules_source_enabled
  RENAME TO idx_parsing_rules_source_enabled;

ALTER INDEX IF EXISTS public.idx_parser_rules_lookup
  RENAME TO idx_parsing_rules_lookup;

-- ─── Tables (역순) ───────────────────────────────────────────────────────────
DO $$
BEGIN
  IF to_regclass('public.parser_rule_sample_urls') IS NOT NULL THEN
    ALTER TABLE public.parser_rule_sample_urls RENAME TO parsing_rule_sample_urls;
  END IF;

  IF to_regclass('public.parser_blacklist') IS NOT NULL THEN
    ALTER TABLE public.parser_blacklist RENAME TO parsing_blacklist;
  END IF;

  IF to_regclass('public.parser_rules') IS NOT NULL THEN
    ALTER TABLE public.parser_rules RENAME TO parsing_rules;
  END IF;
END $$;
