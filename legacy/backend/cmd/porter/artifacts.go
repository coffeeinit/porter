// Artifact provisioning for direct-Firecracker microVMs: `porter kernel set`
// and `porter image add`.
//
// A microVM needs exactly two host files — a vmlinux kernel and an ext4 rootfs.
// These commands prepare and validate them, and register the pair in the
// on-disk image catalog that the control plane resolves deployable images from
// (api.applyImageManifest reads Catalog.All()). No database is touched: the
// catalog is the source of truth for image resolution, and the server reloads
// it on /images.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"porter/internal/config"
	"porter/internal/imagecatalog"
)

// minKernelBytes guards against a truncated download or an HTML error page
// saved as "vmlinux". No real Linux kernel image is under a megabyte.
const minKernelBytes = 1 << 20

// kernelMagic lists the leading bytes of the container/executable formats a
// bootable kernel image can legitimately arrive in.
var kernelMagic = []struct {
	name string
	head []byte
}{
	{"ELF", []byte{0x7f, 'E', 'L', 'F'}},
	{"gzip", []byte{0x1f, 0x8b}},
	{"bzip2", []byte{'B', 'Z', 'h'}},
	{"xz", []byte{0xfd, '7', 'z', 'X'}},
	{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd}},
	{"bzImage/MZ", []byte{'M', 'Z'}},
}

// runKernel implements `porter kernel set <url|path>`.
func runKernel(args []string) int {
	if len(args) < 2 || args[0] != "set" {
		fmt.Fprintln(os.Stderr, "usage: porter kernel set <url|path>")
		return 2
	}
	cfg, err := config.LoadConfig(getenv("PORTER_CONFIG", "porter.toml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}
	dest := cfg.KernelImage
	if dest == "" {
		fmt.Fprintln(os.Stderr, "no kernel destination configured (set firecracker.kernel_image or PORTER_KERNEL_IMAGE)")
		return 1
	}

	if err := fetchTo(args[1], dest); err != nil {
		fmt.Fprintf(os.Stderr, "kernel set: %v\n", err)
		return 1
	}
	info, err := os.Stat(dest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kernel set: %v\n", err)
		return 1
	}
	if info.Size() < minKernelBytes {
		fmt.Fprintf(os.Stderr, "kernel set: %s is only %d bytes; refusing to install it as a kernel\n", dest, info.Size())
		return 1
	}
	if err := checkKernelMagic(dest); err != nil {
		fmt.Fprintf(os.Stderr, "kernel set: %v\n", err)
		return 1
	}
	fmt.Printf("kernel installed: %s (%d bytes)\n", dest, info.Size())
	return 0
}

// checkKernelMagic verifies the file actually looks like a kernel image, so a
// redirect to an HTML error page can never be registered as bootable.
func checkKernelMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("read %s: %w", path, err)
	}
	head = head[:n]
	for _, m := range kernelMagic {
		if len(head) >= len(m.head) && string(head[:len(m.head)]) == string(m.head) {
			return nil
		}
	}
	return fmt.Errorf("%s does not look like a kernel image (leading bytes % x); expected ELF, gzip, bzip2, xz, zstd or a bzImage", path, head)
}

// fetchTo copies src (a local path or an http(s) URL) to dest atomically, so a
// failed transfer never leaves a half-written kernel in place.
func fetchTo(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	defer os.Remove(tmp)

	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		resp, err := http.Get(src) //nolint:gosec // operator-supplied URL
		if err != nil {
			out.Close()
			return fmt.Errorf("download %s: %w", src, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			out.Close()
			return fmt.Errorf("download %s: %s", src, resp.Status)
		}
		if _, err := io.Copy(out, resp.Body); err != nil {
			out.Close()
			return fmt.Errorf("download %s: %w", src, err)
		}
	} else {
		in, err := os.Open(src)
		if err != nil {
			out.Close()
			return err
		}
		defer in.Close()
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return fmt.Errorf("copy %s: %w", src, err)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// runImage implements `porter image add <name> <rootfs.ext4> <vmlinux>`.
func runImage(args []string) int {
	if len(args) < 4 || args[0] != "add" {
		fmt.Fprintln(os.Stderr, "usage: porter image add <name> <rootfs.ext4> <vmlinux>")
		return 2
	}
	name, rootfs, kernel := args[1], args[2], args[3]
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		fmt.Fprintln(os.Stderr, "image add: name must be a plain filename-safe identifier")
		return 2
	}

	cfg, err := config.LoadConfig(getenv("PORTER_CONFIG", "porter.toml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}

	// Validate before registering: a manifest pointing at a missing or
	// truncated artifact would produce a microVM that fails at boot.
	report, err := imagecatalog.ValidateArtifacts(rootfs, kernel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "image add: artifacts are not deployable: %v\n", err)
		if report.Error != "" {
			fmt.Fprintf(os.Stderr, "  %s\n", report.Error)
		}
		return 1
	}

	manifest := imagecatalog.ManifestFromArtifacts(
		name, "custom://"+name, "Registered by porter image add", "custom",
		rootfs, kernel, 1, 256,
	)

	dir := cfg.ImagesDir
	if dir == "" {
		fmt.Fprintln(os.Stderr, "no image catalog configured (firecracker.images_dir)")
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "image add: %v\n", err)
		return 1
	}
	blob, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "image add: %v\n", err)
		return 1
	}
	dest := filepath.Join(dir, name+".json")
	if err := os.WriteFile(dest, append(blob, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "image add: %v\n", err)
		return 1
	}

	fmt.Printf("image registered: %s\n", name)
	fmt.Printf("  catalog:   %s\n", dest)
	fmt.Printf("  reference: %s\n", manifest.Image)
	fmt.Printf("  rootfs:    %s\n", manifest.Rootfs)
	fmt.Printf("  kernel:    %s\n", manifest.Kernel)
	fmt.Printf("  status:    %s (%s, %d vCPU / %d MiB)\n",
		manifest.Status, manifest.Architecture, manifest.VCPUs, manifest.MemMiB)
	return 0
}
