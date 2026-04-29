-- 010_drop_news_articles: 도메인 특화 news_articles 테이블 제거 (이슈 #161)
--
-- 사전 조건:
--   - migration 009 적용 (contents 에 validation tracking 컬럼 존재)
--   - 코드 측에서 NewsArticleRepository / PageToRecord / news_articles 의존 제거 완료
--
-- 영향:
--   - validator 결과 (passed/rejected) 가 contents 단일 테이블에 일원화됨
--   - 옛 news_articles 의 fetched_at 컬럼은 raw_contents 에 동일 정보가 존재하므로 손실 없음
--   - 옛 news_articles 의 reject_code/reject_detail 은 009 에서 contents 로 이전된 컬럼이
--     동일 역할을 수행 (단 본 migration 이 적용되는 시점부터의 신규 데이터에만 해당)
--
-- 운영 점검:
--   본 migration 이전에 news_articles 에 누적된 reject 메타데이터가 운영에 필요하다면
--   사전에 SELECT INTO TEMP / 별도 archive 테이블로 백업한 뒤 본 migration 을 적용해야 합니다.

DROP TABLE IF EXISTS news_articles;
