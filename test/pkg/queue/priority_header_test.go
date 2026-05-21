// queue.PriorityFromHeader 의 매핑 검증 (이슈 #524 — gemini #3278202670 DRY).
//
// 기존 Parser / Validate / Enrich worker package 의 동일 이름 함수가 본 함수로 이관됨.
// 각 worker package 의 동일 테스트는 본 파일로 통합.
package queue_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/queue"
)

func TestPriorityFromHeader_Mapping(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"high (1)", map[string]string{"priority": "1"}, 1},
		{"normal (2)", map[string]string{"priority": "2"}, 2},
		{"low (3)", map[string]string{"priority": "3"}, 3},
		{"missing key → normal", nil, 2},
		{"empty value → normal", map[string]string{"priority": ""}, 2},
		{"non-numeric → normal", map[string]string{"priority": "xx"}, 2},
		{"out of range 0 → normal", map[string]string{"priority": "0"}, 2},
		{"out of range 4 → normal", map[string]string{"priority": "4"}, 2},
		{"negative → normal", map[string]string{"priority": "-1"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queue.PriorityFromHeader(tt.headers)
			assert.Equal(t, tt.want, got)
		})
	}
}
