package subscription

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// ErrInstanceNotFound — ключа нет в источнике записей (ответ 404).
var ErrInstanceNotFound = errors.New("инстанс не найден")

// Deps — зависимости пакета. Формы RecordSource и Mutator предписаны задачей 8
// и здесь не переобъявляются.
type Deps struct {
	Records wdttlink.RecordSource
	Mutator wdttlink.Mutator
	// Fetch — загрузка и разбор документа подписки по её URL. Отдельным швом,
	// а не прямым вызовом wdttlink.DecodeLink: это сетевой запрос, и без шва
	// обновление было бы непроверяемо. Прод-значение подставляет проводка.
	Fetch func(subURL string) (wdttlink.LinkDecodeResult, error)
}

// Service — обновление подписки инстанса.
type Service struct{ deps Deps }

func New(d Deps) *Service { return &Service{deps: d} }

// Refresh перечитывает сохранённый URL подписки и обновляет конфиг клиента
// профилем с ТЕМ ЖЕ адресом сервера. Перенос wdtt.RefreshSubscription
// (subscription_refresh.go) на швы нового мира.
//
// Смена сервера обновлением НЕ делается сознательно (паритет): у клиента на
// адресе висят выделенный порт, связанный AWG-туннель и правила пользователя,
// и молча увести его на другой сервер значило бы поменять выход под ними.
func (s *Service) Refresh(ctx context.Context, key string) (wdttlink.ImportPayload, error) {
	if s.deps.Records == nil {
		return wdttlink.ImportPayload{}, errors.New("источник записей не подключён")
	}
	rec, ok := s.deps.Records.Get(key)
	if !ok {
		return wdttlink.ImportPayload{}, fmt.Errorf("%w: %s", ErrInstanceNotFound, key)
	}
	// Роль сверяется ОБЯЗАТЕЛЬНО: обновлять подписку умеет только
	// wdtt-клиент. У freeturn подписка — поле конфига (-sub), её перечитывает
	// сам процесс, и бэкенду тут делать нечего.
	if rec.Kind != instancestore.KindWdttClient {
		return wdttlink.ImportPayload{}, fmt.Errorf(
			"инстанс %s: обновление подписки есть только у wdtt-клиента; у роли %s подписка — поле конфига, её перечитывает сам процесс",
			key, rec.Kind)
	}
	cfg, err := rec.WdttClientConfig()
	if err != nil {
		return wdttlink.ImportPayload{}, err
	}

	subURL := normalizeSubURL(strings.TrimSpace(rec.Sub))
	if subURL == "" {
		return wdttlink.ImportPayload{}, errors.New("URL подписки не сохранён — импортируйте HTTPS _wdtt.json ещё раз")
	}
	if strings.TrimSpace(cfg.Peer) == "" {
		return wdttlink.ImportPayload{}, errors.New("у клиента не задан peer — нечего сопоставить с подпиской")
	}
	if s.deps.Fetch == nil {
		return wdttlink.ImportPayload{}, errors.New("загрузка подписки не подключена")
	}

	decoded, err := s.deps.Fetch(subURL)
	if err != nil {
		return wdttlink.ImportPayload{}, fmt.Errorf("не удалось загрузить подписку: %w", err)
	}
	profiles := ProfilesFromDecode(decoded)
	if len(profiles) == 0 {
		return wdttlink.ImportPayload{}, errors.New("подписка не содержит профилей")
	}
	profile := FindProfileByPeer(profiles, cfg.Peer)
	if profile == nil {
		return wdttlink.ImportPayload{}, fmt.Errorf(
			"peer %s не найден в подписке (%d профилей) — выберите другой сервер через повторный импорт",
			cfg.Peer, len(profiles),
		)
	}
	if profile.SubURL == "" {
		profile.SubURL = subURL
	}

	if s.deps.Mutator == nil {
		return wdttlink.ImportPayload{}, errors.New("правка инстансов не подключена")
	}
	if err := s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		return applyProfile(r, *profile, subURL)
	}); err != nil {
		return wdttlink.ImportPayload{}, err
	}
	return *profile, nil
}

// applyProfile накладывает профиль на запись ПО МЕСТУ (перенос ApplyImport,
// wdtt/link.go:623-653). Пересборка записи литералом потеряла бы слоты
// адресов, пины интерфейсов и остальные поля.
//
// Пустое поле профиля НЕ затирает сохранённое: подписка отдаёт разный набор
// полей, и отсутствие пароля в документе не значит «пароля больше нет».
// Listen не трогается вовсе: порт выделяет менеджер, и увод его в чужой
// разошёлся бы с аллокатором и связанным AWG-туннелем. Enabled тоже:
// обновление подписки — не запуск.
func applyProfile(r *instancestore.Record, p wdttlink.ImportPayload, subURL string) error {
	if r.WdttClient == nil {
		return fmt.Errorf("инстанс %s: конфиг wdtt-клиента отсутствует", r.Key())
	}
	c := r.WdttClient
	if p.Peer != "" {
		c.Peer = p.Peer
	}
	if p.Password != "" {
		c.Password = p.Password
	}
	if len(p.VKHashes) > 0 {
		c.VKHashes = strings.Join(p.VKHashes, ",")
	}
	if p.Workers > 0 {
		c.Workers = p.Workers
	}
	if p.DeviceID != "" {
		c.DeviceID = p.DeviceID
	}
	if p.ConnMode != "" {
		c.Mode = normalizeConnMode(p.ConnMode)
	}
	r.Sub = subURL
	if name := strings.TrimSpace(p.Name); name != "" {
		r.Name = name
	}
	return nil
}
