package postgres

import (
  "context"
  "encoding/json"
  "errors"
  "fmt"
  "strings"

  "github.com/jackc/pgx/v5"
  "github.com/jackc/pgx/v5/pgconn"
  "github.com/jackc/pgx/v5/pgxpool"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/internal/storage"
  "ecoscrapper/pkg/logger"
)

// pgArticleRepository는 pgx/v5 기반 ArticleRepository 구현체입니다.
type pgArticleRepository struct {
  pool *pgxpool.Pool
  log  *logger.Logger
}

// NewArticleRepository는 pgxpool을 사용하는 ArticleRepository를 생성합니다.
func NewArticleRepository(pool *pgxpool.Pool, log *logger.Logger) storage.ArticleRepository {
  return &pgArticleRepository{pool: pool, log: log}
}

// Save는 article을 upsert합니다.
// URL 충돌 시 content_hash가 변경된 경우에만 content 필드를 업데이트합니다.
func (r *pgArticleRepository) Save(ctx context.Context, article *core.Article) error {
  _, err := r.pool.Exec(ctx, `
    INSERT INTO articles (
      id, source_id, country, language,
      title, body, summary,
      author, published_at, updated_at, category, tags,
      url, canonical_url, image_urls,
      content_hash, word_count, created_at
    ) VALUES (
      $1, $2, $3, $4,
      $5, $6, $7,
      $8, $9, $10, $11, $12,
      $13, $14, $15,
      $16, $17, $18
    )
    ON CONFLICT (url) DO UPDATE SET
      title        = EXCLUDED.title,
      body         = EXCLUDED.body,
      summary      = EXCLUDED.summary,
      author       = EXCLUDED.author,
      updated_at   = EXCLUDED.updated_at,
      category     = EXCLUDED.category,
      tags         = EXCLUDED.tags,
      image_urls   = EXCLUDED.image_urls,
      content_hash = EXCLUDED.content_hash,
      word_count   = EXCLUDED.word_count
    WHERE articles.content_hash != EXCLUDED.content_hash
  `,
    article.ID, article.SourceID, article.Country, article.Language,
    article.Title, article.Body, article.Summary,
    article.Author, article.PublishedAt, article.UpdatedAt, article.Category,
    article.Tags,      // pgx/v5: []string → TEXT[] 자동 변환
    article.URL, article.CanonicalURL,
    article.ImageURLs, // pgx/v5: []string → TEXT[] 자동 변환
    article.ContentHash, article.WordCount, article.CreatedAt,
  )
  if err != nil {
    return fmt.Errorf("save article %s: %w", article.ID, err)
  }

  return nil
}

// SaveBatch는 여러 article을 단일 트랜잭션으로 저장합니다.
func (r *pgArticleRepository) SaveBatch(ctx context.Context, articles []*core.Article) error {
  tx, err := r.pool.Begin(ctx)
  if err != nil {
    return fmt.Errorf("begin transaction: %w", err)
  }
  defer tx.Rollback(ctx) //nolint:errcheck

  for _, article := range articles {
    _, err := tx.Exec(ctx, `
      INSERT INTO articles (
        id, source_id, country, language,
        title, body, summary,
        author, published_at, updated_at, category, tags,
        url, canonical_url, image_urls,
        content_hash, word_count, created_at
      ) VALUES (
        $1, $2, $3, $4,
        $5, $6, $7,
        $8, $9, $10, $11, $12,
        $13, $14, $15,
        $16, $17, $18
      )
      ON CONFLICT (url) DO UPDATE SET
        title        = EXCLUDED.title,
        body         = EXCLUDED.body,
        summary      = EXCLUDED.summary,
        author       = EXCLUDED.author,
        updated_at   = EXCLUDED.updated_at,
        category     = EXCLUDED.category,
        tags         = EXCLUDED.tags,
        image_urls   = EXCLUDED.image_urls,
        content_hash = EXCLUDED.content_hash,
        word_count   = EXCLUDED.word_count
      WHERE articles.content_hash != EXCLUDED.content_hash
    `,
      article.ID, article.SourceID, article.Country, article.Language,
      article.Title, article.Body, article.Summary,
      article.Author, article.PublishedAt, article.UpdatedAt, article.Category,
      article.Tags,
      article.URL, article.CanonicalURL,
      article.ImageURLs,
      article.ContentHash, article.WordCount, article.CreatedAt,
    )
    if err != nil {
      return fmt.Errorf("batch save article %s: %w", article.ID, err)
    }
  }

  return tx.Commit(ctx)
}

