#!/usr/bin/env python3
"""Test double for Firecracker: speaks the same HTTP API on a unix socket and bridges vsock to the REAL
guest agent (mvm-guest --listen-unix). It does not boot anything - it exists so the control plane can be
tested on machines without KVM. NEVER used in production."""
import json, os, shutil, signal, socket, socketserver, subprocess, sys, tempfile, threading, time
from http.server import BaseHTTPRequestHandler

args = sys.argv[1:]
SOCK = args[args.index("--api-sock") + 1] if "--api-sock" in args else "/run/firecracker.socket"
VID = args[args.index("--id") + 1] if "--id" in args else "anon"
GUEST = os.environ.get("FAKE_FC_GUEST", os.path.join(os.path.dirname(__file__), "..", "guest", "mvm-guest"))
S = {"state": "Not started", "cfg": {}, "balloon": None, "mmds": {}, "metrics": None, "logger": None}
agent_proc = None
agent_sock = tempfile.mktemp(prefix="agent-", dir="/tmp")


def bye(*_):
    print("MVM: shutdown", flush=True)
    if agent_proc:
        agent_proc.kill()
    try:
        os.unlink(SOCK)
    except OSError:
        pass
    os._exit(0)


signal.signal(signal.SIGTERM, bye)


def start_vsock(path):
    global agent_proc
    if agent_proc:
        return
    agent_proc = subprocess.Popen([GUEST, "--listen-unix", agent_sock], stderr=subprocess.DEVNULL)
    time.sleep(0.2)
    try:
        os.unlink(path)
    except OSError:
        pass
    ls = socket.socket(socket.AF_UNIX)
    ls.bind(path)
    ls.listen(16)

    def pipe(a, b, first=False):
        try:
            while True:
                d = a.recv(65536)
                if not d:
                    break
                if first and d[:1] == b"\x06":
                    threading.Timer(0.3, bye).start()
                first = False
                b.sendall(d)
        except OSError:
            pass
        try:
            b.shutdown(socket.SHUT_WR)
        except OSError:
            pass

    def serve():
        while True:
            c, _ = ls.accept()
            line = b""
            while not line.endswith(b"\n"):
                x = c.recv(1)
                if not x:
                    break
                line += x
            if not line.startswith(b"CONNECT 52"):
                c.sendall(b"BAD\n"); c.close(); continue
            c.sendall(b"OK 1073741824\n")
            u = socket.socket(socket.AF_UNIX)
            u.connect(agent_sock)
            threading.Thread(target=pipe, args=(c, u, True), daemon=True).start()
            threading.Thread(target=pipe, args=(u, c), daemon=True).start()
    threading.Thread(target=serve, daemon=True).start()


