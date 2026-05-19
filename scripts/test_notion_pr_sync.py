"""Unit tests for notion_pr_sync.py (이슈 #519).

`_normalize_link_url` 및 `pr_to_body_blocks` 의 link context 전파를 검증.
chrome / Notion API 외부 의존성 없는 순수 로직만 테스트.

실행: `cd scripts && python -m pytest test_notion_pr_sync.py -v`
"""
from __future__ import annotations

import os
import sys

import pytest

# 같은 디렉토리의 notion_pr_sync 를 import
sys.path.insert(0, os.path.dirname(__file__))
import notion_pr_sync as nps  # noqa: E402


# ─────────────────────────────────────────────────────────────────────────────
# _normalize_link_url
# ─────────────────────────────────────────────────────────────────────────────


@pytest.fixture(autouse=True)
def _reset_context():
    """매 테스트 전후 link context 초기화 — module-level state 격리."""
    nps._clear_link_context()
    yield
    nps._clear_link_context()


def test_normalize_absolute_https_passthrough():
    assert nps._normalize_link_url("https://example.com/x") == "https://example.com/x"


def test_normalize_absolute_http_passthrough():
    assert nps._normalize_link_url("http://example.com") == "http://example.com"


def test_normalize_mailto_passthrough():
    assert nps._normalize_link_url("mailto:test@example.com") == "mailto:test@example.com"


def test_normalize_tel_passthrough():
    assert nps._normalize_link_url("tel:+1-555-1234") == "tel:+1-555-1234"


def test_normalize_fragment_only_passthrough():
    assert nps._normalize_link_url("#section") == "#section"


def test_normalize_empty_returns_none():
    assert nps._normalize_link_url("") is None


def test_normalize_relative_without_context_returns_none():
    # context 미설정 — relative path 는 link 제거 (caller 가 plain text fallback)
    nps._clear_link_context()
    assert nps._normalize_link_url("internal/foo.go") is None


def test_normalize_relative_with_context_returns_blob_url():
    nps._set_link_context("https://github.com/owner/repo", "abc123")
    got = nps._normalize_link_url("internal/foo.go")
    assert got == "https://github.com/owner/repo/blob/abc123/internal/foo.go"


def test_normalize_relative_dot_prefix_stripped():
    nps._set_link_context("https://github.com/owner/repo", "main")
    got = nps._normalize_link_url("./internal/foo.go")
    assert got == "https://github.com/owner/repo/blob/main/internal/foo.go"


def test_normalize_relative_absolute_slash_stripped():
    nps._set_link_context("https://github.com/owner/repo", "main")
    got = nps._normalize_link_url("/internal/foo.go")
    assert got == "https://github.com/owner/repo/blob/main/internal/foo.go"


def test_normalize_relative_only_slashes_returns_none():
    # "./" 또는 "/" 단독은 정리 후 빈 문자열 → None (link 제거)
    nps._set_link_context("https://github.com/owner/repo", "main")
    assert nps._normalize_link_url("./") is None
    assert nps._normalize_link_url("/") is None


def test_normalize_relative_dotfile_prefix_preserved():
    # `.github/`, `.gitignore` 같은 dotfile prefix 가 깨지면 안 됨 (gemini PR #520).
    # lstrip("./") 가 character set 으로 동작했던 버그 검증.
    nps._set_link_context("https://github.com/owner/repo", "main")
    assert nps._normalize_link_url(".github/workflows/ci.yml") == (
        "https://github.com/owner/repo/blob/main/.github/workflows/ci.yml"
    )
    assert nps._normalize_link_url(".gitignore") == (
        "https://github.com/owner/repo/blob/main/.gitignore"
    )


def test_normalize_relative_parent_path_preserved():
    # `../../foo` 같은 상위 경로도 의도치 않게 축소되지 않아야 함.
    # GitHub blob URL 에 그대로 들어가도 OK — 깨진 link 가 되더라도 PR body 원본 의도 보존.
    nps._set_link_context("https://github.com/owner/repo", "main")
    assert nps._normalize_link_url("../sibling/file") == (
        "https://github.com/owner/repo/blob/main/../sibling/file"
    )


def test_normalize_data_url_passthrough():
    # data: URL 도 absolute scheme 으로 통과 (Notion 이 받아들이는지는 별개 — 본 함수는 거부 안 함)
    url = "data:image/png;base64,iVBORw0KGgo="
    assert nps._normalize_link_url(url) == url


# ─────────────────────────────────────────────────────────────────────────────
# md_inline + link context 통합
# ─────────────────────────────────────────────────────────────────────────────


def test_md_inline_absolute_link_preserved():
    spans = nps.md_inline("[GitHub](https://github.com)")
    # 첫 span 이 [text](link) — link 보존
    assert len(spans) == 1
    assert spans[0]["text"]["link"] == {"url": "https://github.com"}


