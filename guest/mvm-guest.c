/*
 * mvm-guest - tiny static PID 1 + control agent for MicroVM Maker guests.
 *
 *   As PID 1 (init):  mounts pseudo filesystems, configures eth0 from the kernel
 *                     command line (mvm.ip= mvm.gw= mvm.mask= mvm.dns= mvm.host=),
 *                     starts the control agent, starts the workload
 *                     (/etc/mvm/run), reaps children, shuts the VM down cleanly.
 *   Agent mode:       listens on AF_VSOCK port 52 (Firecracker forwards the host
 *                     unix socket CONNECT 52 request to it).
 *   Test mode:        mvm-guest --listen-unix /path/to/sock   (host-side tests)
 *
 * Needs nothing from the image: no shell tools, no socat, no iproute2.
 * Build:  gcc -static -O2 -s -Wall -o mvm-guest mvm-guest.c
 *
 * Wire protocol (all integers big-endian):
 *   request : u8 op, u32 nfields, then per field: u32 len, bytes
 *   response: frames  u8 type, u32 len, bytes   (1 stdout, 2 stderr, 3 exit(i32),
 *             4 data, 5 error(text), 6 info(text)); stream ends with type 3 or 5.
 *   ops     : 1 PING  2 EXEC(cmd,workdir,timeout)  3 PUT(path,mode,data)
 *             4 GET(path)  5 STATS  6 SHUTDOWN(grace)
 */
#define _GNU_SOURCE
#include <stdarg.h>
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/vm_sockets.h>
#include <net/if.h>
#include <net/route.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mount.h>
#include <sys/reboot.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/statvfs.h>
#include <sys/types.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define VERSION "3.5"
#define AGENT_PORT 52
#define MAX_FIELD (1u << 20)
#define MAX_OUT (16u << 20)
#define DEFPATH "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

enum { OP_PING = 1, OP_EXEC, OP_PUT, OP_GET, OP_STATS, OP_SHUTDOWN };
enum { F_OUT = 1, F_ERR, F_EXIT, F_DATA, F_ERROR, F_INFO };

static int g_init;

