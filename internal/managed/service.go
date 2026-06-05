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
//
// Every per-server method takes the server id (InterfaceName, e.g. "Wireguard0")
// as the first argument after ctx. List() returns every configured server.
type ManagedServerService interface {
	// Server CRUD
	Create(ctx context.Context, req CreateServerRequest) (*storage.ManagedServer, error)
	List() []storage.ManagedServer
	Get(id string) (*storage.ManagedServer, error)
	Update(ctx context.Context, id string, req UpdateServerRequest) error
	Delete(ctx context.Context, id string) error

	// SuggestAddress returns a free private /24 (host .1) for the
	// "Create server" UI, scanning live router interfaces to avoid
	// any subnet that is already configured.
	SuggestAddress(ctx context.Context) (address string, mask string, err error)

	// Enable/disable
	SetEnabled(ctx context.Context, id string, enabled bool) error
	RestartOrStart(ctx context.Context, id string) error

	// NAT
	SetNAT(ctx context.Context, id string, enabled bool) error
	SetNATMode(ctx context.Context, id, mode string) error

	// Policy
	SetPolicy(ctx context.Context, id, policy string) error
	ListPolicies(ctx context.Context) ([]PolicyOption, error)

	// Peer management
	AddPeer(ctx context.Context, id string, req AddPeerRequest) (*storage.ManagedPeer, error)
	UpdatePeer(ctx context.Context, id, pubkey string, req UpdatePeerRequest) error
	DeletePeer(ctx context.Context, id, pubkey string) error
	TogglePeer(ctx context.Context, id, pubkey string, enabled bool) error

	// Config generation
	GenerateConf(ctx context.Context, id, pubkey string) (string, error)

	// Runtime stats
	GetStats(ctx context.Context, id string) (*ManagedServerStats, error)

	// ASC params
	GetASCParams(ctx context.Context, id string) (json.RawMessage, error)
	SetASCParams(ctx context.Context, id string, params json.RawMessage) error

	// InvalidateCache invalidates the per-interface WGServerStore cache
	// entry. Called by API mutation handlers immediately after a
	// successful write so the next read (typically the SSE-driven
	// frontend re-fetch) sees fresh NDMS state instead of cached
	// pre-mutation values.
	InvalidateCache(id string)
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
	// wgRun is the wg-tools execution seam. Production uses realWgRunner;
	// tests inject a stub to avoid forking real binaries.
	wgRun wgRunner
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
		wgRun:     realWgRunner,
	}
}

// InvalidateCache flushes the cached NDMS state for the given server id
// (interface name). Safe to call concurrently. No-op when WGServers is
// nil (test fakes) — see rci.go for the same defensive check used by
// InvalidateAll.
func (s *Service) InvalidateCache(id string) {
	if s.queries == nil || s.queries.WGServers == nil {
		return
	}
	s.queries.WGServers.Invalidate(id)
}

// resolveKernelName maps an NDMS interface name (e.g. "Wireguard0") to its
// kernel-side device name (e.g. "nwg0") via the interface store cache.
// Returns "" if the queries layer is unavailable or the name cannot be
// resolved — callers treat empty as "skip the wg-tools call".
func (s *Service) resolveKernelName(ctx context.Context, ndmsName string) string {
	if s.queries == nil || s.queries.Interfaces == nil {
		return ""
	}
	return s.queries.Interfaces.ResolveSystemName(ctx, ndmsName)
}
