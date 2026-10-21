// Package resource defines the canonical Porter domain types.
package resource

// Network is a project-level network abstraction.
type Network struct {
	Metadata `json:"metadata"`
	Spec     NetworkSpec   `json:"spec"`
	Status   NetworkStatus `json:"status"`
}

func (n *Network) GetMetadata() *Metadata { return &n.Metadata }
func (n *Network) GetSpec() interface{}   { return n.Spec }
func (n *Network) GetStatus() *Status     { return &n.Status.Status }
func (n *Network) SetStatus(s *Status)    { n.Status.Status = *s }
func (n *Network) GetKind() string        { return KindNetwork }

// NetworkSpec is the desired state of a network
type NetworkSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id,omitempty"`

	// CIDR is the network CIDR
	CIDR string `json:"cidr"`

	// Driver is the network driver (bridge, overlay, host, none)
	Driver string `json:"driver,omitempty"`

	// Internal indicates no external connectivity
	Internal bool `json:"internal,omitempty"`

	// IPAM configuration
	IPAM *IPAMConfig `json:"ipam,omitempty"`

	// DNS enabled
	DNSEnabled bool `json:"dns_enabled,omitempty"`

	// MTU
	MTU int `json:"mtu,omitempty"`

	// Options driver-specific options
	Options map[string]string `json:"options,omitempty"`
}

// IPAMConfig defines IP address management
type IPAMConfig struct {
	Driver  string            `json:"driver,omitempty"`
	Options map[string]string `json:"options,omitempty"`
	Config  []IPAMPool        `json:"config,omitempty"`
}

// IPAMPool defines an IP pool
type IPAMPool struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway,omitempty"`
	Range   string `json:"range,omitempty"`
}

// NetworkStatus is the observed state of a network
type NetworkStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"`

	// BridgeName is the host bridge name
	BridgeName string `json:"bridge_name,omitempty"`

	// Subnets is the list of allocated subnets
	Subnets []string `json:"subnets,omitempty"`

	// Endpoints is the count of connected endpoints
	Endpoints int `json:"endpoints"`
}
