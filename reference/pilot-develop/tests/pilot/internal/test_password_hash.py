"""bench.toml holds a verifier, not the Admin password itself."""

from __future__ import annotations

from pilot.internal import password_hash
from pilot.internal.password_hash import hash_password, is_hashed, verify_password


def test_a_hash_reveals_neither_the_password_nor_a_repeat_of_itself() -> None:
    first = hash_password("Str0ng!pass")
    second = hash_password("Str0ng!pass")

    assert "Str0ng!pass" not in first
    assert first != second  # per-password salt
    assert first.startswith(f"$pbkdf2-sha256${password_hash.ITERATIONS}$")
    assert is_hashed(first)


def test_verify_accepts_the_password_and_nothing_else() -> None:
    stored = hash_password("Str0ng!pass")

    assert verify_password("Str0ng!pass", stored) is True
    assert verify_password("str0ng!pass", stored) is False
    assert verify_password("", stored) is False
    assert verify_password("Str0ng!pass", "") is False


def test_a_bench_that_has_not_run_the_patch_can_still_sign_in() -> None:
    """The hash_admin_password patch migrates cleartext; until it runs, login works."""
    assert is_hashed("cleartext") is False
    assert verify_password("cleartext", "cleartext") is True
    assert verify_password("wrong", "cleartext") is False


def test_a_damaged_hash_verifies_nothing() -> None:
    for stored in ("$pbkdf2-sha256$", "$pbkdf2-sha256$600000$notbase64$x", "$pbkdf2-sha256$abc$c2E=$c2E="):
        assert verify_password("Str0ng!pass", stored) is False


def test_admin_password_validator_rejects_a_stored_hash_as_input() -> None:
    """A user must not set their password to something hash-shaped, or set_password would
    keep it verbatim instead of hashing it."""
    from pilot.internal.validators import validate_admin_password

    stored = hash_password("Str0ng!pass")
    assert validate_admin_password(stored) == "Password must not be a stored password hash."
    assert validate_admin_password("Str0ng!pass") is None
