package storage

import (
  "context"

  "ecoscrapper/internal/crawler/core"
)

// ArticleRepository는 Article CRUD 연산을 위한 데이터 접근 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
// pgx/v5 구현체: internal/storage/postgres/article.go
type ArticleRepository interface {
  // Save는 article을 저장합니다 (URL 기준 upsert).
  // content_hash가 동일하면 업데이트 없이 기존 레코드를 유지합니다.
  Save(ctx context.Context, article *core.Article) error

  // SaveBatch는 여러 article을 단일 트랜잭션으로 저장합니다.
  // 일부 실패 시 전체 트랜잭션이 롤백됩니다.
  SaveBatch(ctx context.Context, articles []*core.Article) error

  // GetByID는 ID로 article을 조회합니다.
  // 존재하지 않으면 ErrNotFound를 반환합니다.
  GetByID(ctx context.Context, id string) (*core.Article, error)

  // GetByURL은 URL로 article을 조회합니다.
  // 존재하지 않으면 ErrNotFound를 반환합니다.
  GetByURL(ctx context.Context, url string) (*core.Article, error)

  // GetByContentHash는 content_hash로 article을 조회합니다.
  // 중복 감지에 사용됩니다. 존재하지 않으면 ErrNotFound를 반환합니다.
  GetByContentHash(ctx context.Context, hash string) (*core.Article, error)

  // List는 필터 조건에 맞는 article 목록을 published_at DESC 순으로 반환합니다.
  List(ctx context.Context, filter ArticleFilter) ([]*core.Article, error)

  // Count는 필터 조건에 맞는 article 총 개수를 반환합니다 (페이지네이션용).
  Count(ctx context.Context, filter ArticleFilter) (int64, error)

  // Delete는 ID로 article을 삭제합니다. 존재하지 않아도 에러를 반환하지 않습니다.
  Delete(ctx context.Context, id string) error

  // ExistsByURL은 해당 URL의 article이 존재하는지 확인합니다.
  ExistsByURL(ctx context.Context, url string) (bool, error)
}
