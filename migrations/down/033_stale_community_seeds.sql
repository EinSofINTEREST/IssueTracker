-- 033_stale_community_seeds (down): 022 시점의 시드 상태로 되돌린다 (이슈 #504)
--
-- ⚠️ 이 파일만 되돌리려면 psql 로 직접 적용할 것.
--
--   cmd/migrate-down (make pg-migrate-down) 은 **down/ 의 모든 파일을 역순 실행** 한다
--   (migrations/runner.go 의 Rollback). 033 만 되돌리는 명령이 아니며, 그대로 쓰면
--   이전 schema / data 마이그레이션까지 전부 롤백된다. 본 변경은 데이터 갱신 3건이므로
--   개별 롤백이 필요하면 아래처럼 이 파일만 실행하는 편이 안전하다.
--
--     make pg-psql
--     \i migrations/down/033_stale_community_seeds.sql
--
-- 주의: 되돌리면 확인된 실패가 다시 시작된다.
--   - pgr21: Anubis 차단이 유지되는 한 10분 주기 404 실패 재개
--   - inven: /board/* 가 404 이므로 수집 불가 상태로 복귀
-- 차단 해제 / 경로 복구를 확인한 뒤에만 적용할 것.

UPDATE scheduler_entries
SET url        = 'https://www.inven.co.kr/board/it',
    notes      = 'IT 게시판',
    updated_at = NOW()
WHERE category    = 'community'
  AND source_name = 'inven'
  AND url         = 'https://www.inven.co.kr/webzine/news/?site=it';

UPDATE scheduler_entries
SET url        = 'https://www.inven.co.kr/board/webzine',
    notes      = 'webzine',
    updated_at = NOW()
WHERE category    = 'community'
  AND source_name = 'inven'
  AND url         = 'https://www.inven.co.kr/webzine/news/';

UPDATE scheduler_entries
SET enabled    = TRUE,
    notes      = '자유게시판',
    updated_at = NOW()
WHERE category    = 'community'
  AND source_name = 'pgr21'
  AND url         = 'https://www.pgr21.com/freedom';