class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def address_string(self): return "unix"

    def reply(self, code=204, obj=None):
        data = json.dumps(obj).encode() if obj is not None else b""
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def fail(self, msg, code=400): self.reply(code, {"fault_message": msg})

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}") if n else {}

    def do_GET(self):
        p = self.path
        if p == "/": return self.reply(200, {"id": VID, "state": S["state"], "vmm_version": "fake-1.15.0", "app_name": "Firecracker"})
        if p == "/version": return self.reply(200, {"firecracker_version": "fake-1.15.0"})
        if p == "/machine-config": return self.reply(200, S["cfg"].get("machine", {}))
        if p == "/vm/config": return self.reply(200, S["cfg"])
        if p == "/balloon":
            return self.reply(200, S["balloon"]) if S["balloon"] else self.fail("balloon not configured")
        if p == "/balloon/statistics":
            return self.reply(200, {"target_pages": 0, "actual_pages": 0, "target_mib": S["balloon"]["amount_mib"], "actual_mib": S["balloon"]["amount_mib"], "free_memory": 123456})
        if p == "/mmds": return self.reply(200, S["mmds"])
        if p == "/hotplug/memory": return self.reply(200, S["hotplug"]) if S.get("hotplug") else self.fail("not configured")
        self.fail("no such endpoint", 404)

    def do_PUT(self):
        p, b = self.path, self.body()
        pre = S["state"] == "Not started"
        if p == "/actions":
            a = b.get("action_type")
            if a == "InstanceStart":
                if "boot" not in S["cfg"] or "drives" not in S["cfg"]: return self.fail("missing boot source or root drive")
                S["state"] = "Running"; return self.reply()
            if a == "FlushMetrics":
                if S["metrics"]:
                    open(S["metrics"], "a").write(json.dumps({"utc_timestamp_ms": int(time.time()*1000), "net": {"rx_bytes_count": 1234, "tx_bytes_count": 4321}, "block": {"read_bytes": 4096, "write_bytes": 8192}, "api_server": {"process_startup_time_us": 1000}}) + "\n")
                return self.reply()
            if a == "SendCtrlAltDel": threading.Timer(0.3, bye).start(); return self.reply()
            return self.fail("bad action")
        if p == "/snapshot/load":
            if not pre: return self.fail("snapshot can only be loaded pre-boot")
            for k in ("snapshot_path",):
                if not os.path.exists(b[k]): return self.fail(f"{k} missing")
            if not os.path.exists(b["mem_backend"]["backend_path"]): return self.fail("mem missing")
            S["state"] = "Paused" if not b.get("resume_vm") else "Running"
            S["vsock"] = S.get("vsock") or os.path.join(os.path.dirname(SOCK), "vsock.sock")   # real FC restores the device from the snapshot
            start_vsock(S["vsock"])
            return self.reply()
        if p == "/snapshot/create":
            if S["state"] != "Paused": return self.fail("snapshot only on a paused VM")
            open(b["snapshot_path"], "w").write("vmstate-" + VID)
            with open(b["mem_file_path"], "wb") as f: f.truncate(1 << 20)
            return self.reply()
        if not pre and p.split("/")[1] in ("boot-source", "drives", "machine-config", "vsock", "metrics", "logger", "entropy", "mmds", "hotplug"):
            if p.startswith("/mmds") and p != "/mmds/config": S["mmds"] = b; return self.reply()
            return self.fail("The requested operation is not supported after starting the microVM.")
        seg = p.split("/")[1]
        if seg == "boot-source":
            if not os.path.exists(b["kernel_image_path"]): return self.fail("kernel not found")
            S["cfg"]["boot"] = b
        elif seg == "drives":
            if not os.path.exists(b["path_on_host"]): return self.fail("drive file not found")
            S["cfg"].setdefault("drives", []).append(b)
        elif seg == "machine-config": S["cfg"]["machine"] = b
        elif seg == "network-interfaces": S["cfg"].setdefault("nics", []).append(b)
        elif seg == "vsock": S["vsock"] = b["uds_path"]; start_vsock(b["uds_path"])
        elif seg == "balloon": S["balloon"] = b
        elif seg == "metrics": S["metrics"] = b["metrics_path"]; open(S["metrics"], "a").close()
        elif seg == "logger": S["logger"] = b["log_path"]; open(S["logger"], "a").write("fake log started\n")
        elif seg == "mmds":
            if p == "/mmds": S["mmds"] = b
        elif seg == "entropy": pass
        elif seg == "hotplug": S["hotplug"] = {"total_size_mib": b["total_size_mib"], "slot_size_mib": b["slot_size_mib"], "plugged_size_mib": 0, "requested_size_mib": 0}
        else: return self.fail("unknown endpoint", 404)
        self.reply()

    def do_PATCH(self):
        p, b = self.path, self.body()
        if p == "/vm":
            if S["state"] == "Not started": return self.fail("not started")
            S["state"] = "Paused" if b["state"] == "Paused" else "Running"; return self.reply()
        if p == "/balloon":
            if not S["balloon"]: return self.fail("balloon not configured")
            S["balloon"]["amount_mib"] = b["amount_mib"]; return self.reply()
        if p == "/hotplug/memory":
            if not S.get("hotplug"): return self.fail("not configured")
            S["hotplug"]["requested_size_mib"] = b["requested_size_mib"]; S["hotplug"]["plugged_size_mib"] = b["requested_size_mib"]; return self.reply()
        if p.startswith("/network-interfaces") or p.startswith("/drives"): return self.reply()
        if p == "/mmds": S["mmds"].update(b); return self.reply()
        self.fail("unknown endpoint", 404)


class Srv(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


try:
    os.unlink(SOCK)
except OSError:
    pass
os.makedirs(os.path.dirname(SOCK), exist_ok=True)
print(f"fake firecracker {VID} up", flush=True)
Srv(SOCK, H).serve_forever()
