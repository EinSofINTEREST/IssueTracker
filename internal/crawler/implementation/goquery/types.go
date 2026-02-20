package goquery

import (
  "net/http"

  "ecoscrapper/internal/crawler/core"
)

// GoqueryCrawler: goquery 라이브러리 기반 크롤러
// goquery를 사용하여 HTML 파싱과 크롤링을 동시에 처리
type GoqueryCrawler struct {
  name       string
  sourceInfo core.SourceInfo
  config     core.Config
  httpClient *http.Client
}
