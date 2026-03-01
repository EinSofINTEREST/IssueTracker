.PHONY: help build test clean run-crawler run-example lint coverage \
        chrome-start chrome-stop chrome-status run-example-docker \
        run-kafka-pipeline \
        kafka-start kafka-stop kafka-clean kafka-status kafka-logs kafka-topics \
        pg-start pg-stop pg-clean pg-migrate pg-status pg-psql

# 기본 변수
BINARY_DIR=bin
PG_DATA_DIR=/data/ELArchive/issuetracker/postgres
PG_ENV_FILE=.env
# .env가 없으면 기본값(localhost:5432, postgres/postgres) 사용
PG_ENV_ARGS=$(shell [ -f $(PG_ENV_FILE) ] && echo "--env-file $(PG_ENV_FILE)")
CRAWLER_BINARY=$(BINARY_DIR)/crawler
GO=go
GOFLAGS=-v

# Docker Chrome 변수
CHROME_IMAGE=chromedp/headless-shell
CHROME_PORT=9222
CHROME_CONTAINER=issuetracker-chrome

# Kafka Docker Compose 변수
COMPOSE_FILE=deployments/docker/docker-compose.yml
COMPOSE=docker compose
KAFKA_DATA_DIR=/data/ELArchive/issuetracker/kafka
KAFKA_ENV_FILE=deployments/docker/.env
# .env가 없으면 .env.example 기본값 사용
KAFKA_ENV_ARGS=$(shell [ -f $(KAFKA_ENV_FILE) ] && echo "--env-file $(KAFKA_ENV_FILE)")

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

## ─── Kafka ───────────────────────────────────────────────────

kafka-start: ## Kafka 브로커 + UI 시작, 토픽 초기화 (localhost:9092 / UI:8080)
	@echo "Starting Kafka..."
	@mkdir -p $(KAFKA_DATA_DIR)
	@chmod 777 $(KAFKA_DATA_DIR)
	$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) up -d kafka kafka-ui
	@echo "Waiting for Kafka to be healthy..."
	@$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) run --rm kafka-init
	@echo ""
	@echo "  Kafka  → localhost:9092"
	@echo "  UI     → http://localhost:8080"

kafka-stop: ## Kafka 중지 (볼륨 유지)
	@echo "Stopping Kafka..."
	$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) down
	@echo "Kafka stopped (data preserved)"

kafka-clean: ## Kafka 중지 + 볼륨 삭제 (데이터 초기화)
	@echo "Stopping Kafka and removing volumes..."
	$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) down -v
	@echo "Kafka stopped and data removed"

kafka-status: ## Kafka 컨테이너 상태 확인
	@$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) ps

kafka-logs: ## Kafka 브로커 로그 스트리밍
	@$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) logs -f kafka

kafka-topics: ## 생성된 Kafka 토픽 목록 출력
	@$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) exec kafka \
	  /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:29092 --list

kafka-describe: ## 토픽별 파티션 수·리더 상세 출력
	@$(COMPOSE) -f $(COMPOSE_FILE) $(KAFKA_ENV_ARGS) exec kafka \
	  /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:29092 --describe

# 파티션 증설: make kafka-scale-partitions TOPIC=issuetracker.crawl.normal PARTITIONS=16
kafka-scale-partitions: ## 토픽 파티션 증설 (TOPIC, PARTITIONS 필수 / 감소 불가)
	@if [ -z "$(TOPIC)" ] || [ -z "$(PARTITIONS)" ]; then \
	  echo "Usage: make kafka-scale-partitions TOPIC=<topic> PARTITIONS=<count>"; exit 1; fi
	@$(COMPOSE) -f $(COMPOSE_FILE) exec kafka \
	  /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:29092 \
	  --alter --topic $(TOPIC) --partitions $(PARTITIONS)
	@echo "Scaled $(TOPIC) to $(PARTITIONS) partitions"

## ─── PostgreSQL ──────────────────────────────────────────────

pg-start: ## PostgreSQL 컨테이너 시작 (localhost:5432)
	@echo "Starting PostgreSQL..."
	@mkdir -p $(PG_DATA_DIR)
	$(COMPOSE) -f $(COMPOSE_FILE) $(PG_ENV_ARGS) up -d postgres
	@echo "Waiting for PostgreSQL to be healthy..."
	@until $(COMPOSE) -f $(COMPOSE_FILE) exec -T postgres \
	  pg_isready -U $${POSTGRES_USER:-postgres} -d $${POSTGRES_DB:-issuetracker} > /dev/null 2>&1; do \
	  sleep 1; \
	done
	@echo ""
	@echo "  PostgreSQL → localhost:5432"

pg-stop: ## PostgreSQL 중지 (볼륨 유지)
	@echo "Stopping PostgreSQL..."
	$(COMPOSE) -f $(COMPOSE_FILE) $(PG_ENV_ARGS) stop postgres
	@echo "PostgreSQL stopped (data preserved)"

pg-clean: ## PostgreSQL 중지 + 볼륨 데이터 삭제
	@echo "Stopping PostgreSQL and removing data..."
	$(COMPOSE) -f $(COMPOSE_FILE) $(PG_ENV_ARGS) rm -sf postgres
	rm -rf $(PG_DATA_DIR)
	@echo "PostgreSQL stopped and data removed"

pg-migrate: ## DB 마이그레이션 실행 (schema_migrations 기준 멱등)
	@echo "Running database migrations..."
	$(GO) run ./cmd/migrate/...
	@echo "Migrations complete"

pg-migrate-down: ## DB 마이그레이션 롤백 실행 (배포 환경용, dev에서는 사용 지양)
	@echo "Rolling back database migrations..."
	$(GO) run ./cmd/migrate-down/...
	@echo "Rollback complete"

pg-status: ## PostgreSQL 컨테이너 상태 확인
	@$(COMPOSE) -f $(COMPOSE_FILE) $(PG_ENV_ARGS) ps postgres

pg-psql: ## PostgreSQL psql 접속 (docker exec)
	@$(COMPOSE) -f $(COMPOSE_FILE) $(PG_ENV_ARGS) exec postgres \
	  psql -U $${POSTGRES_USER:-postgres} -d $${POSTGRES_DB:-issuetracker}

## ─────────────────────────────────────────────────────────────

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
	rm -f crawler basic_usage crawler_comparison kafka_pipeline
	@echo "Clean complete"

deps: ## 의존성 다운로드
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) mod tidy
	@echo "Dependencies updated"

.DEFAULT_GOAL := help
