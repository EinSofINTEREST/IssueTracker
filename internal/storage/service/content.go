// service 패키지는 repository 인터페이스 위에 비즈니스 로직을 제공합니다.
// 중복 감지, 필터링 등 순수 CRUD 이상의 로직을 담당합니다.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// StoreResult는 StoreBatch에서 각 Content의 저장 결과를 나타냅니다.
type StoreResult struct {
	ContentID   string
	IsDuplicate bool
	Err         error
}

// ContentService는 Content에 대한 비즈니스 로직 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
type ContentService interface {
	// Store는 중복 감지를 포함하여 content를 저장합니다.
	// ContentHash가 동일한 컨텐츠가 이미 존재하면 저장하지 않고 기존 ID를 반환합니다.
	// 반환값: (id, isDuplicate, error)
	Store(ctx context.Context, content *core.Content) (id string, isDuplicate bool, err error)

	// StoreBatch는 여러 content를 항목별로 중복 감지하며 저장합니다.
	StoreBatch(ctx context.Context, contents []*core.Content) ([]StoreResult, error)

	// GetByID는 ID로 content를 조회합니다 (본문 포함 전체 데이터).
	GetByID(ctx context.Context, id string) (*core.Content, error)

	// ListByCountry는 특정 국가의 content를 최신순으로 반환합니다.
	ListByCountry(ctx context.Context, country string, filter storage.ContentFilter) ([]*core.Content, error)

	// Search는 다양한 조건으로 content를 검색합니다.
	Search(ctx context.Context, filter storage.ContentFilter) ([]*core.Content, error)

	// CountByCountry는 최근 N일간 국가별 content 수를 반환합니다.
	CountByCountry(ctx context.Context, days int) (map[string]int64, error)

	// Delete는 ID로 content를 삭제합니다.
	// ON DELETE CASCADE로 content_bodies, content_meta도 함께 삭제됩니다.
	// 존재하지 않아도 에러를 반환하지 않습니다.
	Delete(ctx context.Context, id string) error

	// UpdateValidationStatus 는 URL 기준으로 validator 결과 메타데이터를 갱신합니다 (이슈 #135 / #161).
	// 자세한 내용은 storage.ContentRepository.UpdateValidationStatus 참조.
	UpdateValidationStatus(ctx context.Context, url, status, code, detail string) error
}

// contentService는 ContentService의 구현체입니다.
type contentService struct {
	repo storage.ContentRepository
	log  *logger.Logger
}

// NewContentService는 주어진 repository를 사용하는 ContentService를 생성합니다.
func NewContentService(repo storage.ContentRepository, log *logger.Logger) ContentService {
	return &contentService{repo: repo, log: log}
}

// Store는 중복 감지 후 content를 저장합니다.
//
// 중복 감지 순서:
//  1. ContentHash가 비어있지 않으면 GetByContentHash로 중복 확인
//  2. 기존 레코드 있으면 (existingID, true, nil) 반환
//  3. ErrNotFound면 repo.Save 후 (content.ID, false, nil) 반환
func (s *contentService) Store(ctx context.Context, content *core.Content) (string, bool, error) {
	// ContentHash 기반 중복 감지 (비어있으면 생략)
	if content.ContentHash != "" {
		existing, err := s.repo.GetByContentHash(ctx, content.ContentHash)
		if err == nil {
			// 동일 content_hash 존재 → 중복
			s.log.WithFields(map[string]interface{}{
				"existing_id":  existing.ID,
				"content_hash": content.ContentHash,
			}).Debug("duplicate content detected by content hash")
			return existing.ID, true, nil
		}

		if !errors.Is(err, storage.ErrNotFound) {
			return "", false, core.NewStorageError(core.CodeStorageRead, "check duplicate", true, err)
		}
	}

	if err := s.repo.Save(ctx, content); err != nil {
		return "", false, core.NewStorageError(core.CodeStorageWrite, "save content", true, err)
	}

	return content.ID, false, nil
}

// StoreBatch는 각 content에 대해 독립적으로 중복 감지 후 저장합니다.
func (s *contentService) StoreBatch(ctx context.Context, contents []*core.Content) ([]StoreResult, error) {
	results := make([]StoreResult, 0, len(contents))

	for _, content := range contents {
		id, isDuplicate, err := s.Store(ctx, content)
		results = append(results, StoreResult{
			ContentID:   id,
			IsDuplicate: isDuplicate,
			Err:         err,
		})
	}

	return results, nil
}

// GetByID는 ID로 content를 조회합니다.
func (s *contentService) GetByID(ctx context.Context, id string) (*core.Content, error) {
	content, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, core.NewStorageError(core.CodeStorageRead, "get content by id", true, err)
	}

	return content, nil
}

// ListByCountry는 특정 국가의 content를 필터와 함께 조회합니다.
func (s *contentService) ListByCountry(
	ctx context.Context,
	country string,
	filter storage.ContentFilter,
) ([]*core.Content, error) {
	filter.Country = country

	contents, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, core.NewStorageError(
			core.CodeStorageRead,
			fmt.Sprintf("list contents by country %s", country),
			true,
			err,
		)
	}

	return contents, nil
}

// Search는 ContentFilter 조건으로 content를 검색합니다.
func (s *contentService) Search(ctx context.Context, filter storage.ContentFilter) ([]*core.Content, error) {
	contents, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, core.NewStorageError(core.CodeStorageRead, "search contents", true, err)
	}

	return contents, nil
}

// Delete는 ID로 content를 삭제합니다.
func (s *contentService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// UpdateValidationStatus 는 URL 기준으로 validator 결과 메타데이터를 갱신합니다.
// repo 의 동일 메소드를 그대로 위임합니다 (현재 추가 비즈니스 로직 없음).
func (s *contentService) UpdateValidationStatus(ctx context.Context, url, status, code, detail string) error {
	return s.repo.UpdateValidationStatus(ctx, url, status, code, detail)
}

// CountByCountry는 최근 N일간 국가별 content 수를 반환합니다.
// 각 알려진 국가에 대해 Count를 호출하여 집계합니다.
func (s *contentService) CountByCountry(ctx context.Context, days int) (map[string]int64, error) {
	after := time.Now().AddDate(0, 0, -days)
	countries := []string{"US", "KR"}

	result := make(map[string]int64, len(countries))

	for _, country := range countries {
		count, err := s.repo.Count(ctx, storage.ContentFilter{
			Country:        country,
			PublishedAfter: &after,
		})
		if err != nil {
			return nil, core.NewStorageError(
				core.CodeStorageRead,
				fmt.Sprintf("count contents for country %s", country),
				true,
				err,
			)
		}

		result[country] = count
	}

	return result, nil
}
