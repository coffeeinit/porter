"""Minimal client for the Firecracker HTTP API on a unix socket.
Endpoints follow the official swagger (src/firecracker/swagger/firecracker.yaml)."""
import http.client
import json
import socket


class FCError(RuntimeError):
    def __init__(self, method, path, status, msg):
        super().__init__(f"firecracker {method} {path}: {status} {msg}")
        self.status = status
        self.fault = msg


class _Conn(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self._p = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self._p)


class FC:
    def __init__(self, sock_path, timeout=15):
        self.sock, self.timeout = str(sock_path), timeout

    def _req(self, method, path, body=None):
        c = _Conn(self.sock, self.timeout)
        try:
            payload = None if body is None else json.dumps(body)
            c.request(method, path, payload, {"Content-Type": "application/json", "Accept": "application/json"})
            r = c.getresponse()
            data = r.read().decode("utf-8", "replace")
        finally:
            c.close()
        if r.status >= 300:
            try:
                msg = json.loads(data).get("fault_message", data)
            except ValueError:
                msg = data
            raise FCError(method, path, r.status, msg)
        if not data:
            return {}
        try:
            return json.loads(data)
        except ValueError:
            return {"raw": data}

    def get(self, path): return self._req("GET", path)
    def put(self, path, body): return self._req("PUT", path, body)
    def patch(self, path, body): return self._req("PATCH", path, body)

    # --- convenience wrappers for what the UI uses ---
    def info(self): return self.get("/")
    def version(self): return self.get("/version")
    def vm_config(self): return self.get("/vm/config")
    def action(self, name): return self.put("/actions", {"action_type": name})
    def pause(self): return self.patch("/vm", {"state": "Paused"})
    def resume(self): return self.patch("/vm", {"state": "Resumed"})
    def balloon(self): return self.get("/balloon")
    def balloon_stats(self): return self.get("/balloon/statistics")
    def set_balloon(self, mib): return self.patch("/balloon", {"amount_mib": int(mib)})
    def mmds(self): return self.get("/mmds")
    def set_mmds(self, data): return self.put("/mmds", data)
    def patch_mmds(self, data): return self.patch("/mmds", data)
    def machine(self): return self.get("/machine-config")


def rate_limiter(bandwidth_mbit=0, ops=0):
    """Token-bucket in Firecracker's format. bandwidth in Mbit/s, ops in operations/s. 0 = unlimited."""
    rl = {}
    if bandwidth_mbit and int(bandwidth_mbit) > 0:
        per_sec = int(float(bandwidth_mbit) * 125000)          # bytes per second
        rl["bandwidth"] = {"size": per_sec, "refill_time": 1000, "one_time_burst": per_sec}
    if ops and int(ops) > 0:
        rl["ops"] = {"size": int(ops), "refill_time": 1000, "one_time_burst": int(ops)}
    return rl or None
