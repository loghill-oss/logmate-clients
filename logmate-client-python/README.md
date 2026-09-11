# LogMate Python Client

<div align="center">
  <img src="./assets/logmate.png" alt="LogMate" width="140" />
</div>

The official Python client for sending application logs, metadata, and events
to LogMate.

[Back to all clients](../README.md) ·
[Source](.) ·
[Example](./examples/main.py) ·
[PyPI](https://pypi.org/project/logmate/)

## What the Python client does

The client extends Python's standard `logging.Logger`. Calling `instrument()`
returns a normal logger with support for LogMate metadata and events, then
configures one client instance for the current process.

It can:

- send `DEBUG`, `INFO`, `WARN`, `ERROR`, and `FATAL` records;
- attach JSON-compatible metadata to a record;
- identify business and operational events;
- capture stdout, stderr, stdin, and uncaught exceptions;
- queue records in SQLite and retry temporary delivery failures;
- report process health to LogMate;
- continue as a local logger when no server is configured.

## Download and install

The package requires Python 3.10 or newer.

Install the current production release from PyPI:

```sh
python -m pip install logmate
```

Install a specific production version:

```sh
python -m pip install "logmate==X.Y.Z"
```

Development builds are published to TestPyPI after changes reach `staging`.
Use the complete version shown by the CI run, for example:

```sh
python -m pip install \
  --index-url https://test.pypi.org/simple/ \
  --extra-index-url https://pypi.org/simple/ \
  "logmate==0.1.2.dev12345"
```

## Quick start

This is the complete example from
[`examples/main.py`](./examples/main.py):

```python
import logmate


def main():
    logger = logmate.instrument(
        api_url="http://localhost:8080",
        sender_name="logmate-client-python-example",
    )
    try:
        # Log an info message with metadata
        logger.info(
            "application started",
            metadata={"environment": "development"},
        )

        # Log a warning message
        logger.warning("This is a warning message.")

        # Log with an event name
        logger.info("A simple event message", event="simple-event")
    finally:
        logger.close()


if __name__ == "__main__":
    main()
```

Run the repository example locally:

```sh
cd logmate-client-python
python -m pip install -e .
python examples/main.py
```

After the server accepts the instance, the client prints
`[LogMate] Initialized successfully!`. The three records then appear under the
sender named `logmate-client-python-example`.

### Expected result

The example sends one structured `INFO` record, one `WARN` record, and one
`INFO` record associated with `simple-event`:

![Logs produced by the Python example](./assets/example-logs.png)

## Configuration

### Use a `.env` file

Create `.env` in the application's working directory:

```dotenv
LOGMATE_API_URL=http://localhost:8080
LOGMATE_SENDER_NAME=checkout-api
```

Then instrument without arguments:

```python
import logmate

logger = logmate.instrument()
logger.info("Application started")
```

The URL and sender name must be configured together when they are passed as
arguments. A sender name must contain between 1 and 80 characters.

### Main options

```python
logger = logmate.instrument(
    name="logmate",
    api_url="http://localhost:8080",
    sender_name="checkout-api",
    env_file=".env",
    console=True,
    healthcheck_interval=60.0,
    timeout=10.0,
    retry_attempts=3,
    retry_interval=5.0,
    queue_file=None,
    capture_system_logs=True,
    capture_stdin=True,
    shutdown_flush_timeout=2.0,
)
```

| Option | Default | Purpose |
| --- | --- | --- |
| `api_url` | `LOGMATE_API_URL` | Base URL of the LogMate server. |
| `sender_name` | `LOGMATE_SENDER_NAME` | Sender shown in the dashboard. |
| `env_file` | `.env` discovery | Explicit environment file path. |
| `console` | `True` | Keep formatted logs visible in the terminal. |
| `healthcheck_interval` | `60.0` seconds | Interval between instance health signals. |
| `timeout` | `10.0` seconds | HTTP request timeout. |
| `retry_attempts` | `3` | Initial and delivery retry allowance. |
| `retry_interval` | `5.0` seconds | Delay between retries. |
| `queue_file` | generated under `~/.logmate` | SQLite queue location. |
| `capture_system_logs` | `True` | Capture stdout and stderr produced by the process. |
| `capture_stdin` | `True` | Capture complete lines read from stdin. |
| `shutdown_flush_timeout` | `2.0` seconds | Time allowed to flush during shutdown. |

`LOGMATE_QUEUE_FILE` can override the SQLite path, and
`LOGMATE_SHUTDOWN_TIMEOUT_SECONDS` can override the shutdown timeout.

## Send logs

### Severity methods

```python
logger.debug("Cache lookup started")
logger.info("Order received")
logger.warning("Provider response is slow")
logger.error("Payment could not be completed")
logger.fatal("Database is unavailable")
```

`fatal()` records a `FATAL` log. It does not exit the application.

### Add metadata

Metadata should be a mapping whose values can be represented as JSON:

```python
logger.info(
    "Order received",
    metadata={
        "order_id": "PED-123",
        "region": "sa-east-1",
        "amount": 42.50,
    },
)
```

Metadata remains attached to the same record, making it easier to search for an
order, request, provider, or correlation identifier in LogMate.

### Trigger an event

Use a stable lowercase event name when a record represents a situation that
LogMate should track:

```python
logger.error(
    "Payment declined by provider",
    event="payment-failed",
    event_occurrence_id="payment-PED-123-attempt-1",
    metadata={
        "order_id": "PED-123",
        "provider": "payments",
        "reason": "issuer_unavailable",
    },
)
```

Event names accept lowercase letters, numbers, `_`, and `-`, with 3 to 80
characters. An `event_occurrence_id` can contain at most 200 characters and
should uniquely identify that occurrence. Reuse it only when retrying the same
event payload.

### Send an explicit record

`send()` is useful for scripts that choose the severity dynamically:

```python
result = logger.send(
    "Payment queue is above its limit",
    severity="WARN",
    event="queue-backlog",
    metadata={"queue": "payments", "size": 850},
)
```

Accepted explicit severities are `UNDEFINED`, `TRACE`, `DEBUG`, `INFO`, `WARN`,
`ERROR`, and `FATAL`.

### Record an exception

Handled exceptions can include the traceback and application context:

```python
try:
    process_payment(order)
except PaymentError as error:
    logger.exception(
        "Payment processing failed",
        event="payment-failed",
        metadata={"order_id": order.id, "error": str(error)},
    )
    raise
```

Uncaught exceptions are captured automatically, except `KeyboardInterrupt` and
`SystemExit`.

## Automatic process capture

By default, `instrument()` captures complete lines written to stdout and stderr,
including output written directly to the operating-system file descriptors. It
also captures lines read from stdin and uncaught exceptions. Captured stream
lines use the `UNDEFINED` severity and include their source in metadata.

Disable capture when input may contain passwords, tokens, personal data, or
other secrets:

```python
logger = logmate.instrument(
    api_url="http://localhost:8080",
    sender_name="checkout-api",
    capture_system_logs=False,
    capture_stdin=False,
)
```

## Queue, retry, and shutdown

Logs are queued in FIFO order before delivery. The client uses a transactional
SQLite queue and falls back to memory if the database cannot be prepared or
written. Temporary API failures keep records queued for automatic retry while
the worker is active.

The initial connection is attempted synchronously. When LogMate is unavailable,
the client performs the configured initial retries and then lets the application
continue locally.

Close the logger during an orderly shutdown:

```python
logger.close()
```

The client also registers an `atexit` handler, but explicit shutdown gives the
queue the best opportunity to flush. To wait manually:

```python
delivered = logger.flush(timeout=5.0)
pending = logger.pending_count()
```

## Local mode

If `LOGMATE_API_URL` is absent, `instrument()` returns a local logger and does
not start the API worker. This makes the same application code usable in local
development without a running LogMate server.

## Publishing and releases

From the repository root, install the packaging tools and validate the archives:

```sh
make python-tools
make python-check
```

Publish manually when needed:

```sh
make python-publish-test
make python-publish
```

Twine reads its standard `~/.pypirc`, keyring, environment variables, or prompt.

The automated release channels are:

- changes under `logmate-client-python/**` reaching `staging` publish a unique
  `.dev<github-run-id>` version to TestPyPI;
- changes reaching `main` publish the exact version from `pyproject.toml` to
  PyPI after the test matrix passes;
- a `logmate-client-python/vX.Y.Z` tag creates the matching GitHub Release.

Increment the version in `logmate-client-python/pyproject.toml` before merging
into `main`, because PyPI versions cannot be overwritten.

Trusted Publishing uses GitHub environments named `testpypi` and `pypi`, both
associated with workflow `ci-python.yml` and restricted to `staging` and `main`
respectively.
