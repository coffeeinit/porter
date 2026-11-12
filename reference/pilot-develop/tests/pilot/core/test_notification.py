import stat
from datetime import UTC, datetime

import pytest

from pilot.core.notification import NotificationStore


def _store(tmp_path):
    return NotificationStore(tmp_path / "logs")


def _create(store, title, **kwargs):
    kwargs.setdefault("category", "Tasks")
    kwargs.setdefault("event", "task_failed")
    return store.create(title, **kwargs)


def test_create_and_read_newest_first(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "first")
    _create(store, "second")

    assert [item.title for item in store.list(10)] == ["second", "first"]
    assert all(not item.is_read for item in store.list(10))


def test_writes_to_the_current_iso_week_file_privately(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "a")

    year, week, _ = datetime.now(UTC).isocalendar()
    path = tmp_path / "logs" / f"notifications_{year}_{week:02d}.jsonl"
    assert path.is_file()
    assert stat.S_IMODE(path.stat().st_mode) == 0o600


def test_rejects_unknown_category_and_severity(tmp_path) -> None:
    store = _store(tmp_path)
    with pytest.raises(ValueError):
        store.create("a", category="Billing", event="x")
    with pytest.raises(ValueError):
        store.create("a", category="Tasks", event="x", severity="Critical")


def test_names_are_unique_within_the_same_millisecond(tmp_path) -> None:
    store = _store(tmp_path)
    names = {_create(store, f"n{i}").name for i in range(50)}
    assert len(names) == 50


def test_mark_read_only_affects_that_notification(tmp_path) -> None:
    store = _store(tmp_path)
    first = _create(store, "first")
    _create(store, "second")

    store.mark_read(first.name)

    by_title = {item.title: item.is_read for item in store.list(10)}
    assert by_title == {"first": True, "second": False}
    assert store.unread_count == 1


def test_mark_all_read_covers_everything_already_written(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "first")
    _create(store, "second")

    store.mark_all_read()

    assert store.unread_count == 0
    assert all(item.is_read for item in store.list(10))


def test_mark_all_read_does_not_swallow_later_notifications(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "old")
    store.mark_all_read()
    _create(store, "new")

    assert store.unread_count == 1
    assert [item.title for item in store.list(10, unread_only=True)] == ["new"]


def test_empty_feed_reads_and_marks_without_a_logs_directory(tmp_path) -> None:
    """Nothing has been logged yet, so `logs/` may not exist - the feed still works."""
    store = _store(tmp_path)

    assert store.list(10) == []
    assert store.unread_count == 0
    store.mark_all_read()
    store.mark_read("never-written")

    assert store.unread_count == 0


def test_read_state_survives_a_new_store_instance(tmp_path) -> None:
    store = _store(tmp_path)
    first = _create(store, "first")
    store.mark_read(first.name)

    assert _store(tmp_path).unread_count == 0


def test_filters_by_category_and_unread(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "site down", category="Sites", event="site_down")
    read_one = _create(store, "update failed", category="Updates", event="task_failed")
    _create(store, "backup failed", category="Sites", event="task_failed")
    store.mark_read(read_one.name)

    assert [item.title for item in store.list(10, category="Sites")] == ["backup failed", "site down"]
    assert [item.title for item in store.list(10, unread_only=True)] == ["backup failed", "site down"]
    assert [item.title for item in store.list(10, category="Updates", unread_only=True)] == []


def test_limit_is_exact_when_filtering(tmp_path) -> None:
    """A page must fill from the whole log, not from the newest `limit` rows."""
    store = _store(tmp_path)
    for index in range(20):
        category = "Sites" if index % 2 else "Server"
        _create(store, f"n{index}", category=category, event="task_failed")

    assert len(store.list(5, category="Server")) == 5


def test_corrupt_and_partial_records_are_skipped(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "good")
    shard = next((tmp_path / "logs").glob("notifications_*.jsonl"))
    with shard.open("a") as handle:
        handle.write("not json\n")
        handle.write('{"title": "no name or timestamp"}\n')

    assert [item.title for item in store.list(10)] == ["good"]


def test_hand_edited_read_state_reads_as_nothing_read(tmp_path) -> None:
    store = _store(tmp_path)
    _create(store, "first")
    store.read_state.path.write_text("{ broken")

    assert store.unread_count == 1


def test_unread_count_is_capped(tmp_path) -> None:
    from pilot.core.notification import UNREAD_SCAN_LIMIT

    store = _store(tmp_path)
    for index in range(UNREAD_SCAN_LIMIT + 10):
        _create(store, f"n{index}")

    assert store.unread_count == UNREAD_SCAN_LIMIT


def test_bench_owns_the_store(tmp_path) -> None:
    from pilot.config import BenchConfig
    from pilot.core.bench import Bench

    bench = Bench(BenchConfig.from_flat("t", {}), tmp_path)
    bench.notifications.create("failed", category="Tasks", event="task_failed", severity="Error")

    assert [item.title for item in bench.notifications.list(10)] == ["failed"]


def test_concurrent_marks_do_not_drop_each_other(tmp_path) -> None:
    """Read state is a read-modify-write, and the admin backend serves four threads.
    Without the file lock the later writer replaces the snapshot it started from."""
    import threading

    store = _store(tmp_path)
    names = [_create(store, f"n{index}").name for index in range(25)]
    ready = threading.Barrier(len(names))

    def mark(name: str) -> None:
        ready.wait()
        store.mark_read(name)

    threads = [threading.Thread(target=mark, args=(name,)) for name in names]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()

    assert store.unread_count == 0


def test_mark_all_read_keeps_a_mark_made_while_it_waited_for_the_lock(tmp_path) -> None:
    """The watermark has to be taken under the lock. Taken before, a notification
    created after it but marked read before the write loses its explicit mark."""
    from contextlib import contextmanager
    from unittest.mock import patch

    from pilot.core import notification as notification_module

    store = _store(tmp_path)
    real_lock = notification_module.exclusive_file_lock
    intercepted: list[bool] = []

    @contextmanager
    def lock_once_a_newer_mark_landed(path):
        if not intercepted:
            intercepted.append(True)
            newer = _create(store, "arrived while mark-all waited")
            store.mark_read(newer.name)
        with real_lock(path):
            yield

    with patch.object(notification_module, "exclusive_file_lock", lock_once_a_newer_mark_landed):
        store.mark_all_read()

    assert store.unread_count == 0


def test_create_holds_the_feed_lock_while_it_stamps_and_appends(tmp_path) -> None:
    """Stamping outside the lock leaves a window: mark-all can write a later watermark
    before the append lands, and the record is born read and never shows up at all."""
    from unittest.mock import patch

    from pilot.internal.atomic_file import exclusive_file_lock
    from pilot.internal.jsonl_log import JsonlLog

    store = _store(tmp_path)
    real_append = JsonlLog.append
    locked_during_append: list[bool] = []

    def append_probing_the_lock(self, record):
        try:
            with exclusive_file_lock(store.read_state.path, blocking=False):
                locked_during_append.append(False)
        except BlockingIOError:
            locked_during_append.append(True)
        real_append(self, record)

    with patch.object(JsonlLog, "append", append_probing_the_lock):
        _create(store, "written under the feed lock")

    assert locked_during_append == [True]
    assert store.unread_count == 1