static void logm(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static void logm(const char *fmt, ...) {
    va_list ap; char buf[512];
    int n = snprintf(buf, sizeof buf, "MVM: ");
    va_start(ap, fmt); vsnprintf(buf + n, sizeof buf - (size_t)n - 2, fmt, ap); va_end(ap);
    strcat(buf, "\n");
    (void)!write(2, buf, strlen(buf));
}

/* ---------------- io helpers ---------------- */
static int write_full(int fd, const void *b, size_t n) {
    const char *p = b;
    while (n) {
        ssize_t w = write(fd, p, n);
        if (w < 0) { if (errno == EINTR) continue; return -1; }
        p += w; n -= (size_t)w;
    }
    return 0;
}
static int read_full(int fd, void *b, size_t n) {
    char *p = b;
    while (n) {
        ssize_t r = read(fd, p, n);
        if (r < 0) { if (errno == EINTR) continue; return -1; }
        if (r == 0) return -1;
        p += r; n -= (size_t)r;
    }
    return 0;
}
static int put_frame(int fd, uint8_t type, const void *d, uint32_t n) {
    uint8_t h[5]; h[0] = type; uint32_t be = htonl(n); memcpy(h + 1, &be, 4);
    if (write_full(fd, h, 5)) return -1;
    return n ? write_full(fd, d, n) : 0;
}
static int put_exit(int fd, int code) { uint32_t be = htonl((uint32_t)code); return put_frame(fd, F_EXIT, &be, 4); }
static int put_err(int fd, const char *m) { return put_frame(fd, F_ERROR, m, (uint32_t)strlen(m)); }
static int read_u32(int fd, uint32_t *v) { uint32_t be; if (read_full(fd, &be, 4)) return -1; *v = ntohl(be); return 0; }

/* ---------------- environment ---------------- */
static void load_env(void) {
    setenv("PATH", DEFPATH, 0); setenv("HOME", "/root", 0); setenv("TERM", "xterm", 0);
    FILE *f = fopen("/etc/mvm/env", "r");
    if (!f) return;
    char line[4096];
    while (fgets(line, sizeof line, f)) {
        char *nl = strchr(line, '\n'); if (nl) *nl = 0;
        char *eq = strchr(line, '=');
        if (!eq || eq == line || line[0] == '#') continue;
        *eq = 0; setenv(line, eq + 1, 1);
    }
    fclose(f);
}

/* ---------------- agent operations ---------------- */
typedef struct { char *p; uint32_t n; } field_t;

static void op_exec(int c, field_t *f, uint32_t nf) {
    if (nf < 1) { put_err(c, "exec: missing command"); return; }
    const char *cmd = f[0].p, *wd = nf > 1 ? f[1].p : "";
    int tmo = nf > 2 ? atoi(f[2].p) : 300; if (tmo <= 0 || tmo > 86400) tmo = 300;
    int po[2], pe[2];
    if (pipe2(po, O_CLOEXEC) || pipe2(pe, O_CLOEXEC)) { put_err(c, "pipe failed"); return; }
    pid_t pid = fork();
    if (pid < 0) { put_err(c, "fork failed"); return; }
    if (pid == 0) {
        setsid();
        int nul = open("/dev/null", O_RDONLY); dup2(nul, 0); dup2(po[1], 1); dup2(pe[1], 2);
        load_env();
        if (wd[0] && chdir(wd)) { if (chdir("/")) {} }
        execl("/bin/sh", "sh", "-c", cmd, (char *)0);
        const char *m = "exec: /bin/sh not found in this image\n"; (void)!write(2, m, strlen(m));
        _exit(127);
    }
    close(po[1]); close(pe[1]);
    struct timespec t0; clock_gettime(CLOCK_MONOTONIC, &t0);
    struct pollfd p[2] = {{po[0], POLLIN, 0}, {pe[0], POLLIN, 0}};
    int open_n = 2; uint64_t sent = 0; int timedout = 0, trunc = 0; char buf[16384];
    while (open_n) {
        struct timespec t; clock_gettime(CLOCK_MONOTONIC, &t);
        long el = (t.tv_sec - t0.tv_sec) * 1000 + (t.tv_nsec - t0.tv_nsec) / 1000000;
        long left = (long)tmo * 1000 - el;
        if (left <= 0) { timedout = 1; kill(-pid, SIGKILL); break; }
        int r = poll(p, 2, left > 1000 ? 1000 : (int)left);
        if (r < 0 && errno != EINTR) break;
        for (int i = 0; i < 2; i++) {
            if (p[i].fd < 0 || !(p[i].revents & (POLLIN | POLLHUP | POLLERR))) continue;
            ssize_t n = read(p[i].fd, buf, sizeof buf);
            if (n <= 0) { close(p[i].fd); p[i].fd = -1; open_n--; continue; }
            if (sent + (uint64_t)n > MAX_OUT) { trunc = 1; continue; }
            sent += (uint64_t)n;
            if (put_frame(c, i == 0 ? F_OUT : F_ERR, buf, (uint32_t)n)) { kill(-pid, SIGKILL); waitpid(pid, NULL, 0); return; }
        }
    }
    for (int i = 0; i < 2; i++) if (p[i].fd >= 0) close(p[i].fd);
    int st = 0; waitpid(pid, &st, 0);
    int code = WIFEXITED(st) ? WEXITSTATUS(st) : 128 + WTERMSIG(st);
    if (timedout) { const char *m = "\n[mvm-agent] command timed out and was killed\n"; put_frame(c, F_ERR, m, (uint32_t)strlen(m)); code = 124; }
    if (trunc) { const char *m = "\n[mvm-agent] output truncated at 16 MiB\n"; put_frame(c, F_ERR, m, (uint32_t)strlen(m)); }
    put_exit(c, code);
}

static void mkdirs(char *path) {
    for (char *s = path + 1; *s; s++) if (*s == '/') { *s = 0; mkdir(path, 0755); *s = '/'; }
}

static void op_put(int c, field_t *f, uint32_t nf, uint32_t datalen) {
    /* the data bytes are still unread on the socket */
    char *path = NULL;
    if (nf < 3 || !f[0].p || f[0].p[0] != '/' || strlen(f[0].p) > 4000) {
        put_err(c, "put: absolute path required");
        /* drain body so the client gets the error instead of a reset */
        char b[4096]; uint32_t left = datalen; while (left) { size_t w = left < sizeof b ? left : sizeof b; if (read_full(c, b, w)) break; left -= (uint32_t)w; }
        return;
    }
    path = strdup(f[0].p); mkdirs(path);
    mode_t mode = (mode_t)strtoul(f[1].p, NULL, 8) & 07777;
    char tmp[4200]; snprintf(tmp, sizeof tmp, "%s.mvm-part", path);
    int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW, 0600);
    char buf[65536]; uint32_t left = datalen; int bad = fd < 0 ? 3 : 0;
    while (left) {
        size_t want = left < sizeof buf ? left : sizeof buf;
        if (read_full(c, buf, want)) { bad = 1; break; }
        if (fd >= 0 && write_full(fd, buf, want)) bad = 2;
        left -= (uint32_t)want;
    }
    if (fd >= 0) { fchmod(fd, mode); close(fd); }
    if (bad) {
        if (fd >= 0) unlink(tmp);
        put_err(c, bad == 3 ? "put: cannot create file" : bad == 2 ? "put: write failed (disk full?)" : "put: short read");
        free(path); return;
    }
    if (rename(tmp, path)) { unlink(tmp); put_err(c, "put: rename failed"); free(path); return; }
    free(path); put_exit(c, 0);
}

