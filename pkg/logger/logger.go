package logger

import (
  "context"
  "io"
  "os"
  "time"

  "github.com/rs/zerolog"
)

// Logger는 구조화된 로깅을 위한 wrapper입니다.
type Logger struct {
  logger zerolog.Logger
}

// Level은 로그 레벨을 나타냅니다.
type Level string

const (
  LevelDebug Level = "debug"
  LevelInfo  Level = "info"
  LevelWarn  Level = "warn"
  LevelError Level = "error"
  LevelFatal Level = "fatal"
)

// Config는 로거 설정을 나타냅니다.
type Config struct {
  Level      Level
  Pretty     bool   // Human-readable output (개발용)
  Output     io.Writer
  TimeFormat string
}

// DefaultConfig는 기본 로거 설정을 반환합니다.
func DefaultConfig() Config {
  return Config{
    Level:      LevelInfo,
    Pretty:     false,
    Output:     os.Stdout,
    TimeFormat: time.RFC3339,
  }
}

// New는 새로운 Logger를 생성합니다.
func New(cfg Config) *Logger {
  // Output 설정
  output := cfg.Output
  if output == nil {
    output = os.Stdout
  }

  // Pretty printing for development
  if cfg.Pretty {
    output = zerolog.ConsoleWriter{
      Out:        output,
      TimeFormat: cfg.TimeFormat,
    }
  }

  // Zerolog 설정
  zerolog.TimeFieldFormat = cfg.TimeFormat
  zlog := zerolog.New(output).With().Timestamp().Logger()

  // Level 설정
  switch cfg.Level {
  case LevelDebug:
    zlog = zlog.Level(zerolog.DebugLevel)
  case LevelInfo:
    zlog = zlog.Level(zerolog.InfoLevel)
  case LevelWarn:
    zlog = zlog.Level(zerolog.WarnLevel)
  case LevelError:
    zlog = zlog.Level(zerolog.ErrorLevel)
  case LevelFatal:
    zlog = zlog.Level(zerolog.FatalLevel)
  default:
    zlog = zlog.Level(zerolog.InfoLevel)
  }

  return &Logger{logger: zlog}
}

// WithFields는 추가 필드와 함께 새로운 Logger를 반환합니다.
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
  ctx := l.logger.With()
  for k, v := range fields {
    ctx = ctx.Interface(k, v)
  }
  return &Logger{logger: ctx.Logger()}
}

// WithField는 단일 필드와 함께 새로운 Logger를 반환합니다.
func (l *Logger) WithField(key string, value interface{}) *Logger {
  return &Logger{
    logger: l.logger.With().Interface(key, value).Logger(),
  }
}

// WithError는 에러와 함께 새로운 Logger를 반환합니다.
func (l *Logger) WithError(err error) *Logger {
  return &Logger{
    logger: l.logger.With().Err(err).Logger(),
  }
}

// Debug는 디버그 레벨 로그를 출력합니다.
// 개발 중 상세한 정보를 기록할 때 사용합니다.
func (l *Logger) Debug(msg string) {
  l.logger.Debug().Msg(msg)
}

// Debugf는 포맷팅된 디버그 레벨 로그를 출력합니다.
func (l *Logger) Debugf(format string, args ...interface{}) {
  l.logger.Debug().Msgf(format, args...)
}

// Info는 정보 레벨 로그를 출력합니다.
// 정상적인 동작 상태를 기록할 때 사용합니다.
func (l *Logger) Info(msg string) {
  l.logger.Info().Msg(msg)
}

// Infof는 포맷팅된 정보 레벨 로그를 출력합니다.
func (l *Logger) Infof(format string, args ...interface{}) {
  l.logger.Info().Msgf(format, args...)
}

// Warn은 경고 레벨 로그를 출력합니다.
// 예상치 못한 상황이지만 처리 가능한 경우 사용합니다.
func (l *Logger) Warn(msg string) {
  l.logger.Warn().Msg(msg)
}

// Warnf는 포맷팅된 경고 레벨 로그를 출력합니다.
func (l *Logger) Warnf(format string, args ...interface{}) {
  l.logger.Warn().Msgf(format, args...)
}

// Error는 에러 레벨 로그를 출력합니다.
// 작업 실패 시 사용합니다.
func (l *Logger) Error(msg string) {
  l.logger.Error().Msg(msg)
}

// Errorf는 포맷팅된 에러 레벨 로그를 출력합니다.
func (l *Logger) Errorf(format string, args ...interface{}) {
  l.logger.Error().Msgf(format, args...)
}

// Fatal은 치명적 에러 로그를 출력하고 프로그램을 종료합니다.
func (l *Logger) Fatal(msg string) {
  l.logger.Fatal().Msg(msg)
}

// Fatalf는 포맷팅된 치명적 에러 로그를 출력하고 프로그램을 종료합니다.
func (l *Logger) Fatalf(format string, args ...interface{}) {
  l.logger.Fatal().Msgf(format, args...)
}

// Context key type for logger
type contextKey string

const loggerKey contextKey = "logger"

// ToContext는 logger를 context에 저장합니다.
func (l *Logger) ToContext(ctx context.Context) context.Context {
  return context.WithValue(ctx, loggerKey, l)
}

// FromContext는 context에서 logger를 가져옵니다.
// logger가 없으면 기본 logger를 반환합니다.
func FromContext(ctx context.Context) *Logger {
  if logger, ok := ctx.Value(loggerKey).(*Logger); ok {
    return logger
  }
  return New(DefaultConfig())
}

// WithRequestID는 request ID를 포함한 새로운 Logger를 반환합니다.
// 요청 추적을 위해 사용합니다.
func (l *Logger) WithRequestID(requestID string) *Logger {
  return l.WithField("request_id", requestID)
}

// WithCrawler는 crawler 정보를 포함한 새로운 Logger를 반환합니다.
func (l *Logger) WithCrawler(crawlerName, source, country string) *Logger {
  return &Logger{
    logger: l.logger.With().
      Str("crawler", crawlerName).
      Str("source", source).
      Str("country", country).
      Logger(),
  }
}
