// service 패키지의 blacklist.go 는 parser_blacklist 도메인의 비즈니스 로직을 캡슐화합니다 (이슈 #431).
//
// 책임:
//   - LLM blacklist 결정 처리 (handleBlacklistDecision 흡수 — 이슈 #326)
//     · sample URL → path_pattern regex 변환
//     · ErrDuplicate graceful 흡수
//     · over-reach (host-wide catch-all) 회피
//   - Decorator chain 합성 (timeout + invalidating cache) — wiring 측 boilerplate 제거
//   - 일반 CRUD 위임 (Insert / Update / Delete / GetByID / FindEnabledByHost / List)
package service

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"time"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/decorator"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// BlacklistService 는 parser_blacklist 도메인의 비즈니스 boundary 입니다.
type BlacklistService interface {
	// HandleLLMDecision 은 LLM blacklist 판정 결과를 parser_blacklist 에 자동 등록합니다 (이슈 #326).
	//
	//   - sample URL 의 path 를 escape + ^/$ anchor 한 정확 일치 regex 로 path_pattern 생성
	//   - URL parse 실패 → insert skip (host-wide catch-all over-reach 회피)
	//   - INSERT 성공 → (true, nil)
	//   - ErrDuplicate (이미 등록) → (false, nil) — 정상 (graceful 흡수)
	//   - 그 외 INSERT 에러 → (false, err) — 호출자 정책 (현재 모두 warn 로그 후 흐름 계속)
	HandleLLMDecision(ctx context.Context, host, sampleURL string, targetType model.TargetType, reason string) (inserted bool, err error)

	// Insert 는 row 를 직접 INSERT (운영자 manual 또는 다른 source 경로용).
	Insert(ctx context.Context, rec *model.BlacklistRecord) error
	Update(ctx context.Context, rec *model.BlacklistRecord) error
	Delete(ctx context.Context, id int64) error
	GetByID(ctx context.Context, id int64) (*model.BlacklistRecord, error)
	FindEnabledByHost(ctx context.Context, host string) ([]*model.BlacklistRecord, error)
	List(ctx context.Context, filter model.BlacklistFilter) ([]*model.BlacklistRecord, error)
}

// blacklistService 는 BlacklistService 의 구현체입니다.
type blacklistService struct {
	repo repository.BlacklistRepository
	log  *logger.Logger
}

// BlacklistServiceOption 은 NewBlacklistService 생성자 옵션입니다.
type BlacklistServiceOption func(*blacklistServiceOptions)

type blacklistServiceOptions struct {
	timeout     time.Duration // 0 = 미적용
	invalidator decorator.BlacklistInvalidator
}

// WithBlacklistQueryTimeout 은 repository 메서드에 query-level timeout 을 적용합니다.
func WithBlacklistQueryTimeout(d time.Duration) BlacklistServiceOption {
	return func(o *blacklistServiceOptions) { o.timeout = d }
}

// WithBlacklistInvalidator 는 mutation 후 자동으로 cache invalidate 를 트리거합니다.
func WithBlacklistInvalidator(inv decorator.BlacklistInvalidator) BlacklistServiceOption {
	return func(o *blacklistServiceOptions) { o.invalidator = inv }
}

// NewBlacklistService 는 BlacklistService 인스턴스를 생성합니다 (이슈 #431).
//
// Decorator chain 자동 합성 (안쪽 → 바깥쪽):
//
//	repo → invalidator (optional) → timeout (optional) → service
//
// timeout 은 mutation/read 모든 메서드 진입에 적용 (decorator.WrapBlacklistWithTimeout).
// invalidator 는 mutation 메서드 (Insert/Update/Delete) 성공 후 host cache 무효화.
func NewBlacklistService(repo repository.BlacklistRepository, log *logger.Logger, opts ...BlacklistServiceOption) BlacklistService {
	o := &blacklistServiceOptions{}
	for _, opt := range opts {
		opt(o)
	}
	wrapped := repo
	if o.invalidator != nil {
		wrapped = decorator.WrapBlacklistWithInvalidator(wrapped, o.invalidator)
	}
	if o.timeout > 0 {
		wrapped = decorator.WrapBlacklistWithTimeout(wrapped, o.timeout)
	}
	return &blacklistService{repo: wrapped, log: log}
}

// HandleLLMDecision 흡수 — 기존 llmgen.Generator.handleBlacklistDecision 의 로직.
func (s *blacklistService) HandleLLMDecision(ctx context.Context, host, sampleURL string, targetType model.TargetType, reason string) (bool, error) {
	logFields := map[string]interface{}{
		"host":             host,
		"sample_url":       sampleURL,
		"target_type":      string(targetType),
		"blacklist_reason": reason,
	}

	pathPattern := pathPatternFromURL(sampleURL)
	if pathPattern == "" {
		// URL parse 실패 — host-wide catch-all 회피.
		s.log.WithFields(logFields).Warn("blacklist insert skipped — sample URL parse failed (host-wide catch-all 회피)")
		return false, nil
	}
	rec := &model.BlacklistRecord{
		HostPattern: host,
		PathPattern: pathPattern,
		Reason:      reason,
		Source:      model.BlacklistSourceAuto,
		Mode:        model.BlacklistModeDrop,
		Enabled:     true,
	}
	if err := s.repo.Insert(ctx, rec); err != nil {
		if errors.Is(err, storage.ErrDuplicate) {
			s.log.WithFields(logFields).Info("page already in blacklist — selector insert skipped")
			return false, nil
		}
		// Insert 실패는 non-fatal — 호출자가 warn 로그 후 흐름 계속.
		s.log.WithFields(logFields).WithError(err).Warn("blacklist insert failed (non-fatal — selector insert still skipped)")
		return false, err
	}
	logFields["blacklist_id"] = rec.ID
	s.log.WithFields(logFields).Info("page auto-blacklisted by LLM, selector insert skipped")
	return true, nil
}

// pathPatternFromURL 은 sampleURL 의 path 부분을 RE2 regex 로 escape 한 anchor pattern 을 반환합니다.
//
// 정책:
//   - URL parse 실패 → "" 반환 (호출자가 insert skip)
//   - 정상 parse + 빈 path → "/" 로 normalize → "^/$" pattern
//   - 정상 parse + non-empty path → "^<escaped>$"
func pathPatternFromURL(sampleURL string) string {
	u, err := url.Parse(sampleURL)
	if err != nil {
		return ""
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return "^" + regexp.QuoteMeta(path) + "$"
}

// 이하 CRUD 위임.

func (s *blacklistService) Insert(ctx context.Context, rec *model.BlacklistRecord) error {
	return s.repo.Insert(ctx, rec)
}
func (s *blacklistService) Update(ctx context.Context, rec *model.BlacklistRecord) error {
	return s.repo.Update(ctx, rec)
}
func (s *blacklistService) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}
func (s *blacklistService) GetByID(ctx context.Context, id int64) (*model.BlacklistRecord, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *blacklistService) FindEnabledByHost(ctx context.Context, host string) ([]*model.BlacklistRecord, error) {
	return s.repo.FindEnabledByHost(ctx, host)
}
func (s *blacklistService) List(ctx context.Context, filter model.BlacklistFilter) ([]*model.BlacklistRecord, error) {
	return s.repo.List(ctx, filter)
}
