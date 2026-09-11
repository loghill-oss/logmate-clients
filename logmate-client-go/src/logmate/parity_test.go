package logmate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func successfulTransport(onLog func(string)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/v1/instances/init" {
			return httpResponse(http.StatusOK, `{"sender_id":"sender-1","instance_id":"instance-1","instance_token":"token-1"}`), nil
		}
		if request.URL.Path == "/api/v1/logs" && onLog != nil {
			body, _ := io.ReadAll(request.Body)
			onLog(string(body))
		}
		return httpResponse(http.StatusOK, `{}`), nil
	})}
}

func TestConsoleFormatMatchesPython(t *testing.T) {
	var output bytes.Buffer
	logger := buildLogger(Config{
		consoleWriter:        &output,
		DisableSystemCapture: true,
	})
	t.Cleanup(func() { _ = logger.Close() })

	logger.Warn("This is a warning message.", LogOptions{
		Metadata: map[string]any{"hidden": true},
	})

	pattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[WARNING \] This is a warning message\.\n$`)
	if !pattern.MatchString(output.String()) {
		t.Fatalf("unexpected console output: %q", output.String())
	}
}

func TestInitializationRetriesSynchronouslyBeforeReturning(t *testing.T) {
	var attempts atomic.Int32
	var initializedAttempt atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		if attempt < 3 {
			return nil, errors.New("dial tcp: connection refused")
		}
		return httpResponse(http.StatusOK, `{"sender_id":"sender-1","instance_id":"instance-1","instance_token":"token-1"}`), nil
	})}

	started := time.Now()
	client, err := New(Config{
		APIURL:               "http://logmate.test",
		SenderName:           "parity-test",
		HTTPClient:           httpClient,
		RetryAttempts:        2,
		RetryAttemptsSet:     true,
		RetryInterval:        100 * time.Millisecond,
		DisablePersistence:   true,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
		onStarted:            func() { initializedAttempt.Store(attempts.Load()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if attempts.Load() != 3 {
		t.Fatalf("got %d attempts, want 3", attempts.Load())
	}
	if initializedAttempt.Load() != 3 {
		t.Fatalf("initialization message ran at attempt %d, want 3", initializedAttempt.Load())
	}
	if elapsed := time.Since(started); elapsed < 190*time.Millisecond {
		t.Fatalf("New returned before synchronous retry delays elapsed: %s", elapsed)
	}
	if client.InstanceID() == "" {
		t.Fatal("client returned before initialization completed")
	}
}

func TestInitializationMessageAppearsBeforeApplicationLogs(t *testing.T) {
	var output bytes.Buffer
	logger := buildLogger(Config{
		APIURL:               "http://logmate.test",
		SenderName:           "parity-test",
		HTTPClient:           successfulTransport(nil),
		DisablePersistence:   true,
		DisableSystemCapture: true,
		consoleWriter:        &output,
		ErrorHandler:         func(error) {},
	})
	t.Cleanup(func() { _ = logger.Close() })

	logger.Info("application started")
	text := output.String()
	if !strings.HasPrefix(text, "[LogMate] Initialized successfully!\n") {
		t.Fatalf("initialization message was not the first output: %q", text)
	}
	if strings.Contains(text, "█") {
		t.Fatalf("startup output still contains the ASCII banner: %q", text)
	}
	if strings.Index(text, "[LogMate] Initialized successfully!") > strings.Index(text, "application started") {
		t.Fatalf("application log appeared before initialization message: %q", text)
	}
}

func TestExhaustedStartupRetriesStopNetworkForRun(t *testing.T) {
	var attempts atomic.Int32
	client, err := New(Config{
		APIURL:     "http://logmate.test",
		SenderName: "parity-test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return nil, errors.New("dial tcp: connection refused")
		})},
		RetryAttempts:        1,
		RetryAttemptsSet:     true,
		RetryInterval:        100 * time.Millisecond,
		DisablePersistence:   true,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("got %d attempts, want 2", attempts.Load())
	}
	if err := client.Info("queued for a future run", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if attempts.Load() != 2 {
		t.Fatalf("network attempts continued after startup exhaustion: %d", attempts.Load())
	}
	if client.PendingCount() != 1 {
		t.Fatalf("got %d pending records, want 1", client.PendingCount())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = client.Shutdown(ctx)
}

func TestPermanentInitializationFailureDisablesRemote(t *testing.T) {
	var attempts atomic.Int32
	client, err := New(Config{
		APIURL:     "http://logmate.test",
		SenderName: "parity-test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return httpResponse(http.StatusForbidden, `{}`), nil
		})},
		DisablePersistence:   true,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if attempts.Load() != 1 {
		t.Fatalf("got %d attempts for a permanent failure, want 1", attempts.Load())
	}
	if client.Enabled() {
		t.Fatal("remote client remained enabled after a permanent failure")
	}
}

func TestHTTPFailureClassificationMatchesPython(t *testing.T) {
	tests := []struct {
		status    int
		permanent bool
	}{
		{status: http.StatusUnauthorized, permanent: true},
		{status: http.StatusRequestTimeout, permanent: false},
		{status: http.StatusTooEarly, permanent: false},
		{status: http.StatusTooManyRequests, permanent: false},
		{status: http.StatusInternalServerError, permanent: false},
		{status: http.StatusUnprocessableEntity, permanent: true},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			err := apiError(httpResponse(test.status, `{}`))
			if got := requestIsPermanent(err); got != test.permanent {
				t.Fatalf("status %d permanent=%t, want %t", test.status, got, test.permanent)
			}
		})
	}
}

func TestInvalidEventIsIgnoredWithoutDroppingRecord(t *testing.T) {
	payloads := make(chan string, 1)
	client, err := New(Config{
		APIURL:               "http://logmate.test",
		SenderName:           "parity-test",
		HTTPClient:           successfulTransport(func(payload string) { payloads <- payload }),
		DisablePersistence:   true,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Log(Entry{Severity: SeverityInfo, Message: "kept", Event: "INVALID"}); err != nil {
		t.Fatal(err)
	}

	select {
	case payload := <-payloads:
		if strings.Contains(payload, `"event"`) {
			t.Fatalf("invalid event was sent: %s", payload)
		}
		if !strings.Contains(payload, `"message":"kept"`) {
			t.Fatalf("record was not sent: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("record was not delivered")
	}
}

func TestSuccessfulInitializationClearsPersistedQueue(t *testing.T) {
	queueFile := t.TempDir() + "/queue.sqlite3"
	database, err := sql.Open("sqlite", queueFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`CREATE TABLE log_queue (id INTEGER PRIMARY KEY AUTOINCREMENT, payload TEXT NOT NULL, created_at TEXT NOT NULL)`)
	if err == nil {
		_, err = database.Exec(`INSERT INTO log_queue (payload, created_at) VALUES ('{}', 'now')`)
	}
	_ = database.Close()
	if err != nil {
		t.Fatal(err)
	}

	client, err := New(Config{
		APIURL:               "http://logmate.test",
		SenderName:           "parity-test",
		HTTPClient:           successfulTransport(nil),
		QueueFile:            queueFile,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if pending := client.PendingCount(); pending != 0 {
		t.Fatalf("got %d persisted records after initialization, want 0", pending)
	}
}

func TestHighLevelLogUsesApplicationSourceMetadata(t *testing.T) {
	payloads := make(chan string, 1)
	logger := buildLogger(Config{
		APIURL:               "http://logmate.test",
		SenderName:           "parity-test",
		HTTPClient:           successfulTransport(func(payload string) { payloads <- payload }),
		DisablePersistence:   true,
		DisableConsole:       true,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
		onStarted:            func() {},
	})
	t.Cleanup(func() { _ = logger.Close() })
	logger.Info("source metadata")

	select {
	case payload := <-payloads:
		var record struct {
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(payload), &record); err != nil {
			t.Fatal(err)
		}
		if record.Metadata["module"] != "parity_test" {
			t.Fatalf("got source module %q, want parity_test; payload=%s", record.Metadata["module"], payload)
		}
		if record.Metadata["function"] != "TestHighLevelLogUsesApplicationSourceMetadata" {
			t.Fatalf("got source function %q; payload=%s", record.Metadata["function"], payload)
		}
	case <-time.After(time.Second):
		t.Fatal("record was not delivered")
	}
}

func TestStreamCapturePreservesOutputAndQueuesLine(t *testing.T) {
	payloads := make(chan string, 1)
	client, err := New(Config{
		APIURL:             "http://logmate.test",
		SenderName:         "parity-test",
		HTTPClient:         successfulTransport(func(payload string) { payloads <- payload }),
		DisablePersistence: true,
		ErrorHandler:       func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	terminal, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	target := terminal
	capture, err := startStreamCapture(&target, "stdout", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(target, "\x1b[31mhello\x1b[0m\n"); err != nil {
		t.Fatal(err)
	}
	capture.stop()

	if _, err := terminal.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	echoed, err := io.ReadAll(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if string(echoed) != "\x1b[31mhello\x1b[0m\n" {
		t.Fatalf("captured output was not preserved: %q", echoed)
	}
	select {
	case payload := <-payloads:
		if !strings.Contains(payload, `"severity":"UNDEFINED"`) ||
			!strings.Contains(payload, `"message":"hello"`) ||
			!strings.Contains(payload, `"captured":true`) ||
			!strings.Contains(payload, `"source":"stdout"`) {
			t.Fatalf("unexpected captured payload: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("captured output was not delivered")
	}
}

func TestCaptureReaderQueuesCompleteInputLines(t *testing.T) {
	payloads := make(chan string, 1)
	logger := buildLogger(Config{
		APIURL:               "http://logmate.test",
		SenderName:           "parity-test",
		HTTPClient:           successfulTransport(func(payload string) { payloads <- payload }),
		DisablePersistence:   true,
		DisableConsole:       true,
		DisableSystemCapture: true,
		ErrorHandler:         func(error) {},
		onStarted:            func() {},
	})
	t.Cleanup(func() { _ = logger.Close() })

	input := logger.CaptureReader(strings.NewReader("typed input\n"))
	read, err := io.ReadAll(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) != "typed input\n" {
		t.Fatalf("captured reader changed input: %q", read)
	}
	select {
	case payload := <-payloads:
		if !strings.Contains(payload, `"message":"typed input"`) ||
			!strings.Contains(payload, `"source":"stdin"`) {
			t.Fatalf("unexpected stdin payload: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("captured input was not delivered")
	}
}
