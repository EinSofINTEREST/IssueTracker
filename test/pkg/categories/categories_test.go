package categories_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/categories"
)

func TestCategory_Tier(t *testing.T) {
	tests := []struct {
		name string
		cat  categories.Category
		want categories.Tier
	}{
		// High tier
		{"politics → High", categories.CategoryPolitics, categories.TierHigh},
		{"economy → High", categories.CategoryEconomy, categories.TierHigh},
		{"society → High", categories.CategorySociety, categories.TierHigh},
		{"current_affairs → High", categories.CategoryCurrentAffairs, categories.TierHigh},
		{"breaking_news → High", categories.CategoryBreakingNews, categories.TierHigh},

		// Normal tier
		{"sports → Normal", categories.CategorySports, categories.TierNormal},
		{"culture → Normal", categories.CategoryCulture, categories.TierNormal},
		{"tech → Normal", categories.CategoryTech, categories.TierNormal},
		{"business → Normal", categories.CategoryBusiness, categories.TierNormal},
		{"entertainment → Normal", categories.CategoryEntertainment, categories.TierNormal},
		{"lifestyle → Normal", categories.CategoryLifestyle, categories.TierNormal},
		{"international → Normal", categories.CategoryInternational, categories.TierNormal},
		{"health → Normal", categories.CategoryHealth, categories.TierNormal},
		{"climate → Normal", categories.CategoryClimate, categories.TierNormal},
		{"column → Normal", categories.CategoryColumn, categories.TierNormal},
		{"community → Normal", categories.CategoryCommunity, categories.TierNormal},

		// Unknown / 미등록 → Low
		{"unknown empty → Low", categories.CategoryUnknown, categories.TierLow},
		{"미등록 hint → Low", categories.Category("foobar"), categories.TierLow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cat.Tier())
		})
	}
}

func TestCategory_IsKnown(t *testing.T) {
	tests := []struct {
		name string
		cat  categories.Category
		want bool
	}{
		{"politics → true", categories.CategoryPolitics, true},
		{"sports → true", categories.CategorySports, true},
		{"community → true", categories.CategoryCommunity, true},
		{"empty → false", categories.CategoryUnknown, false},
		{"미등록 → false", categories.Category("foobar"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cat.IsKnown())
		})
	}
}

// TestMetadataKey 는 표준 키가 상수로 노출되어 다운스트림이 동일 키로 read/write 함을 검증.
func TestMetadataKey(t *testing.T) {
	assert.Equal(t, "category", categories.MetadataKey)
}
