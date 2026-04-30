-- 011_parsing_rules_path_pattern (down): path_pattern 컬럼 제거 + 기존 자연키 복원
--
-- 주의:
--   동일 (host_pattern, target_type, version) 에 path_pattern 만 다른 row 가 두 개 이상 존재하면
--   복원된 UNIQUE constraint 위반으로 ADD 가 실패한다. 운영자는 down 적용 전 중복 row 를 정리해야 한다
--   (예: path_pattern='' 인 row 만 남기거나, 가장 우선순위 높은 row 만 enabled 로 두고 나머지 삭제).

ALTER TABLE parsing_rules
  DROP CONSTRAINT IF EXISTS parsing_rules_lookup_key_unique;

ALTER TABLE parsing_rules
  ADD CONSTRAINT parsing_rules_lookup_key_unique
    UNIQUE (host_pattern, target_type, version);

ALTER TABLE parsing_rules
  DROP COLUMN IF EXISTS path_pattern;
