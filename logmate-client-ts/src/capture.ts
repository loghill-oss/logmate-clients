import type { ReadStream, WriteStream } from "node:tty";
import { ANSI_ESCAPE_PATTERN } from "./constants.js";

type AnyFunction = (...args: any[]) => any;

export class ProcessCapture {
  private readonly buffers = { stdout: "", stderr: "", stdin: "" };
  private readonly originalStdoutWrite: AnyFunction;
  private readonly originalStderrWrite: AnyFunction;
  private readonly originalStdinEmit: AnyFunction;
  private installed = false;

  constructor(private readonly emitLine: (message: string, source: string) => void) {
    this.originalStdoutWrite = process.stdout.write;
    this.originalStderrWrite = process.stderr.write;
    this.originalStdinEmit = process.stdin.emit;
  }

  install(captureSystemLogs: boolean, captureStdin: boolean): void {
    if (this.installed) return;
    this.installed = true;
    if (captureSystemLogs) {
      this.patchWrite(process.stdout, "stdout", this.originalStdoutWrite);
      this.patchWrite(process.stderr, "stderr", this.originalStderrWrite);
    }
    if (captureStdin) {
      const capture = this;
      (process.stdin as ReadStream & { emit: AnyFunction }).emit = function (event: string, ...args: unknown[]) {
        if (event === "data" && args[0] !== undefined) capture.consume(String(args[0]), "stdin");
        return Reflect.apply(capture.originalStdinEmit, this, [event, ...args]);
      };
    }
  }

  restore(): void {
    if (!this.installed) return;
    this.drain();
    (process.stdout as WriteStream & { write: AnyFunction }).write = this.originalStdoutWrite;
    (process.stderr as WriteStream & { write: AnyFunction }).write = this.originalStderrWrite;
    (process.stdin as ReadStream & { emit: AnyFunction }).emit = this.originalStdinEmit;
    this.installed = false;
  }

  private patchWrite(stream: WriteStream, source: "stdout" | "stderr", original: AnyFunction): void {
    const capture = this;
    (stream as WriteStream & { write: AnyFunction }).write = function (chunk: unknown, ...args: unknown[]) {
      const result = Reflect.apply(original, this, [chunk, ...args]);
      capture.consume(Buffer.isBuffer(chunk) ? chunk.toString() : String(chunk), source);
      return result;
    };
  }

  private consume(value: string, source: "stdout" | "stderr" | "stdin"): void {
    const parts = (this.buffers[source] + value).split("\n");
    this.buffers[source] = parts.pop() ?? "";
    for (const line of parts) this.emit(line.replace(/\r$/, ""), source);
  }

  private drain(): void {
    for (const source of ["stdout", "stderr", "stdin"] as const) {
      this.emit(this.buffers[source].replace(/\r$/, ""), source);
      this.buffers[source] = "";
    }
  }

  private emit(line: string, source: string): void {
    const clean = line.replace(ANSI_ESCAPE_PATTERN, "");
    if (clean.trim()) this.emitLine(clean, source);
  }
}
