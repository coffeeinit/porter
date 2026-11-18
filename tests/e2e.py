#!/usr/bin/env python3
"""End-to-end test of the control plane against tools/firecracker-fake + the REAL guest agent.
Run:  python3 tests/e2e.py        (no KVM, no root needed; needs: gcc-built guest/mvm-guest, e2fsprogs, fakeroot optional)"""
import io, json, os, re, shutil, signal, subprocess, sys, tarfile, tempfile, time
import requests

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA = tempfile.mkdtemp(prefix="mvmtest-")
PORT = 18089
TOKEN = "test-token-0123456789abcdef"
env = {**os.environ, "MVM_DATA": DATA, "MVM_PORT": str(PORT), "MVM_TOKEN": TOKEN, "MVM_FC": f"{ROOT}/tools/firecracker-fake",
       "MVM_SMOKE": "1", "MVM_RUNTIME": tempfile.mkdtemp(prefix="mvmrt-"), "MVM_MODE": "direct", "MVM_PRIV_HELPER": "/nonexistent", "MVM_ALLOW_ROOT": "1"}
srv = subprocess.Popen([sys.executable, f"{ROOT}/app.py"], env=env, stdout=open(f"{DATA}/server.log", "w"), stderr=subprocess.STDOUT, cwd=ROOT)
B = f"http://127.0.0.1:{PORT}"
passed = failed = 0


def check(name, cond, extra=""):
    global passed, failed
    if cond:
        passed += 1; print(f"  ok   {name}")
    else:
        failed += 1; print(f"  FAIL {name} {extra}")


def wait_job(s, jid, t=300):
    end = time.time() + t
    while time.time() < end:
        j = s.get(f"{B}/api/jobs/{jid}").json()
        if j["status"] in ("done", "error"):
            return j
        time.sleep(0.5)
    raise SystemExit("job timeout")


