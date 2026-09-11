package logmate

import (
	"strings"
	"sync"
)

type lineWriter struct {
	client   *Client
	severity Severity
	mu       sync.Mutex
	buffer   string
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(data)
	w.buffer += string(data)
	for {
		index := strings.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSuffix(w.buffer[:index], "\r")
		w.buffer = w.buffer[index+1:]
		if strings.TrimSpace(line) != "" {
			_ = w.client.Log(Entry{Severity: w.severity, Message: line})
		}
	}
	return written, nil
}
