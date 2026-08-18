package wdttclient

import (
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// rebuildForTest пересоздаёт ресурсы после подмены зависимостей в тесте.
// Пути обвязки в тестах не важны — берётся валидная пара для инстанса.
func (r *Role) rebuildForTest() {
	sock, _ := control.SocketPath(roles.RuntimeDir, roles.ImplWtClient, roles.RoleClient, r.deps.Instance)
	logPath, _ := control.LogPath(roles.RuntimeDir, roles.ImplWtClient, roles.RoleClient, r.deps.Instance)
	r.build(sock, logPath)
}
