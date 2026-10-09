package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
)

const (
	backupUploadMaxBytes = 512 << 20
	// multipartMemoryBytes — сколько ParseMultipartForm держит в куче,
	// остальное спиливается во временный файл.
	multipartMemoryBytes = 4 << 20
)

// BackupQuiescer stops awg-manager child processes before backup/restore.
type BackupQuiescer func(ctx context.Context) error

// BackupResumer restarts child processes after export quiesce (not used on import).
type BackupResumer func(ctx context.Context)

// BackupHandler serves full awg-manager data-dir export/import.
type BackupHandler struct {
	dataDir    string
	appVersion string
	quiesce    BackupQuiescer
	resume     BackupResumer
	restart    func()
	log        *logging.ScopedLogger
	// upgrading — идёт установка обновления; восстановление тогда
	// запрещено: opkg с postinst и Restore боролись бы за одни данные.
	upgrading func() bool
	bus       *events.Bus
}

// SetUpgradeGuard подключает проверку «идёт обновление»; nil — без проверки (тесты).
func (h *BackupHandler) SetUpgradeGuard(upgrading func() bool) { h.upgrading = upgrading }

// SetEventBus подключает SSE-шину; nil — без событий (тесты).
func (h *BackupHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// NewBackupHandler creates a backup handler for dataDir (e.g. /opt/etc/awg-manager).
func NewBackupHandler(dataDir, appVersion string, quiesce BackupQuiescer, resume BackupResumer, restart func(), appLogger logging.AppLogger) *BackupHandler {
	return &BackupHandler{
		dataDir:    strings.TrimSpace(dataDir),
		appVersion: strings.TrimSpace(appVersion),
		quiesce:    quiesce,
		resume:     resume,
		restart:    restart,
		log:        logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubSettings),
	}
}

// Export streams a gzip tar of the awg-manager data directory.
//
//	@Summary		Export full data-dir backup
//	@Description	Останавливает дочерние процессы, стримит tar.gz каталога данных и поднимает их обратно.
//	@Tags			system
//	@Produce		application/gzip
//	@Security		CookieAuth
//	@Success		200	{file}		binary
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/backup/export [get]
func (h *BackupHandler) Export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ctx := r.Context()
	quiesced := false
	if h.quiesce != nil {
		if err := h.quiesce(ctx); err != nil {
			if h.log != nil {
				h.log.Warn("export", "", "quiesce: "+err.Error())
			}
			response.Error(w, "не удалось остановить службы перед резервным копированием: "+err.Error(), "BACKUP_QUIESCE_FAILED")
			return
		}
		quiesced = true
	}

	// Стримим прямо в ответ: каталог данных с историей трафика и логами тянет
	// на десятки мегабайт, а роутеры — mipsel со 128-256 МБ. Промежуточный
	// bytes.Buffer держал бы весь архив в куче.
	if err := backup.CheckDataDir(h.dataDir); err != nil {
		if quiesced && h.resume != nil {
			h.resume(ctx)
		}
		if h.log != nil {
			h.log.Warn("export", "", err.Error())
		}
		response.Error(w, err.Error(), "BACKUP_EXPORT_FAILED")
		return
	}
	filename := backup.Filename(time.Now().UTC())
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	err := backup.Export(h.dataDir, h.appVersion, w)
	if quiesced && h.resume != nil {
		h.resume(ctx)
	}
	if h.log == nil {
		return
	}
	if err != nil {
		// Заголовки уже ушли — отдать JSON-ошибку нельзя; клиент увидит
		// битый gzip, а причина остаётся в журнале.
		h.log.Warn("export", "", "архив отдан не полностью: "+err.Error())
		return
	}
	h.log.Info("export", "", "full data-dir backup downloaded")
}

// Import accepts a backup archive and replaces the data directory, then schedules restart.
//
//	@Summary		Restore full data-dir backup
//	@Description	Принимает tar.gz из поля file, заменяет каталог данных и планирует перезапуск демона.
//	@Tags			system
//	@Accept			mpfd
//	@Produce		json
//	@Security		CookieAuth
//	@Param			file	formData	file	true	"Архив .tar.gz, снятый экспортом"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope	"Идёт установка обновления"
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/system/backup/import [post]
func (h *BackupHandler) Import(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	// MaxBytesReader режет тело ДО разбора; ParseMultipartForm получает
	// maxMemory (не лимит запроса!) — с 512 МБ загрузка целиком осела бы в
	// куче вместо временного файла.
	r.Body = http.MaxBytesReader(w, r.Body, backupUploadMaxBytes)
	if err := r.ParseMultipartForm(multipartMemoryBytes); err != nil {
		response.Error(w, "не удалось прочитать загрузку: "+err.Error(), "BACKUP_IMPORT_BAD_REQUEST")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	file, header, err := r.FormFile("file")
	if err != nil {
		response.Error(w, "ожидается файл в поле file", "BACKUP_IMPORT_NO_FILE")
		return
	}
	defer file.Close()

	name := strings.ToLower(header.Filename)
	if name != "" && !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tgz") && !strings.HasSuffix(name, ".gz") {
		response.Error(w, "ожидается архив .tar.gz", "BACKUP_IMPORT_BAD_FORMAT")
		return
	}

	h.restoreFrom(w, r, file, "import")
}

