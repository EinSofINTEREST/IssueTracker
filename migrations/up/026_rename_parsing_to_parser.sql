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
-- 변경 항목 (총 23 catalog 객체):
--   Tables (3): parsing_rules / parsing_blacklist / parsing_rule_sample_urls
--   Indexes (4): idx_parsing_rules_*, idx_parsing_blacklist_*, idx_parsing_rule_sample_urls_*
--   Named constraints (4): parsing_rules_target_type_check, _version_positive,
--                          _lookup_key_unique, parsing_rule_sample_urls_unique
--   Anonymous CHECK (2): parsing_blacklist_source_check, parsing_blacklist_mode_check
--   Auto-generated PK/FK/UNIQUE (5): *_pkey 3, _key 1, _fkey 1
--   Sequences (3): parsing_*_id_seq (BIGSERIAL backing)
--   Trigger + Function (2): parsing_rules_touch_updated_at
--
-- 멱등성 + 안전성:
--   - 모든 존재 확인은 `to_regclass('public.<obj>')` / `to_regprocedure('public.<func>(...)')`
--     로 schema-qualified 처리 — search_path 변동 / 동명 객체 다른 schema 위험 회피
--     (Copilot / gemini PR #373 피드백)
--   - 모든 DDL 도 `public.<obj>` 로 schema 명시
--   - constraint / trigger 는 (table, conname/tgname) 단위라 pg_class + pg_namespace JOIN
--   - 이미 RENAME 된 상태에서 재실행해도 무처리
--
-- 운영 영향:
--   - ALTER TABLE RENAME 은 ACCESS EXCLUSIVE lock — in-flight 쿼리 ERROR
--   - 배포 순서 강제: migration up 적용 → app 재기동 (구 코드가 `parsing_*` 참조 시 실패)
--   - 롤백은 026_rename_parsing_to_parser down 으로 복귀

-- ─── Tables ──────────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF to_regclass('public.parsing_rules') IS NOT NULL THEN
    ALTER TABLE public.parsing_rules RENAME TO parser_rules;
  END IF;

  IF to_regclass('public.parsing_blacklist') IS NOT NULL THEN
    ALTER TABLE public.parsing_blacklist RENAME TO parser_blacklist;
  END IF;

  IF to_regclass('public.parsing_rule_sample_urls') IS NOT NULL THEN
    ALTER TABLE public.parsing_rule_sample_urls RENAME TO parser_rule_sample_urls;
  END IF;
END $$;

-- ─── Indexes ─────────────────────────────────────────────────────────────────
ALTER INDEX IF EXISTS public.idx_parsing_rules_lookup
  RENAME TO idx_parser_rules_lookup;

ALTER INDEX IF EXISTS public.idx_parsing_rules_source_enabled
  RENAME TO idx_parser_rules_source_enabled;

ALTER INDEX IF EXISTS public.idx_parsing_blacklist_host_enabled
  RENAME TO idx_parser_blacklist_host_enabled;

ALTER INDEX IF EXISTS public.idx_parsing_rule_sample_urls_lookup
  RENAME TO idx_parser_rule_sample_urls_lookup;

-- ─── Sequences ───────────────────────────────────────────────────────────────
-- BIGSERIAL 컬럼이 자동 생성한 sequence — RENAME TABLE 은 sequence 명을 자동 갱신하지 않음.
ALTER SEQUENCE IF EXISTS public.parsing_rules_id_seq
  RENAME TO parser_rules_id_seq;

ALTER SEQUENCE IF EXISTS public.parsing_blacklist_id_seq
  RENAME TO parser_blacklist_id_seq;

ALTER SEQUENCE IF EXISTS public.parsing_rule_sample_urls_id_seq
  RENAME TO parser_rule_sample_urls_id_seq;

-- ─── Constraints (named + anonymous + auto-generated PK/FK) ──────────────────
-- constraint / trigger 는 (table, name) 단위라 schema 는 pg_namespace JOIN 으로 확인.
DO $$
DECLARE
  rec record;
BEGIN
  FOR rec IN
    SELECT * FROM (VALUES
      ('parser_rules',            'parsing_rules_target_type_check',                  'parser_rules_target_type_check'),
      ('parser_rules',            'parsing_rules_version_positive',                   'parser_rules_version_positive'),
      ('parser_rules',            'parsing_rules_lookup_key_unique',                  'parser_rules_lookup_key_unique'),
      ('parser_rules',            'parsing_rules_pkey',                               'parser_rules_pkey'),
      ('parser_blacklist',        'parsing_blacklist_source_check',                   'parser_blacklist_source_check'),
      ('parser_blacklist',        'parsing_blacklist_mode_check',                     'parser_blacklist_mode_check'),
      ('parser_blacklist',        'parsing_blacklist_pkey',                           'parser_blacklist_pkey'),
      ('parser_blacklist',        'parsing_blacklist_host_pattern_path_pattern_key',  'parser_blacklist_host_pattern_path_pattern_key'),
      ('parser_rule_sample_urls', 'parsing_rule_sample_urls_unique',                  'parser_rule_sample_urls_unique'),
      ('parser_rule_sample_urls', 'parsing_rule_sample_urls_pkey',                    'parser_rule_sample_urls_pkey'),
      ('parser_rule_sample_urls', 'parsing_rule_sample_urls_rule_id_fkey',            'parser_rule_sample_urls_rule_id_fkey')
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

-- ─── Trigger + Function ──────────────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_trigger tr
    JOIN pg_class t ON tr.tgrelid = t.oid
    JOIN pg_namespace n ON t.relnamespace = n.oid
    WHERE n.nspname = 'public'
      AND t.relname = 'parser_rules'
      AND tr.tgname = 'parsing_rules_touch_updated_at'
  ) THEN
    ALTER TRIGGER parsing_rules_touch_updated_at ON public.parser_rules
      RENAME TO parser_rules_touch_updated_at;
  END IF;
END $$;

DO $$
BEGIN
  -- to_regprocedure 는 schema + signature 까지 명시 — overloading 충돌 방지.
  IF to_regprocedure('public.parsing_rules_touch_updated_at()') IS NOT NULL THEN
    ALTER FUNCTION public.parsing_rules_touch_updated_at()
      RENAME TO parser_rules_touch_updated_at;
  END IF;
END $$;

-- ─── Comments — 본문에 참조된 테이블 명 갱신 ──────────────────────────────────
COMMENT ON TABLE public.parser_blacklist IS
  'page-parse 블랙리스트 (이슈 #295) — 매칭 URL 은 article job 발행 단계에서 drop.';
