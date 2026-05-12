package worker_test

// helpers_test.go — 테스트 공통 헬퍼

import (
	"issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// newMinimalWorker 는 RequeueForLLMRetry 테스트에만 필요한 최소 ParserWorker 를 생성합니다.
// nil 허용 필드는 전부 nil 로 전달합니다.
func newMinimalWorker(prod queue.Producer, log *logger.Logger) *worker.ParserWorker {
	return worker.NewParserWorker(
		nil,  // consumer
		prod, // producer — RequeueForLLMRetry 가 사용
		nil,  // rawSvc
		nil,  // contentSvc
		nil,  // publisher
		nil,  // parser
		nil,  // resolver
		nil,  // sampleSvc
		nil,  // gate
		nil,  // llmGen
		nil,  // failureCounter
		nil,  // rawIDTracker
		nil,  // upgrader
		0,    // emptyBodyTitleMin
		0,    // emptyBodyContentMin
		1,    // workerCount
		log,
	)
}
