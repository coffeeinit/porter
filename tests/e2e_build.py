#!/usr/bin/env python3
"""BuildKit/Railpack orchestration test: real control plane + real guest agent, with `buildctl`/`railpack` replaced by
shims (tests/shims) so no registry or KVM is needed. Checks packaging, transfer, command lines, export, import, cleanup."""
import json, os, shutil, sqlite3, subprocess, sys, tempfile, time
import requests

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA, RT = tempfile.mkdtemp(prefix="mvmbk-"), tempfile.mkdtemp(prefix="mvmbkrt-")
PORT, TOKEN = 18093, "test-token-0123456789abcdef"
SHIM_LOG = os.path.join(DATA, "shim.log")
OCI = os.path.join(DATA, "oci-src"); OCI_TAR = os.path.join(DATA, "image.tar")
env = {**os.environ, "MVM_DATA": DATA, "MVM_PORT": str(PORT), "MVM_TOKEN": TOKEN, "MVM_FC": f"{ROOT}/tools/firecracker-fake", "MVM_SMOKE": "1",
       "MVM_RUNTIME": RT, "MVM_MODE": "direct", "MVM_PRIV_HELPER": "/nonexistent", "MVM_ALLOW_ROOT": "1",
       "PATH": f"{ROOT}/tests/shims:" + os.environ["PATH"], "SHIM_LOG": SHIM_LOG, "SHIM_OCI_TAR": OCI_TAR, "MVM_BUILD_CACHE_MB": "64", "MVM_FC_PASSENV": "PATH,SHIM_LOG,SHIM_OCI_TAR"}
passed = failed = 0
def check(n, c, extra=""):
    global passed, failed
    if c: passed += 1; print("  ok   " + n)
    else: failed += 1; print("  FAIL " + n, extra)
