// auto_demote.go 는 ParsePage 결과가 index-only 페이지로 판정될 때 parser_blacklist 에
// extract_links_only mode 로 자동 등록하는 helper 입니다 (이슈 #477).
//
// 본 helper 는 *Parser 의 optional 의존 — WithBlacklistAutoDemote 옵션이 주입되면
// 활성화되고, 미주입이면 ParsePage 흐름에 영향을 주지 않음 (nil-safe).
//
// 정책:
//   - URL parse 실패 → insert skip (host-wide catch-all over-reach 회피, service.HandleLLMDecision 와 동일)
//   - Insert 성공 → metric Inc + WARN 로그
//   - ErrDuplicate (이미 등록) → noop (정상 흡수)
//   - 그 외 Insert 에러 → WARN 로그 (non-fatal — ParsePage 호출은 정상 결과 그대로 반환)

package rule

import (
	"context"
	"errors"
	"net/url"
	"regexp"

	"issuetracker/internal/processor/parser/rule/indexonly"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

// AutoDemoteRegisterer 는 parser 가 의존하는 좁은 boundary — Insert 만 필요.
// service.BlacklistService / repository.BlacklistRepository 모두 본 인터페이스를 만족.
//
// 좁은 인터페이스로 의존을 명시하여 service 패키지 import 회피 + 테스트 mock 가벼움.
type AutoDemoteRegisterer interface {
	Insert(ctx context.Context, rec *model.BlacklistRecord) error
}

// autoDemoter 는 Parser 내부에 boxing 되는 자동 강등 동작. nil 이면 기능 비활성.
type autoDemoter struct {
	repo    AutoDemoteRegisterer
	metrics *AutoDemoteMetrics
	log     *logger.Logger
}

// demote 는 index-only 로 판정된 URL 을 parser_blacklist 에 등록합니다.
// 호출자는 score 를 같이 넘겨 로그 가시성 확보.
//
// ctx 는 caller (ParsePage) 의 ctx — query timeout / cancellation 전파.
// 본 함수는 ParsePage 의 호출 후반 단계에서 호출되며 결과가 ParsePage return 값에 영향 X.
func (d *autoDemoter) demote(ctx context.Context, rawURL string, score indexonly.Score) {
	if d == nil {
		return
	}
	host, pathPattern, ok := pathPatternFromURL(rawURL)
	if !ok {
		// URL parse 실패 — host-wide catch-all 회피 (service.HandleLLMDecision 와 동일 정책).
		d.log.WithFields(map[string]interface{}{"url": rawURL}).
			Warn("index-only auto-demote skipped — URL parse failed")
		return
	}

	rec := &model.BlacklistRecord{
		HostPattern: host,
		PathPattern: pathPattern,
		Reason:      "auto: index-only heuristic detected",
		Source:      model.BlacklistSourceAuto,
		Mode:        model.BlacklistModeExtractLinksOnly,
		Enabled:     true,
	}
	if err := d.repo.Insert(ctx, rec); err != nil {
		if errors.Is(err, storage.ErrDuplicate) {
			// 이미 등록 — 매칭이 활성화돼 있다는 뜻이라 정상 흡수.
			return
		}
		// non-fatal — ParsePage 자체는 page 결과를 정상 반환.
		d.log.WithFields(map[string]interface{}{
			"host": host,
			"url":  rawURL,
		}).WithError(err).Warn("index-only auto-demote insert failed (non-fatal)")
		return
	}

	d.metrics.RecordAutoDemote(host)
	d.log.WithFields(map[string]interface{}{
		"host":             host,
		"url":              rawURL,
		"path_pattern":     pathPattern,
		"body_runes":       score.BodyRunes,
		"link_ratio":       score.LinkRatio,
		"article_links":    score.ArticleLinks,
		"blacklist_id":     rec.ID,
		"blacklist_mode":   string(model.BlacklistModeExtractLinksOnly),
		"blacklist_source": string(model.BlacklistSourceAuto),
	}).Warn("index-only page auto-demoted to extract_links_only")
}

// pathPatternFromURL 은 sampleURL 의 path 를 ^/$ 로 anchor 한 정확 일치 RE2 regex 로 변환합니다.
//
// 반환:
//   - host        : URL host (lower-case)
//   - pathPattern : "^" + regexp.QuoteMeta(path) + "$" — 정확 일치
//   - ok          : URL parse 성공 여부. false 면 host-wide catch-all 회피를 위해 호출자가 skip.
//
// 빈 path 는 "/" 로 normalize → "^/$" pattern (service.pathPatternFromURL 와 동일 정책).
func pathPatternFromURL(rawURL string) (host, pathPattern string, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return u.Host, "^" + regexp.QuoteMeta(p) + "$", true
}
