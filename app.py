#!/usr/bin/env python3
"""MicroVM Maker 3 - secure web UI + API for Firecracker microVMs.

Run as a NORMAL user:   python3 app.py          (install once with ./install.sh)
"""
import sys

if sys.version_info < (3, 9):
    raise SystemExit("Python 3.9+ is required")

import atexit
import io
import json
import logging
import os
import re
import signal
import socket
import tempfile
import urllib.parse

try:
    import yaml
    from flask import Flask, Response, jsonify, redirect, render_template, request, send_file
    from werkzeug.exceptions import HTTPException
except ImportError as _e:
    raise SystemExit(f"Missing Python module '{_e.name}'. Run ./install.sh or: pip install -r requirements.txt")

from mvmlib import agent, buildkit, config as C, db, gitsources, host, images, jobs, replicas, security, vmm
from mvmlib import volumes
from mvmlib.fcapi import FCError
from mvmlib.util import Conflict, NotFound, UserError, human, int_in, valid_name, valid_vid

if os.geteuid() == 0 and os.environ.get("MVM_ALLOW_ROOT") != "1":
    raise SystemExit("Refusing to run as root. Run as your normal user: python3 app.py")

C.ensure_dirs()
db.init()
auth = security.Auth()
app = Flask(__name__, static_folder="static", template_folder="templates", static_url_path="/static")
app.config.update(MAX_CONTENT_LENGTH=4 * 1024 * 1024, JSON_SORT_KEYS=False)
log = logging.getLogger("mvm")
TLS = bool(C.TLS_CERT and C.TLS_KEY)
PUBLIC = ("/login", "/api/login", "/api/session", "/static/", "/favicon.ico", "/hooks/")


# ------------------------------------------------------------------ request guards
def client_ip():
    return request.remote_addr or "?"


@app.before_request
def guard():
    if C.HOST not in ("0.0.0.0", "::"):
        hostname = (urllib.parse.urlsplit("//" + request.host).hostname or "").lower()
        if hostname not in C.ALLOWED_HOSTS:
            return jsonify(error=f"bad Host header {hostname!r} (set MVM_ALLOWED_HOSTS)"), 403
    mutating = request.method not in ("GET", "HEAD", "OPTIONS")
    if mutating and not security.origin_ok(request):
        return jsonify(error="cross-origin request refused"), 403
    p = request.path
    request.environ["mvm.csrf"] = None
    sess = auth.read_session(request.cookies.get(security.SESSION_COOKIE))
    bearer = (request.headers.get("Authorization") or "")[7:] if (request.headers.get("Authorization") or "").startswith("Bearer ") else None
    request.authed = False
    if bearer:
        if security.throttle_check(client_ip()):
            return jsonify(error="too many failed attempts, slow down"), 429
        if auth.check_token(bearer):
            request.authed = True
        else:
            security.throttle_fail(client_ip())
            return jsonify(error="invalid token"), 401
    elif sess:
        request.authed = True
        request.environ["mvm.csrf"] = sess.get("csrf")
        if mutating and not p.startswith("/api/login"):
            sent = request.headers.get("X-CSRF-Token", "")
            if not sent or sent != sess.get("csrf"):
                return jsonify(error="missing or wrong CSRF token (reload the page)"), 403
    if any(p == x or p.startswith(x) for x in PUBLIC):
        return None
    if not request.authed:
        if p.startswith("/api/"):
            return jsonify(error="authentication required"), 401
        return redirect("/login")
    if mutating and C.READONLY:
        return jsonify(error="server is in read-only mode (MVM_READONLY)"), 403
    if request.path.startswith("/hooks/"):
        request.max_content_length = 1 << 20
    if request.path == "/api/images/import":
        request.max_content_length = 4 << 30
    elif request.path.endswith("/files"):
        request.max_content_length = 300 << 20
    return None


