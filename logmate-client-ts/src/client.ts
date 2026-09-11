import { RequestFailure, friendlyError } from "./errors.js";
import { QueueStore } from "./queue-store.js";
import { Transport } from "./transport.js";
import type { ResolvedConfig, SendResult } from "./types.js";

function delay(seconds: number, referenced = true): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, Math.max(seconds, 0) * 1_000);
    if (!referenced) timer.unref();
  });
}

function responseValue(value: unknown): string {
  if (value === undefined || value === null || value === false || value === 0) return "";
  return String(value);
}

export class Client {
  private readonly queue: QueueStore;
  private readonly transport: Transport;
  private senderId = "";
  private instanceId = "";
  private instanceToken = "";
  private remoteEnabled: boolean;
  private closed = false;
  private worker: Promise<void> | undefined;
  private healthTimer: NodeJS.Timeout | undefined;
  private wakeWorker: (() => void) | undefined;

  constructor(
    readonly config: ResolvedConfig,
    private readonly reportStatus: (message: string) => void,
    private readonly reportError: (message: string) => void,
    private readonly onStarted: () => void,
    private readonly onDisabled: () => void,
  ) {
    this.remoteEnabled = Boolean(config.apiUrl);
    this.queue = new QueueStore({
      apiUrl: config.apiUrl,
      senderName: config.senderName,
      ...(config.queueFile ? { queueFile: config.queueFile } : {}),
      disablePersistence: config.disablePersistence || !this.remoteEnabled,
      report: reportError,
    });
    this.transport = new Transport(config.apiUrl, config.timeout * 1_000, () => ({
      instanceId: this.instanceId,
      instanceToken: this.instanceToken,
    }));
  }

  get enabled(): boolean {
    return this.remoteEnabled && !this.closed;
  }

  async initialize(): Promise<void> {
    if (!this.remoteEnabled) return;
    await this.queue.prepare();
    let retriesDone = 0;
    while (!this.closed && this.remoteEnabled) {
      try {
        await this.initializeInstance();
        this.startWorker();
        return;
      } catch (error) {
        if (error instanceof RequestFailure) {
          if (!error.retryable) {
            this.disableRemote(error.message);
            return;
          }
          if (retriesDone >= this.config.retryAttempts) {
            this.reportStatus(
              "LogMate is unavailable after the initial attempts. The application will continue without further attempts in this run.",
            );
            return;
          }
          retriesDone += 1;
          this.reportStatus(
            `Failed to initialize the API connection. Retry (${retriesDone}/${this.config.retryAttempts}): ${error.message}`,
          );
          await delay(this.config.retryInterval);
          continue;
        }
        this.reportError(`internal failure while initializing LogMate: ${friendlyError(error)}`);
        return;
      }
    }
  }

  enqueue(payload: Record<string, unknown>): SendResult {
    if (!this.enabled) return { sent: false, queued: false, local: true };
    const queuedPayload = { ...payload };
    if (this.senderId && this.instanceId) {
      queuedPayload._logmate_sender_id = this.senderId;
      queuedPayload._logmate_instance_id = this.instanceId;
    }
    try {
      const queueId = this.queue.enqueue(queuedPayload);
      this.wakeWorker?.();
      return {
        sent: false,
        queued: true,
        ...(queueId === undefined ? {} : { queueId }),
      };
    } catch (error) {
      const reason = friendlyError(error);
      this.reportError(`could not enqueue the log: ${reason}`);
      return { sent: false, queued: false, reason };
    }
  }

  pendingCount(): number {
    return this.queue.pendingCount();
  }

  async flush(timeout = this.config.shutdownFlushTimeout): Promise<boolean> {
    if (this.pendingCount() === 0) return true;
    if (!this.enabled || !this.worker) return false;
    this.wakeWorker?.();
    const deadline = Date.now() + Math.max(timeout, 0) * 1_000;
    while (Date.now() <= deadline) {
      if (this.pendingCount() === 0) return true;
      await delay(Math.min(0.025, Math.max((deadline - Date.now()) / 1_000, 0)));
    }
    return this.pendingCount() === 0;
  }

  async close(): Promise<void> {
    if (this.closed) return;
    await this.flush();
    this.closed = true;
    if (this.healthTimer) clearInterval(this.healthTimer);
    this.wakeWorker?.();
    if (this.worker) {
      await Promise.race([this.worker, delay(Math.min(Math.max(this.config.timeout, 0.1), 2))]);
    }
    this.queue.close();
  }

