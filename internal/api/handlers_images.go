// Images, registries, guest bases, hypervisor image imports/uploads.
package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"porter/internal/buildkit"
	"porter/internal/imagecatalog"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Images / registry / vCenter
// ============================================================================

func goldenImageManifest(gi types.GoldenImage) types.ImageManifest {
	return types.ImageManifest{ID: gi.ID, Name: gi.Name, Type: gi.Kind, Description: gi.Description, Image: gi.Image, Rootfs: gi.Rootfs, Kernel: gi.Kernel, Architecture: gi.Architecture, RootfsSHA256: gi.RootfsSHA256, KernelSHA256: gi.KernelSHA256, Status: gi.Status, VCPUs: gi.VCPUs, MemMiB: gi.MemMiB, Ports: gi.Ports, Env: gi.Env, Tags: gi.Tags, Logo: gi.Logo}
}

func (a *API) imageManifests(ctx context.Context) []types.ImageManifest {
	seen := map[string]bool{}
	out := []types.ImageManifest{}
	if a.catalog != nil {
		if reloadable, ok := a.catalog.(interface{ Reload() }); ok {
			reloadable.Reload()
		}
		for _, im := range a.catalog.All() {
			key := im.Image
			if key == "" {
				key = im.ID
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, im)
			}
		}
	}
	if a.store != nil {
		for _, gi := range a.store.ListGoldenImages() {
			if gi == nil {
				continue
			}
			im := goldenImageManifest(*gi)
			key := im.Image
			if key == "" {
				key = im.ID
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, im)
			}
		}
	}
	// Entries from the upstream catalog are listed even though their artifacts
	// are not on this host yet: the operator sees what is deployable, and pulls
	// the one they want. A remote entry is marked "remote" so the UI can tell
	// it apart from a locally bootable image.
	if a.remoteCat.Enabled() {
		for _, im := range a.remoteCat.Entries(ctx) {
			key := im.Image
			if key == "" {
				key = im.ID
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, err := os.Stat(im.Rootfs); err != nil {
				im.Status = "remote"
				im.Rootfs = ""
				im.Kernel = ""
			}
			out = append(out, im)
		}
	}
	return out
}

func (a *API) handleListImages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.imageManifests(r.Context()))
}

// handlePullImage downloads a catalog image into the local cache so it can be
// booted. Pulling is explicit: Porter keeps no guest images in the repository,
// so this is the step that materialises one, verified against the digest the
// catalog pinned.
func (a *API) handlePullImage(w http.ResponseWriter, r *http.Request) {
	if !a.remoteCat.Enabled() {
		writeError(w, http.StatusNotImplemented, "no image catalog is configured; set PORTER_CATALOG_URL to enable remote images")
		return
	}
	ref := r.PathValue("reference")
	if ref == "" {
		writeError(w, http.StatusBadRequest, "image reference is required")
		return
	}

	// A refresh here means a click in the UI is enough to pick up a catalog
	// the operator just published upstream.
	_ = a.remoteCat.Refresh(r.Context())

	entry, ok := a.remoteCat.Lookup(r.Context(), ref)
	if !ok {
		writeError(w, http.StatusNotFound, "no catalog image matches "+ref)
		return
	}
	dir := a.imagesDir()
	if dir == "" {
		writeError(w, http.StatusServiceUnavailable, "no image directory is configured (firecracker.images_dir)")
		return
	}

	local, err := imagecatalog.Pull(r.Context(), nil, entry, dir)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pull failed: "+err.Error())
		return
	}
	// Reload so the newly written manifest is visible to boot paths at once.
	if reloadable, ok := a.catalog.(interface{ Reload() }); ok {
		reloadable.Reload()
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "image": local})
}

// imagesDir is where pulled and registered images live.
func (a *API) imagesDir() string {
	if a.hostConfig != nil && a.hostConfig.ImagesDir != "" {
		return a.hostConfig.ImagesDir
	}
	return ""
}

func (a *API) handleBaseImage(w http.ResponseWriter, r *http.Request) {
	ref := "base://default"
	if a.hostConfig != nil && a.hostConfig.BaseImageRef != "" {
		ref = a.hostConfig.BaseImageRef
	}
	for _, gi := range a.store.ListGoldenImages() {
		if gi.Image == ref || gi.Name == strings.TrimPrefix(ref, "base://") {
			writeJSON(w, http.StatusOK, gi)
			return
		}
	}
	writeError(w, http.StatusNotFound, "configured base image is not registered")
}