@app.after_request
def after(resp):
    security.add_headers(resp, TLS)
    if request.path.startswith("/api/") and request.method not in ("GET", "HEAD", "OPTIONS"):
        try:
            db.audit(client_ip(), f"{request.method} {request.path}", "", resp.status_code, resp.status_code < 400)
        except Exception:  # noqa: BLE001
            pass
    return resp


@app.errorhandler(Exception)
def err(e):
    if isinstance(e, HTTPException):
        return jsonify(error=e.description), e.code
    if isinstance(e, NotFound) or isinstance(e, KeyError):
        return jsonify(error=f"not found: {e.args[0] if e.args else ''}"), 404
    if isinstance(e, (UserError, ValueError, yaml.YAMLError)):
        return jsonify(error=str(e)), 400
    if isinstance(e, Conflict):
        return jsonify(error=str(e)), 409
    if isinstance(e, FCError):
        return jsonify(error=str(e), firecracker=e.fault), 502
    if isinstance(e, agent.AgentError):
        return jsonify(error=f"guest agent: {e}"), 502
    log.exception("unhandled error on %s", request.path)
    return jsonify(error=str(e) or e.__class__.__name__), 500


def body():
    j = request.get_json(silent=True)
    if not isinstance(j, dict):
        raise UserError("JSON object body required")
    return j


# ------------------------------------------------------------------ pages + auth
@app.get("/")
def index():
    return render_template("index.html", version=C.VERSION, readonly=C.READONLY)


@app.get("/login")
def login_page():
    if request.authed:
        return redirect("/")
    return render_template("login.html", version=C.VERSION)


@app.post("/api/login")
def api_login():
    ip = client_ip()
    wait = security.throttle_check(ip)
    if wait:
        return jsonify(error=f"too many failed attempts - try again in {wait}s"), 429
    tok = str((request.get_json(silent=True) or {}).get("token", ""))
    if not auth.check_token(tok):
        security.throttle_fail(ip)
        return jsonify(error="invalid token"), 401
    security.throttle_ok(ip)
    cookie, csrf = auth.new_session()
    r = jsonify(ok=True, csrf=csrf)
    r.set_cookie(security.SESSION_COOKIE, cookie, max_age=security.SESSION_MAX_AGE, httponly=True,
                 samesite="Strict", secure=TLS, path="/")
    return r


@app.post("/api/logout")
def api_logout():
    r = jsonify(ok=True)
    r.delete_cookie(security.SESSION_COOKIE, path="/")
    return r


@app.get("/api/session")
def api_session():
    return jsonify(authenticated=bool(request.authed), csrf=request.environ.get("mvm.csrf"), readonly=C.READONLY,
                   version=C.VERSION)


# ------------------------------------------------------------------ host / catalog / jobs
@app.get("/api/health")
def api_health():
    return jsonify(host.health())


@app.get("/api/store")
def api_store():
    return jsonify(images.store())


@app.get("/api/jobs")
def api_jobs():
    return jsonify(jobs.list_())


@app.get("/api/jobs/<jid>")
def api_job(jid):
    j = jobs.get(jid)
    if not j:
        raise NotFound(jid)
    return jsonify(j)


@app.post("/api/kernel")
def api_kernel():
    force = bool((request.get_json(silent=True) or {}).get("force"))
    return jsonify(jobs.submit("Download guest kernel", lambda lg: host.fetch_kernel(lg, force), "kernel")), 202


@app.get("/api/audit")
def api_audit():
    rows = db.list_("audit", order="id DESC")[:200]
    return jsonify(rows)


# ------------------------------------------------------------------ images
@app.get("/api/images")
def api_images():
    return jsonify(images.list_images())


@app.get("/api/images/<name>")
def api_image(name):
    return jsonify(images.get_image(name))


@app.delete("/api/images/<name>")
def api_image_delete(name):
    images.delete_image(name)
    return jsonify(ok=True)


@app.get("/api/images/<name>/export")
def api_image_export(name):
    images.get_image(name)
    return send_file(C.IMG / name / "rootfs.ext4", as_attachment=True, download_name=f"{name}.ext4",
                     mimetype="application/octet-stream")


