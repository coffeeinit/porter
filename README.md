# MicroVM Maker 3.2

A Flask web UI + API to build, run and manage **Firecracker microVMs**. Run **replica sets** (N identical microVMs), build images from OCI/Docker images,
Dockerfiles, Compose files and **your GitHub repo's own Dockerfile/compose yml**, boot them as hardware‑isolated VMs, and use everything
Firecracker offers (jailer, snapshots, balloon, memory hotplug, rate limiters, MMDS, vsock, metrics).

Built against the official Firecracker docs and API spec (checked against **Firecracker v1.18**).

```bash
./install.sh            # Ubuntu/Debian: packages, Firecracker+jailer (sha256-verified), guest agent, helper, network
./run.sh                # prints a one-time ACCESS TOKEN, then open http://127.0.0.1:8080
```
In the UI: **Images → Get kernel**, pull `nginx:alpine`, **Launch**. Then use the Terminal / Files / Snapshots tabs.

## What's new in 3.2 — UI

Dark, neutral dashboard styled after Frappe-Cloud-style consoles: workspace header, search palette (**Ctrl/⌘ K**), notifications, grouped sidebar, white primary actions, bordered panels,
filterable microVM list with status dots, VM page with resource **tiles** (CPU / memory / disk / network, thin meters) and live **area charts** (CPU, memory, network in/out, disk read/write with peaks and time axis),
an **Actions** menu, "Visit site" button, and a summary card on the Overview. Light theme via the sun icon. The guest agent now also reports real filesystem usage for the Disk tile (rebuild images to pick up the new agent).
The roadmap for traffic history, autoscaling, a fleet view, private networking and TLS ingress is in `docs/PLAN.md` (design only).

## What it does

| Area | Features |
|---|---|
| **Replica sets** (new) | N identical microVMs from one image, named `web-1…web-N`, each with `REPLICA_INDEX` / `REPLICA_SET` env · scale up/down live · start/stop/restart the whole set · **auto‑heal** (crashed or deleted replicas come back; a set you stopped stays stopped) · **rolling update** one replica at a time, waiting until each answers on the agent · optional **round‑robin TCP load balancer** on one host port that skips dead replicas (binds 127.0.0.1 unless you pick "all interfaces") |
| **Build from GitHub** (new) | Save *repo + branch + the Dockerfile or compose yml you name* — **no framework detection**; the file is the whole build definition and the result is **images only** (compose: one image per service; `build:` services are built, `image:` services pulled). Rebuild **manually**, on a **signed push webhook** (HMAC‑SHA256, only your branch, only if enabled) or by **polling** `git ls‑remote` every N minutes. Same commit is never rebuilt twice. Optionally **roll the new image out to a replica set** after a successful build. Private repos: set `MVM_GIT_TOKEN` (sent only to github.com, never stored) |
| **Images** | OCI registry image → ext4 (skopeo, layers unpacked *without executing anything*, ENV/WORKDIR/ENTRYPOINT/CMD/EXPOSE honoured) · Dockerfile · Git repo (https) · docker‑compose `image:` services · rootfs tarball upload · the official Firecracker CI Ubuntu rootfs · ext4 export |
| **Dockerfile builds** | `RUN` steps run **inside a throw‑away builder microVM**, never in a host chroot. Supported: FROM (single stage), ARG, ENV, WORKDIR, COPY/ADD (local), RUN, CMD, ENTRYPOINT, EXPOSE |
| **Lifecycle** | create / start / graceful shutdown (via agent, Ctrl‑Alt‑Del fallback) / force stop / restart / pause / resume / clone / delete / autostart. VMs survive a restart of the web app (they are adopted again) |
| **Firecracker features** | full snapshots + restore · memory **balloon** (live) · memory **hotplug** (virtio‑mem, live) · **rate limiters** for NIC RX/TX and disk bandwidth/IOPS (live) · **MMDS v2** metadata (live push) · virtio‑rng · CPU templates, SMT, huge pages (None/Transparent/2M), dirty‑page tracking · metrics + logger |
| **Guest agent** (vsock) | web terminal, file upload/download, live CPU/mem/net/disk charts, clean shutdown. **No SSH, no open ports, nothing needed in the image** (static `mvm-guest` is injected as `/mvm-init`) |
| **Network** | per‑VM TAP on bridge `mvmbr0` (172.30.0.0/24), NAT out, **VM‑to‑VM isolation**, guests blocked from your LAN and from host services, optional published ports |
| **Security** | see below |

## Security model

* **Auth**: 256‑bit access token (only its SHA‑256 is stored, shown once). Signed `HttpOnly; SameSite=Strict` session cookie, CSRF token on every state change, `Origin`/`Host` checks, login throttling, strict CSP + security headers, optional Bearer token for scripts. Binds to `127.0.0.1` by default; refuses to run as root.
* **Privilege split**: the web app runs as *you*. The only root code is `mvm-priv` (≈250 lines, root‑owned, fixed sub‑commands, validates every argument, reads settings from root‑owned `/etc/mvm/priv.conf`). It replaces the old `mvm-chroot` that ran scripts as root.
* **Isolation**: with the helper, every VM is started through the **official jailer** (chroot + pivot_root, own mount namespace, cgroup v2 cpu/memory/pids limits, unprivileged uid) on top of Firecracker's built‑in seccomp. Without it the app falls back to *direct* mode and says so in the UI.
* **Images are never executed on the host.** Layers are extracted by a small extractor that resolves every path inside the target dir (absolute symlinks treated as chroot‑relative, `..` clamped, devices skipped) under `fakeroot` so file ownership is preserved.
* **Audit log** of every state‑changing request; `MVM_READONLY=1` makes the UI view‑only; `MVM_DISABLE_EXEC=1` turns off terminal/file transfer.

