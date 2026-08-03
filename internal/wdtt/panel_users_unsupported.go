//go:build mips || mipsle

package wdtt

import "fmt"

// wdtt-server не собирается под mips/mipsel: апстримовый pkg/paneldb тянет
// modernc.org/sqlite → modernc.org/libc, где нет этих архитектур. Панельная
// БД на таком роутере существовать не может, поэтому CRUD пользователей
// вырождается в заглушки.

var errPanelUsersUnsupported = fmt.Errorf("wdtt-server недоступен на этой архитектуре")

func syncPanelMainPassword(string, string, []ServerClient) error { return nil }

func loadPanelUsers(configDir, _ string) (PanelUsersStatus, error) {
	return PanelUsersStatus{Users: []PanelUserEntry{}}, nil
}

func addPanelUser(_, _, _, _, _ string) (ServerClient, error) {
	return ServerClient{}, errPanelUsersUnsupported
}

func removePanelUser(_, _, _ string) error { return errPanelUsersUnsupported }

// panel.db на этих арках не существует: восстанавливать нечего и незачем
// шуметь предупреждением на каждой попытке старта.
func restorePanelUsers(_, _ string, _ []ServerClient) ([]ServerClient, error) {
	return nil, nil
}
