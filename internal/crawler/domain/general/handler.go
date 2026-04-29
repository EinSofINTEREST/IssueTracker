package general

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// Handler 는 Chain of Responsibility 체인에서 하나의 링크를 나타냅니다.
// Handle 실패 시 SetNext 로 연결된 다음 핸들러로 위임합니다.
//
// Handler is a single link in the Chain of Responsibility.
type Handler interface {
	Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error)
	SetNext(h Handler)
}

// baseHandler 는 체인 위임 메커니즘을 제공하는 공통 구조체입니다.
type baseHandler struct {
	next Handler
	log  *logger.Logger
}

func (b *baseHandler) SetNext(h Handler) { b.next = h }

func (b *baseHandler) delegateToNext(ctx context.Context, job *core.CrawlJob, reason error) (*core.RawContent, error) {
	if b.next == nil {
		return nil, fmt.Errorf("all fetch strategies exhausted for %s: %w", job.Target.URL, reason)
	}
	return b.next.Handle(ctx, job)
}

// GoQueryFetchHandler 는 goquery 정적 HTML 크롤링.
type GoQueryFetchHandler struct {
	baseHandler
	fetcher      Fetcher
	lazyKeywords []string
}

func NewGoQueryFetchHandler(fetcher Fetcher, log *logger.Logger, lazyKeywords ...string) *GoQueryFetchHandler {
	return &GoQueryFetchHandler{
		baseHandler:  baseHandler{log: log},
		fetcher:      fetcher,
		lazyKeywords: lazyKeywords,
	}
}

func (h *GoQueryFetchHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
	raw, err := h.fetcher.Fetch(ctx, job.Target)
	if err != nil {
		var crawlerErr *core.CrawlerError
		if errors.As(err, &crawlerErr) && crawlerErr.Category == core.ErrCategoryNotFound {
			h.log.WithFields(map[string]interface{}{
				"handler": "goquery",
				"url":     job.Target.URL,
			}).WithError(err).Error("resource not found, aborting chain")
			return nil, err
		}
		h.log.WithFields(map[string]interface{}{
			"handler": "goquery",
			"url":     job.Target.URL,
		}).WithError(err).Warn("goquery fetch failed, delegating to next handler")
		return h.delegateToNext(ctx, job, err)
	}

	if h.hasLazyContent(raw.HTML) {
		h.log.WithFields(map[string]interface{}{
			"handler": "goquery",
			"url":     job.Target.URL,
		}).Warn("lazy loading detected, delegating to browser handler")
		return h.delegateToNext(ctx, job, fmt.Errorf("lazy loading content detected"))
	}

	h.log.WithFields(map[string]interface{}{
		"handler": "goquery",
		"url":     job.Target.URL,
	}).Info("goquery fetch succeeded")
	return raw, nil
}

func (h *GoQueryFetchHandler) hasLazyContent(html string) bool {
	if len(h.lazyKeywords) == 0 {
		return false
	}
	lower := strings.ToLower(html)
	for _, kw := range h.lazyKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// BrowserFetchHandler 는 chromedp 헤드리스 브라우저 — 체인의 마지막 link.
type BrowserFetchHandler struct {
	baseHandler
	fetcher Fetcher
}

func NewBrowserFetchHandler(fetcher Fetcher, log *logger.Logger) *BrowserFetchHandler {
	return &BrowserFetchHandler{baseHandler: baseHandler{log: log}, fetcher: fetcher}
}

func (h *BrowserFetchHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
	raw, err := h.fetcher.Fetch(ctx, job.Target)
	if err != nil {
		fl := h.log.WithFields(map[string]interface{}{
			"handler": "browser",
			"url":     job.Target.URL,
		})
		// 셧다운 중인 ctx cancel 은 정상 흐름 — 알람 회피.
		// chromedp 의 정상 timeout(captureErr=Canceled) 와 구분하려면 ctx.Err() 를 SoT 로 사용 (errors.Is 금지).
		if ctx.Err() != nil {
			fl.WithError(err).Debug("browser fetch canceled during shutdown")
		} else {
			fl.WithError(err).Error("browser fetch failed, all strategies exhausted")
		}
		return nil, err
	}

	h.log.WithFields(map[string]interface{}{
		"handler": "browser",
		"url":     job.Target.URL,
	}).Info("browser fetch succeeded")
	return raw, nil
}

// BuildChain 은 GoQuery → Browser 순서의 체인을 조립합니다.
// nil fetcher 는 체인에서 제외 — 사이트별 지원 전략만 포함.
//
// 호출자 (registry) 는 wiring 단계에서 fetcher 를 누락한 설정 오류를 검출하기 위해
// (Handler, error) 를 받음. panic 대신 error 반환으로 전환 — production code 가 부팅
// 단계 misconfig 로 crash 하지 않도록 (Coderabbit 피드백).
func BuildChain(gq Fetcher, br Fetcher, log *logger.Logger, lazyKeywords ...string) (Handler, error) {
	var handlers []Handler
	if gq != nil {
		handlers = append(handlers, NewGoQueryFetchHandler(gq, log, lazyKeywords...))
	}
	if br != nil {
		handlers = append(handlers, NewBrowserFetchHandler(br, log))
	}
	if len(handlers) == 0 {
		return nil, fmt.Errorf("BuildChain: at least one fetcher (goquery / browser) must be non-nil")
	}
	for i := 0; i < len(handlers)-1; i++ {
		handlers[i].SetNext(handlers[i+1])
	}
	return handlers[0], nil
}
