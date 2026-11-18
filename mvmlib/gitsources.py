"""Build from GitHub: a saved source = repo + branch + the Dockerfile or compose yml YOU point to.
No framework detection: the file you name is the whole build definition, and the result is only images.
Rebuild triggers: manual, GitHub push webhook (HMAC-signed), or polling `git ls-remote`."""
import hashlib
import hmac
import json
import os
import re
import secrets
import shutil
import tempfile
import threading
import time
from pathlib import Path

import yaml

from . import config as C, db, images, jobs
from .util import Conflict, NotFound, UserError, age_s, int_in, norm_ref, now, run, short_ref, valid_name

_active = {}                       # source id -> job id of a queued/running build
_lock = threading.Lock()
REPO_RE = re.compile(r"https://[A-Za-z0-9.-]+(:\d+)?/[A-Za-z0-9._~%/-]+")
BRANCH_RE = re.compile(r"[A-Za-z0-9._/-]{1,100}")


def _git_env():
    return {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1", "HOME": str(C.BUILD)}


def _auth_args(repo):
    """Private repos: set MVM_GIT_TOKEN (a GitHub token). It is sent only to github.com and never stored or logged."""
    tok = os.environ.get("MVM_GIT_TOKEN")
    if tok and re.match(r"https://([a-z0-9-]+\.)?github\.com/", repo):
        import base64
        b = base64.b64encode(f"x-access-token:{tok}".encode()).decode()
        return ["-c", f"http.extraHeader=Authorization: Basic {b}"]
    return []


def validate(d, partial=False):
    f = {}
    if "name" in d or not partial:
        f["name"] = valid_name(d.get("name"), "source name")
    if "repo" in d or not partial:
        repo = str(d.get("repo", "")).strip().removesuffix("/").removesuffix(".git")
        if not REPO_RE.fullmatch(repo) or "@" in repo.split("/")[2]:
            raise UserError("repository must be a plain https:// URL (no credentials in it; private repos: set MVM_GIT_TOKEN)")
        f["repo"] = repo
    if "branch" in d or not partial:
        b = str(d.get("branch") or "main").strip()
        if not BRANCH_RE.fullmatch(b) or ".." in b or b.startswith("-"):
            raise UserError("bad branch name")
        f["branch"] = b
    if "kind" in d or not partial:
        if d.get("kind") not in ("dockerfile", "compose", "railpack"):
            raise UserError("kind must be 'dockerfile', 'compose' or 'railpack' (auto-detect)")
        f["kind"] = d["kind"]
    if "path" in d or not partial:
        kind = d.get("kind") or "dockerfile"
        p = str(d.get("path") or {"dockerfile": "Dockerfile", "compose": "docker-compose.yml", "railpack": "."}[kind]).strip().lstrip("/") or "."
        if ".." in p.split("/") or len(p) > 200:
            raise UserError("bad file path")
        f["path"] = p
    if "context" in d:
        c = str(d.get("context") or ".").strip().lstrip("/") or "."
        if ".." in c.split("/"):
            raise UserError("bad build context")
        f["context"] = c
    if "image_name" in d or "project" in d:
        if d.get("image_name"):
            f["image_name"] = valid_name(d["image_name"], "image name")
        if d.get("project"):
            f["project"] = valid_name(d["project"], "project")
    if "build_target" in d:
        t = str(d.get("build_target") or "").strip()
        if t and not re.fullmatch(r"[A-Za-z0-9_.-]{1,64}", t):
            raise UserError("bad build target (stage name)")
        f["build_target"] = t or None
    if "build_args" in d:
        a = d.get("build_args") or {}
        if not isinstance(a, dict) or len(a) > 50 or any(not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", str(k)) or len(str(v)) > 2000 for k, v in a.items()):
            raise UserError("build args must be KEY=value pairs (letters, digits, underscore)")
        f["build_args"] = {str(k): str(v) for k, v in a.items()}
    if "poll_minutes" in d:
        f["poll_minutes"] = int_in(d.get("poll_minutes") or 0, 0, 1440, "poll minutes")
    if "auto_rebuild" in d:
        f["auto_rebuild"] = int(bool(d["auto_rebuild"]))
    if "rollout_set" in d:
        rs = d.get("rollout_set")
        if rs and not (db.get("replicasets", rs) or db.get("replicasets", rs, "name")):
            raise NotFound(f"replica set {rs}")
        f["rollout_set"] = (db.get("replicasets", rs) or db.get("replicasets", rs, "name"))["id"] if rs else None
    return f


def create(d):
    f = validate(d)
    base = images.default_image_name(f["repo"])
    if f["kind"] in ("dockerfile", "railpack") and not f.get("image_name"):
        f["image_name"] = base                         # readable default (owner-repo); the user can change it any time
    if f["kind"] == "compose" and not f.get("project"):
        f["project"] = base
    for o in db.list_("gitsources"):
        if f.get("image_name") and o["image_name"] == f["image_name"]:
            raise Conflict(f"source '{o['name']}' already builds the image '{f['image_name']}' - choose another image name")
    f.setdefault("context", ".")
    if db.get("gitsources", f["name"], "name"):
        raise Conflict(f"source '{f['name']}' already exists")
    f.update(id=secrets.token_hex(4), secret=secrets.token_hex(24), created=now())
    db.upsert("gitsources", f)
    return view(db.get("gitsources", f["id"]))


def get(sid):
    s = db.get("gitsources", sid) or db.get("gitsources", sid, "name")
    if not s:
        raise NotFound(sid)
    return s


def update(sid, d):
    s = get(sid)
    f = validate(d, partial=True)
    if f.get("image_name"):
        for o in db.list_("gitsources"):
            if o["id"] != s["id"] and o["image_name"] == f["image_name"]:
                raise Conflict(f"source '{o['name']}' already builds the image '{f['image_name']}'")
    if "name" in f and f["name"] != s["name"] and db.get("gitsources", f["name"], "name"):
        raise Conflict(f"source '{f['name']}' already exists")
    if f:
        db.update("gitsources", s["id"], f)
    return view(get(sid))


def delete(sid):
    s = get(sid)
    db.delete("gitsources", s["id"])
    return {"ok": True}


def view(s, secret=False):
    d = dict(s)
    d["secret"] = s["secret"] if secret else None
    d["building"] = s["id"] in _active
    d["webhook_path"] = f"/hooks/github/{s['id']}"
    return d


def list_sources():
    return [view(s) for s in db.list_("gitsources")]


# ------------------------------------------------------------------ commit tracking
def remote_commit(repo, branch):
    r = run(["git", *_auth_args(repo), "-c", "protocol.file.allow=never", "ls-remote", "--heads", "--", repo, f"refs/heads/{branch}"],
            timeout=40, env=_git_env())
    line = r.stdout.strip().splitlines()[:1]
    if not line:
        raise RuntimeError(f"branch '{branch}' not found in {repo}")
    return line[0].split()[0]


def verify_signature(secret, body, header):
    """GitHub sends X-Hub-Signature-256: sha256=<hex hmac of the raw body>."""
    if not header or not header.startswith("sha256="):
        return False
    mac = hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(mac, header[7:])


def handle_push(sid, body_bytes, headers):
    s = get(sid)
    if not verify_signature(s["secret"], body_bytes, headers.get("X-Hub-Signature-256", "")):
        return 401, {"error": "bad signature"}
    event = headers.get("X-GitHub-Event", "")
    if event == "ping":
        return 200, {"ok": True, "pong": True}
    if event != "push":
        return 200, {"ignored": f"event {event or '?'}"}
    try:
        payload = json.loads(body_bytes or b"{}")
    except ValueError:
        return 400, {"error": "bad json"}
    if payload.get("ref") != f"refs/heads/{s['branch']}":
        return 200, {"ignored": f"push to {payload.get('ref')} (watching {s['branch']})"}
    if not s["auto_rebuild"]:
        return 200, {"ignored": "auto rebuild is off for this source"}
    job = trigger(s["id"], reason=f"push {str(payload.get('after', ''))[:8]}")
    return 202, {"queued": bool(job), "job": job["id"] if job else None}


# ------------------------------------------------------------------ build
def trigger(sid, force=False, reason="manual"):
    s = get(sid)
    with _lock:
        if sid in _active and (jobs.get(_active[sid]) or {}).get("status") in ("queued", "running"):
            return None                     # a build for this source is already waiting/running: it will pick up the newest commit
        job = jobs.submit(f"{s['name']} ({reason})", lambda log, sid=sid, force=force: run_build(sid, log, force), "git")
        _active[sid] = job["id"]
    db.update("gitsources", sid, {"last_job": job["id"], "last_status": "queued"})
    return job


def _finish(sid):
    with _lock:
        _active.pop(sid, None)


def run_build(sid, log, force=False):
    from . import replicas
    s = get(sid)
    work = Path(tempfile.mkdtemp(dir=C.BUILD))
    try:
        db.update("gitsources", sid, {"last_status": "building"})
        log(f"source '{s['name']}': {s['repo']} @ {s['branch']}  ({s['kind']}: {s['path']})")
        src = work / "src"
        commit = _clone(s, src, log)
        if commit == s["last_commit"] and not force and s["last_status"] == "ok":
            log(f"commit {commit[:8]} already built - nothing to do")
            db.update("gitsources", sid, {"last_checked": now(), "last_status": "ok"})
            return []
        built = _build_kind(s, src, commit, log)
        db.update("gitsources", sid, {"last_commit": commit, "last_status": "ok", "last_built": now(), "last_checked": now()})
        log(f"built {', '.join(built)} from commit {commit[:8]}")
        if s["rollout_set"]:
            try:
                replicas.rollout(s["rollout_set"], log)
            except Exception as e:  # noqa: BLE001
                log(f"WARN: rolling update failed: {e}")
        return built
    except Exception:
        db.update("gitsources", sid, {"last_status": "failed", "last_checked": now()})
        raise
    finally:
        shutil.rmtree(work, ignore_errors=True)
        _finish(sid)


def _clone(s, dest, log):
    from .util import run as _r
    if not re.fullmatch(r"https://.+", s["repo"]):
        raise UserError("bad repository")
    argv = ["git", *_auth_args(s["repo"]), "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never",
            "-c", "core.hooksPath=/dev/null", "clone", "--depth", "1", "--no-tags", "--single-branch", "--branch", s["branch"],
            "--", s["repo"], str(dest)]
    log(f"git clone {s['repo']} ({s['branch']})")
    _r(argv, timeout=600, env=_git_env())
    commit = _r(["git", "-C", str(dest), "rev-parse", "HEAD"], timeout=30).stdout.strip()
    shutil.rmtree(Path(dest) / ".git", ignore_errors=True)
    log(f"commit {commit[:12]}")
    return commit


def _safe(root, rel):
    p = (Path(root) / rel).resolve()
    if not (p == Path(root).resolve() or p.is_relative_to(Path(root).resolve())):
        raise UserError(f"path escapes the repository: {rel}")
    return p


def _build_kind(s, src, commit, log):
    args, target = s.get("build_args") or {}, s.get("build_target") or None
    if s["kind"] == "railpack":
        ctx = _safe(src, s["path"] or ".")
        if not ctx.is_dir():
            raise UserError(f"{s['path']} is not a folder in the repository")
        images.build_dockerfile(s["image_name"], None, str(ctx), log, build_args=args, replace=True, frontend="railpack", source="github")
        _tag_commit(s["image_name"], commit, s["repo"])
        return [s["image_name"]]
    if s["kind"] == "dockerfile":
        df = _safe(src, s["path"])
        if not df.is_file():
            raise UserError(f"{s['path']} not found in the repository")
        ctx = _safe(src, s["context"] or ".")
        if not df.is_relative_to(ctx):
            raise UserError("the Dockerfile must be inside the build context folder")
        return [_build_df(s["image_name"], ctx, str(df.relative_to(ctx)), commit, s["repo"], log, args, target)]
    cf = _safe(src, s["path"])
    if not cf.is_file():
        raise UserError(f"{s['path']} not found in the repository")
    return _build_compose(s, cf, commit, log)


def _tag_commit(name, commit, repo):
    row = db.get("images", name, "name")
    if row:
        db.update("images", name, {"digest": commit, "ref": f"git:{repo}@{commit[:8]}", "source": "github"}, pk="name")
        meta = C.IMG / name / "meta.json"
        try:
            d = json.loads(meta.read_text())
            d.update(digest=commit, ref=f"git:{repo}@{commit[:8]}", source="github")
            meta.write_text(json.dumps(d, indent=1))
        except (OSError, ValueError):
            pass


def _build_df(name, ctx, dockerfile_rel, commit, repo, log, args=None, target=None):
    images.build_dockerfile(name, None, str(ctx), log, build_args=args or {}, dockerfile=dockerfile_rel, target=target, replace=True, source="github")
    _tag_commit(name, commit, repo)
    return name


def _build_compose(s, cf, commit, log):
    doc = yaml.safe_load(cf.read_text(errors="replace"))
    svcs = doc.get("services") if isinstance(doc, dict) else None
    if not isinstance(svcs, dict) or not svcs:
        raise UserError("compose file has no services")
    base = cf.parent
    project = s["project"]
    built = []
    for n, svc in svcs.items():
        if not isinstance(svc, dict) or not re.fullmatch(r"[A-Za-z0-9_.-]+", str(n)):
            raise UserError(f"bad service {n!r}")
        name = f"{project}-{n}".lower()
        valid_name(name, "image name")
        b = svc.get("build")
        if b:
            ctxrel, dfrel, bargs, tgt = (b, "Dockerfile", {}, None) if isinstance(b, str) else (b.get("context", "."), b.get("dockerfile", "Dockerfile"), b.get("args") or {}, b.get("target"))
            ctx = _safe(base, ctxrel)
            dfp = _safe(ctx, dfrel)
            if not dfp.is_file():
                raise UserError(f"service {n}: {dfrel} not found in build context {ctxrel}")
            bargs = {str(k): str(v) for k, v in (bargs.items() if isinstance(bargs, dict) else [a.split("=", 1) for a in bargs if "=" in a])}
            log(f"service {n}: build {ctxrel} ({dfrel}{', target ' + tgt if tgt else ''}) -> image {name}")
            built.append(_build_df(name, ctx, str(dfp.relative_to(ctx)), commit, s["repo"], log, {**bargs, **(s.get("build_args") or {})}, tgt))
        elif svc.get("image"):
            log(f"service {n}: pull {svc['image']} -> image {name}")
            env = images._envdict(svc.get("environment"))
            images.build_oci(name, norm_ref(str(svc["image"])), log, env=env, entrypoint=images._listify(svc.get("entrypoint")),
                             cmd=images._listify(svc.get("command")), hostname=str(n), refresh=True, force=True, source="github")
            _tag_commit(name, commit, s["repo"])
            built.append(name)
        else:
            raise UserError(f"service {n} has neither 'build' nor 'image'")
    return built


# ------------------------------------------------------------------ polling
def poll_once():
    for s in db.list_("gitsources"):
        if not s["auto_rebuild"] or not s["poll_minutes"] or s["id"] in _active:
            continue
        if s["last_checked"] and age_s(s["last_checked"]) < s["poll_minutes"] * 60:
            continue
        db.update("gitsources", s["id"], {"last_checked": now()})
        try:
            head = remote_commit(s["repo"], s["branch"])
        except Exception:  # noqa: BLE001 - network hiccup: try again next interval
            continue
        if head != s["last_commit"]:
            trigger(s["id"], reason=f"new commit {head[:8]}")


def _poll_loop():
    while True:
        time.sleep(30)
        try:
            poll_once()
        except Exception:  # noqa: BLE001
            pass


def startup():
    for s in db.list_("gitsources"):
        if s["last_status"] in ("queued", "building"):
            db.update("gitsources", s["id"], {"last_status": "interrupted"})
    threading.Thread(target=_poll_loop, daemon=True, name="git-poll").start()
