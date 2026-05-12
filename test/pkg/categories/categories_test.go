package categories_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/categories"
)

func TestCategory_Priority(t *testing.T) {
	tests := []struct {
		name string
		cat  categories.Category
		want core.Priority
	}{
		// High tier
		{"politics → High", categories.CategoryPolitics, core.PriorityHigh},
		{"economy → High", categories.CategoryEconomy, core.PriorityHigh},
		{"society → High", categories.CategorySociety, core.PriorityHigh},
		{"current_affairs → High", categories.CategoryCurrentAffairs, core.PriorityHigh},
		{"breaking_news → High", categories.CategoryBreakingNews, core.PriorityHigh},

		// Normal tier
		{"sports → Normal", categories.CategorySports, core.PriorityNormal},
		{"culture → Normal", categories.CategoryCulture, core.PriorityNormal},
		{"tech → Normal", categories.CategoryTech, core.PriorityNormal},
		{"business → Normal", categories.CategoryBusiness, core.PriorityNormal},
		{"entertainment → Normal", categories.CategoryEntertainment, core.PriorityNormal},
		{"lifestyle → Normal", categories.CategoryLifestyle, core.PriorityNormal},
		{"international → Normal", categories.CategoryInternational, core.PriorityNormal},
		{"health → Normal", categories.CategoryHealth, core.PriorityNormal},
		{"climate → Normal", categories.CategoryClimate, core.PriorityNormal},
		{"column → Normal", categories.CategoryColumn, core.PriorityNormal},
		{"community → Normal", categories.CategoryCommunity, core.PriorityNormal},

		// Unknown / 미등록 → Low
		{"unknown empty → Low", categories.CategoryUnknown, core.PriorityLow},
		{"미등록 hint → Low", categories.Category("foobar"), core.PriorityLow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cat.Priority())
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
