-- 007_seed_parsing_rules: naver / daum / yonhap / cnn 의 hardcoded selector 들을
-- parsing_rules row 로 이전 (이슈 #100 / #139 follow-up).
--
-- 본 migration 적용 후, 사이트별 parser.go (NaverParser/DaumParser/YonhapParser/CNNParser) 가
-- 제거되고 internal/crawler/parser/rule.Parser 가 단일 엔진으로 동작합니다.
--
-- 운영자 노트:
--   - 사이트가 selector 를 바꾼 경우 본 row 의 selectors JSONB 만 UPDATE 하면 된다 (재배포 X).
--   - 동일 (host_pattern, target_type) 의 새 version 추가 → 기존 enabled=false flip → 새 enabled=true 로 점진 적용.

-- ─────────────────────────────────────────────────────────────────────────────
-- naver — n.news.naver.com (기사) / news.naver.com (목록)
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('naver', 'n.news.naver.com', 'page', 1, TRUE, $${
  "title":        {"css": "#title_area span"},
  "main_content": {"css": "#dic_area p", "multi": true},
  "author":       {"css": ".media_end_head_journalist_name", "multi": true},
  "category":     {"css": "ul.Nlnb_menu_list li.is_active a span"},
  "published_at": {"css": ".media_end_head_info_datestamp_time", "attribute": "data-date-time"},
  "images":       {"css": "span.end_photo_org img", "attribute": "src", "multi": true}
}$$::jsonb, 'naver page rule (이슈 #139 마이그레이션)');

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('naver', 'news.naver.com', 'list', 1, TRUE, $${
  "link_discovery": {
    "article_url_pattern": "^https?://n\\.news\\.naver\\.com/(?:mnews/)?article/\\d+/\\d+",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/comment/", "/photo/", "/tv/"]
  }
}$$::jsonb, 'naver list rule — full-page link discovery (이슈 #139)');

-- ─────────────────────────────────────────────────────────────────────────────
-- daum — v.daum.net (기사) / news.daum.net (목록)
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('daum', 'v.daum.net', 'page', 1, TRUE, $${
  "title":        {"css": "h3.tit_view"},
  "main_content": {"css": ".article_view p", "multi": true},
  "author":       {"css": "span.txt_info"},
  "category":     {"css": ".info_cate"},
  "tags":         {"css": ".keyword_area a", "multi": true},
  "images":       {"css": ".article_view img", "attribute": "src", "multi": true}
}$$::jsonb, 'daum page rule (이슈 #139 마이그레이션)');

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('daum', 'news.daum.net', 'list', 1, TRUE, $${
  "link_discovery": {
    "article_url_pattern": "^https?://v\\.daum\\.net/v/\\d+",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/cp/", "/cafe/", "/photo/"]
  }
}$$::jsonb, 'daum list rule — full-page link discovery (이슈 #139)');

-- ─────────────────────────────────────────────────────────────────────────────
-- yonhap — www.yna.co.kr (기사 + 목록 동일 host)
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('yonhap', 'www.yna.co.kr', 'page', 1, TRUE, $${
  "title":        {"css": "h1.tit01"},
  "main_content": {"css": ".story-news.article p", "multi": true},
  "author":       {"css": "#newsWriterCarousel01 strong", "multi": true},
  "published_at": {"css": ".update-time", "attribute": "data-published-time"},
  "tags":         {"css": ".keyword-zone a", "multi": true},
  "images":       {"css": ".comp-box.photo-group img", "attribute": "src", "multi": true}
}$$::jsonb, 'yonhap page rule (이슈 #139 마이그레이션)');

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('yonhap', 'www.yna.co.kr', 'list', 1, TRUE, $${
  "link_discovery": {
    "article_url_pattern": "^https?://www\\.yna\\.co\\.kr/view/[A-Z]+\\d+",
    "same_origin_only":    true,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/photo/", "/video/"]
  }
}$$::jsonb, 'yonhap list rule — full-page link discovery (이슈 #139)');

-- ─────────────────────────────────────────────────────────────────────────────
-- cnn — edition.cnn.com (기사 + 목록 동일 host)
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('cnn', 'edition.cnn.com', 'page', 1, TRUE, $${
  "title":        {"css": "h1.headline__text"},
  "main_content": {"css": "div.article__content p", "multi": true},
  "author":       {"css": "span.byline__name", "multi": true},
  "category":     {"css": "ol.breadcrumb li:first-child a"},
  "tags":         {"css": "div.metadata__tagline a", "multi": true},
  "images":       {"css": "div.article__content img", "attribute": "src", "multi": true}
}$$::jsonb, 'cnn page rule (이슈 #139 마이그레이션)');

INSERT INTO parsing_rules (source_name, host_pattern, target_type, version, enabled, selectors, description)
VALUES
('cnn', 'edition.cnn.com', 'list', 1, TRUE, $${
  "link_discovery": {
    "article_url_pattern": "^https?://edition\\.cnn\\.com/\\d{4}/\\d{2}/\\d{2}/",
    "same_origin_only":    false,
    "max_links_per_page":  200,
    "exclude_patterns":    ["/videos/", "/gallery/", "/live-news/"]
  }
}$$::jsonb, 'cnn list rule — full-page link discovery (이슈 #139)');