def _job(label, fn):
    return jsonify(jobs.submit(label, fn)), 202


@app.post("/api/images/oci")
def api_img_oci():
    j = body()
    ref = images.norm_ref(j.get("ref"))
    name = valid_name(j.get("name") or images.short_ref(ref), "image name")
    env = {str(k): str(v) for k, v in (j.get("env") or {}).items()}
    return _job(ref, lambda lg: [images.build_oci(name, ref, lg, env=env, entrypoint=images._listify(j.get("entrypoint")),
                                                  cmd=images._listify(j.get("cmd")), refresh=bool(j.get("refresh")),
                                                  force=bool(j.get("force")), extra_mb=int_in(j.get("extra_mb", 64), 0, 65536, "free space"))])


@app.post("/api/images/compose")
def api_img_compose():
    j = body()
    text, project = str(j.get("text", "")), j.get("project") or "stack"
    yaml.safe_load(text)
    valid_name(project, "project")
    return _job(f"compose:{project}", lambda lg: images.build_compose(text, project, bool(j.get("refresh")), lg))


@app.post("/api/images/dockerfile")
def api_img_dockerfile():
    """Dockerfile (full BuildKit syntax, multi-stage, COPY --from ...) or Railpack auto-detect, built in a temporary builder microVM."""
    j = body()
    frontend = j.get("frontend") or "dockerfile"
    if frontend not in ("dockerfile", "railpack"):
        raise UserError("frontend must be dockerfile or railpack")
    name = valid_name(j.get("name"), "image name")
    text = str(j.get("dockerfile", ""))
    if frontend == "dockerfile" and not text.strip():
        raise UserError("Dockerfile is empty")
    ctx = tempfile.mkdtemp(dir=C.BUILD)
    root = images.Path(ctx).resolve()
    for rel, content in (j.get("files") or {}).items():
        p = (root / str(rel).lstrip("/")).resolve()
        if not p.is_relative_to(root):
            images.shutil.rmtree(ctx, ignore_errors=True)
            raise UserError(f"bad file path: {rel}")
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(str(content))
    args = {str(k): str(v) for k, v in (j.get("args") or {}).items()}

    def go(lg):
        try:
            return [images.build_dockerfile(name, text if frontend == "dockerfile" else None, ctx, lg, build_args=args, target=(j.get("target") or None),
                                            replace=bool(j.get("replace")), frontend=frontend, no_cache=bool(j.get("no_cache")),
                                            source="railpack" if frontend == "railpack" else "dockerfile")]
        finally:
            images.shutil.rmtree(ctx, ignore_errors=True)
    return _job(f"{'Railpack' if frontend == 'railpack' else 'Dockerfile'} → {name}", go)


@app.post("/api/images/git")
def api_img_git():
    j = body()
    repo = str(j.get("repo", ""))
    name = valid_name(j.get("name") or images.default_image_name(repo), "image name")
    args = {str(k): str(v) for k, v in (j.get("args") or {}).items()}
    return _job(f"git → {name}", lambda lg: [images.build_git(name, repo, str(j.get("branch", "")), str(j.get("dockerfile") or "Dockerfile"), lg,
                                                                bool(j.get("refresh")), mode=("railpack" if j.get("mode") == "railpack" else "dockerfile"),
                                                                replace=bool(j.get("replace")), target=(j.get("target") or None), build_args=args)])


@app.post("/api/images/<name>/rename")
def api_img_rename(name):
    return jsonify(images.rename_image(name, body().get("name")))


@app.get("/api/builder")
def api_builder():
    return jsonify(buildkit.builder_info())


@app.post("/api/builder/rebuild")
def api_builder_rebuild():
    return _job("Builder image (BuildKit + Railpack)", lambda lg: [buildkit.ensure_builder_image(lg, force=True)] and [])


