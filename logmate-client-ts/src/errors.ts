export class RequestFailure extends Error {
  readonly retryable: boolean;

  constructor(message: string, retryable: boolean) {
    super(message);
    this.name = "RequestFailure";
    this.retryable = retryable;
  }
}

export function friendlyError(error: unknown): string {
  if (error instanceof Error) {
    if (error.name === "AbortError" || error.name === "TimeoutError") {
      return "the API connection timed out.";
    }
    const message = error.message.trim();
    const lower = message.toLowerCase();
    if (lower.includes("enotfound") || lower.includes("getaddrinfo")) {
      return "could not resolve the API address. Check the domain in LOGMATE_API_URL.";
    }
    if (lower.includes("econnrefused") || lower.includes("connection refused")) {
      return "the API connection was refused. Check that the server is running and the port is correct.";
    }
    if (message) return message;
  }
  return String(error || "could not connect to the API. Check LOGMATE_API_URL, the network, and the server.");
}

export function isRetryableStatus(status: number): boolean {
  return status === 408 || status === 425 || status === 429 || status >= 500;
}

export async function httpError(response: Response): Promise<RequestFailure> {
  let body: Record<string, unknown> = {};
  try {
    const parsed: unknown = await response.json();
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      body = parsed as Record<string, unknown>;
    }
  } catch {
    // The status is enough to produce a useful error.
  }
  const nested = body.error && typeof body.error === "object"
    ? body.error as Record<string, unknown>
    : {};
  const code = String(nested.code ?? "");
  const apiMessage = String(nested.message ?? body.message ?? "").trim();
  let message = apiMessage;
  if (response.status === 401 || code === "INVALID_SENDER_KEY" || code === "INVALID_INSTANCE_TOKEN") {
    message = "the instance credential was rejected. Check LOGMATE_SENDER_NAME and restart the application to create a new instance.";
  } else if (response.status === 400) {
    message ||= "the API rejected the submitted data. Check the log fields.";
  } else if (response.status === 403) {
    message = "the provided key is not allowed to perform this operation.";
  } else if (response.status === 404) {
    message = "the API route was not found. Check LOGMATE_API_URL and the API version.";
  } else if (response.status === 408) {
    message = "the API took too long to process the request.";
  } else if (response.status === 409) {
    message ||= "the API encountered a conflict while processing the log.";
  } else if (response.status === 413) {
    message = "the log is too large to send. Reduce the message or metadata.";
  } else if (response.status === 422) {
    message ||= "the API could not validate the submitted data.";
  } else if (response.status === 429) {
    message = "the API received too many requests and temporarily rate-limited sends.";
  } else if (response.status >= 500) {
    message = "the LogMate server is unavailable or encountered an internal failure.";
  }
  message ||= `the API rejected the request with HTTP status ${response.status}.`;
  return new RequestFailure(message, isRetryableStatus(response.status));
}
