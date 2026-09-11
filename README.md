# LogMate Clients

<div align="center">

  <img src="logmate-client-python/assets/logmate.png" alt="LogMate" width="170" />

  **Send application logs to LogMate using the natural logging API of your language.**

  Official clients with structured metadata, events, health signals, retries,
  and SQLite-backed delivery.

  [![Python](https://img.shields.io/pypi/v/logmate-client?logo=pypi&logoColor=white&label=Python)](https://pypi.org/project/logmate-client/)
  [![Go Reference](https://pkg.go.dev/badge/github.com/loghill-oss/logmate-clients/logmate-client-go.svg)](https://pkg.go.dev/github.com/loghill-oss/logmate-clients/logmate-client-go)
  [![npm](https://img.shields.io/npm/v/%40loghill-oss%2Flogmate-client?logo=npm&label=TypeScript)](https://www.npmjs.com/package/@loghill-oss/logmate-client)
  [![Python CI](https://github.com/loghill-oss/logmate-clients/actions/workflows/ci-python.yml/badge.svg)](https://github.com/loghill-oss/logmate-clients/actions/workflows/ci-python.yml)
  [![Go CI](https://github.com/loghill-oss/logmate-clients/actions/workflows/ci-go.yml/badge.svg)](https://github.com/loghill-oss/logmate-clients/actions/workflows/ci-go.yml)
  [![TypeScript CI](https://github.com/loghill-oss/logmate-clients/actions/workflows/ci-typescript.yml/badge.svg)](https://github.com/loghill-oss/logmate-clients/actions/workflows/ci-typescript.yml)
  [![License: CPAL 1.0](https://img.shields.io/badge/License-CPAL%201.0-yellow.svg)](./LICENSE.md)

</div>

## What are the LogMate clients?

LogMate clients connect application code to a
[LogMate](https://github.com/loghill-oss/logmate) server. They preserve the
logging style developers already use while adding the information LogMate needs
to organize and investigate each record.

When a client starts, it registers the running process as an instance of a
sender. Logs from different containers, workers, or local processes can then be
viewed separately without losing their shared sender identity.

The clients are designed for APIs, workers, scheduled jobs, integrations, and
any process that must keep useful diagnostic and business context around a log.

## Choose a client

| Language | Requirement | Download | Documentation |
| --- | --- | --- | --- |
| Python | Python 3.10+ | `pip install logmate-client` | [Python guide](./logmate-client-python/README.md) |
| Go | Go 1.22+ | `go get github.com/loghill-oss/logmate-clients/logmate-client-go@latest` | [Go guide](./logmate-client-go/README.md) |
| TypeScript | Node.js 22.13+ | `npm install @loghill-oss/logmate-client` | [TypeScript guide](./logmate-client-ts/README.md) |

Each client is versioned, tested, and published independently. Path-filtered
workflows ensure that a change to one implementation does not publish either of
the other packages.

## Connect an application

Both clients understand the same basic environment variables. Add a `.env`
file to the application working directory:

```dotenv
LOGMATE_API_URL=http://localhost:8080
LOGMATE_SENDER_NAME=checkout-api
```

`LOGMATE_API_URL` is the address of the LogMate server. `LOGMATE_SENDER_NAME`
identifies the application in the dashboard. The values can also be supplied
directly when the client is instrumented.

Without `LOGMATE_API_URL`, the clients stay in local mode: application logging
continues, but no network delivery is attempted.

## Python quick start

Install the package:

```sh
python -m pip install logmate-client
```

Instrument the process and use the returned logger:

```python
import logmate

logger = logmate.instrument(
    api_url="http://localhost:8080",
    sender_name="logmate-client-python-example",
)

logger.info(
    "application started",
    metadata={"environment": "development"},
)
logger.warning("This is a warning message.")
logger.info("A simple event message", event="simple-event")
```

See the [complete Python guide](./logmate-client-python/README.md) and the
[executable Python example](./logmate-client-python/examples/main.py).

## Go quick start

Add the module:

```sh
go get github.com/loghill-oss/logmate-clients/logmate-client-go@latest
```

Instrument the process and use `LogOptions` for metadata and events:

```go
package main

import logmate "github.com/loghill-oss/logmate-clients/logmate-client-go"

func main() {
    logger := logmate.Instrument(logmate.Config{
        APIURL:     "http://localhost:8080",
        SenderName: "logmate-client-go-example",
    })
    defer logger.Close()

    logger.Info("application started", logmate.LogOptions{
        Metadata: map[string]any{"environment": "development"},
    })
    logger.Warn("This is a warning message.")
    logger.Info("A simple event message", logmate.LogOptions{
        Event: "simple-event",
    })
}
```

See the [complete Go guide](./logmate-client-go/README.md) and the
[executable Go example](./logmate-client-go/examples/main.go).

## TypeScript quick start

Install the package:

```sh
npm install @loghill-oss/logmate-client
```

Instrument the process and await the initial connection attempts:

```ts
import { instrument } from "@loghill-oss/logmate-client";

const logger = await instrument({
  apiUrl: "http://localhost:8080",
  senderName: "logmate-client-typescript-example",
});

try {
  logger.info("application started", {
    metadata: { environment: "development" },
  });
  logger.warning("This is a warning message.");
  logger.info("A simple event message", { event: "simple-event" });
} finally {
  await logger.close();
}
```

See the [complete TypeScript guide](./logmate-client-ts/README.md) and the
[executable TypeScript example](./logmate-client-ts/examples/main.ts).

## See the result in LogMate

The Python, Go, and TypeScript examples produce the same structured records: an information
message with environment metadata, a warning, and an information message linked
to the `simple-event` event.

![Logs sent by all client examples](logmate-client-python/assets/example-logs.png)

## What a record contains

Every record can include:

- a severity: `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`, or `FATAL`;
- a human-readable message;
- a timestamp generated by the client;
- structured metadata such as an order, route, region, or provider;
- an optional event name for business or operational rules;
- an optional event occurrence identifier for correlation and idempotency;
- the sender and running instance assigned by LogMate.

`FATAL` is the highest severity in the LogMate contract. Calling `fatal` in
Python/TypeScript or `Fatal` in Go records the message and does not terminate
the process.

## Delivery behavior

All clients initialize a sender instance, queue records in FIFO order, retry
temporary failures, and send periodic health signals. SQLite is used for the
durable queue, with an in-memory fallback if local persistence is unavailable.

The Go client uses a pure-Go SQLite driver and does not require CGO. The Python
client uses the `sqlite3` module included with Python. TypeScript uses Node's
built-in `node:sqlite`, with no native addon to compile.

The language guides explain shutdown, automatic logging integration, queue
configuration, and the differences between each runtime.

## Repository layout

```text
logmate-clients/
├── logmate-client-go/
│   ├── assets/
│   ├── examples/
│   ├── README.md
│   ├── src/logmate/
│   └── go.mod
├── logmate-client-python/
│   ├── assets/
│   ├── examples/
│   ├── README.md
│   ├── src/logmate/
│   └── pyproject.toml
├── logmate-client-ts/
│   ├── assets/
│   ├── examples/
│   ├── README.md
│   ├── src/
│   └── package.json
├── .github/workflows/
└── Makefile
```

## Release channels

| Client | Staging | Production |
| --- | --- | --- |
| Python | TestPyPI development version | PyPI version from `pyproject.toml` |
| Go | SemVer prerelease such as `v0.1.1-rc.12345` | Stable SemVer tag such as `v0.1.1` |
| TypeScript | npm prerelease on dist-tag `next` | Stable npm version on dist-tag `latest` |

Publishing runs only after the corresponding client CI passes. See the
[Python release instructions](./logmate-client-python/README.md#publishing-and-releases),
[Go release instructions](./logmate-client-go/README.md#publishing-and-releases), or
[TypeScript release instructions](./logmate-client-ts/README.md#publishing-and-releases).

## License

LogMate clients are distributed under the [CPAL 1.0 license](./LICENSE.md).

<div align="center">
  <img src="logmate-client-python/assets/loghill-banner.png" alt="Loghill — observability open-source software" width="800" />
</div>
