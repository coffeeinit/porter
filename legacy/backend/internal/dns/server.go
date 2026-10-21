// Package dns provides an embedded authoritative resolver for the
// `<svc>.<project>.local` and `<svc>-<n>.<project>.local` zones. VMs register
// their IP against `<svc>.<project>.local`; lookups resolve to the replica
// pool. PTR (host -> name) lookups return the svc name for a VM IP.
//
// The Server type provides a real UDP/TCP DNS server using miekg/dns that
// answers queries for *.baseDomain zones, routing traffic to the gateway.
package dns

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/miekg/dns"

	"porter/internal/types"
)

// ServerStore is the persistence surface the authoritative DNS server needs.
type ServerStore interface {
	GetVM(id string) (*types.VM, bool)
	ListVMs() []*types.VM
	ListDomains(projectID string) []*types.Domain
	ListProjects() []*types.Project
}

// Server is an authoritative DNS server for *.baseDomain.
type Server struct {
	store      ServerStore
	baseDomain string
	gatewayIP  net.IP // IP to return for all *.baseDomain A records
	udpServer  *dns.Server
	tcpServer  *dns.Server
	mu         sync.RWMutex
	running    bool
}

// NewServer creates a DNS server that answers queries for baseDomain.
// gatewayIP is the IP address returned for all A record queries.
func NewServer(store ServerStore, baseDomain string, gatewayIP net.IP) *Server {
	s := &Server{
		store:      store,
		baseDomain: strings.ToLower(strings.TrimSuffix(baseDomain, ".")),
		gatewayIP:  gatewayIP,
	}
	return s
}

// Start begins listening on the given addr (e.g., ":53").
func (s *Server) Start(addr string) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("dns server already running")
	}
	s.mu.Unlock()

	handler := dns.HandlerFunc(s.handleDNS)

	s.udpServer = &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: handler,
	}
	s.tcpServer = &dns.Server{
		Addr:    addr,
		Net:     "tcp",
		Handler: handler,
	}

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	go func() {
		if err := s.udpServer.ListenAndServe(); err != nil {
			log.Printf("dns udp error: %v", err)
		}
	}()

	go func() {
		if err := s.tcpServer.ListenAndServe(); err != nil {
			log.Printf("dns tcp error: %v", err)
		}
	}()

	log.Printf("dns: authoritative server listening on %s for *.%s", addr, s.baseDomain)
	return nil
}

// Shutdown gracefully stops the DNS server.
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false

	if s.udpServer != nil {
		s.udpServer.Shutdown()
	}
	if s.tcpServer != nil {
		s.tcpServer.Shutdown()
	}
	log.Printf("dns: server stopped")
}

// handleDNS processes incoming DNS queries.
func (s *Server) handleDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = false

	if len(r.Question) == 0 {
		w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	name := strings.ToLower(q.Name)
	fqdn := dns.Fqdn(name)

	switch q.Qtype {
	case dns.TypeA:
		s.handleA(m, fqdn)
	case dns.TypeAAAA:
		// No IPv6 support yet — return empty
	case dns.TypePTR:
		s.handlePTR(m, fqdn)
	case dns.TypeNS:
		s.handleNS(m, fqdn)
	case dns.TypeSOA:
		s.handleSOA(m, fqdn)
	default:
		// Return empty for unsupported types
		m.Authoritative = false
		m.Rcode = dns.RcodeNameError
	}

	w.WriteMsg(m)
}

// handleA responds to A record queries.
func (s *Server) handleA(m *dns.Msg, name string) {
	// Check if this is a *.baseDomain query
	if !strings.HasSuffix(name, dns.Fqdn(s.baseDomain)) && name != dns.Fqdn(s.baseDomain) {
		m.Authoritative = false
		m.Rcode = dns.RcodeNameError
		return
	}

	// Extract the subdomain
	subdomain := strings.TrimSuffix(name, dns.Fqdn(s.baseDomain))
	subdomain = strings.TrimSuffix(subdomain, ".")

	// Check for VM-specific queries: <svc>.<project>.baseDomain
	parts := strings.SplitN(subdomain, ".", 2)
	if len(parts) == 2 {
		svc, project := parts[0], parts[1]
		s.resolveVM(m, svc, project)
		return
	}

	// Base domain or unknown subdomain — return gateway IP
	if s.gatewayIP != nil {
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: s.gatewayIP,
		})
	}
}

// resolveVM finds VMs matching a service name in a project.
func (s *Server) resolveVM(m *dns.Msg, svc, project string) {
	for _, vm := range s.store.ListVMs() {
		if vm == nil {
			continue
		}
		if !strings.EqualFold(vm.ServiceName, svc) {
			continue
		}
		// Match project by ID or name
		if project != "" && !strings.Contains(strings.ToLower(vm.ProjectID), project) &&
			!strings.Contains(strings.ToLower(vm.Name), project) {
			continue
		}
		ip := net.ParseIP(vm.IPAddress)
		if ip != nil && ip.To4() != nil {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   m.Question[0].Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    30,
				},
				A: ip.To4(),
			})
		}
	}
}

// handlePTR responds to PTR record queries.
func (s *Server) handlePTR(m *dns.Msg, name string) {
	// Extract IP from PTR name (e.g., 5.0.0.10.in-addr.arpa.)
	ip := ptrToIP(name)
	if ip == nil {
		m.Rcode = dns.RcodeNameError
		return
	}

	// Find VM with this IP
	for _, vm := range s.store.ListVMs() {
		if vm == nil {
			continue
		}
		if vm.IPAddress == ip.String() {
			ptrName := fmt.Sprintf("%s.%s.%s.", vm.ServiceName, strings.ToLower(vm.ProjectID), s.baseDomain)
			m.Answer = append(m.Answer, &dns.PTR{
				Hdr: dns.RR_Header{
					Name:   name,
					Rrtype: dns.TypePTR,
					Class:  dns.ClassINET,
					Ttl:    300,
				},
				Ptr: ptrName,
			})
			return
		}
	}
	m.Rcode = dns.RcodeNameError
}

// handleNS responds to NS record queries for the base domain.
func (s *Server) handleNS(m *dns.Msg, name string) {
	nsName := fmt.Sprintf("ns1.%s.", s.baseDomain)
	m.Answer = append(m.Answer, &dns.NS{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeNS,
			Class:  dns.ClassINET,
			Ttl:    86400,
		},
		Ns: nsName,
	})
}

// handleSOA responds to SOA record queries.
func (s *Server) handleSOA(m *dns.Msg, name string) {
	nsName := fmt.Sprintf("ns1.%s.", s.baseDomain)
	mbox := fmt.Sprintf("admin.%s.", s.baseDomain)
	m.Answer = append(m.Answer, &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Ns:      nsName,
		Mbox:    mbox,
		Serial:  1,
		Refresh: 3600,
		Retry:   600,
		Expire:  604800,
		Minttl:  300,
	})
}

// ptrToIP converts a PTR name like "5.0.0.10.in-addr.arpa." to an IP.
func ptrToIP(name string) net.IP {
	name = strings.TrimSuffix(name, ".")
	name = strings.TrimSuffix(name, ".in-addr.arpa")
	name = strings.TrimSuffix(name, ".ip6.arpa")

	parts := strings.Split(name, ".")
	if len(parts) != 4 {
		return nil
	}
	// Reverse the parts
	ip := fmt.Sprintf("%s.%s.%s.%s", parts[3], parts[2], parts[1], parts[0])
	return net.ParseIP(ip)
}