@app.post("/api/builder/reset-cache")
def api_builder_reset():
    return jsonify(buildkit.reset_cache())


@app.post("/api/images/official")
def api_img_official():
    name = valid_name((request.get_json(silent=True) or {}).get("name") or "ubuntu-official", "image name")
    return _job("Official Firecracker Ubuntu rootfs", lambda lg: [images.official_rootfs(name, lg)])


@app.post("/api/images/import")
def api_img_import():
    f = request.files.get("file")
    name = valid_name(request.form.get("name"), "image name")
    if not f:
        raise UserError("choose a rootfs .tar / .tar.gz file")
    tmp = tempfile.NamedTemporaryFile(dir=C.BUILD, delete=False, suffix=".tar")
    f.save(tmp)
    tmp.close()

    def go(lg):
        try:
            return [images.import_tar(name, tmp.name, lg)]
        finally:
            os.unlink(tmp.name)
    return _job(f"import → {name}", go)


# ------------------------------------------------------------------ VMs
def _cfg_from(j):
    keys = ("net", "entropy", "balloon", "mmds", "cpu_template", "smt", "track_dirty_pages", "huge_pages", "hotplug_mib", "rx_mbit", "tx_mbit",
            "disk_mbit", "disk_iops", "env", "command", "boot_args", "metrics", "autostart")
    return {k: j[k] for k in keys if k in j}


@app.get("/api/vms")
def api_vms():
    return jsonify(vmm.list_vms())


@app.post("/api/vms")
def api_vm_create():
    j = body()
    v = vmm.create_vm(str(j.get("image", "")), name=j.get("name") or None, vcpu=j.get("vcpu", 1), mem=j.get("mem", 512),
                      disk_mb=j.get("disk_mb") or 0, config=_cfg_from(j), volumes=j.get("volumes") or None)
    if j.get("start"):
        v = vmm.start_vm(v["id"])
    return jsonify(v), 201


@app.get("/api/vms/<vid>")
def api_vm(vid):
    return jsonify(vmm.view(vmm.get_vm(vid)))


@app.patch("/api/vms/<vid>")
def api_vm_update(vid):
    j = body()
    return jsonify(vmm.update_resources(vid, j.get("vcpu"), j.get("mem"), _cfg_from(j) or None, j.get("disk_mb")))


@app.delete("/api/vms/<vid>")
def api_vm_delete(vid):
    return jsonify(vmm.delete_vm(vid))


@app.post("/api/vms/<vid>/start")
def api_vm_start(vid): return jsonify(vmm.start_vm(vid))


@app.post("/api/vms/<vid>/stop")
def api_vm_stop(vid): return jsonify(vmm.stop_vm(vid, graceful=True))


@app.post("/api/vms/<vid>/kill")
def api_vm_kill(vid): return jsonify(vmm.kill_vm(vid))


@app.post("/api/vms/<vid>/restart")
def api_vm_restart(vid): return jsonify(vmm.restart_vm(vid))


@app.post("/api/vms/<vid>/pause")
def api_vm_pause(vid): return jsonify(vmm.pause_vm(vid))


@app.post("/api/vms/<vid>/resume")
def api_vm_resume(vid): return jsonify(vmm.resume_vm(vid))


@app.post("/api/vms/<vid>/clone")
def api_vm_clone(vid): return jsonify(vmm.clone_vm(vid, (request.get_json(silent=True) or {}).get("name"))), 201


@app.post("/api/vms/<vid>/live")
def api_vm_live(vid): return jsonify(vmm.live_update(vid, body()))


def _text(s):
    return Response(s or "", mimetype="text/plain; charset=utf-8")


@app.get("/api/vms/<vid>/console")
def api_vm_console(vid):
    vmm.get_vm(vid)
    return _text(vmm.console_tail(valid_vid(vid), 60000) or "(no console output yet)")


@app.get("/api/vms/<vid>/firecracker-log")
def api_vm_fclog(vid): return _text(vmm.firecracker_log(vid) or "(empty)")


