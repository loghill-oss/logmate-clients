from __future__ import annotations

import atexit
import hashlib
import io
import json
import logging
import os
import sqlite3
import sys
import threading
import urllib.error
import urllib.request
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Mapping

from .capture import _CapturedFileDescriptor, _CapturedInput, _CapturedStream
from .config import _config, _env, _validation_error
from .constants import (
    ANSI_ESCAPE_RE as _ANSI_ESCAPE_RE,
    QUEUE_FILE_ENV as _QUEUE_FILE_ENV,
)
from .errors import (
    _RequestFailure,
    _friendly_error,
    _http_error_message,
    _is_retryable_http_status,
)
from .queue_store import QueueMixin
from .transport import TransportMixin


_VALID_SEVERITIES = frozenset(
    {"UNDEFINED", "TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}
)


class LogMateLogger(TransportMixin, QueueMixin, logging.Logger):
    """Local logger with optional remote LogMate integration."""

    def __init__(
        self,
        name: str = "logmate",
        *,
        api_url: str | None = None,
        sender_name: str | None = None,
        env_file: str | Path | None = None,
        level: int = logging.DEBUG,
        console: bool = True,
        healthcheck_interval: float = 60.0,
        timeout: float = 10.0,
        retry_attempts: int = 3,
        retry_interval: float = 5.0,
        queue_file: str | Path | None = None,
        capture_system_logs: bool = True,
        capture_stdin: bool = True,
        shutdown_flush_timeout: float = 2.0,
    ) -> None:
        super().__init__(name, level)
        self.api_url = ""
        self.sender_name = ""
        self.healthcheck_interval = healthcheck_interval
        self.timeout = timeout
        self.retry_attempts = max(int(retry_attempts), 0)
        self.retry_interval = max(float(retry_interval), 0.1)
        self.shutdown_flush_timeout = max(float(shutdown_flush_timeout), 0.0)
        self.sender_id = ""
        self.instance_id = ""
        self.instance_token = ""
        self.propagate = False

        self._closed = False
        self._remote_enabled = True
        self._disabled_reason = ""
        self._reported_errors: set[str] = set()
        self._report_lock = threading.Lock()
        self._instance_lock = threading.Lock()
        self._queue_lock = threading.Lock()
        self._stop = threading.Event()
        self._queue_event = threading.Event()
        self._queue_drained = threading.Event()
        self._queue_drained.set()
        self._health_thread: threading.Thread | None = None
        self._worker_thread: threading.Thread | None = None
        self._previous_sys_excepthook = None
        self._previous_threading_excepthook = None
        self._sys_excepthook = None
        self._threading_excepthook = None
        self._queue_path: Path | None = None
        self._persistent_queue_enabled = False
        self._memory_queue: deque[dict[str, Any]] = deque()
        self._capture_system_logs = bool(capture_system_logs)
        self._capture_stdin = bool(capture_stdin)
        self._original_stdout = sys.stdout
        self._original_stderr = sys.stderr
        self._original_stdin = sys.stdin
        self._stdin_capture: _CapturedInput | None = None
        self._stdout_capture: _CapturedStream | None = None
        self._stderr_capture: _CapturedStream | None = None
        self._stdout_fd_capture: _CapturedFileDescriptor | None = None
        self._stderr_fd_capture: _CapturedFileDescriptor | None = None
        self._terminal_stdout = self._original_stdout
        self._terminal_stderr = self._original_stderr
        self._retargeted_stream_handlers: list[tuple[logging.StreamHandler, Any]] = []

        if console:
            try:
                handler = logging.StreamHandler(sys.stdout)
                handler.setLevel(level)
                handler.setFormatter(
                    logging.Formatter(
                        "%(asctime)s [%(levelname)-8s] %(message)s",
                        "%Y-%m-%d %H:%M:%S",
                    )
                )
                self.addHandler(handler)
            except Exception as error:
                self._report_error(
                    f"could not configure terminal output: {_friendly_error(error)}"
                )

        try:
            self.api_url, self.sender_name = _config(api_url, sender_name, env_file, name)
            configured_shutdown_timeout = _env("LOGMATE_SHUTDOWN_TIMEOUT_SECONDS")
            if configured_shutdown_timeout:
                self.shutdown_flush_timeout = max(
                    float(configured_shutdown_timeout), 0.0
                )
            if not self.api_url:
                self._remote_enabled = False
                self._disabled_reason = "LOGMATE_API_URL is not configured; local mode is active."
            else:
                self._queue_path = self._resolve_queue_path(queue_file)
                self._prepare_queue_storage()

                initialized = self._initialize_blocking()

                if initialized and self._remote_enabled:
                    self._start_worker()
        except ValueError:
            raise
        except Exception as error:
            self._disable_remote(_friendly_error(error))

        try:
            atexit.register(self.close)
        except Exception:
            pass

        if self._remote_enabled:
            self._install_exception_hooks()
            self._install_system_log_capture()
            self._install_stdin_capture()

    def _log(
        self,
        level: int,
        msg: object,
        args: tuple[Any, ...],
        exc_info: Any = None,
        extra: dict[str, Any] | None = None,
        stack_info: bool = False,
        stacklevel: int = 1,
        *,
        event: str | None = None,
        event_occurrence_id: str | None = None,
        metadata: Mapping[str, Any] | None = None,
    ) -> None:
        try:
            validation_error = _validation_error(event, event_occurrence_id)
            if validation_error:
                self._report_error(validation_error + " The invalid field was ignored.")
                event = None
                event_occurrence_id = None

            try:
                safe_metadata = dict(metadata or {})
            except Exception:
                safe_metadata = {}
                self._report_error(
                    "invalid metadata: provide a dictionary or another Mapping. The field was ignored."
                )

            super()._log(
                level,
                msg,
                args,
                exc_info=exc_info,
                extra={
                    **(extra or {}),
                    "logmate_event": event,
                    "logmate_occurrence_id": event_occurrence_id,
                    "logmate_metadata": safe_metadata,
                },
                stack_info=stack_info,
                stacklevel=stacklevel + 1,
            )
        except Exception as error:
            self._report_error(f"could not record the message: {_friendly_error(error)}")

    def handle(self, record: logging.LogRecord) -> None:
        try:
            if self.disabled or not self.filter(record):
                return
            if self.handlers or self.propagate:
                self.callHandlers(record)
            if self._remote_enabled:
                self._send_record(record)
        except Exception as error:
            self._report_error(f"could not process the log: {_friendly_error(error)}")

    def send(
        self,
        message: str,
        *,
        severity: str = "INFO",
        event: str | None = None,
        event_occurrence_id: str | None = None,
        metadata: Mapping[str, Any] | None = None,
        timestamp: datetime | None = None,
    ) -> dict[str, Any]:
        """Record locally or enqueue for the API when it is configured."""
        try:
            severity_name = str(severity).upper()
            if severity_name not in _VALID_SEVERITIES:
                reason = f"unsupported severity {severity_name!r}"
                self._report_error(reason)
                return {"sent": False, "queued": False, "reason": reason}

            validation_error = _validation_error(event, event_occurrence_id)
            if validation_error:
                self._report_error(validation_error + " The invalid field was ignored.")
                event = None
                event_occurrence_id = None

            if not self._remote_enabled:
                local_level = {
                    "TRACE": logging.DEBUG,
                    "DEBUG": logging.DEBUG,
                    "INFO": logging.INFO,
                    "WARN": logging.WARNING,
                    "WARNING": logging.WARNING,
                    "ERROR": logging.ERROR,
                    "FATAL": logging.FATAL,
                }.get(severity_name, logging.INFO)
                self.log(local_level, str(message))
                return {"sent": False, "queued": False, "local": True}

            try:
                safe_metadata = dict(metadata or {})
            except Exception:
                safe_metadata = {}
                self._report_error(
                    "invalid metadata: provide a dictionary or another Mapping. The field was ignored."
                )

            payload: dict[str, Any] = {
                "severity": severity_name,
                "message": str(message),
                "timestamp": (timestamp or datetime.now(timezone.utc)).isoformat(),
                "metadata": safe_metadata,
            }
            if event:
                payload["event"] = event
            if event_occurrence_id:
                payload["event_occurrence_id"] = event_occurrence_id

            queue_id = self._enqueue_payload(payload)
            return {
                "sent": False,
                "queued": True,
                "queue_id": queue_id,
            }
        except Exception as error:
            reason = _friendly_error(error)
            self._report_error(f"could not enqueue the log: {reason}")
            return {"sent": False, "queued": False, "reason": reason}

    def close(self) -> None:
        """Stop the threads; records not yet sent remain in SQLite."""
        try:
            if self._closed:
                return
            self._restore_system_log_capture()
            self._restore_stdin_capture()
            if threading.current_thread() is not self._worker_thread:
                self.flush(self.shutdown_flush_timeout)
            self._closed = True
            self._restore_exception_hooks()
            self._stop.set()
            self._queue_event.set()

            current = threading.current_thread()
            for thread in (self._worker_thread, self._health_thread):
                if thread and thread.is_alive() and current is not thread:
                    thread.join(timeout=min(max(float(self.timeout), 0.1), 2.0))
        except Exception as error:
            self._report_error(
                f"could not shut down the logger cleanly: {_friendly_error(error)}"
            )

    def _install_system_log_capture(self) -> None:
        """Capture the real stdout/stderr descriptors used by the whole process."""
        if (
            not self._capture_system_logs
            or self._stdout_capture is not None
            or self._stdout_fd_capture is not None
        ):
            return

        try:
            self._stdout_fd_capture = _CapturedFileDescriptor(
                self._original_stdout, "stdout", self._capture_system_line
            )
            self._terminal_stdout = self._stdout_fd_capture.bypass_stream
            self._stderr_fd_capture = _CapturedFileDescriptor(
                self._original_stderr, "stderr", self._capture_system_line
            )
            self._terminal_stderr = self._stderr_fd_capture.bypass_stream
            sys.stdout = self._stdout_fd_capture.process_stream
            sys.stderr = self._stderr_fd_capture.process_stream

            loggers: list[logging.Logger] = [logging.getLogger(), self]
            loggers.extend(
                logger
                for logger in logging.root.manager.loggerDict.values()
                if isinstance(logger, logging.Logger)
            )
            visited: set[int] = set()
            for logger in loggers:
                for handler in logger.handlers:
                    if id(handler) in visited or not isinstance(handler, logging.StreamHandler):
                        continue
                    visited.add(id(handler))
                    stream = getattr(handler, "stream", None)
                    replacement = None
                    if stream is self._original_stdout or stream is sys.__stdout__:
                        replacement = (
                            self._terminal_stdout
                            if logger is self
                            else self._stdout_fd_capture.process_stream
                        )
                    elif stream is self._original_stderr or stream is sys.__stderr__:
                        replacement = (
                            self._terminal_stderr
                            if logger is self
                            else self._stderr_fd_capture.process_stream
                        )
                    if replacement is not None:
                        handler.setStream(replacement)
                        self._retargeted_stream_handlers.append((handler, stream))
            return
        except (AttributeError, io.UnsupportedOperation, OSError, ValueError):
            self._restore_system_log_capture(flush_pending=False)

        try:
            stdout_capture = _CapturedStream(
                self._original_stdout, "stdout", self._capture_system_line
            )
            stderr_capture = _CapturedStream(
                self._original_stderr, "stderr", self._capture_system_line
            )
            self._stdout_capture = stdout_capture
            self._stderr_capture = stderr_capture
            sys.stdout = stdout_capture
            sys.stderr = stderr_capture

        except Exception as error:
            self._restore_system_log_capture(flush_pending=False)
            self._report_error(
                f"could not capture stdout/stderr: {_friendly_error(error)}"
            )

    def _restore_system_log_capture(self, *, flush_pending: bool = True) -> None:
        stdout_capture = self._stdout_capture
        stderr_capture = self._stderr_capture
        stdout_fd_capture = self._stdout_fd_capture
        stderr_fd_capture = self._stderr_fd_capture
        if (
            stdout_capture is None
            and stderr_capture is None
            and stdout_fd_capture is None
            and stderr_fd_capture is None
        ):
            return

        if flush_pending:
            if stdout_capture is not None:
                stdout_capture.drain()
            if stderr_capture is not None:
                stderr_capture.drain()

        for handler, original in self._retargeted_stream_handlers:
            try:
                stream = getattr(handler, "stream", None)
                if (
                    stream is stdout_capture
                    or stream is stderr_capture
                    or stream is self._terminal_stdout
                    or stream is self._terminal_stderr
                ):
                    handler.setStream(original)
            except Exception:
                pass
        self._retargeted_stream_handlers.clear()

        if (
            stdout_fd_capture is not None
            and sys.stdout is stdout_fd_capture.process_stream
        ):
            sys.stdout = self._original_stdout
        if (
            stderr_fd_capture is not None
            and sys.stderr is stderr_fd_capture.process_stream
        ):
            sys.stderr = self._original_stderr
        if stdout_fd_capture is not None:
            stdout_fd_capture.stop()
        if stderr_fd_capture is not None:
            stderr_fd_capture.stop()

        if stdout_capture is not None and sys.stdout is stdout_capture:
            sys.stdout = self._original_stdout
        if stderr_capture is not None and sys.stderr is stderr_capture:
            sys.stderr = self._original_stderr
        self._stdout_capture = None
        self._stderr_capture = None
        self._stdout_fd_capture = None
        self._stderr_fd_capture = None
        self._terminal_stdout = self._original_stdout
        self._terminal_stderr = self._original_stderr
        if stdout_fd_capture is not None:
            stdout_fd_capture.close_bypass()
        if stderr_fd_capture is not None:
            stderr_fd_capture.close_bypass()

    def _install_stdin_capture(self) -> None:
        if self._capture_stdin and self._stdin_capture is None:
            self._stdin_capture = _CapturedInput(self._original_stdin, self._capture_system_line)
            sys.stdin = self._stdin_capture

    def _restore_stdin_capture(self) -> None:
        if self._stdin_capture is not None and sys.stdin is self._stdin_capture:
            sys.stdin = self._original_stdin
        self._stdin_capture = None

    def _capture_system_line(self, message: str, source: str) -> None:
        if self._closed or not self._remote_enabled:
            return
        message = _ANSI_ESCAPE_RE.sub("", message)
        self.send(
            message,
            severity="UNDEFINED",
            metadata={"captured": True, "source": source},
        )

    def _install_exception_hooks(self) -> None:
        """Capture unhandled tracebacks without replacing the default output."""
        try:
            self._previous_sys_excepthook = sys.excepthook
            self._sys_excepthook = self._handle_unhandled_exception
            sys.excepthook = self._sys_excepthook

            if hasattr(threading, "excepthook"):
                self._previous_threading_excepthook = threading.excepthook
                self._threading_excepthook = self._handle_thread_exception
                threading.excepthook = self._threading_excepthook
        except Exception as error:
            self._report_error(
                f"could not capture unhandled tracebacks: {_friendly_error(error)}"
            )

    def _restore_exception_hooks(self) -> None:
        try:
            if self._sys_excepthook is not None and sys.excepthook is self._sys_excepthook:
                sys.excepthook = self._previous_sys_excepthook or sys.__excepthook__

            if (
                self._threading_excepthook is not None
                and hasattr(threading, "excepthook")
                and threading.excepthook is self._threading_excepthook
            ):
                threading.excepthook = (
                    self._previous_threading_excepthook or threading.__excepthook__
                )
        except Exception:
            pass

    def _handle_unhandled_exception(
        self,
        exc_type: type[BaseException],
        exc_value: BaseException,
        exc_traceback: Any,
    ) -> None:
        try:
            self._send_unhandled_exception(exc_type, exc_value, exc_traceback, thread_name=None)
        finally:
            previous = self._previous_sys_excepthook or sys.__excepthook__
            previous(exc_type, exc_value, exc_traceback)

    def _handle_thread_exception(self, args: threading.ExceptHookArgs) -> None:
        try:
            self._send_unhandled_exception(
                args.exc_type,
                args.exc_value,
                args.exc_traceback,
                thread_name=getattr(args.thread, "name", None),
            )
        finally:
            previous = self._previous_threading_excepthook or threading.__excepthook__
            previous(args)

    def _send_unhandled_exception(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        exc_traceback: Any,
        *,
        thread_name: str | None,
    ) -> None:
        try:
            if exc_type is None or exc_value is None:
                return
            if issubclass(exc_type, (KeyboardInterrupt, SystemExit)):
                return

            message = logging.Formatter().formatException(
                (exc_type, exc_value, exc_traceback)
            )
            self.send(message, severity="ERROR")
        except Exception as error:
            self._report_error(
                f"could not send unhandled traceback: {_friendly_error(error)}"
            )

    def _initialize_blocking(self) -> bool:
        """Initialize the logger while blocking only during the initial attempts.

        The first connection is made immediately. If it fails for a temporary
        reason, ``retry_attempts`` additional attempts are made using
        ``retry_interval``. After that, the application is released without
        starting the remote worker; a new connection is attempted on the next run.
        """
        retries_done = 0

        while not self._stop.is_set() and self._remote_enabled:
            try:
                self._initialize_instance()
                return True

            except _RequestFailure as error:
                if not error.retryable:
                    self._handle_permanent_request_failure(error)
                    return False

                if retries_done >= self.retry_attempts:
                    self._report_status(
                        "LogMate is unavailable after the initial attempts. "
                        "The application will continue without further attempts in this run."
                    )
                    return False

                retries_done += 1
                self._report_status(
                        "Failed to initialize the API connection. "
                        f"Retry ({retries_done}/{self.retry_attempts}): {error}"
                )

                if self._stop.wait(self.retry_interval):
                    return False

            except Exception as error:
                self._report_error(
                    "internal failure while initializing LogMate: "
                    f"{_friendly_error(error)}"
                )
                return False

        return False

    def _start_worker(self) -> None:
        if self._worker_thread and self._worker_thread.is_alive():
            return

        self._worker_thread = threading.Thread(
            target=self._worker_loop,
            name=f"logmate-sender-{self.name}",
            daemon=True,
        )
        self._worker_thread.start()

    def _worker_loop(self) -> None:
        consecutive_failures = 0
        queue_notice_shown = False
        offline_announced = False

        while not self._stop.is_set() and self._remote_enabled:
            try:
                if not self.instance_id:
                    self._initialize_instance()
                    consecutive_failures = 0
                    if offline_announced:
                        self._report_status(
                            f"Connection restored. Resending {self.pending_count()} pending log(s) in order."
                        )
                    queue_notice_shown = False
                    offline_announced = False

                queued = self._peek_payload()
                if queued is None:
                    self._queue_event.clear()
                    self._queue_event.wait(timeout=0.5)
                    continue

                source, queue_id, payload = queued
                api_payload = dict(payload)
                origin_sender_id = str(
                    api_payload.pop("_logmate_sender_id", "") or self.sender_id
                )
                origin_instance_id = str(
                    api_payload.pop("_logmate_instance_id", "") or self.instance_id
                )
                if origin_sender_id != self.sender_id:
                    self._ack_payload(source, queue_id)
                    self._report_status(
                        "A pending record from another sender was removed from the queue."
                    )
                    continue
                api_payload["sender_id"] = origin_sender_id

                self._post(
                    "/api/v1/logs",
                    api_payload,
                    origin_instance_id=origin_instance_id,
                )
                self._ack_payload(source, queue_id)

                if consecutive_failures or offline_announced:
                    self._report_status(
                        f"Connection restored. Resending {self.pending_count()} pending log(s) in order."
                    )
                consecutive_failures = 0
                queue_notice_shown = False
                offline_announced = False

            except _RequestFailure as error:
                if not error.retryable:
                    self._handle_permanent_request_failure(error)
                    return

                consecutive_failures += 1

                if not queue_notice_shown:
                    self._report_status(
                        "API connection failed. Logs are being queued for automatic retry."
                    )
                    queue_notice_shown = True

                if consecutive_failures <= self.retry_attempts:
                    self._report_status(
                        "Connection retry "
                        f"({consecutive_failures}/{self.retry_attempts}): {error}"
                    )
                elif not offline_announced:
                    self._report_status(
                        "LogMate is unavailable. Logs will remain queued and be resent automatically."
                    )
                    offline_announced = True
                    self.instance_id = ""
                    self.sender_id = ""
                    self.instance_token = ""

                self._stop.wait(self.retry_interval)

            except Exception as error:
                self._report_error(
                    f"internal failure in the sending worker: {_friendly_error(error)}"
                )
                self._stop.wait(self.retry_interval)

    def _handle_permanent_request_failure(self, error: _RequestFailure) -> None:
        self._disable_remote(str(error))

    def _initialize_instance(self) -> None:
        if self.instance_id:
            return
        if self._closed:
            raise RuntimeError("the logger is already closed.")

        with self._instance_lock:
            if self.instance_id:
                return

            data = self._post(
                "/api/v1/instances/init",
                {"sender_name": self.sender_name},
            )
            if not isinstance(data, dict):
                raise _RequestFailure(
                    "the API returned an invalid response while initializing the logger.",
                    retryable=False,
                )

            instance_id = data.get("instance_id")
            sender_id = data.get("sender_id") or data.get("sender")
            instance_token = data.get("instance_token")
            if not instance_id or not sender_id or not instance_token:
                raise _RequestFailure(
                    "the API did not return sender_id, instance_id, and instance_token while initializing the logger.",
                    retryable=False,
                )

            self.instance_id = str(instance_id)
            self.sender_id = str(sender_id)
            self.instance_token = str(instance_token)
            discarded = self._clear_persistent_queue()
            if discarded:
                self._report_status(
                    f"{discarded} persisted record(s) were removed from the queue at startup."
                )
            self._start_health_thread()
            self._report_started()

    def _start_health_thread(self) -> None:
        if self._health_thread and self._health_thread.is_alive():
            return

        self._health_thread = threading.Thread(
            target=self._health_loop,
            name=f"logmate-health-{self.sender_id}",
            daemon=True,
        )
        self._health_thread.start()

    def _send_record(self, record: logging.LogRecord) -> None:
        try:
            custom_metadata = getattr(record, "logmate_metadata", {})
            if not isinstance(custom_metadata, Mapping):
                custom_metadata = {}

            metadata = {
                "logger": record.name,
                "module": record.module,
                "function": record.funcName,
                "line": record.lineno,
                "thread": record.threadName,
                **dict(custom_metadata),
            }
            if record.exc_info:
                try:
                    metadata["exception"] = logging.Formatter().formatException(record.exc_info)
                except Exception:
                    metadata["exception"] = "could not format the exception."

            self.send(
                record.getMessage(),
                severity=self._severity(record.levelno),
                event=getattr(record, "logmate_event", None),
                event_occurrence_id=getattr(record, "logmate_occurrence_id", None),
                metadata=metadata,
                timestamp=datetime.fromtimestamp(record.created, timezone.utc),
            )
        except Exception as error:
            self._report_error(f"log was not queued: {_friendly_error(error)}")

    def _health_loop(self) -> None:
        try:
            interval = max(float(self.healthcheck_interval), 1.0)
        except Exception:
            interval = 60.0
            self._report_error("invalid healthcheck_interval; using 60 seconds.")

        while not self._stop.wait(interval):
            try:
                if not self.instance_id or not self.sender_id:
                    continue
                self._post(
                    f"/api/v1/senders/{self.sender_id}/health",
                    {"status": "healthy", "details": {"client": "python-logging"}},
                )
            except _RequestFailure as error:
                if not error.retryable:
                    self._report_error(f"healthcheck rejeitado: {error}")
                else:
                    self._report_error(
                        "healthcheck was not sent; the worker will continue trying to restore the connection: "
                        f"{error}"
                    )
            except Exception as error:
                self._report_error(f"healthcheck was not sent: {_friendly_error(error)}")

    def _disable_remote(self, reason: str) -> None:
        self._remote_enabled = False
        self._restore_system_log_capture(flush_pending=False)
        self._disabled_reason = reason
        self._stop.set()
        self._queue_event.set()
        self._report_error(
            f"{reason} Logs will continue appearing in the terminal. "
            "Already persisted records will be kept for the next initialization."
        )

    def _report_started(self) -> None:
        """Report only after the API successfully initializes the instance."""
        try:
            print("[LogMate] Initialized successfully!", file=self._terminal_stdout, flush=True)
        except Exception:
            pass

    def _report_status(self, message: str) -> None:
        """Display state changes and allow them to be reported again later."""
        try:
            print(f"[LogMate] {str(message).strip()}", file=self._terminal_stderr, flush=True)
        except Exception:
            pass

    def _report_error(self, message: str) -> None:
        try:
            message = str(message).strip() or "an internal failure occurred in the LogMate client."
            with self._report_lock:
                if message in self._reported_errors:
                    return
                self._reported_errors.add(message)
            print(f"[LogMate] {message}", file=self._terminal_stderr, flush=True)
        except Exception:
            pass

    @staticmethod
    def _severity(level: int) -> str:
        try:
            for minimum, name in (
                (logging.FATAL, "FATAL"),
                (logging.ERROR, "ERROR"),
                (logging.WARNING, "WARN"),
                (logging.INFO, "INFO"),
                (logging.DEBUG, "DEBUG"),
            ):
                if level >= minimum:
                    return name
        except Exception:
            pass
        return "TRACE"


def _build_logger(**kwargs: Any) -> LogMateLogger:
    """Return a ready logger while preserving configuration errors."""
    try:
        return LogMateLogger(**kwargs)
    except ValueError:
        raise
    except Exception as error:
        try:
            name = str(kwargs.get("name", "logmate"))
            level = kwargs.get("level", logging.DEBUG)
            logger = LogMateLogger(
                name=name,
                level=level,
                console=bool(kwargs.get("console", True)),
            )
            logger._disable_remote(f"could not create the logger: {_friendly_error(error)}")
            return logger
        except Exception:
            logger = LogMateLogger.__new__(LogMateLogger)
            logging.Logger.__init__(logger, "logmate", logging.DEBUG)
            logger.propagate = False
            handler = logging.StreamHandler(sys.stdout)
            handler.setFormatter(
                logging.Formatter("%(asctime)s [%(levelname)-8s] %(message)s")
            )
            logger.addHandler(handler)
            logger.api_url = ""
            logger.sender_name = ""
            logger.healthcheck_interval = 60.0
            logger.timeout = 10.0
            logger.retry_attempts = 3
            logger.retry_interval = 5.0
            logger.shutdown_flush_timeout = 2.0
            logger.sender_id = ""
            logger.instance_id = ""
            logger.instance_token = ""
            logger._closed = False
            logger._remote_enabled = False
            logger._disabled_reason = "the LogMate client could not be initialized."
            logger._reported_errors = set()
            logger._report_lock = threading.Lock()
            logger._instance_lock = threading.Lock()
            logger._queue_lock = threading.Lock()
            logger._stop = threading.Event()
            logger._queue_event = threading.Event()
            logger._queue_drained = threading.Event()
            logger._queue_drained.set()
            logger._health_thread = None
            logger._worker_thread = None
            logger._previous_sys_excepthook = None
            logger._previous_threading_excepthook = None
            logger._sys_excepthook = None
            logger._threading_excepthook = None
            logger._queue_path = None
            logger._persistent_queue_enabled = False
            logger._memory_queue = deque()
            logger._capture_system_logs = False
            logger._capture_stdin = False
            logger._original_stdout = sys.stdout
            logger._original_stderr = sys.stderr
            logger._original_stdin = sys.stdin
            logger._stdin_capture = None
            logger._stdout_capture = None
            logger._stderr_capture = None
            logger._stdout_fd_capture = None
            logger._stderr_fd_capture = None
            logger._terminal_stdout = logger._original_stdout
            logger._terminal_stderr = logger._original_stderr
            logger._retargeted_stream_handlers = []
            logger._report_error(
                "the LogMate client could not be initialized. Using the terminal only."
            )
            return logger


_instrument_lock = threading.RLock()
_instrumented_logger: LogMateLogger | None = None
_instrumented_pid: int | None = None


def instrument(
    name: str = "logmate",
    *,
    api_url: str | None = None,
    sender_name: str | None = None,
    env_file: str | Path | None = None,
    level: int = logging.DEBUG,
    console: bool = True,
    healthcheck_interval: float = 60.0,
    timeout: float = 10.0,
    retry_attempts: int = 3,
    retry_interval: float = 5.0,
    queue_file: str | Path | None = None,
    capture_system_logs: bool = True,
    capture_stdin: bool = True,
    shutdown_flush_timeout: float = 2.0,
) -> LogMateLogger:
    """Instrument stdout/stderr once per process and return the logger."""
    global _instrumented_logger, _instrumented_pid

    process_id = os.getpid()
    with _instrument_lock:
        current = _instrumented_logger
        if (
            current is not None
            and _instrumented_pid == process_id
            and not current._closed
        ):
            return current

        if current is not None and _instrumented_pid != process_id:
            try:
                current._restore_system_log_capture(flush_pending=False)
                current._closed = True
                current._stop.set()
                current._queue_event.set()
            except Exception:
                pass

        logger = _build_logger(
            name=name,
            api_url=api_url,
            sender_name=sender_name,
            env_file=env_file,
            level=level,
            console=console,
            healthcheck_interval=healthcheck_interval,
            timeout=timeout,
            retry_attempts=retry_attempts,
            retry_interval=retry_interval,
            queue_file=queue_file,
            capture_system_logs=capture_system_logs,
            capture_stdin=capture_stdin,
            shutdown_flush_timeout=shutdown_flush_timeout,
        )
        _instrumented_logger = logger
        _instrumented_pid = process_id
        return logger


def create_logger(
    name: str = "logmate",
    *,
    api_url: str | None = None,
    sender_name: str | None = None,
    env_file: str | Path | None = None,
    level: int = logging.DEBUG,
    console: bool = True,
    healthcheck_interval: float = 60.0,
    timeout: float = 10.0,
    retry_attempts: int = 3,
    retry_interval: float = 5.0,
    queue_file: str | Path | None = None,
    capture_system_logs: bool = True,
    capture_stdin: bool = True,
    shutdown_flush_timeout: float = 2.0,
) -> LogMateLogger:
    """Compatible alias for :func:`instrument`, with one instance per process."""
    return instrument(
        name=name,
        api_url=api_url,
        sender_name=sender_name,
        env_file=env_file,
        level=level,
        console=console,
        healthcheck_interval=healthcheck_interval,
        timeout=timeout,
        retry_attempts=retry_attempts,
        retry_interval=retry_interval,
        queue_file=queue_file,
        capture_system_logs=capture_system_logs,
        capture_stdin=capture_stdin,
        shutdown_flush_timeout=shutdown_flush_timeout,
    )
