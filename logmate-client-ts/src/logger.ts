import { basename, extname } from "node:path";
import { Client } from "./client.js";
import { severityPriority, STARTED_MESSAGE } from "./constants.js";
import { Terminal } from "./console.js";
import { ProcessCapture } from "./capture.js";
import { friendlyError } from "./errors.js";
import type { Entry, InstrumentOptions, LogOptions, Metadata, SendResult, Severity } from "./types.js";
import { resolveConfig } from "./config.js";
import { safeMetadata, sanitizeOptions, validationError } from "./validation.js";

interface SendOptions extends LogOptions {
  severity?: Severity;
  timestamp?: Date | string;
}

function sourceMetadata(): Metadata {
  const stack = new Error().stack?.split("\n").slice(1) ?? [];
  const line = stack.find((candidate) =>
    !candidate.includes("/src/logger.") &&
    !candidate.includes("/dist/index.") &&
    !candidate.includes("node:internal"),
  );
  if (!line) return { logger: "logmate", thread: "main" };
  const match = line.match(/at\s+(?:(.*?)\s+\()?(.+?):(\d+):(\d+)\)?$/);
  if (!match) return { logger: "logmate", thread: "main" };
  const file = match[2] ?? "";
  const filename = basename(file);
  return {
    logger: "logmate",
    module: filename.slice(0, Math.max(filename.length - extname(filename).length, 0)),
    function: match[1] ?? "<anonymous>",
    line: Number(match[3]),
    thread: "main",
  };
}

export class LogMateLogger {
  private readonly capture: ProcessCapture;
  private readonly client: Client;
  private readonly terminal: Terminal;
  private readonly reportedErrors = new Set<string>();
  private closed = false;
  private exceptionMonitor: ((error: Error, origin: NodeJS.UncaughtExceptionOrigin) => void) | undefined;
  private onClose: (() => void) | undefined;

  private constructor(readonly options: ReturnType<typeof resolveConfig>) {
    this.terminal = new Terminal();
    this.capture = new ProcessCapture((message, source) => {
      if (!this.closed && this.client.enabled) {
        this.client.enqueue({
          severity: "UNDEFINED",
          message,
          timestamp: new Date().toISOString(),
          metadata: { captured: true, source },
        });
      }
    });
    this.client = new Client(
      options,
      (message) => this.reportStatus(message),
      (message) => this.reportError(message),
      () => this.terminal.stdoutWrite(`${STARTED_MESSAGE}\n`),
      () => this.capture.restore(),
    );
  }

  static async create(options: InstrumentOptions = {}): Promise<LogMateLogger> {
    const logger = new LogMateLogger(resolveConfig(options));
    await logger.client.initialize();
    if (logger.client.enabled) {
      logger.installExceptionMonitor();
      logger.capture.install(logger.options.captureSystemLogs, logger.options.captureStdin);
    }
    return logger;
  }

  setCloseCallback(callback: () => void): void {
    this.onClose = callback;
  }

  debug(message: unknown, options: LogOptions = {}): void {
    this.write("DEBUG", message, options);
  }

  info(message: unknown, options: LogOptions = {}): void {
    this.write("INFO", message, options);
  }

  warn(message: unknown, options: LogOptions = {}): void {
    this.write("WARN", message, options);
  }

  warning(message: unknown, options: LogOptions = {}): void {
    this.warn(message, options);
  }

  error(message: unknown, options: LogOptions = {}): void {
    this.write("ERROR", message, options);
  }

  fatal(message: unknown, options: LogOptions = {}): void {
    this.write("FATAL", message, options);
  }

  exception(message: unknown, error: unknown, options: LogOptions = {}): void {
    const details = error instanceof Error ? error.stack ?? error.message : String(error);
    this.write("ERROR", message, {
      ...options,
      metadata: { ...safeMetadata(options.metadata), exception: details },
    });
  }

