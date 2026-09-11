from __future__ import annotations

import codecs
import os
import threading
from typing import Any


class _CapturedStream:
    """Preserve the original stream and forward each complete line to LogMate."""

    def __init__(self, stream: Any, source: str, callback: Any) -> None:
        self._stream = stream
        self._source = source
        self._callback = callback
        self._buffers: dict[int, str] = {}
        self._lock = threading.RLock()

    def write(self, value: object) -> int:
        text = str(value)
        written = self._stream.write(text)
        if not text:
            return int(written or 0)

        thread_id = threading.get_ident()
        with self._lock:
            buffered = self._buffers.get(thread_id, "") + text
            parts = buffered.split("\n")
            self._buffers[thread_id] = parts.pop()
        for line in parts:
            self._emit(line.removesuffix("\r"))
        return int(written if written is not None else len(text))

    def flush(self) -> None:
        self._stream.flush()
        thread_id = threading.get_ident()
        with self._lock:
            pending = self._buffers.pop(thread_id, "")
        self._emit(pending.removesuffix("\r"))

    def drain(self) -> None:
        with self._lock:
            pending = list(self._buffers.values())
            self._buffers.clear()
        for line in pending:
            self._emit(line.removesuffix("\r"))

    def _emit(self, line: str) -> None:
        if not line.strip():
            return
        try:
            self._callback(line, self._source)
        except Exception:
            pass

    def __getattr__(self, name: str) -> Any:
        return getattr(self._stream, name)


class _CapturedInput:
    """Preserve stdin and report complete lines read by the application."""

    def __init__(self, stream: Any, callback: Any) -> None:
        self._stream = stream
        self._callback = callback

    def readline(self, *args: Any, **kwargs: Any) -> Any:
        value = self._stream.readline(*args, **kwargs)
        self._emit(value)
        return value

    def read(self, *args: Any, **kwargs: Any) -> Any:
        value = self._stream.read(*args, **kwargs)
        self._emit(value)
        return value

    def _emit(self, value: object) -> None:
        text = str(value).rstrip("\r\n")
        if text.strip():
            try:
                self._callback(text, "stdin")
            except Exception:
                pass

    def __getattr__(self, name: str) -> Any:
        return getattr(self._stream, name)


class _CapturedFileDescriptor:
    """Tee the process file descriptor without depending on a logging framework."""

    def __init__(self, stream: Any, source: str, callback: Any) -> None:
        self._stream = stream
        self._source = source
        self._callback = callback
        self._fd = int(stream.fileno())
        self._saved_fd = -1
        self._read_fd = -1
        self._thread: threading.Thread | None = None
        self.bypass_stream: Any = None
        self.process_stream: Any = None

        try:
            stream.flush()
            self._saved_fd = os.dup(self._fd)
            read_fd, write_fd = os.pipe()
            self._read_fd = read_fd
            try:
                os.dup2(write_fd, self._fd)
            finally:
                os.close(write_fd)

            encoding = getattr(stream, "encoding", None) or "utf-8"
            errors = getattr(stream, "errors", None) or "replace"
            bypass_fd = os.dup(self._saved_fd)
            try:
                if os.name == "nt":
                    import msvcrt

                    msvcrt.setmode(bypass_fd, os.O_BINARY)
                self.bypass_stream = os.fdopen(
                    bypass_fd,
                    "w",
                    buffering=1,
                    encoding=encoding,
                    errors=errors,
                    closefd=True,
                )
            except Exception:
                os.close(bypass_fd)
                raise
            process_fd = os.dup(self._fd)
            try:
                if os.name == "nt":
                    import msvcrt

                    msvcrt.setmode(process_fd, os.O_BINARY)
                self.process_stream = os.fdopen(
                    process_fd,
                    "w",
                    buffering=1,
                    encoding=encoding,
                    errors=errors,
                    closefd=True,
                )
            except Exception:
                os.close(process_fd)
                raise
            self._thread = threading.Thread(
                target=self._read_loop,
                name=f"logmate-terminal-{source}",
                daemon=True,
            )
            self._thread.start()
        except Exception:
            if self._saved_fd >= 0:
                try:
                    os.dup2(self._saved_fd, self._fd)
                except Exception:
                    pass
                try:
                    os.close(self._saved_fd)
                except Exception:
                    pass
                self._saved_fd = -1
            if self._read_fd >= 0:
                try:
                    os.close(self._read_fd)
                except Exception:
                    pass
                self._read_fd = -1
            try:
                if self.process_stream is not None:
                    self.process_stream.close()
            except Exception:
                pass
            self.process_stream = None
            self.close_bypass()
            raise

    def stop(self) -> None:
        if self._saved_fd < 0:
            return
        try:
            if self.process_stream is not None:
                self.process_stream.flush()
                self.process_stream.close()
        except Exception:
            pass
        self.process_stream = None
        os.dup2(self._saved_fd, self._fd)
        if self._thread and self._thread is not threading.current_thread():
            self._thread.join(timeout=2.0)
        os.close(self._saved_fd)
        self._saved_fd = -1

    def close_bypass(self) -> None:
        try:
            if self.bypass_stream is not None:
                self.bypass_stream.close()
        except Exception:
            pass
        self.bypass_stream = None

    def _read_loop(self) -> None:
        encoding = getattr(self._stream, "encoding", None) or "utf-8"
        errors = getattr(self._stream, "errors", None) or "replace"
        decoder = codecs.getincrementaldecoder(encoding)(errors=errors)
        buffered = ""
        try:
            while True:
                chunk = os.read(self._read_fd, 8192)
                if not chunk:
                    break
                text = decoder.decode(chunk)
                if self.bypass_stream is not None:
                    self.bypass_stream.write(text)
                    self.bypass_stream.flush()
                buffered += text
                parts = buffered.split("\n")
                buffered = parts.pop()
                for line in parts:
                    self._emit(line.removesuffix("\r"))
            buffered += decoder.decode(b"", final=True)
            self._emit(buffered.removesuffix("\r"))
        except Exception:
            pass
        finally:
            if self._read_fd >= 0:
                try:
                    os.close(self._read_fd)
                except Exception:
                    pass
                self._read_fd = -1

    def _emit(self, line: str) -> None:
        if not line.strip():
            return
        try:
            self._callback(line, self._source)
        except Exception:
            pass


