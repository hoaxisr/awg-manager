package accesspolicy

import "context"

// Service defines operations on Keenetic NDMS access policies.
type Service interface {
	// List returns all access policies with their permitted interfaces and device counts.
	List(ctx context.Context) ([]Policy, error)

	// Create creates a new policy with the given description.
	// Automatically finds the first free PolicyN index.
	Create(ctx context.Context, description string) (*Policy, error)

	// Delete removes a policy by name (e.g. "Policy0").
	Delete(ctx context.Context, name string) error

	// SetDescription updates the description of a policy.
	SetDescription(ctx context.Context, name, description string) error

	// SetStandalone enables or disables standalone mode on a policy.
	SetStandalone(ctx context.Context, name string, enabled bool) error

	// PermitInterface adds an interface to a policy's permitted list.
	PermitInterface(ctx context.Context, name, iface string, order int) error

	// DenyInterface removes an interface from a policy's permitted list.
	DenyInterface(ctx context.Context, name, iface string) error

	// AssignDevice assigns a device (by MAC) to a policy.
	AssignDevice(ctx context.Context, mac, policyName string) error

	// UnassignDevice removes a device's policy assignment.
	UnassignDevice(ctx context.Context, mac string) error

	// ListDevices returns all known LAN devices with their policy assignments.
	ListDevices(ctx context.Context) ([]Device, error)

	// ListGlobalInterfaces returns all router interfaces available for policy routing.
	ListGlobalInterfaces(ctx context.Context) ([]GlobalInterface, error)

	// SetInterfaceUp brings an interface up or down.
	SetInterfaceUp(ctx context.Context, ndmsName string, up bool) error
}
