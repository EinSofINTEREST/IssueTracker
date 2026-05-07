-- 017_parsing_blacklist_mode: parsing_blacklist 에 mode 컬럼 추가 (이슈 #297)
--
-- 배경:
--   #295 (머지됨, 'drop' 단일 정책) 의 후속 — 분류 대상별 적합도 차이 cover.
--   광고/sponsored 는 그 안의 링크도 광고일 가능성 ↑ → 'drop' 적합.
--   비-article 영역 (about/login/sitemap/menu) 은 그 안에 정상 article 링크 다수 →
--   'extract_links_only' 모드로 list 강제 발행 → ParseLinks 만 진행, ParsePage skip.
--
-- 운영자가 row 단위로 mode 선택 가능. 기존 row 는 default 'drop' — 후방 호환.

ALTER TABLE parsing_blacklist
  ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'drop'
  CHECK (mode IN ('drop', 'extract_links_only'));

COMMENT ON COLUMN parsing_blacklist.mode IS
  'drop: URL 자체 drop (default) / extract_links_only: list 로 강제 발행 → ParseLinks 만 진행, ParsePage skip. 이슈 #297.';
