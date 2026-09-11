package logmate

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
)

const levelFatal = slog.LevelError + 4

// Handler returns an slog.Handler that forwards records to this client.
func (c *Client) Handler(options *slog.HandlerOptions) slog.Handler {
	return &slogHandler{client: c, options: options}
}

type slogHandler struct {
	client  *Client
	options *slog.HandlerOptions
	attrs   []slog.Attr
	groups  []string
}

func (h *slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if !h.client.Enabled() {
		return false
	}
	if h.options != nil && h.options.Level != nil && level < h.options.Level.Level() {
		return false
	}
	return true
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	metadata := make(map[string]any)
	if h.options != nil && h.options.AddSource {
		module, function, line := "", "", 0
		if source, ok := ctx.Value(sourceFieldsContextKey{}).(sourceFields); ok {
			module, function, line = source.module, source.function, source.line
		} else if record.PC != 0 {
			frames := runtime.CallersFrames([]uintptr{record.PC})
			frame, _ := frames.Next()
			function = frame.Function
			if index := strings.LastIndex(function, "."); index >= 0 {
				function = function[index+1:]
			}
			module = strings.TrimSuffix(filepath.Base(frame.File), filepath.Ext(frame.File))
			line = frame.Line
		}
		metadata["logger"] = h.client.loggerName
		metadata["module"] = module
		metadata["function"] = function
		metadata["line"] = line
		metadata["thread"] = "goroutine"
	}
	// Python applies custom metadata after its automatic source fields, so a
	// caller can intentionally override any of them.
	for _, attr := range h.attrs {
		addSlogAttr(metadata, h.groups, attr)
	}
	record.Attrs(func(attr slog.Attr) bool { addSlogAttr(metadata, h.groups, attr); return true })
	entry := Entry{Severity: slogSeverity(record.Level), Message: record.Message, Timestamp: record.Time, Metadata: metadata}
	if fields, ok := ctx.Value(eventFieldsContextKey{}).(eventFields); ok {
		entry.Event = fields.event
		entry.EventOccurrenceID = fields.eventOccurrenceID
	}
	return h.client.Log(entry)
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func slogSeverity(level slog.Level) Severity {
	switch {
	case level >= levelFatal:
		return SeverityFatal
	case level <= slog.LevelDebug:
		return SeverityDebug
	case level < slog.LevelWarn:
		return SeverityInfo
	case level < slog.LevelError:
		return SeverityWarn
	default:
		return SeverityError
	}
}

func addSlogAttr(metadata map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	target := metadata
	for _, group := range groups {
		next, ok := target[group].(map[string]any)
		if !ok {
			next = make(map[string]any)
			target[group] = next
		}
		target = next
	}
	if attr.Value.Kind() == slog.KindGroup {
		child := make(map[string]any)
		for _, nested := range attr.Value.Group() {
			addSlogAttr(child, nil, nested)
		}
		if attr.Key == "" {
			for key, value := range child {
				target[key] = value
			}
		} else {
			target[attr.Key] = child
		}
		return
	}
	target[attr.Key] = attr.Value.Any()
}