static void op_get(int c, field_t *f, uint32_t nf) {
    if (nf < 1) { put_err(c, "get: path required"); return; }
    int fd = open(f[0].p, O_RDONLY);
    struct stat st;
    if (fd < 0 || fstat(fd, &st) || !S_ISREG(st.st_mode)) {
        char e[300]; snprintf(e, sizeof e, "get: %.200s: %s", f[0].p, fd < 0 ? strerror(errno) : "not a regular file");
        put_err(c, e); if (fd >= 0) close(fd); return;
    }
    char buf[65536]; ssize_t n;
    while ((n = read(fd, buf, sizeof buf)) > 0) if (put_frame(c, F_DATA, buf, (uint32_t)n)) { close(fd); return; }
    close(fd); put_exit(c, 0);
}

static void cat_into(char **out, size_t *len, const char *title, const char *path, size_t cap) {
    char hdr[128]; int h = snprintf(hdr, sizeof hdr, "==== %s\n", title);
    *out = realloc(*out, *len + (size_t)h + cap + 2); memcpy(*out + *len, hdr, (size_t)h); *len += (size_t)h;
    int fd = open(path, O_RDONLY); if (fd < 0) return;
    ssize_t n = read(fd, *out + *len, cap); if (n > 0) *len += (size_t)n; close(fd);
    (*out)[(*len)++] = '\n';
}
static void op_stats(int c) {
    char *o = NULL; size_t l = 0;
    cat_into(&o, &l, "loadavg", "/proc/loadavg", 256); cat_into(&o, &l, "uptime", "/proc/uptime", 128);
    cat_into(&o, &l, "meminfo", "/proc/meminfo", 4096); cat_into(&o, &l, "netdev", "/proc/net/dev", 4096);
    cat_into(&o, &l, "stat", "/proc/stat", 2048); cat_into(&o, &l, "diskstats", "/proc/diskstats", 4096);
    struct statvfs sv;
    if (!statvfs("/", &sv)) {
        char b[160]; int n = snprintf(b, sizeof b, "==== statfs\n%llu %llu\n", (unsigned long long)sv.f_blocks * sv.f_frsize, (unsigned long long)sv.f_bavail * sv.f_frsize);
        o = realloc(o, l + (size_t)n + 1); memcpy(o + l, b, (size_t)n); l += (size_t)n;
    }
    put_frame(c, F_INFO, o, (uint32_t)l); put_exit(c, 0); free(o);
}

