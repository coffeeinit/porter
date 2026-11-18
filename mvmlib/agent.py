"""Host side of the guest agent protocol (see guest/mvm-guest.c).
Transport: Firecracker's vsock proxy - connect to the unix socket, send 'CONNECT 52\\n'."""
import socket
import struct

PORT = 52
OP_PING, OP_EXEC, OP_PUT, OP_GET, OP_STATS, OP_SHUTDOWN = 1, 2, 3, 4, 5, 6
F_OUT, F_ERR, F_EXIT, F_DATA, F_ERROR, F_INFO = 1, 2, 3, 4, 5, 6
MAX_CAPTURE = 8 << 20


class AgentError(RuntimeError):
    pass


def _connect(uds, timeout):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(timeout)
    try:
        s.connect(str(uds))
        s.sendall(f"CONNECT {PORT}\n".encode())
        buf = b""
        while not buf.endswith(b"\n"):
            c = s.recv(64)
            if not c:
                raise AgentError("agent closed during handshake (is the guest running mvm-guest?)")
            buf += c
            if len(buf) > 64:
                break
        if not buf.startswith(b"OK"):
            raise AgentError(f"vsock handshake failed: {buf!r}")
    except (OSError, AgentError) as e:
        s.close()
        if isinstance(e, AgentError):
            raise
        raise AgentError(f"cannot reach guest agent: {e}") from None
    return s


def _recv_exact(s, n):
    b = bytearray()
    while len(b) < n:
        c = s.recv(n - len(b))
        if not c:
            raise AgentError("agent connection closed mid-response")
        b += c
    return bytes(b)


def _frames(s):
    while True:
        h = _recv_exact(s, 5)
        t, n = h[0], struct.unpack(">I", h[1:])[0]
        d = _recv_exact(s, n) if n else b""
        yield t, d
        if t in (F_EXIT, F_ERROR):
            return


def _req(op, fields=(), body=None):
    d = bytes([op]) + struct.pack(">I", len(fields) + (1 if body is not None else 0))
    for f in fields:
        f = f.encode() if isinstance(f, str) else f
        d += struct.pack(">I", len(f)) + f
    if body is not None:
        d += struct.pack(">I", len(body))
    return d


def ping(uds, timeout=3):
    s = _connect(uds, timeout)
    try:
        s.sendall(_req(OP_PING))
        info = None
        for t, d in _frames(s):          # read to the end so the guest never writes into a closed socket
            if t == F_INFO:
                info = d.decode()
            elif t == F_ERROR:
                raise AgentError(d.decode())
        return info
    finally:
        s.close()


def exec_(uds, cmd, workdir="/", timeout=60):
    s = _connect(uds, 10)
    try:
        s.sendall(_req(OP_EXEC, [cmd, workdir or "/", str(int(timeout))]))
        s.settimeout(timeout + 15)
        out, err, code, cap = bytearray(), bytearray(), None, 0
        for t, d in _frames(s):
            if t == F_OUT and cap < MAX_CAPTURE:
                out += d
                cap += len(d)
            elif t == F_ERR and cap < MAX_CAPTURE:
                err += d
                cap += len(d)
            elif t == F_EXIT:
                code = struct.unpack(">i", d)[0]
            elif t == F_ERROR:
                raise AgentError(d.decode("utf-8", "replace"))
        return {"exit_code": code, "stdout": out.decode("utf-8", "replace"), "stderr": err.decode("utf-8", "replace")}
    finally:
        s.close()


def put_file(uds, path, data, mode="644"):
    s = _connect(uds, 10)
    try:
        s.settimeout(120)
        s.sendall(_req(OP_PUT, [path, mode], body=data))
        s.sendall(data)
        for t, d in _frames(s):
            if t == F_ERROR:
                raise AgentError(d.decode("utf-8", "replace"))
        return True
    finally:
        s.close()


def put_stream(uds, path, fileobj, size, mode="644", chunk=1 << 20):
    """Upload a large file without holding it in memory (build contexts can be hundreds of MB)."""
    s = _connect(uds, 10)
    try:
        s.settimeout(600)
        pb, mb = path.encode(), mode.encode()
        s.sendall(bytes([OP_PUT]) + struct.pack(">I", 3) + struct.pack(">I", len(pb)) + pb + struct.pack(">I", len(mb)) + mb + struct.pack(">I", size))
        sent = 0
        while sent < size:
            b = fileobj.read(min(chunk, size - sent))
            if not b:
                raise AgentError("source file shorter than announced")
            s.sendall(b)
            sent += len(b)
        for t, d in _frames(s):
            if t == F_ERROR:
                raise AgentError(d.decode("utf-8", "replace"))
        return True
    finally:
        s.close()


