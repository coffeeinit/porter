// Package observability contains low-level, opt-in telemetry boundaries.
package observability

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics records bounded HTTP request counters and duration summaries,
// plus a PG-backed gauge snapshot (per-VM → system usage) for Prometheus.
// It is intentionally dependency-free so the daemon can expose useful
// development metrics before an OTLP collector is configured.
//
// Cardinality rule: per-VM series are capped (maxVMSeries); the snapshot
// hook supplies at most that many VMs, so a large fleet cannot explode
// the exposition endpoint.
type Metrics struct {
	requests      atomic.Uint64
	serverError   atomic.Uint64
	durationNS    atomic.Uint64
	byMethod      [len(metricMethods)]atomic.Uint64
	byStatusClass [len(metricStatusClasses)]atomic.Uint64

	mu       sync.Mutex
	snapshot Snapshot
	hook     func() Snapshot
}

// maxVMSeries caps per-VM label cardinality on /metrics.
const maxVMSeries = 200

// VMGauge is one VM's latest readings for exposition. GPU is 0/empty until
// a GPU sampler reports gpu_percent samples (SRS future resource).
type VMGauge struct {
	ID     string
	State  string
	CPU    float64 // millicores (latest cpu sample)
	MemMiB float64 // latest memory sample
	DiskB  float64 // latest disk-bytes sample
	RxB    float64 // latest net-rx-bytes sample
	TxB    float64 // latest net-tx-bytes sample
	GPU    float64 // latest gpu-util sample (0 = none/unsupported)
}

// Snapshot is the PG-backed gauge set: per-VM readings plus fleet totals.
type Snapshot struct {
	VMs     []VMGauge
	Running int
	Total   int
	Version string
	// System totals (sums over the snapshot VMs).
	SysCPU  float64
	SysMem  float64
	SysDisk float64
	SysRx   float64
	SysTx   float64
}

// Process start info for startup gauges (set once by main).
var (
	startUnix     int64
	startupSecs   float64
	startInfoOnce sync.Mutex
)

// SetStartInfo records process start time and time-to-ready seconds. The
// /metrics exposition carries both so startup is observable and alertable.
func SetStartInfo(startUnixSec int64, readySecs float64) {
	startInfoOnce.Lock()
	defer startInfoOnce.Unlock()
	startUnix = startUnixSec
	startupSecs = readySecs
}

var metricMethods = [...]string{"GET", "POST", "PUT", "PATCH", "DELETE", "OTHER"}
var metricStatusClasses = [...]string{"1xx", "2xx", "3xx", "4xx", "5xx"}

// NewMetrics returns an empty request metrics collector.
func NewMetrics() *Metrics { return &Metrics{} }

// SetSnapshot fixes the gauge set (tests, static deployments).
func (m *Metrics) SetSnapshot(s Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot = s
}

// SetSnapshotHook refreshes gauges on every scrape (main wires the store).
func (m *Metrics) SetSnapshotHook(hook func() Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hook = hook
}

func (m *Metrics) currentSnapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hook != nil {
		func() {
			defer func() { _ = recover() }()
			m.snapshot = m.hook()
		}()
	}
	return m.snapshot
}

// Middleware records method-independent request totals and status classes.
// Request paths are deliberately not labels, preventing unbounded cardinality.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		m.requests.Add(1)
		m.durationNS.Add(uint64(time.Since(started).Nanoseconds()))
		m.byMethod[methodIndex(r.Method)].Add(1)
		m.byStatusClass[statusClassIndex(rw.status)].Add(1)
		if rw.status >= 500 {
			m.serverError.Add(1)
		}
	})
}

