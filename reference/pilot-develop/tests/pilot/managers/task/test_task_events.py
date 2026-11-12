import json
from datetime import UTC, datetime
from pathlib import Path

from pilot.managers.task.models import TaskInfo, TaskStatus
from pilot.managers.task.reader import (
    done_event,
    output_event,
    sse_message,
    status_event,
)


def test_sse_message_encodes_structured_json_with_event_id() -> None:
    message = sse_message(output_event("build: 50%", overwrite=True), event_id=7)

    lines = message.strip().splitlines()
    assert lines[0] == "id: 7"
    assert json.loads(lines[1].removeprefix("data: ")) == {
        "type": "overwrite",
        "line": "build: 50%",
    }


def test_output_text_cannot_impersonate_completion_event() -> None:
    legacy_marker = "__" + "DONE__:0"
    event = output_event(legacy_marker)

    assert event == {"type": "line", "line": legacy_marker}
    assert json.loads(sse_message(event).removeprefix("data: ")) == event


def test_done_event_keeps_terminal_details() -> None:
    assert done_event("killed", None, None) == {
        "type": "done",
        "status": "killed",
        "exit_code": None,
        "failure": None,
    }


def _task(command: str, status: TaskStatus, queue_position: int | None = None) -> TaskInfo:
    return TaskInfo(
        task_id="20260521-143022-aabbcc",
        command=command,
        args={},
        status=status,
        pid=None,
        queued_at=datetime.now(UTC),
        started_at=None,
        finished_at=None,
        exit_code=None,
        output_path=Path("/tmp/output.log"),
        queue_position=queue_position,
    )


def test_status_event_reports_queue_position() -> None:
    assert status_event(_task("build", TaskStatus.QUEUED, 2)) == {
        "type": "status",
        "status": "queued",
        "queue_position": 2,
        "is_cancellable": True,
    }


def test_status_event_marks_a_running_app_install_uncancellable() -> None:
    """Killing one mid-run leaves doctypes behind that fail every retry, so the
    stream has to tell the UI the moment it starts running."""
    queued = status_event(_task("install-app", TaskStatus.QUEUED))
    running = status_event(_task("install-app", TaskStatus.RUNNING))

    assert queued["is_cancellable"] is True
    assert running["is_cancellable"] is False
