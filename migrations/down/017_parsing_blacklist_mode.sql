-- 017_parsing_blacklist_mode (down): mode 컬럼 제거 (이슈 #297 롤백).

ALTER TABLE parsing_blacklist DROP COLUMN IF EXISTS mode;
