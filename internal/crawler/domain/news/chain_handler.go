package news

import (
  "context"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/storage"
  "issuetracker/pkg/logger"
)

// ChainHandler는 NewsHandler 체인과 DB 저장을 결합한 handler.Handler 어댑터입니다.
// worker.KafkaConsumerPool이 요구하는 handler.Handler 인터페이스를 구현하며,
// TargetTypeArticle 처리 후 파싱 → DB 저장을 자동으로 수행합니다.
//
// ChainHandler adapts a NewsHandler chain to the handler.Handler interface
// required by worker.KafkaConsumerPool. After fetching, it parses and
// persists articles when all conditions are met (article target, HTML present,
// parser and repo non-nil).
type ChainHandler struct {
  Crawler NewsCrawler
  Chain   NewsHandler
  Parser  NewsArticleParser        // nil 허용: 파서 없으면 DB 저장 건너뜀
  Repo    storage.NewsArticleRepository // nil 허용: repo 없으면 DB 저장 건너뜀
  Log     *logger.Logger
}

// NewChainHandler는 새로운 ChainHandler를 생성합니다.
func NewChainHandler(
  crawler NewsCrawler,
  chain NewsHandler,
  parser NewsArticleParser,
  repo storage.NewsArticleRepository,
  log *logger.Logger,
) *ChainHandler {
  return &ChainHandler{
    Crawler: crawler,
    Chain:   chain,
    Parser:  parser,
    Repo:    repo,
    Log:     log,
  }
}

// Handle은 CrawlJob을 chain을 통해 처리하고, 기사 조건 충족 시 DB에 저장합니다.
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
  raw, err := h.Chain.Handle(ctx, job)
  if err != nil {
    return nil, err
  }

  h.Log.WithFields(map[string]interface{}{
    "target_type": string(job.Target.Type),
    "has_html":    raw.HTML != "",
    "html_length": len(raw.HTML),
    "has_parser":  h.Parser != nil,
    "has_repo":    h.Repo != nil,
  }).Debug("DB 저장 조건 확인")

  isArticle := job.Target.Type == core.TargetTypeArticle
  canSave := raw.HTML != "" && h.Parser != nil && h.Repo != nil

  if isArticle && canSave {
    h.saveArticle(ctx, raw)
  } else if isArticle {
    // 기사(target_type=article)인데 저장 조건을 충족하지 못한 경우만 warn으로 로깅
    h.Log.WithFields(map[string]interface{}{
      "is_article": isArticle,
      "has_html":   raw.HTML != "",
      "has_parser": h.Parser != nil,
      "has_repo":   h.Repo != nil,
    }).Warn("조건 불충족: saveArticle 건너뜀")
  } else {
    // 정상적인 non-article 요청에서의 saveArticle 미실행은 debug 수준으로만 로깅
    h.Log.WithFields(map[string]interface{}{
      "is_article": isArticle,
      "has_html":   raw.HTML != "",
      "has_parser": h.Parser != nil,
      "has_repo":   h.Repo != nil,
    }).Debug("조건 불충족(정상 흐름): non-article이므로 saveArticle 건너뜀")
  }

  return raw, nil
}

// saveArticle은 RawContent를 파싱하여 DB에 저장합니다.
// 에러는 warn 로그로만 기록하며 호출자에게 전파하지 않습니다.
func (h *ChainHandler) saveArticle(ctx context.Context, raw *core.RawContent) {
  h.Log.WithField("url", raw.URL).Debug("기사 파싱 시작")

  article, err := h.Parser.ParseArticle(raw)
  if err != nil {
    h.Log.WithError(err).Warn("기사 파싱 실패, DB 저장 건너뜀")
    return
  }

  h.Log.WithFields(map[string]interface{}{
    "title":  article.Title,
    "author": article.Author,
    "url":    article.URL,
  }).Debug("기사 파싱 성공, DB 저장 시작")

  record := ArticleToRecord(article, raw)

  if err := h.Repo.Insert(ctx, record); err != nil {
    h.Log.WithError(err).Warn("뉴스 기사 DB 저장 실패")
    return
  }

  h.Log.WithField("url", raw.URL).Debug("뉴스 기사 DB 저장 성공")
}

// ArticleToRecord는 NewsArticle과 RawContent를 storage.NewsArticleRecord로 변환합니다.
// URL은 chain이 확정한 raw.URL을 우선 사용합니다.
func ArticleToRecord(article *NewsArticle, raw *core.RawContent) *storage.NewsArticleRecord {
  record := &storage.NewsArticleRecord{
    SourceName: raw.SourceInfo.Name,
    SourceType: string(raw.SourceInfo.Type),
    Country:    raw.SourceInfo.Country,
    Language:   raw.SourceInfo.Language,
    URL:        raw.URL,
    Title:      article.Title,
    Body:       article.Body,
    Summary:    article.Summary,
    Author:     article.Author,
    Category:   article.Category,
    Tags:       article.Tags,
    ImageURLs:  article.ImageURLs,
    FetchedAt:  raw.FetchedAt,
  }

  if !article.PublishedAt.IsZero() {
    t := article.PublishedAt
    record.PublishedAt = &t
  }

  return record
}
