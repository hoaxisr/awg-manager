// Package systemtunnel provides read-only access to Keenetic native WireGuard tunnels
// with editable AWG obfuscation (ASC) parameters.
package systemtunnel

import (
	"context"
	"encoding/json"

	ndms "github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// Service defines operations on system WireGuard tunnels.
type Service interface {
	List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error)
	Get(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error)
	GetASCParams(ctx context.Context, name string) (json.RawMessage, error)
	SetASCParams(ctx context.Context, name string, params json.RawMessage) error
}

// ServiceImpl implements Service using the new NDMS CQRS layer.
type ServiceImpl struct {
	queries  *query.Queries
	commands *command.Commands
}

// New creates a new system tunnel service.
func New(queries *query.Queries, commands *command.Commands) *ServiceImpl {
	return &ServiceImpl{queries: queries, commands: commands}
}

func (s *ServiceImpl) List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return s.queries.WGServers.ListSystemTunnels(ctx)
}

func (s *ServiceImpl) Get(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	return s.queries.WGServers.GetSystemTunnel(ctx, name)
}

func (s *ServiceImpl) GetASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	return s.queries.WGServers.GetASCParams(ctx, name, osdetect.AtLeast(5, 1))
}

func (s *ServiceImpl) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	return s.commands.Wireguard.SetASCParams(ctx, name, params)
}
