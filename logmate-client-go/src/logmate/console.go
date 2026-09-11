package logmate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// consoleHandler mirrors the compact formatter used by the Python client and
// intentionally leaves structured fields for LogMate instead of duplicating
// them in terminal output.
type consoleHandler struct {
	writer io.Writer
	level  slog.Leveler
	mu     *sync.Mutex
}

func newConsoleHandler(writer io.Writer, level slog.Leveler) slog.Handler {
	return &consoleHandler{writer: writer, level: level, mu: &sync.Mutex{}}
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.level == nil || level >= h.level.Level()
}

func (h *consoleHandler) Handle(_ context.Context, record slog.Record) error {
	timestamp := record.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintf(
		h.writer,
		"%s [%-8s] %s\n",
		timestamp.Local().Format("2006-01-02 15:04:05"),
		consoleLevel(record.Level),
		record.Message,
	)
	return err
}

func (h *consoleHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *consoleHandler) WithGroup(_ string) slog.Handler      { return h }

func consoleLevel(level slog.Level) string {
	switch {
	case level >= levelFatal:
		return "CRITICAL"
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARNING"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}
