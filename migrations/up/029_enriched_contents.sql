-- 029_enriched_contents: enrich 단계 결과 영속화 테이블 (이슈 #450)
--
-- 배경:
--   enricher worker 는 #447 ~ #449 단계에서 추출/검증/외부 맥락을 산출하고 ProcessingMessage.Metadata
--   에 임시 첨부했음. 본 migration 으로 별도 테이블에 영속화 — schema evolution + 재처리 안전.
--
-- 스키마 결정:
--   - content_id: contents.id (VARCHAR(255)) 의 FK. ON DELETE CASCADE 로 컨텐츠 삭제 시 동반 정리
--   - trust_score: NUMERIC(4,3) — 0.000 ~ 1.000 정밀도, CHECK constraint 로 범위 강제
--   - facts/verifications/context: JSONB — 향후 schema evolution 시 컬럼 추가 없이 필드 확장
--   - UNIQUE(content_id): 한 content 당 enriched record 1개 — 재처리 시 UPSERT
--   - idx_enriched_trust: trust_score 기반 필터링 쿼리 (예: 신뢰도 >= 0.8) 지원

CREATE TABLE IF NOT EXISTS enriched_contents (
  id            BIGSERIAL     NOT NULL,
  content_id    VARCHAR(255)  NOT NULL,
  trust_score   NUMERIC(4,3)  NOT NULL,
  facts         JSONB         NOT NULL DEFAULT '{}'::jsonb,
  verifications JSONB         NOT NULL DEFAULT '[]'::jsonb,
  context       JSONB         NOT NULL DEFAULT '{}'::jsonb,
  enriched_at   TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

  CONSTRAINT enriched_contents_pkey PRIMARY KEY (id),
  CONSTRAINT enriched_contents_content_fk
    FOREIGN KEY (content_id) REFERENCES contents(id) ON DELETE CASCADE,
  CONSTRAINT enriched_contents_trust_score_range
    CHECK (trust_score >= 0 AND trust_score <= 1),
  CONSTRAINT enriched_contents_content_id_unique UNIQUE (content_id)
);

CREATE INDEX IF NOT EXISTS idx_enriched_trust
  ON enriched_contents (trust_score);

CREATE INDEX IF NOT EXISTS idx_enriched_at
  ON enriched_contents (enriched_at DESC);

COMMENT ON TABLE enriched_contents IS
  '컨텐츠 enrichment 결과 (이슈 #445/#450) — LLM 추출 facts + cross-verify verdicts + 외부 context + 종합 신뢰도 점수.';
COMMENT ON COLUMN enriched_contents.trust_score IS
  'page 단위 신뢰도 0.000-1.000 — claim 지지율 / 소스 다양성 / 맥락 완전성 종합. CHECK constraint 로 범위 강제.';
