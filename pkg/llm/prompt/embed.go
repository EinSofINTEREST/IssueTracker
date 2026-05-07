package prompt

import (
	"embed"
	"errors"
	"fmt"
	"path"
	"strings"
)

// embeddedAssets 는 binary 빌드 시 assets/ 디렉토리 전체를 함께 컴파일한 read-only FS 입니다.
//
// 외부 파일 부재 / 권한 오류 / 운영자 실수 (마운트 누락 등) 로 FileLoader 가 실패해도
// EmbedLoader 가 fallback 으로 항상 prompt 를 제공 — 부팅 자체가 막히는 가용성 사고 방지.
//
//go:embed all:assets
var embeddedAssets embed.FS

// embedRoot 는 embed.FS 안에서 prompt 파일이 시작되는 path prefix.
// `//go:embed all:assets` 로 인해 모든 entry 가 "assets/" 로 시작.
const embedRoot = "assets"

// EmbedLoader 는 binary 에 내장된 prompt 파일을 반환하는 Loader 입니다.
//
// 외부 디스크 의존성 없음 — Go 빌드 시점에 assets/ 디렉토리가 binary 에 함께 컴파일.
// FileLoader 의 fallback 으로 ChainLoader 와 함께 사용하거나 단독 사용 (production 에서
// prompt 외부 튜닝이 필요 없는 경우) 가능.
type EmbedLoader struct{}

// NewEmbedLoader 는 EmbedLoader 인스턴스를 반환합니다.
//
// 별도 상태가 없어 매번 같은 인스턴스 반환해도 무방하지만, 호출자 일관성 (FileLoader 와 동일
// 생성자 패턴) 을 위해 함수로 노출.
func NewEmbedLoader() *EmbedLoader {
	return &EmbedLoader{}
}

// Load 는 name (예: "llmgen/system") 에 해당하는 embedded prompt 본문을 반환합니다.
//
// FileLoader.Load 와 동일 시맨틱 — name 비어있으면 error, 파일 부재 시 error,
// 빈 파일은 error (운영 실수 가시화).
func (l *EmbedLoader) Load(name string) (string, error) {
	if name == "" {
		return "", errors.New("prompt: EmbedLoader.Load requires non-empty name")
	}
	// embed.FS 경로는 항상 슬래시 — path.Join 으로 중복 슬래시 등 정규화.
	p := path.Join(embedRoot, name+".txt")
	data, err := embeddedAssets.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("prompt: embedded read %q: %w", p, err)
	}
	body := string(data)
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("prompt: embedded %q is empty", p)
	}
	return body, nil
}

// ChainLoader 는 여러 Loader 를 순서대로 시도하는 fallback chain 입니다.
//
// 1번 Loader 가 성공하면 즉시 반환, 실패 시 2번으로 넘어감. 모든 Loader 실패 시 첫 에러 반환
// (가장 구체적인 fail 지점이 보통 1번 — 운영자 디버깅에 유용).
//
// 일반 wiring: NewChainLoader(fileLoader, embedLoader) — 외부 파일 우선, embed 안전망.
type ChainLoader struct {
	loaders []Loader
}

// NewChainLoader 는 loaders 순서대로 시도하는 ChainLoader 를 생성합니다.
//
// 호출자 안전성: 항상 비-nil 포인터 반환. 빈 loaders 도 허용되며, Load 호출 시 명시적
// 에러 반환 (silent ("",nil) 회피). 이전 nil 반환 정책은 Loader 인터페이스에 담길 때
// nil 수신자 패닉을 유발할 수 있어 폐기.
func NewChainLoader(loaders ...Loader) *ChainLoader {
	return &ChainLoader{loaders: loaders}
}

// Load 는 chain 의 각 Loader 를 순서대로 시도합니다.
//
// 첫 번째 성공 결과를 즉시 반환. 모든 Loader 실패 시 첫 번째 (1번 chain) 의 에러 반환 —
// 운영자가 의도한 우선순위의 실패가 가장 진단에 유용 (embed fallback 은 거의 항상 성공).
//
// nil 수신자 또는 빈 chain 은 명시적 에러 — silent 성공 회피.
func (c *ChainLoader) Load(name string) (string, error) {
	if c == nil || len(c.loaders) == 0 {
		return "", errors.New("prompt: no loaders available in chain")
	}
	var firstErr error
	for _, l := range c.loaders {
		body, err := l.Load(name)
		if err == nil {
			return body, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", firstErr
}
