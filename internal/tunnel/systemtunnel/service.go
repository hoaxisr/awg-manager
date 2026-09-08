// Package systemtunnel provides read-only access to Keenetic native WireGuard tunnels
// with editable AWG obfuscation (ASC) parameters.
package systemtunnel

import (
	"context"
	"encoding/json"

	"github.com/hoaxisr/awg-manager/internal/logging"
	ndms "github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// Service defines operations on system WireGuard tunnels.
type Service interface {
	List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error)
	Get(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error)
	GetASCParams(ctx context.Context, name string) (json.RawMessage, error)
	SetASCParams(ctx context.Context, name string, params json.RawMessage) error
}

// ServerMarker сообщает, перенят ли интерфейс как WG-сервер (список
// ServerInterfaces в настройках). Реализуется *storage.SettingsStore;
// узкий интерфейс — чтобы пакет не тянул стор целиком.
type ServerMarker interface {
	IsServerInterface(id string) bool
}

// ServiceImpl implements Service using the new NDMS CQRS layer.
type ServiceImpl struct {
	queries  *query.Queries
	commands *command.Commands
	servers  ServerMarker
	appLog   *logging.ScopedLogger
}

// New creates a new system tunnel service. servers может быть nil — тогда
// сервером считается только встроенный (по описанию интерфейса).
func New(queries *query.Queries, commands *command.Commands, servers ServerMarker, appLog *logging.ScopedLogger) *ServiceImpl {
	return &ServiceImpl{queries: queries, commands: commands, servers: servers, appLog: appLog}
}

// isServerInterface — предикат «интерфейс это WG-сервер, а не туннель-клиент»
// в том же виде, в каком его понимает остальной проект: встроенный сервер по
// описанию либо интерфейс, перенятый пользователем. Слушающий порт признаком
// НЕ является: runtime listen-port есть и у поднятого туннеля-клиента.
func (s *ServiceImpl) isServerInterface(ctx context.Context, name string) bool {
	if s.servers != nil && s.servers.IsServerInterface(name) {
		return true
	}
	t, err := s.queries.WGServers.GetSystemTunnel(ctx, name)
	return err == nil && t != nil && t.Description == ndms.BuiltInVPNServerDescription
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
	// У интерфейса-сервера сигнатура — свойство каждого его пира, а не
	// интерфейса: форма ASC её не задаёт, и до NDMS ключи не доходят.
	if s.isServerInterface(ctx, name) {
		params = stripASCSignatures(params)
	}
	params, note := splitASCSignatures(params)
	if note != "" {
		s.appLog.Info("set-asc", name, signature.RewriteLogMessage(note))
	}
	return s.commands.Wireguard.SetASCParams(ctx, name, params)
}
