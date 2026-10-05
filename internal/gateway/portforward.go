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
	lns    map[int]net.Listener // hostPort -> active listener
	stop   chan struct{}
	done   chan struct{}
}

// NewPortForwarder builds a forwarder over the given store.
func NewPortForwarder(store Store) *PortForwarder {
	return &PortForwarder{
		store:  store,
		logger: log.New(log.Writer(), "portforward: ", log.LstdFlags),
		lns:    map[int]net.Listener{},
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
	for hp, ln := range f.lns {
		_ = ln.Close()
		delete(f.lns, hp)
	}
}

// reconcile makes the set of host listeners match the set of host ports
// currently declared by running VMs. Ports that belong to a running VM but are
// already bound by another VM are skipped with a log line (first claim wins).
func (f *PortForwarder) reconcile() {
	wanted := f.wantedPorts()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Close listeners whose port is no longer wanted.
	for hp, ln := range f.lns {
		if _, ok := wanted[hp]; !ok {
			_ = ln.Close()
			delete(f.lns, hp)
			f.logger.Printf("closed host :%d", hp)
		}
	}

	// Open listeners for newly wanted ports.
	for hp, vm := range wanted {
		if _, ok := f.lns[hp]; ok {
			continue
		}
		cp := vm.ContainerPort()
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(hp))
		if err != nil {
			f.logger.Printf("bind host :%d: %v", hp, err)
			continue
		}
		f.lns[hp] = ln
		f.logger.Printf("bound host :%d -> %s:%d", hp, vm.IPAddress, cp)
		go f.acceptLoop(ln, vm.IPAddress, cp)
	}
}

// wantedPorts returns the set of HostPorts currently claimed by running VMs,
// mapped to the first VM that claims each port.
func (f *PortForwarder) wantedPorts() map[int]*types.VM {
	out := map[int]*types.VM{}
	for _, vm := range f.store.ListVMs() {
		if vm.State != types.StateRunning {
			continue
		}
		for _, p := range vm.Ports {
			if p.HostPort == 0 {
				continue
			}
			if _, taken := out[p.HostPort]; !taken {
				out[p.HostPort] = vm
			}
		}
	}
	return out
}

// acceptLoop accepts connections on a host listener and proxies each to the
// target VM's container port.
func (f *PortForwarder) acceptLoop(ln net.Listener, ip string, port int) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go f.proxy(conn, ip, port)
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
