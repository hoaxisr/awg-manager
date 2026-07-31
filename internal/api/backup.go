package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
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
}

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
// GET /api/system/backup/export
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
// POST /api/system/backup/import (multipart field "file")
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

	quiesced := false
	if h.quiesce != nil {
		if err := h.quiesce(r.Context()); err != nil {
			if h.log != nil {
				h.log.Warn("import", "", "quiesce: "+err.Error())
			}
			response.Error(w, "не удалось остановить службы перед восстановлением: "+err.Error(), "BACKUP_QUIESCE_FAILED")
			return
		}
		quiesced = true
	}
	if err := backup.Restore(h.dataDir, file); err != nil {
		if quiesced && h.resume != nil {
			h.resume(r.Context())
		}
		if h.log != nil {
			h.log.Warn("import", "", err.Error())
		}
		response.Error(w, err.Error(), "BACKUP_IMPORT_FAILED")
		return
	}
	if h.log != nil {
		h.log.Info("import", "", "full data-dir restore applied, scheduling restart")
	}
	if h.restart != nil {
		h.restart()
	}
	response.Success(w, map[string]string{
		"message": "Резервная копия восстановлена. AWG Manager перезапускается…",
	})
}
