from __future__ import annotations

import json
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pyotp
import pytest

from admin.backend.internal.two_factor_authentication import (
    MAX_ENROLLED_DEVICES,
    PENDING_TTL,
    RECOVERY_CODE_COUNT,
    TotpCredentialStore,
    TwoFactorAuthentication,
    TwoFactorError,
)
from pilot.config import BenchConfig
from pilot.core.bench import Bench


def _bench(tmp_path: Path) -> Bench:
    toml_path = tmp_path / "bench.toml"
    toml_path.write_text(BenchConfig.from_flat(tmp_path.name, {"admin_password": "secret"}).dumps())
    return Bench(BenchConfig.from_file(toml_path), tmp_path)


def _enroll(two_factor: TwoFactorAuthentication, name: str = "phone") -> tuple[str, str]:
    """Enroll and confirm a device, returning its id and secret.

    Confirms with the previous step's code so the current one stays unspent: confirmation
    burns the step it used, and re-sending that code would be a replay.
    """
    enrollment = two_factor.start_enrollment(name)
    code = _code(enrollment["secret"], at=int(time.time()) - 30)
    assert two_factor.confirm_enrollment(enrollment["name"], code)
    return enrollment["name"], enrollment["secret"]


def _code(secret: str, at: int | None = None) -> str:
    totp = pyotp.TOTP(secret)
    return totp.at(at) if at else totp.now()


