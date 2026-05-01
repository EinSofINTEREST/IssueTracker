package chromedp

import (
	"context"
	"time"

	"issuetracker/internal/processor/fetcher/core"
)

// ChromedpCrawler: 헤드리스 브라우저 기반 동적 크롤러
// JavaScript 렌더링이 필요한 SPA, 커뮤니티 등 동적 페이지 크롤링에 사용
type ChromedpCrawler struct {
	name       string
	sourceInfo core.SourceInfo
	config     core.Config

	// 브라우저 컨텍스트
	allocCtx    context.Context
	allocCancel context.CancelFunc

	// 옵션
	opts ChromedpOptions
}

// ChromedpOptions: chromedp 크롤러 추가 옵션
type ChromedpOptions struct {
	// Headless 모드 (기본값: true)
	Headless bool

	// 페이지 로드 후 추가 대기 시간 (JS 렌더링 완료 대기)
	WaitStable bool

	// 특정 CSS selector가 나타날 때까지 대기
	WaitSelector string

	// 스크린샷 캡처 여부
	CaptureScreenshot bool

	// 네트워크 유휴 상태까지 대기
	WaitNetworkIdle bool

	// User-Agent 오버라이드 (빈 문자열이면 config 사용)
	UserAgent string

	// 뷰포트 크기
	ViewportWidth  int64
	ViewportHeight int64

	// Docker/원격 Chrome 연결 설정
	// UseRemote=true 이면 로컬 Chrome 실행 대신 원격 Chrome에 연결
	UseRemote bool
	// RemoteURL: 원격 Chrome WebSocket 주소 (기본값: ws://localhost:9222)
	// Docker 실행 예시: docker run -d -p 9222:9222 chromedp/headless-shell
	RemoteURL string

	// GracefulCaptureTimeout: navigation timeout 발생 후 별도 context 로 OuterHTML 을
	// 재캡처할 때 허용하는 최대 시간 (이슈 #146).
	// 0 또는 음수면 DefaultGracefulCaptureTimeout (10s) 사용.
	// 부하 상태의 CDP 응답 + page.StopLoading() 후 OuterHTML 회수까지 시간이 필요해
	// 기존 3s 는 너무 짧았음. 운영 튜닝 가능하도록 옵션화.
	GracefulCaptureTimeout time.Duration
}

// DefaultGracefulCaptureTimeout: GracefulCaptureTimeout 미지정 시 기본값 (이슈 #146).
const DefaultGracefulCaptureTimeout = 10 * time.Second

// DefaultOptions: 로컬 Chrome 실행 기본 옵션
func DefaultOptions() ChromedpOptions {
	return ChromedpOptions{
		Headless:               true,
		WaitStable:             true,
		WaitSelector:           "",
		WaitNetworkIdle:        false,
		ViewportWidth:          1920,
		ViewportHeight:         1080,
		UseRemote:              false,
		GracefulCaptureTimeout: DefaultGracefulCaptureTimeout,
	}
}

// DefaultRemoteOptions: Docker Chrome 연결 기본 옵션
// chromedp/headless-shell 컨테이너를 ws://localhost:9222 에서 연결
func DefaultRemoteOptions() ChromedpOptions {
	return ChromedpOptions{
		Headless:               true,
		WaitStable:             true,
		WaitSelector:           "",
		WaitNetworkIdle:        false,
		ViewportWidth:          1920,
		ViewportHeight:         1080,
		UseRemote:              true,
		RemoteURL:              "ws://localhost:9222",
		GracefulCaptureTimeout: DefaultGracefulCaptureTimeout,
	}
}
