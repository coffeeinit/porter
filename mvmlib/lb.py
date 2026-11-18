"""Tiny round-robin TCP load balancer for replica sets (user-space, no extra privileges).
Listens on bind:port, forwards each connection to the next healthy replica; a replica that refuses
the connection is skipped. Binds 127.0.0.1 unless the set explicitly asks for 0.0.0.0."""
import itertools
import socket
import threading

MAX_CONN = 256
ALGOS = ("round_robin", "least_conn")


class Balancer:
    def __init__(self, bind, port, service_port, backends, algo="round_robin"):
        self.bind, self.port, self.service_port, self.backends = bind, port, service_port, backends
        self.algo = algo if algo in ALGOS else "round_robin"
        self.live = {}                                   # backend name -> active connections (least_conn)
        self.sock = None
        self.stats = {"total": 0, "active": 0, "failed": 0, "per": {}}
        self._rr = itertools.count()
        self._sem = threading.Semaphore(MAX_CONN)
        self._lock = threading.Lock()
        self._stop = False

    def start(self):
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((self.bind, self.port))
        s.listen(128)
        self.sock = s
        threading.Thread(target=self._accept, daemon=True, name=f"lb-{self.port}").start()

    def stop(self):
        self._stop = True
        try:
            self.sock.close()
        except (OSError, AttributeError):
            pass

    def _accept(self):
        while not self._stop:
            try:
                c, _ = self.sock.accept()
            except OSError:
                return
            if not self._sem.acquire(blocking=False):
                c.close()
                continue
            threading.Thread(target=self._serve, args=(c,), daemon=True).start()

    def _serve(self, c):
        up, name = None, None
        try:
            bs = self.backends()
            if not bs:
                self._bump("failed")
                return
            start = next(self._rr)
            order = [bs[(start + i) % len(bs)] for i in range(len(bs))]
            if self.algo == "least_conn":              # fewest active connections first; ties keep round-robin order
                with self._lock:
                    order.sort(key=lambda b: self.live.get(b[0], 0))
            for name, ip in order:
                try:
                    up = socket.create_connection((ip, self.service_port), timeout=2)
                    break
                except OSError:
                    up = None
            if not up:
                self._bump("failed")
                return
            with self._lock:
                self.stats["total"] += 1
                self.stats["active"] += 1
                self.live[name] = self.live.get(name, 0) + 1
                self.stats["per"][name] = self.stats["per"].get(name, 0) + 1
            c.settimeout(None)
            up.settimeout(None)
            t = threading.Thread(target=self._pipe, args=(up, c), daemon=True)
            t.start()
            self._pipe(c, up)
            t.join(5)
            with self._lock:
                self.stats["active"] -= 1
                self.live[name] = max(0, self.live.get(name, 1) - 1)
        finally:
            for x in (c, up):
                try:
                    if x:
                        x.close()
                except OSError:
                    pass
            self._sem.release()

    def _bump(self, k):
        with self._lock:
            self.stats[k] += 1

    @staticmethod
    def _pipe(a, b):
        try:
            while True:
                d = a.recv(65536)
                if not d:
                    break
                b.sendall(d)
        except OSError:
            pass
        try:
            b.shutdown(socket.SHUT_WR)
        except OSError:
            pass
