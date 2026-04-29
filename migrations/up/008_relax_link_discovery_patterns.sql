-- 008_relax_link_discovery_patterns: 사이트별 list rule 의 ArticleURLPattern 을 빈 문자열로 변경
-- + ExcludePatterns 를 사이트별 노이즈 컷 패턴으로 강화 (이슈 #148).
--
-- 배경:
--   migration 007 에서 시드한 list rule 들은 좁은 ArticleURLPattern regex 를 사용하여
--   사이트별 \"기사 URL\" 만 통과시킴. 그러나 본 시스템 타겟은 article 만이 아닌 페이지 내
--   모든 의미 있는 글 (이슈 #100 도메인 일반화). 본 migration 으로 all-pass 모드 전환:
--
--   - article_url_pattern: \"\" (빈 문자열) → all-pass discovery
--   - exclude_patterns: 사이트별 광고/네비/공유/미디어 등 노이즈 패턴
--   - same_origin_only: true (외부 도메인 차단)
--   - max_links_per_page: 200 유지 (publish 폭증 방지)
--
-- 후속 노이즈 컷 정밀화는 운영자가 라이브 모니터링 후 ExcludePatterns 를 점진 추가.

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/comment/", "/photo/", "/tv/", "/about", "/help", "/login", "/sitemap"]
  }'::jsonb
)
WHERE source_name = 'naver' AND host_pattern = 'news.naver.com' AND target_type = 'list';

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/cp/", "/cafe/", "/photo/", "/about", "/help", "/login"]
  }'::jsonb
)
WHERE source_name = 'daum' AND host_pattern = 'news.daum.net' AND target_type = 'list';

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "",
    "same_origin_only":    true,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/photo/", "/video/", "/about", "/help", "/login", "/sitemap"]
  }'::jsonb
)
WHERE source_name = 'yonhap' AND host_pattern = 'www.yna.co.kr' AND target_type = 'list';

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/videos/", "/gallery/", "/live-news/", "/about", "/help"]
  }'::jsonb
)
WHERE source_name = 'cnn' AND host_pattern = 'edition.cnn.com' AND target_type = 'list';
