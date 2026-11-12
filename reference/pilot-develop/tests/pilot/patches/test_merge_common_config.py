from __future__ import annotations

import importlib.util
from pathlib import Path
from types import ModuleType

import pytest

from pilot.config import BenchConfig
from pilot.config.common import CommonConfig
from pilot.exceptions import ConfigError
from pilot.internal import patch_state as state

_PATCH_PATH = Path(__file__).parent.parent.parent.parent / "pilot" / "patches" / "merge_common_config.py"


def _load_patch() -> ModuleType:
    spec = importlib.util.spec_from_file_location("merge_common_config_patch", _PATCH_PATH)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PATCH = _load_patch()

_OLD_STYLE_BENCH_TOML = """
[bench]
name = "{name}"
python = "3.14"

[[apps]]
name = "frappe"
repo = "https://github.com/frappe/frappe"
branch = "version-16"

[mariadb]
host = "localhost"
port = 3306
root_password = "realpw"
admin_user = "root"
socket_path = ""
existing = false

[postgres]
host = "localhost"
port = 5432
root_password = ""
admin_user = "postgres"
existing = false

[redis]
cache_port = 13000
queue_port = 11000

[admin]
port = 7000
timeout = 180
enabled = true
password = "x"
domain = ""
tls = false
allow_bench_management = true
jwks_url = "https://issuer.example.com/jwks.json"
jwks_audience = "fleet"

[letsencrypt]
email = "ops@example.com"
webroot_path = "/var/www/letsencrypt"

[[workers]]
queues = ["default"]
count = 1
"""


def _write_old_style_bench(benches_root: Path, name: str, root_password: str = "realpw") -> Path:
    bench_dir = benches_root / name
    bench_dir.mkdir(parents=True)
    content = _OLD_STYLE_BENCH_TOML.format(name=name).replace(
        'root_password = "realpw"', f'root_password = "{root_password}"'
    )
    (bench_dir / "bench.toml").write_text(content)
    return bench_dir


def test_seeds_common_config_from_first_bench_with_real_values(tmp_path: Path) -> None:
    _write_old_style_bench(tmp_path, "bench1")
    _write_old_style_bench(tmp_path, "bench2")

    PATCH.run(tmp_path)

    common = CommonConfig.read(tmp_path)
    assert common.mariadb.root_password == "realpw"
    assert common.letsencrypt.email == "ops@example.com"
    assert common.jwks_url == "https://issuer.example.com/jwks.json"
    assert common.jwks_audience == "fleet"


def test_strips_shared_fields_from_every_bench_toml(tmp_path: Path) -> None:
    bench1 = _write_old_style_bench(tmp_path, "bench1")
    bench2 = _write_old_style_bench(tmp_path, "bench2")

    PATCH.run(tmp_path)

    for bench_dir in (bench1, bench2):
        raw = BenchConfig.read_raw(bench_dir)
        assert "mariadb" not in raw
        assert "postgres" not in raw
        assert "letsencrypt" not in raw
        assert "jwks_url" not in raw["admin"]
        assert "jwks_audience" not in raw["admin"]
        # Untouched fields survive.
        assert raw["admin"]["domain"] == ""
        assert raw["bench"]["name"] == bench_dir.name


def test_bench_config_still_resolves_correctly_after_migration(tmp_path: Path) -> None:
    bench_dir = _write_old_style_bench(tmp_path, "bench1")

    PATCH.run(tmp_path)

    config = BenchConfig.read(bench_dir)
    assert config.mariadb.root_password == "realpw"
    assert config.letsencrypt.email == "ops@example.com"
    assert config.admin.jwks_url == "https://issuer.example.com/jwks.json"


def test_marks_each_bench_as_migrated(tmp_path: Path) -> None:
    bench_dir = _write_old_style_bench(tmp_path, "bench1")

    PATCH.run(tmp_path)

    assert state.is_applied(bench_dir, PATCH.PATCH_NAME)


