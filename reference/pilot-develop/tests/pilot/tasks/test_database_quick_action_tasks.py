from __future__ import annotations

from pathlib import Path
from unittest.mock import Mock, patch

import pytest

from pilot.exceptions import DatabaseError
from pilot.tasks.restart_database import RestartDatabaseTask
from pilot.tasks.set_innodb_buffer_pool_size import SetInnoDBBufferPoolSizeTask
from pilot.tasks.set_max_database_connections import SetMaxDatabaseConnectionsTask
from pilot.tasks.set_performance_schema import SetPerformanceSchemaTask


def test_database_mutation_tasks_cannot_be_cancelled_while_running() -> None:
    assert RestartDatabaseTask.is_cancellable_while_running is False
    assert SetPerformanceSchemaTask.is_cancellable_while_running is False
    assert SetInnoDBBufferPoolSizeTask.is_cancellable_while_running is False
    assert SetMaxDatabaseConnectionsTask.is_cancellable_while_running is False


def test_restart_task_revalidates_and_runs_quick_action(tmp_path: Path) -> None:
    bench = Mock()
    actions = Mock()
    task = RestartDatabaseTask(bench=bench, bench_root=tmp_path)

    with patch("pilot.tasks.restart_database.DatabaseQuickActions", return_value=actions):
        task.run()

    actions.restart.assert_called_once()


@pytest.mark.parametrize(
    ("state", "enabled"),
    [("enabled", True), ("disabled", False)],
)
def test_performance_schema_task_maps_strict_state_to_boolean(
    tmp_path: Path,
    state: str,
    enabled: bool,
    capsys,
) -> None:
    bench = Mock()
    actions = Mock()
    restart = Mock()

    def set_performance_schema(value, *, restart_executor) -> None:
        assert value is enabled
        restart_executor(restart)

    actions.set_performance_schema.side_effect = set_performance_schema
    task = SetPerformanceSchemaTask(
        bench=bench,
        bench_root=tmp_path,
        state=state,
    )

    with patch(
        "pilot.tasks.set_performance_schema.DatabaseQuickActions",
        return_value=actions,
    ):
        task.run()

    actions.set_performance_schema.assert_called_once_with(
        enabled,
        restart_executor=task.restart,
    )
    restart.assert_called_once()
    output = capsys.readouterr().out
    assert "STEP configure," in output
    assert "STEP restart," in output


def test_performance_schema_task_rejects_invalid_state_before_action(
    tmp_path: Path,
) -> None:
    bench = Mock()
    actions = Mock()
    task = SetPerformanceSchemaTask(
        bench=bench,
        bench_root=tmp_path,
        state="truthy",
    )

    with (
        patch(
            "pilot.tasks.set_performance_schema.DatabaseQuickActions",
            return_value=actions,
        ),
        pytest.raises(DatabaseError, match="must be 'enabled' or 'disabled'"),
    ):
        task.run()

    actions.set_performance_schema.assert_not_called()


@pytest.mark.parametrize(
    ("task_type", "module", "field", "value", "method"),
    [
        (
            SetInnoDBBufferPoolSizeTask,
            "pilot.tasks.set_innodb_buffer_pool_size.DatabaseQuickActions",
            "size_mb",
            256,
            "set_innodb_buffer_pool_size",
        ),
        (
            SetMaxDatabaseConnectionsTask,
            "pilot.tasks.set_max_database_connections.DatabaseQuickActions",
            "max_connections",
            40,
            "set_max_connections",
        ),
    ],
)
def test_sizing_tasks_revalidate_and_run_quick_action(
    tmp_path: Path,
    task_type,
    module: str,
    field: str,
    value: int,
    method: str,
) -> None:
    bench = Mock()
    actions = Mock()
    task = task_type(bench=bench, bench_root=tmp_path, **{field: value})

    with patch(module, return_value=actions):
        task.run()

    getattr(actions, method).assert_called_once_with(value)


@pytest.mark.parametrize(
    ("task_type", "module", "field"),
    [
        (
            SetInnoDBBufferPoolSizeTask,
            "pilot.tasks.set_innodb_buffer_pool_size.DatabaseQuickActions",
            "size_mb",
        ),
        (
            SetMaxDatabaseConnectionsTask,
            "pilot.tasks.set_max_database_connections.DatabaseQuickActions",
            "max_connections",
        ),
    ],
)
def test_sizing_tasks_reject_non_integer_before_action(
    tmp_path: Path,
    task_type,
    module: str,
    field: str,
) -> None:
    bench = Mock()
    actions = Mock()
    task = task_type(bench=bench, bench_root=tmp_path, **{field: True})

    with (
        patch(module, return_value=actions),
        pytest.raises(DatabaseError, match="whole number"),
    ):
        task.run()

    actions.set_innodb_buffer_pool_size.assert_not_called()
    actions.set_max_connections.assert_not_called()
