package autolistdetect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	pkgName = "awgmautolist"
	// keyFile — API-ключ awg-manager для сателлита. Пишет только awg-manager
	// (при установке и смене ключа), init-скрипт сателлита читает его последним.
	keyFile = "/opt/etc/awgmautolist.key"
)

// packageManager — узкий срез opkg.Client.
type packageManager interface {
	Update() (string, error)
	Install(names []string) (string, error)
}

// keySource — узкий срез storage.SettingsStore.
type keySource interface {
	GetApiKey() string
	IsAuthEnabled() bool
}

var ErrNeedApiKey = errors.New("авторизация включена, а API-ключ не создан: создайте его в настройках")

// SetInstaller подключает установку пакета и источник API-ключа.
func (s *Service) SetInstaller(pkg packageManager, keys keySource) {
	s.pkg = pkg
	s.keys = keys
}

func (s *Service) keyFilePath() string {
	if s.keyPath != "" {
		return s.keyPath
	}
	return keyFile
}

// writeKey кладёт ключ shell-присваиванием: init-скрипт делает `. файл`,
// поэтому значение в одинарных кавычках с экранированием самих кавычек.
func (s *Service) writeKey() error {
	key := strings.ReplaceAll(s.keys.GetApiKey(), "'", `'\''`)
	path := s.keyFilePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("AWGM_API_KEY='"+key+"'\n"), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Install ставит пакет из entware-фида роутера; запускает сателлит postinst
// пакета, здесь только подтверждение, что он поднялся.
func (s *Service) Install(ctx context.Context) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.keys.GetApiKey() == "" && s.keys.IsAuthEnabled() {
		return ErrNeedApiKey
	}
	// Ключ — до установки: postinst стартует сателлит сразу.
	if err := s.writeKey(); err != nil {
		return err
	}
	// Установка = намерение запустить: «выключен» от прошлой установки
	// (файл не принадлежит пакету и переживает opkg remove) снимаем.
	if err := os.Remove(s.enabledStatePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Отказ update не фатален: один недоступный фид роняет его целиком,
	// а наш фид мог обновиться. Причину покажем, если упадёт install.
	_, updErr := s.pkg.Update()
	out, err := s.pkg.Install([]string{pkgName})
	if err == nil && !s.installed() {
		// opkg на неизвестный пакет может выйти с 0.
		err = errors.New("пакет не установлен")
	}
	if err != nil {
		err = fmt.Errorf("opkg install %s: %w: %s", pkgName, err, strings.TrimSpace(out))
		if updErr != nil {
			err = errors.Join(err, fmt.Errorf("opkg update: %w", updErr))
		}
		return err
	}
	confirm := s.confirm
	if confirm == nil {
		confirm = s.confirmState
	}
	return confirm(ctx, true)
}

// ApiKeyChanged переписывает ключ и перезапускает сателлит: ключ он читает
// один раз при старте. Ключ берётся из источника под локом, а не аргументом,
// чтобы при двух быстрых сменах последней записью не оказалась старая.
func (s *Service) ApiKeyChanged() error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if !s.installed() {
		return nil
	}
	if err := s.writeKey(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// restart сам уважает ENABLED=no: выключенный сателлит не поднимется.
	return s.runInit(ctx, "restart")
}
