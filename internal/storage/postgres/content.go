package postgres

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"
  "strings"

  "github.com/jackc/pgx/v5"
  "github.com/jackc/pgx/v5/pgxpool"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/internal/storage"
  "ecoscrapper/pkg/logger"
)

// pgContentRepository는 pgx/v5 기반 ContentRepository 구현체입니다.
// 3개 테이블(contents, content_bodies, content_meta)을 투명하게 관리합니다.
type pgContentRepository struct {
  pool *pgxpool.Pool
  log  *logger.Logger
}

// NewContentRepository는 pgxpool을 사용하는 ContentRepository를 생성합니다.
func NewContentRepository(pool *pgxpool.Pool, log *logger.Logger) storage.ContentRepository {
  return &pgContentRepository{pool: pool, log: log}
}

// Save는 content를 3개 테이블에 트랜잭션으로 upsert합니다.
// URL 충돌 시 content_hash가 변경된 경우에만 핵심 필드를 업데이트합니다.
func (r *pgContentRepository) Save(ctx context.Context, c *core.Content) error {
  tx, err := r.pool.Begin(ctx)
  if err != nil {
    return fmt.Errorf("begin transaction: %w", err)
  }
  defer tx.Rollback(ctx) //nolint:errcheck

  if err := saveContentTx(ctx, tx, c); err != nil {
    return err
  }

  return tx.Commit(ctx)
}

// SaveBatch는 여러 content를 단일 트랜잭션으로 저장합니다.
// pgx.Batch를 사용하여 네트워크 왕복을 최소화합니다.
func (r *pgContentRepository) SaveBatch(ctx context.Context, contents []*core.Content) error {
  tx, err := r.pool.Begin(ctx)
  if err != nil {
    return fmt.Errorf("begin transaction: %w", err)
  }
  defer tx.Rollback(ctx) //nolint:errcheck

  batch := &pgx.Batch{}
  for _, c := range contents {
    queueContentBatch(batch, c)
  }

  br := tx.SendBatch(ctx, batch)
  // 각 쿼리 결과를 소비해야 BatchResults가 닫힙니다
  totalQueries := len(contents) * 3 // contents + content_bodies + content_meta
  for i := 0; i < totalQueries; i++ {
    if _, err := br.Exec(); err != nil {
      _ = br.Close()
      return fmt.Errorf("batch save content (query %d): %w", i, err)
    }
  }
  if err := br.Close(); err != nil {
    return fmt.Errorf("close batch results: %w", err)
  }

  return tx.Commit(ctx)
}

// GetByID는 ID로 content를 조회합니다 (3테이블 JOIN).
func (r *pgContentRepository) GetByID(ctx context.Context, id string) (*core.Content, error) {
  row := r.pool.QueryRow(ctx, fullSelectQuery+"WHERE c.id = $1", id)
  content, err := scanContent(row)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return nil, storage.ErrNotFound
    }
    return nil, fmt.Errorf("get content by id %s: %w", id, err)
  }
  return content, nil
}

// GetByURL은 URL로 content를 조회합니다 (3테이블 JOIN).
func (r *pgContentRepository) GetByURL(ctx context.Context, url string) (*core.Content, error) {
  row := r.pool.QueryRow(ctx, fullSelectQuery+"WHERE c.url = $1", url)
  content, err := scanContent(row)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return nil, storage.ErrNotFound
    }
    return nil, fmt.Errorf("get content by url: %w", err)
  }
  return content, nil
}

// GetByContentHash는 content_hash로 content를 조회합니다 (3테이블 JOIN).
// 중복 감지에 사용됩니다.
func (r *pgContentRepository) GetByContentHash(ctx context.Context, hash string) (*core.Content, error) {
  row := r.pool.QueryRow(ctx, fullSelectQuery+"WHERE c.content_hash = $1 LIMIT 1", hash)
  content, err := scanContent(row)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return nil, storage.ErrNotFound
    }
    return nil, fmt.Errorf("get content by content hash: %w", err)
  }
  return content, nil
}