// GetByID는 ID로 article을 조회합니다.
func (r *pgArticleRepository) GetByID(ctx context.Context, id string) (*core.Article, error) {
  row := r.pool.QueryRow(ctx, `
    SELECT id, source_id, country, language,
           title, body, summary,
           author, published_at, updated_at, category, tags,
           url, canonical_url, image_urls,
           content_hash, word_count, created_at
    FROM articles WHERE id = $1
  `, id)

  article, err := scanArticle(row)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return nil, storage.ErrNotFound
    }
    return nil, fmt.Errorf("get article by id %s: %w", id, err)
  }

  return article, nil
}

// GetByURL은 URL로 article을 조회합니다.
func (r *pgArticleRepository) GetByURL(ctx context.Context, url string) (*core.Article, error) {
  row := r.pool.QueryRow(ctx, `
    SELECT id, source_id, country, language,
           title, body, summary,
           author, published_at, updated_at, category, tags,
           url, canonical_url, image_urls,
           content_hash, word_count, created_at
    FROM articles WHERE url = $1
  `, url)

  article, err := scanArticle(row)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return nil, storage.ErrNotFound
    }
    return nil, fmt.Errorf("get article by url: %w", err)
  }

  return article, nil
}

// GetByContentHash는 content_hash로 article을 조회합니다.
func (r *pgArticleRepository) GetByContentHash(ctx context.Context, hash string) (*core.Article, error) {
  row := r.pool.QueryRow(ctx, `
    SELECT id, source_id, country, language,
           title, body, summary,
           author, published_at, updated_at, category, tags,
           url, canonical_url, image_urls,
           content_hash, word_count, created_at
    FROM articles
    WHERE content_hash = $1
    LIMIT 1
  `, hash)

  article, err := scanArticle(row)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return nil, storage.ErrNotFound
    }
    return nil, fmt.Errorf("get article by content hash: %w", err)
  }

  return article, nil
}

// List는 필터 조건에 맞는 article 목록을 반환합니다.
func (r *pgArticleRepository) List(ctx context.Context, filter storage.ArticleFilter) ([]*core.Article, error) {
  query, args := buildArticleListQuery(filter)

  rows, err := r.pool.Query(ctx, query, args...)
  if err != nil {
    return nil, fmt.Errorf("list articles: %w", err)
  }
  defer rows.Close()

  articles := make([]*core.Article, 0)
  for rows.Next() {
    article, err := scanArticle(rows)
    if err != nil {
      return nil, fmt.Errorf("scan article: %w", err)
    }
    articles = append(articles, article)
  }

  return articles, rows.Err()
}

// Count는 필터 조건에 맞는 article 총 개수를 반환합니다.
func (r *pgArticleRepository) Count(ctx context.Context, filter storage.ArticleFilter) (int64, error) {
  query, args := buildArticleCountQuery(filter)

  var count int64
  if err := r.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
    return 0, fmt.Errorf("count articles: %w", err)
  }

  return count, nil
}

// Delete는 ID로 article을 삭제합니다.
func (r *pgArticleRepository) Delete(ctx context.Context, id string) error {
  _, err := r.pool.Exec(ctx, `DELETE FROM articles WHERE id = $1`, id)
  if err != nil {
    return fmt.Errorf("delete article %s: %w", id, err)
  }

  return nil
}

