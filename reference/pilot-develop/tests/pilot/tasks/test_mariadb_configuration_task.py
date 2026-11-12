from __future__ import annotations

from pathlib import Path
from unittest.mock import Mock, patch

import pytest

from pilot.exceptions import DatabaseError
from pilot.internal.tasks.authoring import task_argv_suffix
from pilot.tasks.set_mariadb_configuration import SetMariaDBConfigurationTask


def test_configuration_task_is_not_cancellable_while_mutating_database() -> None:
    assert SetMariaDBConfigurationTask.is_cancellable_while_running is False


def test_configuration_task_preserves_false_as_json_across_cli_boundary() -> None:
    assert task_argv_suffix(
        SetMariaDBConfigurationTask,
        {"variable": "innodb_print_all_deadlocks", "value_json": "false"},
    ) == ["innodb_print_all_deadlocks", "false"]


@pytest.mark.parametrize(
    ("value_json", "expected"),
    [("20", 20), ("false", False)],
)
def test_configuration_task_decodes_typed_json_and_revalidates(
    tmp_path: Path,
    value_json: str,
    expected,
) -> None:
    bench = Mock()
    configurations = Mock()
    task = SetMariaDBConfigurationTask(
        bench=bench,
        bench_root=tmp_path,
        variable="connect_timeout",
        value_json=value_json,
    )

    with patch(
        "pilot.tasks.set_mariadb_configuration.DatabaseConfigurations",
        return_value=configurations,
    ):
        task.run()

    configurations.set.assert_called_once_with("connect_timeout", expected)


def test_configuration_task_rejects_invalid_json_before_database_call(tmp_path: Path) -> None:
    bench = Mock()
    configurations = Mock()
    task = SetMariaDBConfigurationTask(
        bench=bench,
        bench_root=tmp_path,
        variable="connect_timeout",
        value_json="not-json",
    )

    with (
        patch(
            "pilot.tasks.set_mariadb_configuration.DatabaseConfigurations",
            return_value=configurations,
        ),
        pytest.raises(DatabaseError, match="value is invalid"),
    ):
        task.run()

    configurations.set.assert_not_called()