static void handle(int c) {
    uint8_t op; uint32_t nf;
    if (read_full(c, &op, 1) || read_u32(c, &nf) || nf > 16) return;
    field_t f[16]; memset(f, 0, sizeof f); uint32_t datalen = 0; int ok = 1;
    for (uint32_t i = 0; i < nf && ok; i++) {
        uint32_t n; if (read_u32(c, &n)) { ok = 0; break; }
        if (op == OP_PUT && i == 2) { datalen = n; break; }       /* stream the body */
        if (n > MAX_FIELD) { put_err(c, "field too large"); ok = 0; break; }
        f[i].p = calloc(1, (size_t)n + 1); f[i].n = n;
        if (n && read_full(c, f[i].p, n)) ok = 0;
    }
    if (ok) {
        switch (op) {
        case OP_PING: { const char *m = "pong " VERSION; put_frame(c, F_INFO, m, (uint32_t)strlen(m)); put_exit(c, 0); break; }
        case OP_EXEC: op_exec(c, f, nf); break;
        case OP_PUT: op_put(c, f, nf, datalen); break;
        case OP_GET: op_get(c, f, nf); break;
        case OP_STATS: op_stats(c); break;
        case OP_SHUTDOWN: {
            put_frame(c, F_INFO, "shutting down", 13); put_exit(c, 0);
            if (g_init) kill(1, SIGTERM); else logm("shutdown requested (not PID 1, ignored)");
            break; }
        default: put_err(c, "unknown op");
        }
    }
    for (uint32_t i = 0; i < 16; i++) free(f[i].p);
}

static void reap(int s) { (void)s; while (waitpid(-1, NULL, WNOHANG) > 0) {} }

static int agent_loop(int ls) {
    struct sigaction sa; memset(&sa, 0, sizeof sa); sa.sa_handler = reap; sa.sa_flags = SA_RESTART | SA_NOCLDSTOP;
    sigaction(SIGCHLD, &sa, NULL);
    for (;;) {
        int c = accept(ls, NULL, NULL);
        if (c < 0) { if (errno == EINTR) continue; sleep(1); continue; }
        pid_t p = fork();
        if (p == 0) { close(ls); signal(SIGCHLD, SIG_DFL); handle(c); close(c); _exit(0); }
        close(c);
    }
}

static int agent_vsock(void) {
    int s = socket(AF_VSOCK, SOCK_STREAM, 0);
    if (s < 0) { logm("agent: no AF_VSOCK in this kernel (%s)", strerror(errno)); return 1; }
    struct sockaddr_vm a; memset(&a, 0, sizeof a);
    a.svm_family = AF_VSOCK; a.svm_cid = VMADDR_CID_ANY; a.svm_port = AGENT_PORT;
    if (bind(s, (struct sockaddr *)&a, sizeof a) || listen(s, 16)) { logm("agent: bind/listen: %s", strerror(errno)); return 1; }
    logm("agent listening on vsock port %d", AGENT_PORT);
    return agent_loop(s);
}
static int agent_unix(const char *path) {
    int s = socket(AF_UNIX, SOCK_STREAM, 0); struct sockaddr_un a; memset(&a, 0, sizeof a);
    a.sun_family = AF_UNIX; strncpy(a.sun_path, path, sizeof a.sun_path - 1); unlink(path);
    if (bind(s, (struct sockaddr *)&a, sizeof a) || listen(s, 16)) { perror("bind"); return 1; }
    return agent_loop(s);
}

