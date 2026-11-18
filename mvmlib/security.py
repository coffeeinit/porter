"""Authentication and request hardening.

* One access token (random, 256 bit) - only its SHA-256 is stored, shown once at first start.
* Login sets a signed, HttpOnly, SameSite=Strict session cookie (12 h) that carries a CSRF secret.
* State-changing requests need the X-CSRF-Token header (cookie auth) or an Authorization: Bearer token.
* Origin must match Host on state-changing requests; failed logins are throttled per client address.
"""
import hashlib
import hmac
import json
import os
import secrets
import threading
import time
import urllib.parse

from itsdangerous import BadSignature, URLSafeTimedSerializer

from . import config as C
from .util import secure_write

SESSION_COOKIE = "mvm_session"
SESSION_MAX_AGE = 12 * 3600
_fail = {}
_lock = threading.Lock()


def _h(tok):
    return hashlib.sha256(tok.encode()).hexdigest()


class Auth:
    def __init__(self):
        self.token_hash = None
        self.secret = None
        self.new_token = None
        self._load()

    def _load(self):
        env = os.environ.get("MVM_TOKEN")
        data = {}
        try:
            data = json.loads(C.AUTH_FILE.read_text())
        except (OSError, ValueError):
            pass
        if not data.get("secret"):
            data["secret"] = secrets.token_hex(32)
        if env:
            if len(env) < 16:
                raise SystemExit("MVM_TOKEN must be at least 16 characters")
            data["token_sha256"] = _h(env)
        elif not data.get("token_sha256"):
            self.new_token = "mvm_" + secrets.token_urlsafe(32)
            data["token_sha256"] = _h(self.new_token)
        secure_write(C.AUTH_FILE, json.dumps(data))
        self.token_hash, self.secret = data["token_sha256"], data["secret"]
        self.ser = URLSafeTimedSerializer(self.secret, salt="mvm-session")

    def reset(self):
        self.new_token = "mvm_" + secrets.token_urlsafe(32)
        secure_write(C.AUTH_FILE, json.dumps({"token_sha256": _h(self.new_token), "secret": secrets.token_hex(32)}))
        self._load_existing()
        return self.new_token

    def _load_existing(self):
        d = json.loads(C.AUTH_FILE.read_text())
        self.token_hash, self.secret = d["token_sha256"], d["secret"]
        self.ser = URLSafeTimedSerializer(self.secret, salt="mvm-session")

    def check_token(self, tok):
        return bool(tok) and hmac.compare_digest(_h(tok.strip()), self.token_hash)

    def new_session(self):
        csrf = secrets.token_urlsafe(24)
        return self.ser.dumps({"csrf": csrf}), csrf

    def read_session(self, cookie):
        if not cookie:
            return None
        try:
            return self.ser.loads(cookie, max_age=SESSION_MAX_AGE)
        except BadSignature:
            return None


def throttle_check(ip):
    with _lock:
        n, until = _fail.get(ip, (0, 0))
        return max(0, int(until - time.time()))


def throttle_fail(ip):
    with _lock:
        n, _ = _fail.get(ip, (0, 0))
        n += 1
        wait = 0 if n < 5 else min(900, 30 * 2 ** (n - 5))
        _fail[ip] = (n, time.time() + wait)
        if len(_fail) > 5000:
            _fail.clear()


def throttle_ok(ip):
    with _lock:
        _fail.pop(ip, None)


def origin_ok(request):
    o = request.headers.get("Origin")
    if not o:
        return True                     # non-browser clients (curl) send no Origin; they still need the token
    host = urllib.parse.urlsplit(o).netloc.lower()
    return host == request.host.lower()


CSP = ("default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "
       "connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")


def add_headers(resp, tls=False):
    h = resp.headers
    h["Content-Security-Policy"] = CSP
    h["X-Frame-Options"] = "DENY"
    h["X-Content-Type-Options"] = "nosniff"
    h["Referrer-Policy"] = "no-referrer"
    h["Cross-Origin-Opener-Policy"] = "same-origin"
    h["Cross-Origin-Resource-Policy"] = "same-origin"
    h["Permissions-Policy"] = "camera=(), microphone=(), geolocation=()"
    h["Cache-Control"] = "no-store"
    if tls:
        h["Strict-Transport-Security"] = "max-age=31536000"
    return resp
