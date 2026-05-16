package rule_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// fakeAutoDemoteRepo — rule.AutoDemoteRegisterer mock
// ─────────────────────────────────────────────────────────────────────────────

type fakeAutoDemoteRepo struct {
	mu        sync.Mutex
	inserts   []*model.BlacklistRecord
	insertErr error // 다음 Insert 호출에 반환할 에러 (테스트 케이스별 주입)
}

func (r *fakeAutoDemoteRepo) Insert(_ context.Context, rec *model.BlacklistRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.insertErr != nil {
		return r.insertErr
	}
	r.inserts = append(r.inserts, rec)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트 HTML — index-only 페이지 fixture
// ─────────────────────────────────────────────────────────────────────────────

const indexOnlyHTML = `<html><body>
  <h1 class="hl">정치</h1>
  <article>
    <p>정치 카테고리</p>
  </article>
  <a href="/article/1">기사 1 제목입니다 충분히 길게</a>
  <a href="/article/2">기사 2 제목입니다 충분히 길게</a>
  <a href="/article/3">기사 3 제목입니다 충분히 길게</a>
  <a href="/article/4">기사 4 제목입니다 충분히 길게</a>
  <a href="/article/5">기사 5 제목입니다 충분히 길게</a>
  <a href="/article/6">기사 6 제목입니다 충분히 길게</a>
</body></html>`

// indexOnlyPageRule 은 위 indexOnlyHTML 에 맞는 page rule 입니다.
// PublishedAt selector 부재 → 항상 zero-value → IsIndexOnly 가 NoPublishedAt=true.
func indexOnlyPageRule() *model.ParserRuleRecord {
	return &model.ParserRuleRecord{
		ID:          11,
		SourceName:  "test",
		HostPattern: "news.example.com",
		TargetType:  model.TargetTypePage,
		Version:     1,
		Enabled:     true,
		Selectors: model.SelectorMap{
			Title:       &model.FieldSelector{CSS: "h1.hl"},
			MainContent: &model.FieldSelector{CSS: "article p", Multi: true},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParsePage + auto-demote 통합 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestParser_ParsePage_AutoDemote_TriggersInsert 는 index-only HTML 입력 시
// blacklist Insert 가 정확히 1회 호출되고 record 가 올바른 mode/source 인지 검증합니다.
func TestParser_ParsePage_AutoDemote_TriggersInsert(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, _ := rule.NewResolver(repo)
	bl := &fakeAutoDemoteRepo{}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err)
	// page 자체는 정상 반환 — 호출의 결과는 변경되지 않음
	assert.Equal(t, "정치", page.Title)

	require.Len(t, bl.inserts, 1, "expected exactly 1 Insert call")
	rec := bl.inserts[0]
	assert.Equal(t, "news.example.com", rec.HostPattern)
	assert.Equal(t, "^/breakingnews/politics$", rec.PathPattern)
	assert.Equal(t, model.BlacklistSourceAuto, rec.Source)
	assert.Equal(t, model.BlacklistModeExtractLinksOnly, rec.Mode)
	assert.True(t, rec.Enabled)
	assert.Contains(t, rec.Reason, "auto: index-only")
}

// TestParser_ParsePage_AutoDemote_NotTriggeredOnArticle 는 정상 article (긴 본문 +
// PublishedAt set) 에서 Insert 가 호출되지 않는지 검증합니다.
func TestParser_ParsePage_AutoDemote_NotTriggeredOnArticle(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{pageRule()}}
	res, _ := rule.NewResolver(repo)
	bl := &fakeAutoDemoteRepo{}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/article/1", articleHTML))
	require.NoError(t, err)

	assert.Empty(t, bl.inserts, "정상 article 에서 Insert 호출되면 안 됨")
}

// TestParser_ParsePage_AutoDemote_DisabledByDefault 는 옵션을 주지 않으면 Insert 가
// 호출되지 않는지 (기존 동작 유지) 검증합니다.
func TestParser_ParsePage_AutoDemote_DisabledByDefault(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, _ := rule.NewResolver(repo)
	bl := &fakeAutoDemoteRepo{}

	p, err := rule.NewParser(res) // 옵션 미주입
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err)

	assert.Empty(t, bl.inserts, "옵션 미주입 시 autoDemoter 비활성 — Insert 호출되면 안 됨")
}

// TestParser_ParsePage_AutoDemote_DuplicateGracefullyAbsorbed 는 Insert 가 ErrDuplicate
// 반환 시 ParsePage 가 정상 동작 + 호출자 모르게 흡수되는지 검증합니다.
func TestParser_ParsePage_AutoDemote_DuplicateGracefullyAbsorbed(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, _ := rule.NewResolver(repo)
	bl := &fakeAutoDemoteRepo{insertErr: storage.ErrDuplicate}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err, "Insert ErrDuplicate 는 ParsePage 에러로 전파되면 안 됨")
	assert.Equal(t, "정치", page.Title)
}

// TestParser_ParsePage_AutoDemote_InsertFailureNonFatal 는 Insert 가 일반 에러 반환 시
// ParsePage 가 page 결과를 정상 반환 (강등은 실패하지만 본 요청은 영향 X) 하는지 검증합니다.
func TestParser_ParsePage_AutoDemote_InsertFailureNonFatal(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, _ := rule.NewResolver(repo)
	bl := &fakeAutoDemoteRepo{insertErr: errors.New("db connection lost")}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err, "Insert 일반 에러도 ParsePage 결과에 영향 없어야 함 (non-fatal)")
	assert.Equal(t, "정치", page.Title)
}

// TestParser_ParsePage_AutoDemote_NilLoggerOption 은 logger 가 nil 이면 옵션 자체가 noop
// 인지 검증합니다 (방어 — main wiring 사고 회피).
func TestParser_ParsePage_AutoDemote_NilLoggerOption(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, _ := rule.NewResolver(repo)
	bl := &fakeAutoDemoteRepo{}

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, nil))
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err)

	assert.Empty(t, bl.inserts, "log nil → 옵션 noop → Insert 호출 X")
}

// TestParser_ParsePage_AutoDemote_NilRepoOption 은 repo 가 nil 이면 옵션 자체가 noop
// 인지 검증합니다.
func TestParser_ParsePage_AutoDemote_NilRepoOption(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, _ := rule.NewResolver(repo)
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(nil, nil, log))
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err, "repo nil → 옵션 noop → ParsePage 정상 동작")
}
