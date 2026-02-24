// migrations 패키지는 //go:embed를 사용하여 SQL 파일을 바이너리에 포함하고
// 파일명 순서대로 실행하는 migration runner를 제공합니다.
//
// 마이그레이션은 schema_migrations 테이블로 추적되어 멱등성이 보장됩니다.
// 이미 적용된 마이그레이션은 재실행되지 않습니다.
package migrations

import (
  "context"
  "embed"
  "fmt"
  "sort"
  "strings"

  "github.com/jackc/pgx/v5/pgxpool"

  "ecoscrapper/pkg/logger"
)

//go:embed *.sql
var sqlFiles embed.FS

// Run은 모든 *.up.sql 파일을 파일명 순서대로 실행합니다.
// 이미 적용된 마이그레이션은 건너뜁니다 (멱등 실행).
func Run(ctx context.Context, pool *pgxpool.Pool, log *logger.Logger) error {
  if err := ensureTrackingTable(ctx, pool); err != nil {
    return fmt.Errorf("create tracking table: %w", err)
  }

  entries, err := sqlFiles.ReadDir(".")
  if err != nil {
    return fmt.Errorf("read migrations dir: %w", err)
  }

  // *.up.sql 파일만 수집
  upFiles := make([]string, 0, len(entries))
  for _, e := range entries {
    if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
      upFiles = append(upFiles, e.Name())
    }
  }

  // 파일명 순서 보장 (001_, 002_, ...)
  sort.Strings(upFiles)

  for _, filename := range upFiles {
    applied, err := isApplied(ctx, pool, filename)
    if err != nil {
      return fmt.Errorf("check migration %s: %w", filename, err)
    }
    if applied {
      log.WithField("migration", filename).Debug("migration already applied, skipping")
      continue
    }

    sql, err := sqlFiles.ReadFile(filename)
    if err != nil {
      return fmt.Errorf("read migration %s: %w", filename, err)
    }

    log.WithField("migration", filename).Info("applying migration")

    if _, err := pool.Exec(ctx, string(sql)); err != nil {
      return fmt.Errorf("execute migration %s: %w", filename, err)
    }

    if err := markApplied(ctx, pool, filename); err != nil {
      return fmt.Errorf("mark migration %s applied: %w", filename, err)
    }

    log.WithField("migration", filename).Info("migration applied successfully")
  }

  return nil
}

// ensureTrackingTable은 schema_migrations 추적 테이블이 없으면 생성합니다.
func ensureTrackingTable(ctx context.Context, pool *pgxpool.Pool) error {
  _, err := pool.Exec(ctx, `
    CREATE TABLE IF NOT EXISTS schema_migrations (
      filename   TEXT        PRIMARY KEY,
      applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )
  `)
  return err
}

func isApplied(ctx context.Context, pool *pgxpool.Pool, filename string) (bool, error) {
  var count int
  err := pool.QueryRow(ctx,
    `SELECT COUNT(*) FROM schema_migrations WHERE filename = $1`, filename,
  ).Scan(&count)
  return count > 0, err
}

func markApplied(ctx context.Context, pool *pgxpool.Pool, filename string) error {
  _, err := pool.Exec(ctx,
    `INSERT INTO schema_migrations(filename) VALUES($1) ON CONFLICT DO NOTHING`, filename,
  )
  return err
}

// Rollback은 모든 *.down.sql 파일을 파일명 역순으로 실행합니다.
// 배포 환경의 롤백 용도로 제공되며, dev 환경에서는 사용하지 않습니다.
func Rollback(ctx context.Context, pool *pgxpool.Pool, log *logger.Logger) error {
  entries, err := sqlFiles.ReadDir(".")
  if err != nil {
    return fmt.Errorf("read migrations dir: %w", err)
  }

  // *.down.sql 파일만 수집
  downFiles := make([]string, 0, len(entries))
  for _, e := range entries {
    if !e.IsDir() && strings.HasSuffix(e.Name(), ".down.sql") {
      downFiles = append(downFiles, e.Name())
    }
  }

  // 역순 실행 (최신 마이그레이션부터 롤백)
  sort.Sort(sort.Reverse(sort.StringSlice(downFiles)))

  for _, filename := range downFiles {
    sql, err := sqlFiles.ReadFile(filename)
    if err != nil {
      return fmt.Errorf("read rollback %s: %w", filename, err)
    }

    log.WithField("migration", filename).Info("rolling back migration")

    if _, err := pool.Exec(ctx, string(sql)); err != nil {
      return fmt.Errorf("execute rollback %s: %w", filename, err)
    }

    // 대응하는 up 마이그레이션 추적 레코드 제거
    upFilename := strings.TrimSuffix(filename, ".down.sql") + ".up.sql"
    if _, err := pool.Exec(ctx,
      `DELETE FROM schema_migrations WHERE filename = $1`, upFilename,
    ); err != nil {
      return fmt.Errorf("unmark migration %s: %w", upFilename, err)
    }

    log.WithField("migration", filename).Info("rollback applied successfully")
  }

  return nil
}
