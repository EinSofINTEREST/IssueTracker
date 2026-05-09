-- 020 DOWN: news 카테고리 interval 을 1800 → 7200 으로 복원.
--
-- 운영자 manual override 가 1800 외 다른 값을 가졌다면 본 DOWN 은 그 값을 건드리지 않음.

UPDATE scheduler_entries
SET interval_seconds = 7200,
    updated_at = NOW()
WHERE category = 'news'
  AND interval_seconds = 1800;
