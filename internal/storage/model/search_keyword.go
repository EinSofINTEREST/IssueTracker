package model

import "time"

// SearchKeywordSource 는 search_keywords.source 컬럼의 enum.
type SearchKeywordSource string

const (
	SearchKeywordSourceManual SearchKeywordSource = "manual"
	SearchKeywordSourceAuto   SearchKeywordSource = "auto"
)

// SearchKeywordRecord 는 search_keywords 테이블의 단일 행입니다.
//
// Google CSE 기반 search exploration (이슈 #331) 의 keyword set source-of-truth.
type SearchKeywordRecord struct {
	ID             int64
	Keyword        string
	Enabled        bool
	Source         SearchKeywordSource
	Language       string
	Region         string
	Notes          string
	LastSearchedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
