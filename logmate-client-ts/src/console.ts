import { severityPriority } from "./constants.js";
import type { Severity } from "./types.js";

type Write = typeof process.stdout.write;

export class Terminal {
  readonly stdoutWrite: Write;
  readonly stderrWrite: Write;

  constructor() {
    this.stdoutWrite = process.stdout.write.bind(process.stdout);
    this.stderrWrite = process.stderr.write.bind(process.stderr);
  }

  log(severity: Exclude<Severity, "UNDEFINED">, message: string, minimum: Exclude<Severity, "UNDEFINED">): void {
    if (severityPriority[severity] < severityPriority[minimum]) return;
    const label = severity === "WARN" ? "WARNING" : severity === "FATAL" ? "CRITICAL" : severity;
    const now = new Date();
    const timestamp = [
      now.getFullYear(),
      String(now.getMonth() + 1).padStart(2, "0"),
      String(now.getDate()).padStart(2, "0"),
    ].join("-") + " " + [
      String(now.getHours()).padStart(2, "0"),
      String(now.getMinutes()).padStart(2, "0"),
      String(now.getSeconds()).padStart(2, "0"),
    ].join(":");
    this.stdoutWrite(`${timestamp} [${label.padEnd(8)}] ${message}\n`);
  }

  status(message: string): void {
    this.stdoutWrite(`[LogMate] ${message}\n`);
  }

  error(message: string): void {
    this.stderrWrite(`[LogMate] ${message}\n`);
  }
}
