package buildkit

// Static-site engine (SRS §22 build subsystem): assemble a bootable
// Firecracker rootfs that serves a prebuilt static site. Detection
// (detect.go) selects EngineStatic when the context carries dist/, build/,
// or a root index.html — i.e. the publish output already exists, so there is
// nothing to compile. The engine then:
//
//  1. reuses a valid prebuilt OCI tar at the output path (EnsureOCI parity),
//  2. otherwise unpacks a runtime BASE image (the same OCI/docker-save tar
//     mechanism ConvertOCIToExt4 consumes — the backend has no registry
//     client, base artifacts are operator-staged), verifies which static
//     server that base actually ships (busybox httpd applet, nginx, caddy —
//     never assumed: Alpine's default busybox lacks the httpd applet),
//  3. copies the site into the server's docroot, and
//  4. writes a single-layer OCI layout whose image config carries the server
//     argv as Cmd, so ConvertOCIToExt4's BuildInitScript shim execs it as
//     PID 1 (mounts proc/sys/dev, exports env, exec server in foreground).
//
// Loose-directory input is carried by the normal Plan.ContextDir plumbing;
// the runtime base is the one piece the plumbing cannot infer, so it rides
// the documented build args below and fails with guidance when missing.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Build args consumed by the static engine (Plan.Args / Builder.Args).
const (
	// StaticBaseArg names the base OCI (or docker-save) tar on the build
	// host whose userland serves the site, e.g. base_oci=/srv/bases/alpine.tar
	StaticBaseArg = "base_oci"
	// StaticPortArg overrides the guest listen port (default 80).
	StaticPortArg = "static_port"
)

// staticDocroot is where the site lands unless the chosen server's
// conventional docroot already exists in the base.
const staticDocroot = "/srv"

type staticDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type staticManifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	MediaType     string             `json:"mediaType,omitempty"`
	Config        staticDescriptor   `json:"config"`
	Layers        []staticDescriptor `json:"layers"`
}

type staticIndex struct {
	SchemaVersion int                `json:"schemaVersion"`
	MediaType     string             `json:"mediaType,omitempty"`
	Manifests     []staticDescriptor `json:"manifests"`
}

type staticConfig struct {
	Architecture string         `json:"architecture"`
	OS           string         `json:"os"`
	Config       staticConfigIn `json:"config"`
}

type staticConfigIn struct {
	Env        []string `json:"Env,omitempty"`
	Cmd        []string `json:"Cmd,omitempty"`
	WorkingDir string   `json:"WorkingDir,omitempty"`
}

// staticServer is the verified serving arrangement for one base.
type staticServer struct {
	Argv    []string // PID-1 argv; rides the image config Cmd for the init shim
	Docroot string   // guest path the site files are copied into
}

// buildStatic implements the EngineStatic branch of BuildWithPlan.
func (b Builder) buildStatic(ctx context.Context, p Plan, args map[string]string) error {
	if validOCITar(p.OutputOCI) {
		return nil // prebuilt OCI tar wins: air-gapped/CI artifact, never rebuilt
	}
	publishDir, err := locatePublishDir(p.ContextDir)
	if err != nil {
		return err
	}
	baseOCI := strings.TrimSpace(args[StaticBaseArg])
	if baseOCI == "" {
		return fmt.Errorf("buildkit: static engine needs a runtime base: pass %s=<base oci/docker-save tar> (or place a prebuilt OCI tar at %s); the backend has no registry client so bases must be staged on the build host", StaticBaseArg, p.OutputOCI)
	}
	if !validOCITar(baseOCI) {
		return fmt.Errorf("buildkit: static base %s is not a readable OCI/docker-save tar (want index.json or manifest.json)", baseOCI)
	}
	port := 80
	if raw := strings.TrimSpace(args[StaticPortArg]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("buildkit: static %s must be 1-65535, got %q", StaticPortArg, raw)
		}
		port = n
	}
	root, err := os.MkdirTemp("", "porter-static-base-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := unpackOCI(baseOCI, root); err != nil {
		return fmt.Errorf("buildkit: unpack static base: %w", err)
	}
	srv, err := pickStaticServer(root, port)
	if err != nil {
		return err
	}
	if err := copyStaticSite(root, srv.Docroot, publishDir); err != nil {
		return err
	}
	return packStaticOCI(root, baseOCI, srv, p.OutputOCI)
}

