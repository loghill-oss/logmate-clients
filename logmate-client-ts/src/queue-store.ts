import { createHash } from "node:crypto";
import { mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, resolve } from "node:path";
import type { DatabaseSync } from "node:sqlite";
import type { QueuedEntry } from "./types.js";

interface Row {
  id: number;
  payload: string;
}

export class QueueStore {
  private database: DatabaseSync | undefined;
  private readonly memory: Array<Record<string, unknown>> = [];
  private readonly report: (message: string) => void;
  private readonly queueFile: string;
  private readonly disablePersistence: boolean;

  constructor(options: {
    apiUrl: string;
    senderName: string;
    queueFile?: string;
    disablePersistence: boolean;
    report: (message: string) => void;
  }) {
    this.report = options.report;
    this.disablePersistence = options.disablePersistence;
    const identity = `${options.apiUrl}|${options.senderName}`;
    const hash = createHash("sha256").update(identity).digest("hex").slice(0, 12);
    this.queueFile = resolve(options.queueFile ?? `${homedir()}/.logmate/queue-${hash}.sqlite3`);
  }

  async prepare(): Promise<void> {
    if (this.disablePersistence || this.database) return;
    try {
      mkdirSync(dirname(this.queueFile), { recursive: true });
      const originalEmitWarning = process.emitWarning;
      process.emitWarning = function (warning: string | Error, ...args: unknown[]): void {
        const message = warning instanceof Error ? warning.message : String(warning);
        const warningType = typeof args[0] === "string"
          ? args[0]
          : args[0] && typeof args[0] === "object" && "type" in args[0]
            ? String((args[0] as { type?: unknown }).type)
            : "";
        if (message === "SQLite is an experimental feature and might change at any time" && warningType === "ExperimentalWarning") {
          return;
        }
        Reflect.apply(originalEmitWarning, process, [warning, ...args]);
      } as typeof process.emitWarning;
      let sqlite: typeof import("node:sqlite");
      try {
        sqlite = await import("node:sqlite");
      } finally {
        process.emitWarning = originalEmitWarning;
      }
      const database = new sqlite.DatabaseSync(this.queueFile, { timeout: 5_000 });
      database.exec("PRAGMA journal_mode=WAL");
      database.exec("PRAGMA synchronous=FULL");
      database.exec(`
        CREATE TABLE IF NOT EXISTS log_queue (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          payload TEXT NOT NULL,
          created_at TEXT NOT NULL
        )
      `);
      this.database = database;
    } catch (error) {
      this.report(`could not create the persistent queue; using a temporary in-memory queue: ${String(error)}`);
    }
  }

  enqueue(payload: Record<string, unknown>): number | undefined {
    const serialized = JSON.stringify(payload);
    if (this.database) {
      try {
        const result = this.database
          .prepare("INSERT INTO log_queue (payload, created_at) VALUES (?, ?)")
          .run(serialized, new Date().toISOString());
        return Number(result.lastInsertRowid);
      } catch (error) {
        this.report(`failed to write to the persistent queue; the log will remain temporarily in memory: ${String(error)}`);
      }
    }
    this.memory.push(payload);
    return undefined;
  }

  peek(): QueuedEntry | undefined {
    if (this.database) {
      for (;;) {
        try {
          const row = this.database
            .prepare("SELECT id, payload FROM log_queue ORDER BY id ASC LIMIT 1")
            .get() as Row | undefined;
          if (!row) break;
          try {
            const parsed: unknown = JSON.parse(row.payload);
            if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
              throw new Error("the queue content is not a JSON object.");
            }
            return { id: row.id, payload: parsed as Record<string, unknown>, source: "disk" };
          } catch (error) {
            this.database.prepare("DELETE FROM log_queue WHERE id = ?").run(row.id);
            this.report(`an invalid record was removed from the persistent queue: ${String(error)}`);
          }
        } catch (error) {
          this.report(`could not read the persistent queue: ${String(error)}`);
          break;
        }
      }
    }
    const payload = this.memory[0];
    return payload ? { payload, source: "memory" } : undefined;
  }

  acknowledge(entry: QueuedEntry): void {
    if (entry.source === "disk" && entry.id !== undefined && this.database) {
      this.database.prepare("DELETE FROM log_queue WHERE id = ?").run(entry.id);
    } else if (entry.source === "memory") {
      this.memory.shift();
    }
  }

  pendingCount(): number {
    let total = this.memory.length;
    if (this.database) {
      try {
        const row = this.database.prepare("SELECT COUNT(*) AS count FROM log_queue").get() as { count: number };
        total += Number(row.count);
      } catch (error) {
        this.report(`could not query the queue: ${String(error)}`);
      }
    }
    return total;
  }

  clearPersistent(): number {
    if (!this.database) return 0;
    try {
      const row = this.database.prepare("SELECT COUNT(*) AS count FROM log_queue").get() as { count: number };
      const removed = Number(row.count);
      this.database.exec("DELETE FROM log_queue");
      return removed;
    } catch (error) {
      this.report(`could not clear the persistent queue during initialization: ${String(error)}. The SQLite queue will not be used in this run.`);
      try { this.database.close(); } catch { /* already unusable */ }
      this.database = undefined;
      return 0;
    }
  }

  close(): void {
    try { this.database?.close(); } finally { this.database = undefined; }
  }
}
