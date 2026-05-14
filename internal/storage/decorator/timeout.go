// Package storage 의 timeout_decorators.go 는 모든 Repository 인터페이스에 query-level
// timeout 을 부여하는 decorator 모음입니다 (이슈 #427).
//
// 설계 결정 (CodeRabbit / gemini 피드백 반영):
//   - **Pool-level 데코레이터 채택 안 함** — pgx 의 Query / QueryRow / Begin / SendBatch 는
//     반환된 객체 (pgx.Rows / pgx.Row / pgx.Tx / pgx.BatchResults) 가 ctx 를 후속 작업
//     (rows 읽기 / tx 진행 / batch 처리) 에 사용. 호출 직후 `defer cancel()` 하면 후속 동작 invalid.
//   - **Repository 인터페이스 레벨 데코레이터 채택** — 각 메서드가 rows 를 메서드 내부에서 완전
//     소비 후 결과만 반환하므로 메서드 종료 시점 cancel 안전.
//   - **timeout 은 instance 필드** — gemini medium 피드백 반영. 글로벌 atomic 제거 + 다중 DB
//     pool 시나리오 대비.
//   - **context.WithTimeout 의 deadline merge** — 호출자 ctx 가 더 짧은 deadline 보유 시
//     context.WithTimeout 이 자연스럽게 더 짧은 것을 채택 (caller-priority 자동).
package decorator

import (
	"context"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
)

// withTimeout 은 d > 0 일 때 ctx 에 timeout 을 적용하고, d == 0 면 ctx 그대로 + no-op cancel
// 을 반환합니다 (이슈 #427).
//
// context.WithTimeout 은 부모 ctx 의 deadline 이 더 짧으면 그대로 보존하므로 caller-priority
// 추가 로직이 불필요합니다 (gemini medium 피드백).
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.ContentRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutContentRepo struct {
	inner   repository.ContentRepository
	timeout time.Duration
}

// WrapContentWithTimeout 은 repository.ContentRepository 의 모든 메서드 진입에 query-level timeout 을
// 적용하는 decorator 를 반환합니다 (이슈 #427).
func WrapContentWithTimeout(r repository.ContentRepository, d time.Duration) repository.ContentRepository {
	return &timeoutContentRepo{inner: r, timeout: d}
}

func (t *timeoutContentRepo) Save(ctx context.Context, c *core.Content) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Save(ctx, c)
}

func (t *timeoutContentRepo) SaveBatch(ctx context.Context, contents []*core.Content) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.SaveBatch(ctx, contents)
}

func (t *timeoutContentRepo) GetByID(ctx context.Context, id string) (*core.Content, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByID(ctx, id)
}

func (t *timeoutContentRepo) GetByURL(ctx context.Context, url string) (*core.Content, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByURL(ctx, url)
}

func (t *timeoutContentRepo) GetByContentHash(ctx context.Context, hash string) (*core.Content, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByContentHash(ctx, hash)
}

func (t *timeoutContentRepo) List(ctx context.Context, filter model.ContentFilter) ([]*core.Content, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.List(ctx, filter)
}

func (t *timeoutContentRepo) Count(ctx context.Context, filter model.ContentFilter) (int64, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Count(ctx, filter)
}

func (t *timeoutContentRepo) Delete(ctx context.Context, id string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, id)
}

func (t *timeoutContentRepo) ExistsByURL(ctx context.Context, url string) (bool, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.ExistsByURL(ctx, url)
}

func (t *timeoutContentRepo) UpdateValidationStatus(ctx context.Context, id, status, code, detail string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.UpdateValidationStatus(ctx, id, status, code, detail)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.ParserRuleRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutParserRuleRepo struct {
	inner   repository.ParserRuleRepository
	timeout time.Duration
}

// WrapParserRuleWithTimeout 은 repository.ParserRuleRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapParserRuleWithTimeout(r repository.ParserRuleRepository, d time.Duration) repository.ParserRuleRepository {
	return &timeoutParserRuleRepo{inner: r, timeout: d}
}

func (t *timeoutParserRuleRepo) Insert(ctx context.Context, r *model.ParserRuleRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Insert(ctx, r)
}

func (t *timeoutParserRuleRepo) Update(ctx context.Context, r *model.ParserRuleRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Update(ctx, r)
}

