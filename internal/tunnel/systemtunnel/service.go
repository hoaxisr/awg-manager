// Package systemtunnel provides read-only access to Keenetic native WireGuard tunnels
// with editable AWG obfuscation (ASC) parameters.
package systemtunnel

import (
	"context"
	"encoding/json"

	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// Service defines operations on system WireGuard tunnels.
type Service interface {
	List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error)
	Get(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error)
	GetASCParams(ctx context.Context, name string) (json.RawMessage, error)
	SetASCParams(ctx context.Context, name string, params json.RawMessage) error
}

// ServiceImpl implements Service using NDMS client.
type ServiceImpl struct {
	ndms ndms.Client
}

// New creates a new system tunnel service.
func New(ndmsClient ndms.Client) *ServiceImpl {
	return &ServiceImpl{ndms: ndmsClient}
}

func (s *ServiceImpl) List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return s.ndms.ListSystemWireguardTunnels(ctx)
}

func (s *ServiceImpl) Get(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	return s.ndms.GetSystemWireguardTunnel(ctx, name)
}

func (s *ServiceImpl) GetASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	return s.ndms.GetASCParams(ctx, name)
}

func (s *ServiceImpl) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	return s.ndms.SetASCParams(ctx, name, params)
}
