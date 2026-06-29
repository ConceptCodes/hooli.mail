package logger

import (
	"bytes"
	"log"
	"log/slog"
	"os"
)

type (
	Level   = slog.Level
	Leveler = slog.Leveler
	Attr    = slog.Attr
	Logger  = slog.Logger
	Handler = slog.Handler
)

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

func Init(level Level) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))
}

func Default() *slog.Logger {
	return slog.Default()
}

func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}

func Debug(msg string, args ...any) {
	slog.Debug(msg, args...)
}

func With(args ...any) *slog.Logger {
	return slog.With(args...)
}

func Any(key string, value any) slog.Attr {
	return slog.Any(key, value)
}

func String(key, value string) slog.Attr {
	return slog.String(key, value)
}

func Int(key string, value int) slog.Attr {
	return slog.Int(key, value)
}

func Int64(key string, value int64) slog.Attr {
	return slog.Int64(key, value)
}

func Bridge() *log.Logger {
	return log.New(&slogWriter{}, "", 0)
}

type slogWriter struct{}

func (w *slogWriter) Write(p []byte) (int, error) {
	slog.Warn(string(bytes.TrimSpace(p)))
	return len(p), nil
}
