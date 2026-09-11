import { instrument } from "@loghill-oss/logmate";

const logger = await instrument({
  apiUrl: "http://localhost:8080",
  senderName: "logmate-client-typescript-example",
});

try {
  // Log an info message with metadata
  logger.info("application started", {
    metadata: { environment: "development" },
  });

  // Log a warning message
  logger.warning("This is a warning message.");

  // Log with an event name
  logger.info("A simple event message", {
    event: "simple-event",
  });
} finally {
  await logger.close();
}
