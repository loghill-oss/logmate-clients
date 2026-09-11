declare const __LOGMATE_VERSION__: string;

export const VERSION = typeof __LOGMATE_VERSION__ === "string" ? __LOGMATE_VERSION__ : "0.1.0";

export const severities = [
  "UNDEFINED",
  "TRACE",
  "DEBUG",
  "INFO",
  "WARN",
  "ERROR",
  "FATAL",
] as const;

export type Severity = (typeof severities)[number];
export type Metadata = Record<string, unknown>;

export interface LogOptions {
  metadata?: Metadata;
  event?: string;
  eventOccurrenceId?: string;
}

export interface Entry extends LogOptions {
  severity: Severity;
  message: string;
  timestamp?: Date | string;
}

export interface SendResult {
  sent: false;
  queued: boolean;
  queueId?: number;
  local?: true;
  reason?: string;
}

export interface InstrumentOptions {
  name?: string;
  apiUrl?: string;
  senderName?: string;
  envFile?: string;
  level?: Exclude<Severity, "UNDEFINED">;
  console?: boolean;
  healthcheckInterval?: number;
  timeout?: number;
  retryAttempts?: number;
  retryInterval?: number;
  queueFile?: string;
  disablePersistence?: boolean;
  captureSystemLogs?: boolean;
  captureStdin?: boolean;
  shutdownFlushTimeout?: number;
  errorHandler?: (error: Error) => void;
}

export interface ResolvedConfig {
  name: string;
  apiUrl: string;
  senderName: string;
  level: Exclude<Severity, "UNDEFINED">;
  console: boolean;
  healthcheckInterval: number;
  timeout: number;
  retryAttempts: number;
  retryInterval: number;
  queueFile?: string;
  disablePersistence: boolean;
  captureSystemLogs: boolean;
  captureStdin: boolean;
  shutdownFlushTimeout: number;
  errorHandler: (error: Error) => void;
}

export interface QueuedEntry {
  id?: number;
  payload: Record<string, unknown>;
  source: "disk" | "memory";
}
