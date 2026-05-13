-- 027_parser_rules_article_flag DOWN — article 컬럼 삭제 (이슈 #421).
--
-- 운영 영향: 컬럼 drop 은 ACCESS EXCLUSIVE lock + 데이터 영구 손실.
-- 롤백 시점에 article=TRUE 인 룰의 정보는 복원 불가.

ALTER TABLE parser_rules
  DROP COLUMN IF EXISTS article;