  private async initializeInstance(): Promise<void> {
    if (this.instanceId) return;
    if (this.closed) throw new Error("the logger is already closed.");
    const response = await this.transport.post("/api/v1/instances/init", {
      sender_name: this.config.senderName,
    });
    const senderId = responseValue(response.sender_id ?? response.sender);
    const instanceId = responseValue(response.instance_id);
    const instanceToken = responseValue(response.instance_token);
    if (!senderId || !instanceId || !instanceToken) {
      throw new RequestFailure(
        "the API did not return sender_id, instance_id, and instance_token while initializing the logger.",
        false,
      );
    }
    this.senderId = senderId;
    this.instanceId = instanceId;
    this.instanceToken = instanceToken;
    const discarded = this.queue.clearPersistent();
    if (discarded) this.reportStatus(`${discarded} persisted record(s) were removed from the queue at startup.`);
    this.startHealthcheck();
    this.onStarted();
  }

  private startWorker(): void {
    if (this.worker) return;
    this.worker = this.workerLoop().catch((error: unknown) => {
      this.reportError(`internal failure in the sending worker: ${friendlyError(error)}`);
    });
  }

  private async workerLoop(): Promise<void> {
    let consecutiveFailures = 0;
    let queueNoticeShown = false;
    let offlineAnnounced = false;
    while (!this.closed && this.remoteEnabled) {
      try {
        if (!this.instanceId) {
          await this.initializeInstance();
          consecutiveFailures = 0;
          if (offlineAnnounced) {
            this.reportStatus(`Connection restored. Resending ${this.pendingCount()} pending log(s) in order.`);
          }
          queueNoticeShown = false;
          offlineAnnounced = false;
        }
        const queued = this.queue.peek();
        if (!queued) {
          await this.waitForWake(0.5);
          continue;
        }
        const payload = { ...queued.payload };
        const originSenderId = String(payload._logmate_sender_id ?? this.senderId);
        const originInstanceId = String(payload._logmate_instance_id ?? this.instanceId);
        delete payload._logmate_sender_id;
        delete payload._logmate_instance_id;
        if (originSenderId !== this.senderId) {
          this.queue.acknowledge(queued);
          this.reportStatus("A pending record from another sender was removed from the queue.");
          continue;
        }
        payload.sender_id = originSenderId;
        await this.transport.post("/api/v1/logs", payload, originInstanceId);
        this.queue.acknowledge(queued);
        if (consecutiveFailures || offlineAnnounced) {
          this.reportStatus(`Connection restored. Resending ${this.pendingCount()} pending log(s) in order.`);
        }
        consecutiveFailures = 0;
        queueNoticeShown = false;
        offlineAnnounced = false;
      } catch (error) {
        if (error instanceof RequestFailure) {
          if (!error.retryable) {
            this.disableRemote(error.message);
            return;
          }
          consecutiveFailures += 1;
          if (!queueNoticeShown) {
            this.reportStatus("API connection failed. Logs are being queued for automatic retry.");
            queueNoticeShown = true;
          }
          if (consecutiveFailures <= this.config.retryAttempts) {
            this.reportStatus(`Connection retry (${consecutiveFailures}/${this.config.retryAttempts}): ${error.message}`);
          } else if (!offlineAnnounced) {
            this.reportStatus("LogMate is unavailable. Logs will remain queued and be resent automatically.");
            offlineAnnounced = true;
            this.senderId = "";
            this.instanceId = "";
            this.instanceToken = "";
          }
        } else {
          this.reportError(`internal failure in the sending worker: ${friendlyError(error)}`);
        }
        await this.waitForWake(this.config.retryInterval);
      }
    }
  }

  private waitForWake(seconds: number): Promise<void> {
    return new Promise((resolve) => {
      const timer = setTimeout(finish, seconds * 1_000);
      timer.unref();
      const client = this;
      function finish(): void {
        clearTimeout(timer);
        if (client.wakeWorker === finish) client.wakeWorker = undefined;
        resolve();
      }
      this.wakeWorker = finish;
    });
  }

  private startHealthcheck(): void {
    if (this.healthTimer || this.config.healthcheckInterval <= 0) return;
    this.healthTimer = setInterval(() => void this.healthcheck(), Math.max(this.config.healthcheckInterval, 1) * 1_000);
    this.healthTimer.unref();
  }

  private async healthcheck(): Promise<void> {
    if (!this.senderId || !this.instanceId || this.closed) return;
    try {
      await this.transport.post(`/api/v1/senders/${this.senderId}/health`, {
        status: "healthy",
        details: { client: "typescript-console" },
      });
    } catch (error) {
      this.reportError(
        `healthcheck was not sent; the worker will continue trying to restore the connection: ${friendlyError(error)}`,
      );
    }
  }

  private disableRemote(reason: string): void {
    this.remoteEnabled = false;
    this.onDisabled();
    this.wakeWorker?.();
    this.reportError(
      `${reason} Logs will continue appearing in the terminal. Already persisted records will be kept for the next initialization.`,
    );
  }
}