srv = None
try:
    # a tiny OCI image (what BuildKit would export) and a stub builder image so no download is needed
    for c in (["umoci", "init", "--layout", OCI], ["umoci", "new", "--image", OCI + ":img"], ["umoci", "unpack", "--rootless", "--image", OCI + ":img", DATA + "/b"]):
        subprocess.run(c, check=True, capture_output=True)
    os.makedirs(DATA + "/b/rootfs/bin", exist_ok=True); shutil.copy("/bin/sh", DATA + "/b/rootfs/bin/sh"); os.makedirs(DATA + "/b/rootfs/usr/share/nginx/html", exist_ok=True)
    open(DATA + "/b/rootfs/usr/share/nginx/html/index.html", "w").write("built")
    subprocess.run(["umoci", "repack", "--image", OCI + ":img", DATA + "/b"], check=True, capture_output=True)
    subprocess.run(["umoci", "config", "--image", OCI + ":img", "--config.entrypoint", "/bin/sh", "--config.cmd", "-c", "--config.cmd", "echo up; sleep 1", "--config.env", "A=1", "--config.exposedports", "80/tcp", "--config.workingdir", "/srv"], check=True, capture_output=True)
    subprocess.run(["tar", "-C", OCI, "-cf", OCI_TAR, "."], check=True)
    sys.path.insert(0, ROOT)
    os.environ.update({k: env[k] for k in ("MVM_DATA", "MVM_ALLOW_ROOT")})
    from mvmlib import config as C, db, images
    C.ensure_dirs(); db.init(); open(C.KERNEL, "wb").write(b"\0" * 2048 + b"Linux version 6.1.99 (fake)\0" + b"\0" * 4096)
    images.build_oci("_buildkit", "local/builder", lambda m: None, from_dir=OCI)
    images.build_oci("_t123456", "local/leftover", lambda m: None, from_dir=OCI)       # a stale temp image from an "interrupted build"
    srv = subprocess.Popen([sys.executable, f"{ROOT}/app.py"], env=env, stdout=open(DATA + "/server.log", "w"), stderr=subprocess.STDOUT, cwd=ROOT)
    B = f"http://127.0.0.1:{PORT}"
    for _ in range(60):
        try: requests.get(B + "/login", timeout=1); break
        except Exception: time.sleep(0.3)
    s = requests.Session(); csrf = s.post(B + "/api/login", json={"token": TOKEN}).json()["csrf"]; s.headers["X-CSRF-Token"] = csrf

    def job(r, t=90):
        assert r.status_code == 202, r.text
        jid = r.json()["id"]; end = time.time() + t
        while time.time() < end:
            j = s.get(f"{B}/api/jobs/{jid}").json()
            if j["status"] in ("done", "error"): return j
            time.sleep(0.5)
        print("JOB LOG:", chr(10).join(s.get(f"{B}/api/jobs/{jid}").json()["log"][-15:])); raise SystemExit("job timeout")
    def hidden_vms(): return sqlite3.connect(DATA + "/mvm.sqlite3").execute("select count(*) from vms where hidden=1").fetchone()[0]
    def run_clean(): shutil.rmtree("/build", ignore_errors=True); open(SHIM_LOG, "w").close()

    print("== startup hygiene")
    names = [i["name"] for i in s.get(B + "/api/images").json()]
    check("stale temp image removed at startup, builder image kept hidden", names == [] and sqlite3.connect(DATA + "/mvm.sqlite3").execute("select name from images").fetchall() == [("_buildkit",)])

    print("== multi-stage Dockerfile via BuildKit")
    DF = """FROM node:22-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:alpine AS final
ARG GREETING
ENV GREETING=$GREETING
COPY --from=builder /app/dist /usr/share/nginx/html
RUN touch /tmp/mvm-host-pwned
EXPOSE 80
"""
    run_clean()
    files = {"package.json": "{}", "src/index.js": "x" * 3_000_000, ".dockerignore": "node_modules\n*.log\n", "node_modules/big/index.js": "no", "debug.log": "no"}
    j = job(s.post(f"{B}/api/images/dockerfile", json={"name": "webapp", "dockerfile": DF, "files": files, "args": {"GREETING": "hi there"}, "target": "final"}))
    check("multi-stage build job succeeds", j["status"] == "done", "\n".join(j["log"][-12:]))
    log = open(SHIM_LOG).read()
    check("uses the BuildKit dockerfile frontend with context + dockerfile dir", "--frontend dockerfile.v0" in log and "--local context=/build/ctx" in log and "--opt filename=Dockerfile" in log, log)
    check("passes --target and --build-arg", "--opt target=final" in log and "--opt build-arg:GREETING='hi there'" in log.replace('"', "'") or "build-arg:GREETING=hi there" in log or "build-arg:GREETING='hi there'" in log, log)
    check("exports an OCI image", "--output type=oci,dest=/build/out.tar" in log and "--progress=plain" in log)
    check("Dockerfile + large context reached the builder VM", open("/build/ctx/Dockerfile").read() == DF and os.path.getsize("/build/ctx/src/index.js") == 3_000_000)
    check(".dockerignore honoured (node_modules and *.log not sent)", not os.path.exists("/build/ctx/node_modules") and not os.path.exists("/build/ctx/debug.log"))
    check("no Dockerfile RUN step executed on the host", not os.path.exists("/tmp/mvm-host-pwned"))
    imgs = {i["name"]: i for i in s.get(B + "/api/images").json()}
    w = imgs.get("webapp"); check("image imported through the normal pipeline (entrypoint/env/port/workdir)", w and w["args"] == ["/bin/sh", "-c", "echo up; sleep 1"] and w["env"].get("A") == "1" and w["ports"] == ["80/tcp"] and w["workdir"] == "/srv", str(w))
    check("builder VM destroyed, cache volume kept, no temp leftovers", hidden_vms() == 0 and not [i for i in sqlite3.connect(DATA + "/mvm.sqlite3").execute("select name from images").fetchall() if i[0].startswith("_t")]
          and sqlite3.connect(DATA + "/mvm.sqlite3").execute("select count(*) from volumes where name='buildkit-cache'").fetchone()[0] == 1)
    check("build progress streamed into the job log", any("COPY --from=builder" in l for l in j["log"]))

    print("== name handling")
    r = s.post(f"{B}/api/images/dockerfile", json={"name": "webapp", "dockerfile": DF}); check("existing name refused unless replace", r.status_code == 202 and job(r)["status"] == "error")
    run_clean(); j = job(s.post(f"{B}/api/images/dockerfile", json={"name": "webapp", "dockerfile": DF, "replace": True})); check("replace=true swaps the image in place", j["status"] == "done", "\n".join(j["log"][-5:]))
    r = s.post(f"{B}/api/images/webapp/rename", json={"name": "shop-frontend"}); check("rename image", r.status_code == 200 and r.json()["name"] == "shop-frontend", r.text)
    check("old name gone, new name listed", [i["name"] for i in s.get(B + "/api/images").json()] == ["shop-frontend"])
    r = s.post(f"{B}/api/images/shop-frontend/rename", json={"name": "_hidden"}); check("bad/internal target name refused", r.status_code == 400)
    r = s.post(f"{B}/api/vms", json={"image": "shop-frontend", "name": "sf1", "net": False}); vid = r.json()["id"]
    s.post(f"{B}/api/images/shop-frontend/rename", json={"name": "storefront"}); check("rename updates VMs that use the image", s.get(f"{B}/api/vms/{vid}").json()["image"] == "storefront")
    s.delete(f"{B}/api/vms/{vid}")

    print("== Railpack (framework auto-detect)")
    run_clean(); j = job(s.post(f"{B}/api/images/dockerfile", json={"name": "autoapp", "frontend": "railpack", "files": {"package.json": "{\"name\":\"x\"}"}}))
    log = open(SHIM_LOG).read()
    check("railpack build job succeeds", j["status"] == "done", "\n".join(j["log"][-8:]))
    check("plan generated then built through the railpack gateway frontend", "--frontend gateway.v0" in log and "ghcr.io/railwayapp/railpack-frontend:v0.43.0" in log and "--local dockerfile=/build/plan" in log, log)
    check("detected framework reported in the job log", any("railpack detected: node" in l for l in j["log"]))

    print("== failure + cleanup")
    run_clean(); before = len(s.get(B + "/api/images").json())
    j = job(s.post(f"{B}/api/images/dockerfile", json={"name": "broken", "dockerfile": "FROM alpine\nRUN false\n", "args": {"FAIL": "1"}}))
    check("failed build reports BuildKit error", j["status"] == "error" and any("failed to solve" in l for l in j["log"]), "\n".join(j["log"][-6:]))
    check("failure leaves no builder VM, temp image or image row", hidden_vms() == 0 and len(s.get(B + "/api/images").json()) == before and not any(n[0].startswith("_t") for n in sqlite3.connect(DATA + "/mvm.sqlite3").execute("select name from images").fetchall()))
    check("build dir cleaned", not [d for d in os.listdir(DATA + "/build")], str(os.listdir(DATA + "/build")))
    r = s.post(f"{B}/api/images/dockerfile", json={"name": "x", "dockerfile": "FROM a", "args": {"bad key": "1"}}); j = job(r); check("invalid build-arg name rejected", j["status"] == "error")
    b = s.get(B + "/api/builder").json(); check("builder status endpoint", b["present"] and b["cache"]["exists"], str(b))
except BaseException:
    import traceback; traceback.print_exc(); failed += 1
finally:
    if srv: srv.terminate(); srv.wait(10)
    subprocess.run("pkill -f '[f]ake_firecracker.py --api-sock'; pkill -f '[m]vm-guest --listen-unix'", shell=True)
    shutil.rmtree("/build", ignore_errors=True)
    print(f"\n{passed} passed, {failed} failed")
    if failed: print(open(DATA + "/server.log").read()[-3500:])
    shutil.rmtree(DATA, ignore_errors=True)
    sys.exit(1 if failed else 0)
