import logging
from types import SimpleNamespace
from unittest.mock import patch

from pilot.core.notification import NotificationStore
from pilot.core.notification.events import record_alert, task_failed


def _feed(tmp_path):
    return NotificationStore(tmp_path / "logs").list(10)


def test_task_failure_lands_under_the_matching_category(tmp_path) -> None:
    task_failed(tmp_path, {"task_id": "t1", "command": "backup-site", "args": {"site": "a.local"}})

    (item,) = _feed(tmp_path)
    assert item.title == "Backup site failed"
    assert item.category == "Sites"
    assert item.severity == "Error"
    assert item.site == "a.local"
    assert item.task_id == "t1"
    assert item.action_route == "/insights/tasks/t1"


def test_update_commands_are_grouped_separately(tmp_path) -> None:
    task_failed(tmp_path, {"task_id": "t2", "command": "migrate", "args": {}})

    (item,) = _feed(tmp_path)
    assert item.category == "Updates"
    assert item.site is None


def test_unmapped_command_falls_back_to_tasks(tmp_path) -> None:
    task_failed(tmp_path, {"task_id": "t3", "command": "install-app", "args": {}})

    assert _feed(tmp_path)[0].category == "Tasks"


def test_noisy_commands_stay_out_of_the_feed(tmp_path) -> None:
    task_failed(tmp_path, {"task_id": "t4", "command": "clear-cache", "args": {}})
    task_failed(tmp_path, {"task_id": "t5", "command": "", "args": {}})

    assert _feed(tmp_path) == []


def test_task_failure_never_raises(tmp_path, caplog) -> None:
    caplog.set_level(logging.WARNING)
    with patch("pilot.core.notification.NotificationStore.create", side_effect=OSError("disk full")):
        task_failed(tmp_path, {"task_id": "t6", "command": "update", "args": {}})

    assert "Notification skipped for failed task t6" in caplog.text


def test_record_alert_writes_the_benchs_own_copy(tmp_path) -> None:
    bench = SimpleNamespace(notifications=NotificationStore(tmp_path / "logs"))
    payload = {"event": "site_down", "message": "bench: a.local unreachable"}

    record_alert(bench, payload, category="Sites", severity="Error", title="a.local is unreachable", site="a.local")

    (item,) = _feed(tmp_path)
    assert item.title == "a.local is unreachable"
    assert item.event == "site_down"
    assert item.message == "bench: a.local unreachable"
    assert item.action_route == "/sites/a.local"


def test_record_alert_never_raises(tmp_path, caplog) -> None:
    bench = SimpleNamespace(notifications=NotificationStore(tmp_path / "logs"))
    caplog.set_level(logging.WARNING)
    with patch("pilot.core.notification.NotificationStore.create", side_effect=OSError("disk full")):
        record_alert(bench, {"event": "resource_limit_breached"}, category="Server", severity="Warning", title="x")

    assert "Notification skipped for alert resource_limit_breached" in caplog.text