def get_stream(uds, path, dest, limit=8 << 30):
    """Download a large file straight to disk (exported images)."""
    s = _connect(uds, 10)
    try:
        s.settimeout(600)
        s.sendall(_req(OP_GET, [path]))
        n = 0
        with open(dest, "wb") as out:
            for t, d in _frames(s):
                if t == F_DATA:
                    n += len(d)
                    if n > limit:
                        raise AgentError("file larger than the download limit")
                    out.write(d)
                elif t == F_ERROR:
                    raise AgentError(d.decode("utf-8", "replace"))
        return n
    finally:
        s.close()


def get_file(uds, path, limit=256 << 20):
    s = _connect(uds, 10)
    try:
        s.settimeout(120)
        s.sendall(_req(OP_GET, [path]))
        buf = bytearray()
        for t, d in _frames(s):
            if t == F_DATA:
                buf += d
                if len(buf) > limit:
                    raise AgentError("file too large for download (limit 256 MiB)")
            elif t == F_ERROR:
                raise AgentError(d.decode("utf-8", "replace"))
        return bytes(buf)
    finally:
        s.close()


def stats(uds, timeout=4):
    s = _connect(uds, timeout)
    try:
        s.sendall(_req(OP_STATS))
        text = None
        for t, d in _frames(s):
            if t == F_INFO:
                text = d.decode("utf-8", "replace")
            elif t == F_ERROR:
                raise AgentError(d.decode())
        return parse_stats(text) if text is not None else None
    finally:
        s.close()


def shutdown(uds):
    s = _connect(uds, 5)
    try:
        s.sendall(_req(OP_SHUTDOWN))
        for _ in _frames(s):
            pass
    except AgentError:
        pass
    finally:
        s.close()


def parse_stats(text):
    sec, cur = {}, None
    for line in text.splitlines():
        if line.startswith("==== "):
            cur = line[5:].strip()
            sec[cur] = []
        elif cur is not None and line.strip():
            sec[cur].append(line)
    r = {}
    try:
        la = sec.get("loadavg", [""])[0].split()
        r["load1"], r["load5"], r["load15"] = (float(x) for x in la[:3])
    except (ValueError, IndexError):
        pass
    try:
        r["uptime_s"] = float(sec.get("uptime", [""])[0].split()[0])
    except (ValueError, IndexError):
        pass
    mem = {}
    for line in sec.get("meminfo", []):
        k, _, v = line.partition(":")
        try:
            mem[k] = int(v.split()[0]) * 1024
        except (ValueError, IndexError):
            pass
    if mem:
        r["mem_total"] = mem.get("MemTotal", 0)
        r["mem_available"] = mem.get("MemAvailable", mem.get("MemFree", 0))
        r["mem_used"] = r["mem_total"] - r["mem_available"]
        r["swap_total"], r["swap_free"] = mem.get("SwapTotal", 0), mem.get("SwapFree", 0)
    rx = tx = 0
    for line in sec.get("netdev", [])[2:]:
        name, _, rest = line.partition(":")
        if name.strip() in ("lo", ""):
            continue
        f = rest.split()
        try:
            rx += int(f[0])
            tx += int(f[8])
        except (ValueError, IndexError):
            pass
    r["net_rx"], r["net_tx"] = rx, tx
    for line in sec.get("stat", []):
        if line.startswith("cpu "):
            f = [int(x) for x in line.split()[1:9]]
            r["cpu_total"], r["cpu_idle"] = sum(f), f[3] + f[4]
            break
    rd = wr = 0
    for line in sec.get("diskstats", []):
        f = line.split()
        if len(f) > 9 and f[2].startswith("vd") and not f[2][-1].isdigit():
            rd += int(f[5]) * 512
            wr += int(f[9]) * 512
    r["disk_read"], r["disk_write"] = rd, wr
    try:
        total, free = (int(x) for x in sec.get("statfs", [""])[0].split()[:2])
        r["fs_total"], r["fs_used"] = total, total - free
    except (ValueError, IndexError):
        pass
    return r
