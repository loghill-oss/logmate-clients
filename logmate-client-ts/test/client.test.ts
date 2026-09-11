import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, test } from "node:test";
import { instrument, type LogMateLogger } from "../src/index.js";
import { QueueStore } from "../src/queue-store.js";

let currentLogger: LogMateLogger | undefined;

afterEach(async () => {
  await currentLogger?.close();
  currentLogger = undefined;
});

test("uses local mode when no API is configured", async () => {
  const previousUrl = process.env.LOGMATE_API_URL;
  const previousName = process.env.LOGMATE_SENDER_NAME;
  delete process.env.LOGMATE_API_URL;
  delete process.env.LOGMATE_SENDER_NAME;
  try {
    currentLogger = await instrument({ console: false, captureSystemLogs: false, captureStdin: false });
    assert.deepEqual(currentLogger.send("local"), { sent: false, queued: false, local: true });
  } finally {
    if (previousUrl === undefined) delete process.env.LOGMATE_API_URL;
    else process.env.LOGMATE_API_URL = previousUrl;
    if (previousName === undefined) delete process.env.LOGMATE_SENDER_NAME;
    else process.env.LOGMATE_SENDER_NAME = previousName;
  }
});

test("initializes, sends metadata and event, and reports health", async () => {
  const received: Array<{ path: string; body: Record<string, unknown>; headers: Record<string, string | string[] | undefined> }> = [];
  const server = createServer((request, response) => {
    const chunks: Buffer[] = [];
    request.on("data", (chunk: Buffer) => chunks.push(chunk));
    request.on("end", () => {
      const body = JSON.parse(Buffer.concat(chunks).toString()) as Record<string, unknown>;
      received.push({ path: request.url ?? "", body, headers: request.headers });
      response.setHeader("content-type", "application/json");
      response.end(request.url?.endsWith("/init")
        ? JSON.stringify({ sender_id: "sender-1", instance_id: "instance-1", instance_token: "token-1" })
        : "{}");
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert(address && typeof address === "object");
  try {
    currentLogger = await instrument({
      apiUrl: `http://127.0.0.1:${address.port}`,
      senderName: "typescript-test",
      console: false,
      captureSystemLogs: false,
      captureStdin: false,
      disablePersistence: true,
      healthcheckInterval: 1,
      retryInterval: 0.01,
    });
    currentLogger.info("application started", {
      metadata: { environment: "test" },
      event: "simple-event",
    });
    assert.equal(await currentLogger.flush(1), true);
    await new Promise((resolve) => setTimeout(resolve, 1_100));
    const log = received.find((entry) => entry.path === "/api/v1/logs");
    assert(log);
    assert.equal(log.body.message, "application started");
    assert.equal(log.body.event, "simple-event");
    assert.equal((log.body.metadata as Record<string, unknown>).environment, "test");
    assert.equal(log.headers["x-sender-instance-id"], "instance-1");
    assert(received.some((entry) => entry.path === "/api/v1/senders/sender-1/health"));
  } finally {
    await currentLogger?.close();
    currentLogger = undefined;
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});

test("waits for the configured initial retry before returning", async () => {
  let attempts = 0;
  const server = createServer((_request, response) => {
    attempts += 1;
    response.setHeader("content-type", "application/json");
    if (attempts === 1) {
      response.statusCode = 503;
      response.end(JSON.stringify({ message: "temporarily unavailable" }));
      return;
    }
    response.end(JSON.stringify({
      sender_id: "sender-retry",
      instance_id: "instance-retry",
      instance_token: "token-retry",
    }));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert(address && typeof address === "object");
  try {
    currentLogger = await instrument({
      apiUrl: `http://127.0.0.1:${address.port}`,
      senderName: "retry-test",
      console: false,
      captureSystemLogs: false,
      captureStdin: false,
      disablePersistence: true,
      retryAttempts: 1,
      retryInterval: 0.1,
    });
    assert.equal(attempts, 2);
  } finally {
    await currentLogger?.close();
    currentLogger = undefined;
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});

test("ignores invalid event fields without dropping the record", async () => {
  currentLogger = await instrument({ console: false, captureSystemLogs: false, captureStdin: false });
  const result = currentLogger.send("message", { event: "NO" });
  assert.equal(result.local, true);
});

test("persists and acknowledges records in SQLite FIFO order", async () => {
  const directory = mkdtempSync(join(tmpdir(), "logmate-ts-test-"));
  const queue = new QueueStore({
    apiUrl: "http://localhost:8080",
    senderName: "sqlite-test",
    queueFile: join(directory, "queue.sqlite3"),
    disablePersistence: false,
    report: (message) => assert.fail(message),
  });
  try {
    await queue.prepare();
    const firstId = queue.enqueue({ message: "first" });
    const secondId = queue.enqueue({ message: "second" });
    assert.equal(typeof firstId, "number");
    assert.equal(typeof secondId, "number");
    assert.equal(queue.pendingCount(), 2);
    const first = queue.peek();
    assert(first);
    assert.equal(first.payload.message, "first");
    queue.acknowledge(first);
    assert.equal(queue.peek()?.payload.message, "second");
    assert.equal(queue.clearPersistent(), 1);
    assert.equal(queue.pendingCount(), 0);
  } finally {
    queue.close();
    rmSync(directory, { recursive: true, force: true });
  }
});
