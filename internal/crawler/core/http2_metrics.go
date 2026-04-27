package core

import (
	"sort"
	"sync"
	"sync/atomic"
)

// HTTP2ErrorCounter 는 HTTP/2 stream/connection error 발생 횟수를 에러 유형별로
// 카운트합니다. golang.org/x/net/http2 의 Transport.CountError hook 에 연결되어
// 호출됩니다. goroutine-safe 합니다.
//
// 운영 진단 시 다음과 같이 snapshot 으로 조회합니다:
//
//	snap := core.DefaultHTTP2ErrorCounter.Snapshot()
//	for errType, count := range snap {
//	    log.Printf("%s: %d", errType, count)
//	}
//
// 본 카운터는 이슈 #71 의 'received DATA after END_STREAM' 같은 protocol error 의
// 재발 빈도 추적을 목표로 합니다. 빈도 추이가 충분히 누적되면 후속 단계
// (ForceHTTP1 옵션화, 풀 제거 hook) 도입 여부를 결정합니다.
type HTTP2ErrorCounter struct {
	// counts: errType(string) → *atomic.Uint64
	// sync.Map 사용 이유: 키 집합이 사전 미정 + 동시 increment + 쓰기 패턴이 read-mostly
	// (대부분의 카운터는 최초 1회 생성 후 atomic.Add 만 호출됨)
	counts sync.Map
}

// NewHTTP2ErrorCounter 는 새로운 카운터 인스턴스를 생성합니다.
func NewHTTP2ErrorCounter() *HTTP2ErrorCounter {
	return &HTTP2ErrorCounter{}
}

// Increment 는 errType 카운터를 원자적으로 1 증가시키고 증가 후 값을 반환합니다.
// 동일 errType 에 대해 안전하게 동시 호출 가능합니다.
//
// 빈 errType 도 키로 그대로 저장합니다 (http2 라이브러리가 빈 문자열을 보내는
// 경우는 알려져 있지 않으나 방어적으로 데이터를 잃지 않도록 함).
func (c *HTTP2ErrorCounter) Increment(errType string) uint64 {
	val, _ := c.counts.LoadOrStore(errType, new(atomic.Uint64))
	return val.(*atomic.Uint64).Add(1)
}

// Snapshot 은 현재 모든 errType 카운터의 정합성 있는 단일 시점 사본을 반환합니다.
// 반환된 map 은 호출자가 자유롭게 수정해도 카운터에 영향을 주지 않습니다.
//
// 정합성 보장: 각 키에 대해 atomic.Load 를 사용하므로 개별 카운터는 정확합니다.
// 단, 전체 map 은 단일 트랜잭션이 아니므로 Snapshot 호출 중에 새 카운터가
// 추가되거나 기존 카운터가 증가할 수 있습니다 (모니터링 용도로 충분).
func (c *HTTP2ErrorCounter) Snapshot() map[string]uint64 {
	out := make(map[string]uint64)
	c.counts.Range(func(key, value interface{}) bool {
		out[key.(string)] = value.(*atomic.Uint64).Load()
		return true
	})
	return out
}

// SortedSnapshot 은 Snapshot 결과를 errType 사전순으로 정렬한 슬라이스로 반환합니다.
// 로깅이나 진단 출력에서 결정적 순서가 필요할 때 사용합니다.
func (c *HTTP2ErrorCounter) SortedSnapshot() []HTTP2ErrorEntry {
	snap := c.Snapshot()
	out := make([]HTTP2ErrorEntry, 0, len(snap))
	for k, v := range snap {
		out = append(out, HTTP2ErrorEntry{ErrorType: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ErrorType < out[j].ErrorType
	})
	return out
}

// HTTP2ErrorEntry 는 SortedSnapshot 의 단일 항목을 나타냅니다.
type HTTP2ErrorEntry struct {
	ErrorType string
	Count     uint64
}

// DefaultHTTP2ErrorCounter 는 패키지 전역 기본 카운터입니다.
// http_client.go 의 transport hook 이 본 인스턴스에 카운트를 누적합니다.
// 운영 진단 시 core.DefaultHTTP2ErrorCounter.Snapshot() / SortedSnapshot() 으로 조회.
var DefaultHTTP2ErrorCounter = NewHTTP2ErrorCounter()
