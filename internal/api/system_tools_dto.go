package api

import (
	sysfiles "github.com/hoaxisr/awg-manager/internal/sys/files"
	"github.com/hoaxisr/awg-manager/internal/sys/opkg"
	sysports "github.com/hoaxisr/awg-manager/internal/sys/ports"
	"github.com/hoaxisr/awg-manager/internal/sys/procmon"
	"github.com/hoaxisr/awg-manager/internal/sys/services"
)

// DTO вкладки «Система». Раньше все ручки были аннотированы как
// map[string]interface{}: спека не несла ни одного типа, поэтому ни
// schemas.gen.ts, ни мок-сервер их не видели, а клиент типизировался руками.

// SystemFileRootDTO — один разрешённый корень файлового менеджера.
type SystemFileRootDTO struct {
	Path     string `json:"path" example:"/opt/etc"`
	Label    string `json:"label" example:"Entware /opt/etc"`
	ReadOnly bool   `json:"readOnly" example:"false"`
}

// SystemFileRootsResponse — GET /system/files/roots.
type SystemFileRootsResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    []SystemFileRootDTO `json:"data"`
}

// SystemFilesListData — содержимое каталога.
type SystemFilesListData struct {
	Path    string           `json:"path" example:"/opt/etc"`
	Entries []sysfiles.Entry `json:"entries"`
}

// SystemFilesListResponse — GET /system/files/list.
type SystemFilesListResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    SystemFilesListData `json:"data"`
}

// SystemFileReadData — содержимое файла с его метаданными.
type SystemFileReadData struct {
	Path    string         `json:"path" example:"/opt/etc/config"`
	Content string         `json:"content"`
	Info    sysfiles.Entry `json:"info"`
}

// SystemFileReadResponse — GET /system/files/read.
type SystemFileReadResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    SystemFileReadData `json:"data"`
}

// SystemFileChecksumData — контрольная сумма файла.
type SystemFileChecksumData struct {
	Path     string         `json:"path" example:"/opt/bin/awg-manager"`
	Checksum string         `json:"checksum" example:"9f86d081884c7d65"`
	Algo     string         `json:"algo" example:"sha256"`
	Info     sysfiles.Entry `json:"info"`
}

// SystemFileChecksumResponse — GET /system/files/checksum.
type SystemFileChecksumResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    SystemFileChecksumData `json:"data"`
}

// SystemUploadData — путь сохранённого файла.
type SystemUploadData struct {
	Path string `json:"path" example:"/tmp/upload.bin"`
}

// SystemUploadResponse — POST /system/files/upload.
type SystemUploadResponse struct {
	Success bool             `json:"success" example:"true"`
	Data    SystemUploadData `json:"data"`
}

// SystemOKResponse — операция без полезной нагрузки (write, mkdir, remove,
// rename, copy, chmod).
type SystemOKResponse struct {
	Success bool `json:"success" example:"true"`
}

// SystemOKFlagData — подтверждение операции отдельным флагом.
type SystemOKFlagData struct {
	OK bool `json:"ok" example:"true"`
}

// SystemOKFlagResponse — POST /system/services/delete.
type SystemOKFlagResponse struct {
	Success bool             `json:"success" example:"true"`
	Data    SystemOKFlagData `json:"data"`
}

// SystemServicesListResponse — GET /system/services/list.
type SystemServicesListResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    []services.Item `json:"data"`
}

// SystemServiceActionData — результат start/stop/restart/status службы.
type SystemServiceActionData struct {
	Output string `json:"output"`
	OK     bool   `json:"ok" example:"true"`
	Error  string `json:"error,omitempty"`
}

// SystemServiceActionResponse — POST /system/services/action.
type SystemServiceActionResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    SystemServiceActionData `json:"data"`
}

// SystemServiceScriptData — тело init-скрипта.
type SystemServiceScriptData struct {
	Script  string `json:"script" example:"/opt/etc/init.d/S90myservice"`
	Content string `json:"content"`
}

