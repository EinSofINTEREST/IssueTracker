// Package prompt 는 LLM 프롬프트를 외부 파일에서 로드하는 공통 인프라를 제공합니다.
//
// 의도: prompt 가 LLM 응답 품질의 가장 큰 lever 라 운영자가 빈번히 튜닝하는데, Go const 로
// hardcode 되어 있으면 한 줄 수정도 코드 변경 → PR → 재빌드 → 재배포 cycle 을 강제. 외부
// 파일 + 환경변수 (LLM_PROMPT_DIR) 로 분리하여 파일 수정 + 프로세스 재기동만으로 갱신 가능.
//
// 4개 호출자 (llmgen / pathinfer / validator / claudegen) 가 동일 Loader 인터페이스를 공유해
// 중복 코드 회피.
package prompt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Loader 는 prompt name (예: "llmgen/system") 으로 prompt 본문을 반환하는 인터페이스입니다.
//
// name 은 디렉토리 separator 로 sub-package 와 prompt 파일을 구분 — FileLoader 는 자동으로
// ".txt" suffix 를 붙여 디스크에서 로드합니다 (예: "llmgen/system" → "<dir>/llmgen/system.txt").
type Loader interface {
	Load(name string) (string, error)
}

// FileLoader 는 외부 디렉토리의 .txt 파일을 첫 호출 시 lazy load 후 in-memory cache 합니다.
//
// 동시 호출 안전 — sync.RWMutex 로 보호. cache miss 시 file read + 캐싱.
// 프로세스 재기동 전까지 cache 유지 — 운영자가 파일 수정 후 reload 원하면 재기동 필요.
type FileLoader struct {
	dir   string
	mu    sync.RWMutex
	cache map[string]string
}

// NewFileLoader 는 dir 디렉토리를 root 로 하는 FileLoader 를 생성합니다.
//
// dir 이 비어있거나 디렉토리가 아니면 error — 운영 deployment 누락을 fail-fast 로 가시화.
func NewFileLoader(dir string) (*FileLoader, error) {
	if dir == "" {
		return nil, errors.New("prompt: NewFileLoader requires non-empty directory path")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("prompt: stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("prompt: %q is not a directory", dir)
	}
	return &FileLoader{
		dir:   dir,
		cache: make(map[string]string),
	}, nil
}

// Load 는 name 에 해당하는 prompt 본문을 반환합니다.
//
// name 은 sub-package 와 prompt 식별자를 "/" 로 구분 (예: "llmgen/system", "pathinfer/user").
// 파일 경로는 "<dir>/<name>.txt" 로 해석되며 파일 부재 / read 실패 시 error.
// cache hit 시 disk I/O 없이 즉시 반환.
func (l *FileLoader) Load(name string) (string, error) {
	if name == "" {
		return "", errors.New("prompt: Load requires non-empty name")
	}

	l.mu.RLock()
	if v, ok := l.cache[name]; ok {
		l.mu.RUnlock()
		return v, nil
	}
	l.mu.RUnlock()

	path := filepath.Join(l.dir, name+".txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("prompt: read %q: %w", path, err)
	}
	body := string(data)
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("prompt: %q is empty", path)
	}

	l.mu.Lock()
	l.cache[name] = body
	l.mu.Unlock()
	return body, nil
}

// MapLoader 는 테스트 / dev 환경용 in-memory Loader 입니다.
//
// key 는 Load 의 name 인자와 동일 (예: "llmgen/system"). value 는 prompt 본문.
// 부재 key 는 error 반환.
type MapLoader map[string]string

// Load 는 m 에서 name 에 해당하는 prompt 를 반환합니다.
func (m MapLoader) Load(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("prompt: %q not in MapLoader", name)
	}
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("prompt: %q is empty in MapLoader", name)
	}
	return v, nil
}

// Render 는 template 의 placeholder 를 replacements 로 치환한 결과를 반환합니다.
//
// replacements 는 ("{{KEY}}", value) 의 짝수 슬라이스 — strings.NewReplacer 와 동일 인자 형식.
// template 안에 정의되지 않은 placeholder 는 그대로 잔존 — 호출자가 필요한 경우 별도 검증.
//
// 홀수 인자 시 panic — strings.NewReplacer 의 panic 보다 본 패키지 컨텍스트가 명확하도록
// 우선 검증 후 호출자 실수를 즉시 가시화.
//
// Go text/template 의 의존성 회피 — 운영자가 Go template 문법 몰라도 단순 {{KEY}} 치환만 가능.
func Render(template string, replacements ...string) string {
	if len(replacements) == 0 {
		return template
	}
	if len(replacements)%2 != 0 {
		panic(fmt.Sprintf("prompt: Render requires even number of replacements (got %d): %v", len(replacements), replacements))
	}
	return strings.NewReplacer(replacements...).Replace(template)
}
