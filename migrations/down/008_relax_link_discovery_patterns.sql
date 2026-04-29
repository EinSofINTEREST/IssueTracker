-- 008_relax_link_discovery_patterns (down): migration 007 의 좁은 ArticleURLPattern 으로 복원.

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "^https?://n\\.news\\.naver\\.com/(?:mnews/)?article/\\d+/\\d+",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/comment/", "/photo/", "/tv/"]
  }'::jsonb
)
WHERE source_name = 'naver' AND host_pattern = 'news.naver.com' AND target_type = 'list';

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "^https?://v\\.daum\\.net/v/\\d+",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/cp/", "/cafe/", "/photo/"]
  }'::jsonb
)
WHERE source_name = 'daum' AND host_pattern = 'news.daum.net' AND target_type = 'list';

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "^https?://www\\.yna\\.co\\.kr/view/[A-Z]+\\d+",
    "same_origin_only":    true,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/photo/", "/video/"]
  }'::jsonb
)
WHERE source_name = 'yonhap' AND host_pattern = 'www.yna.co.kr' AND target_type = 'list';

UPDATE parsing_rules
SET selectors = jsonb_set(
  selectors,
  '{link_discovery}',
  '{
    "article_url_pattern": "^https?://edition\\.cnn\\.com/\\d{4}/\\d{2}/\\d{2}/",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/videos/", "/gallery/", "/live-news/"]
  }'::jsonb
)
WHERE source_name = 'cnn' AND host_pattern = 'edition.cnn.com' AND target_type = 'list';
