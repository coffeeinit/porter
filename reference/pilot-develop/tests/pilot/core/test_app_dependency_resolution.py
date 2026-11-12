"""Tests for DependencyResolutionCheck, the --dry-run resolve against the bench env."""

from __future__ import annotations

from pathlib import Path

import pytest

from pilot.core.app.validator import dependency_resolution
from pilot.core.app.validator.dependency_resolution import DependencyResolutionCheck
from pilot.exceptions import AppValidationError, CommandError

UV_CONFLICT = (
    "Because wiki==3.0.0 depends on markdown==3.8.2 and markdown>=3.5.1,<3.6.dev0, "
    "we can conclude that your requirements are unsatisfiable."
)


class _FakeApp:
    def __init__(self, bench, name: str) -> None:
        self.bench = bench
        self.config = type("Config", (), {"name": name})
        self.path = bench.apps_path / name


class _FakeBench:
    def __init__(self, root: Path) -> None:
        self.path = root
        self.apps_path = root / "apps"
        self.env_path = root / "env"
        self._apps: list[_FakeApp] = []

    def apps(self) -> list[_FakeApp]:
        return self._apps

    def add_app(self, name: str, dependencies: str = "") -> _FakeApp:
        app = _FakeApp(self, name)
        app.path.mkdir(parents=True)
        (app.path / "pyproject.toml").write_text(f'[project]\nname = "{name}"\n{dependencies}')
        self._apps.append(app)
        return app


def _make_bench(tmp_path: Path, *, with_env: bool = True) -> _FakeBench:
    bench = _FakeBench(tmp_path)
    bench.apps_path.mkdir(parents=True)
    if with_env:
        (bench.env_path / "bin").mkdir(parents=True)
        (bench.env_path / "bin" / "python").write_text("")
    return bench


def test_resolution_runs_the_real_install_command_with_dry_run(monkeypatch, tmp_path: Path) -> None:
    bench = _make_bench(tmp_path)
    app = bench.add_app("myapp")
    commands: list[list[str]] = []
    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(dependency_resolution, "run_command", lambda argv, **kw: commands.append(argv))

    DependencyResolutionCheck().run(app)

    assert commands == [
        [
            "/bin/uv",
            "pip",
            "install",
            "--dry-run",
            "--python",
            str(bench.env_path / "bin" / "python"),
            "-e",
            str(app.path),
        ]
    ]


def test_resolution_constrains_the_resolve_with_what_other_apps_require(monkeypatch, tmp_path) -> None:
    """One environment holds one version of a package, so the other apps' pins bound it."""
    bench = _make_bench(tmp_path)
    bench.add_app("lms", 'dependencies = ["markdown~=3.5.1"]\n')
    app = bench.add_app("myapp", 'dependencies = ["markdown==3.8.2"]\n')
    captured: dict[str, str] = {}

    def capture(argv, **kwargs):
        captured["constraints"] = Path(argv[argv.index("--constraint") + 1]).read_text()

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(dependency_resolution, "run_command", capture)

    DependencyResolutionCheck().run(app)

    assert captured["constraints"] == "markdown~=3.5.1  # lms\n"  # not the app's own pin


def test_resolution_names_the_app_that_pinned_the_package(monkeypatch, tmp_path: Path) -> None:
    bench = _make_bench(tmp_path)
    bench.add_app("lms", 'dependencies = ["markdown~=3.5.1"]\n')
    app = bench.add_app("myapp", 'dependencies = ["markdown==3.8.2"]\n')

    def fail(argv, **kwargs):
        raise CommandError(UV_CONFLICT)

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(dependency_resolution, "run_command", fail)

    with pytest.raises(AppValidationError, match=r"markdown~=3\.5\.1  # lms"):
        DependencyResolutionCheck().run(app)


def test_resolution_drops_the_lines_that_explain_nothing(monkeypatch, tmp_path: Path) -> None:
    """Which uv binary ran and which env it used are plumbing, not a reason."""
    bench = _make_bench(tmp_path)
    app = bench.add_app("myapp")
    noisy = (
        "Command '/home/frappe/.local/bin/uv' failed with exit code 1.\n"
        "Using Python 3.14.6 environment at: /benches/bench1/env\n"
        "  No solution found when resolving dependencies:"
    )

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(
        dependency_resolution, "run_command", lambda argv, **kw: (_ for _ in ()).throw(CommandError(noisy))
    )

    with pytest.raises(AppValidationError) as excinfo:
        DependencyResolutionCheck().run(app)

    assert "No solution found" in str(excinfo.value)
    assert "exit code" not in str(excinfo.value)
    assert "Using Python" not in str(excinfo.value)