@app.get("/api/vms/<vid>/metrics")
def api_vm_metrics(vid): return jsonify(vmm.metrics(vid))


@app.get("/api/vms/<vid>/stats")
def api_vm_stats(vid): return jsonify(vmm.guest_stats(vid))


@app.get("/api/vms/<vid>/machine")
def api_vm_machine(vid): return jsonify(vmm.machine_info(vid))


@app.post("/api/vms/<vid>/exec")
def api_vm_exec(vid):
    j = body()
    return jsonify(vmm.run_exec(vid, j.get("cmd"), j.get("workdir") or "/", j.get("timeout", 60)))


@app.post("/api/vms/<vid>/files")
def api_vm_upload(vid):
    f = request.files.get("file")
    if not f:
        raise UserError("no file")
    path = request.form.get("path") or ""
    if path.endswith("/"):
        path += os.path.basename(f.filename or "upload")
    vmm.file_put(vid, path, f.read(), request.form.get("mode") or "644")
    return jsonify(ok=True, path=path)


@app.get("/api/vms/<vid>/files")
def api_vm_download(vid):
    path = request.args.get("path", "")
    data = vmm.file_get(vid, path)
    return send_file(io.BytesIO(data), as_attachment=True, download_name=os.path.basename(path) or "file",
                     mimetype="application/octet-stream")


@app.get("/api/vms/<vid>/snapshots")
def api_snaps(vid): return jsonify(vmm.list_snapshots(vid))


@app.post("/api/vms/<vid>/snapshots")
def api_snap_create(vid):
    j = request.get_json(silent=True) or {}
    return jsonify(vmm.create_snapshot(vid, j.get("name"), str(j.get("note") or ""))), 201


@app.post("/api/vms/<vid>/snapshots/<sid>/restore")
def api_snap_restore(vid, sid): return jsonify(vmm.restore_snapshot(vid, sid))


@app.delete("/api/vms/<vid>/snapshots/<sid>")
def api_snap_delete(vid, sid): return jsonify(vmm.delete_snapshot(vid, sid))


@app.post("/api/vms/<vid>/forwards")
def api_fwd_add(vid):
    j = body()
    return jsonify(vmm.forward_add(vid, j.get("host_port"), j.get("guest_port"), j.get("proto", "tcp"))), 201


@app.delete("/api/vms/<vid>/forwards/<fid>")
def api_fwd_del(vid, fid): return jsonify(vmm.forward_remove(vid, fid))



# ------------------------------------------------------------------ replica sets
@app.get("/api/replicasets")
def api_rs_list(): return jsonify(replicas.list_sets())


@app.post("/api/replicasets")
def api_rs_create():
    j = body()
    rs = replicas.create_set(j.get("name"), str(j.get("image", "")), j.get("count", 2), j.get("vcpu", 1), j.get("mem", 256),
                             j.get("disk_mb") or 0, _cfg_from(j), j.get("lb_port") or None, j.get("service_port") or None,
                             j.get("lb_bind") or "127.0.0.1", j.get("autoheal", True), start=bool(j.get("start", True)),
                             lb_algo=j.get("lb_algo") or "round_robin", volumes=j.get("volumes") or None)
    return jsonify(rs), 201


@app.get("/api/replicasets/<sid>")
def api_rs_get(sid): return jsonify(replicas.view(replicas.get_set(sid)))


@app.patch("/api/replicasets/<sid>")
def api_rs_patch(sid):
    j = body()
    d = {k: j[k] for k in ("vcpu", "mem", "disk_mb", "image", "autoheal", "lb_port", "service_port", "lb_bind", "lb_algo", "volumes") if k in j}
    c = _cfg_from(j)
    if c:
        d["config"] = c
    return jsonify(replicas.update_set(sid, d))


@app.delete("/api/replicasets/<sid>")
def api_rs_delete(sid): return jsonify(replicas.delete_set(sid, delete_volumes=request.args.get("delete_volumes") == "1"))