try:
    for _ in range(60):
        try:
            requests.get(B + "/login", timeout=1); break
        except Exception:
            time.sleep(0.3)
    print("== security")
    r = requests.get(B + "/api/vms"); check("API needs auth", r.status_code == 401)
    r = requests.get(B + "/", allow_redirects=False); check("page redirects to /login", r.status_code == 302)
    r = requests.get(B + "/login"); check("security headers", "frame-ancestors 'none'" in r.headers.get("Content-Security-Policy", "") and r.headers.get("X-Frame-Options") == "DENY")
    r = requests.post(B + "/api/login", json={"token": "wrong"}); check("bad token rejected", r.status_code == 401)
    s = requests.Session()
    r = s.post(B + "/api/login", json={"token": TOKEN}); check("login ok", r.status_code == 200 and "HttpOnly" in r.headers.get("Set-Cookie", "") and "SameSite=Strict" in r.headers.get("Set-Cookie", ""))
    csrf = r.json()["csrf"]
    r = s.post(B + "/api/vms", json={}); check("CSRF enforced", r.status_code == 403)
    s.headers["X-CSRF-Token"] = csrf
    r = s.post(B + "/api/vms", json={}, headers={"Origin": "http://evil.example"}); check("cross-origin refused", r.status_code == 403)
    r = requests.get(B + "/api/vms", headers={"Authorization": "Bearer " + TOKEN}); check("bearer token works", r.status_code == 200)
    r = requests.get(B + "/api/vms", headers={"Host": "evil.example"}); check("bad Host refused", r.status_code == 403)

    print("== image import")
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tf:
        for name, data, mode in (("bin/sh", b"#!/bin/sh\n", 0o755), ("etc/os-release", b"NAME=test\n", 0o644)):
            ti = tarfile.TarInfo(name); ti.size = len(data); ti.mode = mode; ti.uid = 0; tf.addfile(ti, io.BytesIO(data))
        d = tarfile.TarInfo("var/www"); d.type = tarfile.DIRTYPE; d.mode = 0o755; d.uid = 33; d.gid = 33; tf.addfile(d)
        l = tarfile.TarInfo("var/run"); l.type = tarfile.SYMTYPE; l.linkname = "/run"; tf.addfile(l)
        e = tarfile.TarInfo("var/run/evil"); e.size = 3; tf.addfile(e, io.BytesIO(b"abc"))   # writes THROUGH an absolute symlink: must land in rootfs/run, not host /run
    r = s.post(B + "/api/images/import", data={"name": "tiny"}, files={"file": ("rootfs.tar.gz", buf.getvalue())})
    check("import accepted", r.status_code == 202, r.text)
    j = wait_job(s, r.json()["id"]); check("import job done", j["status"] == "done", "\n".join(j["log"][-8:]))
    check("host /run untouched", not os.path.exists("/run/evil"))
    imgs = s.get(B + "/api/images").json(); check("image listed", [i["name"] for i in imgs] == ["tiny"])
    ext4 = os.path.join(DATA, "images/tiny/rootfs.ext4")
    dump = subprocess.run(["debugfs", "-R", "ls -l /var", ext4], capture_output=True, text=True).stdout
    check("ext4 has /var/www owned by uid 33 (fakeroot)" if shutil.which("fakeroot") else "ext4 built", ("33" in dump) if shutil.which("fakeroot") else True, dump[:200])
    dump = subprocess.run(["debugfs", "-R", "ls -l /run", ext4], capture_output=True, text=True).stdout
    check("symlink write landed inside rootfs", "evil" in dump, dump[:200])
    dump = subprocess.run(["debugfs", "-R", "stat /mvm-init", ext4], capture_output=True, text=True).stdout
    check("guest init injected", "Mode:  0755" in dump, dump[:120])

    print("== kernel (dummy) + VM lifecycle")
    open(os.path.join(DATA, "vmlinux"), "wb").write(b"\0" * 2048 + b"Linux version 6.1.99 (fake)\0" + b"\0" * 4096)
    r = s.post(B + "/api/vms", json={"image": "tiny", "name": "web1", "vcpu": 2, "mem": 256, "disk_mb": 300, "env": {"FOO": "bar"},
                                      "balloon": {"amount_mib": 0, "deflate_on_oom": True, "stats_polling_interval_s": 1}, "hotplug_mib": 512,
                                      "mmds": {"data": {"hello": "world"}}, "rx_mbit": 50, "net": False, "autostart": False})
    check("create VM", r.status_code == 201, r.text)
    vm = r.json(); vid = vm["id"]
    check("disk grown to 300 MiB", vm["disk_mb"] >= 300, str(vm["disk_mb"]))
    dump = subprocess.run(["debugfs", "-R", "cat /etc/mvm/env", os.path.join(DATA, "vms", vid, "rootfs.ext4")], capture_output=True, text=True).stdout
    check("per-VM env written into disk", "FOO=bar" in dump, dump)
    r = s.post(B + "/api/vms", json={"image": "tiny", "name": "web1"}); check("duplicate name -> 409", r.status_code == 409)
    r = s.post(B + "/api/vms", json={"image": "tiny", "boot_args": "init=/bin/evil"}); check("init= override refused", r.status_code == 400)
    r = s.post(f"{B}/api/vms/{vid}/start"); check("start VM", r.status_code == 200 and r.json()["status"] == "running", r.text)
    time.sleep(0.5)
    v = s.get(f"{B}/api/vms/{vid}").json(); check("running + agent reachable", v["status"] == "running" and v["agent"], json.dumps(v)[:300])
    r = s.post(f"{B}/api/vms/{vid}/start"); check("double start -> 409", r.status_code == 409)
    r = s.post(f"{B}/api/vms/{vid}/exec", json={"cmd": "echo hello-from-guest; exit 7"}).json()
    check("exec via vsock agent", r["stdout"].strip() == "hello-from-guest" and r["exit_code"] == 7, str(r))
    r = s.post(f"{B}/api/vms/{vid}/files", data={"path": "/tmp/mvmtest-up/x.txt", "mode": "600"}, files={"file": ("x.txt", b"payload-123")})
    check("file upload", r.status_code == 200, r.text)
    r = s.get(f"{B}/api/vms/{vid}/files", params={"path": "/tmp/mvmtest-up/x.txt"}); check("file download", r.content == b"payload-123")
    r = s.get(f"{B}/api/vms/{vid}/stats").json(); check("guest stats", r.get("mem_total", 0) > 0 and "load1" in r)
    r = s.get(f"{B}/api/vms/{vid}/metrics").json(); check("firecracker metrics + balloon", r["fc"] and r["balloon"] is not None, str(r)[:200])
    r = s.post(f"{B}/api/vms/{vid}/live", json={"balloon_mib": 64}); check("live balloon inflate", r.status_code == 200 and r.json()["config"]["balloon"]["amount_mib"] == 64, r.text)
    r = s.post(f"{B}/api/vms/{vid}/live", json={"hotplug_mib": 256}); check("live memory hotplug", r.status_code == 200, r.text)
    r = s.post(f"{B}/api/vms/{vid}/live", json={"hotplug_mib": 4096}); check("hotplug above total refused", r.status_code == 400)
    r = s.post(f"{B}/api/vms/{vid}/live", json={"rx_mbit": 10}); check("live rate limit needs NIC -> 400", r.status_code == 400)
    r = s.post(f"{B}/api/vms/{vid}/live", json={"mmds": {"hello": "again"}}); check("live metadata update", r.status_code == 200, r.text)
    r = s.post(f"{B}/api/vms/{vid}/pause"); check("pause", r.json()["status"] == "paused", r.text)
    r = s.post(f"{B}/api/vms/{vid}/exec", json={"cmd": "true"}); check("exec while paused -> 409", r.status_code == 409)
    r = s.post(f"{B}/api/vms/{vid}/snapshots", json={"name": "s1"}); check("snapshot of paused VM", r.status_code == 201, r.text)
    r = s.post(f"{B}/api/vms/{vid}/resume"); check("resume", r.json()["status"] == "running")
    r = s.post(f"{B}/api/vms/{vid}/snapshots", json={"name": "s2", "note": "live"}); check("snapshot of running VM", r.status_code == 201, r.text)
    snaps = s.get(f"{B}/api/vms/{vid}/snapshots").json(); check("2 snapshots listed", len(snaps) == 2)
    r = s.post(f"{B}/api/vms/{vid}/snapshots/{snaps[0]['id']}/restore"); check("restore while running -> 409", r.status_code == 409)
    con = s.get(f"{B}/api/vms/{vid}/firecracker-log").text; check("firecracker log readable", "fake log" in con, con)
    t0 = time.time(); r = s.post(f"{B}/api/vms/{vid}/stop"); check("graceful stop via agent", r.json()["status"] == "stopped" and time.time() - t0 < 10, r.text)
    check("disk still present at rest", os.path.exists(os.path.join(DATA, "vms", vid, "rootfs.ext4")))
    r = s.post(f"{B}/api/vms/{vid}/snapshots/{snaps[1]['id']}/restore"); check("restore snapshot", r.status_code == 200 and r.json()["status"] == "running", r.text)
    time.sleep(0.4)
    r = s.post(f"{B}/api/vms/{vid}/exec", json={"cmd": "echo after-restore"}).json(); check("exec after restore", "after-restore" in r["stdout"], str(r))
    r = s.post(f"{B}/api/vms/{vid}/clone", json={"name": "web1-copy"}); check("clone running VM refused", r.status_code == 409)
    s.post(f"{B}/api/vms/{vid}/kill")
    r = s.post(f"{B}/api/vms/{vid}/clone", json={"name": "web1-copy"}); check("clone stopped VM", r.status_code == 201, r.text)
    r = s.patch(f"{B}/api/vms/{vid}", json={"vcpu": 4, "mem": 512}); check("offline resize", r.json()["vcpu"] == 4 and r.json()["mem"] == 512, r.text)
    r = s.delete(f"{B}/api/images/tiny"); check("image in use can't be deleted", r.status_code == 409)


    print("== replica sets")
    r = s.post(f"{B}/api/replicasets", json={"name": "web", "image": "tiny", "count": 3, "vcpu": 1, "mem": 128, "net": False, "env": {"COLOR": "blue"}})
    check("create replica set x3", r.status_code == 201, r.text)
    rs = r.json(); rid = rs["id"]
    check("3 replicas, all running", [x["name"] for x in rs["replicas"]] == ["web-1", "web-2", "web-3"] and rs["running"] == 3, json.dumps(rs)[:300])
    time.sleep(0.8)
    rs = s.get(f"{B}/api/replicasets/{rid}").json(); check("all replicas ready (agent answers)", rs["ready"] == 3, str(rs["ready"]))
    r2 = s.post(f"{B}/api/vms/{rs['replicas'][1]['id']}/exec", json={"cmd": "echo ok"}).json(); check("exec on a replica", r2["stdout"].strip() == "ok")
    dump = subprocess.run(["debugfs", "-R", "cat /etc/mvm/env", os.path.join(DATA, "vms", rs["replicas"][1]["id"], "rootfs.ext4")], capture_output=True, text=True).stdout
    check("replica gets REPLICA_INDEX/SET + shared env", "REPLICA_INDEX=2" in dump and "REPLICA_SET=web" in dump and "COLOR=blue" in dump, dump)
    ips = [x["ip"] for x in rs["replicas"]]; check("each replica has its own IP", len(set(ips)) == 3, str(ips))
    r = s.post(f"{B}/api/replicasets", json={"name": "web", "image": "tiny", "count": 1}); check("duplicate set name -> 409", r.status_code == 409)
    r = s.post(f"{B}/api/replicasets", json={"name": "big", "image": "tiny", "count": 500}); check("absurd count refused", r.status_code == 400)
    r = s.post(f"{B}/api/replicasets/{rid}/scale", json={"count": 2}); check("scale down to 2 (highest index removed)", [x["name"] for x in r.json()["replicas"]] == ["web-1", "web-2"], r.text[:200])
    r = s.post(f"{B}/api/replicasets/{rid}/scale", json={"count": 4}); check("scale up to 4", [x["name"] for x in r.json()["replicas"]] == ["web-1", "web-2", "web-3", "web-4"] and r.json()["running"] == 4, r.text[:300])
    victim = r.json()["replicas"][1]["id"]
    s.post(f"{B}/api/vms/{victim}/kill")
    print("   (waiting for auto-heal ...)")
    healed = False
    for _ in range(40):
        time.sleep(1)
        st = {x["id"]: x["status"] for x in s.get(f"{B}/api/replicasets/{rid}").json()["replicas"]}
        if st.get(victim) == "running":
            healed = True; break
    check("auto-heal restarted the killed replica", healed)
    old_ids = {x["id"] for x in s.get(f"{B}/api/replicasets/{rid}").json()["replicas"]}
    r = s.post(f"{B}/api/replicasets/{rid}/rollout"); check("rollout accepted", r.status_code == 202, r.text)
    j = wait_job(s, r.json()["id"]); check("rolling update done", j["status"] == "done", "\n".join(j["log"][-6:]))
    rs = s.get(f"{B}/api/replicasets/{rid}").json()
    check("rollout replaced every replica, same names, all running", {x["id"] for x in rs["replicas"]}.isdisjoint(old_ids) and [x["name"] for x in rs["replicas"]] == ["web-1", "web-2", "web-3", "web-4"] and rs["running"] == 4, json.dumps(rs)[:300])
    r = s.post(f"{B}/api/replicasets/{rid}/stop"); check("stop set", r.json()["running"] == 0 and r.json()["state"] == "stopped", r.text[:200])
    time.sleep(14)
    rs = s.get(f"{B}/api/replicasets/{rid}").json(); check("stopped set is NOT auto-healed", rs["running"] == 0, str(rs["running"]))
    r = s.post(f"{B}/api/replicasets/{rid}/start"); check("start set", r.json()["running"] == 4, r.text[:200])
    r = s.patch(f"{B}/api/replicasets/{rid}", json={"mem": 192}); check("template change flags pending rollout", "rolling update" in (r.json()["note"] or ""), r.text[:200])
    r = s.patch(f"{B}/api/replicasets/{rid}", json={"lb_port": 80, "service_port": 80}); check("lb port < 1024 refused", r.status_code == 400)
    check("VMs of a set carry rset/ridx", all(v["rset"] == rid for v in s.get(f"{B}/api/vms").json() if v["name"].startswith("web-")))
    r = s.delete(f"{B}/api/replicasets/{rid}"); check("delete set removes replicas", r.status_code == 200 and not [v for v in s.get(f"{B}/api/vms").json() if v["name"].startswith("web-")])

    print("== load balancer (unit)")
    import socket, threading
    sys.path.insert(0, ROOT)
    from mvmlib import lb
    def echo(port, tag):
        ls = socket.socket(); ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); ls.bind(("127.0.0.1", port)); ls.listen(8)
        def run():
            while True:
                try: c, _ = ls.accept()
                except OSError: return
                c.recv(100); c.sendall(tag); c.close()
        threading.Thread(target=run, daemon=True).start(); return ls
    e1 = echo(19101, b"A"); e2 = echo(19102, b"B")
    bal = lb.Balancer("127.0.0.1", 19100, 19101, lambda: [("r1", "127.0.0.1")]); bal.start()
    def hit(port):
        c = socket.create_connection(("127.0.0.1", port), 3); c.sendall(b"x"); d = c.recv(10); c.close(); return d
    check("balancer forwards to a backend", hit(19100) == b"A")
    e1.close(); time.sleep(0.2)
    bal2 = lb.Balancer("127.0.0.1", 19103, 19102, lambda: [("dead", "127.0.0.1"), ("r2", "127.0.0.1")]); bal2.service_port = 19102; bal2.start()
    check("balancer serves from backend and counts it", hit(19103) == b"B" and bal2.stats["total"] == 1, str(bal2.stats))
    bal3 = lb.Balancer("127.0.0.1", 19104, 19999, lambda: [("x", "127.0.0.1")]); bal3.start()
    try: c = socket.create_connection(("127.0.0.1", 19104), 2); d = c.recv(10); c.close()
    except OSError: d = b""
    check("no healthy backend -> connection closed, failure counted", d == b"" and bal3.stats["failed"] >= 1, str(bal3.stats))
    for b_ in (bal, bal2, bal3): b_.stop()


    print("== persistent volumes, backups, restore, resize")
    def dfs(path, *cmds):
        return subprocess.run(["debugfs", "-w", "-R", "; ".join(cmds) if False else cmds[0], path], capture_output=True, text=True).stdout
    def put_marker(volfile, text):
        tmpf = os.path.join(DATA, "marker.tmp"); open(tmpf, "w").write(text)
        subprocess.run(["debugfs", "-w", "-f", "-", volfile], input=f"rm /marker.txt\nwrite {tmpf} /marker.txt\n", capture_output=True, text=True)
    def get_marker(volfile): return subprocess.run(["debugfs", "-R", "cat /marker.txt", volfile], capture_output=True, text=True).stdout
    r = s.post(f"{B}/api/volumes", json={"name": "pgdata", "size_mb": 64, "note": "database"}); check("create volume", r.status_code == 201 and r.json()["size_mb"] == 64, r.text)
    vol = r.json(); vf = os.path.join(DATA, "volumes", vol["id"] + ".ext4")
    check("volume is a sparse ext4 file", os.path.getsize(vf) == 64 << 20 and os.stat(vf).st_blocks * 512 < 20 << 20)
    check("volume fsck clean", subprocess.run(["e2fsck", "-fn", vf], capture_output=True).returncode == 0)
    r = s.post(f"{B}/api/volumes", json={"name": "pgdata", "size_mb": 64}); check("duplicate volume name -> 409", r.status_code == 409)
    r = s.post(f"{B}/api/volumes", json={"name": "tiny", "size_mb": 2}); check("volume below 16 MiB refused", r.status_code == 400)
    r = s.post(f"{B}/api/vms", json={"image": "tiny", "name": "dbvm", "mem": 128, "net": False}); dvm = r.json()["id"]
    r = s.post(f"{B}/api/volumes/{vol['id']}/attach", json={"vm": dvm, "mount": "/proc/x"}); check("reserved mount path refused", r.status_code == 400)
    r = s.post(f"{B}/api/volumes/{vol['id']}/attach", json={"vm": dvm, "mount": "/var/lib/data"}); check("attach volume to stopped VM", r.status_code == 200 and r.json()["vm_name"] == "dbvm", r.text)
    r = s.post(f"{B}/api/volumes/{vol['id']}/attach", json={"vm": dvm, "mount": "/other"}); check("volume cannot be attached twice", r.status_code == 409)
    r = s.patch(f"{B}/api/vms/{dvm}", json={"volumes": [{"vol": "deadbeef", "mount": "/x"}], "vcpu": 1}); check("volumes cannot be injected through VM settings", r.status_code == 200 and [x["id"] for x in r.json()["volumes"]] == [vol["id"]], r.text)
    put_marker(vf, "version-1")
    r = s.post(f"{B}/api/vms/{dvm}/start"); check("VM with a volume starts (fsck + extra drive)", r.status_code == 200 and r.json()["status"] == "running", r.text)
    m = s.get(f"{B}/api/vms/{dvm}/machine").json()["config"]
    check("Firecracker got the volume as a second drive + guest mount spec", len(m["drives"]) == 2 and "mvm.vols=vdb:/var/lib/data" in m["boot"]["boot_args"], json.dumps(m)[:400])
    check("detach/resize/attach refused while running", s.post(f"{B}/api/volumes/{vol['id']}/detach").status_code == 409 and s.post(f"{B}/api/volumes/{vol['id']}/resize", json={"size_mb": 128}).status_code == 409)
    r = s.post(f"{B}/api/vms/{dvm}/snapshots", json={"name": "nope"}); check("VM snapshot refused when volumes are attached (use backups)", r.status_code == 400 and "volume" in r.text)
    s.patch(f"{B}/api/volumes/{vol['id']}", json={"pre_cmd": "echo hook-ran > /tmp/mvm-prebackup-ran", "backup_keep": 2})
    try: os.unlink("/tmp/mvm-prebackup-ran")
    except OSError: pass
    r = s.post(f"{B}/api/volumes/{vol['id']}/backups", json={"note": "running"}); b1 = r.json()
    check("backup of a RUNNING VM: sync + pause + copy", r.status_code == 201 and "paused point-in-time" in b1["consistent"] and "pre-backup" in b1["consistent"], r.text)
    check("pre-backup command (e.g. a DB dump) ran inside the guest first", os.path.exists("/tmp/mvm-prebackup-ran"))
    check("VM resumed after the backup", s.get(f"{B}/api/vms/{dvm}").json()["status"] == "running")
    r = s.post(f"{B}/api/volumes/{vol['id']}/backups", json={"mode": "live"}); check("live (crash-consistent) backup mode", r.status_code == 201 and "crash-consistent" in r.json()["consistent"], r.text)
    s.post(f"{B}/api/vms/{dvm}/stop")
    r = s.post(f"{B}/api/volumes/{vol['id']}/backups", json={"note": "offline"}); b3 = r.json(); check("offline backup of a stopped VM", "offline" in b3["consistent"])
    bl = s.get(f"{B}/api/volumes/{vol['id']}/backups").json(); check("retention keeps only the newest 2", len(bl) == 2 and b1["id"] not in [x["id"] for x in bl], str(len(bl)))
    put_marker(vf, "version-2-after-backup")
    r = s.post(f"{B}/api/volumes/{vol['id']}/backups/{b3['id']}/restore"); check("restore backup", r.status_code == 200 and r.json()["restored"] == b3["id"], r.text)
    check("data is back to the backed-up state", get_marker(vf).strip() == "version-1", get_marker(vf))
    kinds = [x["kind"] for x in s.get(f"{B}/api/volumes/{vol['id']}/backups").json()]; check("a pre-restore safety copy was kept", "pre-restore" in kinds, str(kinds))
    safety = [x for x in s.get(f"{B}/api/volumes/{vol['id']}/backups").json() if x["kind"] == "pre-restore"][0]
    r = s.post(f"{B}/api/volumes/{vol['id']}/backups/{safety['id']}/clone", json={"name": "pgdata-before-restore"}); check("clone a backup into a new volume (staging copy)", r.status_code == 201, r.text)
    cf = os.path.join(DATA, "volumes", r.json()["id"] + ".ext4"); check("the clone holds the pre-restore data", get_marker(cf).strip() == "version-2-after-backup", get_marker(cf))
    r = s.post(f"{B}/api/volumes/{vol['id']}/resize", json={"size_mb": 32}); check("shrinking is refused with guidance", r.status_code == 400 and "grow" in r.text)
    r = s.post(f"{B}/api/volumes/{vol['id']}/resize", json={"size_mb": 128}); check("grow volume 64 -> 128 MiB", r.status_code == 200 and r.json()["size_mb"] == 128 and os.path.getsize(vf) == 128 << 20, r.text)
    sb = subprocess.run(["dumpe2fs", "-h", vf], capture_output=True, text=True).stdout; check("filesystem really grew (resize2fs)", "Block count:              131072" in sb or "Block count:" in sb and int(re.search(r"Block count:\s+(\d+)", sb).group(1)) * int(re.search(r"Block size:\s+(\d+)", sb).group(1)) >= 120 << 20, sb[:300])
    check("data survived the resize", get_marker(vf).strip() == "version-1")
    r = s.post(f"{B}/api/volumes/{vol['id']}/check"); check("volume check (e2fsck)", r.json()["ok"] is True, r.text)
    r = s.delete(f"{B}/api/volumes/{vol['id']}"); check("attached volume cannot be deleted", r.status_code == 409)
    s.delete(f"{B}/api/vms/{dvm}")
    vv = s.get(f"{B}/api/volumes/{vol['id']}").json(); check("deleting the VM keeps the volume (detached, data intact)", vv["attached_to"] is None and get_marker(vf).strip() == "version-1")
    r = s.delete(f"{B}/api/volumes/{vol['id']}"); check("detached volume can be deleted, backups go with it", r.status_code == 200 and not os.path.exists(vf) and not os.path.isdir(os.path.join(DATA, "backups", vol["id"])) or not os.listdir(os.path.join(DATA, "backups", vol["id"])))
    s.delete(f"{B}/api/volumes/{s.get(f'{B}/api/volumes').json()[0]['id']}")

    print("== replica sets with per-replica volumes + least_conn")
    r = s.post(f"{B}/api/replicasets", json={"name": "db", "image": "tiny", "count": 2, "mem": 128, "net": False, "lb_algo": "weird"}); check("unknown load balancing algorithm refused", r.status_code == 400)
    r = s.post(f"{B}/api/replicasets", json={"name": "db", "image": "tiny", "count": 2, "mem": 128, "net": False, "lb_algo": "least_conn",
                                              "volumes": [{"tag": "data", "size_mb": 32, "mount": "/var/lib/db"}]})
    check("create stateful replica set (volume per replica)", r.status_code == 201 and r.json()["lb_algo"] == "least_conn", r.text)
    rid = r.json()["id"]; vols = {v["name"]: v for v in s.get(f"{B}/api/volumes").json()}
    check("one volume per replica, attached with the right mount", sorted(vols) == ["db-1-data", "db-2-data"] and all(v["attached_to"] and v["mount"] == "/var/lib/db" for v in vols.values()), str(vols.keys()))
    ids_before = {k: v["id"] for k, v in vols.items()}
    put_marker_path = lambda name: os.path.join(DATA, "volumes", ids_before[name] + ".ext4")
    r = s.post(f"{B}/api/replicasets/{rid}/rollout"); j = wait_job(s, r.json()["id"]); check("rolling update of a stateful set", j["status"] == "done", "\n".join(j["log"][-5:]))
    vols2 = {v["name"]: v for v in s.get(f"{B}/api/volumes").json()}
    check("rollout kept the SAME volumes and re-attached them to the new VMs", {k: v["id"] for k, v in vols2.items()} == ids_before and all(v["attached_to"] for v in vols2.values()))
    s.post(f"{B}/api/replicasets/{rid}/scale", json={"count": 1}); v3 = {v["name"]: v for v in s.get(f"{B}/api/volumes").json()}
    check("scale down keeps the removed replica's data (detached)", v3["db-2-data"]["attached_to"] is None and v3["db-1-data"]["attached_to"])
    s.post(f"{B}/api/replicasets/{rid}/scale", json={"count": 2}); v4 = {v["name"]: v for v in s.get(f"{B}/api/volumes").json()}
    check("scale back up re-attaches the same volume", v4["db-2-data"]["id"] == ids_before["db-2-data"] and v4["db-2-data"]["attached_to"])
    r = s.patch(f"{B}/api/replicasets/{rid}", json={"lb_algo": "round_robin"}); check("change algorithm", r.json()["lb_algo"] == "round_robin")
    r = s.delete(f"{B}/api/replicasets/{rid}"); check("deleting a set keeps its volumes by default", sorted(r.json()["kept_volumes"]) == ["db-1-data", "db-2-data"], r.text)
    for v in s.get(f"{B}/api/volumes").json(): s.delete(f"{B}/api/volumes/{v['id']}")
    check("cleanup volumes", s.get(f"{B}/api/volumes").json() == [])

    print("== load balancer: least_conn vs round_robin (unit)")
    import socket, threading
    from mvmlib import lb as lbm
    def srv(ip, port, hold):
        ls = socket.socket(); ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); ls.bind((ip, port)); ls.listen(8); got = []
        def run():
            while True:
                try: c, _ = ls.accept()
                except OSError: return
                got.append(c)
                if not hold: c.recv(10); c.sendall(ip.encode()); c.close()
        threading.Thread(target=run, daemon=True).start(); return ls, got
    def pick(port):
        c = socket.create_connection(("127.0.0.1", port), 3); c.settimeout(1.0)
        try: c.sendall(b"x"); d = c.recv(20); return d, c
        except socket.timeout: return b"", c
    for algo, want3 in (("round_robin", b"127.0.0.1"), ("least_conn", b"127.0.0.2")):
        la, ga = srv("127.0.0.1", 19210 if algo == "round_robin" else 19220, True); lb2, g2 = srv("127.0.0.2", 19210 if algo == "round_robin" else 19220, False)
        sp = 19210 if algo == "round_robin" else 19220; lp = sp + 1
        bal = lbm.Balancer("127.0.0.1", lp, sp, lambda: [("r1", "127.0.0.1"), ("r2", "127.0.0.2")], algo=algo); bal.start()
        d1, c1 = pick(lp); time.sleep(0.2); d2, c2 = pick(lp); d3, c3 = pick(lp)
        check(f"{algo}: first connection to r1 (held open), second to r2", d1 == b"" and d2 == b"127.0.0.2", f"{d1} {d2}")
        check(f"{algo}: third connection -> {'r1 (strict rotation)' if algo == 'round_robin' else 'r2 (least active connections)'}", (d3 == want3) if algo == "least_conn" else (d3 == b"" and bal.live.get("r1", 0) >= 2), f"{d3} {bal.live}")
        bal.stop(); la.close(); lb2.close()
        for c in (c1, c2, c3): c.close()

    print("== GitHub sources + webhook")
    import hmac, hashlib
    r = s.post(f"{B}/api/sources", json={"name": "app", "repo": "https://user:pw@github.com/o/r", "kind": "dockerfile"}); check("credentials in repo URL refused", r.status_code == 400)
    r = s.post(f"{B}/api/sources", json={"name": "app", "repo": "http://github.com/o/r", "kind": "dockerfile"}); check("http:// refused", r.status_code == 400)
    r = s.post(f"{B}/api/sources", json={"name": "app", "repo": "https://github.com/o/r", "kind": "dockerfile", "path": "../etc/passwd"}); check("path traversal refused", r.status_code == 400)
    r = s.post(f"{B}/api/sources", json={"name": "app", "repo": "https://github.com/o/r.git", "branch": "main", "kind": "dockerfile", "path": "deploy/Dockerfile", "context": ".", "image_name": "myapp", "auto_rebuild": False})
    check("create source", r.status_code == 201 and r.json()["secret"] and r.json()["repo"] == "https://github.com/o/r", r.text)
    src = r.json(); sec = src["secret"]
    check("secret not in list view", all(x["secret"] is None for x in s.get(f"{B}/api/sources").json()))
    body_ = json.dumps({"ref": "refs/heads/main", "after": "a" * 40}).encode()
    sig = lambda b: "sha256=" + hmac.new(sec.encode(), b, hashlib.sha256).hexdigest()
    hook = f"{B}/hooks/github/{src['id']}"
    r = requests.post(hook, data=body_, headers={"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + "0" * 64}); check("webhook: bad signature -> 401", r.status_code == 401)
    r = requests.post(hook, data=body_, headers={"X-GitHub-Event": "push"}); check("webhook: no signature -> 401", r.status_code == 401)
    r = requests.post(hook, data=b"{}", headers={"X-GitHub-Event": "ping", "X-Hub-Signature-256": sig(b"{}")}); check("webhook: signed ping ok (no session/CSRF needed)", r.status_code == 200, r.text)
    other = json.dumps({"ref": "refs/heads/dev"}).encode()
    r = requests.post(hook, data=other, headers={"X-GitHub-Event": "push", "X-Hub-Signature-256": sig(other)}); check("webhook: other branch ignored", r.status_code == 200 and "ignored" in r.json(), r.text)
    r = requests.post(hook, data=body_, headers={"X-GitHub-Event": "push", "X-Hub-Signature-256": sig(body_)}); check("webhook: auto-rebuild off -> ignored", r.status_code == 200 and "ignored" in r.json(), r.text)
    r = s.patch(f"{B}/api/sources/{src['id']}", json={"auto_rebuild": True}); check("enable auto rebuild", r.json()["auto_rebuild"] == 1, r.text)
    r = requests.post(hook, data=body_, headers={"X-GitHub-Event": "push", "X-Hub-Signature-256": sig(body_)}); check("webhook: signed push -> build queued", r.status_code == 202 and r.json()["queued"], r.text)
    jid = r.json()["job"]; j = wait_job(s, jid, 60); check("build job ran (fails here: no network to GitHub - expected)", j["status"] in ("error", "done"), j["status"])
    st = [x for x in s.get(f"{B}/api/sources").json() if x["id"] == src["id"]][0]; check("source records failed status", st["last_status"] == "failed" and not st["building"], json.dumps(st)[:200])
    r = s.post(f"{B}/api/sources/{src['id']}/rotate-secret"); check("rotate secret", r.status_code == 200 and r.json()["secret"] != sec)
    r = requests.post(hook, data=body_, headers={"X-GitHub-Event": "push", "X-Hub-Signature-256": sig(body_)}); check("old secret no longer works", r.status_code == 401)
    r = requests.post(f"{B}/hooks/github/nonexistent", data=b"{}"); check("unknown source -> 404", r.status_code == 404)
    r = s.patch(f"{B}/api/sources/{src['id']}", json={"rollout_set": "nope"}); check("unknown rollout set refused", r.status_code == 404)
    r = s.delete(f"{B}/api/sources/{src['id']}"); check("delete source", r.status_code == 200)

    print("== persistence across a restart of the web app (VM must survive)")
    s.post(f"{B}/api/vms/{vid}/start")
    srv.send_signal(signal.SIGTERM); srv.wait(10)
    srv = subprocess.Popen([sys.executable, f"{ROOT}/app.py"], env=env, stdout=open(f"{DATA}/server2.log", "w"), stderr=subprocess.STDOUT, cwd=ROOT)
    for _ in range(60):
        try:
            requests.get(B + "/login", timeout=1); break
        except Exception:
            time.sleep(0.3)
    s2 = requests.Session(); csrf = s2.post(B + "/api/login", json={"token": TOKEN}).json()["csrf"]; s2.headers["X-CSRF-Token"] = csrf
    v = s2.get(f"{B}/api/vms/{vid}").json(); check("VM adopted after app restart", v["status"] == "running" and v["agent"], json.dumps(v)[:200])
    r = s2.delete(f"{B}/api/vms/{vid}"); check("delete running VM", r.status_code == 200)
    for v in s2.get(B + "/api/vms").json():
        s2.delete(f"{B}/api/vms/{v['id']}")
    time.sleep(0.5); pg = subprocess.run(["pgrep", "-af", "[f]ake_firecracker.py --api-sock"], capture_output=True, text=True).stdout; check("no VMs left, no leaked processes", s2.get(B + "/api/vms").json() == [] and pg == "", pg)
    a = s2.get(B + "/api/audit").json(); check("audit log records actions", len(a) > 10)
    r = s2.get(B + "/api/health").json(); check("health endpoint", r["mode"] == "direct" and r["kernel"]["version"] == "6.1.99", str(r["kernel"]))
except BaseException:
    import traceback; traceback.print_exc(); failed += 1
finally:
    srv.send_signal(signal.SIGTERM)
    try:
        srv.wait(10)
    except Exception:
        srv.kill()
    subprocess.run(["pkill", "-f", "fake_firecracker"]); subprocess.run(["pkill", "-f", "mvm-guest --listen-unix"])
    print(f"\n{passed} passed, {failed} failed")
    if failed:
        print(open(f"{DATA}/server.log").read()[-3000:])
    shutil.rmtree(DATA, ignore_errors=True)
    sys.exit(1 if failed else 0)
