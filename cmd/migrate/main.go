package main

import (
  "context"
  "os"

  "ecoscrapper/migrations"
  "ecoscrapper/pkg/config"
  pgStorage "ecoscrapper/internal/storage/postgres"
  "ecoscrapper/pkg/logger"
)

func main() {
  logConfig := logger.DefaultConfig()
  logConfig.Level = logger.LevelInfo
  logConfig.Pretty = true
  log := logger.New(logConfig)

  dbCfg, err := config.Load()
  if err != nil {
    log.WithError(err).Fatal("failed to load config")
    os.Exit(1)
  }

  log.WithFields(map[string]interface{}{
    "host":     dbCfg.Host,
    "port":     dbCfg.Port,
    "database": dbCfg.Database,
  }).Info("connecting to database")

  ctx := context.Background()

  pool, err := pgStorage.NewPool(ctx, dbCfg, log)
  if err != nil {
    log.WithError(err).Fatal("failed to connect to database")
    os.Exit(1)
  }
  defer pool.Close()

  if err := migrations.Run(ctx, pool, log); err != nil {
    log.WithError(err).Fatal("migration failed")
    os.Exit(1)
  }

  log.Info("migrations completed successfully")
}
