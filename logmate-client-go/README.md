# LogMate Go Client

<div align="center">
  <img src="./assets/logmate.png" alt="LogMate" width="140" />
</div>

The official Go client for sending application logs, metadata, and events to
LogMate.

[Back to all clients](../README.md) ·
[Source](.) ·
[Example](./examples/main.go) ·
[Go package](https://pkg.go.dev/github.com/loghill-oss/logmate-clients/logmate-client-go)

## What the Go client does

`logmate.Instrument()` returns a process-wide logger with a small API modeled
after the Python client. Optional `LogOptions` carry metadata and event fields
without using loose key/value arguments.

It can:

- send `DEBUG`, `INFO`, `WARN`, `ERROR`, and `FATAL` records;
- attach JSON-compatible metadata to a record;
- identify business and operational events;
- integrate with the standard `log/slog` and `log` packages;
- capture complete lines written through `os.Stdout` and `os.Stderr`;
- persist pending records in SQLite without CGO;
- retry the initial connection synchronously and delivery failures in a
  background worker;
- report process health to LogMate;
- continue logging locally when no server is configured.

## Download and install

The module requires Go 1.22 or newer. Install the latest stable release:

```sh
go get github.com/loghill-oss/logmate-clients/logmate-client-go@latest
```

Install a specific stable version:

```sh
go get github.com/loghill-oss/logmate-clients/logmate-client-go@v0.1.0
```

Staging builds use prerelease versions. Copy the complete version from the CI
summary or GitHub Releases page:

```sh
go get github.com/loghill-oss/logmate-clients/logmate-client-go@v0.1.1-rc.12345
```

The SQLite driver is implemented entirely in Go, so installation and builds do
not require a C compiler or CGO.

## Quick start

This is the complete example from
[`examples/main.go`](./examples/main.go):

```go
package main

import logmate "github.com/loghill-oss/logmate-clients/logmate-client-go"

func main() {
    logger := logmate.Instrument(logmate.Config{
        APIURL:     "http://localhost:8080",
        SenderName: "logmate-client-go-example",
    })
    defer logger.Close()

    // Log an info message with metadata
    logger.Info("application started", logmate.LogOptions{
        Metadata: map[string]any{"environment": "development"},
    })

    // Log a warning message
    logger.Warn("This is a warning message.")

    // Log with an event name
    logger.Info("A simple event message", logmate.LogOptions{
        Event: "simple-event",
    })
}
```

Run the repository example locally:

```sh
cd logmate-client-go
go run ./examples
```

As in Python, initialization and its configured retries finish before
`Instrument()` returns. After the server accepts the instance, the client prints
`[LogMate] Initialized successfully!`; application logs therefore appear after
that initialization message.

Terminal output uses the same compact shape as the Python client instead of the
native `slog` `key=value` formatter:

```text
2026-09-08 02:55:08 [INFO    ] application started
2026-09-08 02:55:08 [WARNING ] This is a warning message.
```

### Expected result

The example sends one structured `INFO` record, one `WARN` record, and one
`INFO` record associated with `simple-event`:

![Logs produced by the Go example](./assets/example-logs.png)

## Configuration

### Use a `.env` file

Create `.env` in the application's working directory:

```dotenv
LOGMATE_API_URL=http://localhost:8080
LOGMATE_SENDER_NAME=checkout-api
```

Then instrument without arguments:

```go
logger := logmate.Instrument()
defer logger.Close()

logger.Info("Application started")
```

When no sender name is configured, `Name` is used and defaults to `logmate`. A
sender name can contain at most 80 characters.

### Use an explicit configuration

```go
logger := logmate.Instrument(logmate.Config{
    Name:                "logmate",
    APIURL:              "http://localhost:8080",
    SenderName:          "checkout-api",
    EnvFile:             ".env",
    Level:               slog.LevelDebug,
    Timeout:             10 * time.Second,
    RetryAttempts:       3,
    RetryInterval:       5 * time.Second,
    HealthcheckInterval: time.Minute,
    ShutdownTimeout:     2 * time.Second,
})
defer logger.Close()
```

| Option | Default | Purpose |
| --- | --- | --- |
| `Name` | `logmate` | Logger name and fallback sender name. |
| `APIURL` | `LOGMATE_API_URL` | Base URL of the LogMate server. |
| `SenderName` | `LOGMATE_SENDER_NAME` | Sender shown in the dashboard. |
| `EnvFile` | `.env` discovery | Explicit environment file path. |
| `HTTPClient` | client created by LogMate | Custom HTTP transport and behavior. |
| `Level` | `slog.LevelDebug` | Minimum console and remote log level. |
| `Timeout` | `10s` | HTTP request timeout. |
| `RetryAttempts` | `3` | Retries after the first connection or delivery attempt. |
| `RetryAttemptsSet` | `false` | Set to `true` only when explicitly selecting zero retries. |
| `RetryInterval` | `5s` | Delay before a temporary failure is retried. |
| `HealthcheckInterval` | `1m` | Interval between instance health signals. |
| `QueueFile` | generated under `~/.logmate` | SQLite queue location. |
| `DisablePersistence` | `false` | Use a memory-only queue when enabled. |
| `DisableConsole` | `false` | Suppress the local text handler when enabled. |
| `DisableSystemCapture` | `false` | Do not capture stdout and stderr. |
| `ShutdownTimeout` | `2s` | Time allowed to flush during `Close`. |
| `ErrorHandler` | writes to stderr | Receives internal delivery and queue errors. |

As in Python, environment values take precedence over `APIURL` and `SenderName`.
Existing process environment values are not overwritten when `.env` is loaded.
`LOGMATE_QUEUE_FILE` overrides the default queue location and
`LOGMATE_SHUTDOWN_TIMEOUT_SECONDS` overrides the shutdown timeout.

The zero value of `RetryAttempts` means “use the default” so ordinary Go config
literals remain concise. To explicitly make only the first request with no
retry, set both `RetryAttempts: 0` and `RetryAttemptsSet: true`.

## Send logs

### Severity methods

```go
logger.Debug("Cache lookup started")
logger.Info("Order received")
logger.Warn("Provider response is slow")
logger.Error("Payment could not be completed")
logger.Fatal("Database is unavailable")
```

`Fatal` records a `FATAL` log. It does not exit the application.

### Add metadata

Pass one optional `LogOptions` value to the severity method. Metadata is
normalized for JSON; values that cannot be encoded are represented as strings,
matching Python's safe fallback:

```go
logger.Info("Order received", logmate.LogOptions{
    Metadata: map[string]any{
        "order_id": "PED-123",
        "region":   "sa-east-1",
        "amount":   42.50,
    },
})
```

High-level records also include `logger`, `module`, `function`, `line`, and
`thread` source fields. Go does not expose goroutine names, so `thread` is set to
`goroutine`; explicitly supplied metadata can override any automatic field.

The high-level LogMate logger intentionally uses `LogOptions`; it does not use
the loose `"key", value` argument format of `slog`.

### Trigger an event

Use a stable lowercase event name when a record represents a situation that
LogMate should track:

```go
logger.Error("Payment declined by provider", logmate.LogOptions{
    Event:             "payment-failed",
    EventOccurrenceID: "payment-PED-123-attempt-1",
    Metadata: map[string]any{
        "order_id": "PED-123",
        "provider": "payments",
        "reason":   "issuer_unavailable",
    },
})
```

Event names accept lowercase letters, numbers, `_`, and `-`, with 3 to 80
characters. An `EventOccurrenceID` can contain at most 200 characters and should
uniquely identify that occurrence. Invalid event fields are reported and ignored
without dropping the log.

### Send an explicit record

Use `Send` when the severity, timestamp, or event fields are built dynamically:

```go
err := logger.Send(logmate.Entry{
    Severity: logmate.SeverityWarn,
    Message:  "Payment queue is above its limit",
    Event:    "queue-backlog",
    Metadata: map[string]any{"queue": "payments", "size": 850},
})
if err != nil {
    // The record was invalid or could not be queued.
}
```

Available severity constants are `SeverityUndefined`, `SeverityTrace`,
`SeverityDebug`, `SeverityInfo`, `SeverityWarn`, `SeverityError`, and
`SeverityFatal`.

## Standard library integration

`Instrument()` installs the underlying logger as the process default for both
`log/slog` and `log`:

```go
slog.Info("HTTP request completed", "status", 200, "route", "/checkout")
log.Print("worker started")
```

Native `slog` calls retain the standard key/value format, and those attributes
become LogMate metadata. Calls made through `logger.Info`, `logger.Error`, and
the other LogMate methods use `LogOptions` instead.

`Instrument()` also preserves and captures complete lines written through
`fmt.Print`, `os.Stdout`, and `os.Stderr`. Captured lines use `UNDEFINED` and
include `{"captured": true, "source": "stdout"}` or `stderr` metadata. Disable
this with `DisableSystemCapture` when terminal output may contain secrets.

Go cannot safely replace stdin for every package. Wrap the reader used by the
application to obtain Python-equivalent input capture:

```go
scanner := bufio.NewScanner(logger.CaptureReader(os.Stdin))
```

Go also has no process-wide hook that can recover panics from arbitrary
goroutines. Install the helper in every goroutine whose unhandled panic should
be sent, including `main`:

```go
defer logger.RecoverPanic()
```

The helper records the stack and resumes the panic, preserving normal Go crash
behavior.

## Queue, retry, and shutdown

The initial connection is synchronous. The first request is followed by up to
`RetryAttempts` retries, separated by `RetryInterval`. On success, the
initialization message is printed and a background worker starts FIFO delivery.
If the initial attempts are exhausted, the application is released, console
logging continues, records may remain queued, and no further network attempt is
made during that run.

After a successful startup, temporary delivery failures remain asynchronous.
The client queues records, reports retry/offline state, and reconnects using the
same policy as Python. A permanent API failure disables remote delivery for the
run while keeping local logging available.

By default, pending records are stored in a transactional SQLite database under
`~/.logmate`. Following the Python queue lifecycle, persisted records are
cleared and their count is reported after a successful instance initialization.
If SQLite cannot be prepared or written, affected records remain in memory and
the configured error handler is notified.

Use `Flush` when the application needs to wait for pending delivery:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if !logger.Flush(ctx) {
    // The deadline ended with records still pending.
}
```

Always arrange for `Close` during an orderly shutdown:

```go
logger := logmate.Instrument()
defer logger.Close()
```

`Close` restores the previous global `slog` and `log` configuration, flushes up
to `ShutdownTimeout`, and releases the background worker and SQLite connection.

## Local mode

If `LOGMATE_API_URL` is absent, `Instrument()` installs a console-only logger
and does not start the API worker. The same application code can therefore run
locally without a LogMate server.

For advanced embedding, create the lower-level client directly:

```go
client, err := logmate.New(logmate.Config{
    APIURL:     "http://localhost:8080",
    SenderName: "checkout-api",
})
if err != nil {
    return err
}
defer client.Close()
```

## Publishing and releases

Go does not have separate production and staging registries. Both channels use
the same module path and are separated by semantic versions:

- changes under `logmate-client-go/**` reaching `staging` create a unique tag
  such as `logmate-client-go/v0.1.1-rc.<github-run-id>` and a GitHub prerelease;
- changes reaching `main` create the stable tag declared by `Version`, such as
  `logmate-client-go/v0.1.1`, and a GitHub Release;
- the tag-based release workflow remains available as a manual fallback.

Set `Version` in `logmate-client-go/src/logmate/types.go` to the next target
before merging. A stable Go version is immutable, so production publishing
fails if its tag already points to another commit.

GitHub environments separate the channels:

- `go-staging` is restricted to branch `staging`;
- `go-production` is restricted to `main` and may require manual approval.

No registry token is required. Publishing a Go module consists of pushing the
correct module-prefixed semantic-version tag. Stable versions are preferred by
`@latest`, so a staging prerelease does not replace the production release.
