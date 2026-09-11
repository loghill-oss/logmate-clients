package logmate

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Logger is the ready-to-use logger returned by Instrument. Debug, Info, Warn,
// Error, and Fatal accept an optional LogOptions value. The embedded slog
// logger remains available for advanced integrations.
type Logger struct {
	*slog.Logger
	client         *Client
	name           string
	mu             sync.Mutex
	isClosed       bool
	previousSlog   *slog.Logger
	previousWriter io.Writer
	previousFlags  int
	previousPrefix string
	standardBridge *loggerWriter
	stdoutCapture  *streamCapture
	stderrCapture  *streamCapture
	captureSystem  bool
	captureMu      sync.Mutex
}

var instrumented struct {
	sync.Mutex
	logger *Logger
}

// Instrument configures logging once per process and returns a ready logger.
// With no argument it reads LOGMATE_API_URL and LOGMATE_SENDER_NAME. The first
// call wins until the returned logger is closed.
func Instrument(configs ...Config) *Logger {
	instrumented.Lock()
	defer instrumented.Unlock()
	if instrumented.logger != nil && !instrumented.logger.closed() {
		return instrumented.logger
	}
	var config Config
	if len(configs) > 0 {
		config = configs[0]
	}
	instrumented.logger = buildLogger(config)
	instrumented.logger.installGlobals()
	instrumented.logger.installSystemCapture()
	return instrumented.logger
}

// CreateLogger is a compatibility alias for Instrument.
func CreateLogger(configs ...Config) *Logger { return Instrument(configs...) }

func buildLogger(config Config) *Logger {
	consoleWriter := config.consoleWriter
	if consoleWriter == nil {
		consoleWriter = os.Stdout
	}
	if config.onStarted == nil {
		config.onStarted = func() { reportStarted(consoleWriter) }
	}
	client, err := New(config)
	if err != nil {
		log.New(os.Stderr, "[LogMate] ", 0).Println(err)
		client = nil
	}
	level := config.Level
	if level == nil {
		level = slog.LevelDebug
	}
	handlers := make([]slog.Handler, 0, 2)
	if !config.DisableConsole {
		handlers = append(handlers, newConsoleHandler(consoleWriter, level))
	}
	if client != nil && client.Enabled() {
		handlers = append(handlers, client.Handler(&slog.HandlerOptions{Level: level, AddSource: true}))
	}
	if len(handlers) == 0 {
		handlers = append(handlers, slog.NewTextHandler(io.Discard, nil))
	}
	name := normalizeName(config.Name)
	if name == "" {
		name = "logmate"
	}
	logger := &Logger{
		Logger:        slog.New(multiHandler(handlers)),
		client:        client,
		name:          name,
		captureSystem: !config.DisableSystemCapture,
	}
	if client != nil {
		client.mu.Lock()
		client.onDisabled = logger.restoreSystemCapture
		client.mu.Unlock()
	}
	return logger
}

// Client returns the underlying delivery client for advanced operations.
func (l *Logger) Client() *Client { return l.client }

// Send queues an Entry with event and idempotency fields when needed.
func (l *Logger) Send(entry Entry) error {
	if l.client != nil && l.client.Enabled() {
		return l.client.Send(entry)
	}
	entry.Metadata = normalizeMetadata(entry.Metadata)
	if validationMessage := sanitizeEventFields(&entry); validationMessage != "" && l.client != nil {
		l.client.report(errors.New(validationMessage + " The invalid field was ignored."))
	}
	if err := validateEntry(&entry); err != nil {
		if l.client != nil {
			l.client.report(err)
		}
		return err
	}
	level := slog.LevelInfo
	switch entry.Severity {
	case SeverityTrace, SeverityDebug:
		level = slog.LevelDebug
	case SeverityWarn:
		level = slog.LevelWarn
	case SeverityError:
		level = slog.LevelError
	case SeverityFatal:
		level = levelFatal
	}
	l.Logger.Log(context.Background(), level, entry.Message)
	return nil
}

// PendingCount returns the number of records awaiting remote delivery.
func (l *Logger) PendingCount() int {
	if l.client == nil {
		return 0
	}
	return l.client.PendingCount()
}

// Flush waits until records queued by this logger have been delivered.
func (l *Logger) Flush(ctx context.Context) bool {
	return l.client == nil || !l.client.Enabled() || l.client.Flush(ctx)
}

// Close flushes pending logs and releases the background worker.
func (l *Logger) Close() error {
	l.mu.Lock()
	if l.isClosed {
		l.mu.Unlock()
		return nil
	}
	l.isClosed = true
	l.mu.Unlock()
	l.restoreSystemCapture()
	l.restoreGlobals()
	if l.client == nil {
		return nil
	}
	return l.client.Close()
}

func (l *Logger) installGlobals() {
	l.previousSlog = slog.Default()
	l.previousWriter = log.Writer()
	l.previousFlags = log.Flags()
	l.previousPrefix = log.Prefix()
	l.standardBridge = &loggerWriter{logger: l.Logger}
	slog.SetDefault(l.Logger)
	log.SetOutput(l.standardBridge)
	log.SetFlags(0)
	log.SetPrefix("")
}

func (l *Logger) restoreGlobals() {
	if slog.Default() == l.Logger && l.previousSlog != nil {
		slog.SetDefault(l.previousSlog)
	}
	if log.Writer() == l.standardBridge && l.previousWriter != nil {
		log.SetOutput(l.previousWriter)
		log.SetFlags(l.previousFlags)
		log.SetPrefix(l.previousPrefix)
	}
}

func (l *Logger) closed() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.isClosed
}

type multiHandler []slog.Handler

type loggerWriter struct{ logger *slog.Logger }

func (w *loggerWriter) Write(data []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			w.logger.Info(line, "source", "standard-log")
		}
	}
	return len(data), nil
}

func (h multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var firstError error
	for _, handler := range h {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record.Clone()); err != nil && firstError == nil {
				firstError = err
			}
		}
	}
	return firstError
}

func (h multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	result := make(multiHandler, len(h))
	for index, handler := range h {
		result[index] = handler.WithAttrs(attrs)
	}
	return result
}

func (h multiHandler) WithGroup(name string) slog.Handler {
	result := make(multiHandler, len(h))
	for index, handler := range h {
		result[index] = handler.WithGroup(name)
	}
	return result
}
