export const EVENT_PATTERN = /^[a-z0-9][a-z0-9_-]{2,79}$/;
export const ANSI_ESCAPE_PATTERN = /\x1b\[[0-?]*[ -/]*[@-~]/g;
export const STARTED_MESSAGE = "[LogMate] Initialized successfully!";

export const severityPriority = {
  TRACE: 5,
  DEBUG: 10,
  INFO: 20,
  WARN: 30,
  ERROR: 40,
  FATAL: 50,
} as const;
