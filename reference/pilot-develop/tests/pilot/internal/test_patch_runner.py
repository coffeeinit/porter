from __future__ import annotations

from pathlib import Path

import pytest

from pilot.internal import patch_runner as runner


def _write_patches_txt(directory: Path, content: str) -> None:
    (directory / "patches.txt").write_text(content)


def _write_dummy_patch(directory: Path, name: str) -> None:
    # Appends its own name to $PATCH_TEST_MARKER, so a test can assert order.
    (directory / f"{name}.py").write_text(
        "import os\n"
        "from pathlib import Path\n"
        "marker = Path(os.environ['PATCH_TEST_MARKER'])\n"
        "marker.write_text((marker.read_text() if marker.exists() else '') + "
        f"{name!r} + '\\n')\n"
    )


@pytest.fixture(autouse=True)
def _patches_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setattr(runner, "PATCHES_DIR", tmp_path)
    monkeypatch.setattr(runner, "PATCHES_TXT", tmp_path / "patches.txt")
    return tmp_path


def test_patch_names_reads_the_right_section(_patches_dir: Path) -> None:
    _write_patches_txt(
        _patches_dir,
        "# a comment\n[pre_update]\nfirst\n\n[post_update]\nsecond\nthird\n",
    )
    assert runner.patch_names("pre_update") == ["first"]
    assert runner.patch_names("post_update") == ["second", "third"]


def test_patch_names_rejects_unknown_phase(_patches_dir: Path) -> None:
    _write_patches_txt(_patches_dir, "[pre_update]\n")
    with pytest.raises(ValueError, match="Unknown patch phase"):
        runner.patch_names("during_update")


def test_run_patches_executes_listed_patches_in_order(
    _patches_dir: Path, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    marker = tmp_path / "marker.txt"
    monkeypatch.setenv("PATCH_TEST_MARKER", str(marker))
    _write_patches_txt(_patches_dir, "[pre_update]\nalpha\nbeta\n\n[post_update]\n")
    _write_dummy_patch(_patches_dir, "alpha")
    _write_dummy_patch(_patches_dir, "beta")

    runner.run_patches("pre_update")

    assert marker.read_text() == "alpha\nbeta\n"


def test_run_patches_all_runs_both_phases_in_order(
    _patches_dir: Path, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    marker = tmp_path / "marker.txt"
    monkeypatch.setenv("PATCH_TEST_MARKER", str(marker))
    _write_patches_txt(_patches_dir, "[pre_update]\nalpha\n\n[post_update]\nbeta\n")
    _write_dummy_patch(_patches_dir, "alpha")
    _write_dummy_patch(_patches_dir, "beta")

    runner.run_patches("all")

    assert marker.read_text() == "alpha\nbeta\n"


def test_run_patches_passes_cli_root_as_pythonpath(
    _patches_dir: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """bench is a plain script, not a pip install, so "pilot" is only on
    sys.path inside this already-running process - a patch subprocess needs
    PYTHONPATH set to the cli root or its own `import pilot` fails."""
    _write_patches_txt(_patches_dir, "[pre_update]\nalpha\n\n[post_update]\n")
    (_patches_dir / "alpha.py").write_text("")
    captured = {}

    def fake_run(cmd, **kwargs):
        captured.update(kwargs)

    monkeypatch.setattr(runner.subprocess, "run", fake_run)

    runner.run_patches("pre_update")

    assert captured["env"]["PYTHONPATH"] == str(runner._CLI_ROOT)


def test_run_patches_skips_a_missing_patch_file_without_raising(_patches_dir: Path) -> None:
    _write_patches_txt(_patches_dir, "[pre_update]\nghost\n\n[post_update]\n")
    messages: list[str] = []

    runner.run_patches("pre_update", on_progress=messages.append)

    assert any("ghost" in message for message in messages)
