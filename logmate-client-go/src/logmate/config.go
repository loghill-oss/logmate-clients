package logmate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultRetryAttempts = 3

// New creates a client. Like the Python client, the first connection and its
// configured retries are performed synchronously. Delivery becomes
// asynchronous only after initialization succeeds.
func New(config Config) (*Client, error) {
	if (strings.TrimSpace(config.APIURL) == "") != (strings.TrimSpace(config.SenderName) == "") {
		return nil, errors.New("APIURL and SenderName must be provided together")
	}
	if err := loadEnv(config.EnvFile); err != nil {
		return nil, fmt.Errorf("could not read the .env file: %w", err)
	}
	name := normalizeName(config.Name)
	if name == "" {
		name = "logmate"
	}
	apiURL := strings.TrimRight(strings.TrimSpace(first(os.Getenv("LOGMATE_API_URL"), config.APIURL)), "/")
	senderName := normalizeName(first(os.Getenv("LOGMATE_SENDER_NAME"), config.SenderName, name))
	if apiURL != "" && !strings.HasPrefix(apiURL, "http://") && !strings.HasPrefix(apiURL, "https://") {
		return nil, errors.New("LOGMATE_API_URL must start with http:// or https://")
	}
	if apiURL == "" {
		senderName = ""
	}
	if utf8.RuneCountInString(senderName) > 80 {
		return nil, errors.New("LOGMATE_SENDER_NAME must contain between 1 and 80 characters")
	}
	retryAttempts := defaultRetryAttempts
	if config.RetryAttemptsSet || config.RetryAttempts != 0 {
		retryAttempts = config.RetryAttempts
		if retryAttempts < 0 {
			retryAttempts = 0
		}
	}
	timeout := durationOr(config.Timeout, 10*time.Second)
	shutdownTimeout := durationOr(config.ShutdownTimeout, 2*time.Second)
	if configured := strings.TrimSpace(os.Getenv("LOGMATE_SHUTDOWN_TIMEOUT_SECONDS")); configured != "" {
		seconds, err := strconv.ParseFloat(configured, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid LOGMATE_SHUTDOWN_TIMEOUT_SECONDS: %w", err)
		}
		shutdownTimeout = time.Duration(max(seconds, 0) * float64(time.Second))
	}
	retryInterval := durationOr(config.RetryInterval, 5*time.Second)
	if retryInterval < 100*time.Millisecond {
		retryInterval = 100 * time.Millisecond
	}
	healthInterval := durationOr(config.HealthcheckInterval, time.Minute)
	if healthInterval < time.Second {
		healthInterval = time.Second
	}
	client := &Client{
		apiURL:          apiURL,
		senderName:      senderName,
		loggerName:      name,
		timeout:         timeout,
		retryAttempts:   retryAttempts,
		retryInterval:   retryInterval,
		healthInterval:  healthInterval,
		shutdownTimeout: shutdownTimeout,
		wake:            make(chan struct{}, 1),
		done:            make(chan struct{}),
		workerDone:      make(chan struct{}),
		onError:         config.ErrorHandler,
		onStarted:       config.onStarted,
		remoteEnabled:   apiURL != "",
		reportedErrors:  make(map[string]struct{}),
	}
	if client.onError == nil {
		stderr := log.New(os.Stderr, "[LogMate] ", 0)
		client.onError = func(err error) { stderr.Println(err) }
	}
	if config.HTTPClient != nil {
		client.httpClient = config.HTTPClient
	} else {
		client.httpClient = &http.Client{Timeout: timeout}
	}
	if apiURL == "" {
		return client, nil
	}
	if !config.DisablePersistence {
		client.queueFile = resolveQueueFile(first(config.QueueFile, os.Getenv("LOGMATE_QUEUE_FILE")))
		if client.queueFile == "" {
			client.queueFile = defaultQueueFile(apiURL, senderName)
		}
		client.prepareQueue()
	}
	if !client.initializeBlocking() {
		return client, nil
	}
	client.mu.Lock()
	client.workerRunning = true
	client.mu.Unlock()
	go client.run()
	return client, nil
}

func resolveQueueFile(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func loadEnv(path string) error {
	paths := []string{path}
	if strings.TrimSpace(path) == "" {
		paths = []string{".env"}
		if _, source, _, ok := runtime.Caller(0); ok {
			base := filepath.Dir(source)
			paths = append(paths, filepath.Join(base, ".env"), filepath.Join(filepath.Dir(base), ".env"))
		}
	}
	for _, candidate := range paths {
		info, err := os.Stat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		return readEnv(candidate)
	}
	return nil
}

func readEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			if _, exists := os.LookupEnv(key); !exists {
				_ = os.Setenv(key, value)
			}
		}
	}
	return scanner.Err()
}

func normalizeName(value string) string { return strings.Join(strings.Fields(value), " ") }
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
func defaultQueueFile(apiURL, senderName string) string {
	sum := sha256.Sum256([]byte(apiURL + "|" + senderName))
	base, err := os.UserHomeDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, ".logmate", "queue-"+hex.EncodeToString(sum[:6])+".sqlite3")
}