def test_is_idempotent(tmp_path: Path) -> None:
    bench_dir = _write_old_style_bench(tmp_path, "bench1")

    PATCH.run(tmp_path)
    first_mtime = (bench_dir / "bench.toml").stat().st_mtime_ns
    PATCH.run(tmp_path)

    assert (bench_dir / "bench.toml").stat().st_mtime_ns == first_mtime


def test_backs_up_bench_toml_before_trimming(tmp_path: Path) -> None:
    bench_dir = _write_old_style_bench(tmp_path, "bench1")
    original = (bench_dir / "bench.toml").read_text()

    PATCH.run(tmp_path)

    assert (bench_dir / "bench.toml.bak").read_text() == original
    assert (bench_dir / "bench.toml").read_text() != original


def test_backup_increments_when_one_already_exists(tmp_path: Path) -> None:
    bench_dir = _write_old_style_bench(tmp_path, "bench1")
    (bench_dir / "bench.toml.bak").write_text("pre-existing backup")

    PATCH.run(tmp_path)

    assert (bench_dir / "bench.toml.bak").read_text() == "pre-existing backup"
    assert (bench_dir / "bench.toml.bak.1").exists()


def test_no_backup_when_bench_has_nothing_to_trim(tmp_path: Path) -> None:
    bench_dir = tmp_path / "fresh"
    bench_dir.mkdir(parents=True)
    (bench_dir / "bench.toml").write_text(BenchConfig.from_flat("fresh").dumps())

    PATCH.run(tmp_path)

    assert not (bench_dir / "bench.toml.bak").exists()


def test_raises_when_benches_disagree_on_mariadb(tmp_path: Path) -> None:
    _write_old_style_bench(tmp_path, "bench1", root_password="realpw")
    _write_old_style_bench(tmp_path, "bench2", root_password="different-pw")

    with pytest.raises(ConfigError, match="mariadb"):
        PATCH.run(tmp_path)

    # Nothing was modified - validation ran before any write.
    assert not CommonConfig.path(tmp_path).exists()
    assert 'root_password = "realpw"' in (tmp_path / "bench1" / "bench.toml").read_text()
    assert 'root_password = "different-pw"' in (tmp_path / "bench2" / "bench.toml").read_text()
    assert not (tmp_path / "bench1" / "bench.toml.bak").exists()
    assert not (tmp_path / "bench2" / "bench.toml.bak").exists()


def test_raises_when_benches_disagree_on_jwks(tmp_path: Path) -> None:
    _write_old_style_bench(tmp_path, "bench1")
    bench2 = _write_old_style_bench(tmp_path, "bench2")
    (bench2 / "bench.toml").write_text(
        (bench2 / "bench.toml").read_text().replace("fleet", "other-audience")
    )

    with pytest.raises(ConfigError, match="jwks"):
        PATCH.run(tmp_path)

    assert not CommonConfig.path(tmp_path).exists()


def test_agreeing_benches_pass_validation(tmp_path: Path) -> None:
    """Identical values across benches are fine - this isn't about forbidding
    more than one bench, only disagreement between them."""
    _write_old_style_bench(tmp_path, "bench1")
    _write_old_style_bench(tmp_path, "bench2")

    PATCH.run(tmp_path)

    assert CommonConfig.read(tmp_path).mariadb.root_password == "realpw"


def test_bench_without_legacy_sections_is_untouched(tmp_path: Path) -> None:
    bench_dir = tmp_path / "fresh"
    bench_dir.mkdir(parents=True)
    (bench_dir / "bench.toml").write_text(BenchConfig.from_flat("fresh").dumps())
    original = (bench_dir / "bench.toml").read_text()

    PATCH.run(tmp_path)

    assert (bench_dir / "bench.toml").read_text() == original
    assert not CommonConfig.path(tmp_path).exists()


def test_no_bench_directories_is_a_no_op(tmp_path: Path) -> None:
    PATCH.run(tmp_path)
    assert not CommonConfig.path(tmp_path).exists()
