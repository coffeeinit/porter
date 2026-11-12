from __future__ import annotations

from pathlib import Path

from pilot.config import BenchConfig
from pilot.internal.password_hash import is_hashed, verify_password
from pilot.internal.patch_state import is_applied
from pilot.patches import hash_admin_password


def _bench(benches_root: Path, name: str, password: str) -> Path:
    bench_dir = benches_root / name
    bench_dir.mkdir(parents=True)
    BenchConfig.write_flat(bench_dir, name, {"admin_enabled": True})
    raw = BenchConfig.read_raw(bench_dir)
    raw.setdefault("admin", {})["password"] = password
    BenchConfig.write_raw(bench_dir, raw)
    return bench_dir


def test_patch_hashes_every_bench_and_keeps_the_password_working(tmp_path: Path) -> None:
    benches = tmp_path / "benches"
    first = _bench(benches, "one", "Str0ng!one")
    second = _bench(benches, "two", "Str0ng!two")

    hash_admin_password.run(benches)

    for bench_dir, password in ((first, "Str0ng!one"), (second, "Str0ng!two")):
        stored = BenchConfig.read_raw(bench_dir)["admin"]["password"]
        assert is_hashed(stored)
        assert password not in stored
        assert verify_password(password, stored)
        assert is_applied(bench_dir, "hash_admin_password")


def test_patch_leaves_an_already_hashed_password_alone(tmp_path: Path) -> None:
    benches = tmp_path / "benches"
    bench_dir = _bench(benches, "one", "Str0ng!one")

    hash_admin_password.run(benches)
    first = BenchConfig.read_raw(bench_dir)["admin"]["password"]
    hash_admin_password.run(benches)

    assert BenchConfig.read_raw(bench_dir)["admin"]["password"] == first


def test_patch_skips_a_bench_with_no_password(tmp_path: Path) -> None:
    benches = tmp_path / "benches"
    bench_dir = _bench(benches, "one", "")

    hash_admin_password.run(benches)

    assert BenchConfig.read_raw(bench_dir)["admin"]["password"] == ""
