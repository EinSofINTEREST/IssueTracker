-- 023 DOWN: search_keywords 테이블 제거 — 본 마이그레이션이 INSERT 한 정확한 row 만 삭제 후 DROP.
--
-- 운영자가 manual 로 추가한 keyword 가 있을 수 있으므로 본 DOWN 은 보수적으로 동작:
--   1) 본 seed 16개 keyword 만 명시 매칭하여 삭제 시도
--   2) 그럼에도 테이블 자체를 DROP 하므로 운영자 수동 row 도 함께 사라짐 — 운영자가 본 DOWN
--      적용 전에 별도 export 필요. 본 주석으로 명시.

DROP INDEX IF EXISTS idx_search_keywords_enabled;
DROP TABLE IF EXISTS search_keywords;
