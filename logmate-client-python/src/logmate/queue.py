from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import threading
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Mapping

from .constants import QUEUE_FILE_ENV
from .errors import _friendly_error


class QueueMixin:
    def pending_count(self) -> int:
        """Return the number of logs waiting to be sent."""
        total = 0
        try:
            with self._queue_lock:
                total += len(self._memory_queue)
                if self._persistent_queue_enabled and self._queue_path:
                    with self._queue_connection() as connection:
                        row = connection.execute("SELECT COUNT(*) FROM log_queue").fetchone()
                        total += int(row[0]) if row else 0
        except Exception as error:
            self._report_error(f"could not query the queue: {_friendly_error(error)}")
        return total

    def flush(self, timeout: float | None = None) -> bool:
        """Wait for the queue to drain without exceeding the given timeout."""
        if self.pending_count() == 0:
            self._queue_drained.set()
            return True
        worker = self._worker_thread
        if not self._remote_enabled or worker is None or not worker.is_alive():
            return False
        self._queue_event.set()
        wait_timeout = self.shutdown_flush_timeout if timeout is None else timeout
        return self._queue_drained.wait(timeout=max(float(wait_timeout), 0.0))

    def _resolve_queue_path(self, queue_file: str | Path | None) -> Path:
        configured = queue_file or os.getenv(_QUEUE_FILE_ENV, "").strip()
        if configured:
            return Path(configured).expanduser().resolve()

        queue_identity = f"{self.api_url}|{self.sender_name}"
        key_hash = hashlib.sha256(queue_identity.encode("utf-8")).hexdigest()[:12]
        return Path.home() / ".logmate" / f"queue-{key_hash}.sqlite3"

    def _prepare_queue_storage(self) -> None:
        if not self._queue_path:
            return

        try:
            self._queue_path.parent.mkdir(parents=True, exist_ok=True)
            with self._queue_connection() as connection:
                connection.execute("PRAGMA journal_mode=WAL")
                connection.execute("PRAGMA synchronous=FULL")
                connection.execute(
                    """
                    CREATE TABLE IF NOT EXISTS log_queue (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        payload TEXT NOT NULL,
                        created_at TEXT NOT NULL
                    )
                    """
                )
                connection.commit()
                pending = connection.execute(
                    "SELECT 1 FROM log_queue LIMIT 1"
                ).fetchone()
                if pending:
                    self._queue_drained.clear()
                else:
                    self._queue_drained.set()
            self._persistent_queue_enabled = True
        except Exception as error:
            self._persistent_queue_enabled = False
            self._report_error(
                "could not create the persistent queue; using a temporary in-memory queue: "
                f"{_friendly_error(error)}"
            )

    def _queue_connection(self) -> sqlite3.Connection:
        if not self._queue_path:
            raise RuntimeError("the persistent queue path was not configured.")
        return sqlite3.connect(str(self._queue_path), timeout=5.0)

    def _enqueue_payload(self, payload: Mapping[str, Any]) -> int | None:
        queued_payload = dict(payload)
        if self.sender_id and self.instance_id:
            queued_payload["_logmate_sender_id"] = self.sender_id
            queued_payload["_logmate_instance_id"] = self.instance_id
        serialized = json.dumps(queued_payload, ensure_ascii=False, default=str)

        with self._queue_lock:
            self._queue_drained.clear()
            if self._persistent_queue_enabled:
                try:
                    with self._queue_connection() as connection:
                        cursor = connection.execute(
                            "INSERT INTO log_queue (payload, created_at) VALUES (?, ?)",
                            (serialized, datetime.now(timezone.utc).isoformat()),
                        )
                        connection.commit()
                        queue_id = int(cursor.lastrowid)
                    self._queue_event.set()
                    return queue_id
                except Exception as error:
                    self._report_error(
                        "failed to write to the persistent queue; the log will remain temporarily in memory: "
                        f"{_friendly_error(error)}"
                    )

            self._memory_queue.append(queued_payload)
            self._queue_event.set()
            return None

    def _peek_payload(self) -> tuple[str, int | None, dict[str, Any]] | None:
        with self._queue_lock:
            if self._persistent_queue_enabled:
                try:
                    while True:
                        with self._queue_connection() as connection:
                            row = connection.execute(
                                "SELECT id, payload FROM log_queue ORDER BY id ASC LIMIT 1"
                            ).fetchone()

                        if not row:
                            break

                        queue_id = int(row[0])
                        try:
                            payload = json.loads(str(row[1]))
                            if not isinstance(payload, dict):
                                raise ValueError("the queue content is not a JSON object.")
                            return "disk", queue_id, payload
                        except Exception as error:
                            self._delete_disk_payload(queue_id)
                            self._report_error(
                                f"an invalid record was removed from the persistent queue: {_friendly_error(error)}"
                            )
                except Exception as error:
                    self._report_error(
                        f"could not read the persistent queue: {_friendly_error(error)}"
                    )

            if self._memory_queue:
                return "memory", None, dict(self._memory_queue[0])

        return None

    def _ack_payload(self, source: str, queue_id: int | None) -> None:
        with self._queue_lock:
            if source == "disk" and queue_id is not None:
                self._delete_disk_payload(queue_id)
            elif source == "memory" and self._memory_queue:
                self._memory_queue.popleft()
            self._update_queue_drained_locked()

    def _clear_persistent_queue(self) -> int:
        """Remove all SQLite records when initializing a run."""
        if not self._persistent_queue_enabled:
            return 0

        connection = None
        with self._queue_lock:
            try:
                connection = self._queue_connection()
                row = connection.execute("SELECT COUNT(*) FROM log_queue").fetchone()
                removed = int(row[0] if row else 0)
                connection.execute("DELETE FROM log_queue")
                connection.commit()
                if self._memory_queue:
                    self._queue_drained.clear()
                else:
                    self._queue_drained.set()
                return removed
            except Exception as error:
                self._persistent_queue_enabled = False
                if self._memory_queue:
                    self._queue_drained.clear()
                else:
                    self._queue_drained.set()
                self._report_error(
                    "could not clear the persistent queue during initialization: "
                    f"{_friendly_error(error)}. The SQLite queue will not be used in this run."
                )
                return 0
            finally:
                if connection is not None:
                    connection.close()

    def _update_queue_drained_locked(self) -> None:
        if self._memory_queue:
            self._queue_drained.clear()
            return
        if self._persistent_queue_enabled:
            try:
                with self._queue_connection() as connection:
                    pending = connection.execute(
                        "SELECT 1 FROM log_queue LIMIT 1"
                    ).fetchone()
                if pending:
                    self._queue_drained.clear()
                    return
            except Exception:
                self._queue_drained.clear()
                return
        self._queue_drained.set()

    def _delete_disk_payload(self, queue_id: int) -> None:
        with self._queue_connection() as connection:
            connection.execute("DELETE FROM log_queue WHERE id = ?", (queue_id,))
            connection.commit()

