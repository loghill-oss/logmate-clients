package logmate

import (
	"bufio"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

type streamCapture struct {
	client   *Client
	source   string
	target   **os.File
	original *os.File
	reader   *os.File
	writer   *os.File
	done     chan struct{}
	stopOnce sync.Once
}

func (l *Logger) installSystemCapture() {
	l.captureMu.Lock()
	defer l.captureMu.Unlock()
	if !l.captureSystem || l.client == nil || !l.client.Enabled() {
		return
	}
	stdout, err := startStreamCapture(&os.Stdout, "stdout", l.client)
	if err != nil {
		l.client.report(errors.New("could not capture stdout: " + err.Error()))
		return
	}
	l.stdoutCapture = stdout
	stderr, err := startStreamCapture(&os.Stderr, "stderr", l.client)
	if err != nil {
		stdout.stop()
		l.stdoutCapture = nil
		l.client.report(errors.New("could not capture stderr: " + err.Error()))
		return
	}
	l.stderrCapture = stderr
}

func (l *Logger) restoreSystemCapture() {
	l.captureMu.Lock()
	defer l.captureMu.Unlock()
	if l.stdoutCapture != nil {
		l.stdoutCapture.stop()
		l.stdoutCapture = nil
	}
	if l.stderrCapture != nil {
		l.stderrCapture.stop()
		l.stderrCapture = nil
	}
}

func startStreamCapture(target **os.File, source string, client *Client) (*streamCapture, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	capture := &streamCapture{
		client:   client,
		source:   source,
		target:   target,
		original: *target,
		reader:   reader,
		writer:   writer,
		done:     make(chan struct{}),
	}
	*target = writer
	go capture.readLoop()
	return capture, nil
}

func (c *streamCapture) readLoop() {
	defer close(c.done)
	buffered := bufio.NewReader(io.TeeReader(c.reader, c.original))
	for {
		line, err := buffered.ReadString('\n')
		if line != "" {
			c.emit(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		}
		if err != nil {
			return
		}
	}
}

func (c *streamCapture) emit(line string) {
	line = ansiEscapePattern.ReplaceAllString(line, "")
	if strings.TrimSpace(line) == "" {
		return
	}
	_ = c.client.Log(Entry{
		Severity: SeverityUndefined,
		Message:  line,
		Metadata: map[string]any{"captured": true, "source": c.source},
	})
}

func (c *streamCapture) stop() {
	c.stopOnce.Do(func() {
		if *c.target == c.writer {
			*c.target = c.original
		}
		_ = c.writer.Close()
		<-c.done
		_ = c.reader.Close()
	})
}
