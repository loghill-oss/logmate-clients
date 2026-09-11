from __future__ import annotations

import json
import socket
import urllib.error
import urllib.request
from typing import Any, Mapping

from .errors import (
    _RequestFailure,
    _friendly_error,
    _http_error_message,
    _is_retryable_http_status,
)


class TransportMixin:
    def _post(
        self,
        path: str,
        payload: Mapping[str, Any],
        *,
        origin_instance_id: str = "",
    ) -> dict[str, Any]:
        headers = {
            "Accept": "application/json",
            "Content-Type": "application/json",
        }
        if self.instance_id:
            headers["X-Sender-Instance-ID"] = self.instance_id
            headers["X-Sender-Instance-Token"] = self.instance_token
        if origin_instance_id and origin_instance_id != self.instance_id:
            headers["X-LogMate-Origin-Instance-ID"] = origin_instance_id

        try:
            encoded_payload = json.dumps(
                payload,
                ensure_ascii=False,
                default=str,
            ).encode("utf-8")
        except Exception as error:
            raise _RequestFailure(
                f"could not convert the log to JSON: {error}",
                retryable=False,
            ) from None

        request = urllib.request.Request(
            self.api_url + path,
            encoded_payload,
            headers,
            method="POST",
        )

        try:
            with urllib.request.urlopen(request, timeout=float(self.timeout)) as response:
                body = response.read()
        except urllib.error.HTTPError as error:
            raise _RequestFailure(
                _http_error_message(error),
                retryable=_is_retryable_http_status(error.code),
            ) from None
        except urllib.error.URLError as error:
            raise _RequestFailure(_friendly_error(error), retryable=True) from None
        except (
            TimeoutError,
            socket.timeout,
            ConnectionRefusedError,
            ConnectionResetError,
            ConnectionAbortedError,
            BrokenPipeError,
            socket.gaierror,
            OSError,
        ) as error:
            raise _RequestFailure(_friendly_error(error), retryable=True) from None
        except Exception as error:
            raise _RequestFailure(_friendly_error(error), retryable=False) from None

        if not body:
            return {}

        try:
            decoded = body.decode("utf-8")
            parsed = json.loads(decoded)
        except Exception as error:
            raise _RequestFailure(_friendly_error(error), retryable=False) from None

        if not isinstance(parsed, dict):
            raise _RequestFailure(
                "the API returned JSON, but the content is not an object.",
                retryable=False,
            )
        return parsed

