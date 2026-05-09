-- 019_scheduler_entries DOWN: scheduler_entries 테이블 + index 제거.

DROP INDEX IF EXISTS idx_scheduler_entries_enabled;
DROP TABLE IF EXISTS scheduler_entries;
