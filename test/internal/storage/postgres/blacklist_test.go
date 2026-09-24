package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 blacklist repository 를 검증합니다 (이슈 #625, 부모 #616).
//
// mode 분기가 link harvest fallback 을 좌우한다 (이슈 #480):
//   - drop               : URL 자체 drop — fetch / parse / 링크추출 모두 안 함
//   - extract_links_only : fetch + ParseLinks 만, ParsePage skip
//
// extract_links_only 가 drop 으로 잘못 떨어지면 **그 안의 정상 article 링크가 통째로
// 버려진다.** 조용히 링크 수집이 줄어드는 종류다.

func newBlacklistRepo(t *testing.T) (repository.BlacklistRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateBlacklist(t, pool)
	t.Cleanup(func() { truncateBlacklist(t, pool) })
	return pgstore.NewBlacklistRepository(pool, logger.New(logger.DefaultConfig())), pool
}

func truncateBlacklist(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE parser_blacklist CASCADE")
	require.NoError(t, err)
}

func blRecord(host, path string) *model.BlacklistRecord {
	return &model.BlacklistRecord{
		HostPattern: host,
		PathPattern: path,
		Reason:      "integration test",
		Enabled:     true,
	}
}

// ── Insert 의 보정 ──────────────────────────────────────────────────────────

// TestBlacklist_Insert_DefaultsMode 는 빈 mode 가 drop 으로 보정되는지 검증합니다.
//
// migration 017 의 기존 row 후방 호환과 같은 값이어야 한다 — 코드와 DB DEFAULT 가
// 갈리면 경로에 따라 다른 정책이 적용된다.
func TestBlacklist_Insert_DefaultsMode(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	rec := blRecord("defmode.example.com", "")
	require.NoError(t, repo.Insert(ctx, rec))

	assert.Equal(t, model.BlacklistModeDrop, rec.Mode, "빈 mode → drop (rec 에도 반영)")

	stored, err := repo.GetByID(ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, model.BlacklistModeDrop, stored.Mode, "DB 값도 drop")
}

// TestBlacklist_Insert_PreservesExtractLinksOnly 는 extract_links_only 가 drop 으로
// 떨어지지 않는지 검증합니다.
//
// **이것이 본 파일의 핵심이다.** 떨어지면 그 안의 정상 article 링크가 통째로 버려진다.
func TestBlacklist_Insert_PreservesExtractLinksOnly(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	rec := blRecord("links.example.com", "/about/.*")
	rec.Mode = model.BlacklistModeExtractLinksOnly
	require.NoError(t, repo.Insert(ctx, rec))

	stored, err := repo.GetByID(ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, model.BlacklistModeExtractLinksOnly, stored.Mode,
		"extract_links_only 가 drop 으로 떨어지면 link cascade 가 끊긴다")
}

// TestBlacklist_Insert_DefaultsSource 는 빈 source 가 manual 로 보정되는지 검증합니다.
func TestBlacklist_Insert_DefaultsSource(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	rec := blRecord("defsrc.example.com", "")
	require.NoError(t, repo.Insert(ctx, rec))
	assert.Equal(t, model.BlacklistSourceManual, rec.Source)
}

// TestBlacklist_Insert_LowercasesHost 는 host 가 소문자로 정규화되는지 검증합니다.
//
// 대소문자가 갈리면 매칭이 빗나간다 — 조회 측은 보통 소문자 host 를 넘긴다.
func TestBlacklist_Insert_LowercasesHost(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	rec := blRecord("MiXeD.Example.COM", "")
	require.NoError(t, repo.Insert(ctx, rec))

	assert.Equal(t, "mixed.example.com", rec.HostPattern, "rec 에도 정규화 반영")

	got, err := repo.FindEnabledByHost(ctx, "mixed.example.com")
	require.NoError(t, err)
	assert.Len(t, got, 1, "소문자 host 로 조회되어야 한다")
}

// ── Insert 의 거부 ──────────────────────────────────────────────────────────

// TestBlacklist_Insert_Duplicate 는 같은 자연키 재INSERT 가 ErrDuplicate 인지 검증합니다.
func TestBlacklist_Insert_Duplicate(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, blRecord("dup.example.com", "/x/.*")))

	err := repo.Insert(ctx, blRecord("dup.example.com", "/x/.*"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrDuplicate), "got: %v", err)
}

// TestBlacklist_Insert_InvalidPathPattern 은 RE2 컴파일 실패가 DB 도달 전에 거부되는지
// 검증합니다.
func TestBlacklist_Insert_InvalidPathPattern(t *testing.T) {
	repo, _ := newBlacklistRepo(t)

	err := repo.Insert(context.Background(), blRecord("bad.example.com", "/article/["))
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrInvalid), "got: %v", err)
}

