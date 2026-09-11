import { EVENT_PATTERN } from "./constants.js";
import type { LogOptions, Metadata } from "./types.js";

export function validationError(event?: string, occurrenceId?: string): string | undefined {
  if (event !== undefined && !EVENT_PATTERN.test(event)) {
    return "invalid event: use only lowercase letters, numbers, '_' or '-', with 3 to 80 characters.";
  }
  if (occurrenceId !== undefined && occurrenceId.length > 200) {
    return "invalid eventOccurrenceId: the limit is 200 characters.";
  }
  return undefined;
}

export function safeMetadata(metadata: unknown): Metadata {
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) return {};
  try {
    return JSON.parse(JSON.stringify(metadata, (_key, value: unknown) => {
      if (typeof value === "bigint") return value.toString();
      if (typeof value === "function" || typeof value === "symbol") return String(value);
      return value;
    })) as Metadata;
  } catch {
    return Object.fromEntries(Object.entries(metadata as Metadata).map(([key, value]) => [key, String(value)]));
  }
}

export function sanitizeOptions(options: LogOptions, report: (message: string) => void): LogOptions {
  const error = validationError(options.event, options.eventOccurrenceId);
  if (error) {
    report(`${error} The invalid field was ignored.`);
    return { metadata: safeMetadata(options.metadata) };
  }
  return {
    metadata: safeMetadata(options.metadata),
    ...(options.event ? { event: options.event } : {}),
    ...(options.eventOccurrenceId ? { eventOccurrenceId: options.eventOccurrenceId } : {}),
  };
}
