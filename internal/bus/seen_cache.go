// seen_cache.go 는 "이미 파이프라인에 있는 URL" 의 단기 기억입니다 (이슈 #507).
//
// category 페이지는 10~30분 주기로 다시 fetch 되고, 그때마다 거의 같은 article 링크 목록을
// 내놓습니다. publisher 는 링크마다 Redis SETNX 로 진입 marker 를 시도하는데 대부분 이미
// 잡혀 있어 그대로 버려집니다 — 라이브 51분에 10,940건이었습니다.
//
// 본 캐시는 "직전에 이미 잡혀 있던" URL 을 기억해 **Redis 왕복 자체를 생략** 합니다.
//
// # 왜 negative 만 캐시하는가
//
// 획득 **성공** 은 캐시하지 않습니다. 성공을 기억하면 marker 가 만료된 뒤에도 "이미 발행함"
// 으로 오판해 재수집이 영영 막힙니다. 반대로 실패(이미 잡힘)는 marker 가 살아 있다는 뜻이라,
// 캐시 수명이 marker TTL 보다 짧기만 하면 판단이 뒤집히지 않습니다.
//
// # 왜 Article 에만 쓰는가
//
// marker TTL 이 target type 별로 다릅니다 — Article 24h, **Category 60s** (정상 흐름은 cycle
// 종료 시 명시적 release). Category 를 캐시하면 marker 가 풀린 뒤에도 캐시 수명 동안 발행이
// 막혀 10~30분 fetch 주기가 깨집니다. 호출자가 Article 경로에서만 캐시를 넘깁니다.
//
// # 정확도
//
// 엄밀한 LRU 가 아니라 **삽입 순서 기반(FIFO) 축출** 입니다. 접근이 잦은 항목을 우대하지
// 않지만, 이 워크로드는 "최근 cycle 에 본 URL" 집합이라 삽입 순서가 곧 최신성입니다.
// 이름을 lru 로 두지 않은 이유입니다.

package bus

import (
	"sync"
	"time"
)

// DefaultSeenCacheSize 는 기억할 URL 수의 기본 상한입니다.
//
// 라이브 관측의 호스트별 중복 상위 8개 합이 약 9.4k — 한 cycle 의 hot set 를 덮는 크기.
const DefaultSeenCacheSize = 10000

// DefaultSeenCacheTTL 은 항목의 기본 수명입니다.
//
// Article marker TTL (24h) 보다 **훨씬 짧아야** 합니다. 그래야 marker 가 만료된 URL 을
// 캐시가 계속 막는 일이 없습니다.
const DefaultSeenCacheTTL = 10 * time.Minute

// seenCache 는 bounded + TTL 기반 URL 집합입니다. goroutine-safe.
//
// 외부 LRU 라이브러리를 들이지 않고 표준 라이브러리만 씁니다 — 의존성 추가는 그만한
// 이득이 있을 때만 합니다.
type seenCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxSize int

	entries map[string]time.Time
	// order 는 삽입 순서입니다. 상한 초과 시 앞에서부터 버립니다.
	order []string

	// now 는 테스트가 시간을 제어하기 위한 seam 입니다. nil 이면 time.Now.
	now func() time.Time
}

// newSeenCache 는 seenCache 를 생성합니다. ttl 이 0 이하면 nil 을 반환 — 비활성 의미.
//
// nil seenCache 의 메소드는 모두 noop 이라 호출자가 nil 검사 없이 쓸 수 있습니다.
func newSeenCache(size int, ttl time.Duration) *seenCache {
	if ttl <= 0 {
		return nil
	}
	if size <= 0 {
		size = DefaultSeenCacheSize
	}
	return &seenCache{
		ttl:     ttl,
		maxSize: size,
		entries: make(map[string]time.Time, size),
		order:   make([]string, 0, size),
	}
}

func (c *seenCache) nowFn() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Has 는 url 이 아직 유효한 기간 안에 기억돼 있는지 반환합니다.
//
// 만료된 항목은 조회 시점에 제거합니다 — 별도 청소 goroutine 을 두지 않습니다.
func (c *seenCache) Has(url string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	at, ok := c.entries[url]
	if !ok {
		return false
	}
	if c.nowFn().Sub(at) >= c.ttl {
		delete(c.entries, url)
		return false
	}
	return true
}

// Add 는 url 을 기억합니다. 상한 초과 시 가장 오래 전에 넣은 항목부터 버립니다.
func (c *seenCache) Add(url string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[url]; !exists {
		c.order = append(c.order, url)
	}
	c.entries[url] = c.nowFn()

	// 상한 초과분 축출. order 에는 이미 삭제된 키가 남아 있을 수 있어 map 기준으로 센다.
	for len(c.entries) > c.maxSize && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	// order 가 map 보다 지나치게 커지면 (만료 삭제 누적) 압축한다 — 메모리 누수 방지.
	if len(c.order) > 2*c.maxSize {
		compacted := make([]string, 0, len(c.entries))
		for _, k := range c.order {
			if _, ok := c.entries[k]; ok {
				compacted = append(compacted, k)
			}
		}
		c.order = compacted
	}
}

// Len 은 현재 기억 중인 항목 수입니다 (만료분 포함 — 조회 시 정리됨). 테스트/관측용.
func (c *seenCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
