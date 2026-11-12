from __future__ import annotations

from unittest.mock import patch

from pilot.commands.admin.run_patches import RunPatchesCommand


def test_default_phase_is_all() -> None:
    with patch("pilot.internal.patch_runner.run_patches") as mock_run:
        RunPatchesCommand().run()
    mock_run.assert_called_once()
    assert mock_run.call_args.args[0] == "all"


def test_phase_is_passed_through() -> None:
    with patch("pilot.internal.patch_runner.run_patches") as mock_run:
        RunPatchesCommand(phase="post_update").run()
    assert mock_run.call_args.args[0] == "post_update"
