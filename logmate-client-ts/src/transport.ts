import { RequestFailure, friendlyError, httpError } from "./errors.js";

export class Transport {
  constructor(
    private readonly apiUrl: string,
    private readonly timeout: number,
    private readonly credentials: () => { instanceId: string; instanceToken: string },
  ) {}

  async post(
    path: string,
    payload: Record<string, unknown>,
    originInstanceId = "",
  ): Promise<Record<string, unknown>> {
    const { instanceId, instanceToken } = this.credentials();
    const headers: Record<string, string> = {
      Accept: "application/json",
      "Content-Type": "application/json",
    };
    if (instanceId) {
      headers["X-Sender-Instance-ID"] = instanceId;
      headers["X-Sender-Instance-Token"] = instanceToken;
    }
    if (originInstanceId && originInstanceId !== instanceId) {
      headers["X-LogMate-Origin-Instance-ID"] = originInstanceId;
    }

    let body: string;
    try {
      body = JSON.stringify(payload);
    } catch (error) {
      throw new RequestFailure(`could not convert the log to JSON: ${friendlyError(error)}`, false);
    }

    let response: Response;
    try {
      response = await fetch(this.apiUrl + path, {
        method: "POST",
        headers,
        body,
        signal: AbortSignal.timeout(this.timeout),
      });
    } catch (error) {
      throw new RequestFailure(friendlyError(error), true);
    }
    if (!response.ok) throw await httpError(response);
    const text = await response.text();
    if (!text) return {};
    try {
      const parsed: unknown = JSON.parse(text);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new RequestFailure("the API returned JSON, but the content is not an object.", false);
      }
      return parsed as Record<string, unknown>;
    } catch (error) {
      if (error instanceof RequestFailure) throw error;
      throw new RequestFailure("the API returned a response that is not valid JSON.", false);
    }
  }
}