def test_resolution_leaves_out_requirements_that_cannot_be_constraints(monkeypatch, tmp_path) -> None:
    """Constraints files reject extras, and an unbounded name constrains nothing."""
    bench = _make_bench(tmp_path)
    bench.add_app("other", 'dependencies = ["markdown~=3.5.1", "requests", "celery[redis]>=5"]\n')
    app = bench.add_app("myapp")
    captured: dict[str, str] = {}

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(
        dependency_resolution,
        "run_command",
        lambda argv, **kw: captured.update(
            constraints=Path(argv[argv.index("--constraint") + 1]).read_text()
        ),
    )

    DependencyResolutionCheck().run(app)

    assert captured["constraints"] == "markdown~=3.5.1  # other\n"


def test_resolution_constrains_url_dependencies_too(monkeypatch, tmp_path) -> None:
    """uv won't resolve a tree containing a URL dependency unless it is pinned
    somewhere, and frappe pins two - leaving them out fails every app that
    depends on frappe, with uv naming a package the app never mentioned."""
    bench = _make_bench(tmp_path)
    bench.add_app("frappe", 'dependencies = ["pypika @ git+https://github.com/frappe/pypika@2c50e61"]\n')
    app = bench.add_app("myapp", 'dependencies = ["frappe>=14.0.0"]\n')
    captured: dict[str, str] = {}

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(
        dependency_resolution,
        "run_command",
        lambda argv, **kw: captured.update(
            constraints=Path(argv[argv.index("--constraint") + 1]).read_text()
        ),
    )

    DependencyResolutionCheck().run(app)

    assert captured["constraints"] == "pypika @ git+https://github.com/frappe/pypika@2c50e61  # frappe\n"


def test_resolution_keeps_a_requirement_that_carries_a_marker(monkeypatch, tmp_path) -> None:
    """A marker is legal in a constraints file. Dropping the whole line instead
    left a URL dependency unpinned, which is the one thing uv won't resolve."""
    bench = _make_bench(tmp_path)
    bench.add_app(
        "frappe",
        'dependencies = ["pypika @ git+https://github.com/frappe/pypika ; python_version >= \'3.10\'", '
        '"backports-zoneinfo>=0.2 ; python_version < \'3.9\'"]\n',
    )
    app = bench.add_app("myapp", 'dependencies = ["frappe>=14.0.0"]\n')
    captured: dict[str, str] = {}

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(
        dependency_resolution,
        "run_command",
        lambda argv, **kw: captured.update(
            constraints=Path(argv[argv.index("--constraint") + 1]).read_text()
        ),
    )

    DependencyResolutionCheck().run(app)

    assert captured["constraints"] == (
        'backports-zoneinfo>=0.2 ; python_version < "3.9"  # frappe\n'
        'pypika @ git+https://github.com/frappe/pypika ; python_version >= "3.10"  # frappe\n'
    )


def test_resolution_blames_the_app_that_pinned_a_url_dependency(monkeypatch, tmp_path) -> None:
    """A '#egg=' fragment must not be mistaken for the trailing app-name comment."""
    bench = _make_bench(tmp_path)
    bench.add_app("frappe", 'dependencies = ["pypika @ git+https://github.com/frappe/pypika#egg=pypika"]\n')
    app = bench.add_app("myapp", 'dependencies = ["frappe>=14.0.0"]\n')

    def fail(argv, **kwargs):
        raise CommandError("Package `pypika` was included as a URL dependency.")

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(dependency_resolution, "run_command", fail)

    with pytest.raises(AppValidationError, match=r"pypika @ git\+https://github\.com/frappe/pypika#egg=pypika"):
        DependencyResolutionCheck().run(app)


def test_resolution_ignores_another_apps_broken_pyproject(monkeypatch, tmp_path) -> None:
    """A malformed pyproject.toml belongs to that app's own checks - it must not
    block an unrelated install, least of all under the other app's name."""
    bench = _make_bench(tmp_path)
    bench.add_app("lms", 'dependencies = ["markdown~=3.5.1"]\n')
    (bench.apps_path / "broken").mkdir()
    (bench.apps_path / "broken" / "pyproject.toml").write_text("[project\nnot valid toml\n")
    bench._apps.append(_FakeApp(bench, "broken"))
    app = bench.add_app("myapp")
    captured: dict[str, str] = {}

    monkeypatch.setattr(dependency_resolution, "ensure_uv", lambda: "/bin/uv")
    monkeypatch.setattr(
        dependency_resolution,
        "run_command",
        lambda argv, **kw: captured.update(
            constraints=Path(argv[argv.index("--constraint") + 1]).read_text()
        ),
    )

    DependencyResolutionCheck().run(app)

    assert captured["constraints"] == "markdown~=3.5.1  # lms\n"


def test_resolution_is_skipped_before_the_bench_has_an_environment(monkeypatch, tmp_path: Path) -> None:
    bench = _make_bench(tmp_path, with_env=False)
    app = bench.add_app("myapp")

    def fail(*args, **kwargs):
        raise AssertionError("nothing to resolve against without an env")

    monkeypatch.setattr(dependency_resolution, "run_command", fail)

    DependencyResolutionCheck().run(app)