// restoreFrom — общий путь восстановления из загрузки и из снимка: остановить
// дочерние процессы, заменить данные, при отказе поднять процессы обратно, при
// успехе запланировать перезапуск.
//
// SSE-подсказки здесь нет: успех кончается перезапуском демона, а на
// переподключении SSE фронт перечитывает все сторы (invalidateAll).
func (h *BackupHandler) restoreFrom(w http.ResponseWriter, r *http.Request, src io.Reader, op string) {
	if h.upgrading != nil && h.upgrading() {
		response.ErrorWithStatus(w, http.StatusConflict,
			"идёт установка обновления — восстановление будет доступно после перезапуска AWG Manager", "BACKUP_UPGRADE_IN_PROGRESS")
		return
	}
	quiesced := false
	if h.quiesce != nil {
		if err := h.quiesce(r.Context()); err != nil {
			if h.log != nil {
				h.log.Warn(op, "", "quiesce: "+err.Error())
			}
			response.Error(w, "не удалось остановить службы перед восстановлением: "+err.Error(), "BACKUP_QUIESCE_FAILED")
			return
		}
		quiesced = true
	}
	if err := backup.Restore(h.dataDir, src); err != nil {
		if quiesced && h.resume != nil {
			h.resume(r.Context())
		}
		if h.log != nil {
			h.log.Warn(op, "", err.Error())
		}
		response.Error(w, err.Error(), "BACKUP_IMPORT_FAILED")
		return
	}
	if h.log != nil {
		h.log.Info(op, "", "full data-dir restore applied, scheduling restart")
	}
	if h.restart != nil {
		h.restart()
	}
	response.Success(w, map[string]string{
		"message": "Резервная копия восстановлена. AWG Manager перезапускается…",
	})
}

// UpdateSnapshotsData — снимки, новые первыми, и сколько их хранится.
type UpdateSnapshotsData struct {
	Snapshots []backup.Snapshot `json:"snapshots"`
	Keep      int               `json:"keep"`
	// TTLDays — срок жизни снимка; самый новый живёт до следующего обновления.
	TTLDays int `json:"ttlDays"`
}

// ListSnapshots returns the settings snapshots taken before updates.
//
//	@Summary		List pre-update snapshots
//	@Description	Снимки каталога данных, которые AWG Manager сохраняет перед установкой обновления; новые первыми.
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=UpdateSnapshotsData}
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/system/backup/snapshots [get]
func (h *BackupHandler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	h.writeSnapshots(w)
}

func (h *BackupHandler) writeSnapshots(w http.ResponseWriter) {
	list, err := backup.ListSnapshots(h.dataDir)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, UpdateSnapshotsData{
		Snapshots: response.MustNotNil(list),
		Keep:      backup.SnapshotKeep,
		TTLDays:   int(backup.SnapshotTTL / (24 * time.Hour)),
	})
}

// snapshotError отвечает на отказ при работе со снимком: неизвестный id — 404.
func snapshotError(w http.ResponseWriter, err error) {
	if errors.Is(err, backup.ErrSnapshotNotFound) {
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "SNAPSHOT_NOT_FOUND")
		return
	}
	response.InternalError(w, err.Error())
}

// DownloadSnapshot streams one pre-update snapshot.
//
//	@Summary		Download pre-update snapshot
//	@Description	Отдаёт снимок как обычный архив резервной копии.
//	@Tags			system
//	@Produce		application/gzip
//	@Security		CookieAuth
//	@Param			id	query		string	true	"Snapshot id"
//	@Success		200	{file}		binary
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/system/backup/snapshots/download [get]
func (h *BackupHandler) DownloadSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	f, err := backup.OpenSnapshot(h.dataDir, id)
	if err != nil {
		snapshotError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "awg-manager-"+id))
	if _, err := io.Copy(w, f); err != nil && h.log != nil {
		h.log.Warn("snapshot-download", id, "снимок отдан не полностью: "+err.Error())
	}
}

// RestoreSnapshot restores the data directory from a pre-update snapshot.
//
//	@Summary		Restore pre-update snapshot
//	@Description	Восстанавливает каталог данных из снимка так же, как из загруженного архива, и планирует перезапуск демона.
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query		string	true	"Snapshot id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"Идёт установка обновления"
//	@Router			/system/backup/snapshots/restore [post]
func (h *BackupHandler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	f, err := backup.OpenSnapshot(h.dataDir, id)
	if err != nil {
		snapshotError(w, err)
		return
	}
	defer f.Close()
	h.restoreFrom(w, r, f, "snapshot-restore")
}

// DeleteSnapshot removes one pre-update snapshot.
//
//	@Summary		Delete pre-update snapshot
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query		string	true	"Snapshot id"
//	@Success		200	{object}	APIEnvelope{data=UpdateSnapshotsData}
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/system/backup/snapshots/delete [post]
func (h *BackupHandler) DeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if err := backup.DeleteSnapshot(h.dataDir, id); err != nil {
		snapshotError(w, err)
		return
	}
	if h.log != nil {
		h.log.Info("snapshot-delete", id, "снимок перед обновлением удалён")
	}
	h.bus.PublishInvalidated(events.ResourceUpdateSnapshots, "deleted")
	h.writeSnapshots(w)
}
