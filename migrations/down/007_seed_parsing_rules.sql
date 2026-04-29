-- 007_seed_parsing_rules (down): seed 한 사이트별 rule row 들을 제거.
-- 본 down 은 row 삭제만 수행 — schema 자체는 006 의 down 에서 제거.

DELETE FROM parsing_rules
 WHERE source_name IN ('naver', 'daum', 'yonhap', 'cnn')
   AND description LIKE '%이슈 #139%';
