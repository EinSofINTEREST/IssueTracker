-- 016_parsing_blacklist (down): parsing_blacklist 테이블 제거 (이슈 #295 롤백).

DROP INDEX IF EXISTS idx_parsing_blacklist_host_enabled;
DROP TABLE IF EXISTS parsing_blacklist;
