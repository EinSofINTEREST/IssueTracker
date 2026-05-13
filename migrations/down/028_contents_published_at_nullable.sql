-- 028_contents_published_at_nullable DOWN — published_at NOT NULL 복원 (이슈 #423).
--
-- 운영 영향:
--   현재 published_at IS NULL 인 row 가 존재하면 본 migration 은 실패.
--   롤백 전에 NULL row 정리 필요 (예: DELETE FROM contents WHERE published_at IS NULL).
--   복원은 ACCESS EXCLUSIVE lock + 즉시 해제 — rewrite 없음.

ALTER TABLE contents
  ALTER COLUMN published_at SET NOT NULL;