func (t *timeoutParserRuleRepo) UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.UpdatePathPattern(ctx, id, pattern, description)
}

func (t *timeoutParserRuleRepo) GetByID(ctx context.Context, id int64) (*model.ParserRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByID(ctx, id)
}

func (t *timeoutParserRuleRepo) FindActive(ctx context.Context, host string, targetType model.TargetType) (*model.ParserRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.FindActive(ctx, host, targetType)
}

func (t *timeoutParserRuleRepo) InsertNextVersion(ctx context.Context, r *model.ParserRuleRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.InsertNextVersion(ctx, r)
}

func (t *timeoutParserRuleRepo) HasAnyRule(ctx context.Context, hostPattern string, targetType model.TargetType) (bool, bool, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.HasAnyRule(ctx, hostPattern, targetType)
}

func (t *timeoutParserRuleRepo) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType model.TargetType, version int) (*model.ParserRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.FindByNaturalKey(ctx, sourceName, hostPattern, pathPattern, targetType, version)
}

func (t *timeoutParserRuleRepo) FindActiveCandidates(ctx context.Context, host string, targetType model.TargetType) ([]*model.ParserRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.FindActiveCandidates(ctx, host, targetType)
}

func (t *timeoutParserRuleRepo) List(ctx context.Context, filter model.ParserRuleFilter) ([]*model.ParserRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.List(ctx, filter)
}

func (t *timeoutParserRuleRepo) Delete(ctx context.Context, id int64) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, id)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.BlacklistRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutBlacklistRepo struct {
	inner   repository.BlacklistRepository
	timeout time.Duration
}

// WrapBlacklistWithTimeout 은 repository.BlacklistRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapBlacklistWithTimeout(r repository.BlacklistRepository, d time.Duration) repository.BlacklistRepository {
	return &timeoutBlacklistRepo{inner: r, timeout: d}
}

func (t *timeoutBlacklistRepo) Insert(ctx context.Context, r *model.BlacklistRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Insert(ctx, r)
}

func (t *timeoutBlacklistRepo) Update(ctx context.Context, r *model.BlacklistRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Update(ctx, r)
}

func (t *timeoutBlacklistRepo) Delete(ctx context.Context, id int64) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, id)
}

func (t *timeoutBlacklistRepo) GetByID(ctx context.Context, id int64) (*model.BlacklistRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByID(ctx, id)
}

func (t *timeoutBlacklistRepo) FindEnabledByHost(ctx context.Context, host string) ([]*model.BlacklistRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.FindEnabledByHost(ctx, host)
}

func (t *timeoutBlacklistRepo) List(ctx context.Context, filter model.BlacklistFilter) ([]*model.BlacklistRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.List(ctx, filter)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.SampleURLRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutSampleURLRepo struct {
	inner   repository.SampleURLRepository
	timeout time.Duration
}

// WrapSampleURLWithTimeout 은 repository.SampleURLRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapSampleURLWithTimeout(r repository.SampleURLRepository, d time.Duration) repository.SampleURLRepository {
	return &timeoutSampleURLRepo{inner: r, timeout: d}
}

func (t *timeoutSampleURLRepo) Insert(ctx context.Context, ruleID int64, url string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Insert(ctx, ruleID, url)
}

func (t *timeoutSampleURLRepo) Count(ctx context.Context, ruleID int64) (int, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Count(ctx, ruleID)
}

func (t *timeoutSampleURLRepo) List(ctx context.Context, ruleID int64, limit int) ([]*model.SampleURL, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.List(ctx, ruleID, limit)
}

func (t *timeoutSampleURLRepo) Purge(ctx context.Context, ruleID int64) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Purge(ctx, ruleID)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.RawContentRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutRawContentRepo struct {
	inner   repository.RawContentRepository
	timeout time.Duration
}

// WrapRawContentWithTimeout 은 repository.RawContentRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapRawContentWithTimeout(r repository.RawContentRepository, d time.Duration) repository.RawContentRepository {
	return &timeoutRawContentRepo{inner: r, timeout: d}
}

func (t *timeoutRawContentRepo) Save(ctx context.Context, raw *core.RawContent) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Save(ctx, raw)
}

func (t *timeoutRawContentRepo) GetByID(ctx context.Context, id string) (*core.RawContent, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByID(ctx, id)
}

