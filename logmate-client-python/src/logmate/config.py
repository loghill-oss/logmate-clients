from __future__ import annotations

import os
from pathlib import Path

from .constants import EVENT_RE, NAME_ENVS, URL_ENVS

_URL_ENVS = URL_ENVS
_NAME_ENVS = NAME_ENVS


def _env(*names: str) -> str:
    return next((value for name in names if (value := os.getenv(name, "").strip())), "")


def _load_env(path: Path) -> bool:
    if not path.is_file():
        return False

    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except Exception as error:
        raise RuntimeError(f"could not read the .env file: {error}") from None

    for raw in lines:
        line = raw.strip().removeprefix("export ").strip()
        if not line or line.startswith("#") or "=" not in line:
            continue

        key, value = map(str.strip, line.split("=", 1))
        if len(value) > 1 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        if key:
            os.environ.setdefault(key, value)

    return True


def _config(
    api_url: str | None,
    sender_name: str | None,
    env_file: str | Path | None,
    default_sender_name: str,
) -> tuple[str, str]:
    base = Path(__file__).resolve().parent
    candidates = (
        [Path(env_file)]
        if env_file
        else [Path.cwd() / ".env", base / ".env", base.parent / ".env"]
    )

    for path in candidates:
        if path.is_file():
            _load_env(path)
            break

    if (api_url is None) != (sender_name is None):
        raise ValueError("api_url and sender_name must be provided together.")

    url = (_env(*_URL_ENVS) or api_url or "").rstrip("/")
    configured_name = _env(*_NAME_ENVS) or sender_name or default_sender_name

    if not url:
        return "", ""
    if not url.startswith(("http://", "https://")):
        raise ValueError("LOGMATE_API_URL must start with http:// or https://.")
    configured_name = " ".join(str(configured_name).split())
    if not configured_name or len(configured_name) > 80:
        raise ValueError("LOGMATE_SENDER_NAME must contain between 1 and 80 characters.")

    return url, configured_name


def _validation_error(event: str | None, occurrence_id: str | None) -> str | None:
    if event is not None and not EVENT_RE.fullmatch(event):
        return "invalid event: use only lowercase letters, numbers, '_' or '-', with 3 to 80 characters."
    if occurrence_id is not None and len(occurrence_id) > 200:
        return "invalid event_occurrence_id: the limit is 200 characters."
    return None


