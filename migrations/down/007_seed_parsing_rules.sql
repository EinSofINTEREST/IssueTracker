-- 007_seed_parsing_rules (down): seed 한 사이트별 rule row 들을 제거.
-- 본 down 은 row 삭제만 수행 — schema 자체는 006 의 down 에서 제거.
--
-- 자연키 (source_name, host_pattern, target_type, version) 의 정확한 8개 row 만 삭제 —
-- 향후 v2 row 추가 / description 편집 시 의도치 않은 삭제 회피 (Coderabbit 피드백).

DELETE FROM parsing_rules
 WHERE (source_name, host_pattern, target_type, version) IN (
   ('naver',  'n.news.naver.com', 'page', 1),
   ('naver',  'news.naver.com',   'list', 1),
   ('daum',   'v.daum.net',       'page', 1),
   ('daum',   'news.daum.net',    'list', 1),
   ('yonhap', 'www.yna.co.kr',    'page', 1),
   ('yonhap', 'www.yna.co.kr',    'list', 1),
   ('cnn',    'edition.cnn.com',  'page', 1),
   ('cnn',    'edition.cnn.com',  'list', 1)
 );