/* ---------------- init ---------------- */
static char cmdline[4096];
static const char *karg(const char *key) {
    static char val[256]; size_t kl = strlen(key); char *p = cmdline;
    while ((p = strstr(p, key))) {
        if ((p == cmdline || p[-1] == ' ') && p[kl] == '=') {
            p += kl + 1; size_t n = strcspn(p, " \n"); if (n >= sizeof val) n = sizeof val - 1;
            memcpy(val, p, n); val[n] = 0; return val;
        }
        p += kl;
    }
    return NULL;
}
static int karg_flag(const char *key) {   /* mvm.mmds=1 */
    const char *v = karg(key); return v && v[0] && v[0] != '0';
}
static void mnt(const char *src, const char *dst, const char *fs, unsigned long fl, const char *data) {
    mkdir(dst, 0755); if (mount(src, dst, fs, fl, data) && errno != EBUSY) logm("mount %s: %s", dst, strerror(errno));
}
static void if_up(const char *name) {
    int s = socket(AF_INET, SOCK_DGRAM, 0); struct ifreq r; memset(&r, 0, sizeof r); strncpy(r.ifr_name, name, IFNAMSIZ - 1);
    if (!ioctl(s, SIOCGIFFLAGS, &r)) { r.ifr_flags |= IFF_UP | IFF_RUNNING; ioctl(s, SIOCSIFFLAGS, &r); }
    close(s);
}
static void sin_set(struct sockaddr *sa, const char *ip) {
    struct sockaddr_in *a = (struct sockaddr_in *)sa; memset(a, 0, sizeof *a); a->sin_family = AF_INET; inet_pton(AF_INET, ip, &a->sin_addr);
}
static int if_addr(const char *name, const char *ip, int prefix) {
    int s = socket(AF_INET, SOCK_DGRAM, 0); struct ifreq r; memset(&r, 0, sizeof r); strncpy(r.ifr_name, name, IFNAMSIZ - 1);
    sin_set(&r.ifr_addr, ip); int e = ioctl(s, SIOCSIFADDR, &r);
    struct in_addr m; m.s_addr = htonl(prefix ? ~0u << (32 - prefix) : 0); char ms[32]; inet_ntop(AF_INET, &m, ms, sizeof ms);
    sin_set(&r.ifr_netmask, ms); e |= ioctl(s, SIOCSIFNETMASK, &r); close(s); return e;
}
static int route_add(const char *dst, const char *mask, const char *gw, const char *dev) {
    int s = socket(AF_INET, SOCK_DGRAM, 0); struct rtentry rt; memset(&rt, 0, sizeof rt);
    sin_set(&rt.rt_dst, dst); sin_set(&rt.rt_genmask, mask); rt.rt_flags = RTF_UP;
    if (gw) { sin_set(&rt.rt_gateway, gw); rt.rt_flags |= RTF_GATEWAY; }
    rt.rt_dev = (char *)dev; int e = ioctl(s, SIOCADDRT, &rt); close(s); return e;
}
static void write_file(const char *p, const char *s) { int fd = open(p, O_WRONLY | O_CREAT | O_TRUNC, 0644); if (fd >= 0) { (void)!write(fd, s, strlen(s)); close(fd); } }

static pid_t spawn_app(void) {
    if (access("/etc/mvm/run", R_OK)) { logm("no /etc/mvm/run - idle VM (use Exec)"); return -1; }
    pid_t p = fork();
    if (p == 0) {
        setsid(); load_env();
        execl("/bin/sh", "sh", "/etc/mvm/run", (char *)0);
        logm("cannot exec /bin/sh: %s", strerror(errno)); _exit(127);
    }
    return p;
}
static pid_t spawn_agent(void) { pid_t p = fork(); if (p == 0) _exit(agent_vsock()); return p; }

