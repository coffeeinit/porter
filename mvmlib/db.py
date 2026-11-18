"""SQLite storage (WAL). One short-lived connection per call keeps threads safe."""
import json
import sqlite3
from contextlib import closing

from . import config as C
from .util import now

SCHEMA = """
CREATE TABLE IF NOT EXISTS images (
  name TEXT PRIMARY KEY, ref TEXT, source TEXT, arch TEXT, digest TEXT, size_mb INTEGER, sha256 TEXT,
  args TEXT, workdir TEXT, user TEXT, ports TEXT, env TEXT, hostname TEXT,
  agent INTEGER DEFAULT 1, guest_sha256 TEXT, notes TEXT, created TEXT
);
CREATE TABLE IF NOT EXISTS vms (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, image TEXT, ip TEXT, mac TEXT, tap TEXT,
  vcpu INTEGER, mem INTEGER, disk_mb INTEGER, status TEXT, pid INTEGER, jailed INTEGER DEFAULT 0,
  config TEXT, hidden INTEGER DEFAULT 0, created TEXT, started TEXT, stopped TEXT, error TEXT, autostart INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY, vm_id TEXT, name TEXT, kind TEXT, mem_mib INTEGER, size_bytes INTEGER, created TEXT, note TEXT
);
CREATE TABLE IF NOT EXISTS forwards (
  id INTEGER PRIMARY KEY AUTOINCREMENT, vm_id TEXT, host_port INTEGER UNIQUE, guest_port INTEGER, proto TEXT DEFAULT 'tcp', created TEXT
);
CREATE TABLE IF NOT EXISTS replicasets (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, image TEXT, desired INTEGER, vcpu INTEGER, mem INTEGER, disk_mb INTEGER,
  config TEXT, state TEXT DEFAULT 'running', autoheal INTEGER DEFAULT 1, lb_port INTEGER, service_port INTEGER,
  lb_bind TEXT DEFAULT '127.0.0.1', git_source TEXT, created TEXT, last_rollout TEXT, note TEXT
);
CREATE TABLE IF NOT EXISTS gitsources (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, repo TEXT, branch TEXT, kind TEXT, path TEXT, context TEXT,
  image_name TEXT, project TEXT, poll_minutes INTEGER DEFAULT 0, auto_rebuild INTEGER DEFAULT 0, secret TEXT,
  rollout_set TEXT, last_commit TEXT, last_status TEXT, last_built TEXT, last_job TEXT, last_checked TEXT, created TEXT
);
CREATE TABLE IF NOT EXISTS volumes (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, size_mb INTEGER, fs TEXT DEFAULT 'ext4', attached_to TEXT, mount TEXT, note TEXT,
  backup_every_h INTEGER DEFAULT 0, backup_keep INTEGER DEFAULT 5, pre_cmd TEXT, last_backup TEXT, health TEXT,
  rset TEXT, ridx INTEGER, created TEXT
);
CREATE TABLE IF NOT EXISTS backups (
  id TEXT PRIMARY KEY, volume_id TEXT, name TEXT, kind TEXT, size_bytes INTEGER, created TEXT, note TEXT, consistent TEXT
);
CREATE TABLE IF NOT EXISTS routes (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, target_kind TEXT, target TEXT, port INTEGER, host TEXT, auto INTEGER DEFAULT 1,
  tls INTEGER DEFAULT 0, created TEXT
);
CREATE TABLE IF NOT EXISTS settings (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, url TEXT, token TEXT, region TEXT, lat REAL, lon REAL, insecure INTEGER DEFAULT 0,
  status TEXT DEFAULT 'unknown', last_seen TEXT, info TEXT, created TEXT
);
CREATE TABLE IF NOT EXISTS fleet_services (
  id TEXT PRIMARY KEY, name TEXT UNIQUE, image TEXT, total INTEGER, placement TEXT, template TEXT, created TEXT, note TEXT
);
CREATE TABLE IF NOT EXISTS users (name TEXT PRIMARY KEY, pw_hash TEXT, created TEXT);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, actor TEXT, action TEXT, target TEXT, detail TEXT, ok INTEGER
);
"""
JSON_COLS = {"images": ("args", "ports", "env"), "vms": ("config",), "replicasets": ("config", "volumes"), "nodes": ("info",), "gitsources": ("build_args",),
             "fleet_services": ("placement", "template")}