// ExistsByURL은 해당 URL의 article 존재 여부를 확인합니다.
func (r *pgArticleRepository) ExistsByURL(ctx context.Context, url string) (bool, error) {
  var exists bool
  err := r.pool.QueryRow(ctx,
    `SELECT EXISTS(SELECT 1 FROM articles WHERE url = $1)`, url,
  ).Scan(&exists)
  if err != nil {
    return false, fmt.Errorf("check article exists by url: %w", err)
  }

  return exists, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 내부 헬퍼 함수
// ─────────────────────────────────────────────────────────────────────────────

// scanArticle은 pgx Row/Rows를 core.Article로 스캔합니다.
// pgx/v5에서 []string은 TEXT[]로, *time.Time은 nullable TIMESTAMPTZ로 자동 매핑됩니다.
func scanArticle(row pgx.Row) (*core.Article, error) {
  var a core.Article
  err := row.Scan(
    &a.ID, &a.SourceID, &a.Country, &a.Language,
    &a.Title, &a.Body, &a.Summary,
    &a.Author, &a.PublishedAt, &a.UpdatedAt, &a.Category, &a.Tags,
    &a.URL, &a.CanonicalURL, &a.ImageURLs,
    &a.ContentHash, &a.WordCount, &a.CreatedAt,
  )
  if err != nil {
    return nil, err
  }

  return &a, nil
}

// buildArticleListQuery는 ArticleFilter를 기반으로 동적 SELECT 쿼리를 생성합니다.
func buildArticleListQuery(filter storage.ArticleFilter) (string, []any) {
  base := `
    SELECT id, source_id, country, language,
           title, body, summary,
           author, published_at, updated_at, category, tags,
           url, canonical_url, image_urls,
           content_hash, word_count, created_at
    FROM articles
  `
  where, args := buildArticleWhere(filter)

  limit := filter.Pagination.Limit
  if limit <= 0 {
    limit = 50
  }

  query := base + where +
    fmt.Sprintf(" ORDER BY published_at DESC LIMIT %d OFFSET %d", limit, filter.Pagination.Offset)

  return query, args
}

// buildArticleCountQuery는 ArticleFilter를 기반으로 COUNT 쿼리를 생성합니다.
func buildArticleCountQuery(filter storage.ArticleFilter) (string, []any) {
  where, args := buildArticleWhere(filter)
  return "SELECT COUNT(*) FROM articles" + where, args
}

// buildArticleWhere는 ArticleFilter를 기반으로 WHERE 절과 인자 목록을 반환합니다.
// 제로값 필드는 조건에서 제외됩니다.
func buildArticleWhere(filter storage.ArticleFilter) (string, []any) {
  var conditions []string
  var args []any
  n := 1

  if filter.Country != "" {
    conditions = append(conditions, fmt.Sprintf("country = $%d", n))
    args = append(args, filter.Country)
    n++
  }
  if filter.Language != "" {
    conditions = append(conditions, fmt.Sprintf("language = $%d", n))
    args = append(args, filter.Language)
    n++
  }
  if filter.Category != "" {
    conditions = append(conditions, fmt.Sprintf("category = $%d", n))
    args = append(args, filter.Category)
    n++
  }
  if filter.Source != "" {
    conditions = append(conditions, fmt.Sprintf("source_id = $%d", n))
    args = append(args, filter.Source)
    n++
  }
  if filter.PublishedAfter != nil {
    conditions = append(conditions, fmt.Sprintf("published_at >= $%d", n))
    args = append(args, *filter.PublishedAfter)
    n++
  }
  if filter.PublishedBefore != nil {
    conditions = append(conditions, fmt.Sprintf("published_at <= $%d", n))
    args = append(args, *filter.PublishedBefore)
    n++
  }
  if len(filter.Tags) > 0 {
    // tags @> ARRAY[$n] — 지정된 태그를 모두 포함
    conditions = append(conditions, fmt.Sprintf("tags @> $%d", n))
    args = append(args, filter.Tags)
    n++
  }
  if filter.MinWordCount > 0 {
    conditions = append(conditions, fmt.Sprintf("word_count >= $%d", n))
    args = append(args, filter.MinWordCount)
    n++ //nolint:ineffassign
  }

  if len(conditions) == 0 {
    return "", args
  }

  return " WHERE " + strings.Join(conditions, " AND "), args
}

// isPgUniqueViolation은 pgconn.PgError가 유일성 제약 위반(23505)인지 확인합니다.
func isPgUniqueViolation(err error) bool {
  var pgErr *pgconn.PgError
  return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// mustMarshalJSON은 값을 JSON 바이트로 변환합니다.
// 변환 실패는 개발 오류이므로 패닉 대신 빈 객체를 반환합니다.
func mustMarshalJSON(v any) []byte {
  b, err := json.Marshal(v)
  if err != nil {
    return []byte("{}")
  }

  return b
}
