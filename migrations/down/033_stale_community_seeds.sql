-- 033_stale_community_seeds (down): 022 시점의 시드 상태로 되돌린다 (이슈 #504)
--
-- 주의: 되돌리면 확인된 실패가 다시 시작된다.
--   - pgr21: Anubis 차단이 유지되는 한 30분 주기 404 실패 재개
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
