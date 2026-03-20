package accesspolicy

// Policy represents a Keenetic NDMS access policy ("ip policy").
type Policy struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Standalone  bool             `json:"standalone"`
	Interfaces  []PermittedIface `json:"interfaces"`
	DeviceCount int              `json:"deviceCount"`
}

// PermittedIface represents an interface in a policy (permitted or denied).
type PermittedIface struct {
	Name   string `json:"name"`
	Label  string `json:"label,omitempty"`
	Order  int    `json:"order"`
	Denied bool   `json:"denied,omitempty"` // true = "no permit" (in list but not used)
}

// Device represents a LAN device known to the router's hotspot.
type Device struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Active   bool   `json:"active"`
	Link     string `json:"link"`
	Policy   string `json:"policy"`
}

// GlobalInterface represents a router interface available for policy routing.
type GlobalInterface struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Up    bool   `json:"up"`
}
