-- 018_parsing_rules_page_type: parsing_rules 에 page_type 메타데이터 컬럼 추가 (이슈 #326)
--
-- 배경:
--   claudegen multi-step extraction 이 LLM 응답에서 페이지 도메인 분류
--   (news / community / info / commercial / paper / other) 을 함께 추출. 이는 추후
--   정보 신뢰도 시스템 (별도 후속 이슈) 의 1차 입력으로 사용 — news / paper 는 높은
--   신뢰도 weight, commercial 은 낮은 weight 등.
--
-- 스키마:
--   - page_type: TEXT — '' (빈 문자열) = 미분류 (기존 row + non-claudegen 경로)
--   - 도메인 분류는 application 측 검증 — DB CHECK 제약은 두지 않음 (확장 친화적):
--     새 분류 추가 시 application struct 만 갱신, migration 불필요
--
-- 진화 친화적 — page_type_confidence 등 추가 메타데이터는 향후 동일 패턴으로 컬럼 추가.

ALTER TABLE parsing_rules
  ADD COLUMN IF NOT EXISTS page_type TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN parsing_rules.page_type IS
  '페이지 도메인 분류 (news/community/info/commercial/paper/other). claudegen multi-step extraction 이 추출. 빈 문자열은 미분류. 이슈 #326.';
