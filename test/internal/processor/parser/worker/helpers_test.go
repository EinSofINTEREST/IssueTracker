package worker_test

// helpers_test.go — 테스트 공통 헬퍼

import (
	"issuetracker/internal/processor/parser/worker"
	bus "issuetracker/internal/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// newMinimalWorker 는 RequeueForLLMRetry 테스트에만 필요한 최소 ParserWorker 를 생성합니다.
// nil 허용 필드는 전부 nil 로 전달합니다.
//
// 이슈 #392 — parser_worker 가 publisher facade 의존으로 변경됨에 따라 mock producer 를
// 실제 *bus.Publisher 로 wrap (Sub 5/7 동일 패턴, newTestPublisher 와 등가).
func newMinimalWorker(prod queue.Producer, log *logger.Logger) *worker.ParserWorker {
	pub := bus.New(prod, nil, log)
	return worker.NewParserWorker(
		nil, // consumer
		pub, // pub — RequeueForLLMRetry/Forward 가 사용
		nil, // rawSvc
		nil, // contentSvc
		nil, // parser
		nil, // resolver
		nil, // sampleSvc
		nil, // gate
		nil, // llmGen
		nil, // failureCounter
		nil, // rawIDTracker
		nil, // upgrader
		0,   // emptyBodyTitleMin
		0,   // emptyBodyContentMin
		1,   // workerCount
		log,
	)
}
