import { LogMateLogger } from "./logger.js";
import type { InstrumentOptions } from "./types.js";

let instrumented: Promise<LogMateLogger> | undefined;

export async function instrument(options: InstrumentOptions = {}): Promise<LogMateLogger> {
  if (instrumented) return instrumented;
  const promise = LogMateLogger.create(options);
  instrumented = promise;
  try {
    const logger = await promise;
    logger.setCloseCallback(() => {
      if (instrumented === promise) instrumented = undefined;
    });
    return logger;
  } catch (error) {
    if (instrumented === promise) instrumented = undefined;
    throw error;
  }
}

export const createLogger = instrument;