static void power_off(int grace) {
    logm("shutdown: SIGTERM to all processes (grace %ds)", grace);
    kill(-1, SIGTERM);
    for (int i = 0; i < grace * 10; i++) {
        pid_t r; int any = 0;
        while ((r = waitpid(-1, NULL, WNOHANG)) > 0) {}
        if (r == 0) any = 1;               /* r==0: children still running; r==-1: none left */
        if (!any) break;
        usleep(100000);
    }
    kill(-1, SIGKILL); sync();
    for (int i = 0; i < 4; i++) { char mp[64]; snprintf(mp, sizeof mp, "/mvm/vol/vd%c", 'b' + i); umount2(mp, MNT_DETACH); }
    sync();
    umount2("/", MNT_FORCE | MNT_DETACH); sync();
    reboot(RB_AUTOBOOT);   /* with reboot=k Firecracker exits on guest reset */
    for (;;) pause();
}


/* persistent volumes: mvm.vols=vdb:/mnt/a,vdc:/mnt/b  -> ext4 at /mvm/vol/<dev>, subdir "data" bind-mounted on the path
 * (a subdir avoids the lost+found directory that would break database images such as postgres). */
static void mount_volumes(void) {
    const char *vols = karg("mvm.vols");
    if (!vols) return;
    char v[1024]; strncpy(v, vols, sizeof v - 1); v[sizeof v - 1] = 0;
    mkdir("/mvm", 0755); mkdir("/mvm/vol", 0755);
    for (char *t = strtok(v, ","); t; t = strtok(NULL, ",")) {
        char *colon = strchr(t, ':'); if (!colon) continue;
        *colon = 0; const char *dev = t; char *path = colon + 1;
        if (strchr(dev, '/') || !*dev || path[0] != '/') { logm("bad volume spec %s", dev); continue; }
        char node[96], mp[96], sub[160];
        snprintf(node, sizeof node, "/dev/%s", dev); snprintf(mp, sizeof mp, "/mvm/vol/%s", dev);
        for (int i = 0; i < 50 && access(node, F_OK); i++) usleep(100000);
        mkdir(mp, 0755);
        if (mount(node, mp, "ext4", MS_NOATIME, NULL)) { logm("volume %s: mount failed: %s", dev, strerror(errno)); continue; }
        snprintf(sub, sizeof sub, "%s/data", mp); mkdir(sub, 0755);
        mkdirs(path); mkdir(path, 0755);
        if (mount(sub, path, NULL, MS_BIND, NULL)) logm("volume %s: bind to %s failed: %s", dev, path, strerror(errno));
        else logm("volume %s mounted at %s", dev, path);
    }
}

