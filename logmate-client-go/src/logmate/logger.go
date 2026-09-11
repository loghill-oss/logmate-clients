package logmate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
)

type eventFields struct {
	event             string
	eventOccurrenceID string
}

type eventFieldsContextKey struct{}

type sourceFields struct {
	module   string
	function string
	line     int
}

type sourceFieldsContextKey struct{}

// Debug records a DEBUG log with optional metadata and event fields.
func (l *Logger) Debug(message string, options ...LogOptions) {
	l.logWithOptions(slog.LevelDebug, message, options)
}

// Info records an INFO log with optional metadata and event fields.
func (l *Logger) Info(message string, options ...LogOptions) {
	l.logWithOptions(slog.LevelInfo, message, options)
}

// Warn records a WARN log with optional metadata and event fields.
func (l *Logger) Warn(message string, options ...LogOptions) {
	l.logWithOptions(slog.LevelWarn, message, options)
}

// Error records an ERROR log with optional metadata and event fields.
func (l *Logger) Error(message string, options ...LogOptions) {
	l.logWithOptions(slog.LevelError, message, options)
}

// Fatal records a FATAL log with optional metadata and event fields. Like
// Python's logging.Logger.fatal, it does not terminate the process.
func (l *Logger) Fatal(message string, options ...LogOptions) {
	l.logWithOptions(levelFatal, message, options)
}

// RecoverPanic records an unhandled panic and then resumes it so Go preserves
// its normal crash behavior. It must be deferred in each goroutine that should
// report panics: defer logger.RecoverPanic().
func (l *Logger) RecoverPanic() {
	value := recover()
	if value == nil {
		return
	}
	l.Error(string(debug.Stack()), LogOptions{
		Metadata: map[string]any{"exception": fmt.Sprint(value)},
	})
	panic(value)
}

func (l *Logger) logWithOptions(level slog.Level, message string, options []LogOptions) {
	option := mergeLogOptions(options)
	entry := Entry{Event: option.Event, EventOccurrenceID: option.EventOccurrenceID}
	if validationMessage := sanitizeEventFields(&entry); validationMessage != "" {
		if l.client != nil {
			l.client.report(errors.New(validationMessage + " The invalid field was ignored."))
		}
		option.Event = ""
		option.EventOccurrenceID = ""
	}
	option.Metadata = normalizeMetadata(option.Metadata)
	ctx := context.Background()
	if source, ok := applicationCaller(); ok {
		ctx = context.WithValue(ctx, sourceFieldsContextKey{}, source)
	}
	if option.Event != "" || option.EventOccurrenceID != "" {
		ctx = context.WithValue(ctx, eventFieldsContextKey{}, eventFields{
			event:             option.Event,
			eventOccurrenceID: option.EventOccurrenceID,
		})
	}
	l.Logger.LogAttrs(ctx, level, message, metadataAttrs(option.Metadata)...)
}

func applicationCaller() (sourceFields, bool) {
	programCounter, file, line, ok := runtime.Caller(3)
	if !ok {
		return sourceFields{}, false
	}
	function := ""
	if details := runtime.FuncForPC(programCounter); details != nil {
		function = details.Name()
		if index := strings.LastIndex(function, "."); index >= 0 {
			function = function[index+1:]
		}
	}
	return sourceFields{
		module:   strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)),
		function: function,
		line:     line,
	}, true
}

func mergeLogOptions(options []LogOptions) LogOptions {
	var result LogOptions
	for _, option := range options {
		if option.Event != "" {
			result.Event = option.Event
		}
		if option.EventOccurrenceID != "" {
			result.EventOccurrenceID = option.EventOccurrenceID
		}
		for key, value := range option.Metadata {
			if result.Metadata == nil {
				result.Metadata = make(map[string]any)
			}
			result.Metadata[key] = value
		}
	}
	return result
}

func metadataAttrs(metadata map[string]any) []slog.Attr {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attrs := make([]slog.Attr, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, slog.Any(key, metadata[key]))
	}
	return attrs
}