// List는 필터 조건에 맞는 content 목록을 반환합니다.
// contents + content_bodies(summary, word_count만) LEFT JOIN.
// body, image_urls, extra는 목록에서 제외됩니다.
func (r *pgContentRepository) List(ctx context.Context, filter storage.ContentFilter) ([]*core.Content, error) {
  query, args := buildContentListQuery(filter)

  rows, err := r.pool.Query(ctx, query, args...)
  if err != nil {
    return nil, fmt.Errorf("list contents: %w", err)
  }
  defer rows.Close()

  contents := make([]*core.Content, 0)
  for rows.Next() {
    content, err := scanContentList(rows)
    if err != nil {
      return nil, fmt.Errorf("scan content: %w", err)
    }
    contents = append(contents, content)
  }

  return contents, rows.Err()
}

// Count는 필터 조건에 맞는 content 총 개수를 반환합니다.
func (r *pgContentRepository) Count(ctx context.Context, filter storage.ContentFilter) (int64, error) {
  query, args := buildContentCountQuery(filter)

  var count int64
  if err := r.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
    return 0, fmt.Errorf("count contents: %w", err)
  }

  return count, nil
}

// Delete는 ID로 content를 삭제합니다.
// ON DELETE CASCADE로 content_bodies, content_meta도 자동 삭제됩니다.
func (r *pgContentRepository) Delete(ctx context.Context, id string) error {
  _, err := r.pool.Exec(ctx, `DELETE FROM contents WHERE id = $1`, id)
  if err != nil {
    return fmt.Errorf("delete content %s: %w", id, err)
  }
  return nil
}