static int init_main(void) {
    g_init = 1; setenv("PATH", DEFPATH, 1);

    mnt("proc", "/proc", "proc", MS_NOSUID | MS_NODEV | MS_NOEXEC, NULL);
    mnt("sysfs", "/sys", "sysfs", MS_NOSUID | MS_NODEV | MS_NOEXEC, NULL);
    mnt("devtmpfs", "/dev", "devtmpfs", MS_NOSUID, "mode=0755");

    /* Standard Linux file descriptor paths */
    if (symlink("/proc/self/fd", "/dev/fd") && errno != EEXIST)
        logm("create /dev/fd: %s", strerror(errno));

    if (symlink("/proc/self/fd/0", "/dev/stdin") && errno != EEXIST)
        logm("create /dev/stdin: %s", strerror(errno));

    if (symlink("/proc/self/fd/1", "/dev/stdout") && errno != EEXIST)
        logm("create /dev/stdout: %s", strerror(errno));

    if (symlink("/proc/self/fd/2", "/dev/stderr") && errno != EEXIST)
        logm("create /dev/stderr: %s", strerror(errno));

    mkdir("/dev/pts", 0755);
    mkdir("/dev/shm", 01777);

    mnt("devpts", "/dev/pts", "devpts", MS_NOSUID | MS_NOEXEC, "mode=0620,ptmxmode=0666");
    mnt("tmpfs", "/dev/shm", "tmpfs", MS_NOSUID | MS_NODEV, "mode=1777");
    mnt("tmpfs", "/run", "tmpfs", MS_NOSUID | MS_NODEV, "mode=0755");
    mnt("tmpfs", "/tmp", "tmpfs", MS_NOSUID | MS_NODEV, "mode=1777");
    mnt("cgroup2", "/sys/fs/cgroup", "cgroup2", MS_NOSUID | MS_NODEV | MS_NOEXEC, NULL);

    reboot(RB_DISABLE_CAD);   /* Ctrl+Alt+Del -> SIGINT to us instead of instant reboot */
    int fd = open("/proc/cmdline", O_RDONLY);
    if (fd >= 0) { ssize_t n = read(fd, cmdline, sizeof cmdline - 1); if (n > 0) cmdline[n] = 0; close(fd); }
    logm("mvm-guest %s starting", VERSION);
    const char *h = karg("mvm.host"); if (h) (void)!sethostname(h, strlen(h));
    if_up("lo");
    const char *ip = karg("mvm.ip"); char ipb[64] = "", gwb[64] = "";
    if (ip) {
        strncpy(ipb, ip, sizeof ipb - 1);
        const char *gw = karg("mvm.gw"); if (gw) strncpy(gwb, gw, sizeof gwb - 1);
        const char *mk = karg("mvm.mask"); int prefix = mk ? atoi(mk) : 24; if (prefix < 1 || prefix > 32) prefix = 24;
        for (int i = 0; i < 50 && access("/sys/class/net/eth0", F_OK); i++) usleep(100000);
        if_up("eth0");
        if (if_addr("eth0", ipb, prefix)) logm("eth0: set address failed: %s", strerror(errno));
        if (gwb[0] && route_add("0.0.0.0", "0.0.0.0", gwb, "eth0")) logm("default route failed: %s", strerror(errno));
        if (karg_flag("mvm.mmds")) route_add("169.254.169.254", "255.255.255.255", NULL, "eth0");
        logm("eth0 = %s/%d gw %s", ipb, prefix, gwb[0] ? gwb : "-");
        const char *dns = karg("mvm.dns");
        if (dns) {
            char r[512] = ""; char d[256]; strncpy(d, dns, sizeof d - 1); d[sizeof d - 1] = 0;
            for (char *t = strtok(d, ","); t; t = strtok(NULL, ",")) {
                strncat(r, "nameserver ", sizeof r - strlen(r) - 1); strncat(r, t, sizeof r - strlen(r) - 1); strncat(r, "\n", sizeof r - strlen(r) - 1);
            }
            if (r[0]) write_file("/etc/resolv.conf", r);
        }
    }
    mount_volumes();
    sigset_t set; sigemptyset(&set);
    sigaddset(&set, SIGCHLD); sigaddset(&set, SIGTERM); sigaddset(&set, SIGINT); sigaddset(&set, SIGUSR1);
    sigprocmask(SIG_BLOCK, &set, NULL);
    pid_t agent = spawn_agent(), app = spawn_app();
    const char *gs = karg("mvm.grace"); int grace = gs ? atoi(gs) : 8; if (grace < 1 || grace > 120) grace = 8;
    logm("ready");
    for (;;) {
        siginfo_t si; struct timespec ts = {2, 0}; int s = sigtimedwait(&set, &si, &ts);
        if (s == SIGTERM || s == SIGINT) power_off(grace);
        pid_t r;
        while ((r = waitpid(-1, NULL, WNOHANG)) > 0) {
            if (r == app) { logm("workload exited - VM stays up for debugging"); app = -1; }
            if (r == agent) { logm("agent exited - restarting"); sleep(1); agent = spawn_agent(); }
        }
    }
    return 0; /* not reached */
}

int main(int argc, char **argv) {
    if (getpid() == 1) return init_main();
    if (argc == 3 && !strcmp(argv[1], "--listen-unix")) return agent_unix(argv[2]);
    if (argc == 2 && !strcmp(argv[1], "--agent")) return agent_vsock();
    if (argc == 2 && !strcmp(argv[1], "--version")) { puts("mvm-guest " VERSION); return 0; }
    fprintf(stderr, "usage: mvm-guest [--agent | --listen-unix PATH | --version]  (PID 1: init)\n");
    return 2;
}
