import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { InstrumentOptions, ResolvedConfig, Severity } from "./types.js";

const LEVELS = new Set<Severity>(["TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"]);

function loadEnv(path: string): void {
  if (!existsSync(path)) return;
  let content: string;
  try {
    content = readFileSync(path, "utf8");
  } catch (error) {
    throw new Error(`could not read the .env file: ${String(error)}`);
  }
  for (const raw of content.split(/\r?\n/)) {
    const line = raw.trim().replace(/^export\s+/, "");
    if (!line || line.startsWith("#") || !line.includes("=")) continue;
    const index = line.indexOf("=");
    const key = line.slice(0, index).trim();
    let value = line.slice(index + 1).trim();
    if (value.length > 1 && value[0] === value.at(-1) && (value[0] === '"' || value[0] === "'")) {
      value = value.slice(1, -1);
    }
    if (key && process.env[key] === undefined) process.env[key] = value;
  }
}

function nonNegative(value: number | undefined, fallback: number): number {
  if (value === undefined) return fallback;
  if (!Number.isFinite(value)) throw new TypeError("LogMate time and retry options must be finite numbers.");
  return Math.max(value, 0);
}

export function resolveConfig(options: InstrumentOptions = {}): ResolvedConfig {
  const envCandidates = options.envFile ? [resolve(options.envFile)] : [resolve(process.cwd(), ".env")];
  for (const candidate of envCandidates) {
    if (existsSync(candidate)) {
      loadEnv(candidate);
      break;
    }
  }

  const hasApiUrl = options.apiUrl !== undefined;
  const hasSenderName = options.senderName !== undefined;
  if (hasApiUrl !== hasSenderName) {
    throw new TypeError("apiUrl and senderName must be provided together.");
  }

  const name = options.name ?? "logmate";
  const apiUrl = (process.env.LOGMATE_API_URL?.trim() || options.apiUrl?.trim() || "").replace(/\/+$/, "");
  const senderName = (process.env.LOGMATE_SENDER_NAME?.trim() || options.senderName || name)
    .trim()
    .replace(/\s+/g, " ");
  if (apiUrl && !/^https?:\/\//.test(apiUrl)) {
    throw new TypeError("LOGMATE_API_URL must start with http:// or https://.");
  }
  if (apiUrl && (!senderName || senderName.length > 80)) {
    throw new TypeError("LOGMATE_SENDER_NAME must contain between 1 and 80 characters.");
  }
  const requestedLevel = (options.level ?? "DEBUG").toUpperCase() as Severity;
  if (!LEVELS.has(requestedLevel)) throw new TypeError(`unsupported log level ${requestedLevel}.`);

  const envShutdown = process.env.LOGMATE_SHUTDOWN_TIMEOUT_SECONDS?.trim();
  const shutdownFlushTimeout = envShutdown
    ? nonNegative(Number(envShutdown), 2)
    : nonNegative(options.shutdownFlushTimeout, 2);
  const queueFile = options.queueFile || process.env.LOGMATE_QUEUE_FILE?.trim();

  return {
    name,
    apiUrl,
    senderName: apiUrl ? senderName : "",
    level: requestedLevel as Exclude<Severity, "UNDEFINED">,
    console: options.console ?? true,
    healthcheckInterval: nonNegative(options.healthcheckInterval, 60),
    timeout: nonNegative(options.timeout, 10),
    retryAttempts: Math.floor(nonNegative(options.retryAttempts, 3)),
    retryInterval: Math.max(nonNegative(options.retryInterval, 5), 0.1),
    ...(queueFile ? { queueFile } : {}),
    disablePersistence: options.disablePersistence ?? false,
    captureSystemLogs: options.captureSystemLogs ?? true,
    captureStdin: options.captureStdin ?? true,
    shutdownFlushTimeout,
    errorHandler: options.errorHandler ?? (() => undefined),
  };
}