// ExistsByURL은 해당 URL의 content가 존재하는지 확인합니다.
func (r *pgContentRepository) ExistsByURL(ctx context.Context, url string) (bool, error) {
  var exists bool
  err := r.pool.QueryRow(ctx,
    `SELECT EXISTS(SELECT 1 FROM contents WHERE url = $1)`, url,
  ).Scan(&exists)
  if err != nil {
    return false, fmt.Errorf("check content exists by url: %w", err)
  }
  return exists, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 내부 헬퍼 함수
// ─────────────────────────────────────────────────────────────────────────────

// fullSelectQuery는 3테이블 JOIN 전체 조회 쿼리의 공통 부분입니다.
// 마지막에 WHERE 절을 붙여 사용합니다.
const fullSelectQuery = `
  SELECT c.id, c.source_id, c.source_type, c.country, c.language,
         c.title, COALESCE(b.body, '') AS body, COALESCE(b.summary, '') AS summary,
         c.author, c.published_at, c.updated_at, c.category, c.tags,
         c.url, c.canonical_url,
         COALESCE(m.image_urls, '{}') AS image_urls,
         c.content_hash, COALESCE(b.word_count, 0) AS word_count,
         c.reliability, c.created_at,
         m.extra
  FROM contents c
  LEFT JOIN content_bodies b ON b.content_id = c.id
  LEFT JOIN content_meta m ON m.content_id = c.id
`

// saveContentTx는 트랜잭션 내에서 3개 테이블에 content를 upsert합니다.
func saveContentTx(ctx context.Context, tx pgx.Tx, c *core.Content) error {
  // 1. contents upsert (URL 기준, content_hash 변경 시에만 업데이트)
  _, err := tx.Exec(ctx, `
    INSERT INTO contents (
      id, source_id, source_type, country, language,
      title, author, published_at, updated_at, category, tags,
      url, canonical_url, content_hash, reliability, created_at
    ) VALUES (
      $1, $2, $3, $4, $5,
      $6, $7, $8, $9, $10, $11,
      $12, $13, $14, $15, $16
    )
    ON CONFLICT (url) DO UPDATE SET
      source_type  = EXCLUDED.source_type,
      title        = EXCLUDED.title,
      author       = EXCLUDED.author,
      updated_at   = EXCLUDED.updated_at,
      category     = EXCLUDED.category,
      tags         = EXCLUDED.tags,
      canonical_url = EXCLUDED.canonical_url,
      content_hash = EXCLUDED.content_hash,
      reliability  = EXCLUDED.reliability
    WHERE contents.content_hash != EXCLUDED.content_hash
  `,
    c.ID, c.SourceID, string(c.SourceType), c.Country, c.Language,
    c.Title, c.Author, c.PublishedAt, c.UpdatedAt, c.Category, c.Tags,
    c.URL, c.CanonicalURL, c.ContentHash, c.Reliability, c.CreatedAt,
  )
  if err != nil {
    return fmt.Errorf("upsert contents %s: %w", c.ID, err)
  }

  // 2. content_bodies upsert
  _, err = tx.Exec(ctx, `
    INSERT INTO content_bodies (content_id, body, summary, word_count)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (content_id) DO UPDATE SET
      body       = EXCLUDED.body,
      summary    = EXCLUDED.summary,
      word_count = EXCLUDED.word_count
  `, c.ID, c.Body, c.Summary, c.WordCount)
  if err != nil {
    return fmt.Errorf("upsert content_bodies %s: %w", c.ID, err)
  }

  // 3. content_meta upsert
  extraJSON := mustMarshalJSON(c.Extra)
  imageURLs := c.ImageURLs
  if imageURLs == nil {
    imageURLs = []string{}
  }
  _, err = tx.Exec(ctx, `
    INSERT INTO content_meta (content_id, image_urls, extra)
    VALUES ($1, $2, $3)
    ON CONFLICT (content_id) DO UPDATE SET
      image_urls = EXCLUDED.image_urls,
      extra      = EXCLUDED.extra
  `, c.ID, imageURLs, extraJSON)
  if err != nil {
    return fmt.Errorf("upsert content_meta %s: %w", c.ID, err)
  }

  return nil
}

// queueContentBatch는 pgx.Batch에 content 관련 3개 쿼리를 추가합니다.
func queueContentBatch(batch *pgx.Batch, c *core.Content) {
  batch.Queue(`
    INSERT INTO contents (
      id, source_id, source_type, country, language,
      title, author, published_at, updated_at, category, tags,
      url, canonical_url, content_hash, reliability, created_at
    ) VALUES (
      $1, $2, $3, $4, $5,
      $6, $7, $8, $9, $10, $11,
      $12, $13, $14, $15, $16
    )
    ON CONFLICT (url) DO UPDATE SET
      source_type  = EXCLUDED.source_type,
      title        = EXCLUDED.title,
      author       = EXCLUDED.author,
      updated_at   = EXCLUDED.updated_at,
      category     = EXCLUDED.category,
      tags         = EXCLUDED.tags,
      canonical_url = EXCLUDED.canonical_url,
      content_hash = EXCLUDED.content_hash,
      reliability  = EXCLUDED.reliability
    WHERE contents.content_hash != EXCLUDED.content_hash
  `,
    c.ID, c.SourceID, string(c.SourceType), c.Country, c.Language,
    c.Title, c.Author, c.PublishedAt, c.UpdatedAt, c.Category, c.Tags,
    c.URL, c.CanonicalURL, c.ContentHash, c.Reliability, c.CreatedAt,
  )

  batch.Queue(`
    INSERT INTO content_bodies (content_id, body, summary, word_count)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (content_id) DO UPDATE SET
      body       = EXCLUDED.body,
      summary    = EXCLUDED.summary,
      word_count = EXCLUDED.word_count
  `, c.ID, c.Body, c.Summary, c.WordCount)

  imageURLs := c.ImageURLs
  if imageURLs == nil {
    imageURLs = []string{}
  }
  batch.Queue(`
    INSERT INTO content_meta (content_id, image_urls, extra)
    VALUES ($1, $2, $3)
    ON CONFLICT (content_id) DO UPDATE SET
      image_urls = EXCLUDED.image_urls,
      extra      = EXCLUDED.extra
  `, c.ID, imageURLs, mustMarshalJSON(c.Extra))
}

// scanContent는 pgx Row를 core.Content로 스캔합니다 (3테이블 JOIN 결과).
// extra는 JSONB → []byte → map[string]interface{} 로 변환합니다.
func scanContent(row pgx.Row) (*core.Content, error) {
  var c core.Content
  var sourceType string
  var extraJSON []byte

  err := row.Scan(
    &c.ID, &c.SourceID, &sourceType, &c.Country, &c.Language,
    &c.Title, &c.Body, &c.Summary,
    &c.Author, &c.PublishedAt, &c.UpdatedAt, &c.Category, &c.Tags,
    &c.URL, &c.CanonicalURL, &c.ImageURLs,
    &c.ContentHash, &c.WordCount, &c.Reliability, &c.CreatedAt,
    &extraJSON,
  )
  if err != nil {
    return nil, err
  }

  c.SourceType = core.SourceType(sourceType)
  if extraJSON != nil {
    if err := json.Unmarshal(extraJSON, &c.Extra); err != nil {
      c.Extra = make(map[string]interface{})
    }
  }

  return &c, nil
}

// listSelectQuery는 List 조회 쿼리의 공통 부분입니다.
// body, image_urls, extra는 포함하지 않습니다.
const listSelectQuery = `
  SELECT c.id, c.source_id, c.source_type, c.country, c.language,
         c.title, COALESCE(b.summary, '') AS summary,
         c.author, c.published_at, c.updated_at, c.category, c.tags,
         c.url, c.canonical_url,
         c.content_hash, COALESCE(b.word_count, 0) AS word_count,
         c.reliability, c.created_at
  FROM contents c
  LEFT JOIN content_bodies b ON b.content_id = c.id
`

// scanContentList는 List 쿼리 결과를 core.Content로 스캔합니다.
// body, image_urls, extra는 빈 값으로 반환됩니다.
func scanContentList(row pgx.Row) (*core.Content, error) {
  var c core.Content
  var sourceType string

  err := row.Scan(
    &c.ID, &c.SourceID, &sourceType, &c.Country, &c.Language,
    &c.Title, &c.Summary,
    &c.Author, &c.PublishedAt, &c.UpdatedAt, &c.Category, &c.Tags,
    &c.URL, &c.CanonicalURL,
    &c.ContentHash, &c.WordCount, &c.Reliability, &c.CreatedAt,
  )
  if err != nil {
    return nil, err
  }

  c.SourceType = core.SourceType(sourceType)
  // 목록 조회이므로 body, image_urls, extra는 기본값 유지
  return &c, nil
}

// buildContentListQuery는 ContentFilter를 기반으로 동적 SELECT 쿼리를 생성합니다.
func buildContentListQuery(filter storage.ContentFilter) (string, []any) {
  where, args := buildContentWhere(filter)

  limit := filter.Pagination.Limit
  if limit <= 0 {
    limit = 50
  }

  query := listSelectQuery + where +
    fmt.Sprintf(" ORDER BY c.published_at DESC LIMIT %d OFFSET %d", limit, filter.Pagination.Offset)

  return query, args
}

// buildContentCountQuery는 ContentFilter를 기반으로 COUNT 쿼리를 생성합니다.
func buildContentCountQuery(filter storage.ContentFilter) (string, []any) {
  where, args := buildContentWhere(filter)
  return "SELECT COUNT(*) FROM contents c" + where, args
}

// buildContentWhere는 ContentFilter를 기반으로 WHERE 절과 인자 목록을 반환합니다.
// 제로값 필드는 조건에서 제외됩니다.
func buildContentWhere(filter storage.ContentFilter) (string, []any) {
  var conditions []string
  var args []any
  n := 1

  if filter.Country != "" {
    conditions = append(conditions, fmt.Sprintf("c.country = $%d", n))
    args = append(args, filter.Country)
    n++
  }
  if filter.Language != "" {
    conditions = append(conditions, fmt.Sprintf("c.language = $%d", n))
    args = append(args, filter.Language)
    n++
  }
  if filter.Category != "" {
    conditions = append(conditions, fmt.Sprintf("c.category = $%d", n))
    args = append(args, filter.Category)
    n++
  }
  if filter.Source != "" {
    conditions = append(conditions, fmt.Sprintf("c.source_id = $%d", n))
    args = append(args, filter.Source)
    n++
  }
  if filter.SourceType != "" {
    conditions = append(conditions, fmt.Sprintf("c.source_type = $%d", n))
    args = append(args, filter.SourceType)
    n++
  }
  if filter.PublishedAfter != nil {
    conditions = append(conditions, fmt.Sprintf("c.published_at >= $%d", n))
    args = append(args, *filter.PublishedAfter)
    n++
  }
  if filter.PublishedBefore != nil {
    conditions = append(conditions, fmt.Sprintf("c.published_at <= $%d", n))
    args = append(args, *filter.PublishedBefore)
    n++
  }
  if len(filter.Tags) > 0 {
    // tags @> ARRAY[$n] — 지정된 태그를 모두 포함
    conditions = append(conditions, fmt.Sprintf("c.tags @> $%d", n))
    args = append(args, filter.Tags)
    n++
  }
  if filter.MinReliability != nil {
    conditions = append(conditions, fmt.Sprintf("c.reliability >= $%d", n))
    args = append(args, *filter.MinReliability)
    n++ //nolint:ineffassign
  }

  if len(conditions) == 0 {
    return "", args
  }

  return " WHERE " + strings.Join(conditions, " AND "), args
}
