"""Background jobs (image builds, kernel download). One heavy job at a time."""
import threading
import time
import uuid

from .util import now

_JOBS = {}
_ORDER = []
_LOCK = threading.Lock()
_RUN = threading.Lock()


def submit(label, fn, kind="build"):
    job = {"id": uuid.uuid4().hex[:8], "label": label, "kind": kind, "status": "queued",
           "log": [], "started": now(), "finished": None, "result": []}
    with _LOCK:
        _JOBS[job["id"]] = job
        _ORDER.append(job["id"])
        while len(_ORDER) > 40:
            _JOBS.pop(_ORDER.pop(0), None)

    def log(msg):
        for line in str(msg).splitlines() or [""]:
            job["log"].append(f"{time.strftime('%H:%M:%S')} {line}")
        del job["log"][:-400]

    def runner():
        with _RUN:
            job["status"] = "running"
            try:
                job["result"] = fn(log) or []
                job["status"] = "done"
            except Exception as e:  # noqa: BLE001 - surfaced to the UI
                job["status"] = "error"
                log(f"ERROR: {e}")
            finally:
                job["finished"] = now()
    threading.Thread(target=runner, daemon=True, name=f"job-{job['id']}").start()
    return job


def get(jid):
    return _JOBS.get(jid)


def list_():
    with _LOCK:
        return [_JOBS[i] for i in reversed(_ORDER) if i in _JOBS]


def busy():
    return any(j["status"] in ("queued", "running") for j in list_())
