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
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
	bl := &fakeAutoDemoteRepo{}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err)
	// page 자체는 정상 반환 — 호출의 결과는 변경되지 않음
	assert.Equal(t, "정치", page.Title)
	// async demote goroutine 완료 대기 (gemini PR #479 피드백으로 비동기 처리)
	p.WaitAutoDemote()

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
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
	bl := &fakeAutoDemoteRepo{}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/article/1", articleHTML))
	require.NoError(t, err)
	p.WaitAutoDemote() // 비동기 demote 가 spawn 되지 않았어야 함을 확정 — wait 자체 즉시 반환

	assert.Empty(t, bl.inserts, "정상 article 에서 Insert 호출되면 안 됨")
}

// TestParser_ParsePage_AutoDemote_DisabledByDefault 는 옵션을 주지 않으면 Insert 가
// 호출되지 않는지 (기존 동작 유지) 검증합니다.
func TestParser_ParsePage_AutoDemote_DisabledByDefault(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
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
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
	bl := &fakeAutoDemoteRepo{insertErr: storage.ErrDuplicate}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err, "Insert ErrDuplicate 는 ParsePage 에러로 전파되면 안 됨")
	assert.Equal(t, "정치", page.Title)
	p.WaitAutoDemote() // goroutine leak 방지
}

// TestParser_ParsePage_AutoDemote_InsertFailureNonFatal 는 Insert 가 일반 에러 반환 시
// ParsePage 가 page 결과를 정상 반환 (강등은 실패하지만 본 요청은 영향 X) 하는지 검증합니다.
func TestParser_ParsePage_AutoDemote_InsertFailureNonFatal(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
	bl := &fakeAutoDemoteRepo{insertErr: errors.New("db connection lost")}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err, "Insert 일반 에러도 ParsePage 결과에 영향 없어야 함 (non-fatal)")
	assert.Equal(t, "정치", page.Title)
	p.WaitAutoDemote() // goroutine leak 방지
}

// TestParser_ParsePage_AutoDemote_NilLoggerOption 은 logger 가 nil 이면 옵션 자체가 noop
// 인지 검증합니다 (방어 — main wiring 사고 회피).
func TestParser_ParsePage_AutoDemote_NilLoggerOption(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
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
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(nil, nil, log))
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err, "repo nil → 옵션 noop → ParsePage 정상 동작")
}

// TestNewParser_NilOptionGuarded 는 ParserOption 슬라이스에 nil 이 섞여도 panic 없이
// skip 되는지 검증합니다 (coderabbit PR #479 피드백 — 호출자 wiring 사고 방어).
func TestNewParser_NilOptionGuarded(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{pageRule()}}
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)

	// nil 옵션이 섞인 슬라이스 — 호출자가 조건부로 옵션을 append 할 때 잘못된 nil entry 가 들어갈 수 있음
	p, err := rule.NewParser(res, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, p)

	// 정상 동작 확인 — nil 만 있는 옵션이라 autoDemoter 도 미설정
	_, err = p.ParsePage(context.Background(),
		makeRaw("https://news.example.com/article/1", articleHTML))
	require.NoError(t, err)
}

// TestParser_ParsePage_AutoDemote_LowercasesHost 는 대문자 host URL 의 path pattern
// 등록 시 host_pattern 이 lower-case 로 정규화되는지 검증합니다 (coderabbit PR #479 피드백).
//
// rule 의 HostPattern 은 lowercase 로 저장되어 있고 (DB 컨벤션 + repo 자체가 ToLower),
// 운영 환경에서는 fetcher 가 URL host 도 normalize 후 라우팅하지만, 본 helper 가 직접
// URL.Host 를 사용하므로 helper 단에서 lowercase 보장이 필요. 본 테스트는 helper 의
// pathPatternFromURL 가 host 를 lowercase 처리하는지를 화이트박스 검증.
func TestParser_ParsePage_AutoDemote_LowercasesHost(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{indexOnlyPageRule()}}
	res, errRes := rule.NewResolver(repo)
	require.NoError(t, errRes)
	bl := &fakeAutoDemoteRepo{}
	log := logger.New(logger.DefaultConfig())

	p, err := rule.NewParser(res, rule.WithBlacklistAutoDemote(bl, nil, log))
	require.NoError(t, err)

	// URL host 에 대문자 — resolver 는 lowercase 라우팅, helper 는 URL.Host (대문자 그대로) 를
	// 받아서 BlacklistRecord 등록 → pathPatternFromURL 의 ToLower 가 동작해야 lower-case 로 저장.
	_, err = p.ParsePage(context.Background(),
		makeRaw("https://News.Example.COM/breakingnews/politics", indexOnlyHTML))
	require.NoError(t, err)
	p.WaitAutoDemote()

	require.Len(t, bl.inserts, 1)
	assert.Equal(t, "news.example.com", bl.inserts[0].HostPattern, "host 는 lower-case 로 정규화돼야 함")
}
