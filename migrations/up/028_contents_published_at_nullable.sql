-- 028_contents_published_at_nullable: contents.published_at NOT NULL → NULL 허용 (이슈 #423)
--
-- 배경:
--   기존 스키마는 published_at TIMESTAMPTZ NOT NULL — 모든 컨텐츠가 published_at 있어야 저장 가능.
--   news 도메인의 비-article 페이지 (인덱스 / 이미지 / 멀티미디어) 는 published_at 부재가 정상인데도
--   validator 가 reject + reparse trigger → 무한 재학습 → DLQ 도달 + 불필요 cost.
--
-- 정책 (이슈 #421, #423):
--   - parser_rules.article=TRUE 룰로 파싱된 article body 페이지만 PublishedAt 강제 (news validator)
--   - parser_rules.article=FALSE 룰로 파싱된 페이지는 published_at NULL 허용
--
-- 운영 영향:
--   ALTER COLUMN DROP NOT NULL 은 PostgreSQL 에서 metadata only — rewrite 없음, ACCESS EXCLUSIVE lock
--   순간만 잡고 즉시 해제. 운영 무중단.

ALTER TABLE contents
  ALTER COLUMN published_at DROP NOT NULL;

COMMENT ON COLUMN contents.published_at IS
  '컨텐츠 발행 시각. NULL = published_at 미상 (parser_rules.article=FALSE 룰로 파싱된 뉴스 인덱스 / 이미지 / 비-article 페이지). 이슈 #423.';
