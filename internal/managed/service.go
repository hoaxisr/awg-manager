package managed

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// ManagedServerService defines the interface for managed WireGuard server operations.
type ManagedServerService interface {
	// Server CRUD
	Create(ctx context.Context, req CreateServerRequest) (*storage.ManagedServer, error)
	Get() *storage.ManagedServer
	Update(ctx context.Context, req UpdateServerRequest) error
	Delete(ctx context.Context) error
	GetInterfaceName() string

	// NAT
	SetNAT(ctx context.Context, enabled bool) error

	// Peer management
	AddPeer(ctx context.Context, req AddPeerRequest) (*storage.ManagedPeer, error)
	UpdatePeer(ctx context.Context, pubkey string, req UpdatePeerRequest) error
	DeletePeer(ctx context.Context, pubkey string) error
	TogglePeer(ctx context.Context, pubkey string, enabled bool) error

	// Config generation
	GenerateConf(ctx context.Context, pubkey string) (string, error)

	// Runtime stats
	GetStats(ctx context.Context) (*ManagedServerStats, error)

	// ASC params
	GetASCParams(ctx context.Context) (json.RawMessage, error)
	SetASCParams(ctx context.Context, params json.RawMessage) error
}

// Service manages the user-created WireGuard server.
type Service struct {
	ndms     ndms.Client
	settings *storage.SettingsStore
	log      *slog.Logger
	appLog   *logging.ScopedLogger
}

// New creates a new managed server service.
func New(ndmsClient ndms.Client, settings *storage.SettingsStore, log *slog.Logger, appLogger logging.AppLogger) *Service {
	return &Service{
		ndms:     ndmsClient,
		settings: settings,
		log:      log,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupServer, logging.SubManaged),
	}
}