// TestBlacklist_CheckConstraint_RejectsUnknownMode 는 DB CHECK 가 두 값만 허용하는지
// 검증합니다.
//
// repository 를 거치면 보정되므로 raw SQL 로 직접 시도한다 — 다른 경로 (수동 SQL / 다른
// 서비스) 로 잘못된 값이 들어오는 것을 DB 가 막아야 한다.
func TestBlacklist_CheckConstraint_RejectsUnknownMode(t *testing.T) {
	_, pool := newBlacklistRepo(t)

	_, err := pool.Exec(context.Background(),
		`INSERT INTO parser_blacklist (host_pattern, path_pattern, reason, source, mode, enabled)
		 VALUES ('chk.example.com', '', 'test', 'manual', 'bogus_mode', TRUE)`)
	require.Error(t, err, "CHECK 제약이 알 수 없는 mode 를 막아야 한다")
}

// ── Update ──────────────────────────────────────────────────────────────────

// TestBlacklist_Update_DefaultsMode 는 Update 도 빈 mode 를 drop 으로 보정하는지
// 검증합니다 — Insert 와 같은 정책이어야 한다.
func TestBlacklist_Update_DefaultsMode(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	rec := blRecord("upd.example.com", "")
	rec.Mode = model.BlacklistModeExtractLinksOnly
	require.NoError(t, repo.Insert(ctx, rec))

	rec.Mode = "" // 호출자가 mode 를 비운 채 갱신
	require.NoError(t, repo.Update(ctx, rec))

	stored, err := repo.GetByID(ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, model.BlacklistModeDrop, stored.Mode)
}

// TestBlacklist_Update_ModeSwitch 는 두 mode 간 전환이 반영되는지 검증합니다.
func TestBlacklist_Update_ModeSwitch(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	rec := blRecord("switch.example.com", "")
	require.NoError(t, repo.Insert(ctx, rec))
	require.Equal(t, model.BlacklistModeDrop, rec.Mode)

	rec.Mode = model.BlacklistModeExtractLinksOnly
	require.NoError(t, repo.Update(ctx, rec))

	stored, err := repo.GetByID(ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, model.BlacklistModeExtractLinksOnly, stored.Mode)
}

// TestBlacklist_Update_NotFound 는 없는 ID 가 ErrNotFound 인지 검증합니다.
func TestBlacklist_Update_NotFound(t *testing.T) {
	repo, _ := newBlacklistRepo(t)

	rec := blRecord("ghost.example.com", "")
	rec.ID = 999999
	err := repo.Update(context.Background(), rec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrNotFound), "got: %v", err)
}

// ── FindEnabledByHost ───────────────────────────────────────────────────────

// TestBlacklist_FindEnabledByHost_ExcludesDisabled 는 비활성 row 가 제외되는지
// 검증합니다.
//
// 포함되면 운영자가 꺼 둔 차단이 계속 적용된다.
func TestBlacklist_FindEnabledByHost_ExcludesDisabled(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()
	host := "filter.example.com"

	off := blRecord(host, "/off/.*")
	off.Enabled = false
	require.NoError(t, repo.Insert(ctx, off))
	require.NoError(t, repo.Insert(ctx, blRecord(host, "/on/.*")))

	got, err := repo.FindEnabledByHost(ctx, host)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/on/.*", got[0].PathPattern)
}

// TestBlacklist_FindEnabledByHost_Empty 는 없는 host 가 에러가 아닌 빈 결과인지
// 검증합니다 — 차단 규칙이 없는 것은 정상 상태다.
func TestBlacklist_FindEnabledByHost_Empty(t *testing.T) {
	repo, _ := newBlacklistRepo(t)

	got, err := repo.FindEnabledByHost(context.Background(), "none.example.com")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ── Delete ──────────────────────────────────────────────────────────────────

// TestBlacklist_Delete_Idempotent 는 없는 ID 삭제가 에러가 아닌지 검증합니다.
func TestBlacklist_Delete_Idempotent(t *testing.T) {
	repo, _ := newBlacklistRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Delete(ctx, 999999), "없는 ID 삭제는 idempotent")

	rec := blRecord("del.example.com", "")
	require.NoError(t, repo.Insert(ctx, rec))
	require.NoError(t, repo.Delete(ctx, rec.ID))
	require.NoError(t, repo.Delete(ctx, rec.ID), "두 번째 삭제도 nil")

	_, err := repo.GetByID(ctx, rec.ID)
	assert.True(t, errors.Is(err, storage.ErrNotFound))
}
