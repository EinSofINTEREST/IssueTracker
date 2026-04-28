-- 005_news_articles_validation_tracking: validator 결과 추적용 컬럼 추가 (이슈 #135)
--
-- 배경:
--   #132 라이브 검증으로 validator 가 maxRetries 영구 실패 시 contents 에서 record 를
--   삭제하면 reject 사유 추적이 불가능함이 확정됐다 (worker.go:205, contentSvc.Delete).
--   news_articles 는 chain_handler 가 INSERT 한 후 그대로 남으므로 검증 결과 메타데이터를
--   여기에 보관하여 사후 추적 / 운영 metric / 알림 기반 작업의 단일 source 로 활용한다.
--
-- 변경 사항:
--   1. validation_status — pending / passed / rejected
--   2. reject_code      — VAL_001 ~ VAL_006 등 (rejected 시에만 NOT NULL 의미)
--   3. reject_detail    — validator errors array JSON 직렬화
--   4. 인덱스           — (validation_status, created_at DESC)

ALTER TABLE news_articles
  ADD COLUMN validation_status TEXT NOT NULL DEFAULT 'pending',
  ADD COLUMN reject_code       VARCHAR(20),
  ADD COLUMN reject_detail     TEXT,
  ADD CONSTRAINT news_articles_validation_status_check
    CHECK (validation_status IN ('pending', 'passed', 'rejected'));

-- reject 분포 / 시계열 조회 가속 (운영 metric / 대시보드 지원)
CREATE INDEX IF NOT EXISTS idx_news_articles_validation_status
  ON news_articles (validation_status, created_at DESC);
