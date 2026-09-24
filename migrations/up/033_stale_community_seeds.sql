-- 033_stale_community_seeds: 반복 실패하던 커뮤니티 시드 정리 (이슈 #504)
--
-- 배경:
--   scheduler_entries 의 'https://www.pgr21.com/freedom' 이 30분 주기로 계속 실패하며
--   무의미한 트래픽과 노이즈 로그를 만들고 있었다 (이슈 #504). 원인 조사 과정에서 같은
--   성격의 시드를 함께 점검했고 (이슈의 "동일 패턴 sweep" 조건), inven 2건도 죽어 있었다.
--
-- 실측 (2026-09-24, 직접 요청 확인):
--
--   | 시드                                    | 결과                                    |
--   |----------------------------------------|----------------------------------------|
--   | https://www.pgr21.com/freedom          | Anubis v1.25.0 "Access Denied"          |
--   | https://www.pgr21.com (루트)            | 동일 — 사이트 전체 차단                   |
--   | https://www.inven.co.kr/board/it       | 404 "요청하신 페이지를 표시할 수 없습니다" |
--   | https://www.inven.co.kr/board/webzine  | 404 (동일)                              |
--   | https://bbs.ruliweb.com/community      | 정상                                    |
--   | https://www.bobaedream.co.kr/list?code=best | 정상                               |
--   | https://lemmy.world/c/news             | 정상 (이슈 #505 의 403 은 IP 기반 추정)   |
--
-- ── pgr21: 비활성화 ──────────────────────────────────────────────────────────
--
--   이슈 #504 는 원인을 "사이트 구조 변경으로 URL 이 사라짐" 으로 추정했으나 진단이 달랐다.
--   /freedom 이 사라진 것이 아니라 **사이트 전체가 Anubis anti-bot 뒤로 들어갔다.**
--   루트도 같은 차단 페이지를 반환하므로 URL 을 고쳐도 해결되지 않는다.
--   크롤러 쪽에는 HTTP 404 로 관측됐다 (Anubis 가 차단 시 반환하는 상태).
--
--   우회하지 않는 이유: Anubis 는 proof-of-work 기반이라 chromedp 로 JS 챌린지를 풀면
--   기술적으로 통과 가능하다. 그러나 사이트 운영자가 자동 수집을 원하지 않는다는 의사를
--   명시적으로 표시한 장치이며, 본 저장소는 같은 성격의 CAPTCHA solving service 를 윤리적
--   사유로 금지한다 (.claude/rules/02-crawler-implementation.md). 동일 원칙을 적용한다.
--
--   삭제가 아니라 enabled=FALSE 로 둔다 — 차단이 풀렸을 때 notes 와 함께 되살릴 수 있도록.
--
-- ── inven: URL 교체 ──────────────────────────────────────────────────────────
--
--   /board/* 경로 자체가 없어졌고 웹진 경로로 이동했다. 대체 URL 은 실제 요청으로
--   기사 목록이 렌더링되는 것을 확인했다 (기사 링크 형식: /webzine/news/?news=<id>).
--
--     board/webzine → https://www.inven.co.kr/webzine/news/
--     board/it      → https://www.inven.co.kr/webzine/news/?site=it   (IT 인벤 섹션)
--
--   parser_rules 는 llmgen 이 런타임 생성하므로 경로 변경과 충돌하지 않고,
--   fetcher_rules 의 'www.inven.co.kr' → chromedp 는 host 단위라 그대로 유효하다.
--
-- ── 미확인 ───────────────────────────────────────────────────────────────────
--
--   mlbpark.donga.com 은 확인 도구의 fetch 정책상 조회하지 못했다. 다음 라이브 회차의
--   로그로 판단한다 (이슈 #504 의 "라이브 로그 sweep" 조건은 실행 환경이 필요해 미완).

-- pgr21 — Anubis 차단으로 비활성화
UPDATE scheduler_entries
SET enabled    = FALSE,
    notes      = '자유게시판 — 2026-09-24 비활성화: 사이트 전체가 Anubis anti-bot 도입 (이슈 #504). 차단 해제 확인 시 재활성화',
    updated_at = NOW()
WHERE category    = 'community'
  AND source_name = 'pgr21'
  AND url         = 'https://www.pgr21.com/freedom';

-- inven — /board/* 소멸, 웹진 경로로 교체
UPDATE scheduler_entries
SET url        = 'https://www.inven.co.kr/webzine/news/',
    notes      = 'webzine — 2026-09-24 URL 교체 (구 /board/webzine 404, 이슈 #504)',
    updated_at = NOW()
WHERE category    = 'community'
  AND source_name = 'inven'
  AND url         = 'https://www.inven.co.kr/board/webzine';

UPDATE scheduler_entries
SET url        = 'https://www.inven.co.kr/webzine/news/?site=it',
    notes      = 'IT 게시판 — 2026-09-24 URL 교체 (구 /board/it 404, 이슈 #504)',
    updated_at = NOW()
WHERE category    = 'community'
  AND source_name = 'inven'
  AND url         = 'https://www.inven.co.kr/board/it';
