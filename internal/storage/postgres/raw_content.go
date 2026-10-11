package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// pgRawContentRepository는 pgx/v5 기반 RawContentRepository 구현체입니다.
type pgRawContentRepository struct {
	pool *pgxpool.Pool
	log  *logger.Logger
}

// NewRawContentRepository는 pgxpool을 사용하는 RawContentRepository를 생성합니다.
func NewRawContentRepository(pool *pgxpool.Pool, log *logger.Logger) repository.RawContentRepository {
	return &pgRawContentRepository{pool: pool, log: log}
}

// Save는 RawContent를 저장합니다.
// URL 유일성 위반 시 ErrDuplicate를 반환합니다.
func (r *pgRawContentRepository) Save(ctx context.Context, raw *core.RawContent) error {
	headersJSON, err := json.Marshal(raw.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}

	metadataJSON, err := json.Marshal(raw.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
    INSERT INTO raw_contents (
      id, url, html, status_code, fetched_at,
      source_country, source_type, source_name, source_base_url, source_language,
      headers, metadata
    ) VALUES (
      $1, $2, $3, $4, $5,
      $6, $7, $8, $9, $10,
      $11, $12
    )
  `,
		raw.ID, raw.URL, raw.HTML, raw.StatusCode, raw.FetchedAt,
		raw.SourceInfo.Country, string(raw.SourceInfo.Type),
		raw.SourceInfo.Name, raw.SourceInfo.BaseURL, raw.SourceInfo.Language,
		headersJSON, metadataJSON,
	)
	if err != nil {
		if isPgUniqueViolation(err) {
			return storage.ErrDuplicate
		}
		return fmt.Errorf("save raw content %s: %w", raw.ID, err)
	}

	return nil
}

// GetByID는 ID로 RawContent를 조회합니다.
func (r *pgRawContentRepository) GetByID(ctx context.Context, id string) (*core.RawContent, error) {
	row := r.pool.QueryRow(ctx, `
    SELECT id, url, html, status_code, fetched_at,
           source_country, source_type, source_name, source_base_url, source_language,
           headers, metadata, created_at
    FROM raw_contents WHERE id = $1
  `, id)

	raw, err := scanRawContent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get raw content by id %s: %w", id, err)
	}

	return raw, nil
}

// GetByURL은 URL로 RawContent를 조회합니다.
func (r *pgRawContentRepository) GetByURL(ctx context.Context, url string) (*core.RawContent, error) {
	row := r.pool.QueryRow(ctx, `
    SELECT id, url, html, status_code, fetched_at,
           source_country, source_type, source_name, source_base_url, source_language,
           headers, metadata, created_at
    FROM raw_contents WHERE url = $1
  `, url)

	raw, err := scanRawContent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get raw content by url: %w", err)
	}

	return raw, nil
}

// List는 필터 조건에 맞는 RawContent 목록을 반환합니다.
func (r *pgRawContentRepository) List(ctx context.Context, filter model.RawContentFilter) ([]*core.RawContent, error) {
	query, args := buildRawContentListQuery(filter)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list raw contents: %w", err)
	}
	defer rows.Close()

	raws := make([]*core.RawContent, 0)
	for rows.Next() {
		raw, err := scanRawContent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan raw content: %w", err)
		}
		raws = append(raws, raw)
	}

	return raws, rows.Err()
}

// Delete는 ID로 RawContent를 삭제합니다.
func (r *pgRawContentRepository) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM raw_contents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete raw content %s: %w", id, err)
	}

	return nil
}

// DeleteBefore는 cutoff 이전에 수집된 원본 데이터를 일괄 삭제합니다.
func (r *pgRawContentRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.pool.Exec(ctx,
		`DELETE FROM raw_contents WHERE fetched_at < $1`, before,
	)
	if err != nil {
		return 0, fmt.Errorf("delete raw contents before %s: %w", before.Format(time.RFC3339), err)
	}

	return result.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 내부 헬퍼 함수
// ─────────────────────────────────────────────────────────────────────────────

// scanRawContent는 pgx Row/Rows를 core.RawContent로 스캔합니다.
// JSONB 컬럼(headers, metadata)은 json.Unmarshal로 역직렬화합니다.
// created_at은 core.RawContent에 없으므로 더미 변수로 소비합니다.
func scanRawContent(row pgx.Row) (*core.RawContent, error) {
	var (
		raw         core.RawContent
		sourceType  string
		headersJSON []byte
		metaJSON    []byte
		createdAt   time.Time // core.RawContent에 없는 DB 컬럼 — 더미 스캔
	)

	err := row.Scan(
		&raw.ID, &raw.URL, &raw.HTML, &raw.StatusCode, &raw.FetchedAt,
		&raw.SourceInfo.Country, &sourceType,
		&raw.SourceInfo.Name, &raw.SourceInfo.BaseURL, &raw.SourceInfo.Language,
		&headersJSON, &metaJSON, &createdAt,
	)
	if err != nil {
		return nil, err
	}

	raw.SourceInfo.Type = core.SourceType(sourceType)

	if len(headersJSON) > 0 && string(headersJSON) != "null" {
		if err := json.Unmarshal(headersJSON, &raw.Headers); err != nil {
			return nil, fmt.Errorf("unmarshal headers: %w", err)
		}
	}

	if len(metaJSON) > 0 && string(metaJSON) != "null" {
		if err := json.Unmarshal(metaJSON, &raw.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}

	return &raw, nil
}

// buildRawContentListQuery는 RawContentFilter를 기반으로 동적 SELECT 쿼리를 생성합니다.
func buildRawContentListQuery(filter model.RawContentFilter) (string, []any) {
	base := `
    SELECT id, url, html, status_code, fetched_at,
           source_country, source_type, source_name, source_base_url, source_language,
           headers, metadata, created_at
    FROM raw_contents
  `
	where, args := buildRawContentWhere(filter)

	limit := filter.Pagination.Limit
	if limit <= 0 {
		limit = 50
	}

	// content.go 와 동일한 가드 — 음수 offset 은 postgres 가 쿼리 자체를 거절한다.
	offset := filter.Pagination.Offset
	if offset < 0 {
		offset = 0
	}

	// id 는 tiebreaker (이슈 #654) — fetched_at 이 유일하지 않아 동률 행의 순서가
	// 보장되지 않으면 페이지를 넘길 때 행이 새거나 중복된다.
	query := base + where +
		fmt.Sprintf(" ORDER BY fetched_at DESC, id DESC LIMIT %d OFFSET %d", limit, offset)

	return query, args
}

// buildRawContentWhere는 RawContentFilter를 기반으로 WHERE 절과 인자 목록을 반환합니다.
func buildRawContentWhere(filter model.RawContentFilter) (string, []any) {
	var conditions []string
	var args []any
	n := 1

	if filter.Country != "" {
		conditions = append(conditions, fmt.Sprintf("source_country = $%d", n))
		args = append(args, filter.Country)
		n++
	}
	if filter.SourceName != "" {
		conditions = append(conditions, fmt.Sprintf("source_name = $%d", n))
		args = append(args, filter.SourceName)
		n++
	}
	if filter.FetchedAfter != nil {
		conditions = append(conditions, fmt.Sprintf("fetched_at >= $%d", n))
		args = append(args, *filter.FetchedAfter)
		n++
	}
	if filter.FetchedBefore != nil {
		conditions = append(conditions, fmt.Sprintf("fetched_at <= $%d", n))
		args = append(args, *filter.FetchedBefore)
		n++
	}
	if filter.StatusCode != 0 {
		conditions = append(conditions, fmt.Sprintf("status_code = $%d", n))
		args = append(args, filter.StatusCode)
		n++ //nolint:ineffassign
	}

	if len(conditions) == 0 {
		return "", args
	}

	return " WHERE " + strings.Join(conditions, " AND "), args
}
