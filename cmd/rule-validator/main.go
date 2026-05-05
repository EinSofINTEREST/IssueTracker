// rule-validator 는 parsing_rules 의 특정 row 를 의미 검증하는 수동 운영 CLI 입니다 (이슈 #257).
//
// 사용법:
//
//	rule-validator -rule-id <int64> -html-file <path>
//
// 동작:
//  1. DB 에서 parsing_rule 조회 (rule-id)
//  2. html-file 에서 샘플 HTML 로드
//  3. ValidatorPool (LLM) 으로 의미 검증
//  4. 결과 출력
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"issuetracker/internal/processor/parser/rule/validator"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/pkg/config"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/chain"
	"issuetracker/pkg/llm/policy"
	_ "issuetracker/pkg/llm/providers"
	"issuetracker/pkg/logger"
)

func main() {
	ruleID := flag.Int64("rule-id", 0, "검증할 parsing_rule ID (int64)")
	htmlFile := flag.String("html-file", "", "샘플 HTML 파일 경로")
	flag.Parse()

	log := logger.New(logger.DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if *ruleID == 0 || *htmlFile == "" {
		fmt.Fprintln(os.Stderr, "사용법: rule-validator -rule-id <id> -html-file <path>")
		os.Exit(1)
	}

	// DB 연결
	dbCfg, err := config.Load()
	if err != nil {
		log.WithError(err).Fatal("failed to load db config")
	}
	pool, err := pgstore.NewPool(ctx, dbCfg, log)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to db")
	}
	defer pool.Close()

	ruleRepo := pgstore.NewParsingRuleRepository(pool, log)

	// parsing_rule 조회
	record, err := ruleRepo.GetByID(ctx, *ruleID)
	if err != nil {
		log.WithError(err).Fatal("rule not found")
	}
	fmt.Printf("Rule: id=%d host=%s type=%s enabled=%v\n",
		record.ID, record.HostPattern, record.TargetType, record.Enabled)

	// HTML 파일 로드
	htmlBytes, err := os.ReadFile(*htmlFile)
	if err != nil {
		log.WithError(err).Fatal("failed to read html file")
	}
	fmt.Printf("HTML: file=%s len=%d\n", *htmlFile, len(htmlBytes))

	// LLM provider 구성
	llmProvider := buildProvider(log)
	if llmProvider == nil {
		fmt.Fprintln(os.Stderr, "LLM provider 설정 없음 — LLM_ENABLED=true 와 API key 환경변수 확인")
		os.Exit(1)
	}

	semPool := validator.NewPool(log, validator.NewLLMValidator(llmProvider))
	res, verr := semPool.Validate(ctx, string(htmlBytes), record.Selectors, record.TargetType)
	if verr != nil {
		fmt.Printf("검증 API 오류 (best-effort): %v\n", verr)
	}

	fmt.Printf("검증 결과: valid=%v\nreason: %s\n", res.Valid, res.Reason)

	if !res.Valid {
		os.Exit(2)
	}
}

func buildProvider(log *logger.Logger) llm.Provider {
	cfg, err := config.LoadLLM()
	if err != nil || !cfg.Enabled || cfg.APIKey == "" {
		return nil
	}
	provider, err := llm.New(llm.Config{
		Provider: cfg.Provider,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Timeout:  cfg.Timeout,
	})
	if err != nil {
		return nil
	}
	pol := policy.NewFixedOrder(cfg.Provider)
	return chain.NewWithPolicy(pol, []llm.Provider{provider}, chain.WithPolicyLogger(log))
}