  send(message: unknown, options?: SendOptions): SendResult;
  send(entry: Entry): SendResult;
  send(messageOrEntry: unknown | Entry, options: SendOptions = {}): SendResult {
    try {
      const entry = messageOrEntry && typeof messageOrEntry === "object" && "message" in messageOrEntry
        ? messageOrEntry as Entry
        : { message: String(messageOrEntry), ...options };
      const severity = String(entry.severity ?? "INFO").toUpperCase() as Severity;
      if (!["UNDEFINED", "TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"].includes(severity)) {
        const reason = `unsupported severity ${JSON.stringify(severity)}`;
        this.reportError(reason);
        return { sent: false, queued: false, reason };
      }
      const invalid = validationError(entry.event, entry.eventOccurrenceId);
      const sanitized = invalid
        ? (this.reportError(`${invalid} The invalid field was ignored.`), { metadata: safeMetadata(entry.metadata) })
        : sanitizeOptions(entry, (message) => this.reportError(message));
      const timestamp = entry.timestamp instanceof Date
        ? entry.timestamp.toISOString()
        : entry.timestamp ? new Date(entry.timestamp).toISOString() : new Date().toISOString();
      if (!this.client.enabled) {
        if (severity !== "UNDEFINED" && this.options.console) {
          this.terminal.log(severity, String(entry.message), this.options.level);
        }
        return { sent: false, queued: false, local: true };
      }
      return this.client.enqueue({
        severity,
        message: String(entry.message),
        timestamp,
        metadata: sanitized.metadata ?? {},
        ...(sanitized.event ? { event: sanitized.event } : {}),
        ...(sanitized.eventOccurrenceId ? { event_occurrence_id: sanitized.eventOccurrenceId } : {}),
      });
    } catch (error) {
      const reason = friendlyError(error);
      this.reportError(`could not enqueue the log: ${reason}`);
      return { sent: false, queued: false, reason };
    }
  }

  pendingCount(): number {
    return this.client.pendingCount();
  }

  flush(timeout?: number): Promise<boolean> {
    return this.client.flush(timeout);
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.capture.restore();
    this.closed = true;
    if (this.exceptionMonitor) {
      process.off("uncaughtExceptionMonitor", this.exceptionMonitor);
      this.exceptionMonitor = undefined;
    }
    await this.client.close();
    this.onClose?.();
  }

  private write(severity: Exclude<Severity, "UNDEFINED" | "TRACE">, message: unknown, options: LogOptions): void {
    try {
      if (severityPriority[severity] < severityPriority[this.options.level]) return;
      const sanitized = sanitizeOptions(options, (value) => this.reportError(value));
      const text = String(message);
      if (this.options.console) this.terminal.log(severity, text, this.options.level);
      if (!this.client.enabled) return;
      this.client.enqueue({
        severity,
        message: text,
        timestamp: new Date().toISOString(),
        metadata: { ...sourceMetadata(), ...(sanitized.metadata ?? {}) },
        ...(sanitized.event ? { event: sanitized.event } : {}),
        ...(sanitized.eventOccurrenceId ? { event_occurrence_id: sanitized.eventOccurrenceId } : {}),
      });
    } catch (error) {
      this.reportError(`could not record the message: ${friendlyError(error)}`);
    }
  }

  private installExceptionMonitor(): void {
    this.exceptionMonitor = (error, origin) => {
      if (this.closed || !this.client.enabled) return;
      this.client.enqueue({
        severity: "ERROR",
        message: error.stack ?? error.message,
        timestamp: new Date().toISOString(),
        metadata: { exception: error.message, origin },
      });
    };
    process.on("uncaughtExceptionMonitor", this.exceptionMonitor);
  }

  private reportStatus(message: string): void {
    this.terminal.stderrWrite(`[LogMate] ${message.trim()}\n`);
  }

  private reportError(message: string): void {
    const clean = message.trim() || "an internal failure occurred in the LogMate client.";
    if (this.reportedErrors.has(clean)) return;
    this.reportedErrors.add(clean);
    this.terminal.stderrWrite(`[LogMate] ${clean}\n`);
    try { this.options.errorHandler(new Error(clean)); } catch { /* diagnostics must not crash the app */ }
  }
}

export type { SendOptions };
