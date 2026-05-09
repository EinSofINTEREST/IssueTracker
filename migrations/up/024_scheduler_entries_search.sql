-- 024_scheduler_entries_search: search 카테고리 진입 entry seed (이슈 #336 / 부모 #331)
--
-- 배경:
--   #331 의 phase 1 — search 카테고리의 첫 entry (Google CSE 기반) 를 scheduler_entries 에 seed.
--   실제 CSE 호출 / handler 로직은 phase 2 (#337) 에서 implement. 본 마이그레이션은 schema-side
--   준비만.
--
-- 설계 메모:
--   google_cse 는 일반 fetcher 체인 (goquery / chromedp) 을 거치지 않고 phase 2 의 dedicated
--   search handler 가 직접 CSE API 를 호출. 따라서 fetcher_rules 에는 row 를 만들지 않음 —
--   RegisterAll 이 Google CSE 를 ChainHandler 로 잘못 wrapping 하는 것을 방지.
--   scheduler_entries.source_name='google_cse' 는 handler dispatch 의 키로 사용.
--
-- 정책:
--   - source_name='google_cse' — phase 2 search handler 가 본 source_name 으로 dispatch
--   - target_type='search_results' — keyword × entry cross product 으로 article URL 발행 신호
--   - interval_seconds=21600 (6h) — Google CSE 무료 plan (100 q/day) 보수적 운용
--     (keyword 16개 × 1 entry × 4 cycle/day = 64 q/day, free tier 안)
--   - priority=3 (low) — news/community 보다 낮음 (long-tail historical sweep 성격)
--   - metadata 에 engine / per_query_max_results / date_range_days 명시 — phase 2 의 CSE
--     client 가 본 metadata 로 호출 파라미터 결정

INSERT INTO scheduler_entries (category, source_name, url, target_type, interval_seconds, priority, enabled, metadata, notes)
VALUES
  ('search', 'google_cse',
   'https://customsearch.googleapis.com/customsearch/v1',
   'search_results', 21600, 3, TRUE,
   '{"engine":"google_cse","per_query_max_results":50,"date_range_days":365}',
   'Google CSE 기반 search entry — keyword × entry cross product 으로 article URL 발행 (phase 2)')
ON CONFLICT (category, source_name, url) DO NOTHING;
