from __future__ import annotations

import json
import socket
import urllib.error


class _RequestFailure(RuntimeError):
    """Controlled API communication failure."""

    def __init__(self, message: str, *, retryable: bool) -> None:
        super().__init__(message)
        self.retryable = retryable


def _http_error_message(error: urllib.error.HTTPError) -> str:
    """Convert API HTTP responses into short, understandable messages."""
    body = ""
    try:
        body = error.read().decode("utf-8", errors="replace")
    except Exception:
        pass

    code = ""
    api_message = ""
    try:
        parsed = json.loads(body)
        details = parsed.get("error", parsed) if isinstance(parsed, dict) else {}
        if isinstance(details, dict):
            code = str(details.get("code", ""))
            api_message = str(details.get("message", ""))
    except Exception:
        pass

    if error.code == 400:
        return api_message or "the API rejected the submitted data. Check the log fields."
    if error.code == 401 or code in {"INVALID_SENDER_KEY", "INVALID_INSTANCE_TOKEN"}:
        return (
            "the instance credential was rejected. "
            "Check LOGMATE_SENDER_NAME and restart the application to create a new instance."
        )
    if error.code == 403:
        return "the provided key is not allowed to perform this operation."
    if error.code == 404:
        return "the API route was not found. Check LOGMATE_API_URL and the API version."
    if error.code == 408:
        return "the API took too long to process the request."
    if error.code == 409:
        return api_message or "the API encountered a conflict while processing the log."
    if error.code == 413:
        return "the log is too large to send. Reduce the message or metadata."
    if error.code == 422:
        return api_message or "the API could not validate the submitted data."
    if error.code == 429:
        return "the API received too many requests and temporarily rate-limited sends."
    if error.code >= 500:
        return "the LogMate server is unavailable or encountered an internal failure."
    if api_message:
        return api_message
    return f"the API rejected the request with HTTP status {error.code}."


def _friendly_error(error: Exception) -> str:
    """Translate known internal failures into a readable message."""
    if isinstance(error, _RequestFailure):
        return str(error)

    if isinstance(error, urllib.error.HTTPError):
        return _http_error_message(error)

    if isinstance(error, urllib.error.URLError):
        reason = error.reason
        if isinstance(reason, socket.timeout):
            return "the API connection timed out."
        if isinstance(reason, ConnectionRefusedError):
            return "the API connection was refused. Check that the server is running and the port is correct."
        if isinstance(reason, OSError):
            return _friendly_error(reason)
        return "could not connect to the API. Check LOGMATE_API_URL, the network, and the server."

    if isinstance(error, (TimeoutError, socket.timeout)):
        return "the API connection timed out."
    if isinstance(error, ConnectionRefusedError):
        return "the API connection was refused. Check that the server is running and the port is correct."
    if isinstance(error, ConnectionResetError):
        return "the API connection was closed by the server before the response completed."
    if isinstance(error, ConnectionAbortedError):
        return "the API connection was aborted before the request completed."
    if isinstance(error, BrokenPipeError):
        return "the API connection was interrupted while sending."
    if isinstance(error, socket.gaierror):
        return "could not resolve the API address. Check the domain in LOGMATE_API_URL."
    if isinstance(error, json.JSONDecodeError):
        return "the API returned a response that is not valid JSON."
    if isinstance(error, UnicodeError):
        return "the API response contains text with an invalid encoding."
    if isinstance(error, (TypeError, ValueError)):
        return str(error) or "the data provided to the logger is invalid."
    if isinstance(error, KeyError):
        field = str(error).strip("'")
        return f"the API response is missing the required field '{field}'."
    if isinstance(error, OSError):
        text = str(error).strip()
        return (
            f"a network or operating system failure occurred: {text}"
            if text
            else "a network or operating system failure occurred."
        )

    text = str(error).strip()
    return text or f"an internal failure occurred in the LogMate client ({type(error).__name__})."


def _is_retryable_http_status(status: int) -> bool:
    return status in {408, 425, 429} or status >= 500