func (a *API) handleBaseImageReadiness(w http.ResponseWriter, r *http.Request) {
	ref := "base://default"
	if a.hostConfig != nil && a.hostConfig.BaseImageRef != "" {
		ref = a.hostConfig.BaseImageRef
	}
	for _, gi := range a.store.ListGoldenImages() {
		if gi.Image == ref || gi.Name == strings.TrimPrefix(ref, "base://") {
			report, err := imagecatalog.ValidateArtifacts(gi.Rootfs, gi.Kernel)
			writeJSON(w, http.StatusOK, map[string]any{"reference": ref, "image": gi, "ready": err == nil, "artifacts": report})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reference": ref, "ready": false, "reason": "configured base image is not registered"})
}

func (a *API) handleImageSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	out := []types.ImageManifest{}
	for _, im := range a.imageManifests(r.Context()) {
		if q == "" || strings.Contains(strings.ToLower(im.Name), q) || strings.Contains(strings.ToLower(im.Image), q) || strings.Contains(strings.ToLower(im.Description), q) {
			out = append(out, im)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetImage(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("reference")
	for _, im := range a.imageManifests(r.Context()) {
		if im.Name == ref || im.Image == ref || im.ID == ref {
			writeJSON(w, http.StatusOK, im)
			return
		}
	}
	writeError(w, http.StatusNotFound, "image not found")
}

func (a *API) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteGoldenImage(r.PathValue("reference")); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed from catalog"})
}

func (a *API) handlePruneImages(w http.ResponseWriter, r *http.Request) {
	inUse := map[string]bool{}
	for _, p := range a.store.ListProjects() {
		for _, vid := range p.VMIDs {
			if vm, ok := a.store.GetVM(vid); ok {
				inUse[vm.Image] = true
				for _, gi := range a.store.ListGoldenImages() {
					if gi.Image == vm.Image {
						inUse[gi.ID] = true
					}
				}
			}
		}
	}
	removed := 0
	for _, gi := range a.store.ListGoldenImages() {
		if inUse[gi.ID] {
			continue
		}
		if err := a.store.DeleteGoldenImage(gi.ID); err == nil {
			removed++
		}
	}
	a.store.AppendDaemonLog(fmt.Sprintf("image prune: removed %d unused golden image(s)", removed))
	writeJSON(w, http.StatusOK, map[string]any{"status": "pruned", "removed": removed})
}

func (a *API) handleImageStats(w http.ResponseWriter, r *http.Request) {
	images := a.store.ListGoldenImages()
	var bytes int64
	for _, gi := range images {
		if gi.Rootfs != "" {
			if fi, err := os.Stat(gi.Rootfs); err == nil {
				bytes += fi.Size()
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": len(images), "bytes": bytes})
}

func (a *API) handleImageLineage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /images/{reference}/lineage")
}

func (a *API) handleSignImage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /images/{reference}/sign")
}

func (a *API) handleImageSBOM(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /images/{reference}/sbom")
}

func (a *API) handleImageProvenance(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /images/{reference}/provenance")
}

func (a *API) handleScanImage(w http.ResponseWriter, r *http.Request) {
	opID, err := a.store.CreateOperationWithPayload("image-scan", "image", r.PathValue("reference"),
		"scan|"+r.PathValue("reference"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

// vCenter / OpenNebula / Proxmox / VMware imports

func (a *API) handleUploadVMDK(w http.ResponseWriter, r *http.Request) {
	a.handleUploadRawVMDisk(w, r, "vmdk")
}

func (a *API) handleUploadQCOW2(w http.ResponseWriter, r *http.Request) {
	a.handleUploadRawVMDisk(w, r, "qcow2")
}

func (a *API) handleUploadOVA(w http.ResponseWriter, r *http.Request) {
	a.handleUploadRawVMDisk(w, r, "ova")
}

func (a *API) handleUploadRaw(w http.ResponseWriter, r *http.Request) {
	a.handleUploadRawVMDisk(w, r, "raw")
}

func (a *API) handleUploadRawVMDisk(w http.ResponseWriter, r *http.Request, kind string) {
	if a.customImagesDir == "" {
		writeError(w, http.StatusBadRequest, "custom images dir not configured")
		return
	}
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	name := filepath.Base(strings.TrimSpace(r.FormValue("name")))
	if name == "" || name == "." || name == "/" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer f.Close()
	dest := filepath.Join(a.customImagesDir, name)
	_ = os.MkdirAll(dest, 0o755)
	saved := filepath.Join(dest, "source."+kind)
	out, err := os.Create(saved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Close()
	opID, err := a.store.CreateOperationWithPayload("image-convert", "image", name,
		"convert|"+kind+"|"+name, map[string]string{"source": saved, "kind": kind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED", "kind": kind, "source": saved})
}

func (a *API) handleImportFromVCenter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string `json:"host"`
		VM       string `json:"vm"`
		Name     string `json:"name"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil || req.Host == "" || req.VM == "" {
		writeError(w, http.StatusBadRequest, "host and vm are required")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("vcenter-import", "image", req.Name,
		"vcenter|"+req.Host+"|"+req.VM, map[string]string{"host": req.Host, "vm": req.VM, "name": req.Name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED", "source": "vcenter"})
}

func (a *API) handleImportFromOpenNebula(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		ImageID  string `json:"image_id"`
		Name     string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil || req.Endpoint == "" || req.ImageID == "" {
		writeError(w, http.StatusBadRequest, "endpoint and image_id are required")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("opennebula-import", "image", req.Name,
		"opennebula|"+req.Endpoint+"|"+req.ImageID, map[string]string{"endpoint": req.Endpoint, "image_id": req.ImageID, "name": req.Name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED", "source": "opennebula"})
}

func (a *API) handleImportFromProxmox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		VMID     string `json:"vm_id"`
		Name     string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil || req.Endpoint == "" || req.VMID == "" {
		writeError(w, http.StatusBadRequest, "endpoint and vm_id are required")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("proxmox-import", "image", req.Name,
		"proxmox|"+req.Endpoint+"|"+req.VMID, map[string]string{"endpoint": req.Endpoint, "vm_id": req.VMID, "name": req.Name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED", "source": "proxmox"})
}

// Registry

func (a *API) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /registries")
}

func (a *API) handleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /registries")
}

func (a *API) handleGetRegistry(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /registries/{id}")
}

func (a *API) handlePatchRegistry(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /registries/{id}")
}

func (a *API) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /registries/{id}")
}

func (a *API) handleValidateRegistry(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /registries/{id}/validate")
}

// recovered from internal/api/handlers.go
func (a *API) handleListGuestBases(w http.ResponseWriter, r *http.Request) {
	bases := imagecatalog.ManagedGuestBases()
	for i := range bases {
		bases[i].KernelPath = "configured-by-host"
		bases[i].RootfsPath = "configured-by-host"
		bases[i].AgentPath = "configured-by-host"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default": "alpine",
		"bases":   bases,
		"custom":  map[string]any{"enabled": true, "requires": []string{"kernel_path", "rootfs_path", "guest_agent"}},
	})
}

// recovered from internal/api/handlers.go
func (a *API) handleUploadCustomImage(w http.ResponseWriter, r *http.Request) {
	if a.customImagesDir == "" {
		writeError(w, http.StatusBadRequest, "custom images dir not configured")
		return
	}
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	name := filepath.Base(strings.TrimSpace(r.FormValue("name")))
	if name == "" || name == "." || name == "/" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	vcpus, _ := strconv.Atoi(r.FormValue("vcpus"))
	memMiB, _ := strconv.Atoi(r.FormValue("mem_mib"))
	if vcpus <= 0 {
		vcpus = 1
	}
	if memMiB <= 0 {
		memMiB = 256
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file (zip) is required")
		return
	}
	defer file.Close()

	dest := filepath.Join(a.customImagesDir, name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "mkdir: "+err.Error())
		return
	}
	if err := unzipTo(file, dest); err != nil {
		writeError(w, http.StatusBadRequest, "unzip: "+err.Error())
		return
	}
	rootfs := filepath.Join(dest, "rootfs.ext4")
	kernel := filepath.Join(dest, "vmlinux")
	report, err := imagecatalog.ValidateArtifacts(rootfs, kernel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid microVM artifacts: "+report.Error)
		return
	}

	gi := &types.GoldenImage{
		ID:           store.NewID(),
		Name:         name,
		Image:        "custom://" + name,
		Kind:         "custom",
		Rootfs:       rootfs,
		Kernel:       kernel,
		Architecture: report.Architecture,
		RootfsSHA256: report.RootfsSHA256,
		KernelSHA256: report.KernelSHA256,
		Status:       report.Status,
		VCPUs:        vcpus,
		MemMiB:       memMiB,
		CreatedAt:    time.Now(), ValidatedAt: func() *time.Time { now := time.Now(); return &now }(),
	}
	if err := a.store.PutGoldenImage(gi); err != nil {
		writeError(w, http.StatusInternalServerError, "save image: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, gi)
}

// recovered from internal/api/feature_oci.go
func (a *API) handleUploadCustomOCIImage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /images/custom/oci")
}

// from feature_oci.go
func entrypointSummary(res buildkit.Result) string {
	argv := append(append([]string{}, res.Entrypoint...), res.Cmd...)
	if len(argv) == 0 {
		return "no entrypoint"
	}
	if len(argv) > 3 {
		argv = append(argv[:3], "...")
	}
	return strings.Join(argv, " ")
}
