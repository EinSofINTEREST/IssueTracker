-- 024 DOWN: search 카테고리 entry 제거 — 정확한 (source_name, url) 매칭으로 운영자 manual row 보호.

DELETE FROM scheduler_entries
WHERE category = 'search'
  AND (source_name, url) IN (
    ('google_cse', 'https://customsearch.googleapis.com/customsearch/v1')
  );
