package llmgen

import (
	"context"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
)

// PendingItem 은 in-flight LLM 학습이 끝난 후 재투입할 URL 의 컨텍스트입니다.
//
// Kafka 메시지 헤더 복원에 필요한 메타 (CrawlerName / TargetType / TimeoutMs / LLMRetryCount)
// 와 raw 식별자 (RawRef) 를 보유. storage.PendingQueue 는 raw bytes 만 다루므로 본 구조의
// JSON marshal/unmarshal 은 Generator 책임.
type PendingItem struct {
	RawRef        core.RawContentRef `json:"raw_ref"`
	CrawlerName   string             `json:"crawler_name"`
	LLMRetryCount int                `json:"llm_retry_count"`
	TargetType    storage.TargetType `json:"target_type"`
	// TimeoutMs 는 원본 crawl job 의 timeout — 카테고리 재투입 시 chained job timeout 보존.
	TimeoutMs int64 `json:"timeout_ms"`
}

// RequeueFunc 는 pending 대기 URL 목록을 파서 워커에 재투입하는 콜백 타입입니다.
// Kafka 발행에 실패한 항목을 반환하면 Generator 가 pending queue 에 재적재합니다.
type RequeueFunc func(ctx context.Context, items []PendingItem) (failed []PendingItem)
