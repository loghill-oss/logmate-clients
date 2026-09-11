package logmate

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Version is the module version exposed to diagnostics.
const Version = "0.1.0"

// Severity is a LogMate severity accepted by the ingestion API.
type Severity string

const (
	SeverityUndefined Severity = "UNDEFINED"
	SeverityTrace     Severity = "TRACE"
	SeverityDebug     Severity = "DEBUG"
	SeverityInfo      Severity = "INFO"
	SeverityWarn      Severity = "WARN"
	SeverityError     Severity = "ERROR"
	SeverityFatal     Severity = "FATAL"
)

// Entry is one structured log record. Metadata must be JSON-serializable.
type Entry struct {
	Severity          Severity       `json:"severity"`
	Message           string         `json:"message"`
	Timestamp         time.Time      `json:"timestamp"`
	Event             string         `json:"event,omitempty"`
	EventOccurrenceID string         `json:"event_occurrence_id,omitempty"`
	Metadata          map[string]any `json:"metadata"`
}

// LogOptions carries the optional structured fields accepted by the high-level
// Logger methods. Go has no named arguments, so this struct is the equivalent
// of Python's metadata, event, and event_occurrence_id keyword arguments.
type LogOptions struct {
	Metadata          map[string]any
	Event             string
	EventOccurrenceID string
}

// Config configures a Client. LOGMATE_API_URL and LOGMATE_SENDER_NAME take
// precedence over APIURL and SenderName, matching the Python client.
type Config struct {
	Name                 string
	APIURL               string
	SenderName           string
	EnvFile              string
	HTTPClient           *http.Client
	Level                slog.Leveler
	Timeout              time.Duration
	RetryAttempts        int
	RetryAttemptsSet     bool
	RetryInterval        time.Duration
	HealthcheckInterval  time.Duration
	QueueFile            string
	DisablePersistence   bool
	DisableConsole       bool
	DisableSystemCapture bool
	ShutdownTimeout      time.Duration
	ErrorHandler         func(error)
	onStarted            func()
	consoleWriter        io.Writer
}

// Client is safe for concurrent use.
type Client struct {
	apiURL, senderName string
	loggerName         string
	httpClient         *http.Client
	timeout            time.Duration
	retryAttempts      int
	retryInterval      time.Duration
	healthInterval     time.Duration
	shutdownTimeout    time.Duration
	onError            func(error)
	remoteEnabled      bool

	mu             sync.Mutex
	memoryQueue    []queuedEntry
	queueFile      string
	queueDB        *sql.DB
	persistent     bool
	senderID       string
	instanceID     string
	instanceToken  string
	closed         bool
	workerRunning  bool
	wake           chan struct{}
	done           chan struct{}
	workerDone     chan struct{}
	once           sync.Once
	workerDoneOnce sync.Once
	onStarted      func()
	onDisabled     func()
	reportMu       sync.Mutex
	reportedErrors map[string]struct{}
}

type queuedEntry struct {
	Entry            Entry  `json:"entry"`
	SenderID         string `json:"sender_id,omitempty"`
	OriginInstanceID string `json:"origin_instance_id,omitempty"`
	queueID          int64
	persisted        bool
}
