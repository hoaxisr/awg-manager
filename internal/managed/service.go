package managed

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ManagedServerService defines the interface for managed WireGuard server operations.
type ManagedServerService interface {
	// Server CRUD
	Create(ctx context.Context, req CreateServerRequest) (*storage.ManagedServer, error)
	Get() *storage.ManagedServer
	Update(ctx context.Context, req UpdateServerRequest) error
	Delete(ctx context.Context) error
	GetInterfaceName() string

	// SuggestAddress returns a free private /24 (host .1) for the
	// "Create server" UI, scanning live router interfaces to avoid
	// any subnet that is already configured.
	SuggestAddress(ctx context.Context) (address string, mask string, err error)

	// Enable/disable
	SetEnabled(ctx context.Context, enabled bool) error

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

// rciPoster is the minimal POST surface managed needs from the NDMS transport.
// *transport.Client satisfies it.
type rciPoster interface {
	Post(ctx context.Context, payload any) (json.RawMessage, error)
}

var _ rciPoster = (*transport.Client)(nil)

// Service manages the user-created WireGuard server.
type Service struct {
	transport rciPoster
	saveCoord *command.SaveCoordinator
	queries   *query.Queries
	commands  *command.Commands
	settings  *storage.SettingsStore
	log       *slog.Logger
	appLog    *logging.ScopedLogger
}

// New creates a new managed server service.
func New(
	transport rciPoster,
	saveCoord *command.SaveCoordinator,
	queries *query.Queries,
	commands *command.Commands,
	settings *storage.SettingsStore,
	log *slog.Logger,
	appLogger logging.AppLogger,
) *Service {
	return &Service{
		transport: transport,
		saveCoord: saveCoord,
		queries:   queries,
		commands:  commands,
		settings:  settings,
		log:       log,
		appLog:    logging.NewScopedLogger(appLogger, logging.GroupServer, logging.SubManaged),
	}
}
