// blacklist_handle_llm_decision_test.go 는 service.BlacklistService.HandleLLMDecision 의
// mode 분기 동작을 검증합니다 (이슈 #480).
//
// 검증 매트릭스:
//   - mode='drop'                → drop 으로 그대로 등록
//   - mode='extract_links_only'  → extract_links_only 로 등록
//   - mode='' (빈 문자열)         → drop fallback
//   - mode='unknown'             → drop fallback + WARN 로그

package storage_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/service"
)

// fakeBlacklistInsertRepo 는 BlacklistRepository 의 Insert 동작만 캡처합니다.
// 다른 메소드는 본 테스트 범위 밖이므로 noop / nil-safe 로 구현.
type fakeBlacklistInsertRepo struct {
	mu      sync.Mutex
	inserts []*model.BlacklistRecord
}

func (r *fakeBlacklistInsertRepo) Insert(_ context.Context, rec *model.BlacklistRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserts = append(r.inserts, rec)
	return nil
}
func (r *fakeBlacklistInsertRepo) Update(_ context.Context, _ *model.BlacklistRecord) error {
	return nil
}
func (r *fakeBlacklistInsertRepo) Delete(_ context.Context, _ int64) error { return nil }
func (r *fakeBlacklistInsertRepo) GetByID(_ context.Context, _ int64) (*model.BlacklistRecord, error) {
	return nil, nil
}
func (r *fakeBlacklistInsertRepo) FindEnabledByHost(_ context.Context, _ string) ([]*model.BlacklistRecord, error) {
	return nil, nil
}
func (r *fakeBlacklistInsertRepo) List(_ context.Context, _ model.BlacklistFilter) ([]*model.BlacklistRecord, error) {
	return nil, nil
}

// TestHandleLLMDecision_ModeMatrix 는 mode 인자의 4 가지 입력 (drop / extract_links_only / 빈 / unknown)
// 이 모두 올바른 BlacklistRecord.Mode 로 INSERT 되는지 검증합니다.
func TestHandleLLMDecision_ModeMatrix(t *testing.T) {
	cases := []struct {
		name      string
		inputMode model.BlacklistMode
		wantMode  model.BlacklistMode
	}{
		{"drop 그대로", model.BlacklistModeDrop, model.BlacklistModeDrop},
		{"extract_links_only 그대로", model.BlacklistModeExtractLinksOnly, model.BlacklistModeExtractLinksOnly},
		{"빈 문자열 → drop fallback", "", model.BlacklistModeDrop},
		{"unknown 값 → drop fallback", model.BlacklistMode("garbage"), model.BlacklistModeDrop},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeBlacklistInsertRepo{}
			svc := service.NewBlacklistService(repo, newTestLogger())

			inserted, err := svc.HandleLLMDecision(
				context.Background(),
				"news.example.com",
				"https://news.example.com/category/news",
				model.TargetTypePage,
				"카테고리 인덱스",
				tt.inputMode,
			)
			require.NoError(t, err)
			require.True(t, inserted)

			require.Len(t, repo.inserts, 1)
			assert.Equal(t, tt.wantMode, repo.inserts[0].Mode)
			assert.Equal(t, model.BlacklistSourceAuto, repo.inserts[0].Source)
			assert.Equal(t, "news.example.com", repo.inserts[0].HostPattern)
			assert.Equal(t, "^/category/news$", repo.inserts[0].PathPattern)
			assert.True(t, repo.inserts[0].Enabled)
		})
	}
}

// TestHandleLLMDecision_URLParseFailure_SkipsInsert 는 sample URL parse 실패 시 host-wide
// catch-all 회피를 위해 Insert 가 스킵되는지 검증합니다 (mode 분기와 독립적인 정책).
func TestHandleLLMDecision_URLParseFailure_SkipsInsert(t *testing.T) {
	repo := &fakeBlacklistInsertRepo{}
	svc := service.NewBlacklistService(repo, newTestLogger())

	// 잘못된 URL (scheme + host 부재) → pathPattern="" → skip
	inserted, err := svc.HandleLLMDecision(
		context.Background(),
		"news.example.com",
		":::invalid:::",
		model.TargetTypePage,
		"reason",
		model.BlacklistModeExtractLinksOnly,
	)
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Empty(t, repo.inserts, "URL parse 실패 시 Insert 호출되면 안 됨")
}