// locatePublishDir mirrors detect.go's EngineStatic triggers: dist/ then
// build/ then a root index.html. Mirroring (not re-deriving) keeps Detect
// and the engine from ever disagreeing about what "static" means.
func locatePublishDir(contextDir string) (string, error) {
	for _, name := range []string{"dist", "build"} {
		if st, err := os.Stat(filepath.Join(contextDir, name)); err == nil && st.IsDir() {
			return filepath.Join(contextDir, name), nil
		}
	}
	if st, err := os.Stat(filepath.Join(contextDir, "index.html")); err == nil && !st.IsDir() {
		return contextDir, nil
	}
	return "", fmt.Errorf("buildkit: static engine found no publish dir in %s (want dist/, build/, or index.html — the same inputs detect.go keys on); or provide a prebuilt OCI tar / use the dockerfile engine", contextDir)
}

// existsInRoot reports whether rel exists under the unpacked base root.
func existsInRoot(root string, rel ...string) bool {
	_, err := os.Stat(filepath.Join(append([]string{root}, rel...)...))
	return err == nil
}

// pickStaticServer verifies, against the unpacked base filesystem, which
// static server the base actually ships. Nothing is assumed: Alpine's
// default busybox has no httpd applet (it lives in busybox-extras), so a
// busybox binary alone is NOT sufficient — the httpd entry must exist too.
func pickStaticServer(root string, port int) (staticServer, error) {
	busybox := ""
	for _, p := range []string{"bin/busybox", "usr/bin/busybox", "sbin/busybox", "usr/sbin/busybox"} {
		if existsInRoot(root, p) {
			busybox = "/" + p
			break
		}
	}
	httpdApplet := false
	for _, p := range []string{"bin/httpd", "usr/bin/httpd", "sbin/httpd", "usr/sbin/httpd"} {
		if existsInRoot(root, p) {
			httpdApplet = true
			break
		}
	}
	nginx := ""
	for _, p := range []string{"usr/sbin/nginx", "sbin/nginx", "usr/bin/nginx", "bin/nginx"} {
		if existsInRoot(root, p) {
			nginx = "/" + p
			break
		}
	}
	caddy := ""
	for _, p := range []string{"bin/caddy", "usr/bin/caddy", "usr/local/bin/caddy"} {
		if existsInRoot(root, p) {
			caddy = "/" + p
			break
		}
	}
	switch {
	case busybox != "" && httpdApplet:
		// busybox httpd: -f foreground (PID 1 must not exit), -p port, -h docroot.
		return staticServer{
			Argv:    []string{busybox, "httpd", "-f", "-p", strconv.Itoa(port), "-h", staticDocroot},
			Docroot: staticDocroot,
		}, nil
	case nginx != "":
		return staticServer{Argv: []string{nginx, "-g", "daemon off;"}, Docroot: nginxDocroot(root)}, nil
	case caddy != "":
		return staticServer{
			Argv:    []string{caddy, "file-server", "--root", staticDocroot, "--listen", ":" + strconv.Itoa(port)},
			Docroot: staticDocroot,
		}, nil
	case busybox != "":
		return staticServer{}, fmt.Errorf("buildkit: static base has busybox at %s but no httpd applet (alpine ships it in busybox-extras); use a base with busybox-extras or nginx, or a prebuilt OCI tar", busybox)
	default:
		return staticServer{}, fmt.Errorf("buildkit: static base has no verified static server (checked busybox httpd, nginx, caddy); stage a base that ships one, or provide a prebuilt OCI tar")
	}
}

// nginxDocroot prefers nginx's conventional docroot when the base ships it.
func nginxDocroot(root string) string {
	if existsInRoot(root, "usr/share/nginx/html") {
		return "/usr/share/nginx/html"
	}
	return staticDocroot
}

// copyStaticSite copies the publish dir into the guest docroot. An empty
// publish dir is an error: an image that serves nothing must not ship.
func copyStaticSite(root, docroot, publishDir string) error {
	dst := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(docroot, "/")))
	entries := 0
	err := filepath.WalkDir(publishDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(publishDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		entries++
		target := filepath.Join(dst, filepath.FromSlash(filepath.ToSlash(rel)))
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		default:
			if !d.Type().IsRegular() {
				return nil // sockets/devices never belong in a site layer
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return copyFilePerm(p, target)
		}
	})
	if err != nil {
		return fmt.Errorf("buildkit: copy static site: %w", err)
	}
	if entries == 0 {
		return fmt.Errorf("buildkit: static publish dir %s is empty; nothing to serve", publishDir)
	}
	return nil
}

