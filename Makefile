.PHONY: help build test clean run-crawler run-example lint coverage \
        chrome-start chrome-stop chrome-status run-example-docker \
        run-kafka-pipeline

# 기본 변수
BINARY_DIR=bin
CRAWLER_BINARY=$(BINARY_DIR)/crawler
GO=go
GOFLAGS=-v

# Docker Chrome 변수
CHROME_IMAGE=chromedp/headless-shell
CHROME_PORT=9222
CHROME_CONTAINER=ecoscrapper-chrome

help: ## 도움말 표시
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## 모든 바이너리 빌드
	@echo "Building binaries..."
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -o $(CRAWLER_BINARY) ./cmd/crawler
	@echo "Build complete: $(CRAWLER_BINARY)"

build-all: ## 모든 실행 파일 빌드
	@echo "Building all binaries..."
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -o $(BINARY_DIR)/crawler ./cmd/crawler
	@echo "All builds complete"

run-crawler: build ## Crawler 실행
	@echo "Running crawler..."
	./$(CRAWLER_BINARY)

run-example: ## Basic example 실행 (로컬 Chrome 또는 Docker Chrome 필요)
	@echo "Running basic example..."
	$(GO) run ./examples/basic_usage.go

run-comparison: ## Crawler 구현체 비교 예제 실행
	@echo "Running crawler comparison..."
	$(GO) run ./examples/crawler_comparison.go

run-kafka-pipeline: ## Kafka 파이프라인 예제 실행 (mock, Kafka 불필요)
	@echo "Running Kafka pipeline example..."
	$(GO) run ./examples/kafka_pipeline/

chrome-start: ## Docker Chrome 컨테이너 시작 (포트 9222)
	@echo "Starting Chrome container ($(CHROME_IMAGE))..."
	@docker run -d --rm \
	  -p $(CHROME_PORT):9222 \
	  --name $(CHROME_CONTAINER) \
	  $(CHROME_IMAGE)
	@echo "Waiting for Chrome to be ready..."
	@sleep 2
	@echo "Chrome started → ws://localhost:$(CHROME_PORT)"

chrome-stop: ## Docker Chrome 컨테이너 중지
	@echo "Stopping Chrome container..."
	-@docker stop $(CHROME_CONTAINER) 2>/dev/null
	@echo "Chrome stopped"

chrome-status: ## Docker Chrome 컨테이너 상태 확인
	@docker ps --filter name=$(CHROME_CONTAINER) \
	  --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" \
	  | { read header; read line 2>/dev/null && echo "$$header" && echo "$$line" || echo "$$header\n(not running)"; }

run-example-docker: ## Docker Chrome으로 basic example 실행 (컨테이너 자동 시작/중지)
	@echo "=== Docker Chrome + Basic Example ==="
	@docker run -d --rm \
	  -p $(CHROME_PORT):9222 \
	  --name $(CHROME_CONTAINER) \
	  $(CHROME_IMAGE); \
	sleep 2; \
	$(GO) run ./examples/basic_usage.go; \
	docker stop $(CHROME_CONTAINER) 2>/dev/null || true

test: ## 모든 테스트 실행
	@echo "Running tests..."
	$(GO) test ./test/...

test-verbose: ## 상세 모드로 테스트 실행
	@echo "Running tests (verbose)..."
	$(GO) test -v ./test/...

coverage: ## 테스트 커버리지 확인
	@echo "Running tests with coverage..."
	$(GO) test -coverpkg=./internal/...,./pkg/... -coverprofile=coverage.out ./test/...
	$(GO) tool cover -func=coverage.out | tail -1
	@echo "Coverage report generated: coverage.out"
	@echo "View HTML report: make coverage-html"

coverage-html: coverage ## 커버리지 HTML 리포트 생성
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage HTML report: coverage.html"

lint: ## golangci-lint 실행
	@echo "Running linters..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	golangci-lint run

fmt: ## 코드 포맷팅
	@echo "Formatting code..."
	$(GO) fmt ./...
	@echo "Formatting complete"

clean: ## 빌드 파일 정리
	@echo "Cleaning..."
	rm -rf $(BINARY_DIR)
	rm -f coverage.out coverage.html
	@echo "Clean complete"

deps: ## 의존성 다운로드
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) mod tidy
	@echo "Dependencies updated"

.DEFAULT_GOAL := help