// SystemServiceScriptResponse — GET /system/services/get.
type SystemServiceScriptResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    SystemServiceScriptData `json:"data"`
}

// SystemServiceSavedData — путь сохранённого init-скрипта.
type SystemServiceSavedData struct {
	OK     bool   `json:"ok" example:"true"`
	Script string `json:"script" example:"/opt/etc/init.d/S90myservice"`
}

// SystemServiceSavedResponse — POST /system/services/save.
type SystemServiceSavedResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    SystemServiceSavedData `json:"data"`
}

// SystemServiceToggleEnableData — результат переключения автозапуска (Sxx <-> Kxx).
type SystemServiceToggleEnableData struct {
	OK        bool   `json:"ok" example:"true"`
	NewScript string `json:"newScript" example:"/opt/etc/init.d/S90myservice"`
	Enabled   bool   `json:"enabled" example:"true"`
}

// SystemServiceToggleEnableResponse — POST /system/services/toggle-enable.
type SystemServiceToggleEnableResponse struct {
	Success bool                          `json:"success" example:"true"`
	Data    SystemServiceToggleEnableData `json:"data"`
}

// SystemOpkgPackagesResponse — списки пакетов opkg.
type SystemOpkgPackagesResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    []opkg.Package `json:"data"`
}

// SystemOutputData — сырой вывод команды opkg.
type SystemOutputData struct {
	Output string `json:"output"`
}

// SystemOutputResponse — opkg update/upgrade/install/remove.
type SystemOutputResponse struct {
	Success bool             `json:"success" example:"true"`
	Data    SystemOutputData `json:"data"`
}

// SystemOpkgAvailableData — страница списка доступных пакетов.
type SystemOpkgAvailableData struct {
	Items  []opkg.Package `json:"items"`
	Total  int            `json:"total" example:"1800"`
	Offset int            `json:"offset" example:"0"`
	Limit  int            `json:"limit" example:"100"`
}

// SystemOpkgAvailableResponse — GET /system/opkg/available.
type SystemOpkgAvailableResponse struct {
	Success bool                    `json:"success" example:"true"`
	Data    SystemOpkgAvailableData `json:"data"`
}

// SystemPortsListResponse — GET /system/ports/list.
type SystemPortsListResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    []sysports.Binding `json:"data"`
}

// SystemPortInspectData — кто занимает порт.
type SystemPortInspectData struct {
	Port     int                `json:"port" example:"8080"`
	Proto    string             `json:"proto" example:"tcp"`
	Bindings []sysports.Binding `json:"bindings"`
	Occupied bool               `json:"occupied" example:"true"`
}

// SystemPortInspectResponse — GET /system/ports/inspect.
type SystemPortInspectResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    SystemPortInspectData `json:"data"`
}

// SystemKillData — итог отправки сигнала процессу.
type SystemKillData struct {
	PID    int    `json:"pid" example:"1234"`
	Signal string `json:"signal" example:"SIGTERM"`
	OK     bool   `json:"ok" example:"true"`
}

// SystemKillResponse — POST /system/ports/kill и POST /system/proc/kill.
type SystemKillResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    SystemKillData `json:"data"`
}

// SystemScriptStatusResponse — GET /system/files/script-status.
type SystemScriptStatusResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    ScriptStatusDTO `json:"data"`
}

// SystemScriptActionData — итог start/stop/restart/run скрипта.
type SystemScriptActionData struct {
	OK      bool   `json:"ok" example:"true"`
	Output  string `json:"output"`
	Running bool   `json:"running" example:"true"`
	PIDs    []int  `json:"pids"`
	Error   string `json:"error,omitempty"`
}

// SystemScriptActionResponse — POST /system/files/script-action.
type SystemScriptActionResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    SystemScriptActionData `json:"data"`
}

// SystemProcSnapshotResponse — GET /system/proc/top.
type SystemProcSnapshotResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    procmon.SystemSnapshot `json:"data"`
}
