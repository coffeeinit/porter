from __future__ import annotations

from pilot.internal.tasks.args import task_has_secrets as _task_has_secrets


def task_has_secrets(command: str, args: dict | None = None) -> bool:
    """Whether this task was given credentials, so a retry would need fresh ones.
    Reads the recorded args, where a secret is present but redacted."""
    return _task_has_secrets(command, args)
