// service 패키지는 repository 인터페이스 위에 비즈니스 로직을 제공합니다.
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

// RawContentService는 RawContent에 대한 비즈니스 로직 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
type RawContentService interface {
	// Store는 중복 감지를 포함하여 RawContent를 저장합니다.
	// 동일 URL이 이미 존재하면 저장하지 않고 기존 ID를 반환합니다.
	// 반환값: (id, isDuplicate, error)
	Store(ctx context.Context, raw *core.RawContent) (id string, isDuplicate bool, err error)

	// GetByID는 ID로 RawContent를 조회합니다.
	GetByID(ctx context.Context, id string) (*core.RawContent, error)

	// List는 필터 조건에 맞는 RawContent 목록을 반환합니다.
	List(ctx context.Context, filter storage.RawContentFilter) ([]*core.RawContent, error)

	// PurgeOlderThan은 cutoff 이전에 수집된 원본 데이터를 일괄 삭제합니다.
	// 원본 데이터 보존 정책(기본 90일) 적용에 사용됩니다.
	// 삭제된 레코드 수를 반환합니다.
	PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// rawContentService는 RawContentService의 구현체입니다.
type rawContentService struct {
	repo storage.RawContentRepository
	log  *logger.Logger
}

// NewRawContentService는 주어진 repository를 사용하는 RawContentService를 생성합니다.
func NewRawContentService(repo storage.RawContentRepository, log *logger.Logger) RawContentService {
	return &rawContentService{repo: repo, log: log}
}

// Store는 URL 중복 감지 후 RawContent를 저장합니다.
//
// 중복 감지 순서:
//  1. repo.Save 호출
//  2. ErrDuplicate 반환 시 repo.GetByURL로 기존 레코드 ID 조회
//  3. (existingID, true, nil) 반환
func (s *rawContentService) Store(ctx context.Context, raw *core.RawContent) (string, bool, error) {
	err := s.repo.Save(ctx, raw)
	if err == nil {
		return raw.ID, false, nil
	}

	if !errors.Is(err, storage.ErrDuplicate) {
		return "", false, fmt.Errorf("save raw content: %w", err)
	}

	// 동일 URL 존재 → 기존 레코드 ID 조회
	existing, err := s.repo.GetByURL(ctx, raw.URL)
	if err != nil {
		return "", false, fmt.Errorf("get existing raw content by url: %w", err)
	}

	s.log.WithFields(map[string]interface{}{
		"existing_id": existing.ID,
		"url":         raw.URL,
	}).Debug("duplicate raw content detected by url")

	return existing.ID, true, nil
}

// GetByID는 ID로 RawContent를 조회합니다.
func (s *rawContentService) GetByID(ctx context.Context, id string) (*core.RawContent, error) {
	raw, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get raw content by id: %w", err)
	}

	return raw, nil
}

// List는 RawContentFilter 조건으로 RawContent를 조회합니다.
func (s *rawContentService) List(ctx context.Context, filter storage.RawContentFilter) ([]*core.RawContent, error) {
	raws, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list raw contents: %w", err)
	}

	return raws, nil
}

// PurgeOlderThan은 cutoff 이전 데이터를 일괄 삭제합니다.
func (s *rawContentService) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := s.repo.DeleteBefore(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge raw contents older than %s: %w", cutoff.Format(time.RFC3339), err)
	}

	s.log.WithFields(map[string]interface{}{
		"deleted_count": n,
		"cutoff":        cutoff.Format(time.RFC3339),
	}).Info("raw content purge completed")

	return n, nil
}