func copyFilePerm(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// packStaticOCI writes the assembled root tree as a fresh single-layer OCI
// layout tar: base config env + the verified server argv as Cmd. The init
// shim that execs that Cmd is installed later by ConvertOCIToExt4
// (BuildInitScript), keeping one PID-1 convention for every engine.
func packStaticOCI(root, baseOCI string, srv staticServer, outputOCI string) error {
	// The composed config inherits the base env/workdir (PATH for nginx and
	// friends) and carries the verified server argv as Cmd. A base without a
	// readable config fails here: without one ConvertOCIToExt4 cannot install
	// the init shim, so the artifact could never boot.
	baseCfg, err := readOCIConfig(baseOCI)
	if err != nil {
		return fmt.Errorf("buildkit: static base config unreadable: %w", err)
	}
	cfgBytes, err := json.Marshal(staticConfig{
		Architecture: "x86_64",
		OS:           "linux",
		Config: staticConfigIn{
			Env:        baseCfg.Config.Env,
			Cmd:        srv.Argv,
			WorkingDir: baseCfg.Config.WorkingDir,
		},
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputOCI), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(outputOCI, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer out.Close()
	tw := tar.NewWriter(out)
	defer tw.Close()

	if _, _, err := writeTarBytes(tw, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return err
	}
	layerDigest, layerSize, err := writeStaticLayer(tw, root)
	if err != nil {
		return err
	}
	cfgDigest, cfgSize, err := writeTarBytes(tw, "blobs/sha256/"+sha256Hex(cfgBytes), cfgBytes)
	if err != nil {
		return err
	}
	man := staticManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: staticDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    "sha256:" + cfgDigest,
			Size:      cfgSize,
		},
		Layers: []staticDescriptor{{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest:    "sha256:" + layerDigest,
			Size:      layerSize,
		}},
	}
	manBytes, err := json.Marshal(man)
	if err != nil {
		return err
	}
	manDigest, manSize, err := writeTarBytes(tw, "blobs/sha256/"+sha256Hex(manBytes), manBytes)
	if err != nil {
		return err
	}
	indexBytes, err := json.Marshal(staticIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []staticDescriptor{{
			MediaType: man.MediaType,
			Digest:    "sha256:" + manDigest,
			Size:      manSize,
		}},
	})
	if err != nil {
		return err
	}
	_, _, err = writeTarBytes(tw, "index.json", indexBytes)
	return err
}

// writeStaticLayer tars the assembled root as one gzipped layer blob and
// returns its content digest (hex, no algorithm prefix) and byte size.
func writeStaticLayer(tw *tar.Writer, root string) (string, int64, error) {
	var layerBuf bytes.Buffer
	gz := gzip.NewWriter(&layerBuf)
	ltw := tar.NewWriter(gz)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			h := &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm())}
			return ltw.WriteHeader(h)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h := &tar.Header{Name: name, Typeflag: tar.TypeSymlink, Mode: int64(info.Mode().Perm()), Linkname: link}
			return ltw.WriteHeader(h)
		default:
			if !info.Mode().IsRegular() {
				return nil
			}
			h := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size()}
			if err := ltw.WriteHeader(h); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(ltw, f)
			return err
		}
	})
	if err != nil {
		return "", 0, fmt.Errorf("buildkit: pack static layer: %w", err)
	}
	if err := ltw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	data := layerBuf.Bytes()
	digest := sha256Hex(data)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "blobs/sha256/" + digest,
		Mode:     0o644,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return "", 0, err
	}
	if _, err := tw.Write(data); err != nil {
		return "", 0, err
	}
	return digest, int64(len(data)), nil
}

// writeTarBytes appends one in-memory blob entry, returning its content
// digest (hex, no algorithm prefix) and byte size.
func writeTarBytes(tw *tar.Writer, name string, data []byte) (string, int64, error) {
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return "", 0, err
	}
	if _, err := tw.Write(data); err != nil {
		return "", 0, err
	}
	return sha256Hex(data), int64(len(data)), nil
}

// sha256Hex returns the hex sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
