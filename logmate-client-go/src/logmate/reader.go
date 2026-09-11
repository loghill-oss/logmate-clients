package logmate

import (
	"io"
	"strings"
	"sync"
)

type capturedReader struct {
	reader io.Reader
	client *Client
	mu     sync.Mutex
	buffer string
}

// CaptureReader wraps an input stream and records every complete line read by
// the application as UNDEFINED, matching Python's stdin capture semantics.
// Go cannot safely replace stdin for every package, so callers pass the
// returned reader to their scanner, decoder, or parser.
func (l *Logger) CaptureReader(reader io.Reader) io.Reader {
	if reader == nil || l.client == nil || !l.client.Enabled() {
		return reader
	}
	return &capturedReader{reader: reader, client: l.client}
}

func (r *capturedReader) Read(data []byte) (int, error) {
	count, readErr := r.reader.Read(data)
	if count > 0 {
		r.consume(string(data[:count]), false)
	}
	if readErr == io.EOF {
		r.consume("", true)
	}
	return count, readErr
}

func (r *capturedReader) consume(value string, final bool) {
	r.mu.Lock()
	r.buffer += value
	parts := strings.Split(r.buffer, "\n")
	r.buffer = parts[len(parts)-1]
	complete := parts[:len(parts)-1]
	if final && r.buffer != "" {
		complete = append(complete, r.buffer)
		r.buffer = ""
	}
	r.mu.Unlock()
	for _, line := range complete {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		_ = r.client.Log(Entry{
			Severity: SeverityUndefined,
			Message:  line,
			Metadata: map[string]any{"captured": true, "source": "stdin"},
		})
	}
}
