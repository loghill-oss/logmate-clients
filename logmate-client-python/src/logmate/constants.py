import re


EVENT_RE = re.compile(r"^[a-z0-9][a-z0-9_-]{2,79}$")
URL_ENVS = ("LOGMATE_API_URL",)
NAME_ENVS = ("LOGMATE_SENDER_NAME",)
QUEUE_FILE_ENV = "LOGMATE_QUEUE_FILE"
ANSI_ESCAPE_RE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
