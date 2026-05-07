package rule

import (
	"context"
	"fmt"

	"issuetracker/internal/storage"
)

// seededHostTargets 는 IssueTracker 가 부팅 시 parsing_rules 테이블에 활성 row 로 존재해야 하는
// (host, target_type) 페어 목록입니다. migration 007 이 등록하는 seed rules 와 1:1 대응.
//
// 신규 사이트 추가 시: migration 또는 별도 seed 입력 후 본 슬라이스에 항목 추가.
var seededHostTargets = []struct {
	Host       string
	TargetType storage.TargetType
}{
	{"n.news.naver.com", storage.TargetTypePage},
	{"news.naver.com", storage.TargetTypeList},
	{"v.daum.net", storage.TargetTypePage},
	{"news.daum.net", storage.TargetTypeList},
	{"www.yna.co.kr", storage.TargetTypePage},
	{"www.yna.co.kr", storage.TargetTypeList},
	{"edition.cnn.com", storage.TargetTypePage},
	{"edition.cnn.com", storage.TargetTypeList},
}

// VerifySeeded 는 seededHostTargets 의 모든 (host, target_type) 페어가 parsing_rules 테이블에
// 활성 row 로 존재하는지 확인합니다.
//
// 부재 시 ErrNoRule 등 진단 에러를 그대로 반환 — 호출자가 Fatal 로 부팅 차단.
// migration 007 이 적용되어야 통과 (또는 운영자가 동등한 row 를 직접 입력).
func VerifySeeded(ctx context.Context, resolver *Resolver) error {
	for _, r := range seededHostTargets {
		// "/" path 로 catch-all (path_pattern='') 매칭 검증 — seed 된 host-only rule 확인.
		if _, err := resolver.Resolve(ctx, r.Host, "/", r.TargetType); err != nil {
			return fmt.Errorf("missing rule for (%s, %s): %w", r.Host, r.TargetType, err)
		}
	}
	return nil
}