@app.post("/api/replicasets/<sid>/scale")
def api_rs_scale(sid): return jsonify(replicas.scale(sid, body().get("count")))


@app.post("/api/replicasets/<sid>/start")
def api_rs_start(sid): return jsonify(replicas.start_set(sid))


@app.post("/api/replicasets/<sid>/stop")
def api_rs_stop(sid): return jsonify(replicas.stop_set(sid))


@app.post("/api/replicasets/<sid>/restart")
def api_rs_restart(sid): return jsonify(replicas.restart_set(sid))


@app.post("/api/replicasets/<sid>/rollout")
def api_rs_rollout(sid):
    rs = replicas.get_set(sid)
    return jsonify(jobs.submit(f"Rolling update: {rs['name']}", lambda lg: [replicas.rollout(rs["id"], lg)["name"]], "rollout")), 202


# ------------------------------------------------------------------ GitHub build sources
@app.get("/api/sources")
def api_src_list(): return jsonify(gitsources.list_sources())


@app.post("/api/sources")
def api_src_create():
    r = gitsources.create(body())
    return jsonify(gitsources.view(gitsources.get(r["id"]), secret=True)), 201


@app.get("/api/sources/<sid>")
def api_src_get(sid): return jsonify(gitsources.view(gitsources.get(sid), secret=request.args.get("secret") == "1"))


@app.patch("/api/sources/<sid>")
def api_src_patch(sid): return jsonify(gitsources.update(sid, body()))


@app.delete("/api/sources/<sid>")
def api_src_delete(sid): return jsonify(gitsources.delete(sid))


@app.post("/api/sources/<sid>/build")
def api_src_build(sid):
    j = request.get_json(silent=True) or {}
    job = gitsources.trigger(sid, force=bool(j.get("force")), reason="manual")
    if not job:
        raise Conflict("a build for this source is already queued or running")
    return jsonify(job), 202


@app.post("/api/sources/<sid>/check")
def api_src_check(sid):
    s = gitsources.get(sid)
    head = gitsources.remote_commit(s["repo"], s["branch"])
    return jsonify(remote=head, built=s["last_commit"], behind=head != s["last_commit"])


@app.post("/api/sources/<sid>/rotate-secret")
def api_src_rotate(sid):
    import secrets as _s
    s = gitsources.get(sid)
    db.update("gitsources", s["id"], {"secret": _s.token_hex(24)})
    return jsonify(gitsources.view(gitsources.get(sid), secret=True))


@app.post("/hooks/github/<sid>")
def hook_github(sid):
    """Public endpoint for GitHub push webhooks. Authenticated ONLY by the HMAC signature of the body."""
    try:
        code, out = gitsources.handle_push(sid, request.get_data(cache=False), request.headers)
    except NotFound:
        code, out = 404, {"error": "unknown source"}
    try:
        db.audit(client_ip(), "webhook github", sid, code, code < 400)
    except Exception:  # noqa: BLE001
        pass
    return jsonify(out), code


# ------------------------------------------------------------------ persistent volumes + backups
@app.get("/api/volumes")
def api_vol_list(): return jsonify(volumes.list_volumes())


@app.post("/api/volumes")
def api_vol_create():
    j = body()
    return jsonify(volumes.create(j.get("name"), j.get("size_mb"), j.get("note") or "", backup_every_h=j.get("backup_every_h") or 0,
                                  backup_keep=j.get("backup_keep") or 5, pre_cmd=j.get("pre_cmd"))), 201


@app.get("/api/volumes/<vid>")
def api_vol_get(vid): return jsonify(volumes.view(volumes.get(vid)))


@app.patch("/api/volumes/<vid>")
def api_vol_patch(vid): return jsonify(volumes.update(vid, body()))


@app.delete("/api/volumes/<vid>")
def api_vol_delete(vid): return jsonify(volumes.delete(vid))