// Handler exposes the stable Prometheus text exposition format.
func (m *Metrics) Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	requests := m.requests.Load()
	seconds := float64(m.durationNS.Load()) / float64(time.Second)
	fmt.Fprintf(w, "# HELP porter_http_requests_total Total HTTP requests handled by Porter.\n# TYPE porter_http_requests_total counter\nporter_http_requests_total %d\n", requests)
	fmt.Fprintln(w, "# HELP porter_http_requests_by_method_total Total HTTP requests by bounded method label.")
	fmt.Fprintln(w, "# TYPE porter_http_requests_by_method_total counter")
	for i, method := range metricMethods {
		fmt.Fprintf(w, "porter_http_requests_by_method_total{method=\"%s\"} %d\n", method, m.byMethod[i].Load())
	}
	fmt.Fprintln(w, "# HELP porter_http_responses_by_status_class_total Total HTTP responses by bounded status class label.")
	fmt.Fprintln(w, "# TYPE porter_http_responses_by_status_class_total counter")
	for i, statusClass := range metricStatusClasses {
		fmt.Fprintf(w, "porter_http_responses_by_status_class_total{status_class=\"%s\"} %d\n", statusClass, m.byStatusClass[i].Load())
	}
	fmt.Fprintf(w, "# HELP porter_http_server_errors_total Total HTTP responses with status 500 or higher.\n# TYPE porter_http_server_errors_total counter\nporter_http_server_errors_total %d\n", m.serverError.Load())
	fmt.Fprintf(w, "# HELP porter_http_request_duration_seconds_sum Sum of HTTP request durations in seconds.\n# TYPE porter_http_request_duration_seconds_sum counter\nporter_http_request_duration_seconds_sum %s\n", strconv.FormatFloat(seconds, 'f', 6, 64))
	fmt.Fprintf(w, "# HELP porter_http_request_duration_seconds_count Count of HTTP request durations.\n# TYPE porter_http_request_duration_seconds_count counter\nporter_http_request_duration_seconds_count %d\n", requests)
	snap := m.currentSnapshot()
	fmt.Fprintln(w, "# HELP porter_vms_running VMs in running state (PG truth).")
	fmt.Fprintln(w, "# TYPE porter_vms_running gauge")
	fmt.Fprintf(w, "porter_vms_running %d\n", snap.Running)
	fmt.Fprintln(w, "# HELP porter_vms_total Total VMs known to the control plane.")
	fmt.Fprintln(w, "# TYPE porter_vms_total gauge")
	fmt.Fprintf(w, "porter_vms_total %d\n", snap.Total)
	fmt.Fprintln(w, "# HELP porter_vm_cpu_millicores Latest per-VM CPU reading (bounded series).")
	fmt.Fprintln(w, "# TYPE porter_vm_cpu_millicores gauge")
	fmt.Fprintln(w, "# HELP porter_vm_memory_mib Latest per-VM memory reading (bounded series).")
	fmt.Fprintln(w, "# TYPE porter_vm_memory_mib gauge")
	n := 0
	for _, vm := range snap.VMs {
		if n >= maxVMSeries || vm.ID == "" {
			continue
		}
		n++
		fmt.Fprintf(w, "porter_vm_cpu_millicores{vm_id=%q,state=%q} %s\n",
			vm.ID, vm.State, strconv.FormatFloat(vm.CPU, 'f', 2, 64))
		fmt.Fprintf(w, "porter_vm_memory_mib{vm_id=%q,state=%q} %s\n",
			vm.ID, vm.State, strconv.FormatFloat(vm.MemMiB, 'f', 2, 64))
		fmt.Fprintf(w, "porter_vm_disk_bytes{vm_id=%q,state=%q} %s\n",
			vm.ID, vm.State, strconv.FormatFloat(vm.DiskB, 'f', 0, 64))
		fmt.Fprintf(w, "porter_vm_net_rx_bytes{vm_id=%q,state=%q} %s\n",
			vm.ID, vm.State, strconv.FormatFloat(vm.RxB, 'f', 0, 64))
		fmt.Fprintf(w, "porter_vm_net_tx_bytes{vm_id=%q,state=%q} %s\n",
			vm.ID, vm.State, strconv.FormatFloat(vm.TxB, 'f', 0, 64))
		fmt.Fprintf(w, "porter_vm_gpu_util{vm_id=%q,state=%q} %s\n",
			vm.ID, vm.State, strconv.FormatFloat(vm.GPU, 'f', 2, 64))
	}
	fmt.Fprintln(w, "# HELP porter_system_cpu_millicores Fleet CPU sum over snapshotted VMs.")
	fmt.Fprintln(w, "# TYPE porter_system_cpu_millicores gauge")
	fmt.Fprintf(w, "porter_system_cpu_millicores %s\n", strconv.FormatFloat(snap.SysCPU, 'f', 2, 64))
	fmt.Fprintln(w, "# HELP porter_system_memory_mib Fleet memory sum.")
	fmt.Fprintln(w, "# TYPE porter_system_memory_mib gauge")
	fmt.Fprintf(w, "porter_system_memory_mib %s\n", strconv.FormatFloat(snap.SysMem, 'f', 2, 64))
	fmt.Fprintln(w, "# HELP porter_system_disk_bytes Fleet disk sum.")
	fmt.Fprintln(w, "# TYPE porter_system_disk_bytes gauge")
	fmt.Fprintf(w, "porter_system_disk_bytes %s\n", strconv.FormatFloat(snap.SysDisk, 'f', 0, 64))
	fmt.Fprintln(w, "# HELP porter_system_net_rx_bytes Fleet receive sum.")
	fmt.Fprintln(w, "# TYPE porter_system_net_rx_bytes gauge")
	fmt.Fprintf(w, "porter_system_net_rx_bytes %s\n", strconv.FormatFloat(snap.SysRx, 'f', 0, 64))
	fmt.Fprintln(w, "# HELP porter_system_net_tx_bytes Fleet transmit sum.")
	fmt.Fprintln(w, "# TYPE porter_system_net_tx_bytes gauge")
	fmt.Fprintf(w, "porter_system_net_tx_bytes %s\n", strconv.FormatFloat(snap.SysTx, 'f', 0, 64))
	startInfoOnce.Lock()
	su, ss := startUnix, startupSecs
	startInfoOnce.Unlock()
	fmt.Fprintln(w, "# HELP porter_process_start_time_seconds Unix time the daemon started.")
	fmt.Fprintln(w, "# TYPE porter_process_start_time_seconds gauge")
	fmt.Fprintf(w, "porter_process_start_time_seconds %d\n", su)
	fmt.Fprintln(w, "# HELP porter_startup_duration_seconds Seconds from process start to serving.")
	fmt.Fprintln(w, "# TYPE porter_startup_duration_seconds gauge")
	fmt.Fprintf(w, "porter_startup_duration_seconds %s\n", strconv.FormatFloat(ss, 'f', 3, 64))
	if snap.Version != "" {
		fmt.Fprintln(w, "# HELP porter_build_info Build version.")
		fmt.Fprintln(w, "# TYPE porter_build_info gauge")
		fmt.Fprintf(w, "porter_build_info{version=%q} 1\n", snap.Version)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func methodIndex(method string) int {
	for i := 0; i < len(metricMethods)-1; i++ {
		if metricMethods[i] == method {
			return i
		}
	}
	return len(metricMethods) - 1
}

func statusClassIndex(status int) int {
	if status < 100 || status >= 600 {
		return len(metricStatusClasses) - 1
	}
	index := status/100 - 1
	if index < 0 {
		return 0
	}
	if index >= len(metricStatusClasses) {
		return len(metricStatusClasses) - 1
	}
	return index
}