func (t *timeoutRawContentRepo) GetByURL(ctx context.Context, url string) (*core.RawContent, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByURL(ctx, url)
}

func (t *timeoutRawContentRepo) List(ctx context.Context, filter model.RawContentFilter) ([]*core.RawContent, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.List(ctx, filter)
}

func (t *timeoutRawContentRepo) Delete(ctx context.Context, id string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, id)
}

func (t *timeoutRawContentRepo) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.DeleteBefore(ctx, before)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.SchedulerEntryRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutSchedulerEntryRepo struct {
	inner   repository.SchedulerEntryRepository
	timeout time.Duration
}

// WrapSchedulerEntryWithTimeout 은 repository.SchedulerEntryRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapSchedulerEntryWithTimeout(r repository.SchedulerEntryRepository, d time.Duration) repository.SchedulerEntryRepository {
	return &timeoutSchedulerEntryRepo{inner: r, timeout: d}
}

func (t *timeoutSchedulerEntryRepo) ListEnabled(ctx context.Context, category model.SchedulerCategory) ([]*model.SchedulerEntryRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.ListEnabled(ctx, category)
}

func (t *timeoutSchedulerEntryRepo) Insert(ctx context.Context, rec *model.SchedulerEntryRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Insert(ctx, rec)
}

func (t *timeoutSchedulerEntryRepo) Update(ctx context.Context, rec *model.SchedulerEntryRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Update(ctx, rec)
}

func (t *timeoutSchedulerEntryRepo) Delete(ctx context.Context, id int64) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, id)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.SearchKeywordRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutSearchKeywordRepo struct {
	inner   repository.SearchKeywordRepository
	timeout time.Duration
}

// WrapSearchKeywordWithTimeout 은 repository.SearchKeywordRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapSearchKeywordWithTimeout(r repository.SearchKeywordRepository, d time.Duration) repository.SearchKeywordRepository {
	return &timeoutSearchKeywordRepo{inner: r, timeout: d}
}

func (t *timeoutSearchKeywordRepo) ListEnabled(ctx context.Context, language, region string) ([]*model.SearchKeywordRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.ListEnabled(ctx, language, region)
}

func (t *timeoutSearchKeywordRepo) Insert(ctx context.Context, rec *model.SearchKeywordRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Insert(ctx, rec)
}

func (t *timeoutSearchKeywordRepo) Update(ctx context.Context, rec *model.SearchKeywordRecord) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Update(ctx, rec)
}

func (t *timeoutSearchKeywordRepo) Delete(ctx context.Context, id int64) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, id)
}

func (t *timeoutSearchKeywordRepo) MarkSearched(ctx context.Context, id int64, ts time.Time) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.MarkSearched(ctx, id, ts)
}

// ─────────────────────────────────────────────────────────────────────────────
// repository.FetcherRuleRepository
// ─────────────────────────────────────────────────────────────────────────────

type timeoutFetcherRuleRepo struct {
	inner   repository.FetcherRuleRepository
	timeout time.Duration
}

// WrapFetcherRuleWithTimeout 은 repository.FetcherRuleRepository 의 모든 메서드 진입에 timeout 을 적용합니다.
func WrapFetcherRuleWithTimeout(r repository.FetcherRuleRepository, d time.Duration) repository.FetcherRuleRepository {
	return &timeoutFetcherRuleRepo{inner: r, timeout: d}
}

func (t *timeoutFetcherRuleRepo) Upsert(ctx context.Context, host string, fetcher model.FetcherKind, reason string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Upsert(ctx, host, fetcher, reason)
}

func (t *timeoutFetcherRuleRepo) GetByHost(ctx context.Context, host string) (*model.FetcherRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.GetByHost(ctx, host)
}

func (t *timeoutFetcherRuleRepo) List(ctx context.Context) ([]*model.FetcherRuleRecord, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.List(ctx)
}

func (t *timeoutFetcherRuleRepo) Delete(ctx context.Context, host string) error {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.Delete(ctx, host)
}

func (t *timeoutFetcherRuleRepo) BulkDowngradeAutoUpgraded(ctx context.Context) ([]string, error) {
	ctx, cancel := withTimeout(ctx, t.timeout)
	defer cancel()
	return t.inner.BulkDowngradeAutoUpgraded(ctx)
}