@app.post("/api/volumes/<vid>/resize")
def api_vol_resize(vid): return jsonify(volumes.resize(vid, body().get("size_mb")))


@app.post("/api/volumes/<vid>/attach")
def api_vol_attach(vid):
    j = body()
    return jsonify(volumes.attach(vid, str(j.get("vm", "")), j.get("mount")))


@app.post("/api/volumes/<vid>/detach")
def api_vol_detach(vid): return jsonify(volumes.detach(vid))


@app.post("/api/volumes/<vid>/check")
def api_vol_check(vid): return jsonify(volumes.check(vid))


@app.get("/api/volumes/<vid>/usage")
def api_vol_usage(vid): return jsonify(volumes.usage(vid) or {})


@app.get("/api/volumes/<vid>/backups")
def api_vol_backups(vid): return jsonify(volumes.list_backups(vid))


@app.post("/api/volumes/<vid>/backups")
def api_vol_backup(vid):
    j = request.get_json(silent=True) or {}
    return jsonify(volumes.backup(vid, str(j.get("note") or ""), "live" if j.get("mode") == "live" else "auto")), 201


@app.post("/api/volumes/<vid>/backups/<bid>/restore")
def api_vol_restore(vid, bid): return jsonify(volumes.restore(vid, bid))


@app.post("/api/volumes/<vid>/backups/<bid>/clone")
def api_vol_clone(vid, bid):
    j = body()
    return jsonify(volumes.restore_as_new(vid, bid, j.get("name"), j.get("size_mb"))), 201


@app.delete("/api/volumes/<vid>/backups/<bid>")
def api_vol_backup_del(vid, bid): return jsonify(volumes.delete_backup(vid, bid))


# ------------------------------------------------------------------ main
def startup_report():
    h = host.health()
    print(f"[mvm] MicroVM Maker {C.VERSION}  data: {C.DATA}")
    print(f"[mvm] launch mode: {h['mode']}   firecracker: {h['firecracker'] or 'not found'}   kvm: {'ok' if h['kvm'] else 'NO ACCESS'}")
    for p in h["problems"]:
        print(f"[mvm] WARNING: {p}")
    if C.HOST not in ("127.0.0.1", "localhost", "::1") and not TLS:
        print("[mvm] WARNING: listening on a non-loopback address without TLS - put it behind an HTTPS reverse proxy")
    shown = "127.0.0.1" if C.HOST in ("0.0.0.0", "::") else C.HOST
    print(f"[mvm] open {'https' if TLS else 'http'}://{shown}:{C.PORT}")


def main():
    if "--show-token" in sys.argv or "--reset-token" in sys.argv:
        if "--reset-token" in sys.argv:
            print("New access token (shown once, store it safely):\n  " + auth.reset())
        else:
            print("Only a hash of the token is stored. Use --reset-token to create a new one, or set MVM_TOKEN.")
        return
    images.gc_internal(lambda m: print(f"[startup] {m}"))
    vmm.startup(lambda m: print(f"[startup] {m}"))
    replicas.startup()
    gitsources.startup()
    volumes.startup()
    atexit.register(vmm.shutdown_all)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    images.preload()
    startup_report()
    if auth.new_token:
        print("\n  ┌─ ACCESS TOKEN (shown once; only its hash is stored) ─────────────")
        print(f"  │ {auth.new_token}")
        print("  └──────────────────────────────────────────────────────────────────")
        print("  lost it?  python3 app.py --reset-token\n")
    if TLS:
        app.run(C.HOST, C.PORT, threaded=True, ssl_context=(C.TLS_CERT, C.TLS_KEY))
    else:
        try:
            from waitress import serve
            serve(app, host=C.HOST, port=C.PORT, threads=16, ident="mvm", max_request_body_size=4 << 30)
        except ImportError:
            print("[mvm] waitress not installed - using Flask's development server (pip install waitress)")
            app.run(C.HOST, C.PORT, threaded=True)


if __name__ == "__main__":
    main()
