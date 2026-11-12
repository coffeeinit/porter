"""Tests for pilot.managers.platform helpers."""

from __future__ import annotations

import threading
from pathlib import Path

from pilot.managers import platform


def test_which_searches_sbin_when_path_is_minimal(tmp_path: Path, monkeypatch) -> None:
    sbin = tmp_path / "usr" / "sbin"
    sbin.mkdir(parents=True)
    daemon = sbin / "mariadbd"
    daemon.write_text("#!/bin/sh\n")
    daemon.chmod(0o755)

    # Minimal PATH without the sbin dir - shutil.which alone would miss it.
    monkeypatch.setenv("PATH", str(tmp_path / "bin"))
    monkeypatch.setattr(platform, "_EXTRA_BIN_DIRS", (str(sbin),))

    assert platform.which("mariadbd") == str(daemon)


def test_which_returns_none_for_missing(monkeypatch) -> None:
    monkeypatch.setattr(platform, "_EXTRA_BIN_DIRS", ())
    assert platform.which("definitely-not-a-real-binary-xyz") is None


def test_noninteractive_privileges_are_isolated_to_the_current_thread(monkeypatch) -> None:
    monkeypatch.setattr(platform, "is_root", lambda: False)
    entered = threading.Event()
    release = threading.Event()
    command_from_thread: list[str] = []

    def build_noninteractive_command() -> None:
        with platform.noninteractive_privileges():
            entered.set()
            assert release.wait(timeout=1)
            command_from_thread.extend(platform._privileged(["true"]))

    thread = threading.Thread(target=build_noninteractive_command)
    thread.start()
    assert entered.wait(timeout=1)
    assert platform._privileged(["true"]) == ["sudo", "true"]
    release.set()
    thread.join(timeout=1)

    assert command_from_thread == ["sudo", "-n", "true"]


def test_task_environment_forces_noninteractive_privileges(monkeypatch) -> None:
    monkeypatch.setattr(platform, "is_root", lambda: False)
    monkeypatch.setenv(platform.NONINTERACTIVE_PRIVILEGES_ENV, "1")

    assert platform._privileged(["true"]) == ["sudo", "-n", "true"]


def _fake_mariadb_config(tmp_path: Path) -> Path:
    config_bin = tmp_path / "mariadb_config"
    config_bin.write_text(
        '#!/bin/sh\ncase "$1" in\n  --cflags) echo "-I/opt/mariadb/include" ;;\n'
        '  --libs) echo "-L/opt/mariadb/lib -lmariadb" ;;\nesac\n'
    )
    config_bin.chmod(0o755)
    return config_bin


def test_add_mysqlclient_flags_sets_flags_from_mariadb_config(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.setattr(platform, "is_macos", lambda: True)
    monkeypatch.setattr(platform, "mariadb_config_bin", lambda: str(_fake_mariadb_config(tmp_path)))

    env: dict[str, str] = {}
    platform.add_mysqlclient_flags(env)

    assert env == {
        "MYSQLCLIENT_CFLAGS": "-I/opt/mariadb/include",
        "MYSQLCLIENT_LDFLAGS": "-L/opt/mariadb/lib -lmariadb",
    }


def test_add_mysqlclient_flags_keeps_caller_values(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.setattr(platform, "is_macos", lambda: True)
    monkeypatch.setattr(platform, "mariadb_config_bin", lambda: str(_fake_mariadb_config(tmp_path)))

    env = {"MYSQLCLIENT_CFLAGS": "-I/custom"}
    platform.add_mysqlclient_flags(env)

    assert env["MYSQLCLIENT_CFLAGS"] == "-I/custom"


def test_add_mysqlclient_flags_is_a_noop_off_macos(monkeypatch) -> None:
    monkeypatch.setattr(platform, "is_macos", lambda: False)

    env: dict[str, str] = {}
    platform.add_mysqlclient_flags(env)

    assert env == {}
