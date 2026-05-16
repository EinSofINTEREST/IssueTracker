package llmgen_test

import (
	"context"
	"net/url"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
)

// fakeEnrichedExtractor 는 ExtractEnriched 결과를 프로그래매틱하게 제어하는 stub.
// SelectorExtractor 와 EnrichedExtractor 인터페이스 모두 구현 — Generator 의 type assertion 분기 검증.
type fakeEnrichedExtractor struct {
	mu     sync.Mutex
	calls  int
	result *llmgen.ExtractResult
	err    error
}

func (e *fakeEnrichedExtractor) Extract(ctx context.Context, host string, t model.TargetType, html string) (model.SelectorMap, error) {
	res, err := e.ExtractEnriched(ctx, host, t, html)
	if err != nil {
		return model.SelectorMap{}, err
	}
	if res.Blacklist != nil {
		// legacy 인터페이스 호환 — Generator 는 EnrichedExtractor 분기를 우선하므로 본 경로는 실제 미사용.
		return model.SelectorMap{}, nil
	}
	return res.Selectors, nil
}

func (e *fakeEnrichedExtractor) ExtractEnriched(_ context.Context, _ string, _ model.TargetType, _ string) (*llmgen.ExtractResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.result, e.err
}

func (e *fakeEnrichedExtractor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// recordingBlacklistRepo 는 HandleLLMDecision 호출을 기록하는 BlacklistAutoRegister stub (이슈 #431).
//
// 이전 (#326) 에는 Generator 가 BlacklistRepository 를 직접 호출했으나, #431 에서 service
// boundary 로 의존성 역전 — 본 stub 은 service interface 만 만족하면 됨.
type recordingBlacklistRepo struct {
	mu       sync.Mutex
	inserts  []*model.BlacklistRecord
	insertOK bool // false 면 (false, ErrDuplicate) 반환 (멱등성 검증)
}

// HandleLLMDecision 은 BlacklistAutoRegister 인터페이스를 만족합니다 (llmgen.Generator 가 의존).
// 본 stub 은 실제 service 와 같은 path_pattern 변환 로직을 직접 수행 — 기존 테스트의 검증 부분
// (rec.PathPattern Contains "/promo/123") 이 그대로 통과되도록.
func (r *recordingBlacklistRepo) HandleLLMDecision(_ context.Context, host, sampleURL string, _ model.TargetType, reason string, mode model.BlacklistMode) (bool, error) {
	u, err := url.Parse(sampleURL)
	if err != nil {
		return false, nil
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	// 이슈 #480 — mode 인자 보정 (빈 문자열은 drop 으로 fallback, 실제 service 와 동일 정책).
	if mode != model.BlacklistModeDrop && mode != model.BlacklistModeExtractLinksOnly {
		mode = model.BlacklistModeDrop
	}
	rec := &model.BlacklistRecord{
		HostPattern: host,
		PathPattern: "^" + regexp.QuoteMeta(path) + "$",
		Reason:      reason,
		Source:      model.BlacklistSourceAuto,
		Mode:        mode,
		Enabled:     true,
	}
	r.mu.Lock()
	r.inserts = append(r.inserts, rec)
	r.mu.Unlock()
	if !r.insertOK {
		return false, storage.ErrDuplicate
	}
	return true, nil
}

func (r *recordingBlacklistRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inserts)
}

// TestGenerator_EnrichedExtractor_BlacklistBranch 는 EnrichedExtractor 가 페이지를 blacklist 로
// 판정하면:
//  1. 셀렉터 INSERT 가 skip 되어야 함
//  2. blacklistRepo.Insert 가 정확히 1회 호출되어야 함 (host_pattern + path regex + reason)
func TestGenerator_EnrichedExtractor_BlacklistBranch(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeEnrichedExtractor{
		result: &llmgen.ExtractResult{
			Blacklist: &llmgen.BlacklistDecision{Reason: "광고 페이지"},
		},
	}
	g.SetExtractor(extractor)

	blRepo := &recordingBlacklistRepo{insertOK: true}
	g.SetBlacklistService(blRepo)

	g.Enqueue(context.Background(), "ads.example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://ads.example.com/promo/123", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	// 셀렉터 INSERT skip 검증.
	assert.Empty(t, repo.inserts(), "blacklist 분기 시 ParserRuleRepository.Insert 발생 X")
	assert.Equal(t, 1, extractor.callCount(), "ExtractEnriched 1회 호출")

	// Blacklist Insert 1회 검증 + 페이로드 확인.
	require.Equal(t, 1, blRepo.callCount(), "BlacklistRepository.Insert 정확히 1회")
	rec := blRepo.inserts[0]
	assert.Equal(t, "ads.example.com", rec.HostPattern)
	assert.Equal(t, model.BlacklistSourceAuto, rec.Source)
	assert.Equal(t, model.BlacklistModeDrop, rec.Mode)
	assert.True(t, rec.Enabled)
	assert.Equal(t, "광고 페이지", rec.Reason)
	// path_pattern 은 ^/promo/123$ 형태로 escape + anchor.
	assert.Contains(t, rec.PathPattern, "/promo/123")
}

// TestGenerator_EnrichedExtractor_Ok_PageTypeStored 는 validity=ok 분기에서 PageType 이
// 정상으로 ParserRuleRecord 에 저장되는지 검증합니다.
func TestGenerator_EnrichedExtractor_Ok_PageTypeStored(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeEnrichedExtractor{
		result: &llmgen.ExtractResult{
			Selectors: model.SelectorMap{
				Title:       &model.FieldSelector{CSS: "h1.article-title"},
				MainContent: &model.FieldSelector{CSS: "article p", Multi: true},
			},
			PageType:           llmgen.PageTypeNews,
			PageTypeConfidence: 0.95,
		},
	}
	g.SetExtractor(extractor)

	g.Enqueue(context.Background(), "example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	recs := repo.inserts()
	require.Len(t, recs, 1)
	assert.Equal(t, "news", recs[0].PageType, "ExtractResult.PageType 가 ParserRuleRecord.PageType 에 저장")
}

// TestGenerator_EnrichedExtractor_BlacklistRepoNil_StillSkipsInsert 는 blacklistRepo 가
// 미설정 (nil) 일 때도 셀렉터 INSERT 만 skip 되고 panic / 에러 없이 graceful 진행하는지 검증.
func TestGenerator_EnrichedExtractor_BlacklistRepoNil_StillSkipsInsert(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeEnrichedExtractor{
		result: &llmgen.ExtractResult{
			Blacklist: &llmgen.BlacklistDecision{Reason: "로그인 요구"},
		},
	}
	g.SetExtractor(extractor)
	// SetBlacklistService 미호출 — nil 상태.

	g.Enqueue(context.Background(), "loginwall.example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://loginwall.example.com/secure", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	assert.Empty(t, repo.inserts(), "blacklist 분기에서 셀렉터 INSERT skip")
}