def test_secret_is_valid_base32_and_usable(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    enrollment = two_factor.start_enrollment("phone")

    # pyotp only rejects a non-base32 secret when a code is generated, not at construction.
    assert pyotp.TOTP(enrollment["secret"]).now()


def test_nothing_is_written_until_enrollment_starts(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    assert two_factor.is_enabled is False
    assert two_factor.get_credentials() == []
    assert not (tmp_path / TotpCredentialStore.FILENAME).exists()


def test_enrollment_requires_a_label(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    with pytest.raises(TwoFactorError):
        two_factor.start_enrollment("   ")


def test_two_factor_is_only_enabled_after_a_valid_code(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    enrollment = two_factor.start_enrollment("phone")

    assert two_factor.confirm_enrollment(enrollment["name"], "000000") is False
    assert two_factor.is_enabled is False

    assert two_factor.confirm_enrollment(enrollment["name"], _code(enrollment["secret"])) is True
    assert two_factor.is_enabled is True


def test_a_pending_credential_cannot_authenticate(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    enrollment = two_factor.start_enrollment("phone")

    assert two_factor.verify_otp(_code(enrollment["secret"])) is False


def test_a_credential_cannot_be_confirmed_twice(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    credential_id, secret = _enroll(two_factor)

    assert two_factor.confirm_enrollment(credential_id, _code(secret)) is False


def test_each_device_has_its_own_secret(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    _, first = _enroll(two_factor, "phone")
    _, second = _enroll(two_factor, "laptop")

    assert first != second


def test_two_devices_can_sign_in_within_the_same_window(tmp_path: Path) -> None:
    """The reason for per-device secrets: one shared secret would serialise logins."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, first = _enroll(two_factor, "phone")
    _, second = _enroll(two_factor, "laptop")
    moment = int(time.time())

    assert two_factor.verify_otp(_code(first, at=moment)) is True
    assert two_factor.verify_otp(_code(second, at=moment)) is True


def test_a_code_cannot_be_used_twice(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, secret = _enroll(two_factor)
    code = _code(secret)

    assert two_factor.verify_otp(code) is True
    assert two_factor.verify_otp(code) is False


def test_an_earlier_code_is_rejected_after_a_later_one(tmp_path: Path) -> None:
    """Replay protection burns the time step, not just the code string."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, secret = _enroll(two_factor)
    now = int(time.time())

    # A later in-window code first, then an earlier one that is still inside the window.
    assert two_factor.verify_otp(_code(secret, at=now + 30)) is True
    assert two_factor.verify_otp(_code(secret, at=now)) is False


def test_burning_one_device_does_not_block_another(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, first = _enroll(two_factor, "phone")
    _, second = _enroll(two_factor, "laptop")
    now = int(time.time())

    assert two_factor.verify_otp(_code(first, at=now)) is True
    assert two_factor.verify_otp(_code(first, at=now)) is False
    assert two_factor.verify_otp(_code(second, at=now)) is True


def test_concurrent_use_of_one_code_is_accepted_once(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, secret = _enroll(two_factor)
    code = _code(secret)

    with ThreadPoolExecutor(max_workers=8) as pool:
        results = list(pool.map(lambda _: two_factor.verify_otp(code), range(8)))

    assert sum(results) == 1


def test_clock_drift_within_one_step_is_accepted(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    enrollment = two_factor.start_enrollment("phone")
    two_factor.confirm_enrollment(enrollment["name"], _code(enrollment["secret"], at=int(time.time()) - 30))

    assert two_factor.is_enabled is True


def test_codes_beyond_the_drift_window_are_rejected(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, secret = _enroll(two_factor)

    assert two_factor.verify_otp(_code(secret, at=int(time.time()) - 120)) is False


def test_verification_without_any_device_fails_closed(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    assert two_factor.verify_otp("000000") is False
    assert two_factor.verify_otp("") is False


def test_removing_the_last_device_turns_two_factor_off(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    phone, _ = _enroll(two_factor, "phone")
    laptop, laptop_secret = _enroll(two_factor, "laptop")

    assert two_factor.remove_credential(phone) is True
    assert two_factor.is_enabled is True

    assert two_factor.remove_credential(laptop) is True
    assert two_factor.is_enabled is False
    assert two_factor.verify_otp(_code(laptop_secret)) is False


def test_removing_an_unknown_device_reports_failure(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    assert two_factor.remove_credential("nope") is False


def test_credentials_never_expose_secrets(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _enroll(two_factor, "phone")
    two_factor.start_enrollment("pending laptop")

    rows = two_factor.get_credentials()

    assert {row["name"] for row in rows} == {"phone", "pending laptop"}
    assert {row["confirmed"] for row in rows} == {True, False}
    assert not any("secret" in row for row in rows)


def test_abandoned_enrollments_are_pruned(tmp_path: Path) -> None:
    bench = _bench(tmp_path)
    two_factor = TwoFactorAuthentication(bench)
    stale = two_factor.start_enrollment("never confirmed")["name"]
    confirmed, _ = _enroll(two_factor, "phone")

    path = tmp_path / TotpCredentialStore.FILENAME
    entries = json.loads(path.read_text())
    entries[stale]["created_at"] = int(time.time()) - PENDING_TTL - 1
    path.write_text(json.dumps(entries))

    # Filtered out of every read, without the read path rewriting the file.
    assert set(two_factor.store.all()) == {confirmed}
    assert set(json.loads(path.read_text())) == {stale, confirmed}

    # The next write is what actually drops it from disk.
    two_factor.start_enrollment("laptop")
    assert stale not in json.loads(path.read_text())


def test_a_confirmed_credential_is_never_pruned(tmp_path: Path) -> None:
    bench = _bench(tmp_path)
    two_factor = TwoFactorAuthentication(bench)
    confirmed, _ = _enroll(two_factor, "phone")

    path = tmp_path / TotpCredentialStore.FILENAME
    entries = json.loads(path.read_text())
    entries[confirmed]["created_at"] = int(time.time()) - PENDING_TTL * 10
    path.write_text(json.dumps(entries))

    assert set(two_factor.store.all()) == {confirmed}


def test_provisioning_url_names_the_bench_and_device(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    enrollment = two_factor.start_enrollment("Ops laptop")

    assert enrollment["provisioning_url"].startswith("otpauth://totp/")
    assert f"issuer=Pilot%20-%20{tmp_path.name}" in enrollment["provisioning_url"]
    assert enrollment["secret"] in enrollment["provisioning_url"]
    assert "Ops%20laptop" in enrollment["provisioning_url"]


def test_state_survives_a_fresh_object(tmp_path: Path) -> None:
    bench = _bench(tmp_path)
    _enroll(TwoFactorAuthentication(bench), "phone")

    assert TwoFactorAuthentication(_bench(tmp_path)).is_enabled is True


def test_recovery_codes_are_generated_and_persisted(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    codes = two_factor.generate_recovery_codes()

    assert len(codes) == RECOVERY_CODE_COUNT
    assert len(set(codes)) == RECOVERY_CODE_COUNT
    assert BenchConfig.from_file(tmp_path / "bench.toml").admin.recovery_codes == codes


def test_a_recovery_code_works_once(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    codes = two_factor.generate_recovery_codes()

    assert two_factor.redeem_recovery_code(codes[3]) is True
    assert two_factor.redeem_recovery_code(codes[3]) is False
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT - 1
    assert codes[3] not in BenchConfig.from_file(tmp_path / "bench.toml").admin.recovery_codes


def test_unknown_and_empty_recovery_codes_are_rejected(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    two_factor.generate_recovery_codes()

    assert two_factor.redeem_recovery_code("not-a-real-code") is False
    assert two_factor.redeem_recovery_code("   ") is False
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT


def test_regenerating_invalidates_the_previous_set(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    old = two_factor.generate_recovery_codes()

    new = two_factor.generate_recovery_codes()

    assert set(old).isdisjoint(new)
    assert two_factor.redeem_recovery_code(old[0]) is False
    assert two_factor.redeem_recovery_code(new[0]) is True


def test_recovery_codes_survive_a_fresh_object(tmp_path: Path) -> None:
    codes = TwoFactorAuthentication(_bench(tmp_path)).generate_recovery_codes()

    # Re-read bench.toml rather than rewriting it, the way a later request would.
    reloaded = Bench(BenchConfig.from_file(tmp_path / "bench.toml"), tmp_path)
    assert TwoFactorAuthentication(reloaded).redeem_recovery_code(codes[0]) is True


def test_second_factor_accepts_an_authenticator_code(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, secret = _enroll(two_factor)
    two_factor.generate_recovery_codes()

    assert two_factor.verify_second_factor(_code(secret)) is True
    # The TOTP path was taken, so no recovery code was spent.
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT


def test_second_factor_falls_back_to_a_recovery_code(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _enroll(two_factor)
    codes = two_factor.generate_recovery_codes()

    assert two_factor.verify_second_factor(codes[0]) is True
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT - 1
    assert two_factor.verify_second_factor(codes[0]) is False


def test_second_factor_rejects_anything_else(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _enroll(two_factor)
    two_factor.generate_recovery_codes()

    assert two_factor.verify_second_factor("000000") is False
    assert two_factor.verify_second_factor("") is False
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT


def test_non_ascii_codes_are_rejected_not_raised(tmp_path: Path) -> None:
    """compare_digest raises on non-ASCII input, which would 500 the sign-in form."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _enroll(two_factor)
    two_factor.generate_recovery_codes()

    assert two_factor.redeem_recovery_code("café-code-xyz") is False
    assert two_factor.redeem_recovery_code("—dash—") is False
    # Both paths matter: the TOTP field is the one people paste into.
    assert two_factor.verify_otp("café12") is False
    assert two_factor.verify_second_factor("café-code-xyz") is False
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT


def test_enrollment_stops_at_the_device_limit(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    for index in range(MAX_ENROLLED_DEVICES):
        two_factor.start_enrollment(f"device {index}")

    with pytest.raises(TwoFactorError) as error:
        two_factor.start_enrollment("one too many")

    assert str(MAX_ENROLLED_DEVICES) in str(error.value)


def test_removing_a_device_frees_a_slot(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    first = two_factor.start_enrollment("device 0")["name"]
    for index in range(1, MAX_ENROLLED_DEVICES):
        two_factor.start_enrollment(f"device {index}")

    two_factor.remove_credential(first)

    assert two_factor.start_enrollment("replacement")["name"]


def test_a_duplicate_device_name_is_rejected(tmp_path: Path) -> None:
    """Overwriting would swap the secret out from under a working device."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _, secret = _enroll(two_factor, "phone")

    with pytest.raises(TwoFactorError):
        two_factor.start_enrollment("phone")

    assert two_factor.verify_otp(_code(secret)) is True


def test_duplicate_names_ignore_case_and_spacing(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    two_factor.start_enrollment("My Phone")

    for variant in ("my phone", "MY PHONE", "  My   Phone  "):
        with pytest.raises(TwoFactorError):
            two_factor.start_enrollment(variant)


def test_device_names_are_normalised(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    assert two_factor.start_enrollment("  My   Phone  ")["name"] == "My Phone"


def test_device_names_must_be_addressable(tmp_path: Path) -> None:
    """The name is the key and travels in the URL path, so it stays to safe characters."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))

    for bad in ("phone/laptop", "null\x00byte", "bell\x07here", "x" * 41):
        with pytest.raises(TwoFactorError):
            two_factor.start_enrollment(bad)

    # Everything else survives percent-encoding, so punctuation and unicode are fine.
    assert two_factor.start_enrollment("new\nline")["name"] == "new line"
    assert two_factor.start_enrollment("Aradhya's iPhone")["name"] == "Aradhya's iPhone"
    assert two_factor.start_enrollment("Phone (work)")["name"] == "Phone (work)"
    assert two_factor.start_enrollment("ラップトップ")["name"] == "ラップトップ"


def test_a_freed_name_can_be_reused(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _enroll(two_factor, "phone")

    two_factor.remove_credential("phone")

    assert two_factor.start_enrollment("phone")["name"] == "phone"


def test_the_store_is_keyed_by_device_name(tmp_path: Path) -> None:
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    _enroll(two_factor, "Ops laptop")

    stored = json.loads((tmp_path / TotpCredentialStore.FILENAME).read_text())

    assert list(stored) == ["Ops laptop"]
    assert "label" not in stored["Ops laptop"]


def test_removing_the_last_device_discards_recovery_codes(tmp_path: Path) -> None:
    """Codes that bypass a gate which is now off protect nothing."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    phone, _ = _enroll(two_factor, "phone")
    laptop, _ = _enroll(two_factor, "laptop")
    codes = two_factor.generate_recovery_codes()

    two_factor.remove_credential(phone)
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT

    two_factor.remove_credential(laptop)
    assert two_factor.unused_recovery_code_count == 0
    assert BenchConfig.from_file(tmp_path / "bench.toml").admin.recovery_codes == []
    assert two_factor.redeem_recovery_code(codes[0]) is False


def test_asking_whether_two_factor_is_enabled_never_writes(tmp_path: Path) -> None:
    """Every sign-in asks this, so it must stay a pure read."""
    two_factor = TwoFactorAuthentication(_bench(tmp_path))
    two_factor.generate_recovery_codes()
    toml_path = tmp_path / "bench.toml"
    before = toml_path.read_text()

    assert two_factor.is_enabled is False

    assert toml_path.read_text() == before
    assert two_factor.unused_recovery_code_count == RECOVERY_CODE_COUNT
