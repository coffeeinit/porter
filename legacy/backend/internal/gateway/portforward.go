// Port-forwarding support for the gateway: a host-port forwarder that binds
// declared HostPorts on the host and proxies connections to the matching
// VM's container port.
//
// This closes the v1.0.0 gap where types.Port.HostPort was parsed but nothing
// on the host actually listened on it. Compose files that map a host port
// (e.g. "8080:80") now get a real host listener that reaches the microVM.
package gateway

import (
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"porter/internal/types"
)

// PortForwarder binds each running VM's declared HostPort on the host and
// proxies TCP connections to the VM's IP + container port. It reconciles on a
// short interval so listeners appear/disappear as VMs boot and stop.
type PortForwarder struct {
	store  Store
	logger *log.Logger
	mu     sync.Mutex
	lns    map[int]*portListener // hostPort -> active binding
	stop   chan struct{}
	done   chan struct{}
}

type forwardTarget struct {
	vmID string
	ip   string
	port int
}

type portListener struct {
	hostPort int
	target   forwardTarget
	ln       net.Listener
}

// NewPortForwarder builds a forwarder over the given store.
func NewPortForwarder(store Store) *PortForwarder {
	return &PortForwarder{
		store:  store,
		logger: log.New(log.Writer(), "portforward: ", log.LstdFlags),
		lns:    map[int]*portListener{},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start begins the reconcile loop. It runs until Close is called.
func (f *PortForwarder) Start() {
	go func() {
		defer close(f.done)
		f.reconcile()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-f.stop:
				return
			case <-ticker.C:
				f.reconcile()
			}
		}
	}()
}

// Close stops the reconcile loop and closes all bound listeners.
func (f *PortForwarder) Close() {
	close(f.stop)
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	for hp, binding := range f.lns {
		_ = binding.ln.Close()
		delete(f.lns, hp)
	}
}

// reconcile makes the set of host listeners match the set of host ports
// currently declared by running VMs. If a port's owner or destination changes,
// the old listener is closed and rebound to the new target. Ports that belong
// to a running VM but are already claimed by another VM are skipped (first
// claim wins).
func (f *PortForwarder) reconcile() {
	wanted := f.wantedPorts()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Retire listeners whose port is no longer wanted or whose target changed.
	for hp, binding := range f.lns {
		target, ok := wanted[hp]
		if !ok || binding.target != target {
			_ = binding.ln.Close()
			delete(f.lns, hp)
			f.logger.Printf("closed host :%d", hp)
		}
	}

	// Open listeners for newly wanted ports or changed targets.
	for hp, target := range wanted {
		if _, ok := f.lns[hp]; ok {
			continue
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(hp))
		if err != nil {
			f.logger.Printf("bind host :%d: %v", hp, err)
			continue
		}
		binding := &portListener{hostPort: hp, target: target, ln: ln}
		f.lns[hp] = binding
		f.logger.Printf("bound host :%d -> %s (%s:%d)", hp, target.vmID, target.ip, target.port)
		go f.acceptLoop(binding)
	}
}

// wantedPorts returns the first running VM that claims each host port, along
// with the matching declared container port. Creation time and VM ID define a
// deterministic winner when VMs claim the same port; VM ID is also part of the
// binding identity so recycling an IP cannot leave a listener attached to its old owner.
func (f *PortForwarder) wantedPorts() map[int]forwardTarget {
	out := map[int]forwardTarget{}
	vms := append([]*types.VM(nil), f.store.ListVMs()...)
	sort.Slice(vms, func(i, j int) bool {
		if vms[i] == nil {
			return false
		}
		if vms[j] == nil {
			return true
		}
		if vms[i].CreatedAt.Equal(vms[j].CreatedAt) {
			return vms[i].ID < vms[j].ID
		}
		return vms[i].CreatedAt.Before(vms[j].CreatedAt)
	})
	for _, vm := range vms {
		if vm == nil || vm.State != types.StateRunning {
			continue
		}
		for _, p := range vm.Ports {
			if p.HostPort <= 0 || p.ContainerPort <= 0 {
				continue
			}
			if _, taken := out[p.HostPort]; !taken {
				out[p.HostPort] = forwardTarget{vmID: vm.ID, ip: vm.IPAddress, port: p.ContainerPort}
			}
		}
	}
	return out
}

// acceptLoop accepts connections on one listener and proxies each to its
// target. A connection accepted concurrently with a target replacement is
// discarded if the listener has already been retired.
func (f *PortForwarder) acceptLoop(binding *portListener) {
	for {
		conn, err := binding.ln.Accept()
		if err != nil {
			return // listener closed
		}
		f.mu.Lock()
		active := f.lns[binding.hostPort] == binding
		f.mu.Unlock()
		if !active {
			_ = conn.Close()
			continue
		}
		go f.proxy(conn, binding.target.ip, binding.target.port)
	}
}

// proxy pipes a single connection to the target (dial once, stream both ways).
func (f *PortForwarder) proxy(conn net.Conn, ip string, port int) {
	defer conn.Close()
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), 10*time.Second)
	if err != nil {
		f.logger.Printf("forward %s -> %s:%d: %v", conn.RemoteAddr(), ip, port, err)
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
}