_OBJ_KEYS = ("config", "env", "info", "placement", "template", "build_args")


def _EMPTY_OBJ(k):
    return {} if k in _OBJ_KEYS else []


def connect():
    c = sqlite3.connect(C.DB_PATH, timeout=20)
    c.row_factory = sqlite3.Row
    return c


def init():
    with closing(connect()) as c:
        c.execute("PRAGMA journal_mode=WAL")
        c.executescript(SCHEMA)
        cols = {r[1] for r in c.execute("PRAGMA table_info(vms)")}          # migrate databases created by 3.0.0
        if "rset" not in cols:
            c.execute("ALTER TABLE vms ADD COLUMN rset TEXT")
        if "ridx" not in cols:
            c.execute("ALTER TABLE vms ADD COLUMN ridx INTEGER")
        gcols = {r[1] for r in c.execute("PRAGMA table_info(gitsources)")}
        for col in ("build_target", "build_args"):
            if col not in gcols:
                c.execute(f"ALTER TABLE gitsources ADD COLUMN {col} TEXT")
        rcols = {r[1] for r in c.execute("PRAGMA table_info(replicasets)")}
        if "lb_algo" not in rcols:
            c.execute("ALTER TABLE replicasets ADD COLUMN lb_algo TEXT DEFAULT 'round_robin'")
        if "volumes" not in rcols:
            c.execute("ALTER TABLE replicasets ADD COLUMN volumes TEXT")
        c.commit()


def _row(table, r):
    if r is None:
        return None
    d = dict(r)
    for k in JSON_COLS.get(table, ()):
        try:
            d[k] = json.loads(d[k]) if d.get(k) else (_EMPTY_OBJ(k))
        except ValueError:
            d[k] = _EMPTY_OBJ(k)
    return d


def _enc(table, d):
    d = dict(d)
    for k in JSON_COLS.get(table, ()):
        if k in d and not isinstance(d[k], str):
            d[k] = json.dumps(d[k])
    return d


def get(table, key, col="id"):
    with closing(connect()) as c:
        return _row(table, c.execute(f"SELECT * FROM {table} WHERE {col}=?", (key,)).fetchone())


def list_(table, where="", args=(), order="rowid"):
    with closing(connect()) as c:
        return [_row(table, r) for r in c.execute(f"SELECT * FROM {table} {where} ORDER BY {order}", args)]


def upsert(table, d, pk="id"):
    d = _enc(table, d)
    cols = list(d)
    sql = (f"INSERT INTO {table} ({','.join(cols)}) VALUES ({','.join('?' * len(cols))}) "
           f"ON CONFLICT({pk}) DO UPDATE SET " + ",".join(f"{k}=excluded.{k}" for k in cols if k != pk))
    with closing(connect()) as c:
        c.execute(sql, [d[k] for k in cols])
        c.commit()


def update(table, key, fields, pk="id"):
    fields = _enc(table, fields)
    with closing(connect()) as c:
        c.execute(f"UPDATE {table} SET {','.join(k + '=?' for k in fields)} WHERE {pk}=?", [*fields.values(), key])
        c.commit()


def delete(table, key, pk="id"):
    with closing(connect()) as c:
        c.execute(f"DELETE FROM {table} WHERE {pk}=?", (key,))
        c.commit()


def insert(table, d):
    d = _enc(table, d)
    with closing(connect()) as c:
        cur = c.execute(f"INSERT INTO {table} ({','.join(d)}) VALUES ({','.join('?' * len(d))})", list(d.values()))
        c.commit()
        return cur.lastrowid


def audit(actor, action, target="", detail="", ok=True):
    with closing(connect()) as c:
        c.execute("INSERT INTO audit (ts,actor,action,target,detail,ok) VALUES (?,?,?,?,?,?)",
                  (now(), actor, action, target, str(detail)[:500], int(ok)))
        c.execute("DELETE FROM audit WHERE id <= (SELECT MAX(id) FROM audit) - 2000")
        c.commit()


def setting(k, default=None):
    r = get("settings", k, "k")
    return r["v"] if r else default


def set_setting(k, v):
    upsert("settings", {"k": k, "v": "" if v is None else str(v)}, pk="k")
