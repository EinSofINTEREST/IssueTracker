package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/agent"
)

// TestNormalizeBackend 는 `*_AGENT_BACKEND` 값 해석을 고정합니다 (이슈 #534).
//
// 이 함수가 main wiring 에서 분리된 이유: backend 선택은 오타 / 대소문자 / 공백 같은
// 운영자 입력에 직접 노출되는 지점인데, main 패키지 안에 있으면 테스트할 수 없습니다.
func TestNormalizeBackend(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		want           agent.Backend
		wantRecognized bool
	}{
		{
			name:           "미지정은 기본 backend, 오류 아님",
			input:          "",
			want:           agent.BackendClaude,
			wantRecognized: true,
		},
		{
			name:           "공백만 있는 값도 미지정과 동일",
			input:          "   ",
			want:           agent.BackendClaude,
			wantRecognized: true,
		},
		{
			name:           "claude",
			input:          "claude",
			want:           agent.BackendClaude,
			wantRecognized: true,
		},
		{
			name:           "codex",
			input:          "codex",
			want:           agent.BackendCodex,
			wantRecognized: true,
		},
		{
			name:           "대문자 허용",
			input:          "CODEX",
			want:           agent.BackendCodex,
			wantRecognized: true,
		},
		{
			name:           "앞뒤 공백 허용",
			input:          "  codex  ",
			want:           agent.BackendCodex,
			wantRecognized: true,
		},
		{
			name:           "오타는 기본값으로 떨어지되 인식 실패를 알린다",
			input:          "codexx",
			want:           agent.BackendClaude,
			wantRecognized: false,
		},
		{
			name:           "다른 backend 이름도 인식 실패",
			input:          "gemini",
			want:           agent.BackendClaude,
			wantRecognized: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, recognized := agent.NormalizeBackend(tt.input)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantRecognized, recognized,
				"인식 여부는 호출자가 WARN 을 남길지 결정하는 근거다")
		})
	}
}

// TestDefaultBackend_IsClaude 는 기본 backend 가 claude 임을 고정합니다.
//
// 기본값이 바뀌면 `*_AGENT_BACKEND` 를 설정하지 않은 모든 운영 환경의 동작이 조용히
// 바뀌므로, 의도적 변경임을 이 테스트 수정으로 드러나게 한다.
func TestDefaultBackend_IsClaude(t *testing.T) {
	assert.Equal(t, agent.BackendClaude, agent.DefaultBackend)
}