def test_md_inline_relative_link_without_context_becomes_plain_text():
    nps._clear_link_context()
    spans = nps.md_inline("[graceful_timeout.go](internal/foo.go)")
    assert len(spans) == 1
    # link 제거 + plain text 만 잔존
    assert spans[0]["text"]["link"] is None
    assert spans[0]["text"]["content"] == "graceful_timeout.go"


def test_md_inline_relative_link_with_context_becomes_blob_url():
    nps._set_link_context("https://github.com/owner/repo", "sha123")
    spans = nps.md_inline("[graceful_timeout.go](internal/foo.go)")
    assert spans[0]["text"]["link"] == {
        "url": "https://github.com/owner/repo/blob/sha123/internal/foo.go"
    }


def test_md_inline_mixed_spans_relative_and_absolute():
    nps._set_link_context("https://github.com/owner/repo", "main")
    spans = nps.md_inline(
        "see [code](internal/foo.go) and [docs](https://example.com/doc)"
    )
    # spans 순서: "see " (plain) / "code" (link blob URL) / " and " (plain) / "docs" (link absolute) / trailing
    link_spans = [s for s in spans if s["text"]["link"] is not None]
    assert len(link_spans) == 2
    assert link_spans[0]["text"]["link"]["url"] == "https://github.com/owner/repo/blob/main/internal/foo.go"
    assert link_spans[1]["text"]["link"]["url"] == "https://example.com/doc"


# ─────────────────────────────────────────────────────────────────────────────
# pr_to_body_blocks context set/clear
# ─────────────────────────────────────────────────────────────────────────────


def _minimal_pr(*, url="https://github.com/owner/repo/pull/123", body="", head_oid="", head_ref=""):
    return {
        "number": 123,
        "title": "Test PR",
        "url": url,
        "body": body,
        "files": [],
        "headRefOid": head_oid,
        "headRefName": head_ref,
    }


def test_pr_to_body_blocks_sets_context_from_pr_url_and_head_sha():
    pr = _minimal_pr(
        url="https://github.com/myowner/myrepo/pull/42",
        body="See [foo](internal/foo.go)",
        head_oid="deadbeef",
    )
    blocks = nps.pr_to_body_blocks(pr)
    # 본문 paragraph 블록 안에 link 가 blob URL 로 변환됐는지
    found = False
    for b in blocks:
        if b["type"] != "paragraph":
            continue
        for span in b["paragraph"]["rich_text"]:
            link = span["text"].get("link")
            if link and "/blob/deadbeef/internal/foo.go" in link.get("url", ""):
                assert link["url"] == "https://github.com/myowner/myrepo/blob/deadbeef/internal/foo.go"
                found = True
                break
    assert found, "relative link 이 blob URL 로 변환되어야 함"


def test_pr_to_body_blocks_falls_back_to_head_ref_name():
    pr = _minimal_pr(
        url="https://github.com/owner/repo/pull/9",
        body="[x](path/to/file.go)",
        head_oid="",
        head_ref="feature/branch-name",
    )
    blocks = nps.pr_to_body_blocks(pr)
    found = False
    for b in blocks:
        if b["type"] != "paragraph":
            continue
        for span in b["paragraph"]["rich_text"]:
            link = span["text"].get("link")
            if link and "/blob/feature/branch-name/path/to/file.go" in link.get("url", ""):
                found = True
                break
    assert found, "headRefOid 부재 시 headRefName 사용"


def test_pr_to_body_blocks_clears_context_after_return():
    pr = _minimal_pr(
        url="https://github.com/owner/repo/pull/1",
        body="[x](rel/path)",
        head_oid="abc",
    )
    _ = nps.pr_to_body_blocks(pr)
    # 종료 후 context 가 비어있어야 — 다음 PR 처리에 leak 없음
    assert nps._link_context["repo_url"] == ""
    assert nps._link_context["ref"] == ""


def test_pr_to_body_blocks_clears_context_on_exception():
    # md_to_blocks 가 어떤 이유로 예외 던져도 context 잔존하지 않음을 검증.
    # pr["title"] 키 부재 → KeyError 발생 시점에 context clear 확인.
    pr = {
        # title 누락 → callout 생성 시 pr['title'] 접근 가능 (default 빈 문자열)
        # 대신 url 도 부재 → pr["url"] 접근 시 KeyError
        "number": 1,
        "body": "[x](rel)",
        "files": [],
    }
    with pytest.raises(KeyError):
        nps.pr_to_body_blocks(pr)
    assert nps._link_context["repo_url"] == ""
    assert nps._link_context["ref"] == ""
