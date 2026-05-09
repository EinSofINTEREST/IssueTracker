-- 021 DOWN: community 진입점 제거.
--
-- scheduler_entries 의 community 카테고리 row + fetcher_rules 의 source_type='community'
-- row 를 일괄 삭제. 운영자 manual 등록 row 도 삭제될 수 있어 보수적으로 source 명단으로
-- 한정.
--
-- parsing_rules 의 LLM 자동 학습 결과 (host 별 row) 는 본 DOWN 에서 건드리지 않음 —
-- 운영자가 별도로 정리.

DELETE FROM scheduler_entries
WHERE category = 'community'
  AND source_name IN ('theqoo', 'clien', 'fmkorea', 'dcinside', 'reddit', 'hackernews');

DELETE FROM fetcher_rules
WHERE source_type = 'community'
  AND source_name IN ('theqoo', 'clien', 'fmkorea', 'dcinside', 'reddit', 'hackernews');
