-- 011_parsing_rules_path_pattern: parsing_rules 에 path_pattern 컬럼 추가 (이슈 #173 단계 1)
--
-- 배경:
--   기존 lookup 키 (host_pattern, target_type) 는 같은 호스트의 모든 페이지를 단일 rule 로
--   처리. SPA / 다양한 페이지 종류가 섞인 사이트는 표현 불가. 본 migration 은 path 기반 정밀
--   매칭을 위한 인프라 (컬럼 + UNIQUE + 검증) 를 추가한다.
--
-- 호환성:
--   - path_pattern DEFAULT '' (모든 path 매칭) — 기존 모든 row 의 동작 100% 보존
--   - 신규 운영자/LLM 자동 생성 rule 만 path_pattern 명시 가능
--   - application 측 (Resolver) 가 host 매칭 후보 슬라이스를 받아 regex 매칭
--
-- 매칭 정책:
--   - SQL 단계: host_pattern + target_type + enabled 로 후보 슬라이스 fetch
--     (application 측 옵션 B — DB regex 평가 회피, index 활용)
--   - application 단계: path_pattern regex 길이 DESC 정렬, 길이 긴 (구체적) 패턴 우선
--   - path_pattern='' 은 모든 path 매칭 (length 0 이라 가장 마지막 후보)

ALTER TABLE parsing_rules
  ADD COLUMN IF NOT EXISTS path_pattern TEXT NOT NULL DEFAULT '';

-- 자연키 변경: (host_pattern, target_type, version) → (host_pattern, path_pattern, target_type, version)
-- 같은 host 의 다른 path_pattern 은 별개 rule 로 운영 가능 (예: /article/.* 와 /sports/.* 가 다른 selector).
ALTER TABLE parsing_rules
  DROP CONSTRAINT IF EXISTS parsing_rules_lookup_key_unique;

ALTER TABLE parsing_rules
  ADD CONSTRAINT parsing_rules_lookup_key_unique
    UNIQUE (host_pattern, path_pattern, target_type, version);

-- idx_parsing_rules_lookup 은 그대로 유지 — Resolver 가 host_pattern + target_type 으로 후보 slice 를
-- fetch 하고 application 측에서 path_pattern regex 매칭. SQL 레벨엔 path_pattern 필터 없음.
