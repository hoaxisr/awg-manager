package hydraroute

// Status represents the current state of HydraRoute Neo daemon.
type Status struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version,omitempty"`
}

// ManagedEntry represents a single DNS list to be written into HydraRoute config files.
type ManagedEntry struct {
	ListID   string
	ListName string
	Domains  []string // regular domains + geosite: tags
	Subnets  []string // CIDR ranges + geoip: tags
	Iface    string   // kernel interface name (DirectRoute target)
}

// ListInput is the input for BuildEntries — domain list data with tunnel ID to resolve.
type ListInput struct {
	ListID   string
	ListName string
	TunnelID string
	Domains  []string
	Subnets  []string
}