## Layout

```
app.py              Flask routes + hardening          guest/mvm-guest.c   static PID-1 init + vsock agent
mvmlib/vmm.py       VM lifecycle, snapshots, balloon…  deploy/mvm-priv     the root helper
mvmlib/replicas.py  replica sets, heal, rollout        mvmlib/gitsources.py  GitHub builds, webhook, polling
mvmlib/lb.py        round-robin TCP balancer
docs/PLAN.md        roadmap: traffic, autoscale, fleet, private net, ingress+TLS
mvmlib/images.py    image pipeline                     tools/              fake Firecracker (tests only)
mvmlib/rootfs_tool.py  safe layer extractor            tests/e2e.py        55 end-to-end checks
static/ templates/  UI                                 install.sh / uninstall.sh / run.sh
```

## Configuration (environment variables)

`MVM_HOST` (127.0.0.1) · `MVM_PORT` (8080) · `MVM_DATA` (./mvm-data) · `MVM_TOKEN` (set your own, ≥16 chars) · `MVM_ALLOWED_HOSTS` (extra Host names) · `MVM_MODE` (auto|jailer|direct) · `MVM_TLS_CERT`/`MVM_TLS_KEY` · `MVM_DNS` · `MVM_MAX_VMS/VCPU/MEM_MIB` · `MVM_STOP_ON_EXIT=1` · `MVM_PRELOAD=nginx:alpine,redis:alpine` · `MVM_KERNEL_URL` / `MVM_KERNEL_SHA256` (pin the kernel) · `MVM_READONLY` · `MVM_DISABLE_EXEC` · `MVM_CGROUPS=0`.
Lost the token? `python3 app.py --reset-token`. For remote access put it behind an HTTPS reverse proxy (add its host name to `MVM_ALLOWED_HOSTS`).

## API

Everything the UI does is `/api/*` JSON (cookie+CSRF or `Authorization: Bearer <token>`):
`GET /api/health` · `POST /api/kernel` · `GET|POST /api/images…` · `GET|POST /api/vms` · `POST /api/vms/<id>/{start,stop,kill,restart,pause,resume,clone,exec,live,snapshots,forwards}` · `GET /api/vms/<id>/{console,metrics,stats,firecracker-log,files}` · `GET|POST /api/replicasets` · `POST /api/replicasets/<id>/{scale,start,stop,restart,rollout}` · `GET|POST /api/sources` · `POST /api/sources/<id>/{build,check,rotate-secret}` · `POST /hooks/github/<id>` (public, HMAC‑authenticated) · `GET /api/jobs` · `GET /api/audit`.

## What has and has not been verified

Verified in development (no KVM available there):
* guest agent protocol (exec, timeouts, file transfer, stats) with the real static binary;
* **every Firecracker API body this app sends was accepted by the real Firecracker v1.18 binary** and matches the official spec; the real jailer started Firecracker with the helper's exact flags;
* 98 end‑to‑end checks (auth/CSRF/origin/host, ownership‑correct image import, symlink‑escape defence, full lifecycle, snapshots, balloon/hotplug/rate‑limit/MMDS live updates, adoption after app restart) against a test double that speaks Firecracker's API and bridges to the real agent;
* OCI → ext4 conversion from a real OCI layout; UI pages/tabs/forms via a headless DOM run.

**Not exercised without real hardware** — an actual guest boot has since been confirmed on a real host (nginx image); still unconfirmed on real hardware: replica sets with real VMs, the load balancer against a real service, GitHub clone/build/webhook delivery from the internet, the Dockerfile builder VM, and snapshot restore. `python3 tests/e2e.py` needs none of that.

## Replicas and GitHub builds: how to use and what to expect

* **Replicas**: *Images → Replicas* (or the Replicas page). Give a name and count; set a load‑balancer port + the port your app listens on if you want one stable address. *Rolling update* recreates replicas from the **current image and template** one at a time (each replica is briefly down while it is replaced, so use ≥2 replicas to keep serving). Replicas have separate disks: they are identical *starting points*, not shared storage.
* **GitHub builds**: *GitHub builds → Add a source.* Pick Dockerfile or compose, give the path in the repo, tick *Rebuild automatically* and choose polling (works with no inbound access) and/or add the webhook shown in the **Webhook** dialog (GitHub must reach the URL: put the server behind an HTTPS reverse proxy or tunnel and add its host name to `MVM_ALLOWED_HOSTS`). A push to other branches is ignored. If a build is already queued, a new push does not start a second one; the running build clones the newest commit.
* Dockerfile/compose limits are the same as the Dockerfile tab (single stage; `RUN` runs in a builder microVM; no `ADD` from URLs). Compose networks/volumes/depends_on are ignored: the output is just images.

## Notes & limits

* Firecracker needs a guest kernel built for it: *Images → Get kernel* downloads the official CI kernel (6.1 line preferred). Balloon/hotplug/vsock need matching kernel options (the CI kernels have vsock + balloon).
* Images need `/bin/sh`. `USER` in an image is recorded, the workload runs as root inside the VM (the VM is the boundary).
* Snapshots are as large as guest RAM and must not be restored into several VMs at once (duplicate secrets/RNG state).
* CPU templates are deprecated upstream; leave at `None` unless you need a uniform fleet.
* Upgrading from v2: the data format changed; rebuild images (the old `mvm-data` is not reused, use a new folder or delete it). `./uninstall.sh` removes the helper, rules and services but keeps your data.

Firecracker is © Amazon, Apache‑2.0. This project is not affiliated with AWS.
