from __future__ import annotations

from pathlib import Path

from pilot.internal.patch_state import is_applied, mark_applied


def test_not_applied_when_no_state_file(tmp_path: Path) -> None:
    assert is_applied(tmp_path, "some_patch") is False


def test_mark_applied_then_is_applied(tmp_path: Path) -> None:
    mark_applied(tmp_path, "some_patch")
    assert is_applied(tmp_path, "some_patch") is True
    assert is_applied(tmp_path, "other_patch") is False


def test_mark_applied_is_idempotent_and_keeps_existing_entries(tmp_path: Path) -> None:
    mark_applied(tmp_path, "first")
    mark_applied(tmp_path, "second")
    mark_applied(tmp_path, "first")
    assert is_applied(tmp_path, "first") is True
    assert is_applied(tmp_path, "second") is True


def test_corrupt_state_file_reads_as_empty(tmp_path: Path) -> None:
    (tmp_path / ".patches.json").write_text("not valid json {{{")
    assert is_applied(tmp_path, "anything") is False
