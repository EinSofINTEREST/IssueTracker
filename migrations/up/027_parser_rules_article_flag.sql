-- 027_parser_rules_article_flag: parser_rules 에 article BOOLEAN 컬럼 추가 (이슈 #421)
--
-- 배경:
--   target_type=page + page_type=news 만으로는 룰이 적용되는 페이지가 실제 뉴스 기사
--   본문 페이지인지 (article body 포함), 아니면 뉴스 인덱스 / 이미지 / 멀티미디어 페이지인지
--   구분 불가. 다운스트림 (validate / classifier / 통계) 가 정확한 분기를 못 함.
--
-- 스키마:
--   - article: BOOLEAN NOT NULL DEFAULT FALSE
--   - FALSE = 뉴스 인덱스 / 이미지 / 멀티미디어 / 기타 비-article 페이지
--   - TRUE  = 순수 뉴스 기사 본문 페이지 (title + main_content + published_at 등 추출 대상)
--
-- 보수적 default (FALSE) — 기존 룰 호환 + operator 명시적 opt-in. 다운스트림 활성화는
-- 별도 후속 issue (validate strict 검증 article=false 룰에 완화 / classifier 통합 등).

ALTER TABLE parser_rules
  ADD COLUMN IF NOT EXISTS article BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN parser_rules.article IS
  '룰이 적용되는 페이지가 뉴스 기사 본문인지 (news 인덱스 / 이미지 / 멀티미디어 제외). true 면 article body 추출 대상 — validate 의 PublishedAt 등 strict 검증 적용. false 면 비-article 페이지 — 향후 downstream 처리 분기에 사용. 이슈 #421.';
